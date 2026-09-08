package codexconfig

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// Inspect reads config.toml without mutating it. Provider secrets and header
// values are intentionally redacted; only their presence and header names are
// reported to the UI.
func (m *Manager) Inspect() (ConfigSnapshot, error) {
	if err := m.validatePaths(); err != nil {
		return ConfigSnapshot{}, err
	}
	data, exists, mode, err := readRegularFile(m.ConfigPath)
	location := ConfigLocation{CodexHome: m.CodexHome, ConfigPath: m.ConfigPath, Exists: exists}
	if err != nil {
		return ConfigSnapshot{Location: location}, err
	}
	if !exists {
		return ConfigSnapshot{Location: location, Valid: true, Context: DefaultContextSettings()}, nil
	}
	var root map[string]any
	if err := toml.Unmarshal(data, &root); err != nil {
		return ConfigSnapshot{Location: location, SHA256: sha256Hex(data), Mode: mode, Valid: false}, fmt.Errorf("parse config.toml: %w", err)
	}
	context, err := readContextSettingsFromRoot(root)
	if err != nil {
		return ConfigSnapshot{Location: location, SHA256: sha256Hex(data), Mode: mode, Valid: false}, fmt.Errorf("read config context settings: %w", err)
	}
	snapshot := ConfigSnapshot{
		Location:      location,
		SHA256:        sha256Hex(data),
		Mode:          mode,
		Valid:         true,
		ModelProvider: stringValue(root["model_provider"]),
		Model:         stringValue(root["model"]),
		ReviewModel:   stringValue(root["review_model"]),
		WebSearch:     webSearchValue(root["web_search"]),
		Context:       context,
		Providers:     providersFromRoot(root),
	}
	// A syntactically valid TOML file is not enough to advertise XIASS Tools
	// as configured. In particular, an arbitrary or half-written
	// [model_providers.xiass_tools] entry must not turn the UI/agent status
	// green merely because model_provider names it. This check never returns
	// any value from the provider; the issue is a fixed redacted enum.
	snapshot.ManagedProviderVerified, snapshot.ManagedProviderIssue = verifyActiveManagedProvider(root)
	// Migration eligibility is intentionally a separate, redacted proof. The
	// renderer must not infer eligibility by inspecting arbitrary Provider
	// records, and an unsupported/ambiguous TOML form must not become a button
	// merely because it happens to contain a legacy-looking ID.
	if _, plan, migrationErr := planLegacyProviderMigration(data, true); migrationErr == nil {
		snapshot.LegacyProviderMigration = plan.status
	}
	snapshot.ConfiguredModels = configuredModels(snapshot.Model, snapshot.ReviewModel)
	return snapshot, nil
}

// webSearchValue is a redacted, user-configurable top-level setting. Keep its
// observable mode in the snapshot so reopening the Codex modal and saving an
// unrelated field cannot silently turn cached or disabled search back to live.
func webSearchValue(value any) string {
	mode := strings.ToLower(strings.TrimSpace(stringValue(value)))
	switch mode {
	case "live", "cached", "disabled":
		return mode
	case "off":
		// Codex accepts this legacy spelling. Present the canonical UI value
		// without changing its disabled semantics on the next explicit save.
		return "disabled"
	default:
		return ""
	}
}

// ReadContextSettings reads only the two first-party-managed context values.
// Missing config values use Codex-compatible defaults.
func (m *Manager) ReadContextSettings() (ContextSettings, error) {
	if err := m.validatePaths(); err != nil {
		return DefaultContextSettings(), err
	}
	data, exists, _, err := readRegularFile(m.ConfigPath)
	if err != nil {
		return DefaultContextSettings(), err
	}
	if !exists || len(strings.TrimSpace(string(data))) == 0 {
		return DefaultContextSettings(), nil
	}
	var root map[string]any
	if err := toml.Unmarshal(data, &root); err != nil {
		return DefaultContextSettings(), fmt.Errorf("read config.toml: %w", err)
	}
	return readContextSettingsFromRoot(root)
}

// Verify validates the on-disk TOML and returns its redacted snapshot. It is a
// useful guard after a UI operation or before presenting an existing config.
func (m *Manager) Verify() (ConfigSnapshot, error) {
	snapshot, err := m.Inspect()
	if err != nil {
		return snapshot, err
	}
	if !snapshot.Valid {
		return snapshot, errors.New("config.toml is invalid")
	}
	return snapshot, nil
}

func providersFromRoot(root map[string]any) []Provider {
	providersRoot, ok := mapValue(root["model_providers"])
	if !ok {
		return nil
	}
	providers := make([]Provider, 0, len(providersRoot))
	for _, id := range sortedStringKeys(providersRoot) {
		raw, ok := mapValue(providersRoot[id])
		if !ok {
			continue
		}
		provider := Provider{
			ID:                    id,
			Name:                  stringValue(raw["name"]),
			BaseURL:               stringValue(raw["base_url"]),
			WireAPI:               stringValue(raw["wire_api"]),
			RequiresOpenAIAuth:    boolValue(raw["requires_openai_auth"]),
			SupportsWebSockets:    boolValue(raw["supports_websockets"]),
			HasExperimentalBearer: strings.TrimSpace(stringValue(raw["experimental_bearer_token"])) != "",
		}
		if headers, ok := mapValue(raw["http_headers"]); ok {
			provider.HeaderNames = sortedStringKeys(headers)
		}
		providers = append(providers, provider)
	}
	return providers
}

