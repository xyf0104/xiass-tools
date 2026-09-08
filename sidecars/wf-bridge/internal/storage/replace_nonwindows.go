//go:build !windows

package storage

import "os"

func replaceStorageFile(source, target string) error {
	return os.Rename(source, target)
}
