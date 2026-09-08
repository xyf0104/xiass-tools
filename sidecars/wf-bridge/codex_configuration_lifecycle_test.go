package main

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"antigravity-wf-assistant/internal/codexconfig"
	"antigravity-wf-assistant/internal/codexdesktop"
)

// These regression tests exercise failure paths only. They never read a real
// Codex config or operate on a real desktop application.
func TestCodexLifecycleRunningDesktopRequiresConfirmationBeforeAnyMutation(t *testing.T) {
	desktop := newCodexLifecycleFakeDesktop(true)
	operations := newCodexLifecycleTestOperations(desktop)
	var applyCalls, repairCalls int
	operations.apply = func(codexconfig.ApplyConfig) (codexconfig.ApplyResult, error) {
		applyCalls++
		return codexconfig.ApplyResult{BackupID: "config-backup"}, nil
	}
	operations.repairHistory = func() (codexconfig.HistoryRepairResult, error) {
		repairCalls++
		return codexconfig.HistoryRepairResult{Skipped: true}, nil
	}
	input := codexLifecycleTestInput("next")
	input.Confirmation = ""

	result := runCodexConfigurationWithLifecycle(context.Background(), input, operations)

	if result.OK {
		t.Fatalf("unconfirmed transaction unexpectedly succeeded: %#v", result)
	}
	if desktop.stopCalls != 0 || applyCalls != 0 || repairCalls != 0 || desktop.launchCalls != 0 {
		t.Fatalf("unconfirmed transaction performed an operation: stop=%d apply=%d repair=%d launch=%d", desktop.stopCalls, applyCalls, repairCalls, desktop.launchCalls)
	}
	if !desktop.status.Running {
		t.Fatal("unconfirmed transaction changed the desktop running state")
	}
}

func TestCodexLifecycleProcessListWarningPreventsMutationEvenWithoutDiscovery(t *testing.T) {
	desktop := newCodexLifecycleFakeDesktop(false)
	desktop.status.Discovered = false
	desktop.status.Warnings = []codexdesktop.Warning{codexdesktop.WarningProcessListUnavailable}
	operations := newCodexLifecycleTestOperations(desktop)
	var applyCalls int
	operations.apply = func(codexconfig.ApplyConfig) (codexconfig.ApplyResult, error) {
		applyCalls++
		return codexconfig.ApplyResult{}, nil
	}

	result := runCodexConfigurationWithLifecycle(context.Background(), codexLifecycleTestInput("previous"), operations)

	if result.OK || applyCalls != 0 || desktop.stopCalls != 0 || desktop.launchCalls != 0 {
		t.Fatalf("process-list warning did not fail closed: result=%#v apply=%d stop=%d launch=%d", result, applyCalls, desktop.stopCalls, desktop.launchCalls)
	}
}

func TestCodexLifecycleSameProviderSkipsHistoryRepair(t *testing.T) {
	desktop := newCodexLifecycleFakeDesktop(false)
	operations := newCodexLifecycleTestOperations(desktop)
	var applyCalls, repairCalls int
	operations.apply = func(codexconfig.ApplyConfig) (codexconfig.ApplyResult, error) {
		applyCalls++
		return codexconfig.ApplyResult{BackupID: "config-backup"}, nil
	}
	operations.repairHistory = func() (codexconfig.HistoryRepairResult, error) {
		repairCalls++
		return codexconfig.HistoryRepairResult{}, nil
	}

	result := runCodexConfigurationWithLifecycle(context.Background(), codexLifecycleTestInput("previous"), operations)

	if !result.OK || !result.Applied {
		t.Fatalf("same-provider transaction did not apply cleanly: %#v", result)
	}
	if applyCalls != 1 || repairCalls != 0 {
		t.Fatalf("same-provider calls = apply:%d repair:%d, want apply:1 repair:0", applyCalls, repairCalls)
	}
	if result.HistoryRepairAttempted || result.HistoryRepairSkipped {
		t.Fatalf("same-provider transaction reported history handling: %#v", result)
	}
	if desktop.stopCalls != 0 || desktop.launchCalls != 0 {
		t.Fatalf("stopped desktop should not receive lifecycle operations: stop=%d launch=%d", desktop.stopCalls, desktop.launchCalls)
	}
}

