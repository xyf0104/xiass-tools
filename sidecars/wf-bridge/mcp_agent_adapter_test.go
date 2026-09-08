package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"antigravity-wf-assistant/internal/agent"
	"antigravity-wf-assistant/internal/mcpconfig"
)

func TestMCPAgentMetadataScopesCursorProjectAndWindsurfGlobalConfiguration(t *testing.T) {
	cursor := newCursorMCPAgentAdapter().Metadata()
	if !strings.Contains(cursor.Description, "global and explicitly selected project MCP configuration") || !strings.Contains(cursor.Description, "no account, OAuth, or session integration") {
		t.Fatalf("Cursor metadata did not accurately scope supported configuration: %q", cursor.Description)
	}
	if declaration := mcpCapabilityDeclaration(cursor, agent.CapabilityConfiguration); declaration.Availability != agent.CapabilityRequiresBinding || !strings.Contains(declaration.Summary, ".cursor/mcp.json") {
		t.Fatalf("Cursor configuration declaration = %#v", declaration)
	}
	if declaration := mcpCapabilityDeclaration(cursor, agent.CapabilityBackup); declaration.Availability != agent.CapabilityNotImplemented || !strings.Contains(declaration.Summary, "user-selected project") {
		t.Fatalf("Cursor recovery-point declaration = %#v", declaration)
	}

	windsurf := newWindsurfMCPAgentAdapter().Metadata()
	if !strings.Contains(windsurf.Description, "global MCP configuration") || strings.Contains(windsurf.Description, "project MCP") || !strings.Contains(windsurf.Description, "no account, OAuth, or session integration") {
		t.Fatalf("Windsurf metadata did not accurately scope global-only configuration: %q", windsurf.Description)
	}
	if declaration := mcpCapabilityDeclaration(windsurf, agent.CapabilityConfiguration); declaration.Availability != agent.CapabilityRequiresBinding || !strings.Contains(declaration.Summary, "global MCP configuration") || strings.Contains(declaration.Summary, "project") {
		t.Fatalf("Windsurf configuration declaration = %#v", declaration)
	}

	for _, metadata := range []agent.Metadata{cursor, windsurf} {
		for _, capability := range []agent.Capability{agent.CapabilityLocalProxy, agent.CapabilityModelCatalog, agent.CapabilityBackup, agent.CapabilityOAuth, agent.CapabilityUsage, agent.CapabilityTwoFactorAuth} {
			declaration := mcpCapabilityDeclaration(metadata, capability)
			if declaration.Availability != agent.CapabilityNotImplemented {
				t.Fatalf("%s declaration for %s was unexpectedly available: %#v", capability, metadata.ID, declaration)
			}
		}
	}
}

func TestMCPAgentStatusOnlyEnablesVerifiedGlobalConfiguration(t *testing.T) {
	metadata := newCursorMCPAgentAdapter().Metadata()
	cases := []struct {
		name          string
		discovered    agent.Status
		snapshot      mcpconfig.Snapshot
		inspected     bool
		wantState     agent.State
		wantConfigure bool
	}{
		{
			name:          "verified Cursor and empty configuration",
			discovered:    agent.Status{State: agent.StateDetected},
			snapshot:      mcpconfig.Snapshot{Target: mcpconfig.TargetCursor, Valid: true},
			inspected:     true,
			wantState:     agent.StateDetected,
			wantConfigure: true,
		},
		{
			name:          "managed global entry",
			discovered:    agent.Status{State: agent.StateReady},
			snapshot:      mcpconfig.Snapshot{Target: mcpconfig.TargetCursor, Valid: true, ManagedServerConfigured: true},
			inspected:     true,
			wantState:     agent.StateReady,
			wantConfigure: true,
		},
		{
			name:          "sensitive configuration is protected",
			discovered:    agent.Status{State: agent.StateDetected},
			snapshot:      mcpconfig.Snapshot{Target: mcpconfig.TargetCursor, Valid: true, HasSensitiveConfiguration: true},
			inspected:     true,
			wantState:     agent.StateDegraded,
			wantConfigure: false,
		},
		{
			name:          "unvalidated configuration is protected",
			discovered:    agent.Status{State: agent.StateDetected},
			snapshot:      mcpconfig.Snapshot{Target: mcpconfig.TargetCursor},
			inspected:     false,
			wantState:     agent.StateDegraded,
			wantConfigure: false,
		},
		{
			name:          "missing client prevents configuration creation",
			discovered:    agent.Status{State: agent.StateNotInstalled},
			snapshot:      mcpconfig.Snapshot{Target: mcpconfig.TargetCursor, Valid: true},
			inspected:     true,
			wantState:     agent.StateNotInstalled,
			wantConfigure: false,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			status := mcpAgentStatus(metadata, testCase.discovered, testCase.snapshot, testCase.inspected, testCase.inspected)
			if status.State != testCase.wantState {
				t.Fatalf("state = %q, want %q", status.State, testCase.wantState)
			}
			if got := agentCapabilityAvailable(status, agent.CapabilityConfiguration); got != testCase.wantConfigure {
				t.Fatalf("configuration available = %v, want %v", got, testCase.wantConfigure)
			}
			if !agentCapabilityAvailable(status, agent.CapabilityDiscovery) || !agentCapabilityAvailable(status, agent.CapabilityDiagnostics) {
				t.Fatalf("discovery/diagnostics were not available: %#v", status.Capabilities)
			}
			for _, capability := range []agent.Capability{agent.CapabilityOAuth, agent.CapabilityUsage, agent.CapabilityTwoFactorAuth, agent.CapabilityLocalProxy, agent.CapabilityModelCatalog} {
				if agentCapabilityAvailable(status, capability) {
					t.Fatalf("unsupported account or proxy capability was advertised: %s", capability)
				}
			}
			if agentCapabilityAvailable(status, agent.CapabilityBackup) {
				t.Fatalf("generic backup capability was advertised despite only global MCP recovery points being supported")
			}
			if reason := agentCapabilityReason(status, agent.CapabilityBackup); !strings.Contains(reason, "所选项目 MCP") {
				t.Fatalf("Cursor backup capability reason did not accurately scope recovery points: %q", reason)
			}
			if testCase.name == "missing client prevents configuration creation" {
				if reason := agentCapabilityReason(status, agent.CapabilityConfiguration); !strings.Contains(reason, "项目 MCP") {
					t.Fatalf("Cursor undetected capability reason omitted explicit project route: %q", reason)
				}
			}
		})
	}
}

