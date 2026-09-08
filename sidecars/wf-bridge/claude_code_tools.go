package main

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"antigravity-wf-assistant/internal/claudeconfig"
	"antigravity-wf-assistant/internal/storage"
)

// ClaudeCodeApplyInput is an inbound-only Wails DTO. Credential and AuthToken
// intentionally exist only long enough to invoke Manager.Apply and are never
// stored on App or returned by any bridge method. AuthToken remains a legacy
// input alias so an already-open older frontend can finish a safe save during
// an in-place upgrade.
type ClaudeCodeApplyInput struct {
	BaseURL                     string `json:"baseUrl"`
	CredentialMode              string `json:"credentialMode"`
	Credential                  string `json:"credential"`
	AuthToken                   string `json:"authToken"`
	APIKeyHelper                string `json:"apiKeyHelper"`
	EnableGatewayModelDiscovery bool   `json:"enableGatewayModelDiscovery"`
	Model                       string `json:"model"`
}

// ClaudeCodeGatewayRequestInput exists only for an explicitly initiated
// one-shot model discovery or Claude Messages connection test. It is never
// populated from settings.json, so XIASS Tools never reads saved credentials.
type ClaudeCodeGatewayRequestInput struct {
	BaseURL        string `json:"baseUrl"`
	CredentialMode string `json:"credentialMode"`
	Credential     string `json:"credential"`
	AuthToken      string `json:"authToken"`
	Model          string `json:"model"`
}

// ClaudeCodeAccountCandidate is a deliberately redacted, reusable upstream
// account. It lets a person apply one of their existing Claude-compatible
// XIASS Tools accounts to Claude Code without moving that account's credential
// through the renderer. API URLs, headers, tokens and OAuth material stay
// native-only.
type ClaudeCodeAccountCandidate struct {
	ID             string                            `json:"id"`
	Label          string                            `json:"label"`
	CredentialMode string                            `json:"credentialMode"`
	Models         []ClaudeCodeAccountCandidateModel `json:"models"`
}

// ClaudeCodeAccountCandidateModel is public model-selection metadata from an
// already account-bound Antigravity model. It is never a model test result and
// never includes route or credential data.
type ClaudeCodeAccountCandidateModel struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
}

// ClaudeCodeAccountCandidatesStatus is safe for the renderer. Unsupported or
// paused accounts are omitted rather than exposed with internal reasons that
// could reveal a private API route or header setup.
type ClaudeCodeAccountCandidatesStatus struct {
	OK         bool                         `json:"ok"`
	Message    string                       `json:"message"`
	Candidates []ClaudeCodeAccountCandidate `json:"candidates"`
}

// ClaudeCodeApplyAccountInput contains only an opaque local account ID and
// the user-selected public model ID. The account credential is looked up and
// consumed entirely on the native side.
type ClaudeCodeApplyAccountInput struct {
	AccountID                   string `json:"accountId"`
	Model                       string `json:"model"`
	EnableGatewayModelDiscovery bool   `json:"enableGatewayModelDiscovery"`
}

const (
	embeddedClaudeCodeCandidateLimit       = 24
	embeddedClaudeCodeCandidateModelLimit  = 24
	embeddedClaudeCodeAccountIDLimit       = 128
	embeddedClaudeCodeModelIDLimit         = 256
	embeddedClaudeCodeLabelLimit           = 160
	embeddedClaudeCodeAppliedMessage       = "已由 XIASS Tools 应用所选 Claude Code 账户。"
	embeddedClaudeCodeCurrentMarkerWarning = "已应用所选 Claude Code 账户；当前账户标记未保存，不影响 Claude Code 使用。"
	embeddedClaudeCodeFallbackApplyMessage = "已将所选 XIASS Tools Claude 账户和模型安全应用到 Claude Code。"
)

// ClaudeCodeConfigurationStatus contains only the manager's redacted
// projection. In particular, it never serializes ANTHROPIC_AUTH_TOKEN or any
// other Claude Code environment value.
type ClaudeCodeConfigurationStatus struct {
	OK                  bool                            `json:"ok"`
	Message             string                          `json:"message"`
	Snapshot            ClaudeCodeConfigurationSnapshot `json:"snapshot"`
	Backups             []claudeconfig.BackupInfo       `json:"backups"`
	LegacyBackups       []claudeconfig.LegacyBackupInfo `json:"legacyBackups"`
	LegacyBackupWarning string                          `json:"legacyBackupWarning,omitempty"`
}

