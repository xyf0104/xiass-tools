package main

import (
	"errors"
	"testing"

	"antigravity-wf-assistant/internal/agent"
	"antigravity-wf-assistant/internal/codexconfig"
	"antigravity-wf-assistant/internal/codexdesktop"
)

func TestCodexAgentSnapshotStatusIsConservative(t *testing.T) {
	metadata := newCodexAgentAdapter().Metadata()
	cases := []struct {
		name              string
		snapshot          codexconfig.ConfigSnapshot
		homeExists        bool
		inspectErr        error
		wantState         agent.State
		wantConfiguration bool
	}{
		{
			name:              "no local home",
			snapshot:          codexconfig.ConfigSnapshot{Location: codexconfig.ConfigLocation{CodexHome: "/home/test/.codex"}, Valid: true},
			wantState:         agent.StateNotInstalled,
			wantConfiguration: true,
		},
		{
			name: "valid unmanaged config",
			snapshot: codexconfig.ConfigSnapshot{
				Location:      codexconfig.ConfigLocation{CodexHome: "/home/test/.codex", ConfigPath: "/home/test/.codex/config.toml", Exists: true},
				Valid:         true,
				ModelProvider: "official",
			},
			homeExists:        true,
			wantState:         agent.StateDetected,
			wantConfiguration: true,
		},
		{
			name: "valid xiass config",
			snapshot: codexconfig.ConfigSnapshot{
				Location:                codexconfig.ConfigLocation{CodexHome: "/home/test/.codex", ConfigPath: "/home/test/.codex/config.toml", Exists: true},
				Valid:                   true,
				ModelProvider:           codexconfig.DefaultProviderID,
				ManagedProviderVerified: true,
			},
			homeExists:        true,
			wantState:         agent.StateReady,
			wantConfiguration: true,
		},
		{
			name: "parseable but incomplete xiass provider is degraded",
			snapshot: codexconfig.ConfigSnapshot{
				Location:             codexconfig.ConfigLocation{CodexHome: "/home/test/.codex", ConfigPath: "/home/test/.codex/config.toml", Exists: true},
				Valid:                true,
				ModelProvider:        codexconfig.DefaultProviderID,
				ManagedProviderIssue: codexconfig.ManagedProviderIssueBearer,
			},
			homeExists:        true,
			wantState:         agent.StateDegraded,
			wantConfiguration: true,
		},
		{
			name: "invalid config",
			snapshot: codexconfig.ConfigSnapshot{
				Location: codexconfig.ConfigLocation{CodexHome: "/home/test/.codex", Exists: true},
				Valid:    false,
			},
			homeExists:        true,
			inspectErr:        errors.New("invalid TOML"),
			wantState:         agent.StateDegraded,
			wantConfiguration: false,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			status := codexAgentSnapshotStatus(metadata, testCase.snapshot, testCase.homeExists, testCase.inspectErr)
			if status.State != testCase.wantState {
				t.Fatalf("state = %q, want %q", status.State, testCase.wantState)
			}
			if got := agentCapabilityAvailable(status, agent.CapabilityConfiguration); got != testCase.wantConfiguration {
				t.Fatalf("configuration available = %v, want %v", got, testCase.wantConfiguration)
			}
			if !agentCapabilityAvailable(status, agent.CapabilityDiscovery) {
				t.Fatal("Codex discovery should report a completed local check")
			}
			if testCase.snapshot.ManagedProviderIssue != codexconfig.ManagedProviderIssueNone && status.Details["managedProviderIssue"] != string(testCase.snapshot.ManagedProviderIssue) {
				t.Fatalf("managed provider issue = %q, want %q", status.Details["managedProviderIssue"], testCase.snapshot.ManagedProviderIssue)
			}
		})
	}
}

func TestCodexDesktopStatusIsSeparateFromLocalConfig(t *testing.T) {
	metadata := newCodexAgentAdapter().Metadata()
	base := codexAgentSnapshotStatus(metadata, codexconfig.ConfigSnapshot{
		Location: codexconfig.ConfigLocation{CodexHome: "/home/test/.codex"},
		Valid:    true,
	}, false, nil)
	decorated := decorateCodexDesktopStatus(base, codexdesktop.Status{
		State: codexdesktop.StateInstalled,
		Installation: codexdesktop.Installation{
			Present: true, Version: "1.8.0", ExecutableVerified: true,
		},
	})
	if decorated.State != agent.StateDetected {
		t.Fatalf("state = %q, want detected when Desktop is installed but config is absent", decorated.State)
	}
	if decorated.Installation.ExecutablePath != "" || decorated.Installation.Version != "1.8.0" || decorated.Installation.Platform != "desktop" {
		t.Fatalf("installation = %+v, config.toml must not be presented as an executable", decorated.Installation)
	}
	if decorated.Details["desktopState"] != string(codexdesktop.StateInstalled) || decorated.Details["configPresent"] != "false" {
		t.Fatalf("details = %+v, want independent desktop/config observations", decorated.Details)
	}
}

func TestCodexDesktopDegradedStateDoesNotClaimHistoryWritesAreSafe(t *testing.T) {
	metadata := newCodexAgentAdapter().Metadata()
	base := codexAgentSnapshotStatus(metadata, codexconfig.ConfigSnapshot{
		Location: codexconfig.ConfigLocation{CodexHome: "/home/test/.codex", Exists: true},
		Valid:    true,
	}, true, nil)
	decorated := decorateCodexDesktopStatus(base, codexdesktop.Status{State: codexdesktop.StateDegraded})
	if decorated.State != agent.StateDegraded || decorated.Details["desktopState"] != string(codexdesktop.StateDegraded) {
		t.Fatalf("degraded desktop status = %+v", decorated)
	}
	if decorated.Installation.ExecutablePath != "" {
		t.Fatal("degraded desktop discovery leaked a configuration path as an executable")
	}
}
