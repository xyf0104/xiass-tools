package codexconfig

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

func writeFileAtomic(path string, data []byte, mode fs.FileMode) (err error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".xiass-tools-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}()

	// The temporary file may contain the Provider token before it is atomically
	// moved into config.toml or a recovery backup. Windows establishes and
	// verifies a protected current-user DACL before any bytes are written.
	if err := preparePrivateFile(tmpPath, secureMode(mode)); err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := replaceFileAtomic(tmpPath, path); err != nil {
		return fmt.Errorf("replace file atomically: %w", err)
	}
	// Re-apply and verify on the final path. This covers a filesystem or
	// redirector that did not retain the temporary file's security descriptor.
	if err := finalizePrivateFile(path, secureMode(mode)); err != nil {
		return err
	}
	return nil
}
