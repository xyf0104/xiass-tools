package main

import (
	"context"
	"errors"
	"strings"

	"antigravity-wf-assistant/internal/codexdesktop"
)

// CodexDesktopControlStatus is the Wails-safe projection of desktop lifecycle
// state. It intentionally contains no filesystem path, executable, command
// line, PID, account data, credential, or operating-system error.
type CodexDesktopControlStatus struct {
	OK           bool                      `json:"ok"`
	Message      string                    `json:"message"`
	Installation codexdesktop.Installation `json:"installation"`
	Discovered   bool                      `json:"discovered"`
	Selected     bool                      `json:"selected"`
	Running      bool                      `json:"running"`
	CanSelect    bool                      `json:"canSelect"`
	CanLaunch    bool                      `json:"canLaunch"`
	CanStop      bool                      `json:"canStop"`
	CanRestart   bool                      `json:"canRestart"`
	Warnings     []codexdesktop.Warning    `json:"warnings,omitempty"`
}

// codexDesktopControlService is intentionally narrow so lifecycle
// transactions can be tested without ever using a real user process. Its
// methods are all backed by the structure-validating Controller in production.
type codexDesktopControlService interface {
	Status(context.Context) codexdesktop.ControlStatus
	SelectPath(context.Context, string) (codexdesktop.ControlStatus, error)
	Launch(context.Context) (codexdesktop.ControlStatus, error)
	Stop(context.Context, string) (codexdesktop.ControlStatus, error)
	Restart(context.Context, string) (codexdesktop.ControlStatus, error)
}

func (a *App) codexDesktopController() codexDesktopControlService {
	if a == nil {
		return nil
	}
	a.codexDesktopMu.Lock()
	defer a.codexDesktopMu.Unlock()
	if a.codexDesktopControl == nil {
		a.codexDesktopControl = codexdesktop.NewController()
	}
	return a.codexDesktopControl
}

// GetCodexDesktopControlStatus is read-only. It never opens a picker,
// launches Codex, closes Codex, or reads Codex configuration/account data.
func (a *App) GetCodexDesktopControlStatus() CodexDesktopControlStatus {
	controller := a.codexDesktopController()
	if controller == nil {
		return CodexDesktopControlStatus{OK: false, Message: "Codex Desktop 控制器尚不可用。"}
	}
	status := controller.Status(a.codexDesktopContext())
	message := "已检查 Codex Desktop 状态。"
	if !status.Discovered {
		message = "尚未发现可验证的 Codex 或 ChatGPT Desktop 应用。"
	}
	return codexDesktopStatusForRenderer(status, true, message)
}

// SelectCodexDesktopInstallation opens a native picker. The selected local
// path is passed directly to the Go-side validator and is never returned to
// the renderer or written to logs/diagnostics.
func (a *App) SelectCodexDesktopInstallation() CodexDesktopControlStatus {
	if a == nil || a.ctx == nil {
		return CodexDesktopControlStatus{OK: false, Message: "助手尚未完成启动，暂时无法打开 Codex 应用选择窗口。"}
	}
	a.codexDesktopOperation.Lock()
	defer a.codexDesktopOperation.Unlock()
	selectedPath, cancelled, err := selectCodexDesktopNativeTarget(a, a.ctx)
	if err != nil {
		return a.codexDesktopFailureStatus("无法打开 Codex 应用选择窗口。")
	}
	if cancelled {
		return a.codexDesktopSuccessStatus("已取消选择 Codex Desktop 应用。")
	}
	status, err := a.codexDesktopController().SelectPath(a.codexDesktopContext(), selectedPath)
	if err != nil {
		return codexDesktopStatusForRenderer(status, false, codexDesktopMessageForError(err, "select"))
	}
	return codexDesktopStatusForRenderer(status, true, "已选择并验证 Codex Desktop 应用；路径仅保留在当前助手进程内。")
}

// SelectCodexDesktopInstallationPath validates one user-pasted local app path
// through the same native Controller.SelectPath path as the file picker. The
// path is deliberately request-local: it is not retained by App, returned to
// the renderer, logged, added to diagnostics, or persisted anywhere.
func (a *App) SelectCodexDesktopInstallationPath(value string) CodexDesktopControlStatus {
	if a == nil || a.ctx == nil {
		return CodexDesktopControlStatus{OK: false, Message: "助手尚未完成启动，暂时无法验证 Codex 应用路径。"}
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return a.codexDesktopFailureStatus("请粘贴 Codex 或 ChatGPT Desktop 的本机应用路径。")
	}

	a.codexDesktopOperation.Lock()
	defer a.codexDesktopOperation.Unlock()
	status, err := a.codexDesktopController().SelectPath(a.codexDesktopContext(), value)
	if err != nil {
		return codexDesktopStatusForRenderer(status, false, codexDesktopMessageForError(err, "select"))
	}
	return codexDesktopStatusForRenderer(status, true, "已验证手动粘贴的 Codex Desktop 应用路径；路径不会保存或显示。")
}

