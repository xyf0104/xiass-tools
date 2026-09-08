package claudeconfig

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	// Claude Code's own gateway model-discovery contract is deliberately much
	// tighter than an inference request: discovery is best-effort startup
	// traffic and must finish within three seconds. Keeping this helper on the
	// same contract prevents a false "model directory works" result when the
	// actual Claude Code picker will time out.
	gatewayDiscoveryRequestTimeout = 3 * time.Second
	// A user-initiated one-token Messages check may take longer than discovery,
	// but it is still bounded and never retried because it can be billable.
	gatewayMessagesRequestTimeout = 15 * time.Second
	maxGatewayResponseBytes       = 1 << 20
	anthropicAPIVersion           = "2023-06-01"
)

var (
	// ErrGatewayHelperCheckUnsupported deliberately prevents XIASS Tools from
	// executing a user-provided apiKeyHelper command. Claude Code itself owns
	// that command's execution environment and credential lifecycle.
	ErrGatewayHelperCheckUnsupported = errors.New("Claude apiKeyHelper cannot be executed for a XIASS Tools gateway check")
	errGatewayInvalidResponse        = errors.New("Claude gateway returned an invalid response")
	errGatewayResponseTooLarge       = errors.New("Claude gateway response exceeded the safe size limit")
)

// GatewayRequest is an in-memory, caller-supplied request. Credential is
// intentionally not serializable and must only be populated for an explicitly
// initiated discovery or connection test; it is never read from settings.json.
type GatewayRequest struct {
	BaseURL        string
	CredentialMode CredentialMode
	Credential     string
	Model          string
}

// GatewayModel is the renderer-safe projection of an officially discoverable
// gateway model. It contains no URL, header, token, request, or raw response.
type GatewayModel struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName,omitempty"`
}

// GatewayModelDiscoveryResult records a completed /v1/models request without
// retaining credentials or response content.
type GatewayModelDiscoveryResult struct {
	Models     []GatewayModel `json:"models"`
	HTTPStatus int            `json:"httpStatus"`
	DurationMS int64          `json:"durationMs"`
}

// GatewayConnectionTestResult records a completed minimal Messages request.
// The response itself is intentionally discarded so model output is not
// needlessly retained or sent over the Wails bridge.
type GatewayConnectionTestResult struct {
	HTTPStatus int   `json:"httpStatus"`
	DurationMS int64 `json:"durationMs"`
}

type normalizedGatewayRequest struct {
	baseURL        string
	credentialMode CredentialMode
	credential     string
	model          string
}

// gatewayModelWire accepts the documented display_name response field and the
// camel-case spelling used by a few compatible gateways. The public bridge
// projection remains GatewayModel with a stable displayName JSON field.
type gatewayModelWire struct {
	ID                string `json:"id"`
	DisplayName       string `json:"display_name"`
	LegacyDisplayName string `json:"displayName"`
}

// DiscoverGatewayModels performs one direct, authenticated model-discovery
// request against the caller-supplied gateway. It does not modify settings,
// cache results, retry, or inspect saved credentials.
func DiscoverGatewayModels(ctx context.Context, input GatewayRequest) (GatewayModelDiscoveryResult, error) {
	return discoverGatewayModels(ctx, input, newGatewayHTTPClient(gatewayDiscoveryRequestTimeout))
}

