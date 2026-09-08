package main

import (
	"errors"
	"strings"

	"antigravity-wf-assistant/internal/agent"
	"antigravity-wf-assistant/internal/agentdiscovery"
	"antigravity-wf-assistant/internal/mcpconfig"
)

// MCPConfigurationInput is intentionally limited to a remote endpoint. XIASS
// Tools does not collect, retain, or write headers, environment values, OAuth
// material, cookies, API keys, or other credentials into Cursor/Windsurf MCP
// configuration files.
type MCPConfigurationInput struct {
	Target    string `json:"target"`
	RemoteURL string `json:"remoteUrl"`
}

// MCPRemoteInput is the target-scoped Wails input used by Cursor and
// Windsurf actions. The remote address is inbound-only: it is passed to the
// manager for this one operation and is never included in a result, backup,
// diagnostic, or App field.
type MCPRemoteInput struct {
	RemoteURL string `json:"remoteUrl"`
}

// MCPConfigurationStatus is safe for the renderer: the configuration snapshot
// deliberately omits paths, endpoint values, server command lines, headers,
// env entries, backup IDs, and all secret material.
type MCPConfigurationStatus struct {
	OK             bool               `json:"ok"`
	Message        string             `json:"message"`
	ClientDetected bool               `json:"clientDetected"`
	CanApply       bool               `json:"canApply"`
	Snapshot       mcpconfig.Snapshot `json:"snapshot"`
}

// MCPRemoveStatus confirms an explicit request to remove only the exact
// xiass-tools MCP entry. It contains no endpoint, path, raw configuration, or
// information about any other MCP entry.
type MCPRemoveStatus struct {
	OK      bool                   `json:"ok"`
	Message string                 `json:"message"`
	Result  mcpconfig.RemoveResult `json:"result"`
}

// MCPBackupListStatus is a renderer-safe recovery-point listing. BackupInfo
// intentionally contains only an opaque ID, creation time, reason, and
// whether the original file existed; it never contains configuration data.
type MCPBackupListStatus struct {
	OK      bool                   `json:"ok"`
	Message string                 `json:"message"`
	Backups []mcpconfig.BackupInfo `json:"backups"`
}

// MCPRestoreStatus exposes only the redacted result of an explicit restore.
// It cannot reveal an endpoint, path, raw JSON, header, token, account, or
// OAuth material.
type MCPRestoreStatus struct {
	OK      bool                    `json:"ok"`
	Message string                  `json:"message"`
	Result  mcpconfig.RestoreResult `json:"result"`
}

// MCPBackupDeleteStatus confirms an explicit recovery-point deletion without
// returning the selected ID or any filesystem information.
type MCPBackupDeleteStatus struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// GetCursorMCPConfiguration reads only Cursor's documented global MCP
// configuration. It is target-scoped so renderer input cannot redirect this
// operation to another application or an arbitrary file.
func (a *App) GetCursorMCPConfiguration() MCPConfigurationStatus {
	return a.mcpConfigurationStatus(mcpconfig.TargetCursor)
}

// ApplyCursorMCPConfiguration explicitly adds or updates only the reserved
// XIASS Tools remote entry in Cursor's documented global MCP configuration.
func (a *App) ApplyCursorMCPConfiguration(input MCPRemoteInput) MCPConfigurationStatus {
	remoteURL := input.RemoteURL
	defer func() {
		input.RemoteURL = ""
		remoteURL = ""
	}()
	return a.applyMCPConfigurationTarget(mcpconfig.TargetCursor, remoteURL)
}

// RemoveCursorMCPConfiguration removes only the reserved xiass-tools entry
// from Cursor's documented global MCP configuration. It never accepts a
// renderer-supplied server ID or file path.
func (a *App) RemoveCursorMCPConfiguration() MCPRemoveStatus {
	return a.removeMCPConfigurationTarget(mcpconfig.TargetCursor)
}

