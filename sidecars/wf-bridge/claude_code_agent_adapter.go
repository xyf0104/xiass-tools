package main

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"runtime"
	"strings"
	"time"

	"antigravity-wf-assistant/internal/agent"
	"antigravity-wf-assistant/internal/agentdiscovery"
	"antigravity-wf-assistant/internal/claudeconfig"
)

// claudeCodeAgentAdapter is the concrete Claude Code integration. It owns no
// account state and only reports the safe state of the documented user
// settings.json target managed by claudeconfig.
type claudeCodeAgentAdapter struct {
	discoverCLI func() (string, error)
}

func newClaudeCodeAgentAdapter() *claudeCodeAgentAdapter {
	return &claudeCodeAgentAdapter{discoverCLI: agentdiscovery.DiscoverClaudeCodeCLI}
}

func (adapter *claudeCodeAgentAdapter) discoverCLIPath() (string, error) {
	if adapter == nil || adapter.discoverCLI == nil {
		return agentdiscovery.DiscoverClaudeCodeCLI()
	}
	return adapter.discoverCLI()
}

func (adapter *claudeCodeAgentAdapter) Metadata() agent.Metadata {
	for _, metadata := range agent.BuiltinMetadata() {
		if metadata.ID == agent.ClaudeCodeID {
			return metadata
		}
	}
	panic("Claude Code agent metadata is not registered")
}

// Detect combines a non-invasive PATH check with the transaction manager's
// redacted user-settings inspection. It never executes Claude Code, reads a
// credential file, or treats a configured token as proof of account login.
func (adapter *claudeCodeAgentAdapter) Detect(ctx context.Context) (agent.Status, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return agent.Status{}, err
		}
	}
	metadata := adapter.Metadata()
	manager, err := claudeconfig.NewDefaultManager()
	if err != nil {
		return claudeCodeAgentErrorStatus(metadata), nil
	}

	snapshot, inspectErr := manager.Inspect()
	backupAvailable := false
	if inspectErr == nil && snapshot.Valid {
		if _, err := manager.ListBackups(); err == nil {
			backupAvailable = true
		}
	}

	cliPath, cliErr := adapter.discoverCLIPath()
	cliPath = strings.TrimSpace(cliPath)
	cliFound := cliPath != "" && cliErr == nil
	cliDiscoveryIssue := cliErr != nil
	return claudeCodeAgentSnapshotStatus(metadata, snapshot, inspectErr, cliPath, cliFound, cliDiscoveryIssue, backupAvailable), nil
}

