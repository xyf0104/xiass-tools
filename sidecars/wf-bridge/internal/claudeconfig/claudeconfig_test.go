package claudeconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const (
	oldTestToken = "test-token-old-not-a-real-credential"
	newTestToken = "test-token-new-not-a-real-credential"
)

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	configDir := filepath.Join(t.TempDir(), "claude-config")
	backupRoot := filepath.Join(t.TempDir(), "app-owned-backups")
	return NewManagerWithOptions(configDir, ManagerOptions{BackupRoot: backupRoot})
}

func writeSettings(t *testing.T, manager *Manager, data []byte) {
	t.Helper()
	if err := os.MkdirAll(manager.ConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manager.SettingsPath, data, 0o640); err != nil {
		t.Fatal(err)
	}
}

func TestApplyInspectRestorePreservesUnknownFieldsAndRedactsToken(t *testing.T) {
	manager := newTestManager(t)
	original := []byte(`{
  "permissions": {"allow": ["Bash(git status)"]},
  "customExtensionSetting": {"enabled": true},
  "model": "sonnet",
  "env": {
    "KEEP_ME": "preserved",
    "ANTHROPIC_API_KEY": "unmanaged-test-key",
    "ANTHROPIC_AUTH_TOKEN": "` + oldTestToken + `"
  }
}
`)
	writeSettings(t, manager, original)

	result, err := manager.Apply(ApplyConfig{
		BaseURL:   "https://gateway.example.test/v1/",
		AuthToken: newTestToken,
		Model:     "claude-opus-4-6",
	})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if result.BackupID == "" || !validSHA256(result.SettingsSHA256) {
		t.Fatalf("Apply() result = %+v", result)
	}

	written, err := os.ReadFile(manager.SettingsPath)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(written, &root); err != nil {
		t.Fatal(err)
	}
	var permissions struct {
		Allow []string `json:"allow"`
	}
	var customExtensionSetting struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.Unmarshal(root["permissions"], &permissions); err != nil || len(permissions.Allow) != 1 || permissions.Allow[0] != "Bash(git status)" {
		t.Fatalf("permissions were not preserved: %v / %s", err, written)
	}
	if err := json.Unmarshal(root["customExtensionSetting"], &customExtensionSetting); err != nil || !customExtensionSetting.Enabled {
		t.Fatalf("custom extension setting was not preserved: %v / %s", err, written)
	}
	var environment map[string]string
	if err := json.Unmarshal(root["env"], &environment); err != nil {
		t.Fatal(err)
	}
	if environment["KEEP_ME"] != "preserved" || environment["ANTHROPIC_API_KEY"] != "" || environment["ANTHROPIC_BASE_URL"] != "https://gateway.example.test/v1" || environment["ANTHROPIC_AUTH_TOKEN"] != newTestToken || environment["model"] != "" {
		t.Fatalf("unexpected managed environment: %#v", environment)
	}
	var model string
	if err := json.Unmarshal(root["model"], &model); err != nil || model != "claude-opus-4-6" {
		t.Fatalf("managed model = %q / %v", model, err)
	}
	if runtime.GOOS != "windows" {
		if info, statErr := os.Stat(manager.SettingsPath); statErr != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("settings mode = %v / %v, want 0600", info, statErr)
		}
	}

	snapshot, err := manager.Verify()
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Valid || !snapshot.Managed || !snapshot.AuthTokenConfigured || snapshot.BaseURL != "https://gateway.example.test/v1" || snapshot.Model != "claude-opus-4-6" {
		t.Fatalf("redacted snapshot = %+v", snapshot)
	}
	encodedSnapshot, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	encodedResult, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	encodedInput, err := json.Marshal(ApplyConfig{BaseURL: "https://gateway.example.test", AuthToken: newTestToken, Model: "sonnet"})
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{oldTestToken, newTestToken, "unmanaged-test-key"} {
		if strings.Contains(string(encodedSnapshot), secret) || strings.Contains(string(encodedResult), secret) || strings.Contains(string(encodedInput), secret) {
			t.Fatalf("safe return value leaked test secret %q", secret)
		}
	}

	manifestPath, err := manager.backupFilePath(result.BackupID, "manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{oldTestToken, newTestToken, "unmanaged-test-key"} {
		if strings.Contains(string(manifest), secret) {
			t.Fatalf("manifest leaked test secret %q", secret)
		}
	}
	backupPath, err := manager.backupFilePath(result.BackupID, settingsFilename)
	if err != nil {
		t.Fatal(err)
	}
	backup, err := os.ReadFile(backupPath)
	if err != nil || !bytes.Equal(backup, original) {
		t.Fatalf("backup did not preserve original settings: %v", err)
	}

	restored, err := manager.Restore(result.BackupID)
	if err != nil {
		t.Fatalf("Restore() error = %v", err)
	}
	if restored.RestoredBackupID != result.BackupID || restored.SafetyBackupID == "" {
		t.Fatalf("Restore() result = %+v", restored)
	}
	afterRestore, err := os.ReadFile(manager.SettingsPath)
	if err != nil || !bytes.Equal(afterRestore, original) {
		t.Fatalf("restored settings mismatch: %v", err)
	}
}

