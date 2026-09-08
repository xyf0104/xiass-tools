package agent

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestBuiltinProfilesAreConservativeAndUnbound(t *testing.T) {
	registry := NewDefaultRegistry()
	profiles := registry.List()
	want := []ID{AntigravityID, CodexID, ClaudeCodeID, CursorID, WindsurfID}
	if got := profileIDs(profiles); !reflect.DeepEqual(got, want) {
		t.Fatalf("profiles = %v, want %v", got, want)
	}

	for _, profile := range profiles {
		if err := profile.Validate(); err != nil {
			t.Fatalf("profile %s is invalid: %v", profile.ID, err)
		}
		for _, declaration := range profile.Capabilities {
			if declaration.Availability == CapabilityAvailable {
				t.Fatalf("unbound profile %s claims %s is available", profile.ID, declaration.Capability)
			}
		}
		status, err := registry.Detect(context.Background(), profile.ID)
		if err != nil {
			t.Fatalf("detect %s: %v", profile.ID, err)
		}
		if status.State != StateUnbound {
			t.Errorf("status %s = %s, want %s", profile.ID, status.State, StateUnbound)
		}
		for _, capability := range status.Capabilities {
			if capability.Available {
				t.Errorf("status %s claims %s is live without a bound adapter", profile.ID, capability.Capability)
			}
		}
	}
}

func TestRegistryBindsAndReplacesRuntimeAdapter(t *testing.T) {
	metadata := testMetadata("example")
	registry, err := NewRegistry(metadata)
	if err != nil {
		t.Fatal(err)
	}
	adapter := &fakeAdapter{
		metadata: metadata,
		status: Status{
			State: StateReady,
			Capabilities: []CapabilityStatus{{
				Capability:   CapabilityConfiguration,
				Availability: CapabilityAvailable,
				Available:    true,
			}},
		},
	}
	if err := registry.Register(adapter); err != nil {
		t.Fatalf("register: %v", err)
	}
	if !registry.IsBound(metadata.ID) {
		t.Fatal("adapter was not bound")
	}
	if err := registry.Register(adapter); !errors.Is(err, ErrAdapterAlreadyBound) {
		t.Fatalf("second register error = %v, want ErrAdapterAlreadyBound", err)
	}

	status, err := registry.Detect(context.Background(), metadata.ID)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if status.State != StateReady || status.AgentID != metadata.ID {
		t.Fatalf("unexpected ready status: %#v", status)
	}
	if len(status.Capabilities) != 2 {
		t.Fatalf("capabilities = %#v, want declared entries", status.Capabilities)
	}
	if !status.Capabilities[0].Available || status.Capabilities[0].Capability != CapabilityConfiguration {
		t.Fatalf("configuration capability not preserved: %#v", status.Capabilities)
	}
	if status.Capabilities[1].Available || status.Capabilities[1].Capability != CapabilityOAuth {
		t.Fatalf("unverified OAuth capability unexpectedly live: %#v", status.Capabilities)
	}

	replacement := &fakeAdapter{metadata: metadata, status: Status{State: StateDetected}}
	if err := registry.Replace(replacement); err != nil {
		t.Fatalf("replace: %v", err)
	}
	status, err = registry.Detect(context.Background(), metadata.ID)
	if err != nil {
		t.Fatalf("detect replacement: %v", err)
	}
	if status.State != StateDetected {
		t.Fatalf("replacement state = %s, want %s", status.State, StateDetected)
	}
	if !registry.Unbind(metadata.ID) {
		t.Fatal("unbind returned false")
	}
	if registry.IsBound(metadata.ID) {
		t.Fatal("adapter remains bound after unbind")
	}
}