func TestCodexLifecycleProviderChangeRepairsBeforeRelaunch(t *testing.T) {
	desktop := newCodexLifecycleFakeDesktop(true)
	operations := newCodexLifecycleTestOperations(desktop)
	operations.apply = func(codexconfig.ApplyConfig) (codexconfig.ApplyResult, error) {
		desktop.events = append(desktop.events, "apply")
		return codexconfig.ApplyResult{BackupID: "config-backup"}, nil
	}
	operations.repairHistory = func() (codexconfig.HistoryRepairResult, error) {
		desktop.events = append(desktop.events, "repair")
		return codexconfig.HistoryRepairResult{BackupID: "history-backup"}, nil
	}

	result := runCodexConfigurationWithLifecycle(context.Background(), codexLifecycleTestInput("next"), operations)

	if !result.OK || !result.Applied || !result.HistoryRepairAttempted || !result.HistoryRepaired || !result.Relaunched {
		t.Fatalf("provider-change transaction did not fully complete: %#v", result)
	}
	wantEvents := []string{"stop", "apply", "repair", "launch"}
	if !reflect.DeepEqual(desktop.events, wantEvents) {
		t.Fatalf("operation order = %#v, want %#v", desktop.events, wantEvents)
	}
	if !desktop.status.Running {
		t.Fatal("successful transaction did not restore the prior running desktop")
	}
}

func TestCodexLifecycleNoDesktopStillAllowsConfigurationWithoutLaunch(t *testing.T) {
	desktop := newCodexLifecycleFakeDesktop(false)
	desktop.status.Discovered = false
	operations := newCodexLifecycleTestOperations(desktop)
	var applyCalls int
	operations.apply = func(codexconfig.ApplyConfig) (codexconfig.ApplyResult, error) {
		applyCalls++
		return codexconfig.ApplyResult{BackupID: "config-backup"}, nil
	}

	result := runCodexConfigurationWithLifecycle(context.Background(), codexLifecycleTestInput("previous"), operations)

	if !result.OK || !result.Applied || applyCalls != 1 {
		t.Fatalf("no-desktop configuration did not complete: result=%#v apply=%d", result, applyCalls)
	}
	if desktop.stopCalls != 0 || desktop.launchCalls != 0 {
		t.Fatalf("no-desktop configuration attempted lifecycle control: stop=%d launch=%d", desktop.stopCalls, desktop.launchCalls)
	}
}

func TestCodexLifecycleNoDesktopLaunchRequestRollsBackVerifiedConfiguration(t *testing.T) {
	desktop := newCodexLifecycleFakeDesktop(false)
	desktop.status.Discovered = false
	operations := newCodexLifecycleTestOperations(desktop)
	var restoreCalls int
	operations.restoreConfig = func(string) (codexconfig.RestoreResult, error) {
		restoreCalls++
		return codexconfig.RestoreResult{}, nil
	}
	input := codexLifecycleTestInput("previous")
	input.LaunchAfter = true

	result := runCodexConfigurationWithLifecycle(context.Background(), input, operations)

	if result.OK || !result.Applied || !result.RolledBack {
		t.Fatalf("no-desktop launch request did not fail transactionally: %#v", result)
	}
	if restoreCalls != 1 || desktop.launchCalls != 0 {
		t.Fatalf("no-desktop launch request calls = restore:%d launch:%d, want restore:1 launch:0", restoreCalls, desktop.launchCalls)
	}
}

func TestCodexLifecycleSupportsAnAbsentConfig(t *testing.T) {
	desktop := newCodexLifecycleFakeDesktop(false)
	operations := newCodexLifecycleTestOperations(desktop)
	absent := codexLifecycleTestSnapshot()
	absent.Location.Exists = false
	absent.SHA256 = ""
	absent.ModelProvider = ""
	absent.Model = ""
	absent.ReviewModel = ""
	absent.WebSearch = ""
	operations.inspect = func() (codexconfig.ConfigSnapshot, error) { return absent, nil }
	var applyCalls int
	operations.apply = func(codexconfig.ApplyConfig) (codexconfig.ApplyResult, error) {
		applyCalls++
		return codexconfig.ApplyResult{BackupID: "config-backup"}, nil
	}
	input := codexLifecycleTestInput("first-provider")
	input.RepairHistoryOnProviderChange = false

	result := runCodexConfigurationWithLifecycle(context.Background(), input, operations)

	if !result.OK || !result.Applied || applyCalls != 1 {
		t.Fatalf("absent-config transaction was rejected: result=%#v apply=%d", result, applyCalls)
	}
	if desktop.stopCalls != 0 || desktop.launchCalls != 0 {
		t.Fatalf("absent-config transaction changed desktop lifecycle: stop=%d launch=%d", desktop.stopCalls, desktop.launchCalls)
	}
}

