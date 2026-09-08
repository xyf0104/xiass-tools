package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"antigravity-wf-assistant/internal/codexconfig"
	"antigravity-wf-assistant/internal/storage"
	"antigravity-wf-assistant/internal/upstream"
)

// CodexConfigurationStatus is a credential-safe view for the renderer. The
// API key used during Apply or Discover never appears in this result and is
// not retained by App.
type CodexConfigurationStatus struct {
	OK                  bool                            `json:"ok"`
	Message             string                          `json:"message"`
	Snapshot            codexconfig.ConfigSnapshot      `json:"snapshot"`
	Backups             []codexconfig.BackupInfo        `json:"backups,omitempty"`
	HistoryBackups      []codexconfig.HistoryBackupInfo `json:"historyBackups,omitempty"`
	LegacyBackups       []codexconfig.LegacyBackupInfo  `json:"legacyBackups"`
	LegacyBackupWarning string                          `json:"legacyBackupWarning,omitempty"`
	// LegacyProviderMigrationCompleted is set when either an explicit migration
	// or a restore-required forward migration completes. It is a boolean
	// acknowledgement, not a copy of a Provider or its data, so the renderer
	// never has to infer a successful migration from a generic config snapshot.
	LegacyProviderMigrationCompleted bool `json:"legacyProviderMigrationCompleted,omitempty"`
	LegacyProviderMigrationWasActive bool `json:"legacyProviderMigrationWasActive,omitempty"`
}

type CodexModelDiscoveryResult struct {
	OK      bool     `json:"ok"`
	Message string   `json:"message"`
	Models  []string `json:"models,omitempty"`
}

// CodexAccountCandidate is the intentionally small renderer-safe projection
// of one reusable XIASS Tools account. Endpoint data, headers, API keys,
// OAuth material and refresh state never leave the native layer.
type CodexAccountCandidate struct {
	ID             string                       `json:"id"`
	Label          string                       `json:"label"`
	CredentialMode string                       `json:"credentialMode"`
	Models         []CodexAccountCandidateModel `json:"models"`
}

// CodexAccountCandidateModel is already-bound, public model metadata. It is
// not a connection test and intentionally omits its route, account and
// capability internals.
type CodexAccountCandidateModel struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
}

// CodexAccountCandidatesStatus is safe to pass through Wails. Accounts that
// cannot be copied exactly into Codex's documented Responses provider are
// omitted rather than listed with potentially sensitive implementation detail.
type CodexAccountCandidatesStatus struct {
	OK         bool                    `json:"ok"`
	Message    string                  `json:"message"`
	Candidates []CodexAccountCandidate `json:"candidates"`
}

// CodexApplyAccountInput lets the renderer choose a saved account by opaque
// ID and configure only public Codex preferences. It deliberately has no URL,
// credential, custom-header, OAuth or provider-ID fields: native code resolves
// and revalidates the selected account before every write.
type CodexApplyAccountInput struct {
	AccountID                  string `json:"accountId"`
	Model                      string `json:"model"`
	ReviewModel                string `json:"reviewModel"`
	WebSearch                  string `json:"webSearch"`
	ModelContextWindow         int64  `json:"modelContextWindow"`
	ModelAutoCompactTokenLimit int64  `json:"modelAutoCompactTokenLimit"`
}

// CodexApplyAccountLifecycleInput adds the existing explicit Desktop lifecycle
// controls to a saved-account apply. Credentials remain resolved and cleared
// entirely in native memory.
type CodexApplyAccountLifecycleInput struct {
	Account                       CodexApplyAccountInput `json:"account"`
	Confirmation                  string                 `json:"confirmation,omitempty"`
	RepairHistoryOnProviderChange bool                   `json:"repairHistoryOnProviderChange"`
	LaunchAfter                   bool                   `json:"launchAfter"`
}

type CodexHistoryRepairStatus struct {
	OK      bool                            `json:"ok"`
	Message string                          `json:"message"`
	Result  codexconfig.HistoryRepairResult `json:"result"`
}

// codexConfigurationRestoreManager is the narrow, non-secret portion of the
// configuration manager required by the restore forward-migration sequence.
// Keeping this boundary explicit lets the transaction be tested without
// substituting a real config.toml, credential, or desktop process.
type codexConfigurationRestoreManager interface {
	Restore(string) (codexconfig.RestoreResult, error)
	InspectLegacyProviderMigration() (codexconfig.LegacyProviderMigrationStatus, error)
	MigrateLegacyProvider() (codexconfig.LegacyProviderMigrationResult, error)
}

// codexConfigurationRestoreOutcome records only control-flow facts from a
// restore. It deliberately omits raw config, Provider contents, credentials,
// paths, and implementation errors so callers cannot accidentally surface
// them through the Wails boundary.
type codexConfigurationRestoreOutcome struct {
	RestoreResult                  codexconfig.RestoreResult
	LegacyProviderMigrated         bool
	LegacyProviderMigrationFailed  bool
	MigrationRollbackWasSuccessful bool
}

