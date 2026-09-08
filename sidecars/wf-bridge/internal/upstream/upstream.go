// Package upstream contains the credential-safe endpoint discovery and health
// checks used by the desktop UI. It deliberately does not know about Wails so
// the same code can be used by the proxy and unit tested with httptest.
package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"antigravity-wf-assistant/internal/storage"
)

// DefaultXIASSBaseURL intentionally contains only the domain. The resolver
// adds the provider-specific /v1 endpoint so users do not need to remember
// protocol suffixes when adding an account or model.
const DefaultXIASSBaseURL = "https://api.xiass.com"

type Config struct {
	// AccountID is the primary account used for an immediate discovery or test.
	// AccountIDs preserves every selected account when the UI batch-imports a
	// model so the runtime pool can schedule and fail over across all of them.
	AccountID  string   `json:"accountId,omitempty"`
	AccountIDs []string `json:"accountIds,omitempty"`
	Provider   string   `json:"provider"`
	APIURL     string   `json:"apiUrl"`
	// UpstreamName is carried through discovery so newly imported models can
	// retain the user-facing card label. It does not affect endpoint routing.
	UpstreamName string `json:"upstreamName,omitempty"`
	// EndpointMode is "auto" for a base domain/path that WF expands to the
	// provider endpoint, or "manual" for an exact endpoint entered by the user.
	// It intentionally remains a separate setting from APIStyle: a user may send
	// a Chat-formatted request to any gateway path they operate themselves.
	EndpointMode    string            `json:"endpointMode,omitempty"`
	APIKey          string            `json:"apiKey"`
	APIStyle        string            `json:"apiStyle"`
	MessagePathMode string            `json:"messagePathMode"`
	AuthMode        string            `json:"authMode"`
	AuthHeader      string            `json:"authHeader"`
	Headers         map[string]string `json:"headers"`
	// OAuthUpstream and ChatGPTAccountID are non-secret runtime metadata copied
	// from a selected account. They make the OpenAI/Codex OAuth route distinct
	// from a normal Bearer API-key gateway.
	OAuthUpstream    string `json:"oauthUpstream,omitempty"`
	ChatGPTAccountID string `json:"chatgptAccountId,omitempty"`
}

type ModelInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type DiscoveryResult struct {
	OK         bool        `json:"ok"`
	Message    string      `json:"message"`
	Models     []ModelInfo `json:"models"`
	Endpoint   string      `json:"endpoint"`
	StatusCode int         `json:"statusCode"`
}

type TestResult struct {
	OK         bool   `json:"ok"`
	Message    string `json:"message"`
	Endpoint   string `json:"endpoint"`
	APIStyle   string `json:"apiStyle"`
	StatusCode int    `json:"statusCode"`
	ElapsedMs  int64  `json:"elapsedMs"`
}

var headerNamePattern = regexp.MustCompile(`^[!#$%&'*+\-.^_` + "`" + `|~0-9A-Za-z]+$`)

// Upstreams sometimes echo request headers in a JSON error message. Preserve
// useful non-sensitive diagnostics, but never surface credentials in the UI.
var sensitiveUpstreamErrorDetailPattern = regexp.MustCompile(`(?i)(?:authorization|bearer|x[\s_-]*api[\s_-]*key|api[\s_-]*key|apikey)`)

var blockedHeaderNames = map[string]struct{}{
	"host": {}, "content-length": {}, "content-type": {}, "transfer-encoding": {},
	"connection": {}, "keep-alive": {}, "proxy-authorization": {}, "proxy-authenticate": {},
	"cookie": {}, "set-cookie": {}, "accept-encoding": {},
}

func ConfigFromModel(model storage.CustomModel) Config {
	return Config{
		Provider: model.Provider, APIURL: model.APIURL, APIKey: model.APIKey,
		UpstreamName: model.UpstreamName,
		EndpointMode: model.EndpointMode, APIStyle: model.APIStyle, MessagePathMode: model.MessagePathMode, AuthMode: model.AuthMode, AuthHeader: model.AuthHeader,
		Headers: model.Headers, OAuthUpstream: model.RuntimeOAuthUpstream, ChatGPTAccountID: model.RuntimeChatGPTAccountID,
	}
}

