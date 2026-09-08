package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// ModelCapabilities declares the features which are safe to advertise to
// Antigravity for a custom upstream model. Configured is deliberately kept
// separate from the booleans: older configs did not contain capability data,
// while a user may explicitly disable an otherwise common capability.
type ModelCapabilities struct {
	Configured              bool     `json:"configured,omitempty"`
	SupportsImages          bool     `json:"supportsImages,omitempty"`
	SupportsFiles           bool     `json:"supportsFiles,omitempty"`
	SupportsAudio           bool     `json:"supportsAudio,omitempty"`
	SupportsVideo           bool     `json:"supportsVideo,omitempty"`
	SupportsToolCalls       bool     `json:"supportsToolCalls,omitempty"`
	SupportsWebSearch       bool     `json:"supportsWebSearch,omitempty"`
	SupportsImageGeneration bool     `json:"supportsImageGeneration,omitempty"`
	SupportsThinking        bool     `json:"supportsThinking,omitempty"`
	SupportedMimeTypes      []string `json:"supportedMimeTypes,omitempty"`
}

// CommonMediaMimeTypes are formats Antigravity can attach in its native chat
// UI. A model is only told about formats that its saved capability profile
// enables, so the picker does not allow an attachment that the proxy will
// subsequently discard.
var CommonMediaMimeTypes = []string{
	"image/png", "image/jpeg", "image/webp", "image/gif", "image/svg+xml",
	"text/plain", "text/markdown", "text/html", "text/css", "text/xml", "text/csv",
	"application/json", "application/pdf", "application/x-javascript",
	"application/x-typescript", "application/x-python-code", "application/x-ipynb+json",
}

var audioMimeTypes = []string{"audio/mpeg", "audio/mp4", "audio/wav", "audio/webm", "audio/ogg"}
var videoMimeTypes = []string{"video/mp4", "video/webm", "video/quicktime"}

// DefaultCapabilities advertises only the features with a concrete conversion
// path. Audio and video are deliberately unavailable because OpenAI Chat,
// Responses and Anthropic Messages do not share a safe common attachment
// format. Hosted web search is exposed only for explicit OpenAI Responses.
// Image generation remains available to OpenAI-compatible Chat models because
// the proxy routes that operation through a separate Images API model.
func DefaultCapabilities(provider, modelName string) ModelCapabilities {
	return defaultCapabilities(provider, modelName, "chat_completions")
}

// DefaultCapabilitiesForAPIStyle is used when a discovered model already has
// an explicit upstream API style. A Chat-only endpoint must not be advertised
// as supporting Responses-only hosted tools.
func DefaultCapabilitiesForAPIStyle(provider, modelName, apiStyle string) ModelCapabilities {
	return defaultCapabilities(provider, modelName, apiStyle)
}

func defaultCapabilities(provider, modelName, apiStyle string) ModelCapabilities {
	name := strings.ToLower(strings.TrimSpace(modelName))
	nonChat := strings.Contains(name, "embedding") || strings.Contains(name, "tts") || strings.Contains(name, "whisper")
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		provider = "openai"
	}
	apiStyle = strings.ToLower(strings.TrimSpace(apiStyle))
	supportsHostedWebSearch := !nonChat && provider == "openai" && apiStyle == "responses"
	supportsDirectImageGeneration := !nonChat && apiStyle != "messages" && (provider == "openai" || provider == "grok" || provider == "custom")
	capabilities := ModelCapabilities{
		SupportsImages:          !nonChat,
		SupportsFiles:           !nonChat,
		SupportsToolCalls:       !nonChat,
		SupportsThinking:        !nonChat,
		SupportsWebSearch:       supportsHostedWebSearch,
		SupportsImageGeneration: supportsDirectImageGeneration,
	}
	capabilities.SupportedMimeTypes = capabilityMimeTypesForProtocol(capabilities, provider, apiStyle)
	return capabilities
}

