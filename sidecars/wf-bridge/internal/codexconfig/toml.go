package codexconfig

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

var managedTopLevelKeys = map[string]struct{}{
	"model_provider":                 {},
	"model":                          {},
	"review_model":                   {},
	"model_context_window":           {},
	"model_auto_compact_token_limit": {},
	"web_search":                     {},
}

func patchConfig(original []byte, input ApplyConfig, managedIDs []string) []byte {
	text := strings.ReplaceAll(string(original), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	body := make([]string, 0, len(lines))
	inTopLevel := true
	skipManagedProvider := false

	for _, line := range lines {
		if path, ok := tomlTablePath(line); ok {
			inTopLevel = false
			skipManagedProvider = isManagedProviderPath(path, managedIDs)
			if skipManagedProvider {
				continue
			}
		}
		if skipManagedProvider {
			continue
		}
		if inTopLevel {
			if key, ok := tomlAssignmentKey(line); ok {
				if _, managed := managedTopLevelKeys[key]; managed {
					continue
				}
			}
		}
		body = append(body, line)
	}

	bodyText := strings.Trim(strings.Join(body, "\n"), "\n")
	top := []string{
		`model_provider = "` + escapeTOML(input.ProviderID) + `"`,
		`model = "` + escapeTOML(input.Model) + `"`,
		`review_model = "` + escapeTOML(input.ReviewModel) + `"`,
		"model_context_window = " + strconv.FormatInt(input.ModelContextWindow, 10),
		"model_auto_compact_token_limit = " + strconv.FormatInt(input.ModelAutoCompactTokenLimit, 10),
		`web_search = "` + escapeTOML(input.WebSearch) + `"`,
	}
	provider := []string{
		"[model_providers." + input.ProviderID + "]",
		`name = "` + escapeTOML(input.ProviderName) + `"`,
		`base_url = "` + escapeTOML(input.BaseURL) + `"`,
		`wire_api = "` + escapeTOML(input.WireAPI) + `"`,
		"requires_openai_auth = false",
		`experimental_bearer_token = "` + escapeTOML(input.APIKey) + `"`,
		`http_headers = { "x-openai-actor-authorization" = "` + escapeTOML(actorAuthorization(input.BaseURL)) + `" }`,
		"supports_websockets = false",
	}
	parts := []string{strings.Join(top, "\n")}
	if bodyText != "" {
		parts = append(parts, bodyText)
	}
	parts = append(parts, strings.Join(provider, "\n"))
	return []byte(strings.Join(parts, "\n\n") + "\n")
}

func verifyManagedConfig(data []byte, expected ApplyConfig) error {
	normalized, err := NormalizeApplyConfig(expected)
	if err != nil {
		return err
	}
	var root map[string]any
	if err := toml.Unmarshal(data, &root); err != nil {
		return err
	}
	for key, want := range map[string]string{
		"model_provider": normalized.ProviderID,
		"model":          normalized.Model,
		"review_model":   normalized.ReviewModel,
		"web_search":     normalized.WebSearch,
	} {
		if got, _ := root[key].(string); got != want {
			return fmt.Errorf("%s mismatch", key)
		}
	}
	if !numberEquals(root["model_context_window"], normalized.ModelContextWindow) || !numberEquals(root["model_auto_compact_token_limit"], normalized.ModelAutoCompactTokenLimit) {
		return errors.New("context window settings mismatch")
	}
	providers, ok := mapValue(root["model_providers"])
	if !ok {
		return errors.New("model_providers table missing")
	}
	provider, ok := mapValue(providers[normalized.ProviderID])
	if !ok {
		return errors.New("managed provider table missing")
	}
	for key, want := range map[string]string{
		"name":                      normalized.ProviderName,
		"base_url":                  normalized.BaseURL,
		"wire_api":                  normalized.WireAPI,
		"experimental_bearer_token": normalized.APIKey,
	} {
		if got, _ := provider[key].(string); got != want {
			return fmt.Errorf("provider %s mismatch", key)
		}
	}
	if value, ok := provider["requires_openai_auth"].(bool); !ok || value {
		return errors.New("requires_openai_auth must be false")
	}
	if value, ok := provider["supports_websockets"].(bool); !ok || value {
		return errors.New("supports_websockets must be false")
	}
	headers, ok := mapValue(provider["http_headers"])
	if !ok || stringValue(headers["x-openai-actor-authorization"]) != actorAuthorization(normalized.BaseURL) {
		return errors.New("actor authorization header mismatch")
	}
	return nil
}

func readContextSettingsFromRoot(root map[string]any) (ContextSettings, error) {
	settings := DefaultContextSettings()
	if value, present := root["model_context_window"]; present {
		parsed, ok := integerValue(value)
		if !ok {
			return settings, errors.New("model_context_window is not an integer")
		}
		settings.ModelContextWindow = parsed
	}
	if value, present := root["model_auto_compact_token_limit"]; present {
		parsed, ok := integerValue(value)
		if !ok {
			return settings, errors.New("model_auto_compact_token_limit is not an integer")
		}
		settings.ModelAutoCompactTokenLimit = parsed
	}
	return NormalizeContextSettings(settings)
}

func actorAuthorization(baseURL string) string {
	// NormalizeApplyConfig already validates this value; parsing here only keeps
	// verification defensive if a caller passes a hand-built config.
	parsed, err := NormalizeBaseURL(baseURL)
	if err != nil {
		return ""
	}
	urlValue := strings.Split(strings.TrimPrefix(parsed, "https://"), "/")[0]
	if strings.HasPrefix(parsed, "http://") {
		urlValue = strings.Split(strings.TrimPrefix(parsed, "http://"), "/")[0]
	}
	// Hostname excludes a loopback port and exactly matches Codex's expected
	// actor authorization host value.
	if strings.HasPrefix(urlValue, "[") {
		if end := strings.Index(urlValue, "]"); end >= 0 {
			return urlValue[1:end]
		}
	}
	if index := strings.LastIndex(urlValue, ":"); index >= 0 && strings.Count(urlValue, ":") == 1 {
		return urlValue[:index]
	}
	return urlValue
}

func mapValue(value any) (map[string]any, bool) {
	mapped, ok := value.(map[string]any)
	return mapped, ok
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func numberEquals(value any, want int64) bool {
	parsed, ok := integerValue(value)
	return ok && parsed == want
}

func integerValue(value any) (int64, bool) {
	switch typed := value.(type) {
	case int64:
		return typed, true
	case int:
		return int64(typed), true
	case float64:
		if typed != float64(int64(typed)) {
			return 0, false
		}
		return int64(typed), true
	case string:
		parsed, err := strconv.ParseInt(typed, 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func escapeTOML(value string) string {
	return strings.NewReplacer("\\", "\\\\", `"`, `\"`, "\r", `\r`, "\n", `\n`).Replace(value)
}

func tomlAssignmentKey(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "[") {
		return "", false
	}
	equals := indexTOMLDelimiter(trimmed, '=')
	if equals <= 0 {
		return "", false
	}
	key := strings.TrimSpace(trimmed[:equals])
	if strings.HasPrefix(key, `"`) {
		unquoted, err := strconv.Unquote(key)
		if err != nil {
			return "", false
		}
		return unquoted, true
	}
	if strings.ContainsAny(key, ". \t") {
		return "", false
	}
	return key, true
}

func tomlTablePath(line string) ([]string, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "[") {
		return nil, false
	}
	array := strings.HasPrefix(trimmed, "[[")
	openLength := 1
	close := "]"
	if array {
		openLength = 2
		close = "]]"
	}
	end := findTableClose(trimmed[openLength:], close)
	if end < 0 {
		return nil, false
	}
	end += openLength
	remainder := strings.TrimSpace(trimmed[end+len(close):])
	if remainder != "" && !strings.HasPrefix(remainder, "#") {
		return nil, false
	}
	return parseTOMLDottedKey(strings.TrimSpace(trimmed[openLength:end]))
}

func findTableClose(value, close string) int {
	inQuote := false
	escaped := false
	for index := 0; index < len(value); index++ {
		character := value[index]
		if inQuote {
			if escaped {
				escaped = false
				continue
			}
			if character == '\\' {
				escaped = true
			} else if character == '"' {
				inQuote = false
			}
			continue
		}
		if character == '"' {
			inQuote = true
			continue
		}
		if strings.HasPrefix(value[index:], close) {
			return index
		}
	}
	return -1
}

func parseTOMLDottedKey(value string) ([]string, bool) {
	parts := make([]string, 0, 2)
	for value != "" {
		value = strings.TrimLeft(value, " \t")
		if value == "" {
			break
		}
		var part string
		if value[0] == '"' {
			end := 1
			escaped := false
			for ; end < len(value); end++ {
				if escaped {
					escaped = false
					continue
				}
				if value[end] == '\\' {
					escaped = true
					continue
				}
				if value[end] == '"' {
					break
				}
			}
			if end >= len(value) {
				return nil, false
			}
			decoded, err := strconv.Unquote(value[:end+1])
			if err != nil {
				return nil, false
			}
			part = decoded
			value = value[end+1:]
		} else {
			end := 0
			for end < len(value) && value[end] != '.' && value[end] != ' ' && value[end] != '\t' {
				end++
			}
			if end == 0 {
				return nil, false
			}
			part = value[:end]
			value = value[end:]
		}
		if part == "" {
			return nil, false
		}
		parts = append(parts, part)
		value = strings.TrimLeft(value, " \t")
		if value == "" {
			break
		}
		if value[0] != '.' {
			return nil, false
		}
		value = value[1:]
	}
	return parts, len(parts) > 0
}

func indexTOMLDelimiter(value string, delimiter byte) int {
	inQuote := false
	escaped := false
	for index := 0; index < len(value); index++ {
		character := value[index]
		if inQuote {
			if escaped {
				escaped = false
				continue
			}
			if character == '\\' {
				escaped = true
			} else if character == '"' {
				inQuote = false
			}
			continue
		}
		if character == '"' {
			inQuote = true
			continue
		}
		if character == delimiter {
			return index
		}
	}
	return -1
}

func isManagedProviderPath(path, providerIDs []string) bool {
	if len(path) < 2 || path[0] != "model_providers" {
		return false
	}
	for _, id := range providerIDs {
		if path[1] == id {
			return true
		}
	}
	return false
}

func sortedStringKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
