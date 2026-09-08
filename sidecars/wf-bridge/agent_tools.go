package main

import (
	"context"
	"runtime"
	"strings"
	"time"

	"antigravity-wf-assistant/internal/agent"
	"antigravity-wf-assistant/internal/launcher"
)

// AgentDiagnosticsResult keeps Wails callers on a normal result path for
// expected local conditions such as an absent installation or an unbound
// adapter. The diagnostics themselves are structured and credential-free.
type AgentDiagnosticsResult struct {
	OK          bool               `json:"ok"`
	Message     string             `json:"message"`
	Diagnostics []agent.Diagnostic `json:"diagnostics,omitempty"`
}

// AgentLaunchResult is deliberately small. In particular, it never echoes an
// application path, command line, process ID, credential, or local profile
// path back into the Wails renderer.
type AgentLaunchResult struct {
	OK      bool     `json:"ok"`
	AgentID agent.ID `json:"agentId,omitempty"`
	Message string   `json:"message"`
}

type agentApplicationLauncher func(string) error

// GetAgentStatuses runs the normal bounded local discovery pass. It is safe
// to call at application startup because every adapter either reads public
// local metadata or uses a command with its own short timeout.
func (a *App) GetAgentStatuses() agent.AggregateStatus {
	return a.detectAgents(false)
}

// RefreshAgentStatuses explicitly asks Antigravity for a fresh structural
// scan, then re-runs all registered local discoverers. The mutex prevents two
// user clicks from starting overlapping deep scans of the same installations.
func (a *App) RefreshAgentStatuses() agent.AggregateStatus {
	return a.detectAgents(true)
}

func (a *App) detectAgents(refreshAntigravity bool) agent.AggregateStatus {
	if a.agentRegistry == nil {
		return unavailableAgentAggregate("The XIASS Tools agent registry is not available.")
	}
	if refreshAntigravity {
		a.agentRefreshMu.Lock()
		defer a.agentRefreshMu.Unlock()
	}
	ctx, cancel := a.agentOperationContext()
	defer cancel()
	if refreshAntigravity && a.antigravityAgent != nil {
		// Refresh returns a cached verified snapshot consumed immediately by
		// Registry.DetectAll below. Other discovery adapters still run in
		// parallel under the same bounded context.
		_, _ = a.antigravityAgent.Refresh(ctx)
	}
	return a.agentRegistry.DetectAll(ctx)
}

// DiagnoseAgent returns target-specific diagnostics only for a registered
// integration. It never performs a patch, starts an application, or exposes
// credentials in its response.
func (a *App) DiagnoseAgent(agentID string) AgentDiagnosticsResult {
	identifier := agent.ID(strings.TrimSpace(agentID))
	if err := identifier.Validate(); err != nil {
		return AgentDiagnosticsResult{OK: false, Message: "无效的工具标识。"}
	}
	if a.agentRegistry == nil {
		return AgentDiagnosticsResult{OK: false, Message: "工具中心尚未完成初始化。"}
	}
	ctx, cancel := a.agentOperationContext()
	defer cancel()
	diagnostics, err := a.agentRegistry.Diagnose(ctx, identifier)
	if err != nil {
		// Adapters may carry filesystem details in their native errors. Keep the
		// renderer result stable and credential-free; structured diagnostics are
		// the only user-visible diagnostic channel.
		return AgentDiagnosticsResult{OK: false, Message: "无法完成本机诊断。请检查该工具是否已正确安装，或导出脱敏诊断日志。", Diagnostics: diagnostics}
	}
	return AgentDiagnosticsResult{OK: true, Message: "本机诊断已完成。", Diagnostics: diagnostics}
}

