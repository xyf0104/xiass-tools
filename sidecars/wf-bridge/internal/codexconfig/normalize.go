package codexconfig

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
)

var providerIDPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)

// NormalizeApplyConfig validates all UI-provided fields and produces the exact
// canonical values written to config.toml. It never logs the API key.
func NormalizeApplyConfig(input ApplyConfig) (ApplyConfig, error) {
	input.BaseURL = strings.TrimSpace(input.BaseURL)
	input.APIKey = strings.TrimSpace(input.APIKey)
	input.KeyName = strings.TrimSpace(input.KeyName)
	input.ProviderID = strings.TrimSpace(input.ProviderID)
	input.ProviderName = strings.TrimSpace(input.ProviderName)
	input.Model = strings.TrimSpace(input.Model)
	input.ReviewModel = strings.TrimSpace(input.ReviewModel)
	input.WireAPI = strings.ToLower(strings.TrimSpace(input.WireAPI))
	input.WebSearch = strings.ToLower(strings.TrimSpace(input.WebSearch))

	if err := validateShortText(input.APIKey, 8192, "API key"); err != nil || input.APIKey == "" {
		if err != nil {
			return input, err
		}
		return input, errors.New("API key is required")
	}
	if input.ProviderID == "" {
		input.ProviderID = DefaultProviderID
	}
	if !providerIDPattern.MatchString(input.ProviderID) {
		return input, errors.New("provider ID must start with a letter and use only letters, numbers, hyphens, or underscores")
	}
	if input.ProviderName == "" && input.KeyName != "" {
		input.ProviderName = input.KeyName
	}
	if input.ProviderName == "" {
		input.ProviderName = DefaultProviderName
	}
	if input.Model == "" {
		input.Model = DefaultModel
	}
	if input.ReviewModel == "" {
		input.ReviewModel = input.Model
	}
	if input.WireAPI == "" {
		input.WireAPI = DefaultWireAPI
	}
	if input.WebSearch == "" {
		input.WebSearch = "live"
	}
	for _, field := range []struct {
		value string
		name  string
	}{
		{input.KeyName, "key name"},
		{input.ProviderName, "provider name"},
		{input.Model, "model name"},
		{input.ReviewModel, "review model name"},
	} {
		if err := validateShortText(field.value, 200, field.name); err != nil {
			return input, err
		}
	}
	if input.WireAPI != "responses" {
		return input, errors.New("Codex provider wire API must be responses")
	}
	switch input.WebSearch {
	case "live", "cached", "disabled", "off":
	default:
		return input, errors.New("invalid web search mode")
	}

	baseURL, err := NormalizeBaseURL(input.BaseURL)
	if err != nil {
		return input, err
	}
	input.BaseURL = baseURL

	context, err := NormalizeContextSettings(ContextSettings{
		ModelContextWindow:         input.ModelContextWindow,
		ModelAutoCompactTokenLimit: input.ModelAutoCompactTokenLimit,
	})
	if err != nil {
		return input, err
	}
	input.ModelContextWindow = context.ModelContextWindow
	input.ModelAutoCompactTokenLimit = context.ModelAutoCompactTokenLimit
	return input, nil
}

// NormalizeBaseURL accepts a bare remote host as HTTPS and a bare loopback
// address as HTTP. It strips query/fragment values and ensures the result is a
// Codex-compatible OpenAI API root ending in /v1.
func NormalizeBaseURL(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 4096 || strings.ContainsAny(value, "\r\n\x00") {
		return "", errors.New("base URL is invalid")
	}
	if !strings.Contains(value, "://") {
		candidate, err := url.Parse("http://" + value)
		if err == nil && isLoopbackHostname(candidate.Hostname()) {
			value = "http://" + value
		} else {
			value = "https://" + value
		}
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Hostname() == "" || parsed.User != nil {
		return "", errors.New("base URL must be a valid HTTPS URL or a loopback HTTP URL")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != "https" && (parsed.Scheme != "http" || !isLoopbackHostname(parsed.Hostname())) {
		return "", errors.New("base URL must use HTTPS; HTTP is allowed only for localhost, 127.0.0.0/8, or ::1")
	}
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	parsed.RawFragment = ""
	parsed.Path = normalizeAPIPath(parsed.Path)
	parsed.RawPath = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

func normalizeAPIPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" || path == "/" {
		return "/v1"
	}
	path = "/" + strings.Trim(path, "/")
	for _, suffix := range []string{
		"/chat/completions",
		"/responses",
		"/messages",
		"/models",
		"/images/generations",
	} {
		if hasPathSuffixFold(path, suffix) {
			path = path[:len(path)-len(suffix)]
			break
		}
	}
	path = strings.TrimRight(path, "/")
	if hasPathSuffixFold(path, "/v1") {
		return path
	}
	return path + "/v1"
}

func hasPathSuffixFold(path, suffix string) bool {
	return len(path) >= len(suffix) && strings.EqualFold(path[len(path)-len(suffix):], suffix)
}

func NormalizeContextSettings(input ContextSettings) (ContextSettings, error) {
	contextWasProvided := input.ModelContextWindow != 0
	if input.ModelContextWindow == 0 {
		input.ModelContextWindow = DefaultContextWindow
	}
	if input.ModelAutoCompactTokenLimit == 0 {
		if contextWasProvided {
			input.ModelAutoCompactTokenLimit = input.ModelContextWindow * 9 / 10
		} else {
			input.ModelAutoCompactTokenLimit = DefaultAutoCompactTokenLimit
		}
	}
	if input.ModelContextWindow < MinimumContextWindow || input.ModelContextWindow > MaximumContextWindow {
		return input, fmt.Errorf("model context window must be between %d and %d tokens", MinimumContextWindow, MaximumContextWindow)
	}
	if input.ModelAutoCompactTokenLimit < MinimumAutoCompactTokenLimit || input.ModelAutoCompactTokenLimit > input.ModelContextWindow {
		return input, fmt.Errorf("automatic compact token limit must be between %d and the context window (%d) tokens", MinimumAutoCompactTokenLimit, input.ModelContextWindow)
	}
	return input, nil
}

func DefaultContextSettings() ContextSettings {
	return ContextSettings{
		ModelContextWindow:         DefaultContextWindow,
		ModelAutoCompactTokenLimit: DefaultAutoCompactTokenLimit,
	}
}

func validateShortText(value string, maximum int, name string) error {
	if len(value) > maximum || strings.ContainsAny(value, "\r\n\x00") {
		return fmt.Errorf("invalid %s", name)
	}
	return nil
}

func isLoopbackHostname(hostname string) bool {
	if strings.EqualFold(strings.TrimSuffix(hostname, "."), "localhost") {
		return true
	}
	ip := net.ParseIP(hostname)
	return ip != nil && ip.IsLoopback()
}
