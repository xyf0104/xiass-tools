package mcpconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (m *manager) readDocument() (configurationDocument, []byte, bool, fs.FileMode, error) {
	data, exists, mode, err := readRegularFile(m.configPath, maxConfigurationBytes)
	if err != nil {
		return configurationDocument{}, data, exists, mode, err
	}
	if !exists {
		return configurationDocument{
			root:    make(map[string]json.RawMessage),
			servers: make(map[string]json.RawMessage),
		}, nil, false, mode, nil
	}
	document, err := parseConfiguration(data)
	if err != nil {
		return configurationDocument{}, data, true, mode, err
	}
	return document, data, true, mode, nil
}

func (m *manager) ensureConfigurationUnchanged(expected []byte, expectedExists bool) error {
	current, exists, _, err := readRegularFile(m.configPath, maxConfigurationBytes)
	defer zeroBytes(current)
	if err != nil || exists != expectedExists || !bytes.Equal(current, expected) {
		return ErrOperation
	}
	return nil
}

func (m *manager) createBackup(original []byte, existed bool, mode fs.FileMode, reason string) (backupManifest, error) {
	id, err := newBackupID()
	if err != nil {
		return backupManifest{}, err
	}
	directory := filepath.Join(m.backupRoot, id)
	if !pathWithin(m.backupRoot, directory) {
		return backupManifest{}, ErrUnsafeConfiguration
	}
	if err := createPrivateDirectory(directory); err != nil {
		return backupManifest{}, err
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(directory)
		}
	}()

	manifest := backupManifest{
		Version:         backupManifestVersion,
		ID:              id,
		Target:          m.target,
		Scope:           m.scope,
		ProjectID:       m.projectID,
		CreatedAt:       nowUTC(),
		Reason:          reason,
		OriginalExisted: existed,
	}
	if existed {
		manifest.OriginalMode = uint32(mode.Perm())
		manifest.OriginalSHA256 = sha256Hex(original)
		if err := writeFileAtomic(filepath.Join(directory, "configuration.json"), original, defaultMode(mode)); err != nil {
			return backupManifest{}, err
		}
	}
	if err := writeBackupManifestAt(directory, manifest); err != nil {
		return backupManifest{}, err
	}
	published = true
	return manifest, nil
}

func (m *manager) writeBackupManifest(manifest backupManifest) error {
	if manifest.ID == "" || !m.matchesBackupManifest(manifest) {
		return ErrOperation
	}
	directory := filepath.Join(m.backupRoot, manifest.ID)
	if !pathWithin(m.backupRoot, directory) {
		return ErrUnsafeConfiguration
	}
	return writeBackupManifestAt(directory, manifest)
}

func writeBackupManifestAt(directory string, manifest backupManifest) error {
	if err := validateBackupManifest(manifest); err != nil {
		return err
	}
	if err := ensureExistingPathNoSymlink(directory); err != nil {
		return err
	}
	info, err := os.Lstat(directory)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return ErrUnsafeConfiguration
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return ErrOperation
	}
	defer zeroBytes(data)
	data = append(data, '\n')
	return writeFileAtomic(filepath.Join(directory, "manifest.json"), data, 0o600)
}

func backupInfo(manifest backupManifest) BackupInfo {
	return BackupInfo{
		ID:              manifest.ID,
		CreatedAt:       manifest.CreatedAt.UTC().Format(time.RFC3339Nano),
		Reason:          manifest.Reason,
		OriginalExisted: manifest.OriginalExisted,
	}
}

func (m *manager) backupDirectory(backupID string) (string, error) {
	if !validBackupID(backupID) {
		return "", ErrOperation
	}
	directory := filepath.Join(m.backupRoot, backupID)
	if !pathWithin(m.backupRoot, directory) {
		return "", ErrUnsafeConfiguration
	}
	return directory, nil
}

