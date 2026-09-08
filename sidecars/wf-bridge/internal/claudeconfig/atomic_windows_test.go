//go:build windows

package claudeconfig

import (
	"path/filepath"
	"runtime"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsClaudeSettingsAndBackupsUseProtectedCurrentUserDACL(t *testing.T) {
	manager := newTestManager(t)
	writeSettings(t, manager, []byte(`{"model":"sonnet","env":{"KEEP_ME":"yes"}}`))
	result, err := manager.Apply(ApplyConfig{
		BaseURL:   "https://gateway.example.test/v1",
		AuthToken: newTestToken,
		Model:     "claude-sonnet-4-6",
	})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}

	assertWindowsCurrentUserOnlyDACL(t, manager.SettingsPath, false)
	assertWindowsCurrentUserOnlyDACL(t, manager.BackupRoot, true)
	backupDirectory, err := manager.backupDirectory(result.BackupID)
	if err != nil {
		t.Fatal(err)
	}
	assertWindowsCurrentUserOnlyDACL(t, backupDirectory, true)
	assertWindowsCurrentUserOnlyDACL(t, filepath.Join(backupDirectory, settingsFilename), false)
	assertWindowsCurrentUserOnlyDACL(t, filepath.Join(backupDirectory, "manifest.json"), false)
}

func TestWindowsPrivateDACLVerifierRejectsAdditionalPrincipal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := writeFileAtomic(path, []byte(`{"model":"sonnet"}`), 0o600); err != nil {
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
