package mcpconfig

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

func (m *manager) withLock(operation func() error) error {
	if err := ensureDirectoryNoSymlink(m.backupRoot); err != nil {
		return err
	}
	release, err := acquireOperationLock(m.lockPath)
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
		return nil, ErrUnavailable
	}
	if err == nil && (initial.Mode()&os.ModeSymlink != 0 || !initial.Mode().IsRegular()) {
		return nil, ErrUnsafeConfiguration
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, ErrUnavailable
	}
	opened, err := file.Stat()
	final, finalErr := os.Lstat(path)
	if err != nil || finalErr != nil || final.Mode()&os.ModeSymlink != 0 || !opened.Mode().IsRegular() || !final.Mode().IsRegular() || !os.SameFile(opened, final) || (initial != nil && !os.SameFile(initial, opened)) {
		_ = file.Close()
		return nil, ErrUnsafeConfiguration
	}
	if err := lockFileNonBlocking(file); err != nil {
		_ = file.Close()
		return nil, ErrOperationBusy
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