// restoreCodexConfigurationWithLegacyForwardMigration restores a selected
// configuration backup and then forward-migrates it only when it proves to be
// an active, structurally verified first-party legacy XIASS Provider. This is
// the compatibility behavior used by the first-party helper after restoring a
// legacy backup. Inactive, ambiguous, or unverified legacy entries are left
// unchanged: automatically changing an unrelated Provider would be unsafe.
//
// If a required forward migration cannot be completed, the pre-restore safety
// backup is restored before this function returns. The result contains no raw
// failure because config.toml can include an API key or private endpoint.
func restoreCodexConfigurationWithLegacyForwardMigration(manager codexConfigurationRestoreManager, backupID string) (codexConfigurationRestoreOutcome, error) {
	result, err := manager.Restore(backupID)
	if err != nil {
		return codexConfigurationRestoreOutcome{}, err
	}

	outcome := codexConfigurationRestoreOutcome{RestoreResult: result}
	eligibility, inspectErr := manager.InspectLegacyProviderMigration()
	if inspectErr != nil || !eligibility.Available || !eligibility.WasActive {
		return outcome, nil
	}

	migration, migrationErr := manager.MigrateLegacyProvider()
	if migrationErr == nil && migration.Migrated {
		outcome.LegacyProviderMigrated = true
		return outcome, nil
	}

	outcome.LegacyProviderMigrationFailed = true
	_, rollbackErr := manager.Restore(result.SafetyBackupID)
	outcome.MigrationRollbackWasSuccessful = rollbackErr == nil
	return outcome, nil
}

func (a *App) codexManager() (*codexconfig.Manager, error) {
	return codexconfig.NewDefaultManager()
}

// GetCodexConfiguration reads only the public, redacted projection of
// config.toml and the names of verified XIASS Tools backups.
func (a *App) GetCodexConfiguration() CodexConfigurationStatus {
	manager, err := a.codexManager()
	if err != nil {
		return codexConfigurationStatusForRenderer(codexConfigurationUnavailableStatus())
	}
	snapshot, inspectErr := manager.Inspect()
	backups, backupErr := manager.ListBackups()
	historyBackups, historyErr := codexconfig.NewHistoryRepairerWithManager(manager).ListBackups()
	legacyBackups, legacyWarning := codexLegacyBackupStatus(manager)
	if inspectErr != nil {
		return codexConfigurationStatusForRenderer(CodexConfigurationStatus{OK: false, Message: "无法安全读取 Codex 配置；当前文件未被修改。", Snapshot: snapshot, Backups: backups, HistoryBackups: historyBackups, LegacyBackups: legacyBackups, LegacyBackupWarning: legacyWarning})
	}
	if backupErr != nil {
		return codexConfigurationStatusForRenderer(CodexConfigurationStatus{OK: false, Message: "Codex 配置可读取，但备份目录未通过安全检查。", Snapshot: snapshot, HistoryBackups: historyBackups, LegacyBackups: legacyBackups, LegacyBackupWarning: legacyWarning})
	}
	if historyErr != nil {
		return codexConfigurationStatusForRenderer(CodexConfigurationStatus{OK: false, Message: "Codex 配置可读取，但历史备份目录未通过安全检查。", Snapshot: snapshot, Backups: backups, LegacyBackups: legacyBackups, LegacyBackupWarning: legacyWarning})
	}
	return codexConfigurationStatusForRenderer(CodexConfigurationStatus{OK: true, Message: "已读取本机 Codex 配置。", Snapshot: snapshot, Backups: backups, HistoryBackups: historyBackups, LegacyBackups: legacyBackups, LegacyBackupWarning: legacyWarning})
}

// ApplyCodexConfiguration delegates all mutation to codexconfig.Manager. The
// manager validates input, creates a checksum-protected backup, writes
// atomically, reads back its result, and rolls back on a failure.
func (a *App) ApplyCodexConfiguration(input codexconfig.ApplyConfig) CodexConfigurationStatus {
	manager, err := a.codexManager()
	if err != nil {
		return codexConfigurationUnavailableStatus()
	}
	result, err := manager.Apply(input)
	if err != nil {
		return codexConfigurationAfterError(manager, "未保存 Codex 配置。请检查 API 地址、Key、模型名称及现有 config.toml 后重试。")
	}
	status := a.GetCodexConfiguration()
	if !status.OK {
		status.Message = "配置已安全写入并通过校验，但刷新状态失败：" + status.Message
		return status
	}
	status.Message = "Codex 配置已安全保存，已创建可恢复备份 " + result.BackupID + "。"
	return status
}