// readVerifiedBackup proves that a recovery point is one created by this
// manager for this exact target, has not been tampered with, contains no
// sensitive configuration, and has no unexpected directory entries.
func (m *manager) readVerifiedBackup(backupID string) (verifiedBackup, error) {
	directory, err := m.backupDirectory(backupID)
	if err != nil {
		return verifiedBackup{}, err
	}
	manifest, err := m.readBackupManifest(backupID)
	if err != nil {
		return verifiedBackup{}, err
	}
	if !m.matchesBackupManifest(manifest) || manifest.ID != backupID {
		return verifiedBackup{}, ErrUnsafeConfiguration
	}
	if err := verifyBackupDirectory(directory, manifest); err != nil {
		return verifiedBackup{}, err
	}
	if !manifest.OriginalExisted {
		return verifiedBackup{manifest: manifest}, nil
	}
	path := filepath.Join(directory, "configuration.json")
	data, exists, _, err := readRegularFile(path, maxConfigurationBytes)
	if err != nil || !exists {
		zeroBytes(data)
		return verifiedBackup{}, ErrUnsafeConfiguration
	}
	if sha256Hex(data) != manifest.OriginalSHA256 {
		zeroBytes(data)
		return verifiedBackup{}, ErrUnsafeConfiguration
	}
	document, err := parseConfiguration(data)
	if err != nil || document.hasSensitiveConfiguration {
		zeroBytes(data)
		return verifiedBackup{}, ErrUnsafeConfiguration
	}
	return verifiedBackup{manifest: manifest, data: data}, nil
}

func (m *manager) readBackupManifest(backupID string) (backupManifest, error) {
	directory, err := m.backupDirectory(backupID)
	if err != nil {
		return backupManifest{}, err
	}
	data, exists, _, err := readRegularFile(filepath.Join(directory, "manifest.json"), maxManifestBytes)
	defer zeroBytes(data)
	if err != nil || !exists {
		return backupManifest{}, ErrUnsafeConfiguration
	}
	manifest, err := parseBackupManifest(data)
	if err != nil {
		return backupManifest{}, err
	}
	if manifest.ID != backupID || !m.matchesBackupManifest(manifest) {
		return backupManifest{}, ErrUnsafeConfiguration
	}
	return manifest, nil
}

func parseBackupManifest(data []byte) (backupManifest, error) {
	root, err := strictJSONObject(data)
	if err != nil {
		return backupManifest{}, ErrUnsafeConfiguration
	}
	allowed := map[string]bool{
		"version": true, "id": true, "target": true, "createdAt": true,
		"reason": true, "originalExisted": true, "originalMode": true,
		"originalSHA256": true, "appliedSHA256": true, "scope": true,
		"projectId": true,
	}
	for key := range root {
		if !allowed[key] {
			return backupManifest{}, ErrUnsafeConfiguration
		}
	}
	for _, required := range []string{"version", "id", "target", "createdAt", "reason", "originalExisted"} {
		if _, exists := root[required]; !exists {
			return backupManifest{}, ErrUnsafeConfiguration
		}
	}
	var manifest backupManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return backupManifest{}, ErrUnsafeConfiguration
	}
	if err := validateBackupManifest(manifest); err != nil {
		return backupManifest{}, err
	}
	return manifest, nil
}

func validateBackupManifest(manifest backupManifest) error {
	if manifest.Version != backupManifestVersion || !manifest.Target.valid() || !validBackupID(manifest.ID) || manifest.CreatedAt.IsZero() || !validBackupReason(manifest.Reason) {
		return ErrUnsafeConfiguration
	}
	// Legacy global recovery points predate the scope field, so an omitted scope
	// remains a valid global manifest. Project recovery points must always carry
	// their opaque project identity and can never be restored through a global
	// manager.
	switch manifest.Scope {
	case "", configurationScopeGlobal:
		if manifest.ProjectID != "" {
			return ErrUnsafeConfiguration
		}
	case configurationScopeProject:
		if manifest.Target != TargetCursor || !validProjectID(manifest.ProjectID) {
			return ErrUnsafeConfiguration
		}
	default:
		return ErrUnsafeConfiguration
	}
	if manifest.OriginalMode > 0o777 || (manifest.OriginalExisted && !validSHA256(manifest.OriginalSHA256)) || (!manifest.OriginalExisted && (manifest.OriginalMode != 0 || manifest.OriginalSHA256 != "")) {
		return ErrUnsafeConfiguration
	}
	if manifest.AppliedSHA256 != "" && !validSHA256(manifest.AppliedSHA256) {
		return ErrUnsafeConfiguration
	}
	return nil
}

func (m *manager) matchesBackupManifest(manifest backupManifest) bool {
	if manifest.Target != m.target {
		return false
	}
	scope := manifest.Scope
	if scope == "" {
		scope = configurationScopeGlobal
	}
	if scope != m.scope {
		return false
	}
	if scope == configurationScopeProject {
		return manifest.ProjectID != "" && manifest.ProjectID == m.projectID
	}
	return manifest.ProjectID == ""
}

