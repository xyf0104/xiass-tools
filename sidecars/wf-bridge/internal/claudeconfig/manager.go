package claudeconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	backupDirectoryName = "claude-settings-backups"
	lockFilename        = "claude-settings.operation.lock"
)

// NewManager constructs a manager for one explicitly selected Claude config
// directory. Use NewDefaultManager in production to follow CLAUDE_CONFIG_DIR
// or the user's ~/.claude directory.
func NewManager(configDir string) *Manager {
	return NewManagerWithOptions(configDir, ManagerOptions{})
}

// NewManagerWithOptions permits an application-owned backup root. It does not
// accept arbitrary target filenames: settings.json is always the sole target.
func NewManagerWithOptions(configDir string, options ManagerOptions) *Manager {
	configDir = canonicalPathOrEmpty(configDir)
	backupRoot := canonicalPathOrEmpty(options.BackupRoot)
	if options.BackupRoot == "" && configDir != "" {
		backupRoot = filepath.Join(configDir, ".xiass-tools", backupDirectoryName)
	}
	legacyRoots := legacyRootsForConfigDir(configDir, backupRoot, options.LegacyBackupRoots)
	return &Manager{
		ConfigDir:         configDir,
		SettingsPath:      filepath.Join(configDir, settingsFilename),
		BackupRoot:        backupRoot,
		LockPath:          filepath.Join(backupRoot, lockFilename),
		legacyBackupRoots: legacyRoots,
	}
}

// NewDefaultManager resolves the documented Claude Code user configuration
// directory. Backup data is kept in an application-owned user-config location,
// not in a Claude credential or session path.
func NewDefaultManager() (*Manager, error) {
	configDir, err := DefaultConfigDir()
	if err != nil {
		return nil, err
	}
	backupRoot, err := defaultBackupRoot()
	if err != nil {
		return nil, err
	}
	return NewManagerWithOptions(configDir, ManagerOptions{BackupRoot: backupRoot, LegacyBackupRoots: defaultLegacyBackupRoots()}), nil
}

// DefaultConfigDir returns CLAUDE_CONFIG_DIR when explicitly configured, or
// ~/.claude otherwise. Relative configured paths are made absolute once so the
// working directory can never redirect a later write.
func DefaultConfigDir() (string, error) {
	if configured := os.Getenv("CLAUDE_CONFIG_DIR"); configured != "" {
		if strings.TrimSpace(configured) == "" || containsControl(configured) {
			return "", errUnsafePath
		}
		resolved := canonicalPathOrEmpty(configured)
		if resolved == "" {
			return "", errUnsafePath
		}
		return resolved, nil
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" || containsControl(home) {
		return "", errors.New("could not determine the Claude Code user home directory")
	}
	resolved := canonicalPathOrEmpty(filepath.Join(home, ".claude"))
	if resolved == "" {
		return "", errUnsafePath
	}
	return resolved, nil
}

func defaultBackupRoot() (string, error) {
	root, err := os.UserConfigDir()
	if err != nil || strings.TrimSpace(root) == "" || containsControl(root) {
		return "", errors.New("could not determine the application user configuration directory")
	}
	resolved := canonicalPathOrEmpty(filepath.Join(root, "XIASS Tools", backupDirectoryName))
	if resolved == "" {
		return "", errUnsafePath
	}
	return resolved, nil
}

func defaultLegacyBackupRoots() []string {
	root, err := os.UserConfigDir()
	if err != nil || strings.TrimSpace(root) == "" || containsControl(root) {
		return nil
	}
	legacy := canonicalPathOrEmpty(filepath.Join(root, "Antigravity-WF-Assistant", backupDirectoryName))
	if legacy == "" {
		return nil
	}
	return []string{legacy}
}

