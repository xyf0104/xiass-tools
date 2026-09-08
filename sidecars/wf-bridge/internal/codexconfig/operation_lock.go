package codexconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// AcquireOperationLock obtains a non-blocking, OS-backed advisory lock. The
// contents of a pre-existing lock file never decide ownership, so a stale PID
// left by a crashed process cannot permanently block a later repair.
func AcquireOperationLock(path string) (func(), error) {
	if path == "" {
		return nil, errors.New("operation lock path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create operation lock directory: %w", err)
	}
	lock, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open operation lock: %w", err)
	}
	if err := lockFileNonBlocking(lock); err != nil {
		_ = lock.Close()
		return nil, errors.New("another Codex configuration operation is already running")
	}
	if err := lock.Truncate(0); err != nil {
		_ = unlockFile(lock)
		_ = lock.Close()
		return nil, err
	}
	if _, err := lock.Seek(0, 0); err != nil {
		_ = unlockFile(lock)
		_ = lock.Close()
		return nil, err
	}
	if _, err := lock.WriteString(fmt.Sprintf("%d\n", os.Getpid())); err != nil {
		_ = unlockFile(lock)
		_ = lock.Close()
		return nil, err
	}
	if err := lock.Sync(); err != nil {
		_ = unlockFile(lock)
		_ = lock.Close()
		return nil, err
	}

	released := false
	return func() {
		if released {
			return
		}
		released = true
		_ = unlockFile(lock)
		_ = lock.Close()
	}, nil
}

func (m *Manager) withLock(fn func() error) error {
	release, err := AcquireOperationLock(m.LockPath)
	if err != nil {
		return err
	}
	defer release()
	return fn()
}