// ListCursorMCPBackups lists only checksum-verified XIASS Tools recovery
// points for Cursor. This read-only action does not require Cursor to be
// installed and never creates a backup directory.
func (a *App) ListCursorMCPBackups() MCPBackupListStatus {
	return a.listMCPBackupsTarget(mcpconfig.TargetCursor)
}

// RestoreCursorMCPBackup explicitly restores one verified Cursor global MCP
// recovery point. It never runs as part of Get or Apply.
func (a *App) RestoreCursorMCPBackup(backupID string) MCPRestoreStatus {
	return a.restoreMCPBackupTarget(mcpconfig.TargetCursor, strings.TrimSpace(backupID))
}

// DeleteCursorMCPBackup explicitly deletes one verified, manager-owned
// Cursor recovery point. It never runs as part of Get or Apply.
func (a *App) DeleteCursorMCPBackup(backupID string) MCPBackupDeleteStatus {
	return a.deleteMCPBackupTarget(mcpconfig.TargetCursor, strings.TrimSpace(backupID))
}

// GetWindsurfMCPConfiguration reads only Windsurf's documented global MCP
// configuration. It is target-scoped so renderer input cannot redirect this
// operation to another application or an arbitrary file.
func (a *App) GetWindsurfMCPConfiguration() MCPConfigurationStatus {
	return a.mcpConfigurationStatus(mcpconfig.TargetWindsurf)
}

// ApplyWindsurfMCPConfiguration explicitly adds or updates only the reserved
// XIASS Tools remote entry in Windsurf's documented global MCP configuration.
func (a *App) ApplyWindsurfMCPConfiguration(input MCPRemoteInput) MCPConfigurationStatus {
	remoteURL := input.RemoteURL
	defer func() {
		input.RemoteURL = ""
		remoteURL = ""
	}()
	return a.applyMCPConfigurationTarget(mcpconfig.TargetWindsurf, remoteURL)
}

// RemoveWindsurfMCPConfiguration removes only the reserved xiass-tools entry
// from Windsurf's documented global MCP configuration. It never accepts a
// renderer-supplied server ID or file path.
func (a *App) RemoveWindsurfMCPConfiguration() MCPRemoveStatus {
	return a.removeMCPConfigurationTarget(mcpconfig.TargetWindsurf)
}

// ListWindsurfMCPBackups lists only checksum-verified XIASS Tools recovery
// points for Windsurf. This read-only action does not require Windsurf to be
// installed and never creates a backup directory.
func (a *App) ListWindsurfMCPBackups() MCPBackupListStatus {
	return a.listMCPBackupsTarget(mcpconfig.TargetWindsurf)
}

// RestoreWindsurfMCPBackup explicitly restores one verified Windsurf global
// MCP recovery point. It never runs as part of Get or Apply.
func (a *App) RestoreWindsurfMCPBackup(backupID string) MCPRestoreStatus {
	return a.restoreMCPBackupTarget(mcpconfig.TargetWindsurf, strings.TrimSpace(backupID))
}

// DeleteWindsurfMCPBackup explicitly deletes one verified, manager-owned
// Windsurf recovery point. It never runs as part of Get or Apply.
func (a *App) DeleteWindsurfMCPBackup(backupID string) MCPBackupDeleteStatus {
	return a.deleteMCPBackupTarget(mcpconfig.TargetWindsurf, strings.TrimSpace(backupID))
}

// GetMCPConfiguration is retained for existing callers. New renderer code
// should use the target-scoped Cursor/Windsurf methods above instead.
func (a *App) GetMCPConfiguration(target string) MCPConfigurationStatus {
	resolved, ok := parseMCPConfigurationTarget(target)
	if !ok {
		return MCPConfigurationStatus{Message: "不支持的 MCP 客户端。"}
	}
	return a.mcpConfigurationStatus(resolved)
}

