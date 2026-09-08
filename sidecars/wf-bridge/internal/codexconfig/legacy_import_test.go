package codexconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const legacyImportTestSecret = "legacy-import-secret-must-not-leak"

func TestLegacyConfigBackupDiscoveryAndCopyImport(t *testing.T) {
	home := t.TempDir()
	manager := NewManager(home)
	sourceID := "20260830T010203.123456789Z-abcdef12"
	original := []byte("model_provider = \"legacy\"\n[model_providers.legacy]\nexperimental_bearer_token = \"" + legacyImportTestSecret + "\"\n")
	legacyDirectory := writeLegacyConfigFixture(t, manager, sourceID, original, true)
	sourceManifestBefore := readTestFile(t, filepath.Join(legacyDirectory, "manifest.json"))
	sourceConfigBefore := readTestFile(t, filepath.Join(legacyDirectory, "config.toml"))

	items, err := manager.ListLegacyBackups()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Kind != LegacyBackupConfig || items[0].SourceID != sourceID || !items[0].Valid || !items[0].Importable {
		t.Fatalf("legacy discovery = %+v", items)
	}
	encodedItems, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encodedItems, []byte(legacyImportTestSecret)) {
		t.Fatalf("legacy discovery leaked a secret: %s", encodedItems)
	}

	result, err := manager.ImportLegacyConfigBackup(sourceID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Kind != LegacyBackupConfig || result.SourceID != sourceID || result.ImportedBackupID == "" {
		t.Fatalf("import result = %+v", result)
	}
	encodedResult, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encodedResult, []byte(legacyImportTestSecret)) {
		t.Fatalf("import result leaked a secret: %s", encodedResult)
	}

	manifest, err := manager.readManifest(result.ImportedBackupID)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Tool != DefaultProviderName || manifest.Reason != "imported_legacy_config" || manifest.ImportSource != legacyImportSource || manifest.ImportSourceID != sourceID || manifest.AppliedSHA256 != "" {
		t.Fatalf("imported manifest = %+v", manifest)
	}
	imported := readTestFile(t, manager.originalPath(result.ImportedBackupID))
	if !bytes.Equal(imported, original) {
		t.Fatal("imported configuration backup changed content")
	}
	if got := readTestFile(t, filepath.Join(legacyDirectory, "manifest.json")); !bytes.Equal(got, sourceManifestBefore) {
		t.Fatal("legacy configuration manifest changed during import")
	}
	if got := readTestFile(t, filepath.Join(legacyDirectory, "config.toml")); !bytes.Equal(got, sourceConfigBefore) {
		t.Fatal("legacy configuration data changed during import")
	}
	if _, err := os.Lstat(manager.ConfigPath); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("active config.toml changed during copy-only import: %v", err)
	}
}

