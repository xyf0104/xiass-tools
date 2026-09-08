package main

import (
	"context"
	"strings"

	"antigravity-wf-assistant/internal/agent"
	"antigravity-wf-assistant/internal/agentdiscovery"
	"antigravity-wf-assistant/internal/launcher"
)

// AgentDesktopSelectionStatus is the redacted, renderer-safe result of an
// explicit Cursor/Windsurf application chooser. It intentionally excludes the
// selected path, executable, process ID, arguments, and any client data.
type AgentDesktopSelectionStatus struct {
	OK        bool     `json:"ok"`
	AgentID   agent.ID `json:"agentId,omitempty"`
	Message   string   `json:"message"`
	Selected  bool     `json:"selected"`
	CanLaunch bool     `json:"canLaunch"`
	Version   string   `json:"version,omitempty"`
}

type agentDesktopNativePicker func(context.Context, agent.ID) (string, bool, error)

// SelectAgentDesktopInstallation opens a native file chooser for Cursor or
// Windsurf. The Vue renderer can provide only a fixed agent ID; it cannot
// submit a filesystem path. A successful choice remains in memory only and is
// revalidated immediately before launch.
func (a *App) SelectAgentDesktopInstallation(agentID string) AgentDesktopSelectionStatus {
	identifier, ok := selectableAgentDesktopID(agentID)
	if !ok {
		return AgentDesktopSelectionStatus{Message: "该工具不支持从 XIASS Tools 选择桌面应用。"}
	}
	return a.selectAgentDesktopInstallationWithPicker(identifier, func(ctx context.Context, id agent.ID) (string, bool, error) {
		return selectAgentDesktopNativeTarget(a, ctx, id)
	})
}

// GetAgentDesktopSelectionStatus exposes only whether this running XIASS
// Tools process currently has a verified manual selection. It is intentionally
// non-persistent: an app path may change after an update or volume remount.
func (a *App) GetAgentDesktopSelectionStatus(agentID string) AgentDesktopSelectionStatus {
	identifier, ok := selectableAgentDesktopID(agentID)
	if !ok {
		return AgentDesktopSelectionStatus{Message: "该工具不支持从 XIASS Tools 选择桌面应用。"}
	}
	selection, selected := a.agentDesktopSelectionForID(identifier)
	if !selected {
		return agentDesktopSelectionStatus(identifier, selection, false, true, "尚未选择手动应用。")
	}
	// A selection is deliberately process-local because an app bundle can be
	// moved or replaced after it was chosen. Status must not advertise it as
	// launchable unless the current public bundle structure still verifies.
	verified, err := agentdiscovery.RevalidateDesktopSelection(selection)
	if err != nil {
		a.forgetAgentDesktopSelection(identifier)
		return agentDesktopSelectionStatus(identifier, agentdiscovery.DesktopSelection{}, false, false, "已选择的应用状态已变化；请重新选择并验证应用。")
	}
	a.storeAgentDesktopSelection(identifier, verified)
	return agentDesktopSelectionStatus(identifier, verified, true, true, "已检查手动选择的应用状态，可安全打开。")
}

func (a *App) selectAgentDesktopInstallationWithPicker(identifier agent.ID, picker agentDesktopNativePicker) AgentDesktopSelectionStatus {
	if a == nil || a.ctx == nil {
		return AgentDesktopSelectionStatus{AgentID: identifier, Message: "助手尚未完成启动，暂时无法打开应用选择窗口。"}
	}
	if picker == nil {
		return AgentDesktopSelectionStatus{AgentID: identifier, Message: "当前平台不支持应用选择窗口。"}
	}
	a.agentDesktopSelectionOperation.Lock()
	defer a.agentDesktopSelectionOperation.Unlock()
	selectedPath, cancelled, err := picker(a.ctx, identifier)
	if err != nil {
		return a.agentDesktopCurrentStatus(identifier, false, "无法打开应用选择窗口。")
	}
	if cancelled {
		return a.agentDesktopCurrentStatus(identifier, true, "已取消选择应用。")
	}
	selection, err := agentdiscovery.ValidateDesktopSelection(identifier, selectedPath)
	if err != nil {
		// Do not erase an earlier verified in-process selection because a later
		// cancelled or invalid picker choice should not retarget a launch.
		return a.agentDesktopCurrentStatus(identifier, false, "所选应用未通过结构验证；已有选择未被更改。")
	}
	a.storeAgentDesktopSelection(identifier, selection)
	return agentDesktopSelectionStatus(identifier, selection, true, true, "已选择并验证应用；路径仅保留在当前助手进程内。")
}