// RemoveCodexXIASSProvider explicitly removes only the xiass_tools Provider
// managed by this app. It never reads credentials, auth.json, cookies,
// history, or Desktop state, and it never starts, stops, or restarts Codex.
// The manager creates a recoverable backup before an actual removal and leaves
// every unrelated provider and TOML setting intact.
func (a *App) RemoveCodexXIASSProvider() CodexConfigurationStatus {
	manager, err := a.codexManager()
	if err != nil {
		return codexConfigurationUnavailableStatus()
	}
	result, err := manager.RemoveXIASSProvider()
	if err != nil {
		return codexConfigurationRemovalAfterError(manager, "未移除 XIASS Tools Codex Provider。当前配置保持不变；请检查 config.toml 后重试。")
	}
	// Disconnect is intentionally config-only. Unlike the general modal refresh
	// it does not enumerate history backups or legacy archives, so this action
	// never reads active history data as a side effect of removing a provider.
	status := codexConfigurationRemovalStatus(manager)
	if !status.OK {
		status.Message = "XIASS Tools Provider 操作已完成，但刷新状态失败：" + status.Message
		return status
	}
	if !result.Removed {
		status.Message = "未发现可移除的 XIASS Tools Codex Provider；当前配置没有被更改。"
		return status
	}
	if result.WasActive {
		status.Message = "已移除 XIASS Tools Codex Provider，并清除当前模型选择；已创建可恢复配置备份 " + result.BackupID + "。"
		return status
	}
	status.Message = "已移除 XIASS Tools Codex Provider；其他当前模型选择和配置保持不变，已创建可恢复配置备份 " + result.BackupID + "。"
	return status
}

// MigrateCodexLegacyProvider performs an explicitly requested, config-only
// migration of one safe first-party predecessor Provider to xiass_tools. The
// native manager refuses to write while Codex is running; callers that need
// XIASS Tools to stop and relaunch an already-running desktop app must use the
// separate confirmed lifecycle transaction below. This method never reads
// auth.json, OAuth, cookies, sessions, history, or another Provider's data.
func (a *App) MigrateCodexLegacyProvider() CodexConfigurationStatus {
	manager, err := a.codexManager()
	if err != nil {
		return codexConfigurationUnavailableStatus()
	}
	result, err := manager.MigrateLegacyProvider()
	if err != nil {
		return codexConfigurationMigrationAfterError(manager, "未迁移旧版 Codex Provider。当前配置保持不变；请先退出 Codex 或使用确认后的高级迁移，再重试。")
	}
	status := codexConfigurationMigrationStatus(manager)
	if !status.OK {
		status.Message = "旧版 Codex Provider 操作已完成，但无法安全刷新 config.toml 状态。"
		return status
	}
	if !result.Migrated {
		status.Message = "未发现可安全迁移的旧版 XIASS Codex Provider；当前配置没有被更改。"
		return status
	}
	status.LegacyProviderMigrationCompleted = true
	status.LegacyProviderMigrationWasActive = result.WasActive
	if result.WasActive {
		status.Message = "已将旧版 XIASS Codex Provider 迁移至 XIASS Tools，并保留模型、审查模型、上下文和联网搜索设置；已创建可恢复配置备份 " + result.BackupID + "。"
		return status
	}
	status.Message = "已将旧版 XIASS Codex Provider 迁移至 XIASS Tools；当前正在使用的其他 Provider 和模型设置保持不变，已创建可恢复配置备份 " + result.BackupID + "。"
	return status
}

// DiscoverCodexModels sends one explicitly user-requested request to the
// compatible upstream's /v1/models endpoint. API keys remain request-local.
func (a *App) DiscoverCodexModels(baseURL, apiKey string) CodexModelDiscoveryResult {
	ctx, cancel := a.upstreamContext(10 * time.Second)
	defer cancel()
	models, err := codexconfig.DiscoverModels(ctx, baseURL, apiKey, codexconfig.ModelDiscoveryOptions{})
	if err != nil {
		return CodexModelDiscoveryResult{OK: false, Message: "获取 Codex 上游模型失败。请检查 API 地址、Key、网络和模型服务后重试。"}
	}
	return CodexModelDiscoveryResult{OK: true, Message: codexModelCatalogMessage(len(models)), Models: models}
}

// GetCodexAccountCandidates lists only saved XIASS Tools accounts whose
// complete request contract is representable by Codex's managed Responses
// provider. The renderer receives no API URL, key, header, OAuth token or
// refresh metadata, and every account is validated again at apply time.
func (a *App) GetCodexAccountCandidates() CodexAccountCandidatesStatus {
	accounts, err := storage.LoadUpstreamAccounts()
	if err != nil {
		return CodexAccountCandidatesStatus{Candidates: emptyCodexAccountCandidates(), Message: "无法读取本机账户池。"}
	}
	models, modelErr := storage.LoadModels()
	if modelErr != nil {
		// A readable account can still be applied with a manually entered model
		// ID. The optional Antigravity model catalog is only a convenience list.
		models = nil
	}
	candidates := make([]CodexAccountCandidate, 0, len(accounts))
	for _, account := range accounts {
		if !codexAccountReusable(account) {
			continue
		}
		candidates = append(candidates, CodexAccountCandidate{
			ID:             strings.TrimSpace(account.ID),
			Label:          codexAccountLabel(account),
			CredentialMode: codexAccountCredentialModeLabel(account),
			Models:         codexAccountCandidateModels(account.ID, models),
		})
	}
	sort.Slice(candidates, func(left, right int) bool {
		return strings.ToLower(candidates[left].Label+"\x00"+candidates[left].ID) < strings.ToLower(candidates[right].Label+"\x00"+candidates[right].ID)
	})
	if len(candidates) == 0 {
		return CodexAccountCandidatesStatus{OK: true, Candidates: emptyCodexAccountCandidates(), Message: "没有可无损应用到 Codex 的已启用 OpenAI Responses 账户。"}
	}
	message := "已读取可无损应用到 Codex 的已启用 OpenAI Responses 账户。"
	if modelErr != nil {
		message = "已读取可复用账户；本机模型目录暂不可用，可手动填写 Responses 模型 ID。"
	}
	return CodexAccountCandidatesStatus{OK: true, Candidates: candidates, Message: message}
}

