package main

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"time"

	"antigravity-wf-assistant/internal/agent"
	"antigravity-wf-assistant/internal/patcher"
)

// antigravityAgentAdapter projects the existing Antigravity integration onto
// the generic XIASS Tools agent contract. It deliberately delegates all
// mutation to the established patch and launch actions in App; discovery here
// is read-only and cannot write into an Antigravity installation.
type antigravityAgentAdapter struct {
	mu        sync.RWMutex
	cached    agent.Status
	cachedAt  time.Time
	cacheLife time.Duration
}

func newAntigravityAgentAdapter() *antigravityAgentAdapter {
	return &antigravityAgentAdapter{cacheLife: 15 * time.Second}
}

func (adapter *antigravityAgentAdapter) Metadata() agent.Metadata {
	for _, metadata := range agent.BuiltinMetadata() {
		if metadata.ID == agent.AntigravityID {
			return metadata
		}
	}
	panic("Antigravity agent metadata is not registered")
}

// Detect keeps first paint responsive by using the existing bounded quick
// scanner. A user-initiated refresh is handled by Refresh, which stores the
// authoritative deep scan for the registry's next read.
func (adapter *antigravityAgentAdapter) Detect(ctx context.Context) (agent.Status, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return agent.Status{}, err
		}
	}
	if status, ok := adapter.cachedStatus(); ok {
		return status, nil
	}
	status := antigravityAgentStatus(adapter.Metadata(), patcher.GetQuickStatus())
	adapter.store(status)
	return status, nil
}

// Refresh performs the same authoritative structural scan used by the
// dashboard Refresh action. It never treats a discovered executable as safe
// to patch unless the existing patcher marked that target supported.
func (adapter *antigravityAgentAdapter) Refresh(ctx context.Context) (agent.Status, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return agent.Status{}, err
		}
	}
	status := antigravityAgentStatus(adapter.Metadata(), patcher.RefreshStatus())
	adapter.store(status)
	return status, nil
}

