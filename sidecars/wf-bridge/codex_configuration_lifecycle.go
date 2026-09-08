package main

import (
	"context"
	"strings"

	"antigravity-wf-assistant/internal/codexconfig"
	"antigravity-wf-assistant/internal/codexdesktop"
)

// CodexConfigurationLifecycleInput is the explicit, opt-in transaction form
// of Codex configuration apply. Config.APIKey is accepted only for this call;
// it is never copied into the response, an App field, a log, or a diagnostic.
//
// A running Codex Desktop requires Confirmation to equal
// codexdesktop.LifecycleConfirmation. When the desktop is not running,
// Confirmation is ignored and no stop operation is attempted.
type CodexConfigurationLifecycleInput struct {
	Config                        codexconfig.ApplyConfig `json:"config"`
	Confirmation                  string                  `json:"confirmation,omitempty"`
	RepairHistoryOnProviderChange bool                    `json:"repairHistoryOnProviderChange"`
	LaunchAfter                   bool                    `json:"launchAfter"`
}

// CodexConfigurationLifecycleStatus is a renderer-safe transaction result.
// It reports only booleans and the existing redacted configuration/desktop
// projections. It never contains API keys, paths, process IDs, command lines,
// raw errors, or history contents.
type CodexConfigurationLifecycleStatus struct {
	OK                     bool                      `json:"ok"`
	Message                string                    `json:"message"`
	Configuration          CodexConfigurationStatus  `json:"configuration"`
	Desktop                CodexDesktopControlStatus `json:"desktop"`
	PriorDesktopRunning    bool                      `json:"priorDesktopRunning"`
	DesktopStopped         bool                      `json:"desktopStopped"`
	Applied                bool                      `json:"applied"`
	HistoryRepairAttempted bool                      `json:"historyRepairAttempted"`
	HistoryRepairSkipped   bool                      `json:"historyRepairSkipped"`
	HistoryRepaired        bool                      `json:"historyRepaired"`
	RolledBack             bool                      `json:"rolledBack"`
	Relaunched             bool                      `json:"relaunched"`
}

// codexLifecycleOperations is a deliberately narrow seam around concrete
// managers. Production wires it to codexconfig.Manager and the verified
// codexdesktop.Controller; tests can model failures without starting or
// stopping a real desktop application.
type codexLifecycleOperations struct {
	desktop        codexDesktopControlService
	inspect        func() (codexconfig.ConfigSnapshot, error)
	apply          func(codexconfig.ApplyConfig) (codexconfig.ApplyResult, error)
	restoreConfig  func(string) (codexconfig.RestoreResult, error)
	repairHistory  func() (codexconfig.HistoryRepairResult, error)
	restoreHistory func(string) error
	refresh        func() CodexConfigurationStatus
}

// ApplyCodexConfigurationWithLifecycle performs one user-confirmed lifecycle
// transaction. The legacy ApplyCodexConfiguration method is intentionally
// unchanged and remains available for callers that want no process control.
func (a *App) ApplyCodexConfigurationWithLifecycle(input CodexConfigurationLifecycleInput) CodexConfigurationLifecycleStatus {
	if a == nil || a.ctx == nil {
		return CodexConfigurationLifecycleStatus{OK: false, Message: "助手尚未完成启动，暂时无法应用 Codex 配置。"}
	}
	a.codexDesktopOperation.Lock()
	defer a.codexDesktopOperation.Unlock()

	manager, err := a.codexManager()
	if err != nil {
		return CodexConfigurationLifecycleStatus{OK: false, Message: "无法识别本机 Codex 配置目录；未执行任何操作。"}
	}
	return a.applyCodexConfigurationWithLifecycleLocked(input, manager, manager.Apply)
}