// DiscoverCodexAccountModels performs one explicit /v1/models request with a
// selected compatible account. The saved credential remains native-only and
// is cleared from the temporary config after the request completes.
func (a *App) DiscoverCodexAccountModels(accountID string) CodexModelDiscoveryResult {
	account, err := storage.GetUpstreamAccount(strings.TrimSpace(accountID))
	accountID = ""
	if err != nil {
		return CodexModelDiscoveryResult{OK: false, Message: "无法使用该账户获取 Codex 上游模型。请刷新账户池后重新选择。"}
	}
	config, err := codexApplyConfigFromAccount(account, CodexApplyAccountInput{Model: codexconfig.DefaultModel})
	if err != nil {
		return CodexModelDiscoveryResult{OK: false, Message: "该账户无法无损用于 Codex。请使用已启用的 OpenAI Responses 账户，或手动填写配置。"}
	}
	defer clearCodexApplyConfig(&config)
	ctx, cancel := a.upstreamContext(10 * time.Second)
	defer cancel()
	models, err := codexconfig.DiscoverModels(ctx, config.BaseURL, config.APIKey, codexconfig.ModelDiscoveryOptions{})
	if err != nil {
		return CodexModelDiscoveryResult{OK: false, Message: "获取 Codex 上游模型失败。请检查账户状态、网络和上游服务后重试。"}
	}
	return CodexModelDiscoveryResult{OK: true, Message: codexModelCatalogMessage(len(models)), Models: models}
}

// ApplyCodexConfigurationFromAccount applies one explicitly selected,
// currently compatible XIASS Tools account without accepting its endpoint or
// credential from Vue. The existing non-secret Codex configuration projection
// may still show its provider root after a successful save; the account
// candidate and request DTO never contain it. Manager.Apply creates a backup,
// atomically writes config.toml, reads it back and rolls back a failed
// validation.
func (a *App) ApplyCodexConfigurationFromAccount(input CodexApplyAccountInput) CodexConfigurationStatus {
	manager, err := a.codexManager()
	if err != nil {
		return codexConfigurationUnavailableStatus()
	}
	account, err := storage.GetUpstreamAccount(strings.TrimSpace(input.AccountID))
	input.AccountID = ""
	if err != nil {
		return codexConfigurationAfterError(manager, "无法使用该账户配置 Codex。请刷新账户池后重新选择。")
	}
	config, err := codexApplyConfigFromAccount(account, input)
	clearCodexApplyAccountInput(&input)
	if err != nil {
		return codexConfigurationAfterError(manager, "该账户无法无损应用到 Codex。请使用已启用的 OpenAI Responses 账户，或手动填写配置。")
	}
	defer clearCodexApplyConfig(&config)
	result, err := manager.Apply(config)
	if err != nil {
		return codexConfigurationAfterError(manager, "未保存所选账户的 Codex 配置。现有配置保持可恢复状态；请检查账户和 config.toml 后重试。")
	}
	status := a.GetCodexConfiguration()
	if !status.OK {
		status.Message = "所选账户的 Codex 配置已安全写入并通过校验，但刷新状态失败：" + status.Message
		return status
	}
	status.Message = "已使用已保存的 XIASS Tools 账户安全配置 Codex，并创建可恢复备份 " + result.BackupID + "。"
	return status
}

// ApplyCodexConfigurationFromAccountWithLifecycle is the saved-account
// counterpart of the existing confirmed Codex lifecycle transaction. The
// account is reloaded and mapped natively under the same operation lock, so a
// stale card selection can never apply a paused or incompatible credential.
func (a *App) ApplyCodexConfigurationFromAccountWithLifecycle(input CodexApplyAccountLifecycleInput) CodexConfigurationLifecycleStatus {
	if a == nil || a.ctx == nil || a.exitRequested.Load() {
		return CodexConfigurationLifecycleStatus{OK: false, Message: "XIASS Tools 尚未完成启动，无法应用 Codex 配置。"}
	}
	a.codexDesktopOperation.Lock()
	defer a.codexDesktopOperation.Unlock()
	manager, err := a.codexManager()
	if err != nil {
		return CodexConfigurationLifecycleStatus{OK: false, Message: "无法识别本机 Codex 配置目录；未执行任何操作。"}
	}
	account, err := storage.GetUpstreamAccount(strings.TrimSpace(input.Account.AccountID))
	input.Account.AccountID = ""
	if err != nil {
		clearCodexApplyAccountInput(&input.Account)
		return CodexConfigurationLifecycleStatus{OK: false, Message: "无法使用该账户配置 Codex。请刷新账户池后重新选择。"}
	}
	config, err := codexApplyConfigFromAccount(account, input.Account)
	clearCodexApplyAccountInput(&input.Account)
	if err != nil {
		return CodexConfigurationLifecycleStatus{OK: false, Message: "该账户无法无损应用到 Codex。请使用已启用的 OpenAI Responses 账户，或手动填写配置。"}
	}
	defer clearCodexApplyConfig(&config)
	return a.applyCodexConfigurationWithLifecycleLocked(CodexConfigurationLifecycleInput{
		Config:                        config,
		Confirmation:                  input.Confirmation,
		RepairHistoryOnProviderChange: input.RepairHistoryOnProviderChange,
		LaunchAfter:                   input.LaunchAfter,
	}, manager, manager.Apply)
}