func TestCodexLifecycleLaunchFailureRestoresPriorDesktopOnlyAfterVerifiedRollback(t *testing.T) {
	desktop := newCodexLifecycleFakeDesktop(true)
	desktop.launchFailures = 1
	operations := newCodexLifecycleTestOperations(desktop)
	operations.apply = func(codexconfig.ApplyConfig) (codexconfig.ApplyResult, error) {
		desktop.events = append(desktop.events, "apply")
		return codexconfig.ApplyResult{BackupID: "config-backup"}, nil
	}

	result := runCodexConfigurationWithLifecycle(context.Background(), codexLifecycleTestInput("previous"), operations)

	if result.OK || !result.RolledBack || !result.Relaunched || !desktop.status.Running {
		t.Fatalf("verified rollback did not restore the prior desktop state: %#v", result)
	}
	wantEvents := []string{"stop", "apply", "launch", "launch"}
	if !reflect.DeepEqual(desktop.events, wantEvents) {
		t.Fatalf("operation order = %#v, want %#v", desktop.events, wantEvents)
	}
}

func TestCodexLifecycleResponseDoesNotExposeSecretsOrLocalPaths(t *testing.T) {
	desktop := newCodexLifecycleFakeDesktop(false)
	operations := newCodexLifecycleTestOperations(desktop)
	operations.refresh = func() CodexConfigurationStatus {
		return CodexConfigurationStatus{
			OK: true,
			Snapshot: codexconfig.ConfigSnapshot{Location: codexconfig.ConfigLocation{
				CodexHome:  "/private/test-user/.codex",
				ConfigPath: "/private/test-user/.codex/config.toml",
				Exists:     true,
			}},
		}
	}
	input := codexLifecycleTestInput("previous")
	input.Config.APIKey = "super-secret-api-key"
	input.Confirmation = "CONFIRM_CODEX_DESKTOP_LIFECYCLE"

	result := runCodexConfigurationWithLifecycle(context.Background(), input, operations)
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal lifecycle status: %v", err)
	}
	for _, forbidden := range []string{"super-secret-api-key", "/private/test-user", "CONFIRM_CODEX_DESKTOP_LIFECYCLE", "api_key", "processId", "commandLine"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("lifecycle response leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestCodexLifecycleApplyFailureDoesNotRelaunchWhenRollbackIsUnverified(t *testing.T) {
	desktop := newCodexLifecycleFakeDesktop(true)
	operations := newCodexLifecycleTestOperations(desktop)
	operations.apply = func(codexconfig.ApplyConfig) (codexconfig.ApplyResult, error) {
		return codexconfig.ApplyResult{BackupID: "config-backup"}, errors.New("simulated apply failure")
	}
	operations.restoreConfig = func(string) (codexconfig.RestoreResult, error) {
		return codexconfig.RestoreResult{}, errors.New("simulated config restore failure")
	}

	result := runCodexConfigurationWithLifecycle(context.Background(), codexLifecycleTestInput("previous"), operations)

	assertCodexLifecycleWasLeftStopped(t, result, desktop, "apply failure")
	assertCodexLifecycleResponseDoesNotContain(t, result, "test-only-key", "simulated apply failure", codexdesktop.LifecycleConfirmation)
	if desktop.stopCalls != 1 {
		t.Fatalf("stop calls = %d, want 1", desktop.stopCalls)
	}
}

func TestCodexLifecycleHistoryFailureDoesNotRelaunchWhenRollbackIsUnverified(t *testing.T) {
	desktop := newCodexLifecycleFakeDesktop(true)
	operations := newCodexLifecycleTestOperations(desktop)
	operations.apply = func(codexconfig.ApplyConfig) (codexconfig.ApplyResult, error) {
		return codexconfig.ApplyResult{BackupID: "config-backup"}, nil
	}
	operations.repairHistory = func() (codexconfig.HistoryRepairResult, error) {
		return codexconfig.HistoryRepairResult{BackupID: "history-backup"}, errors.New("simulated history failure")
	}
	operations.restoreHistory = func(string) error {
		return errors.New("simulated history restore failure")
	}

	result := runCodexConfigurationWithLifecycle(context.Background(), codexLifecycleTestInput("next"), operations)

	assertCodexLifecycleWasLeftStopped(t, result, desktop, "history repair failure")
	if !result.HistoryRepairAttempted {
		t.Fatal("history repair was not attempted")
	}
}

func TestCodexLifecycleLaunchFailureDoesNotRelaunchWhenRollbackIsUnverified(t *testing.T) {
	desktop := newCodexLifecycleFakeDesktop(true)
	desktop.launchErr = errors.New("simulated launch failure")
	operations := newCodexLifecycleTestOperations(desktop)
	operations.apply = func(codexconfig.ApplyConfig) (codexconfig.ApplyResult, error) {
		return codexconfig.ApplyResult{BackupID: "config-backup"}, nil
	}
	operations.restoreConfig = func(string) (codexconfig.RestoreResult, error) {
		return codexconfig.RestoreResult{}, errors.New("simulated config restore failure")
	}

	result := runCodexConfigurationWithLifecycle(context.Background(), codexLifecycleTestInput("previous"), operations)

	assertCodexLifecycleWasLeftStopped(t, result, desktop, "launch failure")
	if desktop.launchCalls != 1 {
		t.Fatalf("launch calls = %d, want exactly the failed initial launch and no automatic re-launch", desktop.launchCalls)
	}
}

func TestCodexLifecycleLaunchFailureDoesNotRelaunchWhenWorkspaceStateCannotBeRestored(t *testing.T) {
	desktop := newCodexLifecycleFakeDesktop(true)
	desktop.launchErr = errors.New("simulated launch failure")
	operations := newCodexLifecycleTestOperations(desktop)
	operations.apply = func(codexconfig.ApplyConfig) (codexconfig.ApplyResult, error) {
		return codexconfig.ApplyResult{BackupID: "config-backup"}, nil
	}
	operations.repairHistory = func() (codexconfig.HistoryRepairResult, error) {
		return codexconfig.HistoryRepairResult{
			BackupID: "history-backup",
			WorkspaceState: &codexconfig.WorkspaceStateRepairResult{
				Updated:  true,
				BackupID: "workspace-state-backup",
			},
		}, nil
	}
	var restoreHistoryCalls int
	operations.restoreHistory = func(string) error {
		restoreHistoryCalls++
		return nil
	}

	result := runCodexConfigurationWithLifecycle(context.Background(), codexLifecycleTestInput("next"), operations)

	if result.RolledBack || result.Relaunched || result.OK {
		t.Fatalf("workspace-state change was incorrectly treated as fully rolled back: %#v", result)
	}
	if restoreHistoryCalls != 1 || desktop.launchCalls != 1 || desktop.status.Running {
		t.Fatalf("workspace-state failure calls = restoreHistory:%d launch:%d running:%t, want 1/1/false", restoreHistoryCalls, desktop.launchCalls, desktop.status.Running)
	}
	if !strings.Contains(result.Message, "保持关闭") {
		t.Fatalf("workspace-state failure did not report closed recovery state: %q", result.Message)
	}
}

func assertCodexLifecycleWasLeftStopped(t *testing.T, result CodexConfigurationLifecycleStatus, desktop *codexLifecycleFakeDesktop, scenario string) {
	t.Helper()
	if result.OK {
		t.Fatalf("%s result unexpectedly succeeded: %#v", scenario, result)
	}
	if result.RolledBack {
		t.Fatalf("%s rollback was reported as verified: %#v", scenario, result)
	}
	if result.Relaunched {
		t.Fatalf("%s re-launched Codex despite unverified rollback: %#v", scenario, result)
	}
	if desktop.launchCalls != 0 && scenario != "launch failure" {
		t.Fatalf("%s launch calls = %d, want 0", scenario, desktop.launchCalls)
	}
	if result.Desktop.Running || desktop.status.Running {
		t.Fatalf("%s left Codex running: result=%#v desktop=%#v", scenario, result.Desktop, desktop.status)
	}
	if !strings.Contains(result.Message, "保持关闭") {
		t.Fatalf("%s message does not tell the user that Codex remains closed: %q", scenario, result.Message)
	}
}

func assertCodexLifecycleResponseDoesNotContain(t *testing.T, result CodexConfigurationLifecycleStatus, forbidden ...string) {
	t.Helper()
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal lifecycle status: %v", err)
	}
	for _, value := range forbidden {
		if strings.Contains(string(encoded), value) {
			t.Fatalf("lifecycle response leaked %q: %s", value, encoded)
		}
	}
}