func ConfigFromAccount(account storage.UpstreamAccount) Config {
	return Config{
		AccountID: account.ID, AccountIDs: []string{account.ID}, Provider: account.Provider, APIURL: account.APIURL,
		EndpointMode: account.EndpointMode, APIKey: account.EffectiveAPIKey(), APIStyle: account.APIStyle,
		MessagePathMode: account.MessagePathMode, AuthMode: account.AuthMode,
		AuthHeader: account.AuthHeader, Headers: account.Headers,
		OAuthUpstream: account.OAuth.Upstream, ChatGPTAccountID: account.Identity.ChatGPTAccountID,
	}
}

// IsOpenAICodexOAuth is intentionally narrower than "type == oauth": only
// the built-in XIASS-compatible direct route gets Codex identity headers and
// alternate model/quota endpoints. Custom OAuth accounts retain their own
// configured HTTP contract.
func IsOpenAICodexOAuth(config Config) bool {
	return NormalizedProvider(config.Provider) == "openai" &&
		strings.EqualFold(strings.TrimSpace(config.OAuthUpstream), storage.OpenAICodexOAuthUpstream)
}

func NormalizedProvider(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "anthropic", "grok", "openai", "custom":
		return value
	default:
		return "openai"
	}
}

// NormalizedEndpointMode accepts a small, deliberately stable vocabulary. An
// unknown/legacy value stays safely in automatic mode rather than accidentally
// treating a base domain as a complete request endpoint.
func NormalizedEndpointMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "manual", "exact", "full", "full_url", "full-url":
		return "manual"
	default:
		return "auto"
	}
}

// UsesManualEndpoint reports whether the entered APIURL must be sent exactly
// as written. MessagePathMode=manual is kept as a backwards-compatible alias
// for configurations produced by earlier WF versions.
func UsesManualEndpoint(config Config) bool {
	return NormalizedEndpointMode(config.EndpointMode) == "manual" ||
		strings.EqualFold(strings.TrimSpace(config.MessagePathMode), "manual")
}

// EffectiveAPIStyle preserves legacy configs as Chat Completions. "auto" is
// retained as a persisted compatibility value, but Antigravity routing treats
// it as Chat unless the caller explicitly selected Responses or uses Codex
// OAuth, whose credential is Responses-only.
func EffectiveAPIStyle(config Config) string {
	if IsOpenAICodexOAuth(config) {
		return "responses"
	}
	if NormalizedProvider(config.Provider) == "anthropic" {
		return "messages"
	}
	switch strings.ToLower(strings.TrimSpace(config.APIStyle)) {
	case "responses", "chat_completions", "messages", "auto":
		return strings.ToLower(strings.TrimSpace(config.APIStyle))
	default:
		return "chat_completions"
	}
}

func validateBaseURL(rawURL string) (*url.URL, error) {
	value := strings.TrimSpace(rawURL)
	if value == "" {
		value = DefaultXIASSBaseURL
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil {
		return nil, fmt.Errorf("API 地址无效")
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname())) {
		return nil, fmt.Errorf("API 地址必须使用 HTTPS；仅本机回环地址可使用 HTTP")
	}
	return parsed, nil
}