// ClaudeCodeConfigurationSnapshot is the renderer-safe projection of a
// claudeconfig.Snapshot. The config directory, settings file path, checksum,
// and file mode remain native-only and are never sent to Vue.
type ClaudeCodeConfigurationSnapshot struct {
	Exists                       bool   `json:"exists"`
	Valid                        bool   `json:"valid"`
	Model                        string `json:"model,omitempty"`
	BaseURL                      string `json:"baseUrl,omitempty"`
	CredentialMode               string `json:"credentialMode,omitempty"`
	CredentialConfigured         bool   `json:"credentialConfigured"`
	AuthTokenConfigured          bool   `json:"authTokenConfigured"`
	APIKeyHelperConfigured       bool   `json:"apiKeyHelperConfigured"`
	GatewayModelDiscoveryEnabled bool   `json:"gatewayModelDiscoveryEnabled"`
	GatewayModelDiscoveryBlocked bool   `json:"gatewayModelDiscoveryBlocked"`
	Managed                      bool   `json:"managed"`
}

// ClaudeCodeGatewayModelDiscoveryStatus is safe to render: model IDs and
// display names are public gateway metadata, while request credentials, URL
// details, headers, and raw response content never cross the bridge.
type ClaudeCodeGatewayModelDiscoveryStatus struct {
	OK         bool                        `json:"ok"`
	Message    string                      `json:"message"`
	Models     []claudeconfig.GatewayModel `json:"models"`
	HTTPStatus int                         `json:"httpStatus"`
	DurationMS int64                       `json:"durationMs"`
}

// ClaudeCodeGatewayTestStatus describes one direct, minimal Anthropic
// Messages request without returning its response body or prompt content.
type ClaudeCodeGatewayTestStatus struct {
	OK         bool   `json:"ok"`
	Message    string `json:"message"`
	HTTPStatus int    `json:"httpStatus"`
	DurationMS int64  `json:"durationMs"`
}

func (a *App) claudeCodeManager() (*claudeconfig.Manager, error) {
	return claudeconfig.NewDefaultManager()
}

// GetClaudeCodeConfiguration performs a read-only inspection of the one
// supported Claude Code user settings file. It does not inspect accounts,
// OAuth data, sessions, projects, MCP files, or other Claude paths.
func (a *App) GetClaudeCodeConfiguration() ClaudeCodeConfigurationStatus {
	manager, err := a.claudeCodeManager()
	if err != nil {
		return ClaudeCodeConfigurationStatus{
			OK:            false,
			Message:       "无法识别 Claude Code 用户设置位置。",
			Backups:       emptyClaudeCodeBackups(),
			LegacyBackups: emptyClaudeCodeLegacyBackups(),
		}
	}

	snapshot, inspectErr := manager.Inspect()
	backups, backupErr := manager.ListBackups()
	legacyBackups, legacyErr := manager.ListLegacyBackups()
	status := ClaudeCodeConfigurationStatus{
		Snapshot:      claudeCodeRendererSnapshot(snapshot),
		Backups:       nonNilClaudeCodeBackups(backups),
		LegacyBackups: nonNilClaudeCodeLegacyBackups(legacyBackups),
	}
	if legacyErr != nil {
		status.LegacyBackups = emptyClaudeCodeLegacyBackups()
		status.LegacyBackupWarning = "检测到旧版 Claude Code 设置备份目录异常，已跳过旧备份；当前用户设置未被修改。"
	}
	if inspectErr != nil {
		status.Message = "无法读取 Claude Code 用户 settings.json。请确认它是有效的普通 JSON 文件。"
		return status
	}
	if backupErr != nil {
		status.Message = "Claude Code 用户设置可读取，但备份目录无法安全验证。"
		return status
	}
	status.OK = true
	status.Message = "已读取 Claude Code 用户设置。"
	return status
}

// ApplyClaudeCodeConfiguration writes only env.ANTHROPIC_BASE_URL, one
// documented credential mode, gateway discovery preference, and model through
// the transaction-safe manager. Credentials are request-local and never placed
// on App, logs, events, or a returned status object.
func (a *App) ApplyClaudeCodeConfiguration(input ClaudeCodeApplyInput) ClaudeCodeConfigurationStatus {
	manager, err := a.claudeCodeManager()
	if err != nil {
		return ClaudeCodeConfigurationStatus{
			OK:            false,
			Message:       "无法识别 Claude Code 用户设置位置。",
			Backups:       emptyClaudeCodeBackups(),
			LegacyBackups: emptyClaudeCodeLegacyBackups(),
		}
	}
	config := claudeconfig.ApplyConfig{
		BaseURL:                     input.BaseURL,
		CredentialMode:              claudeCodeCredentialMode(input.CredentialMode),
		Credential:                  claudeCodeCredential(input.Credential, input.AuthToken),
		APIKeyHelper:                input.APIKeyHelper,
		EnableGatewayModelDiscovery: input.EnableGatewayModelDiscovery,
		Model:                       input.Model,
	}
	// Drop references as soon as the native call finishes. Go strings are
	// immutable, so this is reference minimization rather than an in-place wipe.
	defer func() {
		input.Credential = ""
		input.AuthToken = ""
		input.APIKeyHelper = ""
		config.Credential = ""
		config.AuthToken = ""
		config.APIKeyHelper = ""
	}()

	return a.applyClaudeCodeConfiguration(manager, config)
}

