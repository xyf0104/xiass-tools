package patcher

import (
	"os"
	"path/filepath"
	"strings"

	"antigravity-wf-assistant/internal/storage"
)

// xiassPatcherStorageDir is the one source of truth for non-secret patcher
// state. The app initialises storage before any patch action, so upgrades use
// ~/.xiass-tools after its transactional legacy migration. The environment
// fallbacks keep standalone diagnostics and isolated tests deterministic.
func xiassPatcherStorageDir() string {
	if dir := strings.TrimSpace(storage.StorageDir()); dir != "" {
		return filepath.Clean(dir)
	}
	if dir := strings.TrimSpace(os.Getenv("XIASS_TOOLS_STATE_DIR")); dir != "" {
		return filepath.Clean(dir)
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Join(home, ".xiass-tools")
}

// xiassPatcherBackupDir preserves the old testing overrides as compatibility
// inputs, while production writes use the migrated XIASS Tools state tree.
// Old directory names are never generated as new default destinations.
func xiassPatcherBackupDir() string {
	for _, key := range []string{
		"XIASS_TOOLS_BACKUP_DIR",
		"ANTIGRAVITY_WF_BACKUP_DIR",
		"ANTIGRAVITY_BYOK_BACKUP_DIR",
	} {
		if dir := strings.TrimSpace(os.Getenv(key)); dir != "" {
			return filepath.Clean(dir)
		}
	}
	if root := xiassPatcherStorageDir(); root != "" {
		return filepath.Join(root, "backups")
	}
	return ""
}

func xiassPatcherInstallStatePath(name string) string {
	if root := xiassPatcherStorageDir(); root != "" {
		return filepath.Join(root, name)
	}
	return ""
}
