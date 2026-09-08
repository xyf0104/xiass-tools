package codexconfig

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

const (
	toolDataDirectory     = "xiass-tools"
	backupManifestVersion = 1
)

// NewManager creates a manager for an explicitly selected Codex home. Calling
// code should use NewDefaultManager when it wants the user's default ~/.codex.
func NewManager(codexHome string) *Manager {
	return NewManagerWithOptions(codexHome, ManagerOptions{})
}

func NewManagerWithOptions(codexHome string, options ManagerOptions) *Manager {
	codexHome = strings.TrimSpace(codexHome)
	dataDirectoryName := strings.TrimSpace(options.DataDirectoryName)
	if dataDirectoryName == "" {
		dataDirectoryName = toolDataDirectory
	}
	// The data directory is a single path component so an untrusted UI input
	// cannot redirect backups or locks elsewhere on disk.
	if filepath.Base(dataDirectoryName) != dataDirectoryName || dataDirectoryName == "." {
		dataDirectoryName = toolDataDirectory
	}
	providerID := strings.TrimSpace(options.ProviderID)
	if providerID == "" || !providerIDPattern.MatchString(providerID) {
		providerID = DefaultProviderID
	}
	root := filepath.Join(codexHome, dataDirectoryName)
	writeGuard := options.HistoryWriteGuard
	if writeGuard == nil {
		writeGuard = defaultHistoryWriteGuard
	}
	migrationWrite := options.legacyProviderMigrationWrite
	if migrationWrite == nil {
		migrationWrite = writeFileAtomic
	}
	return &Manager{
		CodexHome:                    codexHome,
		ConfigPath:                   filepath.Join(codexHome, "config.toml"),
		BackupRoot:                   filepath.Join(root, "config-backups"),
		LockPath:                     filepath.Join(root, "codex-config.operation.lock"),
		ProviderID:                   providerID,
		historyWriteGuard:            writeGuard,
		legacyProviderMigrationWrite: migrationWrite,
	}
}

// NewDefaultManager resolves the current user's standard Codex directory.
func NewDefaultManager() (*Manager, error) {
	home, err := DefaultCodexHome()
	if err != nil {
		return nil, err
	}
	return NewManager(home), nil
}

// DefaultCodexHome returns CODEX_HOME when it is explicitly configured, or
// ~/.codex otherwise. A custom location is made absolute before any manager
// paths are derived, so a relative process working directory can never make a
// backup or a configuration write drift to a different location.
func DefaultCodexHome() (string, error) {
	if configured := strings.TrimSpace(os.Getenv("CODEX_HOME")); configured != "" {
		if strings.ContainsAny(configured, "\r\n\x00") {
			return "", errors.New("CODEX_HOME contains an invalid path")
		}
		absolute, err := filepath.Abs(configured)
		if err != nil {
			return "", fmt.Errorf("resolve CODEX_HOME: %w", err)
		}
		return filepath.Clean(absolute), nil
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return "", errors.New("could not determine the user home directory for Codex")
	}
	return filepath.Join(home, ".codex"), nil
}

// DiscoverConfig reports the safety and existence of the config at an explicit
// Codex home. It never creates files or directories.
func DiscoverConfig(codexHome string) (ConfigLocation, error) {
	manager := NewManager(codexHome)
	if strings.TrimSpace(manager.CodexHome) == "" {
		return ConfigLocation{}, errors.New("Codex home is empty")
	}
	_, exists, _, err := readRegularFile(manager.ConfigPath)
	if err != nil {
		return ConfigLocation{}, err
	}
	return ConfigLocation{CodexHome: manager.CodexHome, ConfigPath: manager.ConfigPath, Exists: exists}, nil
}

// DiscoverDefaultConfig is the no-argument counterpart for ~/.codex/config.toml.
func DiscoverDefaultConfig() (ConfigLocation, error) {
	home, err := DefaultCodexHome()
	if err != nil {
		return ConfigLocation{}, err
	}
	return DiscoverConfig(home)
}

