package patcher

import (
	"path/filepath"
	"testing"
)

func TestXIASSPatcherBackupDirPrefersNewOverride(t *testing.T) {
	newDir := filepath.Join(t.TempDir(), "xiass-backups")
	legacyDir := filepath.Join(t.TempDir(), "legacy-backups")
	t.Setenv("XIASS_TOOLS_BACKUP_DIR", newDir)
	t.Setenv("ANTIGRAVITY_BYOK_BACKUP_DIR", legacyDir)
	if got := xiassPatcherBackupDir(); got != filepath.Clean(newDir) {
		t.Fatalf("backup directory = %q, want %q", got, filepath.Clean(newDir))
	}
}

func TestXIASSPatcherBackupDirKeepsLegacyTestOverride(t *testing.T) {
	legacyDir := filepath.Join(t.TempDir(), "legacy-backups")
	t.Setenv("XIASS_TOOLS_BACKUP_DIR", "")
	t.Setenv("ANTIGRAVITY_WF_BACKUP_DIR", legacyDir)
	if got := xiassPatcherBackupDir(); got != filepath.Clean(legacyDir) {
		t.Fatalf("backup directory = %q, want %q", got, filepath.Clean(legacyDir))
	}
}