func TestCustomClaudeConfigDirOnlyTargetsSettingsJSON(t *testing.T) {
	base := t.TempDir()
	configDir := filepath.Join(base, "custom-claude-config")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", configDir)
	resolved, err := DefaultConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	if resolved != canonicalPathOrEmpty(configDir) {
		t.Fatalf("DefaultConfigDir() = %q, want %q", resolved, canonicalPathOrEmpty(configDir))
	}
	legacyPath := filepath.Join(base, ".claude.json")
	credentialPath := filepath.Join(configDir, ".credentials.json")
	legacy := []byte(`{"autoConnectIde":true,"token":"do-not-read"}`)
	credentials := []byte(`{"oauthAccount":{"accessToken":"do-not-read"}}`)
	if err := os.WriteFile(legacyPath, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(credentialPath, credentials, 0o600); err != nil {
		t.Fatal(err)
	}
	manager := NewManagerWithOptions(resolved, ManagerOptions{BackupRoot: filepath.Join(t.TempDir(), "backups")})
	if _, err := manager.Apply(ApplyConfig{BaseURL: "http://127.0.0.1:50999/v1", AuthToken: newTestToken, Model: "sonnet"}); err != nil {
		t.Fatal(err)
	}
	if manager.SettingsPath != filepath.Join(canonicalPathOrEmpty(configDir), settingsFilename) {
		t.Fatalf("settings target = %q", manager.SettingsPath)
	}
	for path, want := range map[string][]byte{legacyPath: legacy, credentialPath: credentials} {
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("unmanaged Claude file changed: %s / %v", filepath.Base(path), err)
		}
	}
}

func TestInspectDoesNotCreateOrReadBackupState(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), "missing-claude-config")
	backupRoot := filepath.Join(t.TempDir(), "missing-xiass-backups")
	manager := NewManagerWithOptions(configDir, ManagerOptions{BackupRoot: backupRoot})
	snapshot, err := manager.Inspect()
	if err != nil || !snapshot.Valid || snapshot.Location.Exists {
		t.Fatalf("Inspect() = %#v / %v", snapshot, err)
	}
	for _, path := range []string{manager.ConfigDir, manager.BackupRoot} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("Inspect unexpectedly created or read-backed %q: %v", path, err)
		}
	}
}

