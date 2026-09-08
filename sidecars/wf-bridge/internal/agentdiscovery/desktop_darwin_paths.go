package agentdiscovery

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"

	"antigravity-wf-assistant/internal/agent"
)

type desktopSpec struct {
	id                 agent.ID
	displayName        string
	bundleName         string
	bundleNames        []string
	bundleIdentifiers  []string
	fallbackExecutable string
	dataNames          []string
}

var (
	cursorSpec = desktopSpec{
		id:                 agent.CursorID,
		displayName:        "Cursor",
		bundleName:         "Cursor.app",
		bundleNames:        []string{"Cursor.app", "Cursor Nightly.app"},
		bundleIdentifiers:  []string{"com.todesktop.230313mzl4w4u92", "co.anysphere.cursor.nightly"},
		fallbackExecutable: "Cursor",
		dataNames:          []string{"Cursor"},
	}
	windsurfSpec = desktopSpec{
		id:                 agent.WindsurfID,
		displayName:        "Windsurf",
		bundleName:         "Windsurf.app",
		bundleNames:        []string{"Windsurf.app"},
		bundleIdentifiers:  []string{"com.exafunction.windsurf"},
		fallbackExecutable: "Windsurf",
		dataNames:          []string{"Windsurf"},
	}
)

func joinClaudeConfigPath(home string) string {
	return filepath.Join(home, ".claude")
}

// knownClaudeCodeCLICandidates deliberately stays short and exact. These are
// the standard native-installer and user-level npm prefix locations used on
// macOS; we never walk a package directory or execute a fallback candidate.
func knownClaudeCodeCLICandidates(filesystem FileSystem) []string {
	candidates := []string{
		"/opt/homebrew/bin/claude",
		"/usr/local/bin/claude",
	}
	home, err := filesystem.UserHomeDir()
	if err == nil && strings.TrimSpace(home) != "" {
		candidates = append(candidates,
			filepath.Join(home, ".local", "bin", "claude"),
			filepath.Join(home, ".npm-global", "bin", "claude"),
		)
	}
	return uniqueNonEmpty(candidates)
}

func pathDirectory(path string) string {
	return filepath.Dir(path)
}

func (adapter *Adapter) detectDesktop(spec desktopSpec) agent.Status {
	filesystem := adapter.fileSystem()
	candidates, environmentIssue := macDesktopCandidates(filesystem, spec)
	installRoot, executablePath, installIssue := findMacInstallation(filesystem, candidates.appBundles, spec)
	dataPath, dataIssue := findFirstDirectory(filesystem, candidates.dataDirectories)

	issues := make([]string, 0, 3)
	if environmentIssue != nil {
		issues = append(issues, environmentIssue.Error())
	}
	if installIssue != nil {
		issues = append(issues, "an application bundle could not be inspected")
	}
	if dataIssue != nil {
		issues = append(issues, "a local data directory could not be inspected")
	}
	if installRoot != "" && executablePath == "" {
		issues = append(issues, "the application bundle executable is missing")
	}

	installFound := installRoot != ""
	dataFound := dataPath != ""
	if !installFound && !dataFound {
		if len(issues) > 0 {
			return adapter.newStatus(agent.StateDegraded, spec.displayName+" discovery is incomplete: "+strings.Join(issues, "; ")+".")
		}
		return adapter.newStatus(agent.StateNotInstalled, spec.displayName+" was not found in supported application or local-data locations.")
	}

	version := ""
	if executablePath != "" {
		// Version metadata is optional and comes only from the public app bundle.
		version, _ = adapter.versionReader()(filesystem, executablePath)
	}

	state := agent.StateDetected
	message := spec.displayName + " was partially detected."
	if installFound && executablePath != "" && dataFound {
		state = agent.StateReady
		message = spec.displayName + " application bundle and local data directory were found."
	}
	if len(issues) > 0 {
		state = agent.StateDegraded
		message = spec.displayName + " was found, but discovery is incomplete: " + strings.Join(issues, "; ") + "."
	}

	status := adapter.newStatus(state, message)
	status.Installation.Root = installRoot
	status.Installation.ExecutablePath = executablePath
	status.Installation.Version = version
	status.Installation.Platform = "macos"
	if !installFound {
		status.Installation.Root = dataPath
	}
	if dataFound {
		status.Details = map[string]string{"dataDirectory": dataPath}
	}
	return status
}

type desktopCandidates struct {
	appBundles      []string
	dataDirectories []string
}

func macDesktopCandidates(filesystem FileSystem, spec desktopSpec) (desktopCandidates, error) {
	home, homeErr := filesystem.UserHomeDir()
	candidates := desktopCandidates{appBundles: []string{filepath.Join("/Applications", spec.bundleName)}}
	if strings.TrimSpace(home) != "" {
		candidates.appBundles = append(candidates.appBundles, filepath.Join(home, "Applications", spec.bundleName))
		for _, dataName := range spec.dataNames {
			candidates.dataDirectories = append(candidates.dataDirectories,
				filepath.Join(home, "Library", "Application Support", dataName),
			)
		}
	}
	candidates.appBundles = uniqueNonEmpty(candidates.appBundles)
	candidates.dataDirectories = uniqueNonEmpty(candidates.dataDirectories)
	if strings.TrimSpace(home) == "" || homeErr != nil {
		return candidates, errors.New("the macOS user home directory could not be resolved")
	}
	return candidates, nil
}