// applyCodexConfigurationWithLifecycleLocked is shared by direct API-key
// configuration and the XIASS native key-selection flow. Its caller owns
// codexDesktopOperation for the entire transaction; this keeps the selected
// credential in native memory only while the configuration mutation runs.
func (a *App) applyCodexConfigurationWithLifecycleLocked(input CodexConfigurationLifecycleInput, manager *codexconfig.Manager, apply func(codexconfig.ApplyConfig) (codexconfig.ApplyResult, error)) CodexConfigurationLifecycleStatus {
	if a == nil || manager == nil || apply == nil {
		return CodexConfigurationLifecycleStatus{OK: false, Message: "Codex 生命周期事务不可用；未修改配置。"}
	}
	historyRepairer := codexconfig.NewHistoryRepairerWithManager(manager)
	if strings.TrimSpace(input.Config.ProviderID) == "" {
		input.Config.ProviderID = manager.ProviderID
	}
	operations := codexLifecycleOperations{
		desktop: a.codexDesktopController(),
		inspect: manager.Inspect,
		apply:   apply,
		restoreConfig: func(backupID string) (codexconfig.RestoreResult, error) {
			return manager.Restore(strings.TrimSpace(backupID))
		},
		repairHistory: historyRepairer.RepairCurrentProviderCompatibility,
		restoreHistory: func(backupID string) error {
			return historyRepairer.RestoreBackup(strings.TrimSpace(backupID))
		},
		refresh: a.GetCodexConfiguration,
	}
	return runCodexConfigurationWithLifecycle(a.codexDesktopContext(), input, operations)
}