func (adapter *claudeCodeAgentAdapter) Diagnose(ctx context.Context) ([]agent.Diagnostic, error) {
	status, err := adapter.Detect(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	diagnostics := make([]agent.Diagnostic, 0, 2)
	switch status.State {
	case agent.StateError:
		diagnostics = append(diagnostics, agent.Diagnostic{
			AgentID: agent.ClaudeCodeID, Code: "claude-code.settings-location-unavailable", Severity: agent.SeverityError,
			Summary:     "无法使用 Claude Code 用户设置位置",
			Detail:      "XIASS Tools 无法安全确定所选 Claude Code settings.json 的位置。",
			Remediation: "请检查 CLAUDE_CONFIG_DIR 或本机 Claude Code 用户目录，再重新检查。", CreatedAt: now,
		})
	case agent.StateDegraded:
		diagnostics = append(diagnostics, agent.Diagnostic{
			AgentID: agent.ClaudeCodeID, Code: "claude-code.settings-needs-attention", Severity: agent.SeverityError,
			Summary:     "Claude Code 用户设置需要处理",
			Detail:      "无法安全验证所选 settings.json，未修改任何设置。",
			Remediation: "请先修复 JSON 文件，或恢复一个已验证的用户设置备份，再保存新配置。", CreatedAt: now,
		})
	case agent.StateNotInstalled:
		diagnostics = append(diagnostics, agent.Diagnostic{
			AgentID: agent.ClaudeCodeID, Code: "claude-code.cli-not-found", Severity: agent.SeverityInfo,
			Summary:     "未找到 Claude Code CLI",
			Detail:      "XIASS Tools 可以准备所选用户设置，但这不代表 Claude Code 已安装或已登录。",
			Remediation: "请安装或打开 Claude Code，再重新检查本机。", CreatedAt: now,
		})
	case agent.StateDetected:
		diagnostics = append(diagnostics, agent.Diagnostic{
			AgentID: agent.ClaudeCodeID, Code: "claude-code.settings-not-managed", Severity: agent.SeverityInfo,
			Summary:     "Claude Code 用户设置可用",
			Detail:      "所选 settings.json 有效，但尚未完全由 XIASS Tools 管理。",
			Remediation: "请通过 Claude Code 配置流程保存 API 根地址、一个凭据方式、可选的网关发现和模型字段。", CreatedAt: now,
		})
	}
	return diagnostics, nil
}

func claudeCodeAgentSnapshotStatus(metadata agent.Metadata, snapshot claudeconfig.Snapshot, inspectErr error, cliPath string, cliFound, cliDiscoveryIssue, backupAvailable bool) agent.Status {
	validConfig := inspectErr == nil && snapshot.Valid
	canWriteConfig := validConfig && backupAvailable
	state := agent.StateDetected
	message := "Claude Code 用户设置已可以配置。"
	switch {
	case !validConfig:
		state = agent.StateDegraded
		message = "无法安全验证所选 Claude Code settings.json。"
	case !cliFound:
		state = agent.StateNotInstalled
		if snapshot.Managed {
			message = "已找到由 XIASS Tools 管理的 Claude Code 设置，但未找到 Claude Code CLI。"
		} else {
			message = "未找到 Claude Code CLI。可以检查本机用户设置，但这不能证明客户端已安装。"
		}
	case !canWriteConfig:
		state = agent.StateDegraded
		message = "Claude Code 设置可读取，但无法安全验证回滚备份位置。"
	case snapshot.Managed:
		state = agent.StateReady
		message = "Claude Code 用户设置已由 XIASS Tools 配置。"
	case snapshot.Location.Exists:
		message = "已发现有效的 Claude Code 用户设置文件。"
	}
	if cliDiscoveryIssue {
		state = agent.StateDegraded
		message = "已检查 Claude Code 用户设置，但未能完成 CLI 发现。"
	}

	status := agent.Status{
		AgentID:     agent.ClaudeCodeID,
		DisplayName: metadata.DisplayName,
		State:       state,
		Message:     message,
		Installation: agent.Installation{
			ExecutablePath: cliPath,
			Platform:       runtime.GOOS,
		},
		Details: map[string]string{
			"settingsPresent":    fmt.Sprintf("%t", snapshot.Location.Exists),
			"settingsValid":      fmt.Sprintf("%t", validConfig),
			"managed":            fmt.Sprintf("%t", snapshot.Managed),
			"cliPresent":         fmt.Sprintf("%t", cliFound),
			"loopbackConfigured": fmt.Sprintf("%t", claudeCodeLoopbackConfigured(snapshot, validConfig)),
			"backupAvailable":    fmt.Sprintf("%t", backupAvailable),
			"loopbackChecked":    "false",
		},
		UpdatedAt: time.Now().UTC(),
	}
	status.Capabilities = claudeCodeCapabilityStatuses(metadata, validConfig, snapshot.Managed, claudeCodeLoopbackConfigured(snapshot, validConfig), backupAvailable)
	return status
}

func claudeCodeAgentErrorStatus(metadata agent.Metadata) agent.Status {
	status := agent.Status{
		AgentID:     agent.ClaudeCodeID,
		DisplayName: metadata.DisplayName,
		State:       agent.StateError,
		Message:     "无法安全确定 Claude Code 用户设置位置。",
		UpdatedAt:   time.Now().UTC(),
	}
	status.Capabilities = claudeCodeCapabilityStatuses(metadata, false, false, false, false)
	return status
}

func claudeCodeCapabilityStatuses(metadata agent.Metadata, validConfig, managed, loopbackConfigured, backupAvailable bool) []agent.CapabilityStatus {
	statuses := make([]agent.CapabilityStatus, 0, len(metadata.Capabilities))
	for _, declaration := range metadata.Capabilities {
		status := agent.CapabilityStatus{
			Capability:   declaration.Capability,
			Availability: declaration.Availability,
			Available:    false,
			Reason:       "Claude Code 尚未实现此功能。",
		}
		switch declaration.Capability {
		case agent.CapabilityDiscovery:
			status.Availability = agent.CapabilityAvailable
			status.Available = true
			status.Reason = "已完成本机 Claude Code 用户设置发现。"
		case agent.CapabilityConfiguration:
			status.Availability = agent.CapabilityAvailable
			status.Available = validConfig && backupAvailable
			if validConfig {
				if backupAvailable {
					status.Reason = "可通过已验证的原子写入和恢复备份修改所选 settings.json。"
				} else {
					status.Reason = "所选 settings.json 可读取，但恢复备份位置无法安全写入。"
				}
			} else {
				status.Reason = "必须先修复所选 settings.json，才能进行修改。"
			}
		case agent.CapabilityModelCatalog:
			status.Availability = agent.CapabilityAvailable
			status.Available = validConfig
			if validConfig {
				status.Reason = "可使用用户提供的网关凭据进行一次模型目录发现；这不代表已有当前目录或推理结果。"
			} else {
				status.Reason = "需要有效的 settings.json，才能使用模型选择和网关发现。"
			}
		case agent.CapabilityLocalProxy:
			status.Availability = agent.CapabilityAvailable
			// Detection deliberately never dials an arbitrary configured endpoint.
			// A loopback URL is useful configuration metadata, not proof that a
			// listener or a Claude-compatible proxy is running right now.
			status.Available = false
			if managed && loopbackConfigured {
				status.Reason = "受管 API 根地址指向本机回环地址，但尚未测试端点健康状态。"
			} else {
				status.Reason = "尚未配置或检查受管的本机回环 API 根地址。"
			}
		case agent.CapabilityDiagnostics:
			status.Availability = agent.CapabilityAvailable
			status.Available = true
			status.Reason = "可使用不会暴露凭据的本机用户设置诊断。"
		case agent.CapabilityBackup:
			status.Availability = agent.CapabilityAvailable
			status.Available = validConfig && backupAvailable
			if status.Available {
				status.Reason = "可使用已验证的 Claude Code 用户设置备份。"
			} else {
				status.Reason = "必须先安全验证用户设置和备份位置。"
			}
		}
		statuses = append(statuses, status)
	}
	return statuses
}

func claudeCodeLoopbackConfigured(snapshot claudeconfig.Snapshot, validConfig bool) bool {
	if !validConfig || !snapshot.Managed || snapshot.BaseURL == "" {
		return false
	}
	parsed, err := url.Parse(snapshot.BaseURL)
	if err != nil || parsed == nil {
		return false
	}
	host := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(parsed.Hostname())), ".")
	if host == "localhost" {
		return true
	}
	parsedIP := net.ParseIP(host)
	return parsedIP != nil && parsedIP.IsLoopback()
}

var _ agent.Adapter = (*claudeCodeAgentAdapter)(nil)
var _ agent.DiagnosticProvider = (*claudeCodeAgentAdapter)(nil)