type codexLifecycleFakeDesktop struct {
	status         codexdesktop.ControlStatus
	stopCalls      int
	launchCalls    int
	launchFailures int
	stopErr        error
	launchErr      error
	events         []string
}

func newCodexLifecycleFakeDesktop(running bool) *codexLifecycleFakeDesktop {
	return &codexLifecycleFakeDesktop{status: codexdesktop.ControlStatus{
		Discovered: true,
		Running:    running,
		CanLaunch:  true,
		CanStop:    running,
		CanRestart: running,
	}}
}

func (fake *codexLifecycleFakeDesktop) Status(context.Context) codexdesktop.ControlStatus {
	return fake.status
}

func (fake *codexLifecycleFakeDesktop) SelectPath(context.Context, string) (codexdesktop.ControlStatus, error) {
	return fake.status, nil
}

func (fake *codexLifecycleFakeDesktop) Launch(context.Context) (codexdesktop.ControlStatus, error) {
	fake.launchCalls++
	fake.events = append(fake.events, "launch")
	if fake.launchFailures > 0 {
		fake.launchFailures--
		return fake.status, errors.New("simulated launch failure")
	}
	if fake.launchErr != nil {
		return fake.status, fake.launchErr
	}
	fake.status.Running = true
	fake.status.CanStop = true
	fake.status.CanRestart = true
	return fake.status, nil
}