func TestLegacyConfigImportRejectsUnsafeOrInconsistentArchives(t *testing.T) {
	t.Run("unexpected config for absent original", func(t *testing.T) {
		home := t.TempDir()
		manager := NewManager(home)
		sourceID := "20260830T020304.123456789Z-bcdef123"
		directory := writeLegacyConfigFixture(t, manager, sourceID, nil, false)
		if err := os.WriteFile(filepath.Join(directory, "config.toml"), []byte("model = \"unexpected\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		result, err := manager.ImportLegacyConfigBackup(sourceID)
		if err == nil || result.ImportedBackupID != "" {
			t.Fatalf("unsafe legacy configuration archive imported: %+v / %v", result, err)
		}
		assertNoCurrentConfigBackups(t, manager)
	})

	t.Run("unknown manifest field", func(t *testing.T) {
		home := t.TempDir()
		manager := NewManager(home)
		sourceID := "20260830T030405.123456789Z-cdef1234"
		directory := writeLegacyConfigFixture(t, manager, sourceID, []byte("model = \"safe\"\n"), true)
		manifestPath := filepath.Join(directory, "manifest.json")
		data := readTestFile(t, manifestPath)
		data = bytes.TrimSpace(data)
		data = append(data[:len(data)-1], []byte(",\n  \"unexpected\": true\n}\n")...)
		if err := os.WriteFile(manifestPath, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := manager.ImportLegacyConfigBackup(sourceID); err == nil {
			t.Fatal("archive with an unknown manifest field was imported")
		}
		assertNoCurrentConfigBackups(t, manager)
	})

	if runtime.GOOS != "windows" {
		t.Run("symbolic link data", func(t *testing.T) {
			home := t.TempDir()
			manager := NewManager(home)
			sourceID := "20260830T040506.123456789Z-def12345"
			directory := writeLegacyConfigFixture(t, manager, sourceID, []byte("model = \"safe\"\n"), true)
			outside := filepath.Join(home, "outside.toml")
			if err := os.WriteFile(outside, []byte("model = \"outside\"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(directory, "config.toml")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(directory, "config.toml")); err != nil {
				t.Fatal(err)
			}
			if _, err := manager.ImportLegacyConfigBackup(sourceID); err == nil {
				t.Fatal("symbolic-link configuration archive was imported")
			}
			assertNoCurrentConfigBackups(t, manager)
		})
	}
}

func TestLegacyConfigManifestOnlyBackupImportsWithoutConfigData(t *testing.T) {
	home := t.TempDir()
	manager := NewManager(home)
	sourceID := "20260830T050607.123456789Z-ef123456"
	writeLegacyConfigFixture(t, manager, sourceID, nil, false)
	result, err := manager.ImportLegacyConfigBackup(sourceID)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := manager.readManifest(result.ImportedBackupID)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.OriginalExisted || manifest.OriginalSHA256 != "" {
		t.Fatalf("manifest-only import = %+v", manifest)
	}
	if _, err := os.Lstat(manager.originalPath(result.ImportedBackupID)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("manifest-only backup unexpectedly has config data: %v", err)
	}
}

func TestLegacyDiscoveryExcludesWorkspaceArchivesAndPartialDestinations(t *testing.T) {
	home := t.TempDir()
	manager := NewManager(home)
	workspaceArchive := filepath.Join(home, legacyHelperDataDirectory, workspaceStateBackupDirectory, "20260830T010203.123456789Z")
	if err := os.MkdirAll(workspaceArchive, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspaceArchive, ".codex-global-state.json"), []byte(`{"local-projects":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	items, err := manager.ListLegacyBackups()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("workspace-only archive was exposed as an importable backup: %+v", items)
	}

	stage, err := createLegacyImportStage(manager.BackupRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(stage)
	destinationID, err := nextLegacyImportID(manager.BackupRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := publishLegacyImportStage(manager.BackupRoot, stage, destinationID); err == nil {
		t.Fatal("stage without a manifest was published")
	}
	assertNoCurrentConfigBackups(t, manager)
}

func TestLegacyHistoryBackupCopyImportExcludesSnapshotsAndPreservesActiveData(t *testing.T) {
	home := t.TempDir()
	manager := NewManager(home)
	sourceID, legacyDirectory, sessionPath, databasePath := writeLegacyHistoryFixture(t, manager)
	sourceManifestBefore := readTestFile(t, filepath.Join(legacyDirectory, "manifest.json"))
	sourceDatabaseBefore := readTestFile(t, filepath.Join(legacyDirectory, "database", "state_5.sqlite"))
	sourceSnapshotBefore := readTestFile(t, filepath.Join(legacyDirectory, "snapshot", "config.toml"))
	activeSessionBefore := readTestFile(t, sessionPath)
	activeDatabaseBefore := readTestFile(t, databasePath)

	items, err := manager.ListLegacyBackups()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Kind != LegacyBackupHistory || items[0].SourceID != sourceID || !items[0].Importable {
		t.Fatalf("legacy history discovery = %+v", items)
	}
	encodedItems, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encodedItems, []byte(legacyImportTestSecret)) {
		t.Fatalf("history discovery leaked a secret: %s", encodedItems)
	}

	result, err := manager.ImportLegacyHistoryBackup(sourceID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Kind != LegacyBackupHistory || result.ImportedBackupID == "" {
		t.Fatalf("history import result = %+v", result)
	}
	encodedResult, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encodedResult, []byte(legacyImportTestSecret)) {
		t.Fatalf("history import result leaked a secret: %s", encodedResult)
	}

	repairer := NewHistoryRepairerWithManager(manager)
	manifest := readHistoryManifest(t, repairer, result.ImportedBackupID)
	if manifest.ManagedBy != historyManagedBy || manifest.Status != historyStatusCommitted || manifest.ImportSource != legacyImportSource || manifest.ImportSourceID != sourceID {
		t.Fatalf("imported history manifest = %+v", manifest)
	}
	importedDirectory := filepath.Join(repairer.BackupRoot, result.ImportedBackupID)
	if data := readTestFile(t, filepath.Join(importedDirectory, "manifest.json")); bytes.Contains(data, []byte(legacyImportTestSecret)) {
		t.Fatal("legacy status metadata was copied into the current manifest")
	}
	if _, err := os.Lstat(filepath.Join(importedDirectory, "snapshot")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("legacy snapshot was copied into current backup: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(importedDirectory, "database", "state_5.sqlite")); err != nil {
		t.Fatalf("imported database snapshot is missing: %v", err)
	}
	if got := readTestFile(t, sessionPath); !bytes.Equal(got, activeSessionBefore) {
		t.Fatal("active session changed during history import")
	}
	if got := readTestFile(t, databasePath); !bytes.Equal(got, activeDatabaseBefore) {
		t.Fatal("active database changed during history import")
	}
	if got := readTestFile(t, filepath.Join(legacyDirectory, "manifest.json")); !bytes.Equal(got, sourceManifestBefore) {
		t.Fatal("legacy history manifest changed during import")
	}
	if got := readTestFile(t, filepath.Join(legacyDirectory, "database", "state_5.sqlite")); !bytes.Equal(got, sourceDatabaseBefore) {
		t.Fatal("legacy database snapshot changed during import")
	}
	if got := readTestFile(t, filepath.Join(legacyDirectory, "snapshot", "config.toml")); !bytes.Equal(got, sourceSnapshotBefore) {
		t.Fatal("legacy snapshot changed during import")
	}
	backups, err := repairer.ListBackups()
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 || backups[0].ID != result.ImportedBackupID {
		t.Fatalf("current history backup list = %+v", backups)
	}
}

func TestLegacyHistoryImportRejectsUnsafeArchivesAndLeavesNoCurrentBackup(t *testing.T) {
	t.Run("non committed", func(t *testing.T) {
		home := t.TempDir()
		manager := NewManager(home)
		sourceID, legacyDirectory, _, _ := writeLegacyHistoryFixture(t, manager)
		manifestPath := filepath.Join(legacyDirectory, "manifest.json")
		manifest := readLegacyHistoryManifestFixture(t, manifestPath)
		manifest.Status = historyStatusApplying
		writeJSONFixture(t, manifestPath, manifest)
		if _, err := manager.ImportLegacyHistoryBackup(sourceID); err == nil {
			t.Fatal("non-committed legacy history archive was imported")
		}
		assertNoCurrentHistoryBackups(t, manager)
	})

	t.Run("extra source file", func(t *testing.T) {
		home := t.TempDir()
		manager := NewManager(home)
		sourceID, legacyDirectory, _, _ := writeLegacyHistoryFixture(t, manager)
		if err := os.WriteFile(filepath.Join(legacyDirectory, "unexpected.txt"), []byte("not a backup artifact"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := manager.ImportLegacyHistoryBackup(sourceID); err == nil {
			t.Fatal("legacy history archive with extra files was imported")
		}
		assertNoCurrentHistoryBackups(t, manager)
	})

	t.Run("database checksum mismatch", func(t *testing.T) {
		home := t.TempDir()
		manager := NewManager(home)
		sourceID, legacyDirectory, _, _ := writeLegacyHistoryFixture(t, manager)
		if err := os.WriteFile(filepath.Join(legacyDirectory, "database", "state_5.sqlite"), []byte("corrupt"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := manager.ImportLegacyHistoryBackup(sourceID); err == nil {
			t.Fatal("legacy history archive with mismatched database checksum was imported")
		}
		assertNoCurrentHistoryBackups(t, manager)
	})
}

func TestHistoryRestoreExplicitlyRejectsNonCommittedBackup(t *testing.T) {
	home := t.TempDir()
	manager := NewManager(home)
	writeHistoryConfig(t, home, DefaultProviderID)
	session := writeHistoryRollout(t, home, "sessions/restore-status.jsonl", "openai", "thread-status")
	createHistoryDatabase(t, filepath.Join(home, "state_5.sqlite"), map[string]string{"thread-status": "openai"})
	repairer := NewHistoryRepairerWithManager(manager)
	result, err := repairer.RepairCurrentProvider()
	if err != nil {
		t.Fatal(err)
	}
	before := readTestFile(t, session)
	manifestPath := filepath.Join(repairer.BackupRoot, result.BackupID, "manifest.json")
	manifest := readHistoryManifest(t, repairer, result.BackupID)
	manifest.Status = historyStatusRolledBack
	writeJSONFixture(t, manifestPath, manifest)
	if err := repairer.RestoreBackup(result.BackupID); err == nil || !strings.Contains(err.Error(), "completed") {
		t.Fatalf("RestoreBackup() error = %v, want completed-status rejection", err)
	}
	if after := readTestFile(t, session); !bytes.Equal(before, after) {
		t.Fatal("history changed despite a non-committed backup rejection")
	}
}

func writeLegacyConfigFixture(t *testing.T, manager *Manager, id string, data []byte, originalExisted bool) string {
	t.Helper()
	directory := filepath.Join(manager.legacyConfigBackupRoot(), id)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := legacyConfigBackupManifest{
		Version:         backupManifestVersion,
		ID:              id,
		Reason:          "apply",
		CreatedAt:       legacyFixtureTime(),
		ConfigPath:      manager.ConfigPath,
		OriginalExisted: originalExisted,
		OriginalMode:    0o600,
	}
	if originalExisted {
		manifest.OriginalSHA256 = sha256Hex(data)
		if err := os.WriteFile(filepath.Join(directory, "config.toml"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeJSONFixture(t, filepath.Join(directory, "manifest.json"), manifest)
	return directory
}

func writeLegacyHistoryFixture(t *testing.T, manager *Manager) (string, string, string, string) {
	t.Helper()
	home := manager.CodexHome
	writeHistoryConfig(t, home, DefaultProviderID)
	sessionPath := writeHistoryRollout(t, home, "sessions/legacy-import.jsonl", "openai", "legacy-import-thread")
	databasePath := filepath.Join(home, "state_5.sqlite")
	createHistoryDatabase(t, databasePath, map[string]string{"legacy-import-thread": "openai"})

	repairer := NewHistoryRepairerWithManager(manager)
	result, err := repairer.RepairCurrentProvider()
	if err != nil {
		t.Fatal(err)
	}
	current := readHistoryManifest(t, repairer, result.BackupID)
	legacy := legacyHistoryBackupManifest{
		Version:            current.Version,
		ID:                 current.ID,
		CreatedAt:          current.CreatedAt,
		CodexHome:          current.CodexHome,
		TargetProvider:     current.TargetProvider,
		SourceProviders:    append([]string(nil), current.SourceProviders...),
		ScannedFiles:       current.ScannedFiles,
		RolloutFilesSHA256: current.RolloutFilesSHA256,
		ManagedBy:          legacyHistoryManagedBy,
		Status:             historyStatusCommitted,
		StatusMessage:      legacyImportTestSecret,
		SessionChanges:     append([]historySessionPlan(nil), current.SessionChanges...),
		DatabaseFiles:      append([]historyBackupFile(nil), current.DatabaseFiles...),
		DatabasePlans:      append([]historyDatabasePlan(nil), current.DatabasePlans...),
	}
	legacyDirectory := filepath.Join(manager.legacyHistoryBackupRoot(), result.BackupID)
	if err := os.MkdirAll(filepath.Join(legacyDirectory, "database"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, file := range legacy.DatabaseFiles {
		source := filepath.Join(repairer.BackupRoot, result.BackupID, filepath.FromSlash(file.BackupPath))
		destination := filepath.Join(legacyDirectory, filepath.FromSlash(file.BackupPath))
		copyFixtureFile(t, source, destination)
	}
	if err := os.MkdirAll(filepath.Join(legacyDirectory, "snapshot"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyDirectory, "snapshot", "config.toml"), []byte("experimental_bearer_token = \""+legacyImportTestSecret+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeJSONFixture(t, filepath.Join(legacyDirectory, "manifest.json"), legacy)
	if err := os.RemoveAll(filepath.Join(repairer.BackupRoot, result.BackupID)); err != nil {
		t.Fatal(err)
	}
	return result.BackupID, legacyDirectory, sessionPath, databasePath
}

func readLegacyHistoryManifestFixture(t *testing.T, path string) legacyHistoryBackupManifest {
	t.Helper()
	data := readTestFile(t, path)
	var manifest legacyHistoryBackupManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	return manifest
}

func writeJSONFixture(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readTestFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func copyFixtureFile(t *testing.T, source, destination string) {
	t.Helper()
	data := readTestFile(t, source)
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertNoCurrentConfigBackups(t *testing.T, manager *Manager) {
	t.Helper()
	backups, err := manager.ListBackups()
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 0 {
		t.Fatalf("current configuration backups unexpectedly exist: %+v", backups)
	}
}

func assertNoCurrentHistoryBackups(t *testing.T, manager *Manager) {
	t.Helper()
	backups, err := NewHistoryRepairerWithManager(manager).ListBackups()
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 0 {
		t.Fatalf("current history backups unexpectedly exist: %+v", backups)
	}
}

func legacyFixtureTime() time.Time {
	return time.Date(2026, time.August, 30, 1, 2, 3, 123456789, time.UTC)
}
