package main

import (
	"context"
	"fmt"
	"time"

	"antigravity-wf-assistant/internal/agent"
	"antigravity-wf-assistant/internal/agentdiscovery"
	"antigravity-wf-assistant/internal/mcpconfig"
)

// mcpAgentAdapter binds Cursor to its documented global MCP configuration and
// a project MCP file only after an explicit native project selection. Windsurf
// remains global-only. Neither integration inspects private account data,
// browser storage, conversations, or undocumented client files.
type mcpAgentAdapter struct {
	id        agent.ID
	target    mcpconfig.Target
	discovery agent.Adapter
}

func newCursorMCPAgentAdapter() *mcpAgentAdapter {
	return newMCPAgentAdapter(agent.CursorID, mcpconfig.TargetCursor, agentdiscovery.NewCursorAdapter())
}

func newWindsurfMCPAgentAdapter() *mcpAgentAdapter {
	return newMCPAgentAdapter(agent.WindsurfID, mcpconfig.TargetWindsurf, agentdiscovery.NewWindsurfAdapter())
}

func newMCPAgentAdapter(id agent.ID, target mcpconfig.Target, discovery agent.Adapter) *mcpAgentAdapter {
	return &mcpAgentAdapter{id: id, target: target, discovery: discovery}
}

func (adapter *mcpAgentAdapter) Metadata() agent.Metadata {
	for _, metadata := range agent.BuiltinMetadata() {
		if metadata.ID == adapter.id {
			return metadata
		}
	}
	panic("MCP client agent metadata is not registered")
}

func (adapter *mcpAgentAdapter) Detect(ctx context.Context) (agent.Status, error) {
	if adapter == nil || adapter.discovery == nil {
		return agent.Status{}, fmt.Errorf("MCP client adapter is unavailable")
	}
	discovered, err := adapter.discovery.Detect(ctx)
	if err != nil {
		return agent.Status{}, err
	}
	metadata := adapter.Metadata()
	manager, managerErr := mcpconfig.NewDefaultManager(adapter.target)
	if managerErr != nil {
		return mcpAgentStatus(metadata, discovered, mcpconfig.Snapshot{Target: adapter.target}, false, false), nil
	}
	snapshot, inspectErr := manager.Inspect()
	return mcpAgentStatus(metadata, discovered, snapshot, inspectErr == nil, inspectErr == nil), nil
}