func legacyRootsForConfigDir(configDir, activeRoot string, additional []string) []legacyBackupRoot {
	paths := make([]string, 0, len(additional)+1)
	if configDir != "" {
		paths = append(paths, filepath.Join(configDir, ".antigravity-wf-assistant", backupDirectoryName))
	}
	paths = append(paths, additional...)
	seen := make(map[string]struct{}, len(paths))
	roots := make([]legacyBackupRoot, 0, len(paths))
	for _, path := range paths {
		path = canonicalPathOrEmpty(path)
		if path == "" || path == activeRoot {
			continue
		}
		if _, exists := seen[path]; exists {
			continue
		}
		seen[path] = struct{}{}
		roots = append(roots, legacyBackupRoot{source: fmt.Sprintf("legacy_%d", len(roots)+1), path: path})
	}
	return roots
}

// DiscoverConfig reports only the selected settings.json location and whether
// it exists. It never creates a directory or parses any other Claude file.
func DiscoverConfig(configDir string) (ConfigLocation, error) {
	manager := NewManager(configDir)
	if err := manager.validateTargetPaths(); err != nil {
		return ConfigLocation{}, err
	}
	_, exists, _, err := readRegularFile(manager.SettingsPath, maxSettingsBytes)
	if err != nil {
		return ConfigLocation{}, err
	}
	return ConfigLocation{ConfigDir: manager.ConfigDir, SettingsPath: manager.SettingsPath, Exists: exists}, nil
}

// DiscoverDefaultConfig is DiscoverConfig for CLAUDE_CONFIG_DIR or ~/.claude.
func DiscoverDefaultConfig() (ConfigLocation, error) {
	configDir, err := DefaultConfigDir()
	if err != nil {
		return ConfigLocation{}, err
	}
	return DiscoverConfig(configDir)
}

// Inspect reads only settings.json and returns a redacted snapshot. No file,
// directory, backup, lock, credential, project, or session is created/read.
func (m *Manager) Inspect() (Snapshot, error) {
	if err := m.validateTargetPaths(); err != nil {
		return Snapshot{}, err
	}
	data, exists, mode, err := readRegularFile(m.SettingsPath, maxSettingsBytes)
	location := ConfigLocation{ConfigDir: m.ConfigDir, SettingsPath: m.SettingsPath, Exists: exists}
	if err != nil {
		return Snapshot{Location: location}, err
	}
	if !exists {
		return Snapshot{Location: location, Valid: true}, nil
	}
	document, err := parseSettings(data)
	if err != nil {
		return Snapshot{Location: location, SHA256: sha256Hex(data), Mode: mode, Valid: false}, errInvalidSettings
	}
	model, baseURL, credentialMode, credentialConfigured, authConfigured, helperConfigured, discoveryEnabled, discoveryBlocked, managed := snapshotFromDocument(document)
	return Snapshot{
		Location:                     location,
		SHA256:                       sha256Hex(data),
		Mode:                         mode,
		Valid:                        true,
		Model:                        model,
		BaseURL:                      baseURL,
		CredentialMode:               credentialMode,
		CredentialConfigured:         credentialConfigured,
		AuthTokenConfigured:          authConfigured,
		APIKeyHelperConfigured:       helperConfigured,
		GatewayModelDiscoveryEnabled: discoveryEnabled,
		GatewayModelDiscoveryBlocked: discoveryBlocked,
		Managed:                      managed,
	}, nil
}

// Verify is the read-only counterpart to Apply and is useful to callers that
// need an explicit valid/invalid result after a completed operation.
func (m *Manager) Verify() (Snapshot, error) {
	snapshot, err := m.Inspect()
	if err != nil {
		return snapshot, err
	}
	if !snapshot.Valid {
		return snapshot, errInvalidSettings
	}
	return snapshot, nil
}