// LaunchDetectedAgent opens only a verified Cursor or Windsurf installation.
// The renderer supplies an agent ID, never a file path: the concrete target is
// either revalidated from an explicit native selection or selected from a fresh
// bounded discovery result and validated again by the platform launcher.
// Antigravity and Codex keep their dedicated lifecycle transactions, while
// Claude Code intentionally remains a terminal workflow rather than opening an
// arbitrary shell session.
func (a *App) LaunchDetectedAgent(agentID string) AgentLaunchResult {
	identifier := agent.ID(strings.TrimSpace(agentID))
	if err := identifier.Validate(); err != nil {
		return AgentLaunchResult{Message: "无效的工具标识。"}
	}
	if identifier == agent.CursorID || identifier == agent.WindsurfID {
		// A manually selected application is a narrower, user-authorized launch
		// target than the normal discovery pass. It is still structurally
		// revalidated by the native layer immediately before launcher.Launch.
		if result, selected := a.launchSelectedAgentDesktop(identifier); selected {
			return result
		}
	}
	if a.agentRegistry == nil {
		return AgentLaunchResult{AgentID: identifier, Message: "工具中心尚未完成初始化。"}
	}
	return launchDetectedAgent(identifier, a.detectAgents(false), runtime.GOOS, launcher.Launch)
}

func launchDetectedAgent(identifier agent.ID, aggregate agent.AggregateStatus, platform string, launch agentApplicationLauncher) AgentLaunchResult {
	result := AgentLaunchResult{AgentID: identifier}
	if launch == nil {
		result.Message = "当前平台不支持从工具中心启动该应用。"
		return result
	}
	if identifier != agent.CursorID && identifier != agent.WindsurfID {
		result.Message = "该工具请使用其专用配置或生命周期入口。"
		return result
	}

	var status *agent.Status
	for index := range aggregate.Agents {
		if aggregate.Agents[index].AgentID == identifier {
			status = &aggregate.Agents[index]
			break
		}
	}
	if status == nil || (status.State != agent.StateReady && status.State != agent.StateDetected && status.State != agent.StateDegraded) {
		result.Message = "未找到可安全启动的本机安装。请完成安装后重新检查。"
		return result
	}

	target := ""
	switch platform {
	case "darwin":
		// macOS discovery validates both the bundle root and its executable.
		if verifiedDarwinDesktopLaunchTarget(status.Installation.Root, status.Installation.ExecutablePath) {
			target = status.Installation.Root
		}
	case "windows":
		// Windows discovery validates the executable under an approved install
		// root. The launcher independently verifies the .exe before starting it.
		if verifiedWindowsDesktopLaunchTarget(status.Installation.Root, status.Installation.ExecutablePath) {
			target = status.Installation.ExecutablePath
		}
	}
	if strings.TrimSpace(target) == "" {
		result.Message = "已检测到应用数据，但没有可验证的启动目标。请重新安装后再试。"
		return result
	}
	if err := launch(target); err != nil {
		// Platform launch errors may contain a local path, so keep this renderer
		// message stable and credential-free.
		result.Message = "无法启动该应用。请从系统应用列表手动打开后重新检查。"
		return result
	}
	result.OK = true
	result.Message = "已请求启动应用。"
	return result
}

// normalizedDesktopLaunchPath is intentionally string-only: launch tests run
// both platform paths on either host, while the platform launcher performs the
// filesystem validation immediately before execution. This helper prevents a
// discovered installation root from being paired with an unrelated executable.
func normalizedDesktopLaunchPath(value string) string {
	value = strings.ReplaceAll(strings.TrimSpace(value), `\`, "/")
	return strings.ToLower(strings.TrimRight(value, "/"))
}

func verifiedDarwinDesktopLaunchTarget(root, executable string) bool {
	root = normalizedDesktopLaunchPath(root)
	executable = normalizedDesktopLaunchPath(executable)
	return strings.HasSuffix(root, ".app") && strings.HasPrefix(executable, root+"/contents/macos/")
}

func verifiedWindowsDesktopLaunchTarget(root, executable string) bool {
	root = normalizedDesktopLaunchPath(root)
	executable = normalizedDesktopLaunchPath(executable)
	return root != "" && strings.HasPrefix(executable, root+"/") && strings.HasSuffix(executable, ".exe")
}

func (a *App) agentOperationContext() (context.Context, context.CancelFunc) {
	parent := a.ctx
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, 6*time.Second)
}

func unavailableAgentAggregate(message string) agent.AggregateStatus {
	return agent.AggregateStatus{
		GeneratedAt: time.Now().UTC(),
		State:       agent.StateError,
		Diagnostics: []agent.Diagnostic{{
			Code:      "agent.registry-unavailable",
			Severity:  agent.SeverityError,
			Summary:   "Agent registry is unavailable",
			Detail:    message,
			CreatedAt: time.Now().UTC(),
		}},
	}
}
