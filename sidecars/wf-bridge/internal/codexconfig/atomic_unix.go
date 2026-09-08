//go:build !windows

package codexconfig

import (
	"io/fs"
	"os"
	"path/filepath"
)

// macOS and other Unix systems retain the package's established chmod-based
// file behavior. The Windows implementation supplies verified NTFS ACLs.
func preparePrivateFile(path string, mode fs.FileMode) error {
	return os.Chmod(path, secureMode(mode))
}

func finalizePrivateFile(string, fs.FileMode) error {
	return nil
}

func protectPrivateDirectory(string) error {
	return nil
}

func replaceFileAtomic(source, destination string) error {
	if err := os.Rename(source, destination); err != nil {
		return err
	}
	// Directory sync makes the rename durable where the filesystem supports it.
	directory, err := os.Open(filepath.Dir(destination))
	if err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return nil
}