// GetClaudeCodeAccountCandidates lists only locally saved accounts that can
// be mapped losslessly to Claude Code's documented settings.json fields. In
// particular, custom headers, manual endpoint paths, paused accounts and
// non-Messages protocols are not offered because silently dropping any of
// those settings could produce a different request from the one the user
// configured in the account pool.
func (a *App) GetClaudeCodeAccountCandidates() ClaudeCodeAccountCandidatesStatus {
	if a != nil && a.embeddedMode {
		return a.getEmbeddedClaudeCodeAccountCandidates()
	}
	accounts, err := storage.LoadUpstreamAccounts()
	if err != nil {
		return ClaudeCodeAccountCandidatesStatus{Candidates: emptyClaudeCodeAccountCandidates(), Message: "无法读取本机账户池。"}
	}
	models, modelErr := storage.LoadModels()
	if modelErr != nil {
		// An account remains usable even when the optional Antigravity model
		// catalog is unavailable. The user can still enter a known model ID.
		models = nil
	}
	candidates := make([]ClaudeCodeAccountCandidate, 0, len(accounts))
	for _, account := range accounts {
		if !claudeCodeAccountReusable(account) {
			continue
		}
		candidates = append(candidates, ClaudeCodeAccountCandidate{
			ID:             account.ID,
			Label:          claudeCodeAccountLabel(account),
			CredentialMode: claudeCodeCredentialModeLabel(account.AuthMode),
			Models:         claudeCodeCandidateModels(account.ID, models),
		})
	}
	sort.Slice(candidates, func(left, right int) bool {
		return strings.ToLower(candidates[left].Label+"\x00"+candidates[left].ID) < strings.ToLower(candidates[right].Label+"\x00"+candidates[right].ID)
	})
	if len(candidates) == 0 {
		return ClaudeCodeAccountCandidatesStatus{OK: true, Candidates: emptyClaudeCodeAccountCandidates(), Message: "没有可直接用于 Claude Code 的已启用账户。"}
	}
	message := "已读取可复用的 Claude Code 兼容账户。"
	if modelErr != nil {
		message = "已读取可复用账户；本机模型目录暂不可用，可手动填写模型 ID。"
	}
	return ClaudeCodeAccountCandidatesStatus{OK: true, Candidates: candidates, Message: message}
}

// getEmbeddedClaudeCodeAccountCandidates intentionally does not inspect the
// legacy helper account store. The parent Tauri process owns the active Claude
// vault, returns a small redacted projection through the authenticated bridge,
// and the helper validates that projection again before Vue receives it.
func (a *App) getEmbeddedClaudeCodeAccountCandidates() ClaudeCodeAccountCandidatesStatus {
	result, err := a.executeNativeAction(nativeActionRequest{Kind: nativeActionClaudeCodeCandidates})
	if err != nil || !result.OK || result.Canceled || strings.TrimSpace(result.Value) == "" {
		return ClaudeCodeAccountCandidatesStatus{
			Candidates: emptyClaudeCodeAccountCandidates(),
			Message:    "无法读取 XIASS Tools Claude 账户。请确认主应用仍在运行后重试。",
		}
	}
	var payload ClaudeCodeAccountCandidatesStatus
	if err := json.Unmarshal([]byte(result.Value), &payload); err != nil || !payload.OK {
		return ClaudeCodeAccountCandidatesStatus{
			Candidates: emptyClaudeCodeAccountCandidates(),
			Message:    "XIASS Tools Claude 账户响应无效。",
		}
	}
	candidates := sanitizeEmbeddedClaudeCodeCandidates(payload.Candidates)
	message := "已读取 XIASS Tools 中可用于 Claude Code 的账户。"
	if len(candidates) == 0 {
		message = "没有可直接用于 Claude Code 的 XIASS Tools 账户。"
	}
	return ClaudeCodeAccountCandidatesStatus{OK: true, Candidates: candidates, Message: message}
}

