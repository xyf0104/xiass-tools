//go:build windows

package codexdesktop

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func platformSelectionAvailable() bool { return true }

func platformDiscoverDesktopTarget(detector *Detector) *desktopTarget {
	if detector == nil {
		return nil
	}
	inspection := detector.inspectWindowsInstallation(context.Background())
	if inspection.installation == nil || inspection.inspectionUnavailable {
		return nil
	}
	target := windowsDesktopTarget(*inspection.installation)
	return &target
}

func platformValidateDesktopTarget(detector *Detector, value string) (desktopTarget, error) {
	if detector == nil {
		return desktopTarget{}, ErrSelectionRejected
	}
	root := windowsRootFromSelection(detector.fileSystem(), value)
	if root == "" {
		return desktopTarget{}, ErrSelectionRejected
	}
	installation, unavailable, invalid := inspectWindowsCandidate(
		detector.fileSystem(),
		windowsCandidate{root: root, source: SourceManualSelection},
		windowsExecutableFromSelection(value),
	)
	if installation == nil || unavailable || invalid {
		return desktopTarget{}, ErrSelectionRejected
	}
	// A manually selected Store executable must still launch through its
	// registered AppUserModelID; starting a WindowsApps image directly is not a
	// reliable or supported activation path.
	ctx, cancel := context.WithTimeout(context.Background(), defaultWindowsPackageTimeout)
	packages, packageErr := detector.windowsPackageFinder().FindPackages(ctx, expectedWindowsPackageIdentity, maxWindowsPackages)
	cancel()
	if packageErr == nil && ctx.Err() == nil {
		for _, registered := range packages {
			if !sameWindowsPath(registered.Executable, installation.executable) {
				continue
			}
			registeredInstallation, registeredUnavailable, registeredInvalid := inspectWindowsRegisteredPackage(detector.fileSystem(), registered)
			if registeredInstallation != nil && !registeredUnavailable && !registeredInvalid {
				registeredInstallation.source = SourceManualSelection
				return windowsDesktopTarget(*registeredInstallation), nil
			}
		}
	}
	if strings.Contains(strings.ToLower(installation.root), `\windowsapps\`) {
		return desktopTarget{}, ErrSelectionRejected
	}
	return windowsDesktopTarget(*installation), nil
}

func platformRevalidateDesktopTarget(detector *Detector, target desktopTarget) (desktopTarget, error) {
	if detector == nil {
		return desktopTarget{}, ErrSelectionRejected
	}
	if strings.TrimSpace(target.storePackage) != "" {
		separator := strings.LastIndex(target.launchTarget, "!")
		if separator < 0 || separator+1 >= len(target.launchTarget) {
			return desktopTarget{}, ErrSelectionRejected
		}
		applicationID := target.launchTarget[separator+1:]
		ctx, cancel := context.WithTimeout(context.Background(), defaultWindowsPackageTimeout)
		defer cancel()
		packages, err := detector.windowsPackageFinder().FindPackages(ctx, expectedWindowsPackageIdentity, maxWindowsPackages)
		if err != nil || ctx.Err() != nil {
			return desktopTarget{}, ErrSelectionRejected
		}
		for _, registered := range packages {
			if !strings.EqualFold(strings.TrimSpace(registered.PackageFamilyName), strings.TrimSpace(target.storePackage)) ||
				!strings.EqualFold(strings.TrimSpace(registered.ApplicationID), strings.TrimSpace(applicationID)) {
				continue
			}
			installation, unavailable, invalid := inspectWindowsRegisteredPackage(detector.fileSystem(), registered)
			if installation == nil || unavailable || invalid || !sameWindowsPath(installation.executable, target.executable) {
				continue
			}
			validated := windowsDesktopTarget(*installation)
			validated.installation.Source = target.installation.Source
			return validated, nil
		}
		return desktopTarget{}, ErrSelectionRejected
	}
	installation, unavailable, invalid := inspectWindowsCandidate(
		detector.fileSystem(),
		windowsCandidate{root: target.location, source: target.installation.Source},
		target.executable,
	)
	if installation == nil || unavailable || invalid {
		return desktopTarget{}, ErrSelectionRejected
	}
	validated := windowsDesktopTarget(*installation)
	validated.installation.Source = target.installation.Source
	return validated, nil
}

func windowsDesktopTarget(installation windowsInstallation) desktopTarget {
	return desktopTarget{
		location:     installation.root,
		executable:   installation.executable,
		launchTarget: installation.launchTarget,
		storePackage: installation.storePackage,
		installation: Installation{
			Present:            true,
			Source:             installation.source,
			Version:            installation.version,
			ExecutableVerified: true,
		},
	}
}

func windowsRootFromSelection(filesystem FileSystem, value string) string {
	path := safeWindowsPath(value)
	if path == "" {
		return ""
	}
	if info, err := filesystem.Stat(path); err == nil && !info.IsDir() {
		path = filepath.Dir(path)
	}
	for depth := 0; depth < 8; depth++ {
		manifest := filepath.Join(path, "AppxManifest.xml")
		if info, err := filesystem.Stat(manifest); err == nil && info.Mode().IsRegular() {
			return path
		}
		parent := filepath.Dir(path)
		if parent == path {
			break
		}
		path = parent
	}
	return ""
}

func windowsExecutableFromSelection(value string) string {
	path := safeWindowsPath(value)
	if path != "" && isSupportedWindowsExecutable(filepath.Base(path)) {
		return path
	}
	return ""
}

func platformTargetMatchesProcess(_ *Detector, target desktopTarget, process Process) bool {
	return sameWindowsPath(process.Executable, target.executable)
}

func systemControlOperations() controlOperations {
	return controlOperations{
		launch: platformLaunchDesktopTarget,
		stop:   platformRequestDesktopStop,
	}
}

func platformLaunchDesktopTarget(ctx context.Context, target desktopTarget) error {
	if strings.TrimSpace(target.executable) == "" || strings.TrimSpace(target.location) == "" {
		return ErrNoVerifiedInstallation
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var command *exec.Cmd
	if strings.TrimSpace(target.storePackage) != "" {
		if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(target.launchTarget)), `shell:appsfolder\`) {
			return ErrNoVerifiedInstallation
		}
		command = exec.CommandContext(ctx, "explorer.exe", target.launchTarget)
	} else {
		command = exec.CommandContext(ctx, target.executable)
	}
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	if err := command.Start(); err != nil {
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
	processes, err := systemProcessLister{}.List(ctx)
	if err != nil {
		return errors.New("could not safely inspect Codex Desktop processes")
	}
	var processIDs []uint32
	for _, process := range processes {
		if sameWindowsPath(process.Executable, target.executable) && process.PID != 0 {
			processIDs = append(processIDs, process.PID)
		}
	}
	if len(processIDs) == 0 {
		return nil
	}
	powershell, err := windowsPowerShellExecutable()
	if err != nil {
		return errors.New("could not request a graceful Codex Desktop exit")
	}
	ids := make([]string, 0, len(processIDs))
	for _, pid := range processIDs {
		ids = append(ids, strconv.FormatUint(uint64(pid), 10))
	}
	script := `$ErrorActionPreference='SilentlyContinue'; $sent=$false; foreach($id in @(` + strings.Join(ids, ",") + `)){ try { $p=[System.Diagnostics.Process]::GetProcessById([int]$id); if($p.CloseMainWindow()){$sent=$true} } catch {} }; if(-not $sent){exit 2}`
	command := exec.CommandContext(ctx, powershell, "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script)
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	if err := command.Run(); err != nil {
		return errors.New("could not request a graceful Codex Desktop exit")
	}
	return nil
}