func capabilityMimeTypes(capabilities ModelCapabilities) []string {
	var values []string
	if len(capabilities.SupportedMimeTypes) > 0 {
		values = capabilities.SupportedMimeTypes
	} else {
		if capabilities.SupportsImages || capabilities.SupportsFiles {
			values = append(values, CommonMediaMimeTypes...)
		}
		if capabilities.SupportsAudio {
			values = append(values, audioMimeTypes...)
		}
		if capabilities.SupportsVideo {
			values = append(values, videoMimeTypes...)
		}
	}
	normalized := normalizeMimeTypes(values)
	result := make([]string, 0, len(normalized))
	for _, mimeType := range normalized {
		if capabilityAllowsMimeType(capabilities, mimeType) {
			result = append(result, mimeType)
		}
	}
	return normalizeMimeTypes(result)
}

// capabilityMimeTypesForProtocol keeps Antigravity's attachment picker in
// lockstep with the converter that will actually handle the request. OpenAI
// Chat has no general PDF input contract, while explicit Responses and Claude
// Messages do. SVG is not advertised as a vision format because the supported
// upstream image inputs are raster images even though SVG has an image/* MIME.
func capabilityMimeTypesForProtocol(capabilities ModelCapabilities, provider, apiStyle string) []string {
	values := capabilityMimeTypes(capabilities)
	provider = strings.ToLower(strings.TrimSpace(provider))
	apiStyle = strings.ToLower(strings.TrimSpace(apiStyle))
	allowsPDF := capabilities.SupportsFiles && (provider == "anthropic" || apiStyle == "messages" || apiStyle == "responses")
	if allowsPDF {
		values = append(values, "application/pdf")
	}
	filtered := make([]string, 0, len(values))
	for _, mimeType := range normalizeMimeTypes(values) {
		if mimeType == "image/svg+xml" || (mimeType == "application/pdf" && !allowsPDF) {
			continue
		}
		filtered = append(filtered, mimeType)
	}
	return filtered
}

func capabilityAllowsMimeType(capabilities ModelCapabilities, mimeType string) bool {
	switch {
	case strings.HasPrefix(mimeType, "audio/"):
		return capabilities.SupportsAudio
	case strings.HasPrefix(mimeType, "video/"):
		return capabilities.SupportsVideo
	case strings.HasPrefix(mimeType, "image/"):
		return capabilities.SupportsImages
	default:
		return capabilities.SupportsFiles
	}
}