func runCodexConfigurationWithLifecycle(ctx context.Context, input CodexConfigurationLifecycleInput, operations codexLifecycleOperations) CodexConfigurationLifecycleStatus {
	if ctx == nil {
		ctx = context.Background()
	}
	result := CodexConfigurationLifecycleStatus{}
	if operations.desktop == nil || operations.inspect == nil || operations.apply == nil || operations.restoreConfig == nil || operations.repairHistory == nil || operations.restoreHistory == nil {
		return lifecycleFailure(result, operations, "Codex 生命周期事务不可用；未修改配置。")
	}

	// Capture the desktop state before validating or mutating Codex files. A
	// process-list warning means we cannot prove that a running app is absent;
	// in that case no configuration write is allowed.
	desktopBefore := operations.desktop.Status(ctx)
	result.Desktop = codexDesktopStatusForRenderer(desktopBefore, true, "")
	result.PriorDesktopRunning = desktopBefore.Running
	if hasCodexDesktopProcessWarning(desktopBefore) {
		return lifecycleFailure(result, operations, "无法安全确认 Codex Desktop 运行状态；未修改配置。")
	}
	if desktopBefore.Running {
		if !desktopBefore.CanStop {
			return lifecycleFailure(result, operations, "Codex Desktop 正在运行但无法安全请求退出；未修改配置。")
		}
		if strings.TrimSpace(input.Confirmation) != codexdesktop.LifecycleConfirmation {
			return lifecycleFailure(result, operations, "Codex Desktop 正在运行，请先确认退出后再应用配置。")
		}
	}

	normalized, err := codexconfig.NormalizeApplyConfig(input.Config)
	if err != nil {
		return lifecycleFailure(result, operations, "Codex 配置输入未通过校验；未修改配置。")
	}
	// The normalized API key is needed only by the immediate apply call. Clear
	// our local copy as soon as the call returns, regardless of its outcome.
	defer func() { normalized.APIKey = "" }()

	beforeSnapshot, err := operations.inspect()
	if err != nil || !beforeSnapshot.Valid {
		return lifecycleFailure(result, operations, "现有 Codex 配置无法安全读取；未修改配置。")
	}
	previousProvider := strings.TrimSpace(beforeSnapshot.ModelProvider)
	providerChanged := previousProvider != strings.TrimSpace(normalized.ProviderID)

	if desktopBefore.Running {
		stoppedStatus, stopErr := operations.desktop.Stop(ctx, input.Confirmation)
		result.Desktop = codexDesktopStatusForRenderer(stoppedStatus, stopErr == nil, "")
		if stopErr != nil || stoppedStatus.Running || hasCodexDesktopProcessWarning(stoppedStatus) {
			// A failed or unverified stop is a hard boundary: no config or
			// history mutation, and importantly no automatic launch attempt.
			return lifecycleFailure(result, operations, "未能确认 Codex Desktop 已退出；未修改配置，也未强制结束进程。")
		}
		result.DesktopStopped = true
	}

	applyResult, applyErr := operations.apply(normalized)
	if applyErr != nil {
		// Do not rely solely on Manager.Apply's atomic-write guarantee. The
		// operation may have failed after a partial state change, or a future
		// implementation may return a backup ID alongside an error. Re-inspect
		// and compare against the snapshot taken before the transaction before
		// treating the rollback as verified or restoring the prior app state.
		result.RolledBack = restoreConfigAfterFailure(operations, beforeSnapshot, applyResult)
		if mayRestorePriorDesktop(result) {
			result = relaunchPriorDesktop(ctx, result, operations)
		}
		if !result.RolledBack {
			return lifecycleFailure(result, operations, "Codex 配置未能安全保存，且无法验证配置已恢复；Codex Desktop 已保持关闭，请先恢复配置后再启动。")
		}
		return lifecycleFailure(result, operations, "Codex 配置未能安全保存；已验证恢复原配置。")
	}
	result.Applied = true

	var completedHistoryRepair *codexconfig.HistoryRepairResult
	if providerChanged && input.RepairHistoryOnProviderChange {
		result.HistoryRepairAttempted = true
		historyResult, historyErr := operations.repairHistory()
		if historyErr != nil {
			historyRestored := restoreHistoryAfterFailure(operations, historyResult)
			configRestored := restoreConfigAfterFailure(operations, beforeSnapshot, applyResult)
			result.RolledBack = historyRestored && configRestored
			if mayRestorePriorDesktop(result) {
				result = relaunchPriorDesktop(ctx, result, operations)
			}
			if !result.RolledBack {
				return lifecycleFailure(result, operations, "供应商已变更，但历史兼容修复未完成，且无法验证配置与历史已恢复；Codex Desktop 已保持关闭，请先恢复后再启动。")
			}
			return lifecycleFailure(result, operations, "供应商已变更，但历史兼容修复未完成；已停止继续操作。")
		}
		completedHistoryRepair = &historyResult
		result.HistoryRepaired = !historyResult.Skipped
	} else if providerChanged {
		// The caller explicitly chose not to rewrite history. This is a safe,
		// observable skip and does not pretend that history was repaired.
		result.HistoryRepairSkipped = true
	}

	shouldLaunch := result.PriorDesktopRunning || input.LaunchAfter
	if shouldLaunch {
		if !desktopBefore.Discovered {
			// An explicit launch request without a verified installation cannot
			// be fulfilled. Roll back the config so the composite operation is
			// still transactional rather than silently partial.
			result.RolledBack = rollbackLifecycleMutations(operations, beforeSnapshot, applyResult, completedHistoryRepair)
			if !result.RolledBack {
				return lifecycleFailure(result, operations, "未找到可验证的 Codex Desktop，且无法验证配置已恢复；请先恢复配置后再启动。")
			}
			return lifecycleFailure(result, operations, "未找到可验证的 Codex Desktop，配置已回滚。")
		}
		launchedStatus, launchErr := operations.desktop.Launch(ctx)
		result.Desktop = codexDesktopStatusForRenderer(launchedStatus, launchErr == nil, "")
		if launchErr != nil || !launchedStatus.Running || hasCodexDesktopProcessWarning(launchedStatus) {
			// A failed launch after a stopped transaction is treated as a failed
			// composite operation. Restore history first, then config; only a
			// fully verified rollback permits restoring the prior running state.
			result.RolledBack = rollbackLifecycleMutations(operations, beforeSnapshot, applyResult, completedHistoryRepair)
			if mayRestorePriorDesktop(result) {
				result = relaunchPriorDesktop(ctx, result, operations)
			}
			if !result.RolledBack {
				return lifecycleFailure(result, operations, "Codex Desktop 未能安全启动，且无法验证配置与历史已恢复；已保持关闭，请先恢复后再启动。")
			}
			return lifecycleFailure(result, operations, "Codex Desktop 未能安全启动；配置已回滚。")
		}
		result.Relaunched = true
	}

	result.OK = true
	result.Message = "Codex 配置已安全保存。"
	result.Configuration = refreshLifecycleConfiguration(operations)
	return result
}

func relaunchPriorDesktop(ctx context.Context, result CodexConfigurationLifecycleStatus, operations codexLifecycleOperations) CodexConfigurationLifecycleStatus {
	launchedStatus, launchErr := operations.desktop.Launch(ctx)
	result.Desktop = codexDesktopStatusForRenderer(launchedStatus, launchErr == nil, "")
	if launchErr == nil && launchedStatus.Running && !hasCodexDesktopProcessWarning(launchedStatus) {
		result.Relaunched = true
	}
	return result
}