// ApplyClaudeCodeConfigurationFromAccount is an explicit transfer of one
// user-selected, compatible XIASS Tools upstream account into the narrowly
// documented Claude Code settings surface. It never reads a third-party
// account/session file and never returns the source credential to Vue.
func (a *App) ApplyClaudeCodeConfigurationFromAccount(input ClaudeCodeApplyAccountInput) ClaudeCodeConfigurationStatus {
	if a != nil && a.embeddedMode {
		return a.applyEmbeddedClaudeCodeConfigurationFromAccount(input)
	}
	manager, err := a.claudeCodeManager()
	if err != nil {
		return ClaudeCodeConfigurationUnavailable()
	}
	account, err := storage.GetUpstreamAccount(strings.TrimSpace(input.AccountID))
	input.AccountID = ""
	if err != nil {
		return claudeCodeConfigurationAfterError(manager, "无法使用该账户配置 Claude Code。请刷新账户池后重新选择。")
	}
	config, err := claudeCodeApplyConfigFromAccount(account, input.Model, input.EnableGatewayModelDiscovery)
	input.Model = ""
	if err != nil {
		return claudeCodeConfigurationAfterError(manager, "该账户无法无损应用到 Claude Code。请使用兼容的 Claude Messages 账户，或手动填写配置。")
	}
	defer clearClaudeCodeApplyConfig(&config)
	return a.applyClaudeCodeConfiguration(manager, config)
}

// applyEmbeddedClaudeCodeConfigurationFromAccount keeps credentials inside the
// Tauri Claude vault. The helper receives only the already-selected opaque ID
// and model, transactionally stages the user-visible model field, then asks
// the host to inject the account. If the host rejects the account selection,
// the staged model write is restored from its verified backup.
func (a *App) applyEmbeddedClaudeCodeConfigurationFromAccount(input ClaudeCodeApplyAccountInput) ClaudeCodeConfigurationStatus {
	manager, err := a.claudeCodeManager()
	if err != nil {
		return ClaudeCodeConfigurationUnavailable()
	}
	accountID := embeddedClaudeCodeSafeAccountID(input.AccountID)
	model := embeddedClaudeCodeSafeModelID(input.Model)
	defer func() {
		input.AccountID = ""
		input.Model = ""
	}()
	if accountID == "" || model == "" {
		return claudeCodeConfigurationAfterError(manager, "请选择有效的 XIASS Tools Claude 账户及模型。")
	}
	modelWrite, err := manager.ApplyModel(model)
	if err != nil {
		return claudeCodeConfigurationAfterError(manager, "未能安全保存所选 Claude Code 模型。")
	}
	result, err := a.executeNativeAction(nativeActionRequest{
		Kind:      nativeActionClaudeCodeApply,
		AccountID: accountID,
		Model:     model,
	})
	if err != nil || !result.OK || result.Canceled {
		if _, rollbackErr := manager.Restore(modelWrite.BackupID); rollbackErr != nil {
			return claudeCodeConfigurationAfterError(manager, "未将所选 XIASS Tools Claude 账户应用到 Claude Code，且模型设置恢复失败。")
		}
		return claudeCodeConfigurationAfterError(manager, "未将所选 XIASS Tools Claude 账户应用到 Claude Code；模型设置已恢复。")
	}
	status := a.GetClaudeCodeConfiguration()
	if !status.OK {
		status.Message = "所选 XIASS Tools Claude 账户已应用，模型设置已保存，但无法刷新可见状态。"
		return status
	}
	status.Message = embeddedClaudeCodeApplyHostMessage(result.Value)
	return status
}

func (a *App) applyClaudeCodeConfiguration(manager *claudeconfig.Manager, config claudeconfig.ApplyConfig) ClaudeCodeConfigurationStatus {
	result, err := manager.Apply(config)
	if err != nil {
		return claudeCodeConfigurationAfterError(manager, "未保存 Claude Code 用户设置。请检查 API 根地址、授权令牌、模型名称及现有 settings.json。")
	}
	status := a.GetClaudeCodeConfiguration()
	if !status.OK {
		status.Message = "Claude Code 用户设置已安全写入并完成校验，但无法刷新可见状态。"
		return status
	}
	status.Message = "Claude Code 用户设置已安全保存，已创建可恢复备份 " + result.BackupID + "。"
	return status
}

func emptyClaudeCodeAccountCandidates() []ClaudeCodeAccountCandidate {
	return []ClaudeCodeAccountCandidate{}
}