func normalizeMimeTypes(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" || !strings.Contains(value, "/") {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

// EffectiveCapabilities migrates old JSON lazily without mutating it on read.
func EffectiveCapabilities(model CustomModel) ModelCapabilities {
	capabilities := model.Capabilities
	if !capabilities.Configured {
		capabilities = DefaultCapabilitiesForAPIStyle(model.Provider, model.ExternalModelName, model.APIStyle)
	}
	// Legacy configs could opt into audio/video even though this proxy has no
	// lossless conversion for either. Never inject an attachment MIME type that
	// the runtime cannot reliably forward.
	capabilities.SupportsAudio = false
	capabilities.SupportsVideo = false
	if !capabilitySupportsHostedWebSearch(model) {
		capabilities.SupportsWebSearch = false
	}
	capabilities.SupportsImageGeneration = capabilitySupportsDirectImageGeneration(model)
	capabilities.SupportedMimeTypes = capabilityMimeTypesForProtocol(capabilities, model.Provider, model.APIStyle)
	return capabilities
}

func capabilitySupportsHostedWebSearch(model CustomModel) bool {
	provider := strings.ToLower(strings.TrimSpace(model.Provider))
	if provider == "" {
		provider = "openai"
	}
	style := strings.ToLower(strings.TrimSpace(model.APIStyle))
	return provider == "openai" && style == "responses"
}

func capabilitySupportsDirectImageGeneration(model CustomModel) bool {
	provider := strings.ToLower(strings.TrimSpace(model.Provider))
	if provider == "" {
		provider = "openai"
	}
	style := strings.ToLower(strings.TrimSpace(model.APIStyle))
	if style == "messages" {
		return false
	}
	name := strings.ToLower(strings.TrimSpace(model.ExternalModelName))
	if strings.Contains(name, "embedding") || strings.Contains(name, "tts") || strings.Contains(name, "whisper") {
		return false
	}
	return provider == "openai" || provider == "grok" || provider == "custom"
}

// CustomModel represents a third-party model configuration.
type CustomModel struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Description string `json:"description"`
	// Enabled is optional so configurations created before model-level toggles
	// remain active. A nil value therefore means enabled; only an explicit
	// false removes the model from Antigravity's injected picker and routing.
	Enabled  *bool  `json:"enabled,omitempty"`
	Provider string `json:"provider"` // "openai" | "anthropic" | "grok" | "custom"
	APIKey   string `json:"apiKey"`
	APIURL   string `json:"apiUrl"`
	// UpstreamName is a user-managed label for the shared upstream card. It is
	// presentation metadata only and never participates in request routing.
	UpstreamName string `json:"upstreamName,omitempty"`
	// EndpointMode is "auto" for a base domain/path that WF expands, or
	// "manual" to send requests to APIURL exactly as entered by the user.
	EndpointMode      string `json:"endpointMode,omitempty"`
	ExternalModelName string `json:"externalModelName"`
	// ReasoningEffort is always normalized against the verified profile for
	// Provider, APIStyle and ExternalModelName before the model is persisted.
	// "auto" means no provider-specific effort value is sent.
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
	// ThinkingEnabled is used only by profiles that expose a real thinking
	// switch (currently DeepSeek V4 and legacy Claude budget thinking). nil
	// preserves the provider default; false must never be represented by an
	// unsupported effort value.
	ThinkingEnabled *bool `json:"thinkingEnabled,omitempty"`
	// ReasoningBudgetTokens is only meaningful for legacy Claude profiles that
	// use thinking.type=enabled plus budget_tokens. Modern Claude models use
	// output_config.effort instead, so this value is cleared for them.
	ReasoningBudgetTokens int    `json:"reasoningBudgetTokens,omitempty"`
	APIStyle              string `json:"apiStyle,omitempty"` // "auto" | "chat_completions" | "responses" | "messages"
	// MessagePathMode controls Anthropic endpoint resolution. auto first uses
	// the standard /v1/messages route; compat selects /v1/chat/messages for
	// gateways that expose that legacy-compatible shape; manual preserves the
	// path supplied in APIURL.
	MessagePathMode string            `json:"messagePathMode,omitempty"`
	AuthMode        string            `json:"authMode,omitempty"` // "bearer" | "x_api_key" | "custom_header"
	AuthHeader      string            `json:"authHeader,omitempty"`
	Headers         map[string]string `json:"headers,omitempty"`
	// AccountIDs binds a model to one or more reusable upstream accounts. Empty
	// preserves legacy per-model API credentials for existing installations.
	AccountIDs   []string          `json:"accountIds,omitempty"`
	Capabilities ModelCapabilities `json:"capabilities,omitempty"`
	// AccountPoolLabel is derived from the currently saved account names every
	// time models are loaded. It is returned to the renderer and model picker,
	// but normalizeModelDisplayName clears it before any persistence operation.
	// Keeping the label separate from DisplayName ensures account renames appear
	// immediately and the real upstream model ID is never polluted by UI text.
	AccountPoolLabel string `json:"accountPoolLabel,omitempty"`
	// RuntimeOAuthUpstream and RuntimeChatGPTAccountID are copied only from the
	// account selected for an in-flight request. They are intentionally not
	// persisted in custom_models.json and can never expose an OAuth token.
	RuntimeOAuthUpstream    string `json:"-"`
	RuntimeChatGPTAccountID string `json:"-"`
}

// IsEnabled treats pre-toggle configurations as enabled. Keeping this
// migration rule at the storage boundary prevents an upgrade from making all
// existing custom models disappear from Antigravity.
func (m CustomModel) IsEnabled() bool {
	return m.Enabled == nil || *m.Enabled
}

