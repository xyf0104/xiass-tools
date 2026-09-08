package claudeconfig

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"regexp"
	"strings"
	"unicode"
)

var (
	errInvalidBaseURL  = errors.New("invalid Claude API root")
	errInvalidAuth     = errors.New("invalid Claude authorization token")
	errInvalidModel    = errors.New("invalid Claude model identifier")
	errInvalidSettings = errors.New("invalid Claude user settings JSON")
	errUnsafePath      = errors.New("unsafe Claude settings path")
)

var modelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@+\-\[\]]{0,255}$`)

type normalizedApplyConfig struct {
	baseURL                     string
	credentialMode              CredentialMode
	credential                  string
	apiKeyHelper                string
	enableGatewayModelDiscovery bool
	model                       string
}

type settingsDocument struct {
	root       map[string]json.RawMessage
	env        map[string]json.RawMessage
	envPresent bool
}

// NormalizeBaseURL accepts an explicit HTTPS API root or an HTTP root bound to
// localhost/loopback only. It intentionally does not append a path such as
// /v1: an API root is caller-owned and guessing endpoint semantics can route
// requests incorrectly.
func NormalizeBaseURL(value string) (string, error) {
	if value == "" || len(value) > maxBaseURLBytes || strings.TrimSpace(value) != value || containsControl(value) {
		return "", errInvalidBaseURL
	}
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed == nil || parsed.Scheme == "" || parsed.Host == "" || parsed.Opaque != "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.RawPath != "" {
		return "", errInvalidBaseURL
	}
	if containsControl(parsed.Hostname()) || containsControl(parsed.Path) || parsed.Hostname() == "" {
		return "", errInvalidBaseURL
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "https" && !(scheme == "http" && isLoopbackHost(parsed.Hostname())) {
		return "", errInvalidBaseURL
	}
	parsed.Scheme = scheme
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	return parsed.String(), nil
}

func isLoopbackHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "localhost" {
		return true
	}
	parsed := net.ParseIP(host)
	return parsed != nil && parsed.IsLoopback()
}

func normalizeApplyConfig(input ApplyConfig) (normalizedApplyConfig, error) {
	baseURL, err := NormalizeBaseURL(input.BaseURL)
	if err != nil {
		return normalizedApplyConfig{}, err
	}
	if input.Model == "" || len(input.Model) > maxModelBytes || !modelPattern.MatchString(input.Model) {
		return normalizedApplyConfig{}, errInvalidModel
	}
	credentialMode, credential, apiKeyHelper, err := normalizeCredential(input)
	if err != nil {
		return normalizedApplyConfig{}, err
	}
	return normalizedApplyConfig{
		baseURL:                     baseURL,
		credentialMode:              credentialMode,
		credential:                  credential,
		apiKeyHelper:                apiKeyHelper,
		enableGatewayModelDiscovery: input.EnableGatewayModelDiscovery,
		model:                       input.Model,
	}, nil
}

// normalizeModelIdentifier is shared by the full configuration transaction
// and the embedded-account model-only transaction. It deliberately keeps the
// same narrow Claude Code model grammar in both paths.
func normalizeModelIdentifier(value string) (string, error) {
	if value == "" || len(value) > maxModelBytes || !modelPattern.MatchString(value) {
		return "", errInvalidModel
	}
	return value, nil
}

// normalizeCredential enforces one documented Claude Code credential mode.
// Keeping the modes mutually exclusive prevents a stale higher-precedence
// value from silently overriding the value a user just saved.
func normalizeCredential(input ApplyConfig) (CredentialMode, string, string, error) {
	mode := input.CredentialMode
	credential := input.Credential
	if mode == "" {
		mode = CredentialModeAuthToken
		if credential == "" {
			credential = input.AuthToken
		}
	}
	switch mode {
	case CredentialModeAuthToken, CredentialModeAPIKey:
		if credential == "" || len(credential) > maxAuthTokenBytes || strings.TrimSpace(credential) != credential || containsControl(credential) || containsWhitespace(credential) || strings.HasPrefix(strings.ToLower(credential), "bearer ") {
			return "", "", "", errInvalidAuth
		}
		return mode, credential, "", nil
	case CredentialModeAPIKeyHelper:
		helper := input.APIKeyHelper
		if helper == "" || len(helper) > maxAPIKeyHelperBytes || strings.TrimSpace(helper) != helper || containsControl(helper) {
			return "", "", "", errInvalidAuth
		}
		return mode, "", helper, nil
	default:
		return "", "", "", errInvalidAuth
	}
}

func containsControl(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}

func containsWhitespace(value string) bool {
	for _, character := range value {
		if unicode.IsSpace(character) {
			return true
		}
	}
	return false
}

func parseSettings(data []byte) (settingsDocument, error) {
	if len(data) == 0 || len(data) > maxSettingsBytes {
		return settingsDocument{}, errInvalidSettings
	}
	root, err := decodeJSONObject(data)
	if err != nil {
		return settingsDocument{}, errInvalidSettings
	}
	document := settingsDocument{root: root, env: make(map[string]json.RawMessage)}
	if rawEnv, ok := root["env"]; ok {
		env, err := decodeJSONObject(rawEnv)
		if err != nil {
			return settingsDocument{}, errInvalidSettings
		}
		document.env = env
		document.envPresent = true
	}
	return document, nil
}

func decodeJSONObject(data []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return nil, errors.New("JSON value is not an object")
	}
	object := make(map[string]json.RawMessage)
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, errors.New("JSON object key is not a string")
		}
		if _, exists := object[key]; exists {
			return nil, errors.New("duplicate JSON object key")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		object[key] = append(json.RawMessage(nil), value...)
	}
	closing, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	closingDelimiter, ok := closing.(json.Delim)
	if !ok || closingDelimiter != '}' {
		return nil, errors.New("JSON object did not close")
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, errors.New("trailing JSON value")
		}
		return nil, err
	}
	return object, nil
}

func (document settingsDocument) updated(config normalizedApplyConfig) ([]byte, error) {
	baseURL, err := json.Marshal(config.baseURL)
	if err != nil {
		return nil, errors.New("encode Claude API root")
	}
	model, err := json.Marshal(config.model)
	if err != nil {
		return nil, errors.New("encode Claude model")
	}
	document.env["ANTHROPIC_BASE_URL"] = baseURL
	// These values have an explicit precedence order in Claude Code. A stale
	// higher-priority value would otherwise override a newly selected mode.
	delete(document.env, "ANTHROPIC_AUTH_TOKEN")
	delete(document.env, "ANTHROPIC_API_KEY")
	delete(document.root, "apiKeyHelper")
	switch config.credentialMode {
	case CredentialModeAuthToken:
		credential, marshalErr := json.Marshal(config.credential)
		if marshalErr != nil {
			return nil, errors.New("encode Claude authorization token")
		}
		document.env["ANTHROPIC_AUTH_TOKEN"] = credential
	case CredentialModeAPIKey:
		credential, marshalErr := json.Marshal(config.credential)
		if marshalErr != nil {
			return nil, errors.New("encode Claude API key")
		}
		document.env["ANTHROPIC_API_KEY"] = credential
	case CredentialModeAPIKeyHelper:
		helper, marshalErr := json.Marshal(config.apiKeyHelper)
		if marshalErr != nil {
			return nil, errors.New("encode Claude API key helper")
		}
		document.root["apiKeyHelper"] = helper
	default:
		return nil, errors.New("encode Claude credential mode")
	}
	if config.enableGatewayModelDiscovery {
		document.env["CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY"] = json.RawMessage(`"1"`)
	} else {
		delete(document.env, "CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY")
	}
	env, err := json.Marshal(document.env)
	if err != nil {
		return nil, errors.New("encode Claude environment settings")
	}
	document.root["env"] = env
	document.root["model"] = model
	updated, err := json.MarshalIndent(document.root, "", "  ")
	if err != nil {
		return nil, errors.New("encode Claude user settings")
	}
	return append(updated, '\n'), nil
}

// updatedModel changes only the documented top-level Claude Code model field.
// Existing env entries, including credentials managed by another native
// component, remain byte-for-byte semantic values in the parsed document.
func (document settingsDocument) updatedModel(model string) ([]byte, error) {
	encodedModel, err := json.Marshal(model)
	if err != nil {
		return nil, errors.New("encode Claude model setting")
	}
	document.root["model"] = encodedModel
	updated, err := json.MarshalIndent(document.root, "", "  ")
	if err != nil {
		return nil, errors.New("encode Claude user settings")
	}
	return append(updated, '\n'), nil
}

func verifyManagedSettings(data []byte, config normalizedApplyConfig) error {
	document, err := parseSettings(data)
	if err != nil {
		return err
	}
	baseURL, baseURLOK := stringValue(document.env["ANTHROPIC_BASE_URL"])
	model, modelOK := stringValue(document.root["model"])
	if !baseURLOK || !modelOK || baseURL != config.baseURL || model != config.model {
		return errors.New("managed settings verification failed")
	}
	if config.enableGatewayModelDiscovery != gatewayModelDiscoveryEnabled(document.env) {
		return errors.New("managed settings verification failed")
	}
	switch config.credentialMode {
	case CredentialModeAuthToken:
		credential, ok := stringValue(document.env["ANTHROPIC_AUTH_TOKEN"])
		if !ok || credential != config.credential || hasStringValue(document.env, "ANTHROPIC_API_KEY") || hasStringValue(document.root, "apiKeyHelper") {
			return errors.New("managed settings verification failed")
		}
	case CredentialModeAPIKey:
		credential, ok := stringValue(document.env["ANTHROPIC_API_KEY"])
		if !ok || credential != config.credential || hasStringValue(document.env, "ANTHROPIC_AUTH_TOKEN") || hasStringValue(document.root, "apiKeyHelper") {
			return errors.New("managed settings verification failed")
		}
	case CredentialModeAPIKeyHelper:
		helper, ok := stringValue(document.root["apiKeyHelper"])
		if !ok || helper != config.apiKeyHelper || hasStringValue(document.env, "ANTHROPIC_AUTH_TOKEN") || hasStringValue(document.env, "ANTHROPIC_API_KEY") {
			return errors.New("managed settings verification failed")
		}
	default:
		return errors.New("managed settings verification failed")
	}
	return nil
}

func verifySelectedModel(data []byte, model string) error {
	document, err := parseSettings(data)
	if err != nil {
		return err
	}
	actual, ok := stringValue(document.root["model"])
	if !ok || actual != model {
		return errors.New("generated Claude model setting did not verify")
	}
	return nil
}

func hasStringValue(values map[string]json.RawMessage, key string) bool {
	value, ok := stringValue(values[key])
	return ok && value != ""
}

func gatewayModelDiscoveryEnabled(env map[string]json.RawMessage) bool {
	value, ok := stringValue(env["CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY"])
	return ok && value == "1"
}

// gatewayModelDiscoveryBlocked follows Claude Code's standard gateway
// discovery constraints without exposing any user-managed provider setting or
// its value to the caller. We deliberately preserve those settings: XIASS
// Tools manages the standard Anthropic gateway fields, not a user's Bedrock,
// Vertex, Foundry, or organization traffic policy.
func gatewayModelDiscoveryBlocked(env map[string]json.RawMessage) bool {
	for key := range env {
		if strings.HasPrefix(strings.ToUpper(key), "CLAUDE_CODE_USE_") && hasStringValue(env, key) {
			return true
		}
	}
	return hasStringValue(env, "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC")
}

func stringValue(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	return value, true
}

func snapshotFromDocument(document settingsDocument) (model, baseURL string, credentialMode CredentialMode, credentialConfigured, authConfigured, helperConfigured, discoveryEnabled, discoveryBlocked, managed bool) {
	if rawModel, ok := stringValue(document.root["model"]); ok && modelPattern.MatchString(rawModel) {
		model = rawModel
	} else if rawModel != "" {
		model = "configured"
	}
	if rawBaseURL, ok := stringValue(document.env["ANTHROPIC_BASE_URL"]); ok && rawBaseURL != "" {
		if normalized, err := NormalizeBaseURL(rawBaseURL); err == nil {
			baseURL = normalized
		} else {
			baseURL = "configured"
		}
	}
	authToken, authTokenPresent := stringValue(document.env["ANTHROPIC_AUTH_TOKEN"])
	apiKey, apiKeyPresent := stringValue(document.env["ANTHROPIC_API_KEY"])
	helper, helperPresent := stringValue(document.root["apiKeyHelper"])
	authConfigured = authTokenPresent && authToken != ""
	apiKeyConfigured := apiKeyPresent && apiKey != ""
	helperConfigured = helperPresent && helper != ""
	configuredCount := 0
	if authConfigured {
		configuredCount++
		credentialMode = CredentialModeAuthToken
	}
	if apiKeyConfigured {
		configuredCount++
		credentialMode = CredentialModeAPIKey
	}
	if helperConfigured {
		configuredCount++
		credentialMode = CredentialModeAPIKeyHelper
	}
	if configuredCount != 1 {
		credentialMode = ""
	}
	_, hasBaseURL := document.env["ANTHROPIC_BASE_URL"]
	_, hasModel := document.root["model"]
	discoveryEnabled = gatewayModelDiscoveryEnabled(document.env)
	discoveryBlocked = discoveryEnabled && gatewayModelDiscoveryBlocked(document.env)
	credentialConfigured = configuredCount == 1
	managed = hasBaseURL && credentialConfigured && hasModel
	return model, baseURL, credentialMode, credentialConfigured, authConfigured, helperConfigured, discoveryEnabled, discoveryBlocked, managed
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