// Apply validates, backs up, atomically writes, reads back, and verifies the
// three explicitly managed Claude Code user settings. Existing valid JSON
// fields and unmanaged env entries are retained semantically.
func (m *Manager) Apply(input ApplyConfig) (ApplyResult, error) {
	if err := m.validatePaths(); err != nil {
		return ApplyResult{}, err
	}
	config, err := normalizeApplyConfig(input)
	if err != nil {
		return ApplyResult{}, err
	}

	var result ApplyResult
	err = m.withLock(func() error {
		if err := ensureDirectoryNoSymlink(m.ConfigDir); err != nil {
			return err
		}
		original, existed, mode, err := readRegularFile(m.SettingsPath, maxSettingsBytes)
		if err != nil {
			return err
		}
		var document settingsDocument
		if existed {
			document, err = parseSettings(original)
			if err != nil {
				return errInvalidSettings
			}
		} else {
			document = settingsDocument{root: make(map[string]json.RawMessage), env: make(map[string]json.RawMessage)}
		}
		updated, err := document.updated(config)
		if err != nil {
			return errors.New("could not prepare Claude user settings")
		}
		if err := verifyManagedSettings(updated, config); err != nil {
			return errors.New("generated Claude user settings did not verify")
		}
		backup, err := m.createBackup(original, existed, mode, "apply")
		if err != nil {
			return err
		}
		if err := ensureFileUnchanged(m.SettingsPath, original, existed); err != nil {
			return err
		}
		if err := writeFileAtomic(m.SettingsPath, updated, secureMode(mode)); err != nil {
			return m.rollbackMutation(errors.New("could not write Claude user settings"), original, existed, mode)
		}
		if m.afterAtomicWriteForTest != nil && m.afterAtomicWriteForTest() != nil {
			return m.rollbackMutation(errors.New("Claude user settings read-back verification failed"), original, existed, mode)
		}
		written, writtenExists, _, err := readRegularFile(m.SettingsPath, maxSettingsBytes)
		if err != nil || !writtenExists || !bytes.Equal(written, updated) || verifyManagedSettings(written, config) != nil {
			return m.rollbackMutation(errors.New("Claude user settings read-back verification failed"), original, existed, mode)
		}
		backup.AppliedSHA256 = sha256Hex(written)
		if err := m.writeManifest(backup); err != nil {
			return m.rollbackMutation(errors.New("could not verify Claude user settings backup metadata"), original, existed, mode)
		}
		result = ApplyResult{BackupID: backup.ID, SettingsSHA256: backup.AppliedSHA256}
		return nil
	})
	return result, err
}

// ApplyModel is the narrow companion to Apply for an account whose
// credential is owned and injected by another XIASS Tools native component.
// It never reads, rewrites, or returns a credential; it transactionally
// updates only the documented top-level model setting and preserves all env
// entries already present in settings.json.
func (m *Manager) ApplyModel(model string) (ApplyResult, error) {
	if err := m.validatePaths(); err != nil {
		return ApplyResult{}, err
	}
	model, err := normalizeModelIdentifier(model)
	if err != nil {
		return ApplyResult{}, err
	}

	var result ApplyResult
	err = m.withLock(func() error {
		if err := ensureDirectoryNoSymlink(m.ConfigDir); err != nil {
			return err
		}
		original, existed, mode, err := readRegularFile(m.SettingsPath, maxSettingsBytes)
		if err != nil {
			return err
		}
		var document settingsDocument
		if existed {
			document, err = parseSettings(original)
			if err != nil {
				return errInvalidSettings
			}
		} else {
			document = settingsDocument{root: make(map[string]json.RawMessage), env: make(map[string]json.RawMessage)}
		}
		updated, err := document.updatedModel(model)
		if err != nil {
			return errors.New("could not prepare Claude model setting")
		}
		if err := verifySelectedModel(updated, model); err != nil {
			return errors.New("generated Claude model setting did not verify")
		}
		backup, err := m.createBackup(original, existed, mode, "model_selection")
		if err != nil {
			return err
		}
		if err := ensureFileUnchanged(m.SettingsPath, original, existed); err != nil {
			return err
		}
		if err := writeFileAtomic(m.SettingsPath, updated, secureMode(mode)); err != nil {
			return m.rollbackMutation(errors.New("could not write Claude model setting"), original, existed, mode)
		}
		if m.afterAtomicWriteForTest != nil && m.afterAtomicWriteForTest() != nil {
			return m.rollbackMutation(errors.New("Claude model setting read-back verification failed"), original, existed, mode)
		}
		written, writtenExists, _, err := readRegularFile(m.SettingsPath, maxSettingsBytes)
		if err != nil || !writtenExists || !bytes.Equal(written, updated) || verifySelectedModel(written, model) != nil {
			return m.rollbackMutation(errors.New("Claude model setting read-back verification failed"), original, existed, mode)
		}
		backup.AppliedSHA256 = sha256Hex(written)
		if err := m.writeManifest(backup); err != nil {
			return m.rollbackMutation(errors.New("could not verify Claude model backup metadata"), original, existed, mode)
		}
		result = ApplyResult{BackupID: backup.ID, SettingsSHA256: backup.AppliedSHA256}
		return nil
	})
	return result, err
}