// EnabledModels returns a stable copy of the models that may be advertised to
// and selected by Antigravity. Callers rendering management UI should use
// LoadModels so disabled entries remain visible and can be re-enabled.
func EnabledModels(models []CustomModel) []CustomModel {
	active := make([]CustomModel, 0, len(models))
	for _, model := range models {
		if model.IsEnabled() {
			active = append(active, model)
		}
	}
	return active
}

type modelsStore struct {
	Models []CustomModel `json:"models"`
}

var (
	mu         sync.RWMutex
	storageDir string
	modelsFile string
)

func Init(dir string) {
	storageDir = dir
	modelsFile = filepath.Join(dir, "custom_models.json")
	initAccountsFile(dir)
	_ = os.MkdirAll(dir, 0o700)
	_ = os.Chmod(dir, 0o700)
	_ = os.Chmod(modelsFile, 0o600)
}

func StorageDir() string { return storageDir }

func LoadModels() ([]CustomModel, error) {
	mu.RLock()
	models, err := loadModelsLocked()
	mu.RUnlock()
	if err != nil {
		return nil, err
	}
	return attachAccountPoolLabels(models), nil
}

// LoadEnabledModels is intentionally separate from LoadModels: the settings
// UI needs the complete catalog, whereas the proxy must never inject or route
// a model the user has unchecked in its upstream card.
func LoadEnabledModels() ([]CustomModel, error) {
	mu.RLock()
	models, err := loadModelsLocked()
	mu.RUnlock()
	if err != nil {
		return nil, err
	}
	return EnabledModels(attachAccountPoolLabels(models)), nil
}

// attachAccountPoolLabels runs only after the model lock has been released.
// MergeDiscoveredAccountModelsForCurrentAccount intentionally acquires the
// account lock before the model lock, so reversing that order here would make
// simultaneous account sync and model loading deadlock.
func attachAccountPoolLabels(models []CustomModel) []CustomModel {
	if len(models) == 0 {
		return models
	}
	accounts, err := LoadUpstreamAccounts()
	if err != nil || len(accounts) == 0 {
		return models
	}
	accountNames := make(map[string]string, len(accounts))
	for _, account := range accounts {
		id := strings.TrimSpace(account.ID)
		if id == "" {
			continue
		}
		name := strings.TrimSpace(account.Name)
		if name == "" {
			name = accountProviderLabel(account.Provider) + " 账户"
		}
		accountNames[id] = name
	}
	for index := range models {
		labels := make([]string, 0, len(models[index].AccountIDs))
		seen := make(map[string]struct{}, len(models[index].AccountIDs))
		for _, accountID := range normalizedAccountIDs(models[index].AccountIDs) {
			label := strings.TrimSpace(accountNames[accountID])
			if label == "" {
				continue
			}
			key := strings.ToLower(label)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			labels = append(labels, label)
		}
		switch len(labels) {
		case 0:
			models[index].AccountPoolLabel = ""
		case 1, 2:
			models[index].AccountPoolLabel = strings.Join(labels, " / ")
		default:
			models[index].AccountPoolLabel = strings.Join(labels[:2], " / ") + fmt.Sprintf(" 等%d个账户", len(labels))
		}
	}
	return models
}

// VisibleModelDisplayName is the only display-name source shared by XIASS Tools
// and Antigravity injection. Routing continues to use ExternalModelName.
func VisibleModelDisplayName(model CustomModel) string {
	base := strings.TrimSpace(model.DisplayName)
	if base == "" {
		base = strings.TrimSpace(model.ExternalModelName)
	}
	if base == "" {
		base = strings.TrimPrefix(strings.TrimSpace(model.Name), "models/")
	}
	// An account-backed model follows the live account-pool name so renames are
	// reflected immediately. Direct-credential models fall back to the saved
	// supplier-card label. Neither label is ever folded into DisplayName or the
	// real ExternalModelName used for routing.
	label := strings.TrimSpace(model.AccountPoolLabel)
	if label == "" {
		label = strings.TrimSpace(model.UpstreamName)
	}
	if label != "" {
		return base + " · " + label
	}
	return base
}

