package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"antigravity-wf-assistant/internal/mcpconfig"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const cursorProjectMCPSelectionTTL = 30 * time.Minute

// cursorProjectMCPSelection is a short-lived native-only capability. The
// selected root stays inside the Go process and is never returned to the
// renderer, persisted, logged, or included in diagnostics.
type cursorProjectMCPSelection struct {
	projectRoot string
	projectName string
	expiresAt   time.Time
}

// CursorProjectMCPStatus is the path-redacted status returned for a selected
// Cursor project. SelectionID is an opaque, short-lived native handle; it is
// the only value the renderer may present for a project-level operation.
type CursorProjectMCPStatus struct {
	OK          bool               `json:"ok"`
	Message     string             `json:"message"`
	SelectionID string             `json:"selectionId,omitempty"`
	ProjectName string             `json:"projectName,omitempty"`
	ExpiresAt   string             `json:"expiresAt,omitempty"`
	CanApply    bool               `json:"canApply"`
	Snapshot    mcpconfig.Snapshot `json:"snapshot"`
}

// CursorProjectMCPRemoteInput intentionally excludes a project path, headers,
// command, arguments, environment values, and credentials. RemoteURL is used
// for one native transaction only and is never echoed back to the renderer.
type CursorProjectMCPRemoteInput struct {
	SelectionID string `json:"selectionId"`
	RemoteURL   string `json:"remoteUrl"`
}

// CursorProjectMCPSelectionInput keeps non-write project actions bound to an
// opaque selection capability rather than a renderer-controlled filesystem
// path.
type CursorProjectMCPSelectionInput struct {
	SelectionID string `json:"selectionId"`
}

// CursorProjectMCPBackupInput can address a recovery point only within the
// selected project's native session. A recovery-point ID from another project
// or from global Cursor configuration is rejected by the manager.
type CursorProjectMCPBackupInput struct {
	SelectionID string `json:"selectionId"`
	BackupID    string `json:"backupId"`
}

type CursorProjectMCPRemoveStatus struct {
	OK          bool                   `json:"ok"`
	Message     string                 `json:"message"`
	SelectionID string                 `json:"selectionId,omitempty"`
	ProjectName string                 `json:"projectName,omitempty"`
	ExpiresAt   string                 `json:"expiresAt,omitempty"`
	Result      mcpconfig.RemoveResult `json:"result"`
}

type CursorProjectMCPBackupListStatus struct {
	OK          bool                   `json:"ok"`
	Message     string                 `json:"message"`
	SelectionID string                 `json:"selectionId,omitempty"`
	ProjectName string                 `json:"projectName,omitempty"`
	ExpiresAt   string                 `json:"expiresAt,omitempty"`
	Backups     []mcpconfig.BackupInfo `json:"backups"`
}

type CursorProjectMCPRestoreStatus struct {
	OK          bool                    `json:"ok"`
	Message     string                  `json:"message"`
	SelectionID string                  `json:"selectionId,omitempty"`
	ProjectName string                  `json:"projectName,omitempty"`
	ExpiresAt   string                  `json:"expiresAt,omitempty"`
	Result      mcpconfig.RestoreResult `json:"result"`
}

type CursorProjectMCPBackupDeleteStatus struct {
	OK          bool   `json:"ok"`
	Message     string `json:"message"`
	SelectionID string `json:"selectionId,omitempty"`
	ProjectName string `json:"projectName,omitempty"`
	ExpiresAt   string `json:"expiresAt,omitempty"`
}

// ChooseCursorProjectMCPConfiguration opens the native directory picker and
// creates an in-memory project selection capability. It never accepts a path
// from the renderer.
func (a *App) ChooseCursorProjectMCPConfiguration() CursorProjectMCPStatus {
	if a == nil || a.ctx == nil {
		return CursorProjectMCPStatus{Message: "助手尚未完成启动，请稍后再选择 Cursor 项目目录。", Snapshot: cursorProjectMCPSnapshot()}
	}
	projectRoot, err := a.openDirectoryDialog(runtime.OpenDialogOptions{
		Title: "选择 Cursor 项目目录",
	})
	if err != nil {
		return CursorProjectMCPStatus{Message: "无法打开项目目录选择窗口。", Snapshot: cursorProjectMCPSnapshot()}
	}
	if strings.TrimSpace(projectRoot) == "" {
		return CursorProjectMCPStatus{OK: true, Message: "已取消选择 Cursor 项目目录。", Snapshot: cursorProjectMCPSnapshot()}
	}
	return a.selectCursorProjectMCPConfiguration(projectRoot)
}

