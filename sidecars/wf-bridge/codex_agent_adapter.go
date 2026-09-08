package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"antigravity-wf-assistant/internal/agent"
	"antigravity-wf-assistant/internal/codexconfig"
	"antigravity-wf-assistant/internal/codexdesktop"
)

// codexAgentAdapter exposes the verified, local Codex configuration lifecycle
// through the shared Agent Registry. It intentionally does not read
// ~/.codex/auth.json, claim an OpenAI account is connected, or start/stop a
// Codex application.
type codexAgentAdapter struct{}

func newCodexAgentAdapter() *codexAgentAdapter { return &codexAgentAdapter{} }

func (adapter *codexAgentAdapter) Metadata() agent.Metadata {
	for _, metadata := range agent.BuiltinMetadata() {
		if metadata.ID == agent.CodexID {
			return metadata
		}
	}
	panic("Codex agent metadata is not registered")
}

func (adapter *codexAgentAdapter) Detect(ctx context.Context) (agent.Status, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return agent.Status{}, err
		}
	}
	metadata := adapter.Metadata()
	desktop := codexdesktop.Discover(ctx)
	manager, err := codexconfig.NewDefaultManager()
	if err != nil {
		return decorateCodexDesktopStatus(codexAgentErrorStatus(metadata, err), desktop), nil
	}
	homeExists, homeError := existingCodexDirectory(manager.CodexHome)
	if homeError != nil {
		return decorateCodexDesktopStatus(codexAgentErrorStatus(metadata, homeError), desktop), nil
	}
	snapshot, inspectErr := manager.Inspect()
	if inspectErr != nil {
		return decorateCodexDesktopStatus(codexAgentSnapshotStatus(metadata, snapshot, homeExists, inspectErr), desktop), nil
	}
	return decorateCodexDesktopStatus(codexAgentSnapshotStatus(metadata, snapshot, homeExists, nil), desktop), nil
}

