package main

import (
	"testing"

	"antigravity-wf-assistant/internal/agent"
	"antigravity-wf-assistant/internal/patcher"
)

func TestAntigravityAgentStatusReflectsVerifiedSnapshot(t *testing.T) {
	metadata := newAntigravityAgentAdapter().Metadata()
	cases := []struct {
		name              string
		snapshot          patcher.Status
		wantState         agent.State
		wantConfiguration bool
		wantProxy         bool
	}{
		{
			name:              "no installation",
			wantState:         agent.StateNotInstalled,
			wantConfiguration: false,
			wantProxy:         false,
		},
		{
			name: "supported but not connected",
			snapshot: patcher.Status{Targets: []patcher.TargetStatus{{
				Name: "Antigravity IDE", Kind: "ide", Version: "2.8.1", AppPath: "/Applications/Antigravity.app",
				ExecutablePath: "/Applications/Antigravity.app/Contents/MacOS/Antigravity", Supported: true,
			}}},
			wantState:         agent.StateDetected,
			wantConfiguration: true,
			wantProxy:         false,
		},
		{
			name: "all supported targets connected",
			snapshot: patcher.Status{ProxyListening: true, Targets: []patcher.TargetStatus{{
				Name: "Antigravity 2.0", Kind: "agent", Version: "2.8.1", Supported: true, Patched: true,
			}}},
			wantState:         agent.StateReady,
			wantConfiguration: true,
			wantProxy:         true,
		},
		{
			name: "detected structure is unsupported",
			snapshot: patcher.Status{Targets: []patcher.TargetStatus{{
				Name: "Antigravity IDE", Kind: "ide", Reason: "unsupported layout", Supported: false,
			}}},
			wantState:         agent.StateDegraded,
			wantConfiguration: false,
			wantProxy:         false,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			status := antigravityAgentStatus(metadata, testCase.snapshot)
			if status.State != testCase.wantState {
				t.Fatalf("state = %q, want %q", status.State, testCase.wantState)
			}
			if got := agentCapabilityAvailable(status, agent.CapabilityConfiguration); got != testCase.wantConfiguration {
				t.Fatalf("configuration available = %v, want %v", got, testCase.wantConfiguration)
			}
			if got := agentCapabilityAvailable(status, agent.CapabilityLocalProxy); got != testCase.wantProxy {
				t.Fatalf("proxy available = %v, want %v", got, testCase.wantProxy)
			}
			if !agentCapabilityAvailable(status, agent.CapabilityDiscovery) {
				t.Fatal("discovery should always describe a completed local check")
			}
		})
	}
}

func TestAntigravityAgentStatusPrefersSupportedInstallation(t *testing.T) {
	status := antigravityAgentStatus(newAntigravityAgentAdapter().Metadata(), patcher.Status{Targets: []patcher.TargetStatus{
		{Name: "Unsupported", AppPath: "/unsupported", Supported: false},
		{Name: "Supported", AppPath: "/supported", ExecutablePath: "/supported/run", Version: "2.8.1", Supported: true},
	}})
	if status.Installation.Root != "/supported" {
		t.Fatalf("installation root = %q, want supported target", status.Installation.Root)
	}
	if status.Details["supportedTargets"] != "1" || status.Details["unsupportedTargets"] != "1" {
		t.Fatalf("target details = %#v", status.Details)
	}
}

func TestTargetCountsToleratesMissingOrMalformedStatusDetails(t *testing.T) {
	for _, details := range []map[string]string{nil, {}, {"supportedTargets": "not-a-number", "patchedTargets": "3"}} {
		supported, patched := targetCounts(details)
		if details == nil || len(details) == 0 {
			if supported != 0 || patched != 0 {
				t.Fatalf("empty details = %d, %d", supported, patched)
			}
		}
	}
}

func agentCapabilityAvailable(status agent.Status, capability agent.Capability) bool {
	for _, item := range status.Capabilities {
		if item.Capability == capability {
			return item.Available
		}
	}
	return false
}