// GetCursorProjectMCPConfiguration reads only the selected project's
// documented .cursor/mcp.json. It cannot inspect global MCP state or a path
// supplied by the renderer.
func (a *App) GetCursorProjectMCPConfiguration(selectionID string) CursorProjectMCPStatus {
	selection, ok := a.cursorProjectMCPSelectionForID(selectionID)
	if !ok {
		return cursorProjectMCPExpiredStatus()
	}
	manager, err := mcpconfig.NewCursorProjectManager(selection.projectRoot)
	if err != nil {
		a.forgetCursorProjectMCPSelection(selectionID)
		return cursorProjectMCPUnavailableStatus(selectionID, selection)
	}
	snapshot, err := manager.Inspect()
	if err != nil {
		status := cursorProjectMCPStatus(selectionID, selection, snapshot, false, "选定项目的 Cursor MCP 设置无法通过安全校验；当前内容未被修改。")
		status.OK = false
		return status
	}
	return cursorProjectMCPInspectedStatus(selectionID, selection, snapshot)
}

// ApplyCursorProjectMCPConfiguration writes only the reserved xiass-tools
// remote entry inside the selected project's .cursor/mcp.json. It performs no
// global MCP write and never accepts a project path from the renderer.
func (a *App) ApplyCursorProjectMCPConfiguration(input CursorProjectMCPRemoteInput) CursorProjectMCPStatus {
	selectionID := strings.TrimSpace(input.SelectionID)
	remoteURL := input.RemoteURL
	defer func() {
		input.SelectionID = ""
		input.RemoteURL = ""
		selectionID = ""
		remoteURL = ""
	}()
	selection, ok := a.cursorProjectMCPSelectionForID(selectionID)
	if !ok {
		return cursorProjectMCPExpiredStatus()
	}
	manager, err := mcpconfig.NewCursorProjectManager(selection.projectRoot)
	if err != nil {
		a.forgetCursorProjectMCPSelection(selectionID)
		return cursorProjectMCPUnavailableStatus(selectionID, selection)
	}
	before, err := manager.Inspect()
	if err != nil || !before.Valid || before.HasSensitiveConfiguration {
		return cursorProjectMCPReadOnlyStatus(selectionID, selection, before)
	}
	result, err := manager.ApplyRemote(mcpconfig.ApplyInput{RemoteURL: remoteURL})
	if err != nil {
		status := cursorProjectMCPStatus(selectionID, selection, before, false, cursorProjectMCPApplyErrorMessage(err))
		status.OK = false
		return status
	}
	return cursorProjectMCPStatus(selectionID, selection, result.Snapshot, result.Snapshot.Valid && !result.Snapshot.HasSensitiveConfiguration, "已安全写入所选项目的 XIASS Tools MCP 远程配置，并完成写入校验与本地恢复点备份；未测试远端 MCP 服务。")
}