func sanitizeEmbeddedClaudeCodeCandidates(values []ClaudeCodeAccountCandidate) []ClaudeCodeAccountCandidate {
	if len(values) == 0 {
		return emptyClaudeCodeAccountCandidates()
	}
	seenAccounts := make(map[string]struct{})
	result := make([]ClaudeCodeAccountCandidate, 0, len(values))
	for _, value := range values {
		id := embeddedClaudeCodeSafeAccountID(value.ID)
		label := embeddedClaudeCodeSafeLabel(value.Label)
		if id == "" || label == "" || !embeddedClaudeCodeCredentialMode(value.CredentialMode) {
			continue
		}
		if _, exists := seenAccounts[id]; exists {
			continue
		}
		models := sanitizeEmbeddedClaudeCodeCandidateModels(value.Models)
		if len(models) == 0 {
			continue
		}
		seenAccounts[id] = struct{}{}
		result = append(result, ClaudeCodeAccountCandidate{
			ID:             id,
			Label:          label,
			CredentialMode: value.CredentialMode,
			Models:         models,
		})
		if len(result) == embeddedClaudeCodeCandidateLimit {
			break
		}
	}
	sort.Slice(result, func(left, right int) bool {
		return strings.ToLower(result[left].Label+"\x00"+result[left].ID) < strings.ToLower(result[right].Label+"\x00"+result[right].ID)
	})
	return result
}

func sanitizeEmbeddedClaudeCodeCandidateModels(values []ClaudeCodeAccountCandidateModel) []ClaudeCodeAccountCandidateModel {
	seen := make(map[string]struct{})
	result := make([]ClaudeCodeAccountCandidateModel, 0, len(values))
	for _, value := range values {
		id := embeddedClaudeCodeSafeModelID(value.ID)
		label := embeddedClaudeCodeSafeLabel(value.DisplayName)
		if id == "" || label == "" {
			continue
		}
		key := strings.ToLower(id)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, ClaudeCodeAccountCandidateModel{ID: id, DisplayName: label})
		if len(result) == embeddedClaudeCodeCandidateModelLimit {
			break
		}
	}
	sort.Slice(result, func(left, right int) bool {
		return strings.ToLower(result[left].DisplayName+"\x00"+result[left].ID) < strings.ToLower(result[right].DisplayName+"\x00"+result[right].ID)
	})
	return result
}

func embeddedClaudeCodeSafeAccountID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > embeddedClaudeCodeAccountIDLimit {
		return ""
	}
	for _, character := range value {
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '_' || character == '-') {
			return ""
		}
	}
	return value
}

func embeddedClaudeCodeSafeModelID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > embeddedClaudeCodeModelIDLimit {
		return ""
	}
	for _, character := range value {
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || strings.ContainsRune("._:/@+-[]", character)) {
			return ""
		}
	}
	return value
}

func embeddedClaudeCodeSafeLabel(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len([]rune(value)) > embeddedClaudeCodeLabelLimit {
		return ""
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return ""
		}
	}
	return value
}

func embeddedClaudeCodeCredentialMode(value string) bool {
	switch value {
	case "OAuth", "Setup Token", "API Key":
		return true
	default:
		return false
	}
}

// Only these fixed, credential-free host messages may cross the embedded
// native-action boundary into the renderer. A malformed or future message is
// deliberately reduced to the established generic success text.
func embeddedClaudeCodeApplyHostMessage(value string) string {
	switch strings.TrimSpace(value) {
	case embeddedClaudeCodeAppliedMessage:
		return embeddedClaudeCodeFallbackApplyMessage
	case embeddedClaudeCodeCurrentMarkerWarning:
		return embeddedClaudeCodeCurrentMarkerWarning
	default:
		return embeddedClaudeCodeFallbackApplyMessage
	}
}

func claudeCodeAccountReusable(account storage.UpstreamAccount) bool {
	config, err := claudeCodeApplyConfigFromAccount(account, "claude-account-candidate", false)
	if err != nil {
		return false
	}
	clearClaudeCodeApplyConfig(&config)
	return true
}

