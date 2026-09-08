package main

import (
	"errors"
	"strings"
	"testing"

	"antigravity-wf-assistant/internal/agent"
	"antigravity-wf-assistant/internal/claudeconfig"
)

func TestClaudeCodeProductionAdapterUsesPlatformAwareCLIDiscovery(t *testing.T) {
	adapter := newClaudeCodeAgentAdapter()
	called := false
	adapter.discoverCLI = func() (string, error) {
		called = true
		return "/Users/test/.local/bin/claude", nil
	}
	path, err := adapter.discoverCLIPath()
	if err != nil || path != "/Users/test/.local/bin/claude" || !called {
		t.Fatalf("discover CLI = %q, %v, called=%v", path, err, called)
	}
}

func TestClaudeCodeAgentCapabilityStatusIsConservative(t *testing.T) {
	metadata := newClaudeCodeAgentAdapter().Metadata()
	cases := []struct {
		name             string
		snapshot         claudeconfig.Snapshot
		inspectErr       error
		cliFound         bool
		backupAvailable  bool
		wantState        agent.State
		wantConfig       bool
		wantModelCatalog bool
		wantLocalProxy   bool
		wantBackup       bool
	}{
		{
			name:             "absent settings remain explicitly configurable",
			snapshot:         claudeconfig.Snapshot{Location: claudeconfig.ConfigLocation{ConfigDir: "/home/test/.claude", SettingsPath: "/home/test/.claude/settings.json"}, Valid: true},
			backupAvailable:  true,
			wantState:        agent.StateNotInstalled,
			wantConfig:       true,
			wantModelCatalog: true,
			wantBackup:       true,
		},
		{
			name:             "valid existing unmanaged settings",
			snapshot:         claudeconfig.Snapshot{Location: claudeconfig.ConfigLocation{ConfigDir: "/home/test/.claude", SettingsPath: "/home/test/.claude/settings.json", Exists: true}, Valid: true},
			cliFound:         true,
			backupAvailable:  true,
			wantState:        agent.StateDetected,
			wantConfig:       true,
			wantModelCatalog: true,
			wantBackup:       true,
		},
		{
			name:             "managed loopback address is not an endpoint health check",
			snapshot:         claudeconfig.Snapshot{Location: claudeconfig.ConfigLocation{ConfigDir: "/home/test/.claude", SettingsPath: "/home/test/.claude/settings.json", Exists: true}, Valid: true, Managed: true, BaseURL: "http://127.0.0.1:50999/v1"},
			cliFound:         true,
			backupAvailable:  true,
			wantState:        agent.StateReady,
			wantConfig:       true,
			wantModelCatalog: true,
			wantBackup:       true,
		},
		{
			name:             "managed remote endpoint does not claim local proxy",
			snapshot:         claudeconfig.Snapshot{Location: claudeconfig.ConfigLocation{ConfigDir: "/home/test/.claude", SettingsPath: "/home/test/.claude/settings.json", Exists: true}, Valid: true, Managed: true, BaseURL: "https://api.example.test/v1"},
			cliFound:         true,
			backupAvailable:  true,
			wantState:        agent.StateReady,
			wantConfig:       true,
			wantModelCatalog: true,
			wantBackup:       true,
		},
		{
			name:             "backup-unavailable settings stay read-only",
			snapshot:         claudeconfig.Snapshot{Location: claudeconfig.ConfigLocation{ConfigDir: "/home/test/.claude", SettingsPath: "/home/test/.claude/settings.json", Exists: true}, Valid: true, Managed: true, BaseURL: "https://api.example.test/v1"},
			cliFound:         true,
			wantState:        agent.StateDegraded,
			wantModelCatalog: true,
		},
		{
			name:             "managed settings without CLI are not ready",
			snapshot:         claudeconfig.Snapshot{Location: claudeconfig.ConfigLocation{ConfigDir: "/home/test/.claude", SettingsPath: "/home/test/.claude/settings.json", Exists: true}, Valid: true, Managed: true, BaseURL: "https://api.example.test/v1"},
			backupAvailable:  true,
			wantState:        agent.StateNotInstalled,
			wantConfig:       true,
			wantModelCatalog: true,
			wantBackup:       true,
		},
		{
			name:       "invalid settings disable mutations",
			snapshot:   claudeconfig.Snapshot{Location: claudeconfig.ConfigLocation{ConfigDir: "/home/test/.claude", SettingsPath: "/home/test/.claude/settings.json", Exists: true}, Valid: false},
			inspectErr: errors.New("invalid JSON"),
			wantState:  agent.StateDegraded,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			status := claudeCodeAgentSnapshotStatus(metadata, testCase.snapshot, testCase.inspectErr, "/usr/local/bin/claude", testCase.cliFound, false, testCase.backupAvailable)
			if status.State != testCase.wantState {
				t.Fatalf("state = %q, want %q", status.State, testCase.wantState)
			}
			for capability, want := range map[agent.Capability]bool{
				agent.CapabilityDiscovery:     true,
				agent.CapabilityConfiguration: testCase.wantConfig,
				agent.CapabilityModelCatalog:  testCase.wantModelCatalog,
				agent.CapabilityLocalProxy:    testCase.wantLocalProxy,
				agent.CapabilityBackup:        testCase.wantBackup,
				agent.CapabilityDiagnostics:   true,
			} {
				if got := claudeCodeCapabilityAvailable(status, capability); got != want {
					t.Errorf("%s available = %v, want %v", capability, got, want)
				}
			}
			if claudeCodeCapabilityAvailable(status, agent.CapabilityOAuth) || claudeCodeCapabilityAvailable(status, agent.CapabilityUsage) || claudeCodeCapabilityAvailable(status, agent.CapabilityTwoFactorAuth) {
				t.Fatalf("account-related capabilities were advertised: %#v", status.Capabilities)
			}
		})
	}
}