// RemoveCursorProjectMCPConfiguration removes only the xiass-tools entry from
// the selected project. It never touches Cursor's global configuration.
func (a *App) RemoveCursorProjectMCPConfiguration(input CursorProjectMCPSelectionInput) CursorProjectMCPRemoveStatus {
	selectionID := strings.TrimSpace(input.SelectionID)
	defer func() {
		input.SelectionID = ""
		selectionID = ""
	}()
	selection, ok := a.cursorProjectMCPSelectionForID(selectionID)
	if !ok {
		return CursorProjectMCPRemoveStatus{Message: "项目 MCP 选择已过期或无效，请重新选择项目目录。", Result: mcpconfig.RemoveResult{Snapshot: cursorProjectMCPSnapshot()}}
	}
	status := cursorProjectMCPRemoveStatus(selectionID, selection, mcpconfig.RemoveResult{Snapshot: cursorProjectMCPSnapshot()})
	manager, err := mcpconfig.NewCursorProjectManager(selection.projectRoot)
	if err != nil {
		a.forgetCursorProjectMCPSelection(selectionID)
		status.Message = "选定项目已不可用，未修改任何 MCP 设置。"
		return status
	}
	before, err := manager.Inspect()
	status.Result.Snapshot = before
	if err != nil || !before.Valid || before.HasSensitiveConfiguration {
		status.Message = "选定项目的 Cursor MCP 设置无法安全修改；当前内容未被修改。"
		return status
	}
	if !before.ManagedServerConfigured {
		status.OK = true
		status.Message = "未发现所选项目中的 XIASS Tools MCP 远程连接；项目设置未被修改。"
		return status
	}
	result, err := manager.RemoveManagedRemote()
	if err != nil {
		status.Message = cursorProjectMCPRemoveErrorMessage(err)
		return status
	}
	status.OK = true
	status.Result = result
	if result.Removed {
		status.Message = "已移除所选项目中的 XIASS Tools MCP 远程连接，并创建了经过校验的恢复点。其他项目 MCP 条目保持不变。"
		return status
	}
	status.Message = "未发现所选项目中的 XIASS Tools MCP 远程连接；项目设置未被修改。"
	return status
}

// ListCursorProjectMCPBackups lists only the current selected project's
// checksum-verified recovery points. It never lists global or other-project
// recovery points.
func (a *App) ListCursorProjectMCPBackups(input CursorProjectMCPSelectionInput) CursorProjectMCPBackupListStatus {
	selectionID := strings.TrimSpace(input.SelectionID)
	defer func() {
		input.SelectionID = ""
		selectionID = ""
	}()
	selection, ok := a.cursorProjectMCPSelectionForID(selectionID)
	if !ok {
		return CursorProjectMCPBackupListStatus{Message: "项目 MCP 选择已过期或无效，请重新选择项目目录。", Backups: emptyMCPBackups()}
	}
	status := cursorProjectMCPBackupListStatus(selectionID, selection)
	manager, err := mcpconfig.NewCursorProjectManager(selection.projectRoot)
	if err != nil {
		a.forgetCursorProjectMCPSelection(selectionID)
		status.Message = "选定项目已不可用，无法读取项目 MCP 恢复点。"
		return status
	}
	backups, err := manager.ListBackups()
	if err != nil {
		status.Message = "项目 MCP 恢复点未通过安全校验，当前设置未被修改。"
		return status
	}
	status.OK = true
	status.Message = "已读取所选项目经过校验的 MCP 恢复点。"
	status.Backups = nonNilMCPBackups(backups)
	return status
}

// RestoreCursorProjectMCPBackup restores a verified recovery point only for
// the selected project. The mcpconfig manager rejects copied global/other
// project backups before any write occurs.
func (a *App) RestoreCursorProjectMCPBackup(input CursorProjectMCPBackupInput) CursorProjectMCPRestoreStatus {
	selectionID := strings.TrimSpace(input.SelectionID)
	backupID := strings.TrimSpace(input.BackupID)
	defer func() {
		input.SelectionID = ""
		input.BackupID = ""
		selectionID = ""
		backupID = ""
	}()
	selection, ok := a.cursorProjectMCPSelectionForID(selectionID)
	if !ok {
		return CursorProjectMCPRestoreStatus{Message: "项目 MCP 选择已过期或无效，请重新选择项目目录。", Result: mcpconfig.RestoreResult{Snapshot: cursorProjectMCPSnapshot()}}
	}
	status := cursorProjectMCPRestoreStatus(selectionID, selection, mcpconfig.RestoreResult{Snapshot: cursorProjectMCPSnapshot()})
	manager, err := mcpconfig.NewCursorProjectManager(selection.projectRoot)
	if err != nil {
		a.forgetCursorProjectMCPSelection(selectionID)
		status.Message = "选定项目已不可用，未恢复任何 MCP 设置。"
		return status
	}
	result, err := manager.Restore(backupID)
	if err != nil {
		status.Message = cursorProjectMCPRestoreErrorMessage(err)
		return status
	}
	status.OK = true
	status.Result = result
	status.Message = "已恢复所选项目的 MCP 恢复点，并创建新的安全恢复点。"
	return status
}

