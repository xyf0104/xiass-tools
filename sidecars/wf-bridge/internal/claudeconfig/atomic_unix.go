//go:build !windows

package claudeconfig

import (
	"io/fs"
	"os"
	"path/filepath"
)

// These hooks retain the established POSIX permission behavior. Windows has
// its own implementation because chmod does not establish a private NTFS ACL.
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
	directory, err := os.Open(filepath.Dir(destination))
	if err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return nil
}