func (adapter *mcpAgentAdapter) Diagnose(ctx context.Context) ([]agent.Diagnostic, error) {
	status, err := adapter.Detect(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	diagnostics := make([]agent.Diagnostic, 0, 2)
	if !mcpClientDetected(status) {
		diagnostics = append(diagnostics, agent.Diagnostic{
			AgentID: adapter.id, Code: string(adapter.id) + ".not-installed", Severity: agent.SeverityInfo,
			Summary:     "未找到 " + adapter.Metadata().DisplayName,
			Detail:      mcpNotInstalledDetail(adapter.Metadata()),
			Remediation: "请安装或打开客户端，再重新检查本机。", CreatedAt: now,
		})
		return diagnostics, nil
	}
	if status.Details["mcpConfigSafe"] != "true" {
		diagnostics = append(diagnostics, agent.Diagnostic{
			AgentID: adapter.id, Code: string(adapter.id) + ".mcp-config-unsafe", Severity: agent.SeverityWarning,
			Summary:     "无法安全修改全局 MCP 配置",
			Detail:      "XIASS Tools 检测到无效或包含敏感内容的配置，未显示或修改其内容。",
			Remediation: "请先在客户端中处理该配置，再重新检查本机。", CreatedAt: now,
		})
		return diagnostics, nil
	}
	if status.Details["mcpManaged"] != "true" {
		diagnostics = append(diagnostics, agent.Diagnostic{
			AgentID: adapter.id, Code: string(adapter.id) + ".mcp-not-configured", Severity: agent.SeverityInfo,
			Summary:     "全局 MCP 配置已就绪",
			Detail:      "当前没有启用 XIASS Tools MCP 条目。",
			Remediation: "请使用配置操作添加保留的 XIASS Tools 远程 MCP 条目。", CreatedAt: now,
		})
	}
	return diagnostics, nil
}

func mcpAgentStatus(metadata agent.Metadata, discovered agent.Status, snapshot mcpconfig.Snapshot, inspected, inspectSafe bool) agent.Status {
	clientDetected := mcpClientDetected(discovered)
	configurationSafe := inspected && inspectSafe && snapshot.Valid && !snapshot.HasSensitiveConfiguration
	status := discovered
	status.AgentID = metadata.ID
	status.DisplayName = metadata.DisplayName
	if status.Details == nil {
		status.Details = make(map[string]string)
	}
	status.Details["mcpConfigPresent"] = fmt.Sprintf("%t", snapshot.Exists)
	status.Details["mcpConfigValid"] = fmt.Sprintf("%t", snapshot.Valid && inspected)
	status.Details["mcpConfigSafe"] = fmt.Sprintf("%t", configurationSafe)
	status.Details["mcpSensitive"] = fmt.Sprintf("%t", snapshot.HasSensitiveConfiguration)
	status.Details["mcpManaged"] = fmt.Sprintf("%t", snapshot.ManagedServerConfigured)

	switch {
	case !clientDetected:
		status.Message = "未找到 " + metadata.DisplayName + "，不会修改其全局 MCP 配置。"
	case !configurationSafe:
		status.State = agent.StateDegraded
		if snapshot.HasSensitiveConfiguration {
			status.Message = "全局 MCP 配置包含敏感值，在 XIASS Tools 中只能读取。"
		} else {
			status.Message = "无法安全验证全局 MCP 配置。"
		}
	case snapshot.ManagedServerConfigured:
		status.State = agent.StateReady
		status.Message = "已在通过验证的全局 MCP 配置中设置 XIASS Tools MCP 条目。"
	case status.State == agent.StateReady || status.State == agent.StateDetected:
		status.State = agent.StateDetected
		status.Message = "已安装 " + metadata.DisplayName + "，其全局 MCP 配置已可以设置。"
	}
	status.Capabilities = mcpAgentCapabilities(metadata, clientDetected, configurationSafe)
	return status
}

func mcpAgentCapabilities(metadata agent.Metadata, clientDetected, configurationSafe bool) []agent.CapabilityStatus {
	statuses := make([]agent.CapabilityStatus, 0, len(metadata.Capabilities))
	for _, declaration := range metadata.Capabilities {
		status := agent.CapabilityStatus{
			Capability: declaration.Capability, Availability: declaration.Availability,
			Reason: mcpUnsupportedCapabilityReason(metadata),
		}
		switch declaration.Capability {
		case agent.CapabilityDiscovery:
			status.Availability = agent.CapabilityAvailable
			status.Available = true
			status.Reason = "已完成本机应用发现，未读取任何私密账号数据。"
		case agent.CapabilityConfiguration:
			status.Availability = agent.CapabilityAvailable
			status.Available = clientDetected && configurationSafe
			if status.Available {
				status.Reason = mcpAvailableConfigurationReason(metadata)
			} else if !clientDetected {
				status.Reason = mcpUndetectedConfigurationReason(metadata)
			} else {
				status.Reason = mcpUnsafeConfigurationReason(metadata)
			}
		case agent.CapabilityDiagnostics:
			status.Availability = agent.CapabilityAvailable
			status.Available = true
			status.Reason = mcpDiagnosticsReason(metadata)
		case agent.CapabilityBackup:
			// The bridge can list, restore, and delete only verified recovery
			// points created by explicit MCP Apply/Restore operations. It
			// intentionally does not implement the registry's broad BackupProvider
			// contract, which could imply account, session, or arbitrary filesystem
			// backups. Cursor keeps global and explicit-project recovery points
			// isolated; Windsurf has only global recovery points.
			status.Availability = agent.CapabilityNotImplemented
			status.Available = false
			status.Reason = mcpRecoveryPointReason(metadata)
		}
		statuses = append(statuses, status)
	}
	return statuses
}

func mcpSupportsExplicitProjectConfiguration(metadata agent.Metadata) bool {
	return metadata.ID == agent.CursorID
}

func mcpNotInstalledDetail(metadata agent.Metadata) string {
	if mcpSupportsExplicitProjectConfiguration(metadata) {
		return "在本机检测到 Cursor 前，XIASS Tools 不会创建或修改全局 MCP 配置；仍可通过明确的项目 MCP 操作为单独选择的 Cursor 项目进行配置。"
	}
	return "在本机检测到该客户端前，XIASS Tools 不会创建或修改其全局 MCP 配置。"
}

func mcpUnsupportedCapabilityReason(metadata agent.Metadata) string {
	if mcpSupportsExplicitProjectConfiguration(metadata) {
		return "Cursor 的公开全局 MCP 配置或明确选择的项目 MCP 配置尚未实现此功能。"
	}
	return "公开的全局 MCP 配置尚未实现此功能。"
}

func mcpAvailableConfigurationReason(metadata agent.Metadata) string {
	if mcpSupportsExplicitProjectConfiguration(metadata) {
		return "可通过原子备份和回滚修改公开的全局 MCP 配置；单独选择项目后，可明确管理该项目的 .cursor/mcp.json。"
	}
	return "可通过原子备份和回滚修改公开的全局 MCP 配置。"
}

func mcpUndetectedConfigurationReason(metadata agent.Metadata) string {
	if mcpSupportsExplicitProjectConfiguration(metadata) {
		return "必须先在本机检测到 Cursor，才能修改其全局 MCP 配置；单独选择的项目仍可使用明确的项目 MCP 操作。"
	}
	return "必须先在本机检测到客户端，才能修改其 MCP 配置。"
}

func mcpUnsafeConfigurationReason(metadata agent.Metadata) string {
	if mcpSupportsExplicitProjectConfiguration(metadata) {
		return "现有全局 MCP 配置必须安全且有效，才能修改；明确选择项目后会单独检查项目 MCP 配置。"
	}
	return "现有 MCP 配置必须安全且有效，才能修改。"
}

func mcpDiagnosticsReason(metadata agent.Metadata) string {
	if mcpSupportsExplicitProjectConfiguration(metadata) {
		return "可使用不包含凭据的全局 MCP 诊断；只有明确选择项目后才会执行项目 MCP 诊断。"
	}
	return "可使用不包含凭据的本机 MCP 配置诊断。"
}

func mcpRecoveryPointReason(metadata agent.Metadata) string {
	if mcpSupportsExplicitProjectConfiguration(metadata) {
		return "已验证的恢复点仅由明确的全局 MCP 或所选项目 MCP 应用/恢复操作创建；不会开放通用 Agent 备份。"
	}
	return "已验证的恢复点仅由明确的全局 MCP 配置应用/恢复操作创建；不会开放通用 Agent 备份。"
}

func mcpClientDetected(status agent.Status) bool {
	return status.State == agent.StateReady || status.State == agent.StateDetected
}

var _ agent.Adapter = (*mcpAgentAdapter)(nil)
var _ agent.DiagnosticProvider = (*mcpAgentAdapter)(nil)