// DeleteCursorProjectMCPBackup deletes only a verified recovery point that is
// bound to the selected project. It never accepts a path or raw JSON.
func (a *App) DeleteCursorProjectMCPBackup(input CursorProjectMCPBackupInput) CursorProjectMCPBackupDeleteStatus {
	selectionID := strings.TrimSpace(input.SelectionID)
	backupID := strings.TrimSpace(input.BackupID)
	defer func() {
		input.SelectionID = ""
		input.BackupID = ""
		selectionID = ""
		backupID = ""
	}()
	selection, ok := a.cursorProjectMCPSelectionForID(selectionID)
	if !ok {
		return CursorProjectMCPBackupDeleteStatus{Message: "项目 MCP 选择已过期或无效，请重新选择项目目录。"}
	}
	status := cursorProjectMCPBackupDeleteStatus(selectionID, selection)
	manager, err := mcpconfig.NewCursorProjectManager(selection.projectRoot)
	if err != nil {
		a.forgetCursorProjectMCPSelection(selectionID)
		status.Message = "选定项目已不可用，未删除任何 MCP 恢复点。"
		return status
	}
	if err := manager.DeleteBackup(backupID); err != nil {
		status.Message = cursorProjectMCPDeleteErrorMessage(err)
		return status
	}
	status.OK = true
	status.Message = "已删除所选项目的 MCP 恢复点。"
	return status
}

func (a *App) selectCursorProjectMCPConfiguration(projectRoot string) CursorProjectMCPStatus {
	manager, err := mcpconfig.NewCursorProjectManager(projectRoot)
	if err != nil {
		return CursorProjectMCPStatus{Message: "所选目录不是可安全管理的 Cursor 项目目录。请选择一个真实存在的项目文件夹。", Snapshot: cursorProjectMCPSnapshot()}
	}
	snapshot, err := manager.Inspect()
	if err != nil {
		return CursorProjectMCPStatus{Message: "所选项目的 Cursor MCP 设置无法通过安全校验；当前内容未被修改。", Snapshot: snapshot}
	}
	selectionID, err := newCursorProjectMCPSelectionID()
	if err != nil {
		return CursorProjectMCPStatus{Message: "无法创建项目 MCP 选择会话，请重新选择项目目录。", Snapshot: snapshot}
	}
	selection := cursorProjectMCPSelection{
		projectRoot: projectRoot,
		projectName: cursorProjectMCPProjectName(projectRoot),
		expiresAt:   time.Now().Add(cursorProjectMCPSelectionTTL),
	}
	a.storeCursorProjectMCPSelection(selectionID, selection)
	return cursorProjectMCPInspectedStatus(selectionID, selection, snapshot)
}

func (a *App) storeCursorProjectMCPSelection(selectionID string, selection cursorProjectMCPSelection) {
	if a == nil || !validCursorProjectMCPSelectionID(selectionID) || selection.projectRoot == "" || selection.expiresAt.IsZero() {
		return
	}
	a.cursorProjectMCPMu.Lock()
	defer a.cursorProjectMCPMu.Unlock()
	a.discardExpiredCursorProjectMCPSelectionsLocked(time.Now())
	if a.cursorProjectMCP == nil {
		a.cursorProjectMCP = make(map[string]cursorProjectMCPSelection)
	}
	a.cursorProjectMCP[selectionID] = selection
}

func (a *App) cursorProjectMCPSelectionForID(selectionID string) (cursorProjectMCPSelection, bool) {
	selectionID = strings.TrimSpace(selectionID)
	if a == nil || !validCursorProjectMCPSelectionID(selectionID) {
		return cursorProjectMCPSelection{}, false
	}
	a.cursorProjectMCPMu.Lock()
	defer a.cursorProjectMCPMu.Unlock()
	a.discardExpiredCursorProjectMCPSelectionsLocked(time.Now())
	selection, found := a.cursorProjectMCP[selectionID]
	return selection, found
}

func (a *App) forgetCursorProjectMCPSelection(selectionID string) {
	selectionID = strings.TrimSpace(selectionID)
	if a == nil || !validCursorProjectMCPSelectionID(selectionID) {
		return
	}
	a.cursorProjectMCPMu.Lock()
	defer a.cursorProjectMCPMu.Unlock()
	delete(a.cursorProjectMCP, selectionID)
}