// Restore restores only a checksum-verified backup. It first creates a safety
// backup of the current settings state, then atomically writes/removes the
// target and reads it back. A tampered or foreign backup is never applied.
func (m *Manager) Restore(backupID string) (RestoreResult, error) {
	if err := m.validatePaths(); err != nil {
		return RestoreResult{}, err
	}
	if backupID != "" && !validBackupID(backupID) {
		return RestoreResult{}, errors.New("invalid Claude user settings backup ID")
	}

	var result RestoreResult
	err := m.withLock(func() error {
		if backupID == "" {
			backups, err := m.listBackups()
			if err != nil {
				return err
			}
			if len(backups) == 0 {
				return errors.New("no verified Claude user settings backup is available")
			}
			backupID = backups[0].ID
		}
		target, targetData, err := m.readVerifiedBackup(backupID)
		if err != nil {
			return err
		}
		if err := ensureDirectoryNoSymlink(m.ConfigDir); err != nil {
			return err
		}
		current, currentExists, currentMode, err := readRegularFile(m.SettingsPath, maxSettingsBytes)
		if err != nil {
			return err
		}
		safety, err := m.createBackup(current, currentExists, currentMode, "pre_restore")
		if err != nil {
			return err
		}
		if err := ensureFileUnchanged(m.SettingsPath, current, currentExists); err != nil {
			return err
		}

		if target.OriginalExisted {
			if err := writeFileAtomic(m.SettingsPath, targetData, secureMode(fs.FileMode(target.OriginalMode))); err != nil {
				return m.rollbackMutation(errors.New("could not restore Claude user settings"), current, currentExists, currentMode)
			}
			restored, restoredExists, _, err := readRegularFile(m.SettingsPath, maxSettingsBytes)
			if err != nil || !restoredExists || !bytes.Equal(restored, targetData) || sha256Hex(restored) != target.OriginalSHA256 {
				return m.rollbackMutation(errors.New("Claude user settings restore verification failed"), current, currentExists, currentMode)
			}
		} else {
			if err := os.Remove(m.SettingsPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return m.rollbackMutation(errors.New("could not remove helper-created Claude user settings"), current, currentExists, currentMode)
			}
			if _, err := os.Lstat(m.SettingsPath); !errors.Is(err, fs.ErrNotExist) {
				return m.rollbackMutation(errors.New("Claude user settings removal verification failed"), current, currentExists, currentMode)
			}
		}
		result = RestoreResult{RestoredBackupID: target.ID, SafetyBackupID: safety.ID}
		return nil
	})
	return result, err
}

// ListBackups returns only verified, redacted backup metadata, newest first.
func (m *Manager) ListBackups() ([]BackupInfo, error) {
	if err := m.validatePaths(); err != nil {
		return nil, err
	}
	return m.listBackups()
}

func (m *Manager) listBackups() ([]BackupInfo, error) {
	if err := ensureExistingPathNoSymlink(m.BackupRoot); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(m.BackupRoot)
	if errors.Is(err, fs.ErrNotExist) {
		return []BackupInfo{}, nil
	}
	if err != nil {
		return nil, errors.New("could not list Claude user settings backups")
	}
	backups := make([]BackupInfo, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !validBackupID(entry.Name()) {
			continue
		}
		manifest, _, err := m.readVerifiedBackup(entry.Name())
		if err != nil {
			continue
		}
		backups = append(backups, BackupInfo{
			ID:              manifest.ID,
			Reason:          manifest.Reason,
			CreatedAt:       manifest.CreatedAt,
			OriginalExisted: manifest.OriginalExisted,
		})
	}
	sort.Slice(backups, func(left, right int) bool { return backups[left].CreatedAt.After(backups[right].CreatedAt) })
	return backups, nil
}