func loadModelsLocked() ([]CustomModel, error) {
	data, err := os.ReadFile(modelsFile)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var store modelsStore
	if err := json.Unmarshal(data, &store); err != nil {
		return nil, err
	}
	for i := range store.Models {
		store.Models[i] = normalizeModelDisplayName(store.Models[i])
	}
	return store.Models, nil
}

func SaveModels(models []CustomModel) error {
	mu.Lock()
	defer mu.Unlock()
	for i := range models {
		models[i] = normalizeModelDisplayName(models[i])
	}
	return saveModelsLocked(models)
}

func AddOrUpdateModel(m CustomModel) error {
	mu.Lock()
	defer mu.Unlock()
	m = normalizeModelDisplayName(m)
	models, err := loadModelsLocked()
	if err != nil {
		return err
	}
	for i, existing := range models {
		if existing.Name == m.Name {
			models[i] = m
			return saveModelsLocked(models)
		}
	}
	models = append(models, m)
	return saveModelsLocked(models)
}

// DiscoveredAccountModelMergeResult describes an account-pool import without
// exposing any account credential. Added counts new Antigravity models; Bound
// counts existing models that gained one or more account bindings; Unchanged
// counts models that were already bound to every requested account.
type DiscoveredAccountModelMergeResult struct {
	Added     int
	Bound     int
	Unchanged int
}

// ErrAccountSyncChanged is returned before any model write when the saved
// account was deleted or its connection route changed while its /models request
// was in flight. It intentionally does not include account metadata or
// credentials because it is displayed directly to the user.
var ErrAccountSyncChanged = errors.New("账户在同步期间已删除或连接配置发生变化，请重新同步")

// AccountSyncSnapshot captures only the route fields that make a discovered
// model list safe to attach to an account. It deliberately excludes API keys,
// refresh tokens, quota state, and scheduling metadata. A credential rotation
// can therefore complete during a model-list request without discarding a safe
// result, while an endpoint or protocol change cannot attach stale models.
type AccountSyncSnapshot struct {
	AccountID        string
	Provider         string
	Type             string
	APIURL           string
	EndpointMode     string
	APIStyle         string
	MessagePathMode  string
	AuthMode         string
	AuthHeader       string
	Headers          map[string]string
	OAuthUpstream    string
	ChatGPTAccountID string
}

func NewAccountSyncSnapshot(account UpstreamAccount) AccountSyncSnapshot {
	return AccountSyncSnapshot{
		AccountID:        strings.TrimSpace(account.ID),
		Provider:         normalizeAccountProvider(account.Provider),
		Type:             normalizeAccountType(account.Type),
		APIURL:           strings.TrimSpace(account.APIURL),
		EndpointMode:     normalizeEndpointMode(account.EndpointMode),
		APIStyle:         strings.ToLower(strings.TrimSpace(account.APIStyle)),
		MessagePathMode:  normalizeMessagePathMode(account.MessagePathMode),
		AuthMode:         strings.ToLower(strings.TrimSpace(account.AuthMode)),
		AuthHeader:       strings.TrimSpace(account.AuthHeader),
		Headers:          cloneStringMap(account.Headers),
		OAuthUpstream:    strings.ToLower(strings.TrimSpace(account.OAuth.Upstream)),
		ChatGPTAccountID: strings.TrimSpace(account.Identity.ChatGPTAccountID),
	}
}

