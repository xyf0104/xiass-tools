package main

import (
	"context"
	"strings"

	"antigravity-wf-assistant/internal/codexconfig"
	"antigravity-wf-assistant/internal/codexdesktop"
)

// CodexLegacyProviderMigrationLifecycleStatus is the renderer-safe result of
// the opt-in migration transaction. It contains only booleans and existing
// redacted configuration/Desktop projections; credentials, raw TOML, paths,
// process IDs, OAuth, sessions, and history never cross this boundary.
type CodexLegacyProviderMigrationLifecycleStatus struct {
	OK                  bool                      `json:"ok"`
	Message             string                    `json:"message"`
	Configuration       CodexConfigurationStatus  `json:"configuration"`
	Desktop             CodexDesktopControlStatus `json:"desktop"`
	PriorDesktopRunning bool                      `json:"priorDesktopRunning"`
	DesktopStopped      bool                      `json:"desktopStopped"`
	Migrated            bool                      `json:"migrated"`
	RolledBack          bool                      `json:"rolledBack"`
	Relaunched          bool                      `json:"relaunched"`
}

type codexLegacyProviderMigrationLifecycleOperations struct {
	desktop codexDesktopControlService
	inspect func() (codexconfig.ConfigSnapshot, error)
	migrate func() (codexconfig.LegacyProviderMigrationResult, error)
	restore func(string) (codexconfig.RestoreResult, error)
	refresh func() CodexConfigurationStatus
}

// MigrateCodexLegacyProviderWithLifecycle is the only migration entry point
// that may touch config.toml after XIASS Tools has observed a running Codex
// Desktop. The caller must explicitly confirm the normal exit/relaunch
// transaction; failure to stop or re-observe Codex is a hard no-write boundary.
// It never scans or rewrites Codex history as a side effect.
func (a *App) MigrateCodexLegacyProviderWithLifecycle(confirmation string) CodexLegacyProviderMigrationLifecycleStatus {
	if a == nil || a.ctx == nil {
		return CodexLegacyProviderMigrationLifecycleStatus{OK: false, Message: "助手尚未完成启动，暂时无法迁移 Codex Provider。"}
	}
	a.codexDesktopOperation.Lock()
	defer a.codexDesktopOperation.Unlock()

	manager, err := a.codexManager()
	if err != nil {
		return CodexLegacyProviderMigrationLifecycleStatus{OK: false, Message: "无法识别本机 Codex 配置目录；未执行任何操作。"}
	}
	operations := codexLegacyProviderMigrationLifecycleOperations{
		desktop: a.codexDesktopController(),
		inspect: manager.Inspect,
		migrate: manager.MigrateLegacyProvider,
		restore: func(backupID string) (codexconfig.RestoreResult, error) {
			return manager.Restore(strings.TrimSpace(backupID))
		},
		refresh: func() CodexConfigurationStatus {
			return codexConfigurationMigrationStatus(manager)
		},
	}
	return runCodexLegacyProviderMigrationWithLifecycle(a.codexDesktopContext(), strings.TrimSpace(confirmation), operations)
}