func claudeCodeApplyConfigFromAccount(account storage.UpstreamAccount, model string, enableGatewayModelDiscovery bool) (claudeconfig.ApplyConfig, error) {
	if !account.Enabled {
		return claudeconfig.ApplyConfig{}, errors.New("upstream account is paused")
	}
	provider := strings.ToLower(strings.TrimSpace(account.Provider))
	apiStyle := strings.ToLower(strings.TrimSpace(account.APIStyle))
	// Claude Code always sends the Anthropic Messages protocol.  Both parts of
	// the source contract must therefore agree: accepting either an Anthropic
	// label *or* a Messages label would silently rewrite a saved account into a
	// different request shape.
	if provider != "anthropic" || apiStyle != "messages" {
		return claudeconfig.ApplyConfig{}, errors.New("upstream account does not use Claude Messages")
	}
	// A browser OAuth access token and a refresh-token account have a lifecycle
	// that Claude Code's static settings file cannot represent.  Do not copy a
	// short-lived credential into its configuration; the user can continue to
	// use that account through XIASS Tools instead.
	if !claudeCodeStaticCredentialAccountType(account.Type) {
		return claudeconfig.ApplyConfig{}, errors.New("OAuth or non-static account credentials cannot be mapped to Claude Code")
	}
	if strings.EqualFold(strings.TrimSpace(account.EndpointMode), "manual") {
		return claudeconfig.ApplyConfig{}, errors.New("manual endpoint cannot be mapped to Claude Code")
	}
	switch strings.ToLower(strings.TrimSpace(account.MessagePathMode)) {
	case "", "auto", "standard":
	default:
		return claudeconfig.ApplyConfig{}, errors.New("nonstandard message path cannot be mapped to Claude Code")
	}
	if len(account.Headers) != 0 || strings.EqualFold(strings.TrimSpace(account.AuthMode), "custom_header") {
		return claudeconfig.ApplyConfig{}, errors.New("custom headers cannot be mapped to Claude Code")
	}
	baseURL, err := claudeconfig.NormalizeBaseURL(strings.TrimSpace(account.APIURL))
	if err != nil {
		return claudeconfig.ApplyConfig{}, err
	}
	credential := account.EffectiveAPIKey()
	if strings.TrimSpace(credential) == "" {
		return claudeconfig.ApplyConfig{}, errors.New("upstream account has no credential")
	}
	mode := claudeconfig.CredentialModeAuthToken
	switch strings.ToLower(strings.TrimSpace(account.AuthMode)) {
	case "", "bearer":
		mode = claudeconfig.CredentialModeAuthToken
	case "x_api_key":
		mode = claudeconfig.CredentialModeAPIKey
	default:
		return claudeconfig.ApplyConfig{}, errors.New("upstream account authentication cannot be mapped to Claude Code")
	}
	return claudeconfig.ApplyConfig{
		BaseURL:                     baseURL,
		CredentialMode:              mode,
		Credential:                  credential,
		EnableGatewayModelDiscovery: enableGatewayModelDiscovery,
		Model:                       model,
	}, nil
}

func clearClaudeCodeApplyConfig(config *claudeconfig.ApplyConfig) {
	if config == nil {
		return
	}
	config.BaseURL = ""
	config.Credential = ""
	config.AuthToken = ""
	config.APIKeyHelper = ""
	config.Model = ""
}

func claudeCodeAccountLabel(account storage.UpstreamAccount) string {
	if label := strings.TrimSpace(account.Name); label != "" {
		return label
	}
	return "Claude 兼容账户"
}

func claudeCodeCredentialModeLabel(authMode string) string {
	if strings.EqualFold(strings.TrimSpace(authMode), "x_api_key") {
		return "API Key"
	}
	return "Bearer 令牌"
}

// claudeCodeStaticCredentialAccountType is deliberately narrower than the
// account-pool credential formats.  It admits only credentials that remain
// valid as a static Claude Code settings value; OAuth and refresh flows must
// retain their managed lifecycle inside XIASS Tools.
func claudeCodeStaticCredentialAccountType(accountType string) bool {
	switch strings.ToLower(strings.TrimSpace(accountType)) {
	case "api_key", "x_api_key", "setup_token", "codex_pat":
		return true
	default:
		return false
	}
}

func claudeCodeCandidateModels(accountID string, models []storage.CustomModel) []ClaudeCodeAccountCandidateModel {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" || len(models) == 0 {
		return []ClaudeCodeAccountCandidateModel{}
	}
	seen := make(map[string]struct{})
	result := make([]ClaudeCodeAccountCandidateModel, 0)
	for _, model := range models {
		if !claudeCodeModelBoundToAccount(model, accountID) || !claudeCodeCompatibleModel(model) {
			continue
		}
		id := strings.TrimSpace(model.ExternalModelName)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		label := strings.TrimSpace(model.DisplayName)
		if label == "" {
			label = id
		}
		result = append(result, ClaudeCodeAccountCandidateModel{ID: id, DisplayName: label})
	}
	sort.Slice(result, func(left, right int) bool {
		return strings.ToLower(result[left].DisplayName+"\x00"+result[left].ID) < strings.ToLower(result[right].DisplayName+"\x00"+result[right].ID)
	})
	return result
}

func claudeCodeModelBoundToAccount(model storage.CustomModel, accountID string) bool {
	for _, bound := range model.AccountIDs {
		if strings.TrimSpace(bound) == accountID {
			return true
		}
	}
	return false
}

func claudeCodeCompatibleModel(model storage.CustomModel) bool {
	return strings.EqualFold(strings.TrimSpace(model.Provider), "anthropic") && strings.EqualFold(strings.TrimSpace(model.APIStyle), "messages")
}