func (a *App) clearCursorProjectMCPSelections() {
	if a == nil {
		return
	}
	a.cursorProjectMCPMu.Lock()
	defer a.cursorProjectMCPMu.Unlock()
	for key := range a.cursorProjectMCP {
		delete(a.cursorProjectMCP, key)
	}
}

func (a *App) discardExpiredCursorProjectMCPSelectionsLocked(now time.Time) {
	for selectionID, selection := range a.cursorProjectMCP {
		if selection.expiresAt.IsZero() || !selection.expiresAt.After(now) {
			delete(a.cursorProjectMCP, selectionID)
		}
	}
}

func newCursorProjectMCPSelectionID() (string, error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}

func validCursorProjectMCPSelectionID(selectionID string) bool {
	if len(selectionID) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(selectionID)
	return err == nil && len(decoded) == 32 && strings.ToLower(selectionID) == selectionID
}

func cursorProjectMCPProjectName(projectRoot string) string {
	name := strings.TrimSpace(filepath.Base(filepath.Clean(projectRoot)))
	if name == "" || name == "." || name == string(filepath.Separator) {
		return "所选项目"
	}
	return name
}

func cursorProjectMCPSnapshot() mcpconfig.Snapshot {
	return mcpconfig.Snapshot{Target: mcpconfig.TargetCursor}
}

func cursorProjectMCPStatus(selectionID string, selection cursorProjectMCPSelection, snapshot mcpconfig.Snapshot, canApply bool, message string) CursorProjectMCPStatus {
	return CursorProjectMCPStatus{
		OK:          message != "" && snapshot.Valid,
		Message:     message,
		SelectionID: selectionID,
		ProjectName: selection.projectName,
		ExpiresAt:   selection.expiresAt.UTC().Format(time.RFC3339Nano),
		CanApply:    canApply,
		Snapshot:    snapshot,
	}
}

func cursorProjectMCPInspectedStatus(selectionID string, selection cursorProjectMCPSelection, snapshot mcpconfig.Snapshot) CursorProjectMCPStatus {
	canApply := snapshot.Valid && !snapshot.HasSensitiveConfiguration
	message := "所选项目的 Cursor MCP 设置已验证，可以添加 XIASS Tools MCP 远程配置。"
	switch {
	case snapshot.HasSensitiveConfiguration:
		message = "所选项目的 Cursor MCP 设置含有敏感内容。XIASS Tools 不会读取、展示或改写该文件。"
	case !snapshot.Valid:
		message = "所选项目的 Cursor MCP 设置格式无效，修复前不会写入。"
	case snapshot.ManagedServerConfigured:
		message = "所选项目已配置 XIASS Tools MCP 远程连接；未测试远端 MCP 服务。"
	}
	status := cursorProjectMCPStatus(selectionID, selection, snapshot, canApply, message)
	status.OK = true
	return status
}

func cursorProjectMCPReadOnlyStatus(selectionID string, selection cursorProjectMCPSelection, snapshot mcpconfig.Snapshot) CursorProjectMCPStatus {
	message := "所选项目的 Cursor MCP 设置无法安全修改；当前内容未被修改。"
	if snapshot.HasSensitiveConfiguration {
		message = "所选项目的 Cursor MCP 设置含有敏感内容。XIASS Tools 不会读取、展示或改写该文件。"
	}
	status := cursorProjectMCPStatus(selectionID, selection, snapshot, false, message)
	status.OK = false
	return status
}

func cursorProjectMCPExpiredStatus() CursorProjectMCPStatus {
	return CursorProjectMCPStatus{Message: "项目 MCP 选择已过期或无效，请重新选择项目目录。", Snapshot: cursorProjectMCPSnapshot()}
}

func cursorProjectMCPUnavailableStatus(selectionID string, selection cursorProjectMCPSelection) CursorProjectMCPStatus {
	return CursorProjectMCPStatus{
		Message:     "选定项目已不可用，未修改任何 MCP 设置。",
		SelectionID: selectionID,
		ProjectName: selection.projectName,
		ExpiresAt:   selection.expiresAt.UTC().Format(time.RFC3339Nano),
		Snapshot:    cursorProjectMCPSnapshot(),
	}
}