func (adapter *antigravityAgentAdapter) Diagnose(ctx context.Context) ([]agent.Diagnostic, error) {
	status, err := adapter.Detect(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	diagnostics := make([]agent.Diagnostic, 0, 3)
	supportedTargets, patchedTargets := targetCounts(status.Details)
	switch status.State {
	case agent.StateNotInstalled:
		diagnostics = append(diagnostics, agent.Diagnostic{
			AgentID: agent.AntigravityID, Code: "antigravity.not-installed", Severity: agent.SeverityInfo,
			Summary: "未检测到 Antigravity 安装", Detail: status.Message,
			Remediation: "请安装 Antigravity IDE 或 Antigravity 2.0，再重新检查本机。", CreatedAt: now,
		})
	case agent.StateDegraded:
		diagnostics = append(diagnostics, agent.Diagnostic{
			AgentID: agent.AntigravityID, Code: "antigravity.requires-attention", Severity: agent.SeverityWarning,
			Summary: "Antigravity 需要处理后才能安全使用", Detail: status.Message,
			Remediation: "请在运行总览中检查检测到的安装，仅在显示支持时应用连接补丁。", CreatedAt: now,
		})
	}
	if supportedTargets > 0 && patchedTargets < supportedTargets {
		diagnostics = append(diagnostics, agent.Diagnostic{
			AgentID: agent.AntigravityID, Code: "antigravity.patch-incomplete", Severity: agent.SeverityWarning,
			Summary: "一个或多个受支持的安装尚未连接", Detail: fmt.Sprintf("%d/%d 个受支持的安装已连接。", patchedTargets, supportedTargets),
			Remediation: "请在 Antigravity 运行总览中连接所有受支持的安装。", CreatedAt: now,
		})
	}
	return diagnostics, nil
}

func (adapter *antigravityAgentAdapter) cachedStatus() (agent.Status, bool) {
	adapter.mu.RLock()
	defer adapter.mu.RUnlock()
	if adapter.cachedAt.IsZero() || time.Since(adapter.cachedAt) > adapter.cacheLife {
		return agent.Status{}, false
	}
	return adapter.cached, true
}

func (adapter *antigravityAgentAdapter) store(status agent.Status) {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	adapter.cached = status
	adapter.cachedAt = time.Now()
}

func antigravityAgentStatus(metadata agent.Metadata, snapshot patcher.Status) agent.Status {
	targets := snapshot.Targets
	supportedTargets := 0
	patchedTargets := 0
	unsupportedTargets := 0
	var primary *patcher.TargetStatus
	for index := range targets {
		target := &targets[index]
		if target.Supported {
			supportedTargets++
			if target.Patched {
				patchedTargets++
			}
			if primary == nil || !primary.Supported {
				primary = target
			}
			continue
		}
		unsupportedTargets++
		if primary == nil {
			primary = target
		}
	}

	state := agent.StateDetected
	message := "已检测到 Antigravity 安装；应用变更前请先确认兼容状态。"
	switch {
	case len(targets) == 0:
		state = agent.StateNotInstalled
		message = "未检测到 Antigravity IDE 或 Antigravity 2.0。"
	case supportedTargets == 0:
		state = agent.StateDegraded
		message = "已检测到 Antigravity，但当前结构尚未通过兼容检查。"
	case patchedTargets == supportedTargets && snapshot.ProxyListening:
		state = agent.StateReady
		message = "所有受支持的 Antigravity 安装均已连接到本地代理。"
	case patchedTargets > 0:
		state = agent.StateDegraded
		message = "已检测到 Antigravity，但部分受支持的安装仍需处理。"
	case snapshot.ProxyListening:
		message = "已检测到受支持的 Antigravity 安装，可以开始连接。"
	default:
		message = "已检测到受支持的 Antigravity 安装；请先启动本地代理再连接。"
	}

	status := agent.Status{
		AgentID:     agent.AntigravityID,
		DisplayName: metadata.DisplayName,
		State:       state,
		Message:     message,
		UpdatedAt:   time.Now().UTC(),
		Details: map[string]string{
			"targetCount":        fmt.Sprintf("%d", len(targets)),
			"supportedTargets":   fmt.Sprintf("%d", supportedTargets),
			"patchedTargets":     fmt.Sprintf("%d", patchedTargets),
			"unsupportedTargets": fmt.Sprintf("%d", unsupportedTargets),
		},
	}
	if primary != nil {
		status.Installation = agent.Installation{
			Root:           primary.AppPath,
			ExecutablePath: primary.ExecutablePath,
			Version:        primary.Version,
			Platform:       runtime.GOOS,
		}
		if primary.ConnectionMode != "" {
			status.Details["connectionMode"] = primary.ConnectionMode
		}
	}
	status.Capabilities = antigravityCapabilityStatuses(metadata, len(targets), supportedTargets, snapshot.ProxyListening)
	return status
}

func antigravityCapabilityStatuses(metadata agent.Metadata, targetCount, supportedTargets int, proxyListening bool) []agent.CapabilityStatus {
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
			if targetCount == 0 {
				status.Reason = "已完成本机 Antigravity 发现，但未找到安装。"
			} else {
				status.Reason = "已完成本机 Antigravity 发现。"
			}
		case agent.CapabilityLocalProxy:
			status.Availability = agent.CapabilityAvailable
			status.Available = proxyListening
			if proxyListening {
				status.Reason = "本地代理已启动。"
			} else {
				status.Reason = "本地代理尚未启动。"
			}
		case agent.CapabilityConfiguration, agent.CapabilityPatchInjection, agent.CapabilityModelCatalog,
			agent.CapabilitySessionRecovery, agent.CapabilityImageIO, agent.CapabilityDiagnostics, agent.CapabilityBackup:
			status.Availability = agent.CapabilityAvailable
			status.Available = supportedTargets > 0
			if supportedTargets > 0 {
				status.Reason = "已验证兼容的 Antigravity 安装。"
			} else {
				status.Reason = "尚未验证兼容的 Antigravity 安装。"
			}
		}
		statuses = append(statuses, status)
	}
	return statuses
}

func targetCounts(details map[string]string) (int, int) {
	if len(details) == 0 {
		return 0, 0
	}
	var supported, patched int
	_, _ = fmt.Sscanf(strings.TrimSpace(details["supportedTargets"]), "%d", &supported)
	_, _ = fmt.Sscanf(strings.TrimSpace(details["patchedTargets"]), "%d", &patched)
	return supported, patched
}