// Apply validates, backs up, atomically writes, and reads back config.toml.
// No write begins if the original file is malformed, a symlink, or changes
// between the initial read and the final atomic replacement.
func (m *Manager) Apply(input ApplyConfig) (ApplyResult, error) {
	if err := m.validatePaths(); err != nil {
		return ApplyResult{}, err
	}
	if strings.TrimSpace(input.ProviderID) == "" {
		input.ProviderID = m.ProviderID
	}
	normalized, err := NormalizeApplyConfig(input)
	if err != nil {
		return ApplyResult{}, err
	}

	var result ApplyResult
	err = m.withLock(func() error {
		original, existed, mode, err := readRegularFile(m.ConfigPath)
		if err != nil {
			return err
		}
		if existed && len(bytes.TrimSpace(original)) > 0 {
			if err := validateTOML(original); err != nil {
				return fmt.Errorf("existing config.toml is invalid; no changes were made: %w", err)
			}
		}
		backup, err := m.createBackup(original, existed, mode, "apply")
		if err != nil {
			return fmt.Errorf("create config backup: %w", err)
		}

		updated := patchConfig(original, normalized, m.managedProviderIDs(normalized.ProviderID))
		if err := verifyManagedConfig(updated, normalized); err != nil {
			return fmt.Errorf("generated config verification failed: %w", err)
		}
		if err := ensureFileUnchanged(m.ConfigPath, original, existed); err != nil {
			return err
		}
		if err := writeFileAtomic(m.ConfigPath, updated, secureMode(mode)); err != nil {
			return rollbackMutation(fmt.Errorf("write config.toml: %w", err), m.ConfigPath, original, existed, mode)
		}

		written, writtenExists, _, err := readRegularFile(m.ConfigPath)
		if err != nil || !writtenExists {
			cause := errors.New("read back config.toml failed")
			if err != nil {
				cause = fmt.Errorf("read back config.toml: %w", err)
			}
			return rollbackMutation(cause, m.ConfigPath, original, existed, mode)
		}
		if err := verifyManagedConfig(written, normalized); err != nil {
			return rollbackMutation(fmt.Errorf("written config verification failed: %w", err), m.ConfigPath, original, existed, mode)
		}

		backup.AppliedSHA256 = sha256Hex(written)
		if err := m.writeManifest(backup); err != nil {
			return rollbackMutation(fmt.Errorf("record verified backup metadata: %w", err), m.ConfigPath, original, existed, mode)
		}
		result = ApplyResult{BackupID: backup.ID, ConfigSHA: backup.AppliedSHA256, ProviderID: normalized.ProviderID}
		return nil
	})
	return result, err
}

// Restore restores a checksum-verified original config while first saving the
// current state as a safety backup. A tampered backup never changes config.toml.
func (m *Manager) Restore(backupID string) (RestoreResult, error) {
	if err := m.validatePaths(); err != nil {
		return RestoreResult{}, err
	}
	var result RestoreResult
	err := m.withLock(func() error {
		backups, err := m.ListBackups()
		if err != nil {
			return err
		}
		if len(backups) == 0 {
			return errors.New("no XIASS Tools Codex configuration backup was found")
		}
		if strings.TrimSpace(backupID) == "" {
			backupID = backups[0].ID
		}
		if !validBackupID(backupID) {
			return errors.New("invalid backup ID")
		}
		target, err := m.readManifest(backupID)
		if err != nil {
			return err
		}
		if filepath.Clean(target.ConfigPath) != filepath.Clean(m.ConfigPath) {
			return errors.New("backup belongs to a different Codex config path")
		}

		current, currentExisted, currentMode, err := readRegularFile(m.ConfigPath)
		if err != nil {
			return err
		}
		safety, err := m.createBackup(current, currentExisted, currentMode, "pre_restore")
		if err != nil {
			return fmt.Errorf("create pre-restore safety backup: %w", err)
		}

		if target.OriginalExisted {
			backupData, err := m.readBackupOriginal(target.ID)
			if err != nil {
				return err
			}
			if sha256Hex(backupData) != target.OriginalSHA256 {
				return errors.New("backup checksum mismatch; restore cancelled")
			}
			if len(bytes.TrimSpace(backupData)) > 0 {
				if err := validateTOML(backupData); err != nil {
					return fmt.Errorf("backup TOML is invalid; restore cancelled: %w", err)
				}
			}
			if err := ensureFileUnchanged(m.ConfigPath, current, currentExisted); err != nil {
				return err
			}
			if err := writeFileAtomic(m.ConfigPath, backupData, secureMode(fs.FileMode(target.OriginalMode))); err != nil {
				return rollbackMutation(fmt.Errorf("restore config.toml: %w", err), m.ConfigPath, current, currentExisted, currentMode)
			}
			restored, restoredExisted, _, err := readRegularFile(m.ConfigPath)
			if err != nil || !restoredExisted || sha256Hex(restored) != target.OriginalSHA256 {
				cause := errors.New("restore read-back verification failed")
				if err != nil {
					cause = fmt.Errorf("restore read-back verification failed: %w", err)
				}
				return rollbackMutation(cause, m.ConfigPath, current, currentExisted, currentMode)
			}
		} else {
			if err := ensureFileUnchanged(m.ConfigPath, current, currentExisted); err != nil {
				return err
			}
			if err := os.Remove(m.ConfigPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return rollbackMutation(fmt.Errorf("remove helper-created config.toml: %w", err), m.ConfigPath, current, currentExisted, currentMode)
			}
			if _, err := os.Lstat(m.ConfigPath); !errors.Is(err, fs.ErrNotExist) {
				return rollbackMutation(errors.New("restore verification failed; config.toml still exists"), m.ConfigPath, current, currentExisted, currentMode)
			}
		}
		result = RestoreResult{RestoredBackupID: target.ID, SafetyBackupID: safety.ID}
		return nil
	})
	return result, err
}