func (a *App) ApplyMCPConfiguration(input MCPConfigurationInput) MCPConfigurationStatus {
	resolved, ok := parseMCPConfigurationTarget(input.Target)
	if !ok {
		return MCPConfigurationStatus{Message: "不支持的 MCP 客户端。"}
	}
	remoteURL := input.RemoteURL
	defer func() {
		input.RemoteURL = ""
		remoteURL = ""
	}()
	return a.applyMCPConfigurationTarget(resolved, remoteURL)
}

func (a *App) applyMCPConfigurationTarget(target mcpconfig.Target, remoteURL string) MCPConfigurationStatus {
	status := a.mcpConfigurationStatus(target)
	if !status.ClientDetected {
		status.Message = "尚未在本机确认该客户端。为避免创建无效配置，XIASS Tools 不会写入全局 MCP 设置。"
		return status
	}
	if !status.Snapshot.Valid || status.Snapshot.HasSensitiveConfiguration {
		status.Message = "现有全局 MCP 设置无法安全修改。XIASS Tools 不会读取、展示或改写其中的敏感内容。"
		return status
	}
	manager, err := mcpconfig.NewDefaultManager(target)
	if err != nil {
		status.Message = "无法安全定位该客户端的全局 MCP 设置。"
		return status
	}
	applyInput := mcpconfig.ApplyInput{RemoteURL: remoteURL}
	result, err := manager.ApplyRemote(applyInput)
	applyInput.RemoteURL = ""
	remoteURL = ""
	if err != nil {
		status.Message = mcpConfigurationErrorMessage(err)
		return status
	}
	return MCPConfigurationStatus{
		OK:             true,
		Message:        "已安全写入 XIASS Tools 的 MCP 远程配置，并完成写入校验与本地恢复点备份；未测试远端 MCP 服务。",
		ClientDetected: true,
		CanApply:       result.Snapshot.Valid && !result.Snapshot.HasSensitiveConfiguration,
		Snapshot:       result.Snapshot,
	}
}

func (a *App) removeMCPConfigurationTarget(target mcpconfig.Target) MCPRemoveStatus {
	status := a.mcpConfigurationStatus(target)
	empty := MCPRemoveStatus{Result: mcpconfig.RemoveResult{Snapshot: status.Snapshot}}
	if !status.ClientDetected {
		empty.Message = "尚未在本机确认该客户端。为避免修改无效设置，XIASS Tools 不会移除全局 MCP 条目。"
		return empty
	}
	if !status.OK || !status.Snapshot.Valid || status.Snapshot.HasSensitiveConfiguration {
		empty.Message = "现有全局 MCP 设置无法安全修改。XIASS Tools 不会读取、展示或改写其中的敏感内容。"
		return empty
	}
	// A direct native call can arrive after the renderer's last refresh. Keep
	// this no-op local rather than allocating a manager lock or a recovery
	// point when the exact managed ID is already absent.
	if !status.Snapshot.ManagedServerConfigured {
		empty.OK = true
		empty.Message = "未发现 XIASS Tools 的 MCP 远程连接；现有全局 MCP 设置未被修改。"
		return empty
	}
	manager, err := mcpconfig.NewDefaultManager(target)
	if err != nil {
		empty.Message = "无法安全定位该客户端的全局 MCP 设置。"
		return empty
	}
	result, err := manager.RemoveManagedRemote()
	if err != nil {
		empty.Message = mcpRemoveErrorMessage(err)
		return empty
	}
	if !result.Removed {
		return MCPRemoveStatus{
			OK:      true,
			Message: "未发现 XIASS Tools 的 MCP 远程连接；现有全局 MCP 设置未被修改。",
			Result:  result,
		}
	}
	return MCPRemoveStatus{
		OK:      true,
		Message: "已移除 XIASS Tools 的 MCP 远程连接，并创建了经过校验的恢复点。其他 MCP 条目保持不变。",
		Result:  result,
	}
}

