package codexdesktop

import (
	"io/fs"
	"os"
)

// systemFileSystem exposes only read-only os operations to the detector.
type systemFileSystem struct{}

func (systemFileSystem) Stat(path string) (fs.FileInfo, error) {
	return os.Stat(path)
}

func (systemFileSystem) ReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func (systemFileSystem) UserHomeDir() (string, error) {
	return os.UserHomeDir()
}

func (systemFileSystem) Getenv(key string) string {
	return os.Getenv(key)
}