func emptyCodexAccountCandidates() []CodexAccountCandidate {
	return []CodexAccountCandidate{}
}

// codexAccountReusable intentionally asks the exact mapping function used by
// Apply. If this predicate returns true, a second revalidation during Apply
// still protects against account edits, disablement or credential removal.
func codexAccountReusable(account storage.UpstreamAccount) bool {
	config, err := codexApplyConfigFromAccount(account, CodexApplyAccountInput{Model: codexconfig.DefaultModel})
	if err != nil {
		return false
	}
	clearCodexApplyConfig(&config)
	return true
}

// codexApplyConfigFromAccount permits only the account contract that Codex's
// managed provider can reproduce exactly: an enabled, automatic OpenAI
// Responses endpoint with a stable Bearer credential and no custom headers.
// Direct ChatGPT/Codex OAuth is deliberately excluded: it needs its own
// manual path, account identity header and refresh lifecycle, none of which a
// standard Codex provider table can represent without silently changing it.
func codexApplyConfigFromAccount(account storage.UpstreamAccount, input CodexApplyAccountInput) (codexconfig.ApplyConfig, error) {
	if !account.Enabled {
		return codexconfig.ApplyConfig{}, errors.New("upstream account is paused")
	}
	if !strings.EqualFold(strings.TrimSpace(account.Provider), "openai") {
		return codexconfig.ApplyConfig{}, errors.New("upstream account is not OpenAI-compatible")
	}
	if account.IsOpenAICodexOAuth() || !codexStableBearerAccountType(account.Type) {
		return codexconfig.ApplyConfig{}, errors.New("OAuth or non-static account credentials cannot be mapped to Codex")
	}
	if !strings.EqualFold(strings.TrimSpace(account.APIStyle), "responses") {
		return codexconfig.ApplyConfig{}, errors.New("upstream account does not use Responses")
	}
	if !codexAutomaticEndpointMode(account.EndpointMode) || !codexAutomaticMessagePathMode(account.MessagePathMode) {
		return codexconfig.ApplyConfig{}, errors.New("manual endpoint contract cannot be mapped to Codex")
	}
	if !strings.EqualFold(strings.TrimSpace(account.AuthMode), "bearer") || strings.TrimSpace(account.AuthHeader) != "" || len(account.Headers) != 0 {
		return codexconfig.ApplyConfig{}, errors.New("upstream authentication headers cannot be mapped to Codex")
	}
	credential := account.EffectiveAPIKey()
	if strings.TrimSpace(credential) == "" {
		return codexconfig.ApplyConfig{}, errors.New("upstream account has no credential")
	}

	baseURL, err := codexconfig.NormalizeBaseURL(strings.TrimSpace(account.APIURL))
	if err != nil {
		return codexconfig.ApplyConfig{}, err
	}
	// Compare the actual endpoint generated by the account's auto contract
	// with the endpoint Codex will derive from its normalized base URL. This
	// rejects query-string routing and non-/v1 custom paths that would otherwise
	// look valid but route to a different server request after import.
	sourceEndpoint, err := upstream.ResolveResponsesURLForConfig(upstream.ConfigFromAccount(account))
	if err != nil {
		return codexconfig.ApplyConfig{}, err
	}
	mappedEndpoint, err := upstream.ResolveResponsesURL(baseURL)
	if err != nil || sourceEndpoint != mappedEndpoint {
		return codexconfig.ApplyConfig{}, errors.New("upstream endpoint would change when mapped to Codex")
	}

	config, err := codexconfig.NormalizeApplyConfig(codexconfig.ApplyConfig{
		BaseURL:                    baseURL,
		APIKey:                     credential,
		KeyName:                    codexAccountLabel(account),
		ProviderName:               codexAccountLabel(account),
		ProviderID:                 codexconfig.DefaultProviderID,
		Model:                      strings.TrimSpace(input.Model),
		ReviewModel:                strings.TrimSpace(input.ReviewModel),
		WireAPI:                    codexconfig.DefaultWireAPI,
		WebSearch:                  strings.TrimSpace(input.WebSearch),
		ModelContextWindow:         input.ModelContextWindow,
		ModelAutoCompactTokenLimit: input.ModelAutoCompactTokenLimit,
	})
	credential = ""
	return config, err
}

func codexStableBearerAccountType(accountType string) bool {
	switch strings.ToLower(strings.TrimSpace(accountType)) {
	case "api_key", "codex_pat", "setup_token":
		return true
	default:
		return false
	}
}

func codexAutomaticEndpointMode(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "auto":
		return true
	default:
		return false
	}
}

func codexAutomaticMessagePathMode(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "auto":
		return true
	default:
		return false
	}
}