func isLoopbackHost(host string) bool {
	host = strings.Trim(strings.ToLower(host), "[]")
	if host == "localhost" || host == "::1" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func endpointURL(rawURL, leaf string) (string, error) {
	parsed, err := validateBaseURL(rawURL)
	if err != nil {
		return "", err
	}
	path := strings.TrimRight(parsed.Path, "/")
	for _, suffix := range []string{"/chat/completions", "/chat/messages", "/responses", "/messages", "/images/generations", "/models"} {
		if strings.HasSuffix(path, suffix) {
			path = strings.TrimSuffix(path, suffix)
			break
		}
	}
	if path == "" {
		path = "/v1"
	}
	parsed.Path = strings.TrimRight(path, "/") + "/" + strings.TrimLeft(leaf, "/")
	parsed.RawPath = ""
	return parsed.String(), nil
}

// manualEndpointURL validates a user-provided full URL but does not add,
// remove, or replace its path. Query parameters are kept as well, which is
// important for gateways that route on a workspace or deployment query.
func manualEndpointURL(rawURL string) (string, error) {
	parsed, err := validateBaseURL(rawURL)
	if err != nil {
		return "", err
	}
	parsed.Fragment = ""
	return parsed.String(), nil
}

func endpointURLForConfig(config Config, leaf string) (string, error) {
	if UsesManualEndpoint(config) {
		return manualEndpointURL(config.APIURL)
	}
	return endpointURL(config.APIURL, leaf)
}

func ResolveChatCompletionsURL(rawURL string) (string, error) {
	return endpointURL(rawURL, "chat/completions")
}

func ResolveChatCompletionsURLForConfig(config Config) (string, error) {
	return endpointURLForConfig(config, "chat/completions")
}

func ResolveResponsesURL(rawURL string) (string, error) { return endpointURL(rawURL, "responses") }

func ResolveResponsesURLForConfig(config Config) (string, error) {
	return endpointURLForConfig(config, "responses")
}

// ResolveImagesGenerationsURLForConfig resolves OpenAI's dedicated image
// generation endpoint. A manually entered URL is preserved only when it is
// already an images endpoint; a saved Chat or Responses endpoint still needs
// to be converted to its sibling image route. This lets one upstream account
// serve both a text model and a separate image model without asking users to
// duplicate its base URL.
func ResolveImagesGenerationsURLForConfig(config Config) (string, error) {
	if UsesManualEndpoint(config) {
		parsed, err := validateBaseURL(config.APIURL)
		if err != nil {
			return "", err
		}
		if strings.HasSuffix(strings.TrimRight(parsed.Path, "/"), "/images/generations") {
			return manualEndpointURL(config.APIURL)
		}
	}
	return endpointURL(config.APIURL, "images/generations")
}

func ResolveAnthropicMessagesURL(rawURL string) (string, error) {
	return endpointURL(rawURL, "messages")
}

// ResolveAnthropicMessagesURLForConfig supports both the standard Anthropic
// Messages API and gateways whose compatible endpoint is /v1/chat/messages.
// "manual" preserves a full messages endpoint entered in APIURL; automatic
// mode starts with the standard route and callers can use the candidate helper
// to retry the compatibility route only when the endpoint is absent.
func ResolveAnthropicMessagesURLForConfig(config Config) (string, error) {
	if UsesManualEndpoint(config) {
		return manualEndpointURL(config.APIURL)
	}
	if strings.EqualFold(strings.TrimSpace(config.MessagePathMode), "compat") {
		return endpointURL(config.APIURL, "chat/messages")
	}
	return ResolveAnthropicMessagesURL(config.APIURL)
}

func ResolveAnthropicMessageCandidates(config Config) ([]string, error) {
	primary, err := ResolveAnthropicMessagesURLForConfig(config)
	if err != nil {
		return nil, err
	}
	if !UsesManualEndpoint(config) && (strings.EqualFold(strings.TrimSpace(config.MessagePathMode), "auto") || strings.TrimSpace(config.MessagePathMode) == "") {
		compat, err := endpointURL(config.APIURL, "chat/messages")
		if err != nil {
			return nil, err
		}
		if compat != primary {
			return []string{primary, compat}, nil
		}
	}
	return []string{primary}, nil
}
func ResolveModelsURL(rawURL string) (string, error) { return endpointURL(rawURL, "models") }

// ResolveModelsURLForConfig still derives /models from a manually supplied
// request endpoint, unless the user explicitly supplied a /models endpoint.
// A full chat endpoint cannot also be a model-list endpoint, while this keeps
// discovery convenient and leaves manually-added models entirely unrestricted.
func ResolveModelsURLForConfig(config Config) (string, error) {
	if IsOpenAICodexOAuth(config) {
		return ResolveOpenAICodexModelsURL(config)
	}
	if UsesManualEndpoint(config) {
		parsed, err := validateBaseURL(config.APIURL)
		if err != nil {
			return "", err
		}
		if strings.HasSuffix(strings.TrimRight(parsed.Path, "/"), "/models") {
			return manualEndpointURL(config.APIURL)
		}
	}
	return ResolveModelsURL(config.APIURL)
}

func ValidateConfig(config Config) error {
	if _, err := validateBaseURL(config.APIURL); err != nil {
		return err
	}
	if strings.TrimSpace(config.APIKey) == "" {
		return fmt.Errorf("请填写 API Key 或访问令牌")
	}
	for name := range config.Headers {
		if err := validateHeaderName(name, false); err != nil {
			return err
		}
	}
	if strings.EqualFold(strings.TrimSpace(config.AuthMode), "custom_header") {
		if err := validateHeaderName(config.AuthHeader, true); err != nil {
			return err
		}
	}
	return nil
}

func validateHeaderName(name string, allowAuth bool) error {
	name = strings.TrimSpace(name)
	lower := strings.ToLower(name)
	if name == "" || !headerNamePattern.MatchString(name) {
		return fmt.Errorf("请求头名称无效")
	}
	if _, blocked := blockedHeaderNames[lower]; blocked {
		return fmt.Errorf("请求头 %s 不允许覆盖", name)
	}
	if !allowAuth && (lower == "authorization" || lower == "x-api-key") {
		return fmt.Errorf("认证请求头请使用认证方式配置")
	}
	return nil
}

func ApplyCredentials(req *http.Request, config Config) error {
	if err := ValidateConfig(config); err != nil {
		return err
	}
	for name, value := range config.Headers {
		if strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("请求头值无效")
		}
		req.Header.Set(strings.TrimSpace(name), strings.TrimSpace(value))
	}
	mode := strings.ToLower(strings.TrimSpace(config.AuthMode))
	if mode == "" {
		if NormalizedProvider(config.Provider) == "anthropic" {
			mode = "x_api_key"
		} else {
			mode = "bearer"
		}
	}
	switch mode {
	case "bearer":
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(config.APIKey))
	case "x_api_key":
		req.Header.Set("x-api-key", strings.TrimSpace(config.APIKey))
	case "custom_header":
		req.Header.Set(strings.TrimSpace(config.AuthHeader), strings.TrimSpace(config.APIKey))
	default:
		return fmt.Errorf("不支持的认证方式")
	}
	if NormalizedProvider(config.Provider) == "anthropic" {
		req.Header.Set("anthropic-version", "2023-06-01")
	}
	if IsOpenAICodexOAuth(config) {
		applyOpenAICodexOAuthHeaders(req, config)
	}
	return nil
}