func (a *App) listMCPBackupsTarget(target mcpconfig.Target) MCPBackupListStatus {
	manager, err := mcpconfig.NewDefaultManager(target)
	if err != nil {
		return MCPBackupListStatus{Message: "无法安全访问该客户端的全局 MCP 恢复点。", Backups: emptyMCPBackups()}
	}
	backups, err := manager.ListBackups()
	if err != nil {
		return MCPBackupListStatus{Message: "全局 MCP 恢复点未通过安全校验，当前设置未被修改。", Backups: emptyMCPBackups()}
	}
	return MCPBackupListStatus{
		OK:      true,
		Message: "已读取经过校验的全局 MCP 恢复点。",
		Backups: nonNilMCPBackups(backups),
	}
}

func (a *App) restoreMCPBackupTarget(target mcpconfig.Target, backupID string) MCPRestoreStatus {
	empty := MCPRestoreStatus{Result: mcpconfig.RestoreResult{Snapshot: mcpconfig.Snapshot{Target: target}}}
	if !a.mcpTargetDetected(target) {
		empty.Message = "尚未在本机确认该客户端。为避免修改无效设置，XIASS Tools 不会恢复全局 MCP 设置。"
		return empty
	}
	manager, err := mcpconfig.NewDefaultManager(target)
	if err != nil {
		empty.Message = "无法安全访问该客户端的全局 MCP 恢复点。"
		return empty
	}
	result, err := manager.Restore(backupID)
	backupID = ""
	if err != nil {
		empty.Message = mcpRestoreErrorMessage(err)
		return empty
	}
	return MCPRestoreStatus{
		OK:      true,
		Message: "已恢复选定的全局 MCP 恢复点，并创建新的安全恢复点。",
		Result:  result,
	}
}

func (a *App) deleteMCPBackupTarget(target mcpconfig.Target, backupID string) MCPBackupDeleteStatus {
	manager, err := mcpconfig.NewDefaultManager(target)
	if err != nil {
		return MCPBackupDeleteStatus{Message: "无法安全访问该客户端的全局 MCP 恢复点。"}
	}
	err = manager.DeleteBackup(backupID)
	backupID = ""
	if err != nil {
		return MCPBackupDeleteStatus{Message: mcpDeleteBackupErrorMessage(err)}
	}
	return MCPBackupDeleteStatus{OK: true, Message: "已删除选定的全局 MCP 恢复点。"}
}

func emptyMCPBackups() []mcpconfig.BackupInfo {
	return []mcpconfig.BackupInfo{}
}

func nonNilMCPBackups(backups []mcpconfig.BackupInfo) []mcpconfig.BackupInfo {
	if backups == nil {
		return emptyMCPBackups()
	}
	return backups
}

func (a *App) mcpConfigurationStatus(target mcpconfig.Target) MCPConfigurationStatus {
	clientDetected := a.mcpTargetDetected(target)
	status := MCPConfigurationStatus{ClientDetected: clientDetected, Snapshot: mcpconfig.Snapshot{Target: target}}
	manager, err := mcpconfig.NewDefaultManager(target)
	if err != nil {
		status.Message = "无法安全定位该客户端的全局 MCP 设置。"
		return status
	}
	snapshot, err := manager.Inspect()
	status.Snapshot = snapshot
	if err != nil {
		status.Message = "全局 MCP 设置无法通过安全校验。"
		return status
	}
	status.OK = true
	status.CanApply = clientDetected && snapshot.Valid && !snapshot.HasSensitiveConfiguration
	switch {
	case !clientDetected:
		status.Message = "尚未在本机确认该客户端；不会创建或修改其全局 MCP 设置。"
	case snapshot.HasSensitiveConfiguration:
		status.Message = "检测到敏感 MCP 设置。内容保持私密，XIASS Tools 将其作为只读保护处理，不会修改该文件。"
	case !snapshot.Valid:
		status.Message = "全局 MCP 设置格式无效，修复前不会进行写入。"
	case snapshot.ManagedServerConfigured:
		status.Message = "XIASS Tools MCP 远程配置已写入；未测试远端 MCP 服务。"
	default:
		status.Message = "全局 MCP 设置已验证，可以添加 XIASS Tools MCP 远程配置。"
	}
	return status
}

