package claudeconfig

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

func (m *Manager) withLock(operation func() error) error {
	if err := ensureDirectoryNoSymlink(m.BackupRoot); err != nil {
		return err
	}
	// Backup files can contain the original settings.json, including a Claude
	// credential. Windows must establish a protected current-user DACL before
	// the operation lock or a backup file is created. POSIX keeps its existing
	// 0700 directory behavior through a no-op platform hook.
	if err := protectPrivateDirectory(m.BackupRoot); err != nil {
		return errors.New("could not secure Claude user settings backup storage")
	}
	release, err := acquireOperationLock(m.LockPath)
	if err != nil {
		return err
	}
	defer release()
	return operation()
}

func acquireOperationLock(path string) (func(), error) {
	if err := ensureDirectoryNoSymlink(filepath.Dir(path)); err != nil {
		return nil, err
	}
	initial, err := os.Lstat(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, errors.New("could not inspect Claude user settings operation lock")
	}
	if err == nil && (initial.Mode()&os.ModeSymlink != 0 || !initial.Mode().IsRegular()) {
		return nil, errUnsafePath
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, errors.New("could not open Claude user settings operation lock")
	}
	opened, err := file.Stat()
	final, finalErr := os.Lstat(path)
	if err != nil || finalErr != nil || final.Mode()&os.ModeSymlink != 0 || !opened.Mode().IsRegular() || !final.Mode().IsRegular() || !os.SameFile(opened, final) || (initial != nil && !os.SameFile(initial, opened)) {
		_ = file.Close()
		return nil, errUnsafePath
	}
	if err := lockFileNonBlocking(file); err != nil {
		_ = file.Close()
		return nil, errors.New("another Claude user settings operation is already running")
	}
	released := false
	return func() {
		if released {
			return
		}
		released = true
		_ = unlockFile(file)
		_ = file.Close()
	}, nil
}