func DiscoverModels(ctx context.Context, config Config) DiscoveryResult {
	if err := ValidateConfig(config); err != nil {
		return DiscoveryResult{Message: err.Error()}
	}
	endpoint, err := ResolveModelsURLForConfig(config)
	if err != nil {
		return DiscoveryResult{Message: err.Error()}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return DiscoveryResult{Message: "无法创建模型列表请求", Endpoint: endpoint}
	}
	request.Header.Set("Accept", "application/json")
	if err := ApplyCredentials(request, config); err != nil {
		return DiscoveryResult{Message: err.Error(), Endpoint: endpoint}
	}
	client := &http.Client{Timeout: 30 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return DiscoveryResult{Message: "无法连接上游：" + safeNetworkError(err), Endpoint: endpoint}
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return DiscoveryResult{Message: statusMessage(response.StatusCode, body, config), Endpoint: endpoint, StatusCode: response.StatusCode}
	}
	models, err := ParseModels(body)
	if err != nil {
		return DiscoveryResult{Message: "上游返回的模型列表无法识别", Endpoint: endpoint, StatusCode: response.StatusCode}
	}
	if len(models) == 0 {
		return DiscoveryResult{Message: "上游未返回可添加的模型", Endpoint: endpoint, StatusCode: response.StatusCode}
	}
	return DiscoveryResult{OK: true, Message: fmt.Sprintf("已发现 %d 个可用模型", len(models)), Models: models, Endpoint: endpoint, StatusCode: response.StatusCode}
}

func ParseModels(body []byte) ([]ModelInfo, error) {
	var decoded any
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, err
	}
	seen := map[string]ModelInfo{}
	collectModelList(decoded, seen, 0)
	result := make([]ModelInfo, 0, len(seen))
	for _, model := range seen {
		result = append(result, model)
	}
	sort.Slice(result, func(i, j int) bool { return strings.ToLower(result[i].ID) < strings.ToLower(result[j].ID) })
	return result, nil
}