func findMacInstallation(filesystem FileSystem, bundles []string, spec desktopSpec) (string, string, error) {
	var firstErr error
	var incompleteBundle string
	for _, bundle := range bundles {
		present, err := existingDirectory(filesystem, bundle)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if !present {
			continue
		}
		executable, executableErr := macBundleExecutable(filesystem, bundle, spec.fallbackExecutable)
		if executable != "" {
			return bundle, executable, nil
		}
		if incompleteBundle == "" {
			incompleteBundle = bundle
		}
		if executableErr != nil && firstErr == nil {
			firstErr = executableErr
		}
	}
	if incompleteBundle != "" {
		return incompleteBundle, "", firstErr
	}
	return "", "", firstErr
}

func macBundleExecutable(filesystem FileSystem, bundle, fallback string) (string, error) {
	infoPath := filepath.Join(bundle, "Contents", "Info.plist")
	name := ""
	data, readErr := filesystem.ReadFile(infoPath)
	if readErr == nil {
		name = plistString(data, "CFBundleExecutable")
	}
	names := uniqueNonEmpty([]string{name, fallback})
	var firstErr error
	if readErr != nil && !errors.Is(readErr, fs.ErrNotExist) {
		firstErr = readErr
	}
	for _, candidate := range names {
		path := filepath.Join(bundle, "Contents", "MacOS", candidate)
		present, err := existingRegularFile(filesystem, path)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if present {
			return path, nil
		}
	}
	return "", firstErr
}

func findFirstDirectory(filesystem FileSystem, candidates []string) (string, error) {
	var firstErr error
	for _, candidate := range candidates {
		present, err := existingDirectory(filesystem, candidate)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if present {
			return candidate, nil
		}
	}
	return "", firstErr
}

// validatePlatformDesktopSelection accepts only a real Cursor/Windsurf app
// package returned by the native chooser. It reads the public Info.plist and
// declared executable inside that package; it never inspects user settings,
// account data, workspaces, or a client process.
func validatePlatformDesktopSelection(filesystem FileSystem, spec desktopSpec, selectedPath string) (DesktopSelection, error) {
	bundle := macDesktopBundleFromSelection(selectedPath)
	if bundle == "" || !filepath.IsAbs(bundle) || !matchesMacDesktopBundleName(spec, filepath.Base(bundle)) {
		return DesktopSelection{}, ErrDesktopSelectionRejected
	}
	present, err := existingDirectory(filesystem, bundle)
	if err != nil || !present {
		return DesktopSelection{}, ErrDesktopSelectionRejected
	}
	data, err := filesystem.ReadFile(filepath.Join(bundle, "Contents", "Info.plist"))
	if err != nil || len(data) == 0 || len(data) > 1<<20 {
		return DesktopSelection{}, ErrDesktopSelectionRejected
	}
	identifier := plistString(data, "CFBundleIdentifier")
	if !matchesMacDesktopBundleIdentifier(spec, identifier) {
		return DesktopSelection{}, ErrDesktopSelectionRejected
	}
	executableName := plistString(data, "CFBundleExecutable")
	if !strings.EqualFold(strings.TrimSpace(executableName), strings.TrimSpace(spec.fallbackExecutable)) {
		return DesktopSelection{}, ErrDesktopSelectionRejected
	}
	executable := filepath.Join(bundle, "Contents", "MacOS", executableName)
	present, err = existingRegularFile(filesystem, executable)
	if err != nil || !present {
		return DesktopSelection{}, ErrDesktopSelectionRejected
	}
	version := parseVersion(plistString(data, "CFBundleShortVersionString"))
	if version == "" {
		version = parseVersion(plistString(data, "CFBundleVersion"))
	}
	return DesktopSelection{
		agentID:      spec.id,
		root:         bundle,
		executable:   executable,
		launchTarget: bundle,
		version:      version,
	}, nil
}

func macDesktopBundleFromSelection(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsRune(value, '\x00') {
		return ""
	}
	current := filepath.Clean(value)
	for {
		if strings.EqualFold(filepath.Ext(current), ".app") {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			return ""
		}
		current = parent
	}
}

func matchesMacDesktopBundleName(spec desktopSpec, value string) bool {
	for _, allowed := range spec.bundleNames {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(allowed)) {
			return true
		}
	}
	return false
}

func matchesMacDesktopBundleIdentifier(spec desktopSpec, value string) bool {
	for _, allowed := range spec.bundleIdentifiers {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(allowed)) {
			return true
		}
	}
	return false
}

func uniqueNonEmpty(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = filepath.Clean(strings.TrimSpace(value))
		if value == "" || value == "." {
			continue
		}
		key := strings.ToLower(value)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	return result
}

// readDesktopProductVersion reads CFBundleShortVersionString first and falls
// back to CFBundleVersion. It does not inspect framework package metadata or
// any file outside the selected application bundle.
func readDesktopProductVersion(filesystem FileSystem, executablePath string) (string, error) {
	appPath := filepath.Dir(filepath.Dir(filepath.Dir(executablePath)))
	data, err := filesystem.ReadFile(filepath.Join(appPath, "Contents", "Info.plist"))
	if err != nil {
		return "", err
	}
	if version := parseVersion(plistString(data, "CFBundleShortVersionString")); version != "" {
		return version, nil
	}
	return parseVersion(plistString(data, "CFBundleVersion")), nil
}

func plistString(data []byte, wanted string) string {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	for {
		token, err := decoder.Token()
		if err != nil {
			return ""
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "key" {
			continue
		}
		var key string
		if err := decoder.DecodeElement(&key, &start); err != nil || strings.TrimSpace(key) != wanted {
			continue
		}
		for {
			next, err := decoder.Token()
			if err != nil {
				return ""
			}
			valueStart, ok := next.(xml.StartElement)
			if !ok {
				continue
			}
			if valueStart.Name.Local != "string" {
				_ = decoder.Skip()
				return ""
			}
			var value string
			if err := decoder.DecodeElement(&value, &valueStart); err != nil {
				return ""
			}
			return strings.TrimSpace(value)
		}
	}
}