func (snapshot AccountSyncSnapshot) matches(account UpstreamAccount) bool {
	current := NewAccountSyncSnapshot(account)
	return snapshot.AccountID != "" && snapshot.AccountID == current.AccountID &&
		snapshot.Provider == current.Provider && snapshot.Type == current.Type &&
		snapshot.APIURL == current.APIURL && snapshot.EndpointMode == current.EndpointMode &&
		snapshot.APIStyle == current.APIStyle && snapshot.MessagePathMode == current.MessagePathMode &&
		snapshot.AuthMode == current.AuthMode && snapshot.AuthHeader == current.AuthHeader &&
		snapshot.OAuthUpstream == current.OAuthUpstream && snapshot.ChatGPTAccountID == current.ChatGPTAccountID &&
		equalModelHeaders(snapshot.Headers, current.Headers)
}

func equalModelHeaders(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

// MergeDiscoveredAccountModels atomically adds discovered models to the local
// catalog or attaches their account pool to an existing equivalent model. It
// deliberately preserves the existing model's user-facing name, capabilities,
// direct credential, and prior account IDs. A sync from one account must never
// overwrite a model already configured for another account.
//
// An equivalent model has the same provider, upstream endpoint, external
// model identifier, and complete request-shape contract. In particular, two
// accounts must not share a model pool when their API surface, endpoint mode,
// or Anthropic message path differ: the router selects and serializes the
// request before a pooled account is acquired. Mixing those contracts could
// otherwise send an already-serialized request to the wrong endpoint.
//
// Manual endpoints remain exact because a trailing slash or query can be
// meaningful to a user-managed gateway.
func MergeDiscoveredAccountModels(candidates []CustomModel) (DiscoveredAccountModelMergeResult, error) {
	mu.Lock()
	defer mu.Unlock()
	return mergeDiscoveredAccountModelsLocked(candidates)
}

// MergeDiscoveredAccountModelsForCurrentAccount verifies the account snapshot
// and persists the model bindings while holding the account and model storage
// locks together. This prevents a slow discovery response from binding a
// deleted account ID or models from an endpoint the user has since replaced.
func MergeDiscoveredAccountModelsForCurrentAccount(snapshot AccountSyncSnapshot, candidates []CustomModel) (DiscoveredAccountModelMergeResult, error) {
	result := DiscoveredAccountModelMergeResult{}
	if strings.TrimSpace(snapshot.AccountID) == "" {
		return result, ErrAccountSyncChanged
	}
	accountsMu.Lock()
	defer accountsMu.Unlock()
	accounts, err := loadAccountsLocked()
	if err != nil {
		return result, err
	}
	var current *UpstreamAccount
	for index := range accounts {
		if accounts[index].ID == snapshot.AccountID {
			current = &accounts[index]
			break
		}
	}
	if current == nil || !snapshot.matches(*current) {
		return result, ErrAccountSyncChanged
	}
	for _, candidate := range candidates {
		bound := false
		for _, accountID := range normalizedAccountIDs(candidate.AccountIDs) {
			if accountID == snapshot.AccountID {
				bound = true
				break
			}
		}
		if !bound {
			return result, ErrAccountSyncChanged
		}
	}

	mu.Lock()
	defer mu.Unlock()
	return mergeDiscoveredAccountModelsLocked(candidates)
}

func mergeDiscoveredAccountModelsLocked(candidates []CustomModel) (DiscoveredAccountModelMergeResult, error) {
	result := DiscoveredAccountModelMergeResult{}
	if len(candidates) == 0 {
		return result, nil
	}
	models, err := loadModelsLocked()
	if err != nil {
		return result, err
	}

	changed := false
	for _, candidate := range candidates {
		candidate = normalizeModelDisplayName(candidate)
		if len(candidate.AccountIDs) == 0 {
			continue
		}
		index := equivalentDiscoveredAccountModelIndex(models, candidate)
		if index < 0 {
			// The original discovery name is derived from only provider, endpoint,
			// and upstream model ID. Once route contracts are deliberately kept
			// separate, those values can legitimately collide. Give the new model a
			// stable, route-aware internal name so placeholder allocation and request
			// routing never collapse two incompatible configurations into one.
			candidate = uniqueDiscoveredAccountModelName(models, candidate)
			models = append(models, candidate)
			result.Added++
			changed = true
			continue
		}

		mergedIDs, didBind := mergeModelAccountIDs(models[index].AccountIDs, candidate.AccountIDs)
		if !didBind {
			result.Unchanged++
			continue
		}
		models[index].AccountIDs = mergedIDs
		models[index] = normalizeModelDisplayName(models[index])
		result.Bound++
		changed = true
	}

	if !changed {
		return result, nil
	}
	return result, saveModelsLocked(models)
}

func equivalentDiscoveredAccountModelIndex(models []CustomModel, candidate CustomModel) int {
	candidate = normalizeModelDisplayName(candidate)
	for index, existing := range models {
		existing = normalizeModelDisplayName(existing)
		if !strings.EqualFold(existing.Provider, candidate.Provider) ||
			!strings.EqualFold(discoveredExternalModelID(existing.ExternalModelName), discoveredExternalModelID(candidate.ExternalModelName)) ||
			!sameDiscoveredModelRouteContract(existing, candidate) {
			continue
		}
		return index
	}
	return -1
}

// sameDiscoveredModelRouteContract reports whether two models can safely
// share a single account pool without changing the request protocol after the
// proxy has built the upstream body.
func sameDiscoveredModelRouteContract(existing, candidate CustomModel) bool {
	return normalizedDiscoveredModelAPIStyle(existing.APIStyle) == normalizedDiscoveredModelAPIStyle(candidate.APIStyle) &&
		normalizeEndpointMode(existing.EndpointMode) == normalizeEndpointMode(candidate.EndpointMode) &&
		normalizeMessagePathMode(existing.MessagePathMode) == normalizeMessagePathMode(candidate.MessagePathMode) &&
		sameDiscoveredModelEndpoint(existing, candidate)
}

// normalizedDiscoveredModelAPIStyle mirrors upstream.EffectiveAPIStyle's
// legacy fallback without importing the upstream package (which depends on
// storage). An empty/unknown saved style was historically Chat Completions;
// it must not silently pool with automatic or Responses models.
func normalizedDiscoveredModelAPIStyle(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "auto", "chat_completions", "responses", "messages":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "chat_completions"
	}
}

