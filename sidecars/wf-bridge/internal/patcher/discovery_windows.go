//go:build windows

package patcher

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

func locateWindowsInstallations() []windowsTarget {
	candidates, explicit := windowsInstallCandidates()
	seenRoots := map[string]bool{}
	seenTargets := map[string]bool{}
	var targets []windowsTarget
	for _, candidate := range candidates {
		root := normalizeWindowsInstallRoot(candidate)
		key := strings.ToLower(root)
		if root == "" || seenRoots[key] {
			continue
		}
		seenRoots[key] = true
		if target, ok := inspectWindowsInstall(root); ok {
			targetKey := strings.ToLower(target.kind + "|" + target.root)
			if !seenTargets[targetKey] {
				target.version = windowsVersionFromTarget(target)
				targets = append(targets, target)
				seenTargets[targetKey] = true
			}
		}
	}
	// Explicit paths form a hard safety boundary, just like on macOS.
	if explicit && len(targets) == 0 {
		return nil
	}
	sort.SliceStable(targets, func(i, j int) bool {
		if windowsTargetRank(targets[i]) != windowsTargetRank(targets[j]) {
			return windowsTargetRank(targets[i]) < windowsTargetRank(targets[j])
		}
		return strings.ToLower(targets[i].root) < strings.ToLower(targets[j].root)
	})
	return targets
}

func windowsInstallCandidates() ([]string, bool) {
	var candidates []string
	add := func(values ...string) {
		for _, value := range values {
			if strings.TrimSpace(value) != "" {
				candidates = append(candidates, value)
			}
		}
	}
	for _, variable := range []string{"ANTIGRAVITY_APP_PATH", "ANTIGRAVITY_APP_PATHS"} {
		for _, value := range filepath.SplitList(os.Getenv(variable)) {
			add(value)
		}
	}
	if len(candidates) > 0 {
		return candidates, true
	}

	for _, base := range []string{
		os.Getenv("LOCALAPPDATA"), os.Getenv("PROGRAMFILES"), os.Getenv("ProgramFiles"),
		os.Getenv("PROGRAMFILES(X86)"), os.Getenv("ProgramW6432"),
	} {
		if base == "" {
			continue
		}
		add(
			filepath.Join(base, "Programs", "antigravity"),
			filepath.Join(base, "Programs", "Antigravity"),
			filepath.Join(base, "Programs", "Antigravity IDE"),
			filepath.Join(base, "antigravity"),
			filepath.Join(base, "Antigravity"),
			filepath.Join(base, "Antigravity IDE"),
		)
	}
	if home, err := os.UserHomeDir(); err == nil {
		add(
			filepath.Join(home, "AppData", "Local", "Programs", "antigravity"),
			filepath.Join(home, "AppData", "Local", "Programs", "Antigravity IDE"),
			filepath.Join(home, "Applications", "Antigravity"),
		)
	}

	// Cover portable/custom installations such as D:\Antigravity without
	// recursively scanning entire disks.
	for drive := 'C'; drive <= 'Z'; drive++ {
		root := fmt.Sprintf("%c:\\", drive)
		if info, err := os.Stat(root); err != nil || !info.IsDir() {
			continue
		}
		for _, relative := range []string{
			"Antigravity", "Antigravity IDE", "Apps\\Antigravity", "Apps\\Antigravity IDE",
			"Programs\\Antigravity", "Programs\\Antigravity IDE",
			"Program Files\\Antigravity", "Program Files\\Antigravity IDE",
		} {
			add(filepath.Join(root, relative))
		}
	}
	add(windowsShellDiscoveredPaths()...)
	return candidates, false
}