func (a *App) mcpTargetDetected(target mcpconfig.Target) bool {
	ctx, cancel := a.agentOperationContext()
	defer cancel()
	var adapter agent.Adapter
	switch target {
	case mcpconfig.TargetCursor:
		adapter = agentdiscovery.NewCursorAdapter()
	case mcpconfig.TargetWindsurf:
		adapter = agentdiscovery.NewWindsurfAdapter()
	default:
		return false
	}
	status, err := adapter.Detect(ctx)
	return err == nil && mcpClientDetected(status)
}

func parseMCPConfigurationTarget(target string) (mcpconfig.Target, bool) {
	switch strings.TrimSpace(strings.ToLower(target)) {
	case string(mcpconfig.TargetCursor):
		return mcpconfig.TargetCursor, true
	case string(mcpconfig.TargetWindsurf):
		return mcpconfig.TargetWindsurf, true
	default:
		return "", false
	}
}

func mcpConfigurationErrorMessage(err error) string {
	switch {
	case errors.Is(err, mcpconfig.ErrInvalidRemote):
		return "远程地址必须是 HTTPS，或无凭据的本机 localhost/回环 HTTP 地址。"
	case errors.Is(err, mcpconfig.ErrUnsafeConfiguration):
		return "现有全局 MCP 设置含有敏感或不安全内容，XIASS Tools 未做任何修改。"
	case errors.Is(err, mcpconfig.ErrInvalidConfiguration):
		return "现有全局 MCP 设置格式无效，XIASS Tools 未做任何修改。"
	case errors.Is(err, mcpconfig.ErrOperationBusy):
		return "另一项 MCP 设置操作正在进行，请完成后重试。"
	default:
		return "未能安全保存 MCP 远程连接；现有设置已保持不变。"
	}
}

func mcpRemoveErrorMessage(err error) string {
	switch {
	case errors.Is(err, mcpconfig.ErrUnsafeConfiguration):
		return "现有全局 MCP 设置含有敏感或不安全内容，XIASS Tools 未做任何修改。"
	case errors.Is(err, mcpconfig.ErrInvalidConfiguration):
		return "现有全局 MCP 设置格式无效，XIASS Tools 未做任何修改。"
	case errors.Is(err, mcpconfig.ErrOperationBusy):
		return "另一项 MCP 设置操作正在进行，请完成后重试。"
	default:
		return "未能安全移除 XIASS Tools MCP 远程连接；现有设置已保持不变。"
	}
}

func mcpRestoreErrorMessage(err error) string {
	switch {
	case errors.Is(err, mcpconfig.ErrUnsafeConfiguration), errors.Is(err, mcpconfig.ErrInvalidConfiguration):
		return "选定恢复点或当前全局 MCP 设置未通过安全校验，未执行恢复。"
	case errors.Is(err, mcpconfig.ErrOperationBusy):
		return "另一项 MCP 设置操作正在进行，请完成后重试。"
	default:
		return "未能恢复全局 MCP 设置；当前设置已保持不变。"
	}
}

func mcpDeleteBackupErrorMessage(err error) string {
	switch {
	case errors.Is(err, mcpconfig.ErrUnsafeConfiguration), errors.Is(err, mcpconfig.ErrInvalidConfiguration):
		return "选定恢复点未通过安全校验，未执行删除。"
	case errors.Is(err, mcpconfig.ErrOperationBusy):
		return "另一项 MCP 设置操作正在进行，请完成后重试。"
	default:
		return "未能删除全局 MCP 恢复点；现有设置未被修改。"
	}
}