func TestClaudeCodeLoopbackAddressIsNotAdvertisedAsHealthyProxy(t *testing.T) {
	metadata := newClaudeCodeAgentAdapter().Metadata()
	status := claudeCodeAgentSnapshotStatus(
		metadata,
		claudeconfig.Snapshot{Location: claudeconfig.ConfigLocation{Exists: true}, Valid: true, Managed: true, BaseURL: "http://127.0.0.1:50999/v1"},
		nil,
		"/usr/local/bin/claude",
		true,
		false,
		true,
	)
	if claudeCodeCapabilityAvailable(status, agent.CapabilityLocalProxy) {
		t.Fatal("an untested loopback URL was advertised as a healthy local proxy")
	}
	for _, capability := range status.Capabilities {
		if capability.Capability == agent.CapabilityLocalProxy && (!strings.Contains(capability.Reason, "回环地址") || !strings.Contains(capability.Reason, "尚未测试")) {
			t.Fatalf("local proxy reason = %q", capability.Reason)
		}
	}
}

func TestClaudeCodeLoopbackDetectionRejectsRemoteAndUnmanagedEndpoints(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		snapshot claudeconfig.Snapshot
		want     bool
	}{
		{name: "localhost", snapshot: claudeconfig.Snapshot{Valid: true, Managed: true, BaseURL: "http://localhost:50999/v1"}, want: true},
		{name: "ipv6 loopback", snapshot: claudeconfig.Snapshot{Valid: true, Managed: true, BaseURL: "http://[::1]:50999/v1"}, want: true},
		{name: "remote", snapshot: claudeconfig.Snapshot{Valid: true, Managed: true, BaseURL: "https://api.example.test/v1"}, want: false},
		{name: "unmanaged", snapshot: claudeconfig.Snapshot{Valid: true, Managed: false, BaseURL: "http://127.0.0.1:50999/v1"}, want: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := claudeCodeLoopbackConfigured(testCase.snapshot, true); got != testCase.want {
				t.Fatalf("loopback configured = %v, want %v", got, testCase.want)
			}
		})
	}
}

func claudeCodeCapabilityAvailable(status agent.Status, capability agent.Capability) bool {
	for _, item := range status.Capabilities {
		if item.Capability == capability {
			return item.Available && item.Availability == agent.CapabilityAvailable
		}
	}
	return false
}