func collectModelList(value any, seen map[string]ModelInfo, depth int) {
	if depth > 5 {
		return
	}
	switch current := value.(type) {
	case []any:
		for _, item := range current {
			collectModelList(item, seen, depth+1)
		}
	case map[string]any:
		if id := modelID(current); id != "" {
			seen[id] = ModelInfo{ID: id, Name: modelDisplayName(current, id)}
		}
		for _, key := range []string{"data", "models", "available_models", "availableModels", "result", "response"} {
			if child, ok := current[key]; ok {
				collectModelList(child, seen, depth+1)
			}
		}
	case string:
		id := strings.TrimSpace(strings.TrimPrefix(current, "models/"))
		if id != "" && !strings.ContainsAny(id, " \t\r\n") {
			seen[id] = ModelInfo{ID: id, Name: id}
		}
	}
}

func modelID(value map[string]any) string {
	// The direct ChatGPT Codex manifest identifies a model with `slug`, while
	// ordinary OpenAI-compatible /models responses use `id`. Accept both so
	// the OAuth account list reflects the live manifest instead of falling
	// back to a local default model.
	for _, field := range []string{"id", "model", "model_id", "modelId", "slug", "name"} {
		candidate, _ := value[field].(string)
		candidate = strings.TrimSpace(strings.TrimPrefix(candidate, "models/"))
		if candidate != "" {
			return candidate
		}
	}
	return ""
}

func modelDisplayName(value map[string]any, fallback string) string {
	for _, field := range []string{"display_name", "displayName", "name", "id", "model", "slug"} {
		if candidate, ok := value[field].(string); ok && strings.TrimSpace(candidate) != "" {
			return strings.TrimSpace(strings.TrimPrefix(candidate, "models/"))
		}
	}
	return fallback
}

// TestModel sends a tiny non-streaming request only after the user explicitly
// presses the test action. It never logs credentials or response bodies.
func TestModel(ctx context.Context, config Config, model string) TestResult {
	started := time.Now()
	finish := func(result TestResult) TestResult {
		result.ElapsedMs = time.Since(started).Milliseconds()
		return result
	}
	if err := ValidateConfig(config); err != nil {
		return finish(TestResult{Message: err.Error()})
	}
	model = strings.TrimSpace(strings.TrimPrefix(model, "models/"))
	if model == "" {
		return finish(TestResult{Message: "请选择需要测试的模型"})
	}
	style := EffectiveAPIStyle(config)
	if style == "auto" {
		return finish(testChatCompletions(ctx, config, model))
	}
	switch style {
	case "messages":
		return finish(testAnthropic(ctx, config, model))
	case "responses":
		return finish(testResponses(ctx, config, model))
	default:
		return finish(testChatCompletions(ctx, config, model))
	}
}

// CanFallbackToChat identifies only endpoint-availability errors. Callers must
// not hide an authentication, quota, capability, or validation error by
// retrying it on a different API surface.
func CanFallbackToChat(statusCode int) bool {
	return statusCode == http.StatusNotFound || statusCode == http.StatusMethodNotAllowed || statusCode == http.StatusNotImplemented
}