func configuredModels(model, review string) []string {
	set := map[string]struct{}{}
	for _, candidate := range []string{strings.TrimSpace(model), strings.TrimSpace(review)} {
		if candidate != "" {
			set[candidate] = struct{}{}
		}
	}
	models := make([]string, 0, len(set))
	for model := range set {
		models = append(models, model)
	}
	sort.Strings(models)
	return models
}

func boolValue(value any) bool {
	parsed, _ := value.(bool)
	return parsed
}

// verifyActiveManagedProvider validates the exact non-secret semantics of the
// active provider XIASS Tools writes. It is intentionally stricter than TOML
// parsing: only an active xiass_tools provider with the responses transport,
// a non-empty bearer token, canonical base URL, required actor header, safe
// boolean flags, and all managed top-level settings earns the verified state.
//
// The function must not return wrapped errors. Config values may include a
// credential or proprietary endpoint, while ManagedProviderIssue is exposed
// to the renderer and diagnostics.
func verifyActiveManagedProvider(root map[string]any) (bool, ManagedProviderIssue) {
	if stringValue(root["model_provider"]) != DefaultProviderID {
		return false, ManagedProviderIssueInactive
	}
	if !validManagedText(root["model"]) {
		return false, ManagedProviderIssueModel
	}
	if !validManagedText(root["review_model"]) {
		return false, ManagedProviderIssueReviewModel
	}
	if !validManagedWebSearch(root["web_search"]) {
		return false, ManagedProviderIssueWebSearch
	}
	if !validManagedContext(root) {
		return false, ManagedProviderIssueContext
	}

	providers, ok := mapValue(root["model_providers"])
	if !ok {
		return false, ManagedProviderIssueProviderMissing
	}
	rawProvider, present := providers[DefaultProviderID]
	if !present {
		return false, ManagedProviderIssueProviderMissing
	}
	provider, ok := mapValue(rawProvider)
	if !ok {
		return false, ManagedProviderIssueProviderMalformed
	}
	if !validManagedText(provider["name"]) {
		return false, ManagedProviderIssueProviderMalformed
	}
	baseURL, ok := provider["base_url"].(string)
	if !ok || strings.TrimSpace(baseURL) == "" {
		return false, ManagedProviderIssueBaseURL
	}
	canonicalBaseURL, err := NormalizeBaseURL(baseURL)
	if err != nil || canonicalBaseURL != baseURL {
		return false, ManagedProviderIssueBaseURL
	}
	if provider["wire_api"] != DefaultWireAPI {
		return false, ManagedProviderIssueWireAPI
	}
	bearer, ok := provider["experimental_bearer_token"].(string)
	if !ok || strings.TrimSpace(bearer) == "" || validateShortText(bearer, 8192, "bearer token") != nil {
		return false, ManagedProviderIssueBearer
	}
	if requiresAuth, ok := provider["requires_openai_auth"].(bool); !ok || requiresAuth {
		return false, ManagedProviderIssueFlags
	}
	if supportsWebSockets, ok := provider["supports_websockets"].(bool); !ok || supportsWebSockets {
		return false, ManagedProviderIssueFlags
	}
	headers, ok := mapValue(provider["http_headers"])
	if !ok || len(headers) != 1 {
		return false, ManagedProviderIssueHeaders
	}
	headerActorAuthorization, ok := headers["x-openai-actor-authorization"].(string)
	if !ok || headerActorAuthorization != actorAuthorization(canonicalBaseURL) {
		return false, ManagedProviderIssueHeaders
	}
	return true, ManagedProviderIssueNone
}

func validManagedText(value any) bool {
	text, ok := value.(string)
	return ok && strings.TrimSpace(text) != "" && validateShortText(text, 200, "managed value") == nil
}

func validManagedWebSearch(value any) bool {
	mode, ok := value.(string)
	if !ok {
		return false
	}
	switch mode {
	case "live", "cached", "disabled", "off":
		return true
	default:
		return false
	}
}

func validManagedContext(root map[string]any) bool {
	window, windowOK := integerValue(root["model_context_window"])
	compactLimit, compactOK := integerValue(root["model_auto_compact_token_limit"])
	if !windowOK || !compactOK {
		return false
	}
	normalized, err := NormalizeContextSettings(ContextSettings{
		ModelContextWindow:         window,
		ModelAutoCompactTokenLimit: compactLimit,
	})
	return err == nil && normalized.ModelContextWindow == window && normalized.ModelAutoCompactTokenLimit == compactLimit
}