func validBackupReason(reason string) bool {
	return reason == "apply" || reason == "remove" || reason == "restore"
}

func validBackupID(value string) bool {
	if len(value) < 32 || len(value) > 128 || filepath.Base(value) != value || strings.ContainsAny(value, `/\\`) {
		return false
	}
	for _, character := range value {
		if !(character >= '0' && character <= '9') && !(character >= 'a' && character <= 'f') && character != 'T' && character != 'Z' && character != '-' && character != '.' {
			return false
		}
	}
	return true
}

func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !(character >= '0' && character <= '9') && !(character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

func validProjectID(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, character := range value {
		if !(character >= '0' && character <= '9') && !(character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

func verifyBackupDirectory(directory string, manifest backupManifest) error {
	if err := ensureExistingPathNoSymlink(directory); err != nil {
		return err
	}
	info, err := os.Lstat(directory)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return ErrUnsafeConfiguration
	}
	expected := map[string]bool{"manifest.json": true}
	if manifest.OriginalExisted {
		expected["configuration.json"] = true
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return ErrUnavailable
	}
	for _, entry := range entries {
		if !expected[entry.Name()] {
			return ErrUnsafeConfiguration
		}
		info, err := entry.Info()
		if err != nil || entry.Type()&os.ModeSymlink != 0 || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return ErrUnsafeConfiguration
		}
		delete(expected, entry.Name())
	}
	if len(expected) != 0 {
		return ErrUnsafeConfiguration
	}
	return nil
}

func (m *manager) removeVerifiedBackup(backupID string) error {
	backup, err := m.readVerifiedBackup(backupID)
	if err != nil {
		return err
	}
	defer zeroBytes(backup.data)
	directory, err := m.backupDirectory(backupID)
	if err != nil {
		return err
	}
	if backup.manifest.OriginalExisted {
		if err := removeVerifiedRegularFile(filepath.Join(directory, "configuration.json")); err != nil {
			return err
		}
	}
	if err := removeVerifiedRegularFile(filepath.Join(directory, "manifest.json")); err != nil {
		return err
	}
	if err := os.Remove(directory); err != nil {
		return ErrOperation
	}
	if _, err := os.Lstat(directory); !errors.Is(err, fs.ErrNotExist) {
		return ErrOperation
	}
	return nil
}

func removeVerifiedRegularFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return ErrUnsafeConfiguration
	}
	if err := os.Remove(path); err != nil {
		return ErrOperation
	}
	if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
		return ErrOperation
	}
	return nil
}

func nowUTC() time.Time {
	return time.Now().UTC()
}

// readRegularFile follows no symlinks and verifies the opened file still
// matches the lstat result. It returns only controlled, non-path-bearing
// errors so callers cannot leak local layout in UI or diagnostics.
func readRegularFile(path string, limit int64) ([]byte, bool, fs.FileMode, error) {
	if limit <= 0 || ensureExistingPathNoSymlink(filepath.Dir(path)) != nil {
		return nil, false, 0, ErrUnsafeConfiguration
	}
	initial, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, 0o600, nil
	}
	if err != nil {
		return nil, false, 0, ErrUnavailable
	}
	if initial.Mode()&os.ModeSymlink != 0 || !initial.Mode().IsRegular() {
		return nil, false, 0, ErrUnsafeConfiguration
	}
	if initial.Size() < 0 || initial.Size() > limit {
		return nil, false, 0, ErrInvalidConfiguration
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, false, 0, ErrUnavailable
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(initial, opened) {
		return nil, false, 0, ErrUnsafeConfiguration
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(data)) > limit {
		zeroBytes(data)
		return nil, false, 0, ErrInvalidConfiguration
	}
	final, err := os.Lstat(path)
	if err != nil || final.Mode()&os.ModeSymlink != 0 || !final.Mode().IsRegular() || !os.SameFile(initial, final) {
		zeroBytes(data)
		return nil, false, 0, ErrUnsafeConfiguration
	}
	return data, true, initial.Mode().Perm(), nil
}

func ensureExistingPathNoSymlink(path string) error {
	root, components, err := absolutePathComponents(path)
	if err != nil {
		return ErrUnsafeConfiguration
	}
	current := root
	for _, component := range components {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return ErrUnsafeConfiguration
		}
	}
	return nil
}