func (a *App) agentDesktopCurrentStatus(identifier agent.ID, ok bool, message string) AgentDesktopSelectionStatus {
	selection, selected := a.agentDesktopSelectionForID(identifier)
	return agentDesktopSelectionStatus(identifier, selection, selected, ok, message)
}

func agentDesktopSelectionStatus(identifier agent.ID, selection agentdiscovery.DesktopSelection, selected, ok bool, message string) AgentDesktopSelectionStatus {
	status := AgentDesktopSelectionStatus{
		OK:        ok,
		AgentID:   identifier,
		Message:   message,
		Selected:  selected,
		CanLaunch: selected,
	}
	if selected {
		status.Version = selection.Version()
	}
	return status
}

func selectableAgentDesktopID(value string) (agent.ID, bool) {
	identifier := agent.ID(strings.TrimSpace(value))
	switch identifier {
	case agent.CursorID, agent.WindsurfID:
		return identifier, true
	default:
		return "", false
	}
}

func (a *App) storeAgentDesktopSelection(identifier agent.ID, selection agentdiscovery.DesktopSelection) {
	if a == nil || selection.AgentID() != identifier {
		return
	}
	a.agentDesktopSelectionMu.Lock()
	defer a.agentDesktopSelectionMu.Unlock()
	if a.agentDesktopSelections == nil {
		a.agentDesktopSelections = make(map[agent.ID]agentdiscovery.DesktopSelection)
	}
	a.agentDesktopSelections[identifier] = selection
}

func (a *App) agentDesktopSelectionForID(identifier agent.ID) (agentdiscovery.DesktopSelection, bool) {
	if a == nil {
		return agentdiscovery.DesktopSelection{}, false
	}
	a.agentDesktopSelectionMu.Lock()
	defer a.agentDesktopSelectionMu.Unlock()
	selection, ok := a.agentDesktopSelections[identifier]
	return selection, ok
}

func (a *App) forgetAgentDesktopSelection(identifier agent.ID) {
	if a == nil {
		return
	}
	a.agentDesktopSelectionMu.Lock()
	defer a.agentDesktopSelectionMu.Unlock()
	delete(a.agentDesktopSelections, identifier)
}

// launchSelectedAgentDesktop is used by LaunchDetectedAgent after an explicit
// native selection. The previous selection is revalidated against the current
// public bundle/product metadata before the launcher receives a target.
func (a *App) launchSelectedAgentDesktop(identifier agent.ID) (AgentLaunchResult, bool) {
	selection, selected := a.agentDesktopSelectionForID(identifier)
	if !selected {
		return AgentLaunchResult{}, false
	}
	result := AgentLaunchResult{AgentID: identifier}
	verified, err := agentdiscovery.RevalidateDesktopSelection(selection)
	if err != nil {
		a.forgetAgentDesktopSelection(identifier)
		result.Message = "已选择的应用状态已变化，未启动；请重新选择并验证应用。"
		return result, true
	}
	a.storeAgentDesktopSelection(identifier, verified)
	if err := launcher.Launch(verified.LaunchTarget()); err != nil {
		result.Message = "无法启动已验证的应用。请从系统应用列表手动打开后重新检查。"
		return result, true
	}
	result.OK = true
	result.Message = "已请求启动应用。"
	return result, true
}
