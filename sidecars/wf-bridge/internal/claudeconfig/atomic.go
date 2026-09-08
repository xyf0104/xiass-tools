package claudeconfig

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

func writeFileAtomic(path string, data []byte, mode fs.FileMode) (err error) {
	directory := filepath.Dir(path)
	if err := ensureDirectoryNoSymlink(directory); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".xiass-tools-claude-settings-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}()
	// A temporary file can contain a credential before it is atomically moved
	// into place. Set its private access control before writing any bytes. On
	// Windows this is a verified protected DACL, not a best-effort POSIX mode.
	if err := preparePrivateFile(temporaryPath, secureMode(mode)); err != nil {
		return err
	}
	if written, err := temporary.Write(data); err != nil || written != len(data) {
		if err != nil {
			return err
		}
		return io.ErrShortWrite
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := replaceFileAtomic(temporaryPath, path); err != nil {
		return fmt.Errorf("replace Claude user settings atomically: %w", err)
	}
	// MoveFileExW normally preserves the temporary file security descriptor,
	// but verify the destination and repair it on Windows before reporting a
	// successful write. A filesystem without enforceable ACLs therefore fails
	// closed instead of retaining a credential-bearing file with inherited ACLs.
	if err := finalizePrivateFile(path, secureMode(mode)); err != nil {
		return err
	}
	return nil
}