func (adapter *codexAgentAdapter) Diagnose(ctx context.Context) ([]agent.Diagnostic, error) {
	status, err := adapter.Detect(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	diagnostics := make([]agent.Diagnostic, 0, 3)
	if status.Details["desktopState"] == string(codexdesktop.StateDegraded) {
		diagnostics = append(diagnostics, agent.Diagnostic{
			AgentID: agent.CodexID, Code: "codex.desktop-inspection-incomplete", Severity: agent.SeverityWarning,
			Summary: "无法完整验证 Codex Desktop 安装", Detail: status.Message,
			Remediation: "如果 Codex 正在运行，请先退出，再重新检查本机。XIASS Tools 不会自动关闭或控制 Codex。", CreatedAt: now,
		})
	}
	switch status.State {
	case agent.StateNotInstalled:
		diagnostics = append(diagnostics, agent.Diagnostic{
			AgentID: agent.CodexID, Code: "codex.home-not-found", Severity: agent.SeverityInfo,
			Summary: "未找到本机 Codex 配置目录", Detail: status.Message,
			Remediation: "请先打开一次 Codex，或将 CODEX_HOME 设置为要使用的本机配置目录。", CreatedAt: now,
		})
	case agent.StateDegraded:
		diagnostics = append(diagnostics, agent.Diagnostic{
			AgentID: agent.CodexID, Code: "codex.config-invalid", Severity: agent.SeverityError,
			Summary: "Codex 配置需要处理", Detail: status.Message,
			Remediation: "请先修复无效的 config.toml，或恢复一个已验证的 XIASS Tools 备份，再应用新配置。", CreatedAt: now,
		})
	case agent.StateDetected:
		diagnostics = append(diagnostics, agent.Diagnostic{
			AgentID: agent.CodexID, Code: "codex.not-configured", Severity: agent.SeverityInfo,
			Summary: "已找到 Codex 配置，但尚未由 XIASS Tools 管理", Detail: status.Message,
			Remediation: "请通过 Codex 配置流程添加独立的 XIASS Tools Provider；不会替换无关 Provider。", CreatedAt: now,
		})
	}
	return diagnostics, nil
}

func codexAgentSnapshotStatus(metadata agent.Metadata, snapshot codexconfig.ConfigSnapshot, homeExists bool, inspectErr error) agent.Status {
	state := agent.StateDetected
	message := "已发现本机 Codex 配置目录。"
	switch {
	case !homeExists:
		state = agent.StateNotInstalled
		message = "未找到本机 Codex 配置目录。"
	case inspectErr != nil || !snapshot.Valid:
		state = agent.StateDegraded
		message = "无法验证本机 Codex config.toml。"
	case !snapshot.Location.Exists:
		message = "Codex 配置目录已就绪，但尚未创建 config.toml。"
	case snapshot.ModelProvider == codexconfig.DefaultProviderID && snapshot.ManagedProviderVerified:
		state = agent.StateReady
		message = "Codex 已配置为使用 XIASS Tools Provider。"
	case snapshot.ModelProvider == codexconfig.DefaultProviderID:
		// Do not report a generic, merely parseable xiass_tools entry as ready.
		// Inspect has already reduced the reason to a fixed non-sensitive enum;
		// no config value or credential is copied into this agent status.
		state = agent.StateDegraded
		message = "当前 XIASS Tools Provider 未通过结构验证。"
	default:
		message = "Codex config.toml 有效，但当前启用的不是 XIASS Tools Provider。"
	}
	status := agent.Status{
		AgentID:     agent.CodexID,
		DisplayName: metadata.DisplayName,
		State:       state,
		Message:     message,
		Details: map[string]string{
			"configPresent":           fmt.Sprintf("%t", snapshot.Location.Exists),
			"configValid":             fmt.Sprintf("%t", snapshot.Valid && inspectErr == nil),
			"provider":                snapshot.ModelProvider,
			"managedProviderVerified": fmt.Sprintf("%t", snapshot.ManagedProviderVerified),
		},
		UpdatedAt: time.Now().UTC(),
	}
	if inspectErr != nil {
		status.Details["inspectionError"] = redactCodexInspectionError(inspectErr)
	}
	if snapshot.ManagedProviderIssue != codexconfig.ManagedProviderIssueNone {
		status.Details["managedProviderIssue"] = string(snapshot.ManagedProviderIssue)
	}
	status.Capabilities = codexCapabilityStatuses(metadata, homeExists, snapshot.Location.Exists, snapshot.Valid && inspectErr == nil)
	return status
}

// decorateCodexDesktopStatus keeps desktop discovery and config discovery
// distinct. A config.toml is not an executable, so it is deliberately never
// reported as Installation.ExecutablePath.
func decorateCodexDesktopStatus(status agent.Status, desktop codexdesktop.Status) agent.Status {
	if status.Details == nil {
		status.Details = make(map[string]string)
	}
	status.Details["desktopState"] = string(desktop.State)
	status.Details["desktopRunning"] = fmt.Sprintf("%t", desktop.Running)
	status.Details["desktopPresent"] = fmt.Sprintf("%t", desktop.Installation.Present)
	if desktop.Installation.Source != "" {
		status.Details["desktopSource"] = string(desktop.Installation.Source)
	}
	if desktop.Installation.Present {
		status.Installation = agent.Installation{
			Version:  desktop.Installation.Version,
			Platform: "desktop",
		}
	}

	switch desktop.State {
	case codexdesktop.StateRunning:
		status.Message = joinCodexStatusMessage(status.Message, "Codex Desktop 正在运行；配置变更会在你明确重启后生效。")
	case codexdesktop.StateInstalled:
		if status.State == agent.StateNotInstalled {
			status.State = agent.StateDetected
			status.Message = "已找到 Codex Desktop，但尚未创建本机配置。"
		} else {
			status.Message = joinCodexStatusMessage(status.Message, "已安装 Codex Desktop。")
		}
	case codexdesktop.StateNotInstalled:
		if status.State != agent.StateNotInstalled {
			status.Message = joinCodexStatusMessage(status.Message, "在受支持的公开安装位置中未找到 Codex Desktop。")
		}
	case codexdesktop.StateDegraded:
		status.State = agent.StateDegraded
		status.Message = joinCodexStatusMessage(status.Message, "无法完整验证 Codex Desktop；在能够安全确认运行状态前，不会写入历史记录。")
	}
	return status
}

func joinCodexStatusMessage(left, right string) string {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	if left == "" {
		return right
	}
	if right == "" {
		return left
	}
	return left + " " + right
}

func codexAgentErrorStatus(metadata agent.Metadata, err error) agent.Status {
	return agent.Status{
		AgentID:      agent.CodexID,
		DisplayName:  metadata.DisplayName,
		State:        agent.StateError,
		Message:      "无法确定本机 Codex 配置位置。",
		Details:      map[string]string{"inspectionError": redactCodexInspectionError(err)},
		UpdatedAt:    time.Now().UTC(),
		Capabilities: codexCapabilityStatuses(metadata, false, false, false),
	}
}

func codexCapabilityStatuses(metadata agent.Metadata, homeExists, configExists, configValid bool) []agent.CapabilityStatus {
	statuses := make([]agent.CapabilityStatus, 0, len(metadata.Capabilities))
	for _, declaration := range metadata.Capabilities {
		status := agent.CapabilityStatus{
			Capability:   declaration.Capability,
			Availability: declaration.Availability,
			Reason:       "尚未在本机验证此功能。",
		}
		switch declaration.Capability {
		case agent.CapabilityDiscovery:
			status.Availability = agent.CapabilityAvailable
			status.Available = true
			status.Reason = "已完成本机 Codex 配置发现。"
		case agent.CapabilityConfiguration, agent.CapabilityModelCatalog:
			status.Availability = agent.CapabilityAvailable
			// A missing ~/.codex is an initial state, not an unsafe state. The
			// manager has already resolved its fixed default target and can create
			// the directory through the same verified atomic-write path used for
			// an existing valid config.toml.
			status.Available = configValid
			if status.Available {
				if homeExists {
					status.Reason = "可通过已验证的原子写入管理 Codex 配置。"
				} else {
					status.Reason = "可在已验证的默认配置位置安全初始化 Codex。"
				}
			} else if homeExists {
				status.Reason = "必须先修复 Codex 配置，才能进行修改。"
			} else {
				status.Reason = "无法安全准备 Codex 配置位置。"
			}
		case agent.CapabilityDiagnostics:
			status.Availability = agent.CapabilityAvailable
			status.Available = true
			status.Reason = "可使用不会暴露凭据的本机诊断。"
		case agent.CapabilityBackup, agent.CapabilitySessionRecovery:
			status.Availability = agent.CapabilityAvailable
			status.Available = homeExists && configExists && configValid
			if status.Available {
				status.Reason = "可使用已验证的本机备份与恢复操作。"
			} else {
				status.Reason = "需要有效的本机 config.toml，才能使用恢复操作。"
			}
		}
		statuses = append(statuses, status)
	}
	return statuses
}

func existingCodexDirectory(path string) (bool, error) {
	info, err := os.Stat(path)
	if err == nil {
		if !info.IsDir() {
			return false, fmt.Errorf("Codex 主目录不是文件夹")
		}
		return true, nil
	}
	if errorsIsNotExist(err) {
		return false, nil
	}
	return false, err
}

func errorsIsNotExist(err error) bool {
	return err != nil && os.IsNotExist(err)
}

func redactCodexInspectionError(err error) string {
	if err == nil {
		return ""
	}
	return "本机 Codex 配置检查未能安全完成。"
}

var _ agent.Adapter = (*codexAgentAdapter)(nil)
var _ agent.DiagnosticProvider = (*codexAgentAdapter)(nil)