func TestRegistryAggregatesDetectionFailureWithoutBlockingOtherAgents(t *testing.T) {
	ready := testMetadata("ready-agent")
	broken := testMetadata("broken-agent")
	registry, err := NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(&fakeAdapter{metadata: ready, status: Status{State: StateReady}}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(&fakeAdapter{metadata: broken, status: Status{State: StateDetected}, detectErr: errors.New("fixture failure")}); err != nil {
		t.Fatal(err)
	}

	aggregate := registry.DetectAll(context.Background())
	if aggregate.State != StateDegraded {
		t.Fatalf("aggregate state = %s, want %s", aggregate.State, StateDegraded)
	}
	if len(aggregate.Agents) != 2 {
		t.Fatalf("agents = %d, want 2", len(aggregate.Agents))
	}
	if aggregate.Agents[0].State != StateReady {
		t.Fatalf("first agent state = %s, want ready", aggregate.Agents[0].State)
	}
	if aggregate.Agents[1].State != StateError {
		t.Fatalf("broken agent state = %s, want error", aggregate.Agents[1].State)
	}
	if len(aggregate.Diagnostics) != 1 || aggregate.Diagnostics[0].Code != "agent.detect-failed" {
		t.Fatalf("diagnostics = %#v", aggregate.Diagnostics)
	}
}

func TestAggregateStateForUnboundProfiles(t *testing.T) {
	aggregate := NewDefaultRegistry().DetectAll(context.Background())
	if aggregate.State != StateUnbound {
		t.Fatalf("aggregate state = %s, want %s", aggregate.State, StateUnbound)
	}
}

func TestRegistryDelegatesOptionalProviders(t *testing.T) {
	metadata := testMetadata("provider-agent")
	registry, err := NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	adapter := &fakeAdapter{
		metadata: metadata,
		status:   Status{State: StateReady},
		diagnostics: []Diagnostic{{
			Code:     "provider.ready",
			Severity: SeverityInfo,
			Summary:  "Provider is ready",
		}},
		backup: Backup{ID: "backup-1", Files: []BackupFile{{Source: "config.toml", Destination: "snapshot/config.toml"}}},
		operation: OperationResult{
			Message: "applied",
		},
	}
	if err := registry.Bind(adapter); err != nil {
		t.Fatal(err)
	}

	diagnostics, err := registry.Diagnose(context.Background(), metadata.ID)
	if err != nil {
		t.Fatalf("diagnose: %v", err)
	}
	if len(diagnostics) != 1 || diagnostics[0].AgentID != metadata.ID || diagnostics[0].CreatedAt.IsZero() {
		t.Fatalf("diagnostics = %#v", diagnostics)
	}

	backup, err := registry.Backup(context.Background(), metadata.ID, BackupRequest{IncludeConfig: true})
	if err != nil {
		t.Fatalf("backup: %v", err)
	}
	if backup.AgentID != metadata.ID || backup.CreatedAt.IsZero() || len(backup.Files) != 1 {
		t.Fatalf("backup = %#v", backup)
	}

	result, err := registry.Perform(context.Background(), metadata.ID, OperationRequest{Operation: OperationApply})
	if err != nil {
		t.Fatalf("perform: %v", err)
	}
	if result.AgentID != metadata.ID || result.Operation != OperationApply || result.Message != "applied" {
		t.Fatalf("operation result = %#v", result)
	}

	registry.Unbind(metadata.ID)
	if _, err := registry.Backup(context.Background(), metadata.ID, BackupRequest{}); !errors.Is(err, ErrAdapterUnbound) {
		t.Fatalf("unbound backup error = %v, want ErrAdapterUnbound", err)
	}
}

func TestMetadataReturnsDetachedCopy(t *testing.T) {
	registry := NewDefaultRegistry()
	metadata, ok := registry.Metadata(AntigravityID)
	if !ok {
		t.Fatal("missing Antigravity metadata")
	}
	metadata.DisplayName = "mutated"
	metadata.Capabilities[0].Summary = "mutated"
	stored, ok := registry.Metadata(AntigravityID)
	if !ok {
		t.Fatal("missing Antigravity metadata after mutation")
	}
	if stored.DisplayName == "mutated" || stored.Capabilities[0].Summary == "mutated" {
		t.Fatalf("registry metadata was mutated: %#v", stored)
	}
}

func TestBuiltinMetadataCannotBePromotedByAnAdapterDeclaration(t *testing.T) {
	registry := NewDefaultRegistry()
	metadata, ok := registry.Metadata(CodexID)
	if !ok {
		t.Fatal("missing Codex metadata")
	}
	for index := range metadata.Capabilities {
		if metadata.Capabilities[index].Capability == CapabilityOAuth {
			metadata.Capabilities[index].Availability = CapabilityAvailable
		}
	}
	if err := registry.Register(&fakeAdapter{metadata: metadata, status: Status{State: StateReady}}); err != nil {
		t.Fatalf("bind altered metadata: %v", err)
	}
	stored, ok := registry.Metadata(CodexID)
	if !ok {
		t.Fatal("missing Codex metadata after bind")
	}
	for _, capability := range stored.Capabilities {
		if capability.Capability == CapabilityOAuth && capability.Availability != CapabilityNotImplemented {
			t.Fatalf("OAuth capability was promoted to %s", capability.Availability)
		}
	}
}

func TestRegistryRejectsTypedNilAdapter(t *testing.T) {
	registry, err := NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	var adapter *fakeAdapter
	if err := registry.Register(adapter); !errors.Is(err, ErrNilAdapter) {
		t.Fatalf("typed nil adapter error = %v, want ErrNilAdapter", err)
	}
}

func testMetadata(id ID) Metadata {
	return Metadata{
		ID:          id,
		DisplayName: string(id),
		Category:    CategoryTerminal,
		Capabilities: []CapabilityDeclaration{
			{Capability: CapabilityConfiguration, Availability: CapabilityRequiresBinding},
			{Capability: CapabilityOAuth, Availability: CapabilityNotImplemented},
		},
	}
}

func profileIDs(profiles []Metadata) []ID {
	ids := make([]ID, 0, len(profiles))
	for _, profile := range profiles {
		ids = append(ids, profile.ID)
	}
	return ids
}

type fakeAdapter struct {
	metadata    Metadata
	status      Status
	detectErr   error
	diagnostics []Diagnostic
	diagnoseErr error
	backup      Backup
	backupErr   error
	operation   OperationResult
	performErr  error
}

func (adapter *fakeAdapter) Metadata() Metadata {
	return adapter.metadata
}

func (adapter *fakeAdapter) Detect(context.Context) (Status, error) {
	return adapter.status, adapter.detectErr
}

func (adapter *fakeAdapter) Diagnose(context.Context) ([]Diagnostic, error) {
	return append([]Diagnostic(nil), adapter.diagnostics...), adapter.diagnoseErr
}

func (adapter *fakeAdapter) Backup(context.Context, BackupRequest) (Backup, error) {
	return adapter.backup, adapter.backupErr
}

func (adapter *fakeAdapter) Perform(_ context.Context, request OperationRequest) (OperationResult, error) {
	result := adapter.operation
	if result.Operation == "" {
		result.Operation = request.Operation
	}
	return result, adapter.performErr
}

func TestStatusTimestampsAreUTC(t *testing.T) {
	metadata := testMetadata("time-agent")
	registry, err := NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 8, 30, 9, 0, 0, 0, time.FixedZone("fixture", 8*60*60))
	registry.now = func() time.Time { return clock }
	if err := registry.Register(&fakeAdapter{metadata: metadata, status: Status{State: StateReady}}); err != nil {
		t.Fatal(err)
	}
	status, err := registry.Detect(context.Background(), metadata.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.UpdatedAt.Location() != time.UTC || status.UpdatedAt.Hour() != 1 {
		t.Fatalf("timestamp = %v, want UTC conversion", status.UpdatedAt)
	}
}