func discoverGatewayModels(ctx context.Context, input GatewayRequest, client *http.Client) (GatewayModelDiscoveryResult, error) {
	requestConfig, err := normalizeGatewayRequest(input, false)
	if err != nil {
		return GatewayModelDiscoveryResult{Models: []GatewayModel{}}, err
	}
	endpoint, err := gatewayDiscoveryEndpoint(requestConfig.baseURL)
	if err != nil {
		return GatewayModelDiscoveryResult{Models: []GatewayModel{}}, err
	}
	requestContext, cancel := context.WithTimeout(ctx, gatewayDiscoveryRequestTimeout)
	defer cancel()
	request, err := newGatewayHTTPRequest(requestContext, http.MethodGet, endpoint, requestConfig, nil)
	if err != nil {
		return GatewayModelDiscoveryResult{Models: []GatewayModel{}}, err
	}
	startedAt := time.Now()
	response, err := client.Do(request)
	result := GatewayModelDiscoveryResult{Models: []GatewayModel{}, DurationMS: elapsedMilliseconds(startedAt)}
	if err != nil {
		return result, errors.New("Claude gateway request failed")
	}
	defer response.Body.Close()
	result.HTTPStatus = response.StatusCode
	body, err := readGatewayResponse(response.Body)
	if err != nil {
		return result, err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return result, &GatewayHTTPError{StatusCode: response.StatusCode}
	}

	var envelope struct {
		Data   []gatewayModelWire `json:"data"`
		Models []gatewayModelWire `json:"models"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return result, errGatewayInvalidResponse
	}
	candidates := envelope.Data
	if len(candidates) == 0 {
		candidates = envelope.Models
	}
	result.Models = normalizeGatewayModels(candidates)
	return result, nil
}

// TestGatewayMessages sends one intentionally tiny Anthropic Messages
// request. It never retries because a retry would be a second billable model
// invocation, and it never returns the model's response content.
func TestGatewayMessages(ctx context.Context, input GatewayRequest) (GatewayConnectionTestResult, error) {
	return testGatewayMessages(ctx, input, newGatewayHTTPClient(gatewayMessagesRequestTimeout))
}

func testGatewayMessages(ctx context.Context, input GatewayRequest, client *http.Client) (GatewayConnectionTestResult, error) {
	requestConfig, err := normalizeGatewayRequest(input, true)
	if err != nil {
		return GatewayConnectionTestResult{}, err
	}
	endpoint, err := gatewayMessagesEndpoint(requestConfig.baseURL)
	if err != nil {
		return GatewayConnectionTestResult{}, err
	}
	payload, err := json.Marshal(struct {
		Model     string `json:"model"`
		MaxTokens int    `json:"max_tokens"`
		Messages  []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}{
		Model:     requestConfig.model,
		MaxTokens: 1,
		Messages: []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}{{Role: "user", Content: "Reply with OK."}},
	})
	if err != nil {
		return GatewayConnectionTestResult{}, errors.New("could not prepare Claude gateway test")
	}
	requestContext, cancel := context.WithTimeout(ctx, gatewayMessagesRequestTimeout)
	defer cancel()
	request, err := newGatewayHTTPRequest(requestContext, http.MethodPost, endpoint, requestConfig, payload)
	if err != nil {
		return GatewayConnectionTestResult{}, err
	}
	startedAt := time.Now()
	response, err := client.Do(request)
	result := GatewayConnectionTestResult{DurationMS: elapsedMilliseconds(startedAt)}
	if err != nil {
		return result, errors.New("Claude gateway request failed")
	}
	defer response.Body.Close()
	result.HTTPStatus = response.StatusCode
	body, err := readGatewayResponse(response.Body)
	if err != nil {
		return result, err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return result, &GatewayHTTPError{StatusCode: response.StatusCode}
	}
	if err := validateGatewayMessageResponse(body); err != nil {
		return result, err
	}
	return result, nil
}

// GatewayHTTPError does not include server response text because gateways can
// accidentally echo credentials, request metadata, or internal URLs in error
// bodies. The status code is sufficient for a user to distinguish a rejected
// credential from an unavailable endpoint.
type GatewayHTTPError struct {
	StatusCode int
}

func (e *GatewayHTTPError) Error() string {
	if e == nil {
		return "Claude gateway request failed"
	}
	return fmt.Sprintf("Claude gateway request failed with HTTP %d", e.StatusCode)
}

func normalizeGatewayRequest(input GatewayRequest, requireModel bool) (normalizedGatewayRequest, error) {
	baseURL, err := NormalizeBaseURL(input.BaseURL)
	if err != nil {
		return normalizedGatewayRequest{}, err
	}
	mode := input.CredentialMode
	if mode == "" {
		mode = CredentialModeAuthToken
	}
	if mode == CredentialModeAPIKeyHelper {
		return normalizedGatewayRequest{}, ErrGatewayHelperCheckUnsupported
	}
	normalizedMode, credential, _, err := normalizeCredential(ApplyConfig{CredentialMode: mode, Credential: input.Credential})
	if err != nil {
		return normalizedGatewayRequest{}, err
	}
	result := normalizedGatewayRequest{baseURL: baseURL, credentialMode: normalizedMode, credential: credential}
	if requireModel {
		if input.Model == "" || len(input.Model) > maxModelBytes || !modelPattern.MatchString(input.Model) {
			return normalizedGatewayRequest{}, errInvalidModel
		}
		result.model = input.Model
	}
	return result, nil
}

func newGatewayHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func newGatewayHTTPRequest(ctx context.Context, method, endpoint string, requestConfig normalizedGatewayRequest, payload []byte) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, errors.New("could not prepare Claude gateway request")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Anthropic-Version", anthropicAPIVersion)
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/json")
	}
	switch requestConfig.credentialMode {
	case CredentialModeAuthToken:
		request.Header.Set("Authorization", "Bearer "+requestConfig.credential)
	case CredentialModeAPIKey:
		request.Header.Set("X-Api-Key", requestConfig.credential)
	default:
		return nil, errors.New("could not prepare Claude gateway authentication")
	}
	return request, nil
}

func gatewayEndpoint(baseURL, operation string) (string, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed == nil {
		return "", errInvalidBaseURL
	}
	path := strings.TrimRight(parsed.Path, "/")
	if strings.HasSuffix(strings.ToLower(path), "/v1") {
		parsed.Path = path + "/" + operation
	} else {
		parsed.Path = path + "/v1/" + operation
	}
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	return parsed.String(), nil
}

// gatewayDiscoveryEndpoint mirrors Claude Code's documented discovery
// request exactly. The explicit limit avoids a gateway's short default page
// producing a helper-side model list that differs from Claude Code's picker.
func gatewayDiscoveryEndpoint(baseURL string) (string, error) {
	endpoint, err := gatewayEndpoint(baseURL, "models")
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed == nil {
		return "", errInvalidBaseURL
	}
	query := url.Values{}
	query.Set("limit", "1000")
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

// gatewayMessagesEndpoint mirrors the standard Anthropic-format inference
// request Claude Code sends through ANTHROPIC_BASE_URL. The test deliberately
// follows the same query shape so it exercises the same gateway routing path
// instead of a convenient-but-different endpoint.
func gatewayMessagesEndpoint(baseURL string) (string, error) {
	endpoint, err := gatewayEndpoint(baseURL, "messages")
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed == nil {
		return "", errInvalidBaseURL
	}
	query := url.Values{}
	query.Set("beta", "true")
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func readGatewayResponse(reader io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maxGatewayResponseBytes+1))
	if err != nil {
		return nil, errors.New("could not read Claude gateway response")
	}
	if len(data) > maxGatewayResponseBytes {
		return nil, errGatewayResponseTooLarge
	}
	return data, nil
}

// validateGatewayMessageResponse verifies only the small, non-sensitive
// structural contract that proves the response is an Anthropic Messages
// result. A HTTP 2xx alone is not sufficient: login HTML, empty bodies, or
// an error envelope would later fail inside Claude Code even though a helper
// UI might otherwise claim the model is usable. The response content is
// deliberately never projected to callers.
func validateGatewayMessageResponse(body []byte) error {
	document, err := decodeJSONObject(body)
	if err != nil {
		return errGatewayInvalidResponse
	}
	messageType, typeOK := stringValue(document["type"])
	messageID, idOK := stringValue(document["id"])
	rawContent, contentOK := document["content"]
	if !typeOK || messageType != "message" || !idOK || !strings.HasPrefix(messageID, "msg_") || len(messageID) <= len("msg_") || !contentOK {
		return errGatewayInvalidResponse
	}
	content := bytes.TrimSpace(rawContent)
	if len(content) == 0 || content[0] != '[' {
		return errGatewayInvalidResponse
	}
	var blocks []json.RawMessage
	if err := json.Unmarshal(content, &blocks); err != nil {
		return errGatewayInvalidResponse
	}
	return nil
}

func normalizeGatewayModels(candidates []gatewayModelWire) []GatewayModel {
	modelsByID := make(map[string]GatewayModel, len(candidates))
	for _, candidate := range candidates {
		id := strings.TrimSpace(candidate.ID)
		if !modelPattern.MatchString(id) {
			continue
		}
		lowerID := strings.ToLower(id)
		// Claude Code retains any gateway model ID containing either string,
		// not merely IDs beginning with them. This admits provider-prefixed
		// IDs such as vertex_ai/claude-* and bedrock/anthropic.claude-*.
		if !strings.Contains(lowerID, "claude") && !strings.Contains(lowerID, "anthropic") {
			continue
		}
		displayName := candidate.DisplayName
		if displayName == "" {
			displayName = candidate.LegacyDisplayName
		}
		displayName = strings.TrimSpace(displayName)
		if len(displayName) > maxModelBytes || containsControl(displayName) {
			displayName = ""
		}
		modelsByID[id] = GatewayModel{ID: id, DisplayName: displayName}
	}
	models := make([]GatewayModel, 0, len(modelsByID))
	for _, model := range modelsByID {
		models = append(models, model)
	}
	sort.Slice(models, func(left, right int) bool { return models[left].ID < models[right].ID })
	return models
}

func elapsedMilliseconds(startedAt time.Time) int64 {
	return time.Since(startedAt).Round(time.Millisecond).Milliseconds()
}
