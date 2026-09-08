package launcher

import "time"

// Supported reports whether the current platform implements safe application
// lifecycle control.
func Supported() bool {
	return platformSupported()
}

// IsRunning reports whether a process belonging to appPath is active.
func IsRunning(appPath string) (bool, error) {
	return platformIsRunning(appPath)
}

// QuitGracefully asks the application to quit through the operating system and
// waits for every process inside its bundle to exit. It never force-kills.
func QuitGracefully(appPath string, timeout time.Duration) error {
	return platformQuitGracefully(appPath, timeout)
}

// Launch opens the application bundle through the operating system.
func Launch(appPath string) error {
	return platformLaunch(appPath)
}

// WaitUntilRunning waits until the application process appears.
func WaitUntilRunning(appPath string, timeout time.Duration) error {
	return platformWaitUntilRunning(appPath, timeout)
}
