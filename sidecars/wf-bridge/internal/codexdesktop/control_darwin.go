//go:build darwin

package codexdesktop

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
)

func platformSelectionAvailable() bool { return true }

func platformDiscoverDesktopTarget(detector *Detector) *desktopTarget {
	if detector == nil {
		return nil
	}
	inspection := detector.inspectMacInstallation(context.Background())
	if inspection.installation == nil {
		return nil
	}
	target := macDesktopTarget(*inspection.installation)
	return &target
}

func platformValidateDesktopTarget(detector *Detector, value string) (desktopTarget, error) {
	if detector == nil {
		return desktopTarget{}, ErrSelectionRejected
	}
	bundle := macBundleFromSelection(value)
	if bundle == "" || !isSupportedMacBundleName(filepath.Base(bundle)) {
		return desktopTarget{}, ErrSelectionRejected
	}
	installation, unavailable, invalid := inspectMacCandidate(detector.fileSystem(), macCandidate{
		bundle: bundle,
		source: SourceManualSelection,
	})
	if installation == nil || unavailable || invalid {
		return desktopTarget{}, ErrSelectionRejected
	}
	return macDesktopTarget(*installation), nil
}

func platformRevalidateDesktopTarget(detector *Detector, target desktopTarget) (desktopTarget, error) {
	return platformValidateDesktopTarget(detector, target.location)
}

func macDesktopTarget(installation macInstallation) desktopTarget {
	return desktopTarget{
		location:   installation.bundle,
		executable: installation.executable,
		installation: Installation{
			Present:            true,
			Source:             installation.source,
			Version:            installation.version,
			ExecutableVerified: true,
		},
	}
}

// macBundleFromSelection accepts a package itself or a path below it returned
// by a native chooser, then keeps only the enclosing .app package. It never
// sends the original value into an error message or command line.
func macBundleFromSelection(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsRune(value, '\x00') {
		return ""
	}
	current := filepath.Clean(value)
	for {
		if strings.HasSuffix(strings.ToLower(filepath.Base(current)), ".app") {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			return ""
		}
		current = parent
	}
}

func isSupportedMacBundleName(name string) bool {
	return strings.EqualFold(strings.TrimSpace(name), "Codex.app") ||
		strings.EqualFold(strings.TrimSpace(name), "ChatGPT.app")
}

func platformTargetMatchesProcess(_ *Detector, target desktopTarget, process Process) bool {
	return sameMacPath(process.Executable, target.executable)
}

func systemControlOperations() controlOperations {
	return controlOperations{
		launch: platformLaunchDesktopTarget,
		stop:   platformRequestDesktopStop,
	}
}

func platformLaunchDesktopTarget(ctx context.Context, target desktopTarget) error {
	if strings.TrimSpace(target.location) == "" || strings.TrimSpace(target.executable) == "" {
		return ErrNoVerifiedInstallation
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := exec.CommandContext(ctx, "/usr/bin/open", target.location).Start(); err != nil {
		return errors.New("could not start verified Codex Desktop")
	}
	return nil
}

func platformRequestDesktopStop(ctx context.Context, target desktopTarget) error {
	if strings.TrimSpace(target.executable) == "" {
		return ErrNoVerifiedInstallation
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// The public bundle identifier is validated from Info.plist before this
	// command can be reached. osascript sends a normal application quit event;
	// unlike kill(2), it gives the app a chance to flush and close cleanly.
	if err := exec.CommandContext(ctx, "/usr/bin/osascript", "-e", `tell application id "com.openai.codex" to quit`).Run(); err != nil {
		return errors.New("could not request a graceful Codex Desktop exit")
	}
	return nil
}