func TestApplyRejectsInvalidJSONSymlinkAndSecretBearingInput(t *testing.T) {
	manager := newTestManager(t)
	invalid := []byte(`{"model":"sonnet",`)
	writeSettings(t, manager, invalid)
	if _, err := manager.Apply(ApplyConfig{BaseURL: "https://gateway.example.test", AuthToken: newTestToken, Model: "sonnet"}); !errors.Is(err, errInvalidSettings) {
		t.Fatalf("invalid settings apply error = %v", err)
	}
	afterInvalid, err := os.ReadFile(manager.SettingsPath)
	if err != nil || !bytes.Equal(afterInvalid, invalid) {
		t.Fatalf("invalid settings changed: %v", err)
	}
	for _, input := range []ApplyConfig{
		{BaseURL: "https://secret@host.example.test", AuthToken: newTestToken, Model: "sonnet"},
		{BaseURL: "https://gateway.example.test?token=not-allowed", AuthToken: newTestToken, Model: "sonnet"},
		{BaseURL: "https://gateway.example.test", AuthToken: "Bearer " + newTestToken, Model: "sonnet"},
		{BaseURL: "http://gateway.example.test", AuthToken: newTestToken, Model: "sonnet"},
	} {
		if _, err := manager.Apply(input); err == nil {
			t.Fatalf("unsafe configuration was accepted: %#v", input)
		} else if strings.Contains(err.Error(), newTestToken) || strings.Contains(err.Error(), "secret@") || strings.Contains(err.Error(), "not-allowed") {
			t.Fatalf("validation error leaked sensitive input: %v", err)
		}
	}
	if runtime.GOOS == "windows" {
		return
	}
	target := filepath.Join(t.TempDir(), "settings-target.json")
	if err := os.WriteFile(target, []byte(`{"model":"sonnet"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(manager.SettingsPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, manager.SettingsPath); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Apply(ApplyConfig{BaseURL: "https://gateway.example.test", AuthToken: newTestToken, Model: "sonnet"}); !errors.Is(err, errUnsafePath) {
		t.Fatalf("symlink settings apply error = %v", err)
	}
	if targetData, err := os.ReadFile(target); err != nil || string(targetData) != `{"model":"sonnet"}` {
		t.Fatalf("symlink target changed: %v", err)
	}
}

func TestApplyRollsBackAfterReadbackFailureWithoutLeakingToken(t *testing.T) {
	manager := newTestManager(t)
	original := []byte("{\"model\":\"sonnet\",\"env\":{\"KEEP_ME\":\"yes\"}}\n")
	writeSettings(t, manager, original)
	manager.afterAtomicWriteForTest = func() error { return errors.New("forced test verification failure") }
	_, err := manager.Apply(ApplyConfig{BaseURL: "https://gateway.example.test/v1", AuthToken: newTestToken, Model: "claude-sonnet-4-6"})
	var mutation *MutationError
	if !errors.As(err, &mutation) || mutation.RollbackErr != nil {
		t.Fatalf("rollback result = %T %v", err, err)
	}
	if strings.Contains(err.Error(), newTestToken) {
		t.Fatalf("rollback error leaked token: %v", err)
	}
	after, readErr := os.ReadFile(manager.SettingsPath)
	if readErr != nil || !bytes.Equal(after, original) {
		t.Fatalf("rollback did not restore original: %v", readErr)
	}
}

func TestRestoreAndDeleteRejectTamperedBackup(t *testing.T) {
	manager := newTestManager(t)
	original := []byte("{\"model\":\"sonnet\",\"env\":{\"KEEP_ME\":\"yes\"}}\n")
	writeSettings(t, manager, original)
	result, err := manager.Apply(ApplyConfig{BaseURL: "https://gateway.example.test/v1", AuthToken: newTestToken, Model: "claude-sonnet-4-6"})
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(manager.SettingsPath)
	if err != nil {
		t.Fatal(err)
	}
	backupPath, err := manager.backupFilePath(result.BackupID, settingsFilename)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backupPath, []byte(`{"model":"tampered"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Restore(result.BackupID); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("tampered backup restore error = %v", err)
	}
	after, err := os.ReadFile(manager.SettingsPath)
	if err != nil || !bytes.Equal(after, before) {
		t.Fatalf("active settings changed after failed restore: %v", err)
	}
	if err := manager.DeleteBackup(result.BackupID); err == nil {
		t.Fatal("tampered backup deletion was accepted")
	}
}

func TestRestoreRemovesSettingsCreatedForAnAbsentOriginal(t *testing.T) {
	manager := newTestManager(t)
	result, err := manager.Apply(ApplyConfig{BaseURL: "https://gateway.example.test/v1", AuthToken: newTestToken, Model: "sonnet[1m]"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Restore(result.BackupID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(manager.SettingsPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("settings file remains after restoring absent original: %v", err)
	}
}

func TestNormalizeBaseURLAndRejectDuplicateSettingsKeys(t *testing.T) {
	for input, want := range map[string]string{
		"https://gateway.example.test/v1/": "https://gateway.example.test/v1",
		"http://127.0.0.1:50999/v1":        "http://127.0.0.1:50999/v1",
		"http://[::1]:50999/v1":            "http://[::1]:50999/v1",
	} {
		got, err := NormalizeBaseURL(input)
		if err != nil || got != want {
			t.Fatalf("NormalizeBaseURL(%q) = %q / %v, want %q", input, got, err, want)
		}
	}
	manager := newTestManager(t)
	duplicate := []byte(`{"model":"sonnet","model":"opus","env":{}}`)
	writeSettings(t, manager, duplicate)
	if _, err := manager.Apply(ApplyConfig{BaseURL: "https://gateway.example.test", AuthToken: newTestToken, Model: "sonnet"}); !errors.Is(err, errInvalidSettings) {
		t.Fatalf("duplicate key settings apply error = %v", err)
	}
}

func TestLegacyBackupsAreReadOnlyAndMigrateByCopy(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), "claude-config")
	legacyRoot := filepath.Join(t.TempDir(), "legacy-backups")
	manager := NewManagerWithOptions(configDir, ManagerOptions{
		BackupRoot:        filepath.Join(t.TempDir(), "xiass-tools-backups"),
		LegacyBackupRoots: []string{legacyRoot},
	})
	legacyID, err := newBackupID()
	if err != nil {
		t.Fatal(err)
	}
	legacyDirectory := filepath.Join(legacyRoot, legacyID)
	if err := os.MkdirAll(legacyDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"model":"sonnet","env":{"KEEP_ME":"legacy"}}`)
	manifest := BackupManifest{
		Version:         backupManifestVersion,
		Tool:            legacyBackupToolName,
		ID:              legacyID,
		Reason:          "apply",
		CreatedAt:       time.Now().UTC(),
		TargetSHA256:    sha256Hex([]byte(manager.SettingsPath)),
		OriginalExisted: true,
		OriginalMode:    0o600,
		OriginalSHA256:  sha256Hex(original),
	}
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	legacySettingsPath := filepath.Join(legacyDirectory, settingsFilename)
	legacyManifestPath := filepath.Join(legacyDirectory, "manifest.json")
	if err := os.WriteFile(legacySettingsPath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyManifestPath, manifestData, 0o600); err != nil {
		t.Fatal(err)
	}
	beforeSettings, _ := os.ReadFile(legacySettingsPath)
	beforeManifest, _ := os.ReadFile(legacyManifestPath)

	legacy, err := manager.ListLegacyBackups()
	if err != nil || len(legacy) != 1 || legacy[0].ID != legacyID {
		t.Fatalf("legacy backup list = %#v / %v", legacy, err)
	}
	migrated, err := manager.MigrateLegacyBackup(legacy[0].Source, legacyID)
	if err != nil || migrated.ID == "" || migrated.Reason != "migrated_legacy" {
		t.Fatalf("legacy migration = %#v / %v", migrated, err)
	}
	afterSettings, settingsErr := os.ReadFile(legacySettingsPath)
	afterManifest, manifestErr := os.ReadFile(legacyManifestPath)
	if settingsErr != nil || manifestErr != nil || !bytes.Equal(beforeSettings, afterSettings) || !bytes.Equal(beforeManifest, afterManifest) {
		t.Fatal("legacy backup was modified during migration")
	}
	backups, err := manager.ListBackups()
	if err != nil || len(backups) != 1 || backups[0].ID != migrated.ID {
		t.Fatalf("migrated backup list = %#v / %v", backups, err)
	}
}