// CanFallbackToChatResponse recognises a narrow class of HTTP 400 responses
// emitted by compatibility gateways when a request reached a Chat-only route
// through /v1/responses. The generic status alone is deliberately insufficient:
// semantic validation, authentication and model errors must stay visible.
// Callers use this only for automatic API style; explicit Responses and Codex
// OAuth never change protocol silently.
func CanFallbackToChatResponse(statusCode int, body string) bool {
	if CanFallbackToChat(statusCode) {
		return true
	}
	if statusCode != http.StatusBadRequest {
		return false
	}
	value := strings.ToLower(strings.TrimSpace(body))
	for _, marker := range []string{
		"upstream request failed", "upstream_request_failed",
		"responses api is not supported", "responses endpoint is not supported",
		"unsupported responses endpoint", "missing required parameter: messages",
		"messages is required", "unknown parameter: input",
		"unrecognized request argument supplied: input", "unknown field: input",
		"unknown field \"input\"",
	} {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}

func testChatCompletions(ctx context.Context, config Config, model string) TestResult {
	endpoint, err := ResolveChatCompletionsURLForConfig(config)
	if err != nil {
		return TestResult{Message: err.Error(), APIStyle: "chat_completions"}
	}
	body, _ := json.Marshal(map[string]any{
		"model": model, "stream": false, "max_tokens": 8,
		"messages": []any{map[string]any{"role": "user", "content": "Reply with OK."}},
	})
	return doTestRequest(ctx, config, endpoint, "chat_completions", body)
}

func testResponses(ctx context.Context, config Config, model string) TestResult {
	endpoint, err := ResolveResponsesURLForConfig(config)
	if err != nil {
		return TestResult{Message: err.Error(), APIStyle: "responses"}
	}
	body, _ := json.Marshal(map[string]any{"model": model, "input": "Reply with OK.", "max_output_tokens": 8})
	return doTestRequest(ctx, config, endpoint, "responses", body)
}

func testAnthropic(ctx context.Context, config Config, model string) TestResult {
	body, _ := json.Marshal(map[string]any{
		"model": model, "max_tokens": 8, "messages": []any{map[string]any{"role": "user", "content": "Reply with OK."}},
	})
	endpoints, err := ResolveAnthropicMessageCandidates(config)
	if err != nil {
		return TestResult{Message: err.Error(), APIStyle: "messages"}
	}
	result := doTestRequest(ctx, config, endpoints[0], "messages", body)
	if len(endpoints) > 1 && !result.OK && CanFallbackToChat(result.StatusCode) {
		return doTestRequest(ctx, config, endpoints[1], "messages", body)
	}
	return result
}

func doTestRequest(ctx context.Context, config Config, endpoint, style string, body []byte) TestResult {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return TestResult{Message: "无法创建测试请求", Endpoint: endpoint, APIStyle: style}
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	if err := ApplyCredentials(request, config); err != nil {
		return TestResult{Message: err.Error(), Endpoint: endpoint, APIStyle: style}
	}
	response, err := (&http.Client{Timeout: 45 * time.Second}).Do(request)
	if err != nil {
		return TestResult{Message: "无法连接上游：" + safeNetworkError(err), Endpoint: endpoint, APIStyle: style}
	}
	defer response.Body.Close()
	responseBody, _ := io.ReadAll(io.LimitReader(response.Body, 512<<10))
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return TestResult{Message: statusMessage(response.StatusCode, responseBody, config), Endpoint: endpoint, APIStyle: style, StatusCode: response.StatusCode}
	}
	return TestResult{OK: true, Message: fmt.Sprintf("模型可用（HTTP %d）", response.StatusCode), Endpoint: endpoint, APIStyle: style, StatusCode: response.StatusCode}
}

func safeNetworkError(err error) string {
	if errors, ok := err.(interface{ Timeout() bool }); ok && errors.Timeout() {
		return "请求超时"
	}
	return "连接失败"
}

func statusMessage(statusCode int, body []byte, config Config) string {
	var decoded map[string]any
	if json.Unmarshal(body, &decoded) == nil {
		if errorValue, ok := decoded["error"].(map[string]any); ok {
			if message, ok := errorValue["message"].(string); ok && strings.TrimSpace(message) != "" {
				return fmt.Sprintf("上游返回 HTTP %d：%s", statusCode, safeStatusDetail(message, config))
			}
		}
		if message, ok := decoded["message"].(string); ok && strings.TrimSpace(message) != "" {
			return fmt.Sprintf("上游返回 HTTP %d：%s", statusCode, safeStatusDetail(message, config))
		}
	}
	return fmt.Sprintf("上游返回 HTTP %d", statusCode)
}

func safeStatusDetail(message string, config Config) string {
	message = strings.TrimSpace(message)
	if sensitiveUpstreamErrorDetailPattern.MatchString(message) || containsConfiguredCredential(message, config) {
		return "上游拒绝了请求（鉴权详情已隐藏）"
	}
	return message
}

// containsConfiguredCredential catches a gateway that mirrors a request value
// verbatim without a recognizable Authorization or API-key label. Treat every
// configured credential/header value as opaque: once any exact value appears
// in an error, hide the entire upstream detail rather than trying to preserve
// a potentially incomplete or transformed fragment.
func containsConfiguredCredential(message string, config Config) bool {
	message = strings.TrimSpace(message)
	if message == "" {
		return false
	}
	values := make([]string, 0, len(config.Headers)+1)
	if apiKey := strings.TrimSpace(config.APIKey); apiKey != "" {
		values = append(values, apiKey)
	}
	for _, value := range config.Headers {
		if value = strings.TrimSpace(value); value != "" {
			values = append(values, value)
		}
	}
	for _, value := range values {
		if strings.Contains(message, value) {
			return true
		}
	}
	return false
}
