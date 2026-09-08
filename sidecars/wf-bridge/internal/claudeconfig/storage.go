package claudeconfig

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func ensureExistingPathNoSymlink(path string) error {
	root, components, err := absolutePathComponents(path)
	if err != nil {
		return errUnsafePath
	}
	current := root
	for _, component := range components {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return errors.New("could not inspect Claude settings path")
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errUnsafePath
		}
	}
	return nil
}

func ensureDirectoryNoSymlink(path string) error {
	root, components, err := absolutePathComponents(path)
	if err != nil {
		return errUnsafePath
	}
	current := root
	for _, component := range components {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) {
			if err := os.Mkdir(current, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
				return errors.New("could not create Claude settings directory")
			}
			info, err = os.Lstat(current)
		}
		if err != nil {
			return errors.New("could not inspect Claude settings directory")
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errUnsafePath
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
			return errUnsafePath
		}
		return errors.New("Claude user settings backup already exists")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return errors.New("could not inspect Claude user settings backup directory")
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		return errors.New("could not create Claude user settings backup directory")
	}
	info, err = os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errUnsafePath
	}
	if err := protectPrivateDirectory(path); err != nil {
		return errors.New("could not secure Claude user settings backup directory")
	}
	return nil
}

func absolutePathComponents(path string) (string, []string, error) {
	if path == "" || containsControl(path) {
		return "", nil, errUnsafePath
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", nil, err
	}
	abs = filepath.Clean(abs)
	if !filepath.IsAbs(abs) {
		return "", nil, errUnsafePath
	}
	volume := filepath.VolumeName(abs)
	rest := strings.TrimPrefix(abs, volume)
	separator := string(os.PathSeparator)
	if !strings.HasPrefix(rest, separator) {
		return "", nil, errUnsafePath
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
			return "", nil, errUnsafePath
		}
		components = append(components, part)
	}
	return root, components, nil
}

func readRegularFile(path string, limit int) ([]byte, bool, fs.FileMode, error) {
	if limit <= 0 {
		return nil, false, 0, errors.New("invalid Claude settings file limit")
	}
	if err := ensureExistingPathNoSymlink(filepath.Dir(path)); err != nil {
		return nil, false, 0, err
	}
	initial, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, 0o600, nil
	}
	if err != nil {
		return nil, false, 0, errors.New("could not inspect Claude user settings file")
	}
	if initial.Mode()&os.ModeSymlink != 0 || !initial.Mode().IsRegular() {
		return nil, false, 0, errUnsafePath
	}
	if initial.Size() < 0 || initial.Size() > int64(limit) {
		return nil, false, 0, errors.New("Claude user settings file exceeds the supported size")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, false, 0, errors.New("could not open Claude user settings file")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(initial, opened) {
		return nil, false, 0, errUnsafePath
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil || len(data) > limit {
		return nil, false, 0, errors.New("could not safely read Claude user settings file")
	}
	final, err := os.Lstat(path)
	if err != nil || final.Mode()&os.ModeSymlink != 0 || !final.Mode().IsRegular() || !os.SameFile(initial, final) {
		return nil, false, 0, errUnsafePath
	}
	return data, true, initial.Mode().Perm(), nil
}

func ensureFileUnchanged(path string, expected []byte, expectedExisted bool) error {
	current, exists, _, err := readRegularFile(path, maxSettingsBytes)
	if err != nil {
		return err
	}
	if exists != expectedExisted || !bytes.Equal(current, expected) {
		return errors.New("Claude user settings changed during this operation")
	}
	return nil
}

func restoreOriginalVerified(path string, data []byte, existed bool, mode fs.FileMode) error {
	if existed {
		if err := writeFileAtomic(path, data, secureMode(mode)); err != nil {
			return err
		}
		restored, restoredExists, _, err := readRegularFile(path, maxSettingsBytes)
		if err != nil || !restoredExists || !bytes.Equal(restored, data) {
			return errors.New("Claude user settings rollback verification failed")
		}
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return errors.New("could not remove Claude user settings during rollback")
	}
	if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
		return errors.New("Claude user settings rollback removal verification failed")
	}
	return nil
}

func secureMode(mode fs.FileMode) fs.FileMode {
	if mode == 0 {
		return 0o600
	}
	mode = mode.Perm() & 0o700
	if mode&0o600 != 0o600 {
		mode |= 0o600
	}
	return mode
}

func newBackupID() (string, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return time.Now().UTC().Format("20060102T150405.000000000Z") + "-" + hex.EncodeToString(random), nil
}

func validBackupID(id string) bool {
	if len(id) < 32 || len(id) > 128 || filepath.Base(id) != id || strings.ContainsAny(id, `/\\`) {
		return false
	}
	for _, character := range id {
		if !(character >= '0' && character <= '9') && !(character >= 'a' && character <= 'f') && character != 'T' && character != 'Z' && character != '-' && character != '.' {
			return false
		}
	}
	return true
}

func (m *Manager) backupDirectory(id string) (string, error) {
	return backupDirectoryForRoot(m.BackupRoot, id)
}

func backupDirectoryForRoot(root, id string) (string, error) {
	if !validBackupID(id) {
		return "", errors.New("invalid Claude user settings backup ID")
	}
	directory := filepath.Join(root, id)
	if !pathWithin(root, directory) {
		return "", errUnsafePath
	}
	return directory, nil
}

func (m *Manager) backupFilePath(id, filename string) (string, error) {
	return backupFilePathForRoot(m.BackupRoot, id, filename)
}

func backupFilePathForRoot(root, id, filename string) (string, error) {
	if filename != settingsFilename && filename != "manifest.json" {
		return "", errUnsafePath
	}
	directory, err := backupDirectoryForRoot(root, id)
	if err != nil {
		return "", err
	}
	path := filepath.Join(directory, filename)
	if !pathWithin(directory, path) {
		return "", errUnsafePath
	}
	return path, nil
}

func pathWithin(root, path string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) || filepath.IsAbs(relative) {
		return false
	}
	return true
}

func (m *Manager) removeVerifiedBackup(id string, originalExisted bool) error {
	directory, err := m.backupDirectory(id)
	if err != nil {
		return err
	}
	info, err := os.Lstat(directory)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("Claude user settings backup directory is unsafe")
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return errors.New("could not inspect Claude user settings backup directory")
	}
	expected := map[string]bool{"manifest.json": true}
	if originalExisted {
		expected[settingsFilename] = true
	}
	if len(entries) != len(expected) {
		return errors.New("Claude user settings backup directory has unexpected entries")
	}
	for _, entry := range entries {
		if !expected[entry.Name()] {
			return errors.New("Claude user settings backup directory has unexpected entries")
		}
		path := filepath.Join(directory, entry.Name())
		entryInfo, err := os.Lstat(path)
		if err != nil || entryInfo.Mode()&os.ModeSymlink != 0 || !entryInfo.Mode().IsRegular() {
			return errors.New("Claude user settings backup directory is unsafe")
		}
	}
	for _, filename := range []string{settingsFilename, "manifest.json"} {
		if !expected[filename] {
			continue
		}
		if err := os.Remove(filepath.Join(directory, filename)); err != nil {
			return errors.New("could not delete Claude user settings backup")
		}
	}
	if err := os.Remove(directory); err != nil {
		return errors.New("could not delete Claude user settings backup directory")
	}
	return nil
}