// DiscoverClaudeCodeGatewayModels performs an explicit, one-shot /v1/models
// request using only the credentials supplied to this invocation. It does not
// read the stored credential or change the user's settings.
func (a *App) DiscoverClaudeCodeGatewayModels(input ClaudeCodeGatewayRequestInput) ClaudeCodeGatewayModelDiscoveryStatus {
	request := claudeGatewayRequest(input)
	defer clearClaudeGatewayRequest(&input, &request)
	result, err := claudeconfig.DiscoverGatewayModels(context.Background(), request)
	status := ClaudeCodeGatewayModelDiscoveryStatus{
		Models:     nonNilClaudeGatewayModels(result.Models),
		HTTPStatus: result.HTTPStatus,
		DurationMS: result.DurationMS,
	}
	if err != nil {
		status.Message = claudeGatewayFailureMessage(err, "获取模型目录")
		return status
	}
	status.OK = true
	if len(status.Models) == 0 {
		status.Message = "网关已响应，但没有返回可供 Claude Code 显示的 Claude / Anthropic 模型。"
		return status
	}
	status.Message = "已从网关获取可用于 Claude Code 的模型目录。"
	return status
}

// TestClaudeCodeGateway performs one small, non-retried Claude Messages
// request using request-local data. A second retry could be billable, so the
// caller receives the first precise HTTP result instead of a synthetic pass.
func (a *App) TestClaudeCodeGateway(input ClaudeCodeGatewayRequestInput) ClaudeCodeGatewayTestStatus {
	request := claudeGatewayRequest(input)
	defer clearClaudeGatewayRequest(&input, &request)
	result, err := claudeconfig.TestGatewayMessages(context.Background(), request)
	status := ClaudeCodeGatewayTestStatus{HTTPStatus: result.HTTPStatus, DurationMS: result.DurationMS}
	if err != nil {
		status.Message = claudeGatewayFailureMessage(err, "测试 Claude Messages")
		return status
	}
	status.OK = true
	status.Message = "Claude Messages 实际请求成功。"
	return status
}

// RestoreClaudeCodeConfiguration explicitly restores one checksum-verified
// user-settings backup. It never restores credentials, OAuth state, projects,
// sessions, or MCP configuration.
func (a *App) RestoreClaudeCodeConfiguration(backupID string) ClaudeCodeConfigurationStatus {
	manager, err := a.claudeCodeManager()
	if err != nil {
		return ClaudeCodeConfigurationUnavailable()
	}
	result, err := manager.Restore(strings.TrimSpace(backupID))
	if err != nil {
		return claudeCodeConfigurationAfterError(manager, "未恢复 Claude Code 用户设置备份。该备份可能已损坏、已被删除或不适用于当前设置文件。")
	}
	status := a.GetClaudeCodeConfiguration()
	if !status.OK {
		status.Message = "Claude Code 用户设置备份已恢复，但无法刷新可见状态。"
		return status
	}
	status.Message = "已恢复 Claude Code 用户设置备份 " + result.RestoredBackupID + "，并创建了新的安全备份。"
	return status
}

// DeleteClaudeCodeConfigurationBackup removes only one verified,
// manager-owned user-settings backup.
func (a *App) DeleteClaudeCodeConfigurationBackup(backupID string) ClaudeCodeConfigurationStatus {
	manager, err := a.claudeCodeManager()
	if err != nil {
		return ClaudeCodeConfigurationUnavailable()
	}
	if err := manager.DeleteBackup(strings.TrimSpace(backupID)); err != nil {
		return claudeCodeConfigurationAfterError(manager, "未删除 Claude Code 用户设置备份。只可删除通过校验的备份。")
	}
	status := a.GetClaudeCodeConfiguration()
	if status.OK {
		status.Message = "已删除选定的 Claude Code 用户设置备份。"
	}
	return status
}

// MigrateClaudeCodeLegacyBackup copies one verified legacy backup to the
// current XIASS Tools backup root. It does not restore, alter, or delete the
// source backup.
func (a *App) MigrateClaudeCodeLegacyBackup(source, backupID string) ClaudeCodeConfigurationStatus {
	manager, err := a.claudeCodeManager()
	if err != nil {
		return ClaudeCodeConfigurationUnavailable()
	}
	if _, err := manager.MigrateLegacyBackup(strings.TrimSpace(source), strings.TrimSpace(backupID)); err != nil {
		return claudeCodeConfigurationAfterError(manager, "未导入旧版 Claude Code 用户设置备份。该备份未通过安全校验或无法复制。")
	}
	status := a.GetClaudeCodeConfiguration()
	if status.OK {
		status.Message = "已将旧版 Claude Code 用户设置备份复制为新的恢复点；旧备份保持不变。"
	}
	return status
}