func codexAccountLabel(account storage.UpstreamAccount) string {
	if label := strings.TrimSpace(account.Name); label != "" {
		return label
	}
	return "XIASS Tools OpenAI 账户"
}

func codexAccountCredentialModeLabel(account storage.UpstreamAccount) string {
	switch strings.ToLower(strings.TrimSpace(account.Type)) {
	case "codex_pat":
		return "Codex PAT"
	case "setup_token":
		return "Setup Token"
	default:
		return "Bearer API Key"
	}
}

func codexAccountCandidateModels(accountID string, models []storage.CustomModel) []CodexAccountCandidateModel {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" || len(models) == 0 {
		return []CodexAccountCandidateModel{}
	}
	seen := make(map[string]struct{})
	result := make([]CodexAccountCandidateModel, 0)
	for _, model := range models {
		if !model.IsEnabled() || !codexModelBoundToAccount(model, accountID) || !codexResponsesCompatibleModel(model) {
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
		result = append(result, CodexAccountCandidateModel{ID: id, DisplayName: label})
	}
	sort.Slice(result, func(left, right int) bool {
		return strings.ToLower(result[left].DisplayName+"\x00"+result[left].ID) < strings.ToLower(result[right].DisplayName+"\x00"+result[right].ID)
	})
	return result
}

func codexModelBoundToAccount(model storage.CustomModel, accountID string) bool {
	for _, bound := range model.AccountIDs {
		if strings.TrimSpace(bound) == accountID {
			return true
		}
	}
	return false
}

func codexResponsesCompatibleModel(model storage.CustomModel) bool {
	return strings.EqualFold(strings.TrimSpace(model.Provider), "openai") &&
		strings.EqualFold(strings.TrimSpace(model.APIStyle), "responses") &&
		codexAutomaticEndpointMode(model.EndpointMode) &&
		codexAutomaticMessagePathMode(model.MessagePathMode)
}

func clearCodexApplyAccountInput(input *CodexApplyAccountInput) {
	if input == nil {
		return
	}
	input.AccountID = ""
	input.Model = ""
	input.ReviewModel = ""
	input.WebSearch = ""
	input.ModelContextWindow = 0
	input.ModelAutoCompactTokenLimit = 0
}

func clearCodexApplyConfig(config *codexconfig.ApplyConfig) {
	if config == nil {
		return
	}
	config.BaseURL = ""
	config.APIKey = ""
	config.KeyName = ""
	config.ProviderID = ""
	config.ProviderName = ""
	config.Model = ""
	config.ReviewModel = ""
	config.WireAPI = ""
	config.WebSearch = ""
	config.ModelContextWindow = 0
	config.ModelAutoCompactTokenLimit = 0
}

// codexModelCatalogMessage deliberately distinguishes a readable /v1/models
// catalog from a successful Responses inference request. Discovery is a
// user-requested metadata request only; it must never be presented as a model
// availability test or trigger an implicit billable inference call.
func codexModelCatalogMessage(count int) string {
	return "已发现 " + formatCodexModelCount(count) + " 个模型 ID（尚未验证 Responses 推理）。"
}

func (a *App) RestoreCodexConfiguration(backupID string) CodexConfigurationStatus {
	// Restoring config.toml can switch the active Provider and model beneath a
	// live Codex process. Unlike a read-only backup listing, it is therefore a
	// configuration mutation and must not race a running Desktop instance. The
	// direct restore flow intentionally refuses rather than stopping or killing
	// Codex; callers can exit it explicitly, then retry this operation.
	if a == nil {
		return codexConfigurationUnavailableStatus()
	}
	a.codexDesktopOperation.Lock()
	defer a.codexDesktopOperation.Unlock()
	controller := a.codexDesktopController()
	if controller == nil {
		return codexConfigurationUnavailableStatus()
	}
	desktop := controller.Status(a.codexDesktopContext())
	if desktop.Running || hasCodexDesktopProcessWarning(desktop) {
		return CodexConfigurationStatus{OK: false, Message: "Codex Desktop 正在运行或运行状态无法安全确认；未恢复配置。请先退出 Codex，再重试。", LegacyBackups: emptyCodexLegacyBackups()}
	}
	manager, err := a.codexManager()
	if err != nil {
		return codexConfigurationUnavailableStatus()
	}
	outcome, err := restoreCodexConfigurationWithLegacyForwardMigration(manager, strings.TrimSpace(backupID))
	if err != nil {
		return codexConfigurationAfterError(manager, "未恢复 Codex 配置。该备份可能已损坏、已被删除或不适用于当前配置。")
	}
	if outcome.LegacyProviderMigrationFailed {
		if outcome.MigrationRollbackWasSuccessful {
			return codexConfigurationAfterError(manager, "已恢复的旧版 XIASS Codex Provider 无法安全前向迁移；已恢复到本次操作前的配置。请保持 Codex 退出并检查配置后重试。")
		}
		return codexConfigurationAfterError(manager, "已恢复的旧版 XIASS Codex Provider 无法安全前向迁移，且无法验证恢复到本次操作前的配置。请保持 Codex 退出并先手动检查配置。")
	}
	status := a.GetCodexConfiguration()
	if !status.OK {
		if outcome.LegacyProviderMigrated {
			status.Message = "配置备份已恢复并完成旧版 XIASS Provider 前向迁移，但刷新状态失败：" + status.Message
		} else {
			status.Message = "配置备份已恢复，但刷新状态失败：" + status.Message
		}
		return status
	}
	if outcome.LegacyProviderMigrated {
		status.LegacyProviderMigrationCompleted = true
		status.LegacyProviderMigrationWasActive = true
		status.Message = "已恢复 Codex 配置备份 " + outcome.RestoreResult.RestoredBackupID + "，并将已验证的旧版 XIASS Provider 前向迁移为 XIASS Tools；已创建新的安全备份。"
		return status
	}
	status.Message = "已恢复 Codex 配置备份 " + outcome.RestoreResult.RestoredBackupID + "，并创建新的安全备份。"
	return status
}

