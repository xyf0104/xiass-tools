//go:build !windows

package patcher

import "fmt"

func runWindows(_ string) (string, error) {
	return "", fmt.Errorf("Windows patcher is unavailable on this platform")
}

func getWindowsStatus() Status { return Status{} }