func runCodexLegacyProviderMigrationWithLifecycle(ctx context.Context, confirmation string, operations codexLegacyProviderMigrationLifecycleOperations) CodexLegacyProviderMigrationLifecycleStatus {
	if ctx == nil {
		ctx = context.Background()
	}
	result := CodexLegacyProviderMigrationLifecycleStatus{}
	if operations.desktop == nil || operations.inspect == nil || operations.migrate == nil || operations.restore == nil {
		return legacyProviderMigrationLifecycleFailure(result, operations, "Codex 旧 Provider 迁移事务不可用；未修改配置。")
	}

	desktopBefore := operations.desktop.Status(ctx)
	result.Desktop = codexDesktopStatusForRenderer(desktopBefore, true, "")
	result.PriorDesktopRunning = desktopBefore.Running
	if hasCodexDesktopProcessWarning(desktopBefore) {
		return legacyProviderMigrationLifecycleFailure(result, operations, "无法安全确认 Codex Desktop 运行状态；未修改配置。")
	}
	if desktopBefore.Running {
		if !desktopBefore.CanStop {
			return legacyProviderMigrationLifecycleFailure(result, operations, "Codex Desktop 正在运行但无法安全请求退出；未修改配置。")
		}
		if confirmation != codexdesktop.LifecycleConfirmation {
			return legacyProviderMigrationLifecycleFailure(result, operations, "Codex Desktop 正在运行，请先确认退出后再迁移旧 Provider。")
		}
	}

	before, err := operations.inspect()
	if err != nil || !before.Valid || !before.LegacyProviderMigration.Available {
		return legacyProviderMigrationLifecycleFailure(result, operations, "未发现可安全迁移的旧版 XIASS Codex Provider；未修改配置。")
	}

	if desktopBefore.Running {
		stopped, stopErr := operations.desktop.Stop(ctx, confirmation)
		result.Desktop = codexDesktopStatusForRenderer(stopped, stopErr == nil, "")
		if stopErr != nil || stopped.Running || hasCodexDesktopProcessWarning(stopped) {
			return legacyProviderMigrationLifecycleFailure(result, operations, "未能确认 Codex Desktop 已退出；未修改配置，也未强制结束进程。")
		}
		result.DesktopStopped = true
	}

	migration, migrationErr := operations.migrate()
	if migrationErr != nil {
		result.RolledBack = legacyProviderMigrationConfigMatches(before, operations.inspect)
		if result.RolledBack {
			result = relaunchPriorDesktopAfterLegacyMigration(ctx, result, operations)
		}
		if !result.RolledBack {
			return legacyProviderMigrationLifecycleFailure(result, operations, "旧 Provider 迁移未完成，且无法验证配置已恢复；Codex Desktop 已保持关闭。")
		}
		return legacyProviderMigrationLifecycleFailure(result, operations, "旧 Provider 迁移未完成；已验证原配置保持不变。")
	}
	if !migration.Migrated {
		// The config may have changed after the initial read but before the
		// native lock. It is a no-op, not a reason to leave a previously
		// running user app closed.
		result.RolledBack = legacyProviderMigrationConfigMatches(before, operations.inspect)
		if result.RolledBack {
			result = relaunchPriorDesktopAfterLegacyMigration(ctx, result, operations)
		}
		return legacyProviderMigrationLifecycleFailure(result, operations, "未发现可安全迁移的旧版 XIASS Codex Provider；当前配置没有被更改。")
	}
	result.Migrated = true

	if result.PriorDesktopRunning {
		launched, launchErr := operations.desktop.Launch(ctx)
		result.Desktop = codexDesktopStatusForRenderer(launched, launchErr == nil, "")
		if launchErr != nil || !launched.Running || hasCodexDesktopProcessWarning(launched) {
			result.RolledBack = restoreLegacyProviderMigrationAfterFailure(operations, before, migration)
			if result.RolledBack {
				result = relaunchPriorDesktopAfterLegacyMigration(ctx, result, operations)
			}
			if !result.RolledBack {
				return legacyProviderMigrationLifecycleFailure(result, operations, "Codex Desktop 未能安全启动，且无法验证原配置已恢复；Codex 已保持关闭。")
			}
			return legacyProviderMigrationLifecycleFailure(result, operations, "Codex Desktop 未能安全启动；旧 Provider 迁移已回滚。")
		}
		result.Relaunched = true
	}

	result.OK = true
	result.Message = "旧版 XIASS Codex Provider 已安全迁移至 XIASS Tools。"
	result.Configuration = legacyProviderMigrationLifecycleRefresh(operations)
	return result
}

func legacyProviderMigrationConfigMatches(before codexconfig.ConfigSnapshot, inspect func() (codexconfig.ConfigSnapshot, error)) bool {
	if inspect == nil {
		return false
	}
	after, err := inspect()
	return err == nil && lifecycleConfigMatches(before, after)
}

func restoreLegacyProviderMigrationAfterFailure(operations codexLegacyProviderMigrationLifecycleOperations, before codexconfig.ConfigSnapshot, migration codexconfig.LegacyProviderMigrationResult) bool {
	if strings.TrimSpace(migration.BackupID) == "" {
		return false
	}
	if _, err := operations.restore(migration.BackupID); err != nil {
		return false
	}
	return legacyProviderMigrationConfigMatches(before, operations.inspect)
}

func relaunchPriorDesktopAfterLegacyMigration(ctx context.Context, result CodexLegacyProviderMigrationLifecycleStatus, operations codexLegacyProviderMigrationLifecycleOperations) CodexLegacyProviderMigrationLifecycleStatus {
	if !result.PriorDesktopRunning || !result.DesktopStopped || !result.RolledBack || operations.desktop == nil {
		return result
	}
	launched, launchErr := operations.desktop.Launch(ctx)
	result.Desktop = codexDesktopStatusForRenderer(launched, launchErr == nil, "")
	if launchErr == nil && launched.Running && !hasCodexDesktopProcessWarning(launched) {
		result.Relaunched = true
	}
	return result
}

func legacyProviderMigrationLifecycleRefresh(operations codexLegacyProviderMigrationLifecycleOperations) CodexConfigurationStatus {
	if operations.refresh == nil {
		return CodexConfigurationStatus{}
	}
	return codexConfigurationStatusForRenderer(operations.refresh())
}

func legacyProviderMigrationLifecycleFailure(result CodexLegacyProviderMigrationLifecycleStatus, operations codexLegacyProviderMigrationLifecycleOperations, message string) CodexLegacyProviderMigrationLifecycleStatus {
	result.OK = false
	result.Message = message
	result.Configuration = legacyProviderMigrationLifecycleRefresh(operations)
	return result
}