// DeleteBackup removes only a verified, manager-owned backup directory. It
// refuses traversal, symlinks, unknown entries, and tampered backup data.
func (m *Manager) DeleteBackup(backupID string) error {
	if err := m.validatePaths(); err != nil {
		return err
	}
	if !validBackupID(backupID) {
		return errors.New("invalid Claude user settings backup ID")
	}
	return m.withLock(func() error {
		manifest, _, err := m.readVerifiedBackup(backupID)
		if err != nil || manifest.ID != backupID {
			return errors.New("Claude user settings backup could not be verified")
		}
		return m.removeVerifiedBackup(backupID, manifest.OriginalExisted)
	})
}

func (m *Manager) createBackup(data []byte, existed bool, mode fs.FileMode, reason string) (BackupManifest, error) {
	id, err := newBackupID()
	if err != nil {
		return BackupManifest{}, errors.New("could not create Claude user settings backup ID")
	}
	directory, err := m.backupDirectory(id)
	if err != nil {
		return BackupManifest{}, err
	}
	if err := createPrivateDirectory(directory); err != nil {
		return BackupManifest{}, errors.New("could not create Claude user settings backup")
	}
	manifest := BackupManifest{
		Version:         backupManifestVersion,
		Tool:            backupToolName,
		ID:              id,
		Reason:          reason,
		CreatedAt:       time.Now().UTC(),
		TargetSHA256:    sha256Hex([]byte(m.SettingsPath)),
		OriginalExisted: existed,
	}
	if existed {
		manifest.OriginalMode = uint32(mode.Perm())
		manifest.OriginalSHA256 = sha256Hex(data)
		if err := writeFileAtomic(filepath.Join(directory, settingsFilename), data, 0o600); err != nil {
			return BackupManifest{}, errors.New("could not write Claude user settings backup")
		}
	}
	if err := m.writeManifest(manifest); err != nil {
		return BackupManifest{}, err
	}
	return manifest, nil
}

func (m *Manager) writeManifest(manifest BackupManifest) error {
	if err := validateManifest(manifest, m.SettingsPath); err != nil {
		return err
	}
	directory, err := m.backupDirectory(manifest.ID)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return errors.New("could not encode Claude user settings backup metadata")
	}
	if err := writeFileAtomic(filepath.Join(directory, "manifest.json"), append(data, '\n'), 0o600); err != nil {
		return errors.New("could not write Claude user settings backup metadata")
	}
	return nil
}

func (m *Manager) readVerifiedBackup(backupID string) (BackupManifest, []byte, error) {
	manifest, err := m.readManifest(backupID)
	if err != nil {
		return BackupManifest{}, nil, err
	}
	if !manifest.OriginalExisted {
		path, err := m.backupFilePath(backupID, settingsFilename)
		if err != nil {
			return BackupManifest{}, nil, err
		}
		if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
			return BackupManifest{}, nil, errors.New("Claude user settings backup has unexpected data")
		}
		return manifest, nil, nil
	}
	path, err := m.backupFilePath(backupID, settingsFilename)
	if err != nil {
		return BackupManifest{}, nil, err
	}
	data, exists, _, err := readRegularFile(path, maxSettingsBytes)
	if err != nil || !exists || sha256Hex(data) != manifest.OriginalSHA256 {
		return BackupManifest{}, nil, errors.New("Claude user settings backup checksum mismatch")
	}
	if _, err := parseSettings(data); err != nil {
		return BackupManifest{}, nil, errors.New("Claude user settings backup JSON is invalid")
	}
	return manifest, data, nil
}

func (m *Manager) readManifest(backupID string) (BackupManifest, error) {
	path, err := m.backupFilePath(backupID, "manifest.json")
	if err != nil {
		return BackupManifest{}, err
	}
	data, exists, _, err := readRegularFile(path, maxManifestBytes)
	if err != nil || !exists {
		return BackupManifest{}, errors.New("Claude user settings backup manifest is unavailable")
	}
	var manifest BackupManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return BackupManifest{}, errors.New("Claude user settings backup manifest is invalid")
	}
	if err := validateManifest(manifest, m.SettingsPath); err != nil || manifest.ID != backupID {
		return BackupManifest{}, errors.New("Claude user settings backup manifest is not valid for this settings file")
	}
	return manifest, nil
}