func (fake *codexLifecycleFakeDesktop) Stop(context.Context, string) (codexdesktop.ControlStatus, error) {
	fake.stopCalls++
	fake.events = append(fake.events, "stop")
	if fake.stopErr != nil {
		return fake.status, fake.stopErr
	}
	fake.status.Running = false
	fake.status.CanStop = false
	fake.status.CanRestart = false
	return fake.status, nil
}

func (fake *codexLifecycleFakeDesktop) Restart(context.Context, string) (codexdesktop.ControlStatus, error) {
	return fake.status, errors.New("restart is not part of this transaction")
}

func newCodexLifecycleTestOperations(desktop *codexLifecycleFakeDesktop) codexLifecycleOperations {
	before := codexLifecycleTestSnapshot()
	return codexLifecycleOperations{
		desktop: desktop,
		inspect: func() (codexconfig.ConfigSnapshot, error) {
			return before, nil
		},
		apply: func(codexconfig.ApplyConfig) (codexconfig.ApplyResult, error) {
			return codexconfig.ApplyResult{BackupID: "config-backup"}, nil
		},
		restoreConfig: func(string) (codexconfig.RestoreResult, error) {
			return codexconfig.RestoreResult{}, nil
		},
		repairHistory: func() (codexconfig.HistoryRepairResult, error) {
			return codexconfig.HistoryRepairResult{Skipped: true}, nil
		},
		restoreHistory: func(string) error { return nil },
		refresh:        func() CodexConfigurationStatus { return CodexConfigurationStatus{} },
	}
}

func codexLifecycleTestSnapshot() codexconfig.ConfigSnapshot {
	return codexconfig.ConfigSnapshot{
		Location:      codexconfig.ConfigLocation{Exists: true},
		SHA256:        "before-sha256",
		Valid:         true,
		ModelProvider: "previous",
		Model:         "gpt-5.6-sol",
		ReviewModel:   "gpt-5.6-sol",
		WebSearch:     "live",
		Context:       codexconfig.DefaultContextSettings(),
	}
}

func codexLifecycleTestInput(providerID string) CodexConfigurationLifecycleInput {
	return CodexConfigurationLifecycleInput{
		Config: codexconfig.ApplyConfig{
			BaseURL:    "https://api.example.test",
			APIKey:     "test-only-key",
			ProviderID: providerID,
			Model:      "gpt-5.6-sol",
		},
		Confirmation:                  codexdesktop.LifecycleConfirmation,
		RepairHistoryOnProviderChange: true,
	}
}
