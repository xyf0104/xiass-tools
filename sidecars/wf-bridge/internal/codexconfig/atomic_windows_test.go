//go:build windows

package codexconfig

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsCodexApplyAndRemoveUseProtectedCurrentUserDACL(t *testing.T) {
	manager := NewManager(t.TempDir())
	if err := os.WriteFile(manager.ConfigPath, []byte(originalConfig), 0o640); err != nil {
		t.Fatal(err)
	}
	apply, err := manager.Apply(ApplyConfig{
		BaseURL: "https://api.xiass.example/v1",
		APIKey:  "not-a-real-test-key",
		Model:   "gpt-5.6-sol",
	})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	assertWindowsCurrentUserOnlyDACL(t, manager.ConfigPath, false)
	assertWindowsBackupDACLs(t, manager, apply.BackupID)

	removed, err := manager.RemoveXIASSProvider()
	if err != nil {
		t.Fatalf("RemoveXIASSProvider() error = %v", err)
	}
	if !removed.Removed || removed.BackupID == "" {
		t.Fatalf("RemoveXIASSProvider() result = %#v", removed)
	}
	assertWindowsCurrentUserOnlyDACL(t, manager.ConfigPath, false)
	assertWindowsBackupDACLs(t, manager, removed.BackupID)
}

func TestWindowsCodexLegacyProviderMigrationUsesProtectedCurrentUserDACL(t *testing.T) {
	manager := NewManagerWithOptions(t.TempDir(), ManagerOptions{HistoryWriteGuard: func() error { return nil }})
	legacy := []byte(`model_provider = "xiass"
model = "gpt-5.6-sol"

[model_providers.xiass]
name = "XIASS legacy"
base_url = "https://api.xiass.example/v1"
experimental_bearer_token = "legacy-test-key"
`)
	if err := os.WriteFile(manager.ConfigPath, legacy, 0o640); err != nil {
		t.Fatal(err)
	}
	result, err := manager.MigrateLegacyProvider()
	if err != nil {
		t.Fatalf("MigrateLegacyProvider() error = %v", err)
	}
	if !result.Migrated || result.BackupID == "" {
		t.Fatalf("MigrateLegacyProvider() result = %#v", result)
	}
	assertWindowsCurrentUserOnlyDACL(t, manager.ConfigPath, false)
	assertWindowsBackupDACLs(t, manager, result.BackupID)
}

func TestWindowsCodexPrivateDACLVerifierRejectsAdditionalPrincipal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := writeFileAtomic(path, []byte("model = \"test\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	currentSID, storage, err := currentProcessUserSID()
	if err != nil {
		t.Fatal(err)
	}
	worldSID, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	if err != nil {
		t.Fatal(err)
	}
	var pinner runtime.Pinner
	pinner.Pin(&storage[0])
	pinner.Pin(worldSID)
	defer pinner.Unpin()
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{
		{
			AccessPermissions: privateFileAllAccess,
			AccessMode:        windows.GRANT_ACCESS,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_USER,
				TrusteeValue: windows.TrusteeValueFromSID(currentSID),
			},
		},
		{
			AccessPermissions: windows.GENERIC_READ,
			AccessMode:        windows.GRANT_ACCESS,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
				TrusteeValue: windows.TrusteeValueFromSID(worldSID),
			},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		acl,
		nil,
	); err != nil {
		t.Fatal(err)
	}
	if err := verifyCurrentUserOnlyDACL(path, currentSID, false); err == nil {
		t.Fatal("verifier accepted a DACL granting access to Everyone")
	}
	runtime.KeepAlive(storage)
	runtime.KeepAlive(worldSID)
}

func assertWindowsBackupDACLs(t *testing.T, manager *Manager, backupID string) {
	t.Helper()
	assertWindowsCurrentUserOnlyDACL(t, manager.BackupRoot, true)
	directory := filepath.Join(manager.BackupRoot, backupID)
	assertWindowsCurrentUserOnlyDACL(t, directory, true)
	assertWindowsCurrentUserOnlyDACL(t, filepath.Join(directory, "config.toml"), false)
	assertWindowsCurrentUserOnlyDACL(t, filepath.Join(directory, "manifest.json"), false)
}

func assertWindowsCurrentUserOnlyDACL(t *testing.T, path string, directory bool) {
	t.Helper()
	sid, storage, err := currentProcessUserSID()
	if err != nil {
		t.Fatal(err)
	}
	var pinner runtime.Pinner
	pinner.Pin(&storage[0])
	defer pinner.Unpin()
	if err := verifyCurrentUserOnlyDACL(path, sid, directory); err != nil {
		t.Fatalf("private DACL verification failed for %q: %v", path, err)
	}
	runtime.KeepAlive(storage)
}