func ensureDirectoryNoSymlink(path string) error {
	root, components, err := absolutePathComponents(path)
	if err != nil {
		return ErrUnsafeConfiguration
	}
	current := root
	for _, component := range components {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) {
			if err := os.Mkdir(current, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
				return ErrOperation
			}
			info, err = os.Lstat(current)
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return ErrUnsafeConfiguration
		}
	}
	return nil
}

func createPrivateDirectory(path string) error {
	if err := ensureDirectoryNoSymlink(filepath.Dir(path)); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return ErrUnsafeConfiguration
		}
		return ErrOperation
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return ErrUnavailable
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		return ErrOperation
	}
	info, err = os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return ErrUnsafeConfiguration
	}
	return nil
}

func absolutePathComponents(path string) (string, []string, error) {
	if path == "" || containsControl(path) {
		return "", nil, ErrUnsafeConfiguration
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", nil, ErrUnsafeConfiguration
	}
	abs = filepath.Clean(abs)
	if !filepath.IsAbs(abs) {
		return "", nil, ErrUnsafeConfiguration
	}
	volume := filepath.VolumeName(abs)
	rest := strings.TrimPrefix(abs, volume)
	separator := string(os.PathSeparator)
	if !strings.HasPrefix(rest, separator) {
		return "", nil, ErrUnsafeConfiguration
	}
	root := volume + separator
	rest = strings.TrimLeft(rest, separator)
	if rest == "" || rest == "." {
		return root, nil, nil
	}
	parts := strings.Split(rest, separator)
	components := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", nil, ErrUnsafeConfiguration
		}
		components = append(components, part)
	}
	return root, components, nil
}

func writeFileAtomic(path string, data []byte, mode fs.FileMode) error {
	directory := filepath.Dir(path)
	if err := ensureDirectoryNoSymlink(directory); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".xiass-tools-mcp-*")
	if err != nil {
		return ErrOperation
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}()
	if err := temporary.Chmod(defaultMode(mode)); err != nil {
		return ErrOperation
	}
	if written, err := temporary.Write(data); err != nil || written != len(data) {
		return ErrOperation
	}
	if err := temporary.Sync(); err != nil {
		return ErrOperation
	}
	if err := temporary.Close(); err != nil {
		return ErrOperation
	}
	if err := replaceFileAtomic(temporaryPath, path); err != nil {
		return ErrOperation
	}
	return nil
}

func restoreOriginal(path string, data []byte, existed bool, mode fs.FileMode) error {
	if existed {
		if err := writeFileAtomic(path, data, defaultMode(mode)); err != nil {
			return err
		}
		restored, restoredExists, _, err := readRegularFile(path, maxConfigurationBytes)
		defer zeroBytes(restored)
		if err != nil || !restoredExists || !bytes.Equal(restored, data) {
			return ErrOperation
		}
		return nil
	}
	if err := ensureExistingPathNoSymlink(filepath.Dir(path)); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return ErrUnsafeConfiguration
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return ErrUnavailable
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return ErrOperation
	}
	if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
		return ErrOperation
	}
	return nil
}

func normalizeRemoteEndpoint(value string) (string, error) {
	if strings.TrimSpace(value) != value || value == "" || len(value) > 2048 || containsControl(value) || strings.ContainsAny(value, "?#") {
		return "", ErrInvalidRemote
	}
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed == nil || parsed.Opaque != "" || parsed.User != nil || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", ErrInvalidRemote
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	switch parsed.Scheme {
	case "https":
		if parsed.Hostname() == "" {
			return "", ErrInvalidRemote
		}
	case "http":
		if !isLoopbackHost(parsed.Hostname()) {
			return "", ErrInvalidRemote
		}
	default:
		return "", ErrInvalidRemote
	}
	return parsed.String(), nil
}

func isLoopbackHost(host string) bool {
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	parsed := net.ParseIP(host)
	return parsed != nil && parsed.IsLoopback()
}

func rawContainsSensitiveEndpoint(raw json.RawMessage) bool {
	entry, err := strictJSONObject(raw)
	if err != nil {
		return true
	}
	for _, field := range []string{"url", "serverUrl"} {
		value, exists := entry[field]
		if !exists {
			continue
		}
		var endpoint string
		if json.Unmarshal(value, &endpoint) != nil {
			return true
		}
		parsed, err := url.Parse(endpoint)
		if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return true
		}
	}
	return false
}

func zeroBytes(data []byte) {
	for index := range data {
		data[index] = 0
	}
}