func discoveredExternalModelID(value string) string {
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(value), "models/"))
}

func sameDiscoveredModelEndpoint(existing, candidate CustomModel) bool {
	existingURL := strings.TrimSpace(existing.APIURL)
	candidateURL := strings.TrimSpace(candidate.APIURL)
	if normalizeEndpointMode(existing.EndpointMode) == "manual" || normalizeEndpointMode(candidate.EndpointMode) == "manual" {
		return existingURL == candidateURL
	}
	return strings.TrimRight(existingURL, "/") == strings.TrimRight(candidateURL, "/")
}

func uniqueDiscoveredAccountModelName(models []CustomModel, candidate CustomModel) CustomModel {
	baseName := strings.TrimSpace(candidate.Name)
	if baseName == "" || !discoveredModelNameInUse(models, baseName) {
		return candidate
	}

	routeSuffix := strings.Trim(modelNameUnsafe.ReplaceAllString(strings.ToLower(strings.Join([]string{
		normalizedDiscoveredModelAPIStyle(candidate.APIStyle),
		normalizeEndpointMode(candidate.EndpointMode),
		normalizeMessagePathMode(candidate.MessagePathMode),
	}, "-")), "-"), "-")
	if routeSuffix == "" {
		routeSuffix = "route"
	}
	baseName += "-" + routeSuffix
	name := baseName
	for suffix := 2; discoveredModelNameInUse(models, name); suffix++ {
		name = fmt.Sprintf("%s-%d", baseName, suffix)
	}
	candidate.Name = name
	return candidate
}

func discoveredModelNameInUse(models []CustomModel, name string) bool {
	for _, model := range models {
		if strings.EqualFold(strings.TrimSpace(model.Name), name) {
			return true
		}
	}
	return false
}