func (a *App) DeleteCodexConfigurationBackup(backupID string) CodexConfigurationStatus {
	manager, err := a.codexManager()
	if err != nil {
		return codexConfigurationUnavailableStatus()
	}
	if err := manager.DeleteBackup(strings.TrimSpace(backupID)); err != nil {
		return codexConfigurationAfterError(manager, "未删除 Codex 配置备份。只可删除通过校验的备份。")
	}
	status := a.GetCodexConfiguration()
	if status.OK {
		status.Message = "已删除选定的 Codex 配置备份。"
	}
	return status
}

// ImportCodexLegacyConfigBackup stages one verified first-party XIASS Codex
// Helper configuration archive as a current XIASS Tools backup. It never
// restores the active config.toml.
func (a *App) ImportCodexLegacyConfigBackup(sourceID string) CodexConfigurationStatus {
	manager, err := a.codexManager()
	if err != nil {
		return codexConfigurationUnavailableStatus()
	}
	if _, err := manager.ImportLegacyConfigBackup(strings.TrimSpace(sourceID)); err != nil {
		return codexConfigurationAfterError(manager, "导入旧版 Codex 配置备份失败：该备份未通过安全校验或导入未完成。")
	}
	status := a.GetCodexConfiguration()
	if !status.OK {
		status.Message = "旧版 Codex 配置备份已安全导入，但当前状态刷新失败；请重新打开此页面查看备份。"
		return status
	}
	status.Message = "已导入旧版 Codex 配置备份，现可在配置备份中恢复。"
	return status
}

// ImportCodexLegacyHistoryBackup stages one verified, completed first-party
// XIASS Codex Helper history archive. It never restores or writes active
// Codex sessions, SQLite databases, configuration, or workspace state.
func (a *App) ImportCodexLegacyHistoryBackup(sourceID string) CodexConfigurationStatus {
	manager, err := a.codexManager()
	if err != nil {
		return CodexConfigurationStatus{OK: false, Message: "无法识别本机 Codex 配置目录：" + err.Error(), LegacyBackups: emptyCodexLegacyBackups()}
	}
	if _, err := manager.ImportLegacyHistoryBackup(strings.TrimSpace(sourceID)); err != nil {
		return codexConfigurationAfterError(manager, "导入旧版 Codex 历史备份失败：该备份未通过安全校验或导入未完成。")
	}
	status := a.GetCodexConfiguration()
	if !status.OK {
		status.Message = "旧版 Codex 历史备份已安全导入，但当前状态刷新失败；请重新打开此页面查看备份。"
		return status
	}
	status.Message = "已导入旧版 Codex 历史备份，现可在历史备份中恢复。"
	return status
}

// RepairCodexHistory is intentionally explicit. It repairs provider metadata
// and incompatible local response records only after the user asks for it;
// normal configuration saves never rewrite conversation history.
func (a *App) RepairCodexHistory(compatibility bool) CodexHistoryRepairStatus {
	manager, err := a.codexManager()
	if err != nil {
		return CodexHistoryRepairStatus{OK: false, Message: "无法识别本机 Codex 配置目录；历史未被修改。"}
	}
	repairer := codexconfig.NewHistoryRepairerWithManager(manager)
	var result codexconfig.HistoryRepairResult
	if compatibility {
		result, err = repairer.RepairCurrentProviderCompatibility()
	} else {
		result, err = repairer.RepairCurrentProvider()
	}
	if err != nil {
		return CodexHistoryRepairStatus{OK: false, Message: "Codex 历史修复未完成；系统已保留可恢复备份，当前内容未被进一步修改。", Result: result}
	}
	if result.Skipped {
		return CodexHistoryRepairStatus{OK: true, Message: "已检查 Codex 历史，无需修改：" + result.SkipReason, Result: result}
	}
	return CodexHistoryRepairStatus{OK: true, Message: "Codex 历史已安全检查并完成必要修复。", Result: result}
}

func (a *App) RestoreCodexHistoryBackup(backupID string) CodexConfigurationStatus {
	manager, err := a.codexManager()
	if err != nil {
		return codexConfigurationUnavailableStatus()
	}
	if err := codexconfig.NewHistoryRepairerWithManager(manager).RestoreBackup(strings.TrimSpace(backupID)); err != nil {
		return codexConfigurationAfterError(manager, "未恢复 Codex 历史备份。该备份可能已损坏、已被删除或当前数据不再适用。")
	}
	status := a.GetCodexConfiguration()
	if status.OK {
		status.Message = "已恢复选定的 Codex 历史备份。"
	}
	return status
}

