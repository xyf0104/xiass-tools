//go:build darwin

package updater

import "os/exec"

// LaunchInstaller opens the verified PKG in the native Installer application.
// macOS keeps the authorization step visible to the user; WF never attempts a
// privileged silent install.
func LaunchInstaller(path string) error {
	return exec.Command("open", path).Start()
}
