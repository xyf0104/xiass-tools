package claudeconfig

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"sort"
)

const legacyBackupToolName = "Antigravity WF Assistant Claude user settings"

// ListLegacyBackups discovers only checksum-verified backups in the known
// pre-XIASS Tools locations. It is read-only and never creates, updates, or
// deletes an old directory.
func (m *Manager) ListLegacyBackups() ([]LegacyBackupInfo, error) {
	if err := m.validatePaths(); err != nil {
		return nil, err
	}
	backups := make([]LegacyBackupInfo, 0)
	for _, root := range m.legacyBackupRoots {
		if err := ensureExistingPathNoSymlink(root.path); err != nil {
			return nil, err
		}
		entries, err := os.ReadDir(root.path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, errors.New("could not list legacy Claude user settings backups")
		}
		for _, entry := range entries {
			if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !validBackupID(entry.Name()) {
				continue
			}
			manifest, _, err := m.readVerifiedLegacyBackup(root.path, entry.Name())
			if err != nil {
				continue
			}
			backups = append(backups, LegacyBackupInfo{
				Source:          root.source,
				ID:              manifest.ID,
				Reason:          manifest.Reason,
				CreatedAt:       manifest.CreatedAt,
				OriginalExisted: manifest.OriginalExisted,
			})
		}
	}
	sort.Slice(backups, func(left, right int) bool {
		if backups[left].Source == backups[right].Source {
			return backups[left].CreatedAt.After(backups[right].CreatedAt)
		}
		return backups[left].Source < backups[right].Source
	})
	return backups, nil
}

// MigrateLegacyBackup copies a verified legacy backup to the XIASS Tools
// backup root. It never deletes or rewrites the legacy source; callers may
// continue to keep the original folder as an independent recovery copy.
func (m *Manager) MigrateLegacyBackup(source, backupID string) (BackupInfo, error) {
	if err := m.validatePaths(); err != nil {
		return BackupInfo{}, err
	}
	if !validBackupID(backupID) {
		return BackupInfo{}, errors.New("invalid legacy Claude user settings backup ID")
	}
	root, found := m.legacyBackupRoot(source)
	if !found {
		return BackupInfo{}, errors.New("unknown legacy Claude user settings backup source")
	}
	var result BackupInfo
	err := m.withLock(func() error {
		manifest, data, err := m.readVerifiedLegacyBackup(root.path, backupID)
		if err != nil {
			return err
		}
		copied, err := m.createBackup(data, manifest.OriginalExisted, fs.FileMode(manifest.OriginalMode), "migrated_legacy")
		if err != nil {
			return err
		}
		result = BackupInfo{ID: copied.ID, Reason: copied.Reason, CreatedAt: copied.CreatedAt, OriginalExisted: copied.OriginalExisted}
		return nil
	})
	return result, err
}

func (m *Manager) legacyBackupRoot(source string) (legacyBackupRoot, bool) {
	for _, root := range m.legacyBackupRoots {
		if root.source == source {
			return root, true
		}
	}
	return legacyBackupRoot{}, false
}

func (m *Manager) readVerifiedLegacyBackup(root, backupID string) (BackupManifest, []byte, error) {
	manifest, err := m.readLegacyManifest(root, backupID)
	if err != nil {
		return BackupManifest{}, nil, err
	}
	if !manifest.OriginalExisted {
		path, err := backupFilePathForRoot(root, backupID, settingsFilename)
		if err != nil {
			return BackupManifest{}, nil, err
		}
		if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
			return BackupManifest{}, nil, errors.New("legacy Claude user settings backup has unexpected data")
		}
		return manifest, nil, nil
	}
	path, err := backupFilePathForRoot(root, backupID, settingsFilename)
	if err != nil {
		return BackupManifest{}, nil, err
	}
	data, exists, _, err := readRegularFile(path, maxSettingsBytes)
	if err != nil || !exists || sha256Hex(data) != manifest.OriginalSHA256 {
		return BackupManifest{}, nil, errors.New("legacy Claude user settings backup checksum mismatch")
	}
	if _, err := parseSettings(data); err != nil {
		return BackupManifest{}, nil, errors.New("legacy Claude user settings backup JSON is invalid")
	}
	return manifest, data, nil
}

func (m *Manager) readLegacyManifest(root, backupID string) (BackupManifest, error) {
	path, err := backupFilePathForRoot(root, backupID, "manifest.json")
	if err != nil {
		return BackupManifest{}, err
	}
	data, exists, _, err := readRegularFile(path, maxManifestBytes)
	if err != nil || !exists {
		return BackupManifest{}, errors.New("legacy Claude user settings backup manifest is unavailable")
	}
	var manifest BackupManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return BackupManifest{}, errors.New("legacy Claude user settings backup manifest is invalid")
	}
	if err := validateLegacyManifest(manifest, m.SettingsPath); err != nil || manifest.ID != backupID {
		return BackupManifest{}, errors.New("legacy Claude user settings backup manifest is not valid for this settings file")
	}
	return manifest, nil
}
