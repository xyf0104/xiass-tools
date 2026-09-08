//go:build !windows

package mcpconfig

import (
	"os"
	"path/filepath"
)

func replaceFileAtomic(source, destination string) error {
	if err := os.Rename(source, destination); err != nil {
		return ErrOperation
	}
	directory, err := os.Open(filepath.Dir(destination))
	if err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return nil
}