func cursorProjectMCPRemoveStatus(selectionID string, selection cursorProjectMCPSelection, result mcpconfig.RemoveResult) CursorProjectMCPRemoveStatus {
	return CursorProjectMCPRemoveStatus{
		SelectionID: selectionID,
		ProjectName: selection.projectName,
		ExpiresAt:   selection.expiresAt.UTC().Format(time.RFC3339Nano),
		Result:      result,
	}
}

func cursorProjectMCPBackupListStatus(selectionID string, selection cursorProjectMCPSelection) CursorProjectMCPBackupListStatus {
	return CursorProjectMCPBackupListStatus{
		SelectionID: selectionID,
		ProjectName: selection.projectName,
		ExpiresAt:   selection.expiresAt.UTC().Format(time.RFC3339Nano),
		Backups:     emptyMCPBackups(),
	}
}

func cursorProjectMCPRestoreStatus(selectionID string, selection cursorProjectMCPSelection, result mcpconfig.RestoreResult) CursorProjectMCPRestoreStatus {
	return CursorProjectMCPRestoreStatus{
		SelectionID: selectionID,
		ProjectName: selection.projectName,
		ExpiresAt:   selection.expiresAt.UTC().Format(time.RFC3339Nano),
		Result:      result,
	}
}

func cursorProjectMCPBackupDeleteStatus(selectionID string, selection cursorProjectMCPSelection) CursorProjectMCPBackupDeleteStatus {
	return CursorProjectMCPBackupDeleteStatus{
		SelectionID: selectionID,
		ProjectName: selection.projectName,
		ExpiresAt:   selection.expiresAt.UTC().Format(time.RFC3339Nano),
	}
}

func cursorProjectMCPApplyErrorMessage(err error) string {
	switch {
	case errors.Is(err, mcpconfig.ErrInvalidRemote):
		return "远程地址必须是 HTTPS，或无凭据的本机 localhost/回环 HTTP 地址。"
	case errors.Is(err, mcpconfig.ErrUnsafeConfiguration), errors.Is(err, mcpconfig.ErrInvalidConfiguration):
		return "所选项目的 Cursor MCP 设置无法安全修改；当前内容未被修改。"
	case errors.Is(err, mcpconfig.ErrOperationBusy):
		return "另一项项目 MCP 设置操作正在进行，请完成后重试。"
	default:
		return "未能安全保存所选项目的 MCP 远程连接；现有设置已保持不变。"
	}
}

func cursorProjectMCPRemoveErrorMessage(err error) string {
	switch {
	case errors.Is(err, mcpconfig.ErrUnsafeConfiguration), errors.Is(err, mcpconfig.ErrInvalidConfiguration):
		return "所选项目的 MCP 设置无法安全修改；当前内容未被修改。"
	case errors.Is(err, mcpconfig.ErrOperationBusy):
		return "另一项项目 MCP 设置操作正在进行，请完成后重试。"
	default:
		return "未能安全移除所选项目的 XIASS Tools MCP 远程连接；现有设置已保持不变。"
	}
}

func cursorProjectMCPRestoreErrorMessage(err error) string {
	switch {
	case errors.Is(err, mcpconfig.ErrUnsafeConfiguration), errors.Is(err, mcpconfig.ErrInvalidConfiguration):
		return "选定项目的 MCP 恢复点或当前设置未通过安全校验，未执行恢复。"
	case errors.Is(err, mcpconfig.ErrOperationBusy):
		return "另一项项目 MCP 设置操作正在进行，请完成后重试。"
	default:
		return "未能恢复所选项目的 MCP 设置；当前设置已保持不变。"
	}
}

func cursorProjectMCPDeleteErrorMessage(err error) string {
	switch {
	case errors.Is(err, mcpconfig.ErrUnsafeConfiguration), errors.Is(err, mcpconfig.ErrInvalidConfiguration):
		return "选定项目的 MCP 恢复点未通过安全校验，未执行删除。"
	case errors.Is(err, mcpconfig.ErrOperationBusy):
		return "另一项项目 MCP 设置操作正在进行，请完成后重试。"
	default:
		return "未能删除所选项目的 MCP 恢复点；现有设置未被修改。"
	}
}
