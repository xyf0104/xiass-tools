package main

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"antigravity-wf-assistant/internal/codexconfig"
	"antigravity-wf-assistant/internal/codexdesktop"
)

func TestLegacyProviderMigrationLifecycleRequiresConfirmationBeforeAnyMutation(t *testing.T) {
	desktop := newCodexLifecycleFakeDesktop(true)
	operations, _, migrateCalls := newLegacyProviderMigrationLifecycleTestOperations(desktop)

	result := runCodexLegacyProviderMigrationWithLifecycle(context.Background(), "", operations)

	if result.OK || desktop.stopCalls != 0 || *migrateCalls != 0 || desktop.launchCalls != 0 {
		t.Fatalf("unconfirmed migration changed lifecycle state: %#v stop=%d migrate=%d launch=%d", result, desktop.stopCalls, *migrateCalls, desktop.launchCalls)
	}
	if !desktop.status.Running {
		t.Fatal("unconfirmed migration changed desktop running state")
	}
}

func TestLegacyProviderMigrationLifecycleStopsMigratesAndRelaunchesOnlyWithConfirmation(t *testing.T) {
	desktop := newCodexLifecycleFakeDesktop(true)
	operations, _, migrateCalls := newLegacyProviderMigrationLifecycleTestOperations(desktop)

	result := runCodexLegacyProviderMigrationWithLifecycle(context.Background(), codexdesktop.LifecycleConfirmation, operations)

	if !result.OK || !result.Migrated || !result.DesktopStopped || !result.Relaunched || *migrateCalls != 1 || !desktop.status.Running {
		t.Fatalf("confirmed migration lifecycle = %#v, migrate=%d", result, *migrateCalls)
	}
	if want := []string{"stop", "migrate", "launch"}; !reflect.DeepEqual(desktop.events, want) {
		t.Fatalf("migration lifecycle events = %#v, want %#v", desktop.events, want)
	}
}

func TestLegacyProviderMigrationLifecycleLaunchFailureRestoresBeforeRelaunch(t *testing.T) {
	desktop := newCodexLifecycleFakeDesktop(true)
	desktop.launchFailures = 1
	operations, current, migrateCalls := newLegacyProviderMigrationLifecycleTestOperations(desktop)
	before := *current

	result := runCodexLegacyProviderMigrationWithLifecycle(context.Background(), codexdesktop.LifecycleConfirmation, operations)

	if result.OK || !result.Migrated || !result.RolledBack || !result.Relaunched || *migrateCalls != 1 || !desktop.status.Running {
		t.Fatalf("launch-failure migration lifecycle = %#v, migrate=%d", result, *migrateCalls)
	}
	if !lifecycleConfigMatches(before, *current) {
		t.Fatalf("launch-failure rollback did not restore prior configuration: before=%#v after=%#v", before, *current)
	}
	if want := []string{"stop", "migrate", "launch", "restore", "launch"}; !reflect.DeepEqual(desktop.events, want) {
		t.Fatalf("launch-failure events = %#v, want %#v", desktop.events, want)
	}
}

func TestLegacyProviderMigrationLifecycleResponseIsRedacted(t *testing.T) {
	desktop := newCodexLifecycleFakeDesktop(false)
	operations, _, _ := newLegacyProviderMigrationLifecycleTestOperations(desktop)
	operations.refresh = func() CodexConfigurationStatus {
		return CodexConfigurationStatus{
			OK:      true,
			Message: "safe fixed copy",
			Snapshot: codexconfig.ConfigSnapshot{Location: codexconfig.ConfigLocation{
				CodexHome:  "/private/legacy-migration-user/.codex",
				ConfigPath: "/private/legacy-migration-user/.codex/config.toml",
				Exists:     true,
			}},
		}
	}

	result := runCodexLegacyProviderMigrationWithLifecycle(context.Background(), "", operations)
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"/private/legacy-migration-user", "experimental_bearer_token", "legacy-key"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("lifecycle migration response leaked protected material: %s", encoded)
		}
	}
}

func newLegacyProviderMigrationLifecycleTestOperations(desktop *codexLifecycleFakeDesktop) (codexLegacyProviderMigrationLifecycleOperations, *codexconfig.ConfigSnapshot, *int) {
	before := codexLifecycleTestSnapshot()
	before.ModelProvider = "xiass"
	before.SHA256 = "legacy-before"
	before.LegacyProviderMigration = codexconfig.LegacyProviderMigrationStatus{Available: true, ProviderID: "xiass", WasActive: true}
	current := before
	migrateCalls := 0
	operations := codexLegacyProviderMigrationLifecycleOperations{
		desktop: desktop,
		inspect: func() (codexconfig.ConfigSnapshot, error) {
			return current, nil
		},
		migrate: func() (codexconfig.LegacyProviderMigrationResult, error) {
			migrateCalls++
			desktop.events = append(desktop.events, "migrate")
			current.ModelProvider = codexconfig.DefaultProviderID
			current.SHA256 = "legacy-after"
			current.LegacyProviderMigration = codexconfig.LegacyProviderMigrationStatus{}
			return codexconfig.LegacyProviderMigrationResult{BackupID: "legacy-config-backup", Migrated: true, ProviderID: "xiass", WasActive: true}, nil
		},
		restore: func(string) (codexconfig.RestoreResult, error) {
			desktop.events = append(desktop.events, "restore")
			current = before
			return codexconfig.RestoreResult{RestoredBackupID: "legacy-config-backup"}, nil
		},
		refresh: func() CodexConfigurationStatus {
			return CodexConfigurationStatus{OK: true, Snapshot: current}
		},
	}
	return operations, &current, &migrateCalls
}