func windowsShellDiscoveredPaths() []string {
	path, err := exec.LookPath("powershell.exe")
	if err != nil {
		path, err = exec.LookPath("pwsh.exe")
		if err != nil {
			return nil
		}
	}
	const script = `[Console]::OutputEncoding=[Text.UTF8Encoding]::new(); $r=@(); ` +
		`Get-CimInstance Win32_Process -ErrorAction SilentlyContinue | Where-Object {$_.Name -like '*antigravity*.exe'} | ForEach-Object {$r += $_.ExecutablePath}; ` +
		`$u=@('HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\*','HKLM:\Software\Microsoft\Windows\CurrentVersion\Uninstall\*','HKLM:\Software\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall\*'); ` +
		`Get-ItemProperty $u -ErrorAction SilentlyContinue | Where-Object {$_.DisplayName -like '*Antigravity*'} | ForEach-Object {$r += $_.InstallLocation; $r += $_.DisplayIcon}; ` +
		`$r | Where-Object {$_} | Sort-Object -Unique`
	cmd := exec.Command(path, "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script)
	configureCommand(cmd)
	output, err := cmd.Output()
	if err != nil {
		return nil
	}
	var paths []string
	for _, line := range strings.Split(strings.ReplaceAll(string(output), "\r", ""), "\n") {
		if value := strings.TrimSpace(line); value != "" {
			paths = append(paths, value)
		}
	}
	return paths
}

func normalizeWindowsInstallRoot(value string) string {
	value = strings.Trim(strings.TrimSpace(value), `"`)
	if value == "" {
		return ""
	}
	if index := strings.LastIndex(strings.ToLower(value), ".exe,"); index >= 0 {
		value = value[:index+4]
	}
	value = filepath.Clean(value)
	lower := strings.ToLower(value)
	separator := string(os.PathSeparator)
	resourceToken := separator + "resources" + separator
	if index := strings.Index(lower, resourceToken); index >= 0 {
		value = value[:index]
	} else if strings.HasSuffix(lower, separator+"resources") {
		value = filepath.Dir(value)
	} else if strings.EqualFold(filepath.Ext(value), ".exe") {
		value = filepath.Dir(value)
	}
	abs, err := filepath.Abs(value)
	if err == nil {
		value = abs
	}
	return filepath.Clean(value)
}

func inspectWindowsInstall(root string) (windowsTarget, bool) {
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return windowsTarget{}, false
	}
	executable := locateWindowsExecutable(root)
	if executable == "" {
		return windowsTarget{}, false
	}
	resources := filepath.Join(root, "resources")
	unpacked := filepath.Join(resources, "app")
	mainPath := windowsExistingFile(filepath.Join(unpacked, "out", "main.js"))
	extensionRoot := filepath.Join(unpacked, "extensions", "antigravity")
	extensionEntry := windowsExistingFile(filepath.Join(extensionRoot, "dist", "extension.js"))
	language := locateWindowsLanguageServer(filepath.Join(extensionRoot, "bin"))
	if mainPath != "" && (extensionEntry != "" || language != "") {
		return windowsTarget{
			root: root, name: windowsProductName(executable, "Antigravity IDE"), kind: "ide",
			executable: executable, main: mainPath, extensionEntry: extensionEntry, language: language,
		}, true
	}

	asarPath := windowsExistingFile(filepath.Join(resources, "app.asar"))
	if asarPath == "" {
		return windowsTarget{}, false
	}
	for _, binDir := range []string{
		filepath.Join(resources, "bin"),
		filepath.Join(resources, "app.asar.unpacked", "bin"),
		filepath.Join(resources, "app", "extensions", "antigravity", "bin"),
	} {
		if language = locateWindowsLanguageServer(binDir); language != "" {
			break
		}
	}
	return windowsTarget{
		root: root, name: windowsProductName(executable, "Antigravity"), kind: "agent",
		executable: executable, asar: asarPath, language: language,
	}, true
}

func locateWindowsExecutable(root string) string {
	preferred := []string{
		filepath.Join(root, "Antigravity IDE.exe"), filepath.Join(root, "Antigravity.exe"),
		filepath.Join(root, "antigravity.exe"), filepath.Join(root, "Antigravity 2.exe"),
	}
	for _, path := range preferred {
		if windowsExistingFile(path) != "" {
			return path
		}
	}
	matches, _ := filepath.Glob(filepath.Join(root, "*.exe"))
	sort.Strings(matches)
	for _, path := range matches {
		name := strings.ToLower(filepath.Base(path))
		if strings.Contains(name, "antigravity") && !strings.Contains(name, "uninstall") &&
			!strings.Contains(name, "update") && !strings.Contains(name, "language_server") {
			return path
		}
	}
	return ""
}

func locateWindowsLanguageServer(binDir string) string {
	if binDir == "" {
		return ""
	}
	patterns := []string{"language_server_windows_x64.exe", "language_server.exe", "language_server*.exe"}
	for _, pattern := range patterns {
		matches, _ := filepath.Glob(filepath.Join(binDir, pattern))
		sort.Strings(matches)
		for _, path := range matches {
			if windowsExistingFile(path) != "" {
				return path
			}
		}
	}
	return ""
}

func windowsProductName(executable, fallback string) string {
	name := strings.TrimSuffix(filepath.Base(executable), filepath.Ext(executable))
	if strings.TrimSpace(name) == "" {
		return fallback
	}
	return name
}

func windowsTargetRank(target windowsTarget) int {
	lower := strings.ToLower(target.root)
	local := strings.ToLower(os.Getenv("LOCALAPPDATA"))
	if local != "" && strings.HasPrefix(lower, local) {
		return 0
	}
	if target.kind == "agent" {
		return 1
	}
	return 2
}