func TestMCPAgentStatusKeepsWindsurfGlobalOnly(t *testing.T) {
	metadata := newWindsurfMCPAgentAdapter().Metadata()
	status := mcpAgentStatus(metadata, agent.Status{State: agent.StateReady}, mcpconfig.Snapshot{Target: mcpconfig.TargetWindsurf, Valid: true}, true, true)
	if reason := agentCapabilityReason(status, agent.CapabilityConfiguration); !strings.Contains(reason, "全局 MCP 配置") || strings.Contains(reason, "项目") {
		t.Fatalf("Windsurf configuration reason incorrectly advertised project configuration: %q", reason)
	}
	if reason := agentCapabilityReason(status, agent.CapabilityBackup); !strings.Contains(reason, "全局 MCP 配置") || strings.Contains(reason, "项目") {
		t.Fatalf("Windsurf recovery point reason incorrectly advertised project configuration: %q", reason)
	}
}

func agentCapabilityReason(status agent.Status, wanted agent.Capability) string {
	for _, capability := range status.Capabilities {
		if capability.Capability == wanted {
			return capability.Reason
		}
	}
	return ""
}

func mcpCapabilityDeclaration(metadata agent.Metadata, wanted agent.Capability) agent.CapabilityDeclaration {
	for _, declaration := range metadata.Capabilities {
		if declaration.Capability == wanted {
			return declaration
		}
	}
	return agent.CapabilityDeclaration{}
}

func TestMCPBridgeRejectsUnknownTargetWithoutEchoingEndpoint(t *testing.T) {
	endpoint := "https://token-in-hostname.example.invalid/mcp?secret=must-not-escape"
	status := (&App{}).ApplyMCPConfiguration(MCPConfigurationInput{Target: "unknown-client", RemoteURL: endpoint})
	if status.OK || status.Message == "" {
		t.Fatalf("unknown target result = %#v", status)
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), endpoint) || strings.Contains(string(encoded), "must-not-escape") {
		t.Fatalf("bridge result echoed endpoint data: %s", encoded)
	}
}

func TestMCPTargetAndErrorMessagesStayGeneric(t *testing.T) {
	if target, ok := parseMCPConfigurationTarget(" CURSOR "); !ok || target != mcpconfig.TargetCursor {
		t.Fatalf("cursor target parse = %q, %v", target, ok)
	}
	if target, ok := parseMCPConfigurationTarget("windsurf"); !ok || target != mcpconfig.TargetWindsurf {
		t.Fatalf("windsurf target parse = %q, %v", target, ok)
	}
	if _, ok := parseMCPConfigurationTarget("cursor-project"); ok {
		t.Fatal("unsupported project target was accepted")
	}
	for _, err := range []error{mcpconfig.ErrInvalidRemote, mcpconfig.ErrUnsafeConfiguration, mcpconfig.ErrInvalidConfiguration, mcpconfig.ErrOperationBusy, errors.New("/private/path/token")} {
		message := mcpConfigurationErrorMessage(err)
		if message == "" || strings.Contains(message, "/private/path/token") {
			t.Fatalf("unsafe error message %q for %v", message, err)
		}
	}
}