func ClaudeCodeConfigurationUnavailable() ClaudeCodeConfigurationStatus {
	return ClaudeCodeConfigurationStatus{
		OK:            false,
		Message:       "无法识别 Claude Code 用户设置位置。",
		Backups:       emptyClaudeCodeBackups(),
		LegacyBackups: emptyClaudeCodeLegacyBackups(),
	}
}

func claudeCodeConfigurationAfterError(manager *claudeconfig.Manager, message string) ClaudeCodeConfigurationStatus {
	snapshot, _ := manager.Inspect()
	backups, _ := manager.ListBackups()
	legacyBackups, legacyErr := manager.ListLegacyBackups()
	status := ClaudeCodeConfigurationStatus{
		OK:            false,
		Message:       message,
		Snapshot:      claudeCodeRendererSnapshot(snapshot),
		Backups:       nonNilClaudeCodeBackups(backups),
		LegacyBackups: nonNilClaudeCodeLegacyBackups(legacyBackups),
	}
	if legacyErr != nil {
		status.LegacyBackups = emptyClaudeCodeLegacyBackups()
		status.LegacyBackupWarning = "检测到旧版 Claude Code 设置备份目录异常，已跳过旧备份；当前用户设置未被修改。"
	}
	return status
}

func emptyClaudeCodeBackups() []claudeconfig.BackupInfo {
	return []claudeconfig.BackupInfo{}
}

func nonNilClaudeCodeBackups(backups []claudeconfig.BackupInfo) []claudeconfig.BackupInfo {
	if backups == nil {
		return emptyClaudeCodeBackups()
	}
	return backups
}

func emptyClaudeCodeLegacyBackups() []claudeconfig.LegacyBackupInfo {
	return []claudeconfig.LegacyBackupInfo{}
}

func nonNilClaudeCodeLegacyBackups(backups []claudeconfig.LegacyBackupInfo) []claudeconfig.LegacyBackupInfo {
	if backups == nil {
		return emptyClaudeCodeLegacyBackups()
	}
	return backups
}

func claudeCodeRendererSnapshot(snapshot claudeconfig.Snapshot) ClaudeCodeConfigurationSnapshot {
	return ClaudeCodeConfigurationSnapshot{
		Exists:                       snapshot.Location.Exists,
		Valid:                        snapshot.Valid,
		Model:                        snapshot.Model,
		BaseURL:                      snapshot.BaseURL,
		CredentialMode:               string(snapshot.CredentialMode),
		CredentialConfigured:         snapshot.CredentialConfigured,
		AuthTokenConfigured:          snapshot.AuthTokenConfigured,
		APIKeyHelperConfigured:       snapshot.APIKeyHelperConfigured,
		GatewayModelDiscoveryEnabled: snapshot.GatewayModelDiscoveryEnabled,
		GatewayModelDiscoveryBlocked: snapshot.GatewayModelDiscoveryBlocked,
		Managed:                      snapshot.Managed,
	}
}

func claudeCodeCredentialMode(value string) claudeconfig.CredentialMode {
	return claudeconfig.CredentialMode(strings.TrimSpace(value))
}

func claudeCodeCredential(credential, legacyAuthToken string) string {
	if credential != "" {
		return credential
	}
	return legacyAuthToken
}

func claudeGatewayRequest(input ClaudeCodeGatewayRequestInput) claudeconfig.GatewayRequest {
	return claudeconfig.GatewayRequest{
		BaseURL:        input.BaseURL,
		CredentialMode: claudeCodeCredentialMode(input.CredentialMode),
		Credential:     claudeCodeCredential(input.Credential, input.AuthToken),
		Model:          input.Model,
	}
}

func clearClaudeGatewayRequest(input *ClaudeCodeGatewayRequestInput, request *claudeconfig.GatewayRequest) {
	if input != nil {
		input.Credential = ""
		input.AuthToken = ""
	}
	if request != nil {
		request.Credential = ""
	}
}

func nonNilClaudeGatewayModels(models []claudeconfig.GatewayModel) []claudeconfig.GatewayModel {
	if models == nil {
		return []claudeconfig.GatewayModel{}
	}
	return models
}

func claudeGatewayFailureMessage(err error, action string) string {
	var httpError *claudeconfig.GatewayHTTPError
	switch {
	case errors.Is(err, claudeconfig.ErrGatewayHelperCheckUnsupported):
		return "apiKeyHelper 会由 Claude Code 自身执行；XIASS Tools 不会运行该脚本进行远程测试。"
	case errors.As(err, &httpError):
		return action + "未通过（HTTP " + strconv.Itoa(httpError.StatusCode) + "）。网关未返回的详细错误不会显示，以免泄露凭据或内部信息。"
	default:
		return action + "未通过。请核对本次填写的地址、凭据模式、模型与网关的 Anthropic Messages 兼容性。"
	}
}