// mayRestorePriorDesktop deliberately requires a verified rollback. If a
// configuration or history write might remain half-applied, opening Codex
// again could cause it to read or extend that state. Leaving it stopped makes
// recovery explicit and keeps the transaction fail-closed.
func mayRestorePriorDesktop(result CodexConfigurationLifecycleStatus) bool {
	return result.PriorDesktopRunning && result.DesktopStopped && result.RolledBack &&
		!result.Desktop.Running && !hasRendererCodexDesktopProcessWarning(result.Desktop)
}

func hasRendererCodexDesktopProcessWarning(status CodexDesktopControlStatus) bool {
	for _, warning := range status.Warnings {
		if warning == codexdesktop.WarningProcessListUnavailable {
			return true
		}
	}
	return false
}

// restoreConfigAfterFailure restores the backup when one is available and
// always compares a fresh redacted inspection with the pre-transaction
// snapshot. A missing backup ID is not proof that no write occurred.
func restoreConfigAfterFailure(operations codexLifecycleOperations, before codexconfig.ConfigSnapshot, applied codexconfig.ApplyResult) bool {
	if strings.TrimSpace(applied.BackupID) != "" {
		if _, err := operations.restoreConfig(applied.BackupID); err != nil {
			return false
		}
	}
	after, err := operations.inspect()
	return err == nil && lifecycleConfigMatches(before, after)
}

// lifecycleConfigMatches compares only the configuration facts that are
// meaningful to the lifecycle boundary. It intentionally compares no secret
// fields: ConfigSnapshot is already redacted.
func lifecycleConfigMatches(before, after codexconfig.ConfigSnapshot) bool {
	return before.Location.Exists == after.Location.Exists &&
		before.Valid == after.Valid &&
		before.SHA256 == after.SHA256 &&
		before.ModelProvider == after.ModelProvider &&
		before.Model == after.Model &&
		before.ReviewModel == after.ReviewModel &&
		before.WebSearch == after.WebSearch &&
		before.Context == after.Context
}

func restoreHistoryAfterFailure(operations codexLifecycleOperations, repaired codexconfig.HistoryRepairResult) bool {
	// Compatibility repair may also normalize Codex's workspace-state cache.
	// The current history-backup restore primitive does not restore that
	// separate cache. We still restore any ordinary history backup below, but
	// must not claim a complete rollback or relaunch Codex when workspace state
	// was changed and cannot be independently verified as restored.
	workspaceStateUnrestored := repaired.WorkspaceState != nil && repaired.WorkspaceState.Updated
	if repaired.Skipped {
		// No backup means no verified history mutation to restore. This is
		// true for a skipped repair and for a repair implementation that
		// reports no changes.
		return !workspaceStateUnrestored
	}
	if strings.TrimSpace(repaired.BackupID) == "" {
		return false
	}
	return operations.restoreHistory(repaired.BackupID) == nil && !workspaceStateUnrestored
}

func rollbackLifecycleMutations(operations codexLifecycleOperations, before codexconfig.ConfigSnapshot, applied codexconfig.ApplyResult, historyResult *codexconfig.HistoryRepairResult) bool {
	// Restore dependent history while the newly-applied configuration is still
	// present, then restore the original configuration. Both recoveries are
	// attempted even if the first one fails so the caller gets the safest
	// attainable state, but success is reported only when both are verified.
	historyOK := true
	if historyResult != nil {
		historyOK = restoreHistoryAfterFailure(operations, *historyResult)
	}
	configOK := restoreConfigAfterFailure(operations, before, applied)
	return historyOK && configOK
}

func refreshLifecycleConfiguration(operations codexLifecycleOperations) CodexConfigurationStatus {
	if operations.refresh == nil {
		return CodexConfigurationStatus{}
	}
	// Keep this boundary defensive even though production refresh currently
	// calls GetCodexConfiguration, which already redacts paths. A future
	// refresh implementation must not be able to make lifecycle responses
	// retain a local Codex home or config.toml path in the renderer.
	return codexConfigurationStatusForRenderer(operations.refresh())
}

func lifecycleFailure(result CodexConfigurationLifecycleStatus, operations codexLifecycleOperations, message string) CodexConfigurationLifecycleStatus {
	result.OK = false
	result.Message = message
	result.Configuration = refreshLifecycleConfiguration(operations)
	return result
}

func hasCodexDesktopProcessWarning(status codexdesktop.ControlStatus) bool {
	for _, warning := range status.Warnings {
		if warning == codexdesktop.WarningProcessListUnavailable {
			return true
		}
	}
	return false
}