// LaunchCodexDesktop starts only a currently structure-validated Codex.app /
// ChatGPT.app target. It is a direct user action and never runs as a side
// effect of reading or saving a configuration.
func (a *App) LaunchCodexDesktop() CodexDesktopControlStatus {
	if a == nil || a.ctx == nil {
		return CodexDesktopControlStatus{OK: false, Message: "助手尚未完成启动，暂时无法启动 Codex Desktop。"}
	}
	a.codexDesktopOperation.Lock()
	defer a.codexDesktopOperation.Unlock()
	status, err := a.codexDesktopController().Launch(a.codexDesktopContext())
	if err != nil {
		return codexDesktopStatusForRenderer(status, false, codexDesktopMessageForError(err, "launch"))
	}
	return codexDesktopStatusForRenderer(status, true, "Codex Desktop 已启动并完成状态确认。")
}

// StopCodexDesktop asks Codex Desktop to exit gracefully. confirmation must
// equal codexdesktop.LifecycleConfirmation. No process is force-killed if the
// app does not exit, and no configuration/history operation is triggered.
func (a *App) StopCodexDesktop(confirmation string) CodexDesktopControlStatus {
	if a == nil || a.ctx == nil {
		return CodexDesktopControlStatus{OK: false, Message: "助手尚未完成启动，暂时无法停止 Codex Desktop。"}
	}
	a.codexDesktopOperation.Lock()
	defer a.codexDesktopOperation.Unlock()
	status, err := a.codexDesktopController().Stop(a.codexDesktopContext(), confirmation)
	if err != nil {
		return codexDesktopStatusForRenderer(status, false, codexDesktopMessageForError(err, "stop"))
	}
	return codexDesktopStatusForRenderer(status, true, "Codex Desktop 已正常退出。")
}

// RestartCodexDesktop uses the same explicit confirmation as Stop. It only
// launches after a verified graceful exit; a timeout never falls back to a
// force kill or an automatic relaunch.
func (a *App) RestartCodexDesktop(confirmation string) CodexDesktopControlStatus {
	if a == nil || a.ctx == nil {
		return CodexDesktopControlStatus{OK: false, Message: "助手尚未完成启动，暂时无法重启 Codex Desktop。"}
	}
	a.codexDesktopOperation.Lock()
	defer a.codexDesktopOperation.Unlock()
	status, err := a.codexDesktopController().Restart(a.codexDesktopContext(), confirmation)
	if err != nil {
		return codexDesktopStatusForRenderer(status, false, codexDesktopMessageForError(err, "restart"))
	}
	return codexDesktopStatusForRenderer(status, true, "Codex Desktop 已重启并完成状态确认。")
}

func (a *App) codexDesktopContext() context.Context {
	if a != nil && a.ctx != nil {
		return a.ctx
	}
	return context.Background()
}

func (a *App) codexDesktopSuccessStatus(message string) CodexDesktopControlStatus {
	controller := a.codexDesktopController()
	if controller == nil {
		return CodexDesktopControlStatus{OK: false, Message: "Codex Desktop 控制器尚不可用。"}
	}
	return codexDesktopStatusForRenderer(controller.Status(a.codexDesktopContext()), true, message)
}

func (a *App) codexDesktopFailureStatus(message string) CodexDesktopControlStatus {
	controller := a.codexDesktopController()
	if controller == nil {
		return CodexDesktopControlStatus{OK: false, Message: message}
	}
	return codexDesktopStatusForRenderer(controller.Status(a.codexDesktopContext()), false, message)
}

func codexDesktopStatusForRenderer(status codexdesktop.ControlStatus, ok bool, message string) CodexDesktopControlStatus {
	return CodexDesktopControlStatus{
		OK:           ok,
		Message:      message,
		Installation: status.Installation,
		Discovered:   status.Discovered,
		Selected:     status.Selected,
		Running:      status.Running,
		CanSelect:    status.CanSelect,
		CanLaunch:    status.CanLaunch,
		CanStop:      status.CanStop,
		CanRestart:   status.CanRestart,
		Warnings:     append([]codexdesktop.Warning(nil), status.Warnings...),
	}
}

func codexDesktopMessageForError(err error, operation string) string {
	switch {
	case errors.Is(err, codexdesktop.ErrConfirmationRequired):
		return "请在确认操作后再停止或重启 Codex Desktop。"
	case errors.Is(err, codexdesktop.ErrSelectionRejected):
		return "所选应用未通过 Codex Desktop 结构验证；未保存该路径。"
	case errors.Is(err, codexdesktop.ErrNoVerifiedInstallation):
		return "未找到可验证的 Codex 或 ChatGPT Desktop 应用。"
	case errors.Is(err, codexdesktop.ErrDesktopAlreadyRunning):
		return "Codex Desktop 已在运行。"
	case errors.Is(err, codexdesktop.ErrDesktopNotRunning):
		return "Codex Desktop 当前未运行；可直接使用启动操作。"
	case errors.Is(err, codexdesktop.ErrLifecycleTimeout):
		return "未能在安全等待时间内确认 Codex Desktop 状态；未执行强制结束。"
	default:
		switch operation {
		case "select":
			return "未能验证所选 Codex Desktop 应用。"
		case "launch":
			return "未能安全启动 Codex Desktop。"
		case "stop":
			return "未能确认 Codex Desktop 已退出；未执行强制结束。"
		case "restart":
			return "未能安全重启 Codex Desktop。"
		default:
			return "Codex Desktop 操作未完成。"
		}
	}
}