func validateManifest(manifest BackupManifest, settingsPath string) error {
	return validateManifestForTool(manifest, settingsPath, backupToolName)
}

func validateLegacyManifest(manifest BackupManifest, settingsPath string) error {
	return validateManifestForTool(manifest, settingsPath, legacyBackupToolName)
}

func validateManifestForTool(manifest BackupManifest, settingsPath, expectedTool string) error {
	if manifest.Version != backupManifestVersion || manifest.Tool != expectedTool || !validBackupID(manifest.ID) || manifest.Reason == "" || manifest.CreatedAt.IsZero() || manifest.TargetSHA256 != sha256Hex([]byte(settingsPath)) {
		return errors.New("invalid Claude user settings backup manifest")
	}
	if manifest.OriginalExisted {
		if !validSHA256(manifest.OriginalSHA256) {
			return errors.New("invalid Claude user settings backup checksum")
		}
	} else if manifest.OriginalSHA256 != "" || manifest.OriginalMode != 0 {
		return errors.New("invalid empty Claude user settings backup")
	}
	if manifest.AppliedSHA256 != "" && !validSHA256(manifest.AppliedSHA256) {
		return errors.New("invalid applied Claude user settings checksum")
	}
	return nil
}

func (m *Manager) rollbackMutation(cause error, original []byte, existed bool, mode fs.FileMode) error {
	if rollbackErr := restoreOriginalVerified(m.SettingsPath, original, existed, mode); rollbackErr != nil {
		return &MutationError{Cause: cause, RollbackErr: rollbackErr}
	}
	return &MutationError{Cause: cause}
}

func (m *Manager) validatePaths() error {
	if err := m.validateTargetPaths(); err != nil {
		return err
	}
	if m.BackupRoot == "" || m.LockPath == "" || !filepath.IsAbs(m.BackupRoot) || !filepath.IsAbs(m.LockPath) {
		return errUnsafePath
	}
	if containsControl(m.BackupRoot) || containsControl(m.LockPath) || filepath.Clean(m.LockPath) != filepath.Join(filepath.Clean(m.BackupRoot), lockFilename) {
		return errUnsafePath
	}
	return ensureExistingPathNoSymlink(m.BackupRoot)
}

func (m *Manager) validateTargetPaths() error {
	if m == nil || m.ConfigDir == "" || m.SettingsPath == "" || !filepath.IsAbs(m.ConfigDir) || !filepath.IsAbs(m.SettingsPath) {
		return errUnsafePath
	}
	if containsControl(m.ConfigDir) || containsControl(m.SettingsPath) || filepath.Clean(m.SettingsPath) != filepath.Join(filepath.Clean(m.ConfigDir), settingsFilename) {
		return errUnsafePath
	}
	if err := ensureExistingPathNoSymlink(m.ConfigDir); err != nil {
		return err
	}
	return nil
}

func canonicalPathOrEmpty(value string) string {
	if strings.TrimSpace(value) == "" || containsControl(value) {
		return ""
	}
	abs, err := filepath.Abs(value)
	if err != nil {
		return ""
	}
	abs = filepath.Clean(abs)
	// macOS commonly exposes /var and /tmp as system symlinks. Resolve the
	// deepest existing parent before deriving manager paths, while leaving the
	// requested final component intact so an actual config-dir symlink is still
	// rejected by validatePaths/readRegularFile.
	parent := filepath.Dir(abs)
	suffix := []string{filepath.Base(abs)}
	for parent != filepath.Dir(parent) {
		if resolved, err := filepath.EvalSymlinks(parent); err == nil && filepath.IsAbs(resolved) {
			return filepath.Join(append([]string{resolved}, suffix...)...)
		}
		suffix = append([]string{filepath.Base(parent)}, suffix...)
		parent = filepath.Dir(parent)
	}
	return abs
}