func (a *App) DeleteCodexHistoryBackup(backupID string) CodexConfigurationStatus {
	manager, err := a.codexManager()
	if err != nil {
		return codexConfigurationUnavailableStatus()
	}
	if err := codexconfig.NewHistoryRepairerWithManager(manager).DeleteBackup(strings.TrimSpace(backupID)); err != nil {
		return codexConfigurationAfterError(manager, "未删除 Codex 历史备份。只可删除通过校验的备份。")
	}
	status := a.GetCodexConfiguration()
	if status.OK {
		status.Message = "已删除选定的 Codex 历史备份。"
	}
	return status
}

func codexConfigurationAfterError(manager *codexconfig.Manager, message string) CodexConfigurationStatus {
	snapshot, _ := manager.Inspect()
	backups, _ := manager.ListBackups()
	historyBackups, _ := codexconfig.NewHistoryRepairerWithManager(manager).ListBackups()
	legacyBackups, legacyWarning := codexLegacyBackupStatus(manager)
	return codexConfigurationStatusForRenderer(CodexConfigurationStatus{OK: false, Message: message, Snapshot: snapshot, Backups: backups, HistoryBackups: historyBackups, LegacyBackups: legacyBackups, LegacyBackupWarning: legacyWarning})
}

// codexConfigurationRemovalStatus is the narrow post-disconnect projection.
// It reads the verified config and its config backups only. In particular it
// does not call any history repair/listing, scan legacy archives, access
// auth.json, or perform a Codex Desktop lifecycle observation.
func codexConfigurationRemovalStatus(manager *codexconfig.Manager) CodexConfigurationStatus {
	snapshot, inspectErr := manager.Inspect()
	backups, backupErr := manager.ListBackups()
	if inspectErr != nil {
		return codexConfigurationStatusForRenderer(CodexConfigurationStatus{OK: false, Message: "XIASS Tools Provider 操作已完成，但无法安全刷新 config.toml 状态。", Snapshot: snapshot, Backups: backups, LegacyBackups: emptyCodexLegacyBackups()})
	}
	if backupErr != nil {
		return codexConfigurationStatusForRenderer(CodexConfigurationStatus{OK: false, Message: "XIASS Tools Provider 操作已完成，但配置备份目录未通过安全检查。", Snapshot: snapshot, LegacyBackups: emptyCodexLegacyBackups()})
	}
	return codexConfigurationStatusForRenderer(CodexConfigurationStatus{OK: true, Snapshot: snapshot, Backups: backups, LegacyBackups: emptyCodexLegacyBackups()})
}

func codexConfigurationRemovalAfterError(manager *codexconfig.Manager, message string) CodexConfigurationStatus {
	status := codexConfigurationRemovalStatus(manager)
	status.OK = false
	status.Message = message
	return status
}

// codexConfigurationMigrationStatus mirrors the narrow disconnect refresh.
// Migration is a config-only operation: it must not enumerate or mutate
// history, legacy archives, authentication material, or Desktop lifecycle
// state just to render an acknowledgement.
func codexConfigurationMigrationStatus(manager *codexconfig.Manager) CodexConfigurationStatus {
	return codexConfigurationRemovalStatus(manager)
}

func codexConfigurationMigrationAfterError(manager *codexconfig.Manager, message string) CodexConfigurationStatus {
	status := codexConfigurationMigrationStatus(manager)
	status.OK = false
	status.Message = message
	return status
}

// codexConfigurationStatusForRenderer removes machine-specific paths before a
// snapshot crosses the Wails boundary. The UI only needs the existence flag;
// exposing CODEX_HOME or config.toml locations would let renderer state retain
// local filesystem details with no user-facing benefit.
func codexConfigurationStatusForRenderer(status CodexConfigurationStatus) CodexConfigurationStatus {
	status.Snapshot.Location.CodexHome = ""
	status.Snapshot.Location.ConfigPath = ""
	return status
}

func codexConfigurationUnavailableStatus() CodexConfigurationStatus {
	return CodexConfigurationStatus{
		OK:            false,
		Message:       "无法识别本机 Codex 配置目录。请确认 CODEX_HOME 或默认配置目录可用。",
		LegacyBackups: emptyCodexLegacyBackups(),
	}
}

func codexLegacyBackupStatus(manager *codexconfig.Manager) ([]codexconfig.LegacyBackupInfo, string) {
	backups, err := manager.ListLegacyBackups()
	if err != nil {
		return emptyCodexLegacyBackups(), "检测到旧版 XIASS Codex Helper 备份目录异常，已跳过旧备份导入；当前 Codex 配置不受影响。"
	}
	if backups == nil {
		backups = emptyCodexLegacyBackups()
	}
	return backups, ""
}

func emptyCodexLegacyBackups() []codexconfig.LegacyBackupInfo {
	return []codexconfig.LegacyBackupInfo{}
}

func formatCodexModelCount(count int) string {
	return fmt.Sprintf("%d", count)
}