func mergeModelAccountIDs(existing, incoming []string) ([]string, bool) {
	merged := normalizedAccountIDs(existing)
	seen := make(map[string]struct{}, len(merged))
	for _, id := range merged {
		seen[id] = struct{}{}
	}
	changed := false
	for _, id := range normalizedAccountIDs(incoming) {
		if _, found := seen[id]; found {
			continue
		}
		seen[id] = struct{}{}
		merged = append(merged, id)
		changed = true
	}
	return merged, changed
}

func normalizeModelDisplayName(model CustomModel) CustomModel {
	// This field is always recalculated from upstream_accounts.json after the
	// model lock is released. Never let a stale/forged derived label reach disk.
	model.AccountPoolLabel = ""
	model.Provider = strings.ToLower(strings.TrimSpace(model.Provider))
	if model.Provider == "" {
		model.Provider = "openai"
	}
	model.APIStyle = strings.ToLower(strings.TrimSpace(model.APIStyle))
	model.EndpointMode = normalizeEndpointMode(model.EndpointMode)
	model.MessagePathMode = normalizeMessagePathMode(model.MessagePathMode)
	model.AuthMode = strings.ToLower(strings.TrimSpace(model.AuthMode))
	model.AuthHeader = strings.TrimSpace(model.AuthHeader)
	model.UpstreamName = strings.TrimSpace(model.UpstreamName)
	model.ExternalModelName = strings.TrimSpace(model.ExternalModelName)
	model.Name = strings.TrimSpace(model.Name)
	if strings.TrimSpace(model.DisplayName) == "" {
		model.DisplayName = strings.TrimSpace(model.ExternalModelName)
	}
	model.DisplayName = strings.TrimSpace(model.DisplayName)
	model.AccountIDs = normalizedAccountIDs(model.AccountIDs)
	model.Capabilities.SupportedMimeTypes = capabilityMimeTypes(model.Capabilities)
	return NormalizeModelReasoning(model)
}

var modelNameUnsafe = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// NewDiscoveredModel creates a collision-resistant internal name while keeping
// the user-facing name equal to the upstream model id by default.
func NewDiscoveredModel(provider, apiURL, apiKey, externalModelName string) CustomModel {
	provider = strings.ToLower(strings.TrimSpace(provider))
	externalModelName = strings.TrimSpace(strings.TrimPrefix(externalModelName, "models/"))
	namePart := strings.Trim(modelNameUnsafe.ReplaceAllString(strings.ToLower(externalModelName), "-"), "-")
	if namePart == "" {
		namePart = "model"
	}
	endpointPart := strings.Trim(modelNameUnsafe.ReplaceAllString(strings.ToLower(strings.TrimSpace(apiURL)), "-"), "-")
	if len(endpointPart) > 24 {
		endpointPart = endpointPart[len(endpointPart)-24:]
	}
	if endpointPart == "" {
		endpointPart = "upstream"
	}
	enabled := true
	return normalizeModelDisplayName(CustomModel{
		Name:              fmt.Sprintf("models/%s-%s-%s", provider, namePart, endpointPart),
		DisplayName:       externalModelName,
		Enabled:           &enabled,
		Provider:          provider,
		APIKey:            apiKey,
		APIURL:            apiURL,
		ExternalModelName: externalModelName,
		APIStyle:          "chat_completions",
		EndpointMode:      "auto",
		MessagePathMode:   "auto",
		AuthMode:          "bearer",
		Capabilities:      DefaultCapabilities(provider, externalModelName),
	})
}

func DeleteModel(name string) error {
	mu.Lock()
	defer mu.Unlock()
	models, err := loadModelsLocked()
	if err != nil {
		return err
	}
	out := models[:0]
	for _, m := range models {
		if m.Name != name {
			out = append(out, m)
		}
	}
	return saveModelsLocked(out)
}

func saveModelsLocked(models []CustomModel) error {
	data, err := json.MarshalIndent(modelsStore{Models: models}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(modelsFile), 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(modelsFile), ".custom-models-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(append(data, '\n')); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return replaceStorageFile(tempPath, modelsFile)
}