func (m *Manager) ListBackups() ([]BackupInfo, error) {
	if err := m.validatePaths(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(m.BackupRoot)
	if errors.Is(err, fs.ErrNotExist) {
		return []BackupInfo{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list config backups: %w", err)
	}
	backups := make([]BackupInfo, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !validBackupID(entry.Name()) {
			continue
		}
		manifest, err := m.readManifest(entry.Name())
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
	sort.Slice(backups, func(i, j int) bool { return backups[i].CreatedAt.After(backups[j].CreatedAt) })
	return backups, nil
}

func (m *Manager) DeleteBackup(backupID string) error {
	if err := m.validatePaths(); err != nil {
		return err
	}
	return m.withLock(func() error {
		if !validBackupID(backupID) {
			return errors.New("invalid backup ID")
		}
		manifest, err := m.readManifest(backupID)
		if err != nil {
			return err
		}
		if filepath.Clean(manifest.ConfigPath) != filepath.Clean(m.ConfigPath) {
			return errors.New("backup belongs to a different Codex config path")
		}
		return removeBackupDirectory(m.BackupRoot, backupID)
	})
}

func (m *Manager) createBackup(data []byte, existed bool, mode fs.FileMode, reason string) (BackupManifest, error) {
	id, err := newBackupID()
	if err != nil {
		return BackupManifest{}, err
	}
	manifest := BackupManifest{
		Version:         backupManifestVersion,
		Tool:            DefaultProviderName,
		ID:              id,
		Reason:          reason,
		CreatedAt:       time.Now().UTC(),
		ConfigPath:      m.ConfigPath,
		OriginalExisted: existed,
		OriginalMode:    uint32(mode.Perm()),
	}
	if existed {
		manifest.OriginalSHA256 = sha256Hex(data)
	}
	directory := filepath.Join(m.BackupRoot, id)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return BackupManifest{}, err
	}
	// Backups may preserve an API key or provider header byte-for-byte. Restrict
	// both the app-owned root and this backup ID before any recovery file is
	// created. On Windows the platform hook sets and verifies a protected DACL.
	if err := protectPrivateDirectory(m.BackupRoot); err != nil {
		return BackupManifest{}, errors.New("could not secure Codex configuration backup storage")
	}
	if err := protectPrivateDirectory(directory); err != nil {
		return BackupManifest{}, errors.New("could not secure Codex configuration backup directory")
	}
	if existed {
		if err := writeFileAtomic(m.originalPath(id), data, 0o600); err != nil {
			return BackupManifest{}, err
		}
	}
	if err := m.writeManifest(manifest); err != nil {
		return BackupManifest{}, err
	}
	return manifest, nil
}

func (m *Manager) writeManifest(manifest BackupManifest) error {
	if !validBackupID(manifest.ID) {
		return errors.New("invalid backup manifest ID")
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(m.BackupRoot, manifest.ID, "manifest.json"), append(data, '\n'), 0o600)
}

func (m *Manager) readManifest(id string) (BackupManifest, error) {
	if !validBackupID(id) {
		return BackupManifest{}, errors.New("invalid backup ID")
	}
	data, exists, _, err := readRegularFile(filepath.Join(m.BackupRoot, id, "manifest.json"))
	if err != nil {
		return BackupManifest{}, fmt.Errorf("read backup manifest: %w", err)
	}
	if !exists {
		return BackupManifest{}, errors.New("backup manifest does not exist")
	}
	var manifest BackupManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return BackupManifest{}, fmt.Errorf("decode backup manifest: %w", err)
	}
	if manifest.Version != backupManifestVersion || manifest.ID != id || manifest.Tool != DefaultProviderName || manifest.ConfigPath == "" {
		return BackupManifest{}, errors.New("unsupported or mismatched backup manifest")
	}
	return manifest, nil
}

func (m *Manager) readBackupOriginal(id string) ([]byte, error) {
	data, exists, _, err := readRegularFile(m.originalPath(id))
	if err != nil {
		return nil, fmt.Errorf("read backup: %w", err)
	}
	if !exists {
		return nil, errors.New("backup data does not exist")
	}
	return data, nil
}

func (m *Manager) originalPath(id string) string {
	return filepath.Join(m.BackupRoot, id, "config.toml")
}

func (m *Manager) managedProviderIDs(target string) []string {
	// A normal save owns only its target and the fixed current XIASS Tools
	// entry. Predecessor IDs may contain a working, user-owned credential; they
	// are deliberately preserved until the user explicitly invokes the narrow
	// first-party migration transaction.
	return normalizedProviderIDs([]string{target, DefaultProviderID})
}

func (m *Manager) validatePaths() error {
	if strings.TrimSpace(m.CodexHome) == "" || strings.TrimSpace(m.ConfigPath) == "" || strings.TrimSpace(m.BackupRoot) == "" || strings.TrimSpace(m.LockPath) == "" {
		return errors.New("Codex configuration paths are incomplete")
	}
	return nil
}

func normalizedProviderIDs(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if !providerIDPattern.MatchString(value) {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func validBackupID(id string) bool {
	return id != "" && filepath.Base(id) == id && id != "." && id != ".." && !strings.ContainsAny(id, `/\\`) && len(id) <= 160
}

func removeBackupDirectory(root, backupID string) error {
	if !validBackupID(backupID) {
		return errors.New("invalid backup ID")
	}
	root = filepath.Clean(root)
	directory := filepath.Join(root, backupID)
	relative, err := filepath.Rel(root, directory)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return errors.New("invalid backup path")
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("backup is not a managed directory")
	}
	return os.RemoveAll(directory)
}

func readRegularFile(path string) ([]byte, bool, fs.FileMode, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, 0o600, nil
	}
	if err != nil {
		return nil, false, 0, fmt.Errorf("inspect %s: %w", filepath.Base(path), err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, false, 0, fmt.Errorf("%s is a symbolic link; no changes were made", filepath.Base(path))
	}
	if !info.Mode().IsRegular() {
		return nil, false, 0, fmt.Errorf("%s is not a regular file; no changes were made", filepath.Base(path))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false, 0, fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}
	return data, true, info.Mode().Perm(), nil
}

func ensureFileUnchanged(path string, expected []byte, expectedExisted bool) error {
	current, existed, _, err := readRegularFile(path)
	if err != nil {
		return err
	}
	if existed != expectedExisted || !bytes.Equal(current, expected) {
		return errors.New("config.toml changed during this operation; no changes were made")
	}
	return nil
}

func restoreOriginal(path string, data []byte, existed bool, mode fs.FileMode) error {
	if !existed {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	return writeFileAtomic(path, data, secureMode(mode))
}

func restoreOriginalVerified(path string, data []byte, existed bool, mode fs.FileMode) error {
	if err := restoreOriginal(path, data, existed, mode); err != nil {
		return err
	}
	if !existed {
		if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
			return nil
		} else if err != nil {
			return fmt.Errorf("verify removed config.toml: %w", err)
		}
		return errors.New("verify removed config.toml: file still exists")
	}
	restored, restoredExists, _, err := readRegularFile(path)
	if err != nil {
		return fmt.Errorf("verify restored config.toml: %w", err)
	}
	if !restoredExists || !bytes.Equal(restored, data) {
		return errors.New("verify restored config.toml: content mismatch")
	}
	return nil
}

func rollbackMutation(cause error, path string, data []byte, existed bool, mode fs.FileMode) error {
	if rollbackErr := restoreOriginalVerified(path, data, existed, mode); rollbackErr != nil {
		return &MutationError{Cause: cause, RollbackErr: rollbackErr}
	}
	return &MutationError{Cause: cause}
}

func secureMode(mode fs.FileMode) fs.FileMode {
	if mode == 0 {
		return 0o600
	}
	return mode.Perm() & 0o700
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func newBackupID() (string, error) {
	random := make([]byte, 4)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return time.Now().UTC().Format("20060102T150405.000000000Z") + "-" + hex.EncodeToString(random), nil
}

func validateTOML(data []byte) error {
	var root map[string]any
	return toml.Unmarshal(data, &root)
}
