//go:build windows

package codexdesktop

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io/fs"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	expectedWindowsPackageIdentity = "OpenAI.Codex"
	maxWindowsManifestSize         = 1 << 20
	maxWindowsPackages             = 8
	defaultWindowsPackageTimeout   = 3 * time.Second
)

var windowsVersionPattern = regexp.MustCompile(`\d+(?:\.\d+){0,3}`)

type windowsCandidate struct {
	root   string
	source Source
}

type windowsInstallation struct {
	root          string
	executable    string
	launchTarget  string
	storePackage  string
	applicationID string
	source        Source
	version       string
}

type windowsInspection struct {
	installation           *windowsInstallation
	verifiedExecutables    []string
	environmentUnavailable bool
	inspectionUnavailable  bool
	invalidInstallation    bool
}

type windowsManifest struct {
	Identity struct {
		Name    string `xml:"Name,attr"`
		Version string `xml:"Version,attr"`
	} `xml:"Identity"`
	Applications struct {
		Applications []struct {
			ID         string `xml:"Id,attr"`
			Executable string `xml:"Executable,attr"`
		} `xml:"Application"`
	} `xml:"Applications"`
}

// Discover verifies the exact OpenAI Codex package identity or a portable
// extraction of that same signed-package layout, then compares only exact
// executable paths against a bounded native process snapshot. Codex CLI and
// VS Code app-server processes are therefore not mistaken for the desktop app.
func (detector *Detector) Discover(ctx context.Context) Status {
	status := Status{CheckedAt: detector.now()}
	if ctx == nil {
		ctx = context.Background()
	}
	inspection := detector.inspectWindowsInstallation(ctx)
	if inspection.installation == nil {
		if inspection.environmentUnavailable {
			addWarning(&status, WarningEnvironmentUnavailable)
		}
		if inspection.inspectionUnavailable {
			addWarning(&status, WarningInspectionUnavailable)
		}
		if inspection.invalidInstallation {
			addWarning(&status, WarningInvalidInstallation)
		}
		if len(status.Warnings) == 0 {
			status.State = StateNotInstalled
		} else {
			status.State = StateDegraded
		}
		return status
	}

	status.Installation = Installation{
		Present:            true,
		Source:             inspection.installation.source,
		Version:            inspection.installation.version,
		ExecutableVerified: true,
	}
	// Failure to inspect Store registration means another verified Store copy
	// could be running outside the fixed portable roots. Fail closed instead of
	// declaring configuration writes safe from an incomplete observation.
	if inspection.inspectionUnavailable {
		addWarning(&status, WarningInspectionUnavailable)
		status.State = StateDegraded
		return status
	}

	processContext, cancel := context.WithTimeout(ctx, detector.processTimeout())
	defer cancel()
	processes, err := detector.processLister().List(processContext)
	if err != nil || processContext.Err() != nil {
		addWarning(&status, WarningProcessListUnavailable)
		status.State = StateDegraded
		return status
	}
	for _, process := range processes {
		if inspection.matchesExecutable(process.Executable) {
			status.Running = true
			status.State = StateRunning
			return status
		}
	}
	status.State = StateInstalled
	return status
}

func (detector *Detector) inspectWindowsInstallation(ctx context.Context) windowsInspection {
	inspection := windowsInspection{}
	packageContext, cancel := context.WithTimeout(ctx, defaultWindowsPackageTimeout)
	packages, err := detector.windowsPackageFinder().FindPackages(
		packageContext,
		expectedWindowsPackageIdentity,
		maxWindowsPackages,
	)
	cancel()
	if err != nil || packageContext.Err() != nil {
		inspection.inspectionUnavailable = true
	} else {
		for _, registered := range packages {
			installation, unavailable, invalid := inspectWindowsRegisteredPackage(detector.fileSystem(), registered)
			if installation != nil {
				inspection.addInstallation(installation)
			}
			inspection.inspectionUnavailable = inspection.inspectionUnavailable || unavailable
			inspection.invalidInstallation = inspection.invalidInstallation || invalid
		}
	}

	for _, candidate := range windowsFixedCandidates(detector.fileSystem(), &inspection) {
		installation, unavailable, invalid := inspectWindowsCandidate(detector.fileSystem(), candidate, "")
		if installation != nil {
			inspection.addInstallation(installation)
		}
		inspection.inspectionUnavailable = inspection.inspectionUnavailable || unavailable
		inspection.invalidInstallation = inspection.invalidInstallation || invalid
	}
	return inspection
}

func (detector *Detector) windowsPackageFinder() WindowsPackageFinder {
	if detector != nil && detector.options.WindowsPackages != nil {
		return detector.options.WindowsPackages
	}
	return systemWindowsPackageFinder{}
}

func windowsFixedCandidates(filesystem FileSystem, inspection *windowsInspection) []windowsCandidate {
	var candidates []windowsCandidate
	seen := map[string]struct{}{}
	add := func(root string, source Source) {
		root = safeWindowsPath(root)
		if root == "" {
			return
		}
		key := strings.ToLower(root)
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		candidates = append(candidates, windowsCandidate{root: root, source: source})
	}

	localAppData := strings.TrimSpace(filesystem.Getenv("LOCALAPPDATA"))
	if localAppData == "" {
		if home, err := filesystem.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
			localAppData = filepath.Join(home, "AppData", "Local")
		} else if inspection != nil {
			inspection.environmentUnavailable = true
		}
	}
	if localAppData != "" {
		for _, relative := range [][]string{
			{"Programs", "Codex"},
			{"Programs", "ChatGPT"},
			{"Programs", "OpenAI", "Codex"},
			{"Programs", "OpenAI", "ChatGPT"},
			{"Codex"},
		} {
			add(filepath.Join(append([]string{localAppData}, relative...)...), SourceLocalAppData)
		}
	}
	for _, environment := range []string{"ProgramFiles", "ProgramW6432", "ProgramFiles(x86)"} {
		root := strings.TrimSpace(filesystem.Getenv(environment))
		if root == "" {
			continue
		}
		for _, relative := range [][]string{
			{"Codex"},
			{"ChatGPT"},
			{"OpenAI", "Codex"},
			{"OpenAI", "ChatGPT"},
		} {
			add(filepath.Join(append([]string{root}, relative...)...), SourceProgramFiles)
		}
	}
	return candidates
}

func inspectWindowsRegisteredPackage(filesystem FileSystem, registered WindowsPackage) (*windowsInstallation, bool, bool) {
	root := safeWindowsPath(registered.Root)
	executable := safeWindowsPath(registered.Executable)
	if root == "" || executable == "" || !windowsPathWithinRoot(executable, root) ||
		strings.TrimSpace(registered.PackageFamilyName) == "" || strings.TrimSpace(registered.ApplicationID) == "" {
		return nil, false, true
	}
	installation, unavailable, invalid := inspectWindowsCandidate(
		filesystem,
		windowsCandidate{root: root, source: SourceWindowsStore},
		executable,
	)
	if installation == nil {
		return nil, unavailable, invalid
	}
	installation.storePackage = strings.TrimSpace(registered.PackageFamilyName)
	installation.applicationID = strings.TrimSpace(registered.ApplicationID)
	installation.launchTarget = `shell:AppsFolder\` + installation.storePackage + "!" + installation.applicationID
	if sanitized := sanitizeWindowsVersion(registered.Version); sanitized != "" {
		installation.version = sanitized
	}
	return installation, false, false
}

func inspectWindowsCandidate(filesystem FileSystem, candidate windowsCandidate, preferredExecutable string) (*windowsInstallation, bool, bool) {
	root := safeWindowsPath(candidate.root)
	if root == "" {
		return nil, false, true
	}
	info, err := filesystem.Stat(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, false
	}
	if err != nil {
		return nil, true, false
	}
	if !info.IsDir() {
		return nil, false, true
	}
	manifestPath := filepath.Join(root, "AppxManifest.xml")
	manifestInfo, err := filesystem.Stat(manifestPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, true
	}
	if err != nil {
		return nil, true, false
	}
	if !manifestInfo.Mode().IsRegular() || manifestInfo.Size() <= 0 || manifestInfo.Size() > maxWindowsManifestSize {
		return nil, false, true
	}
	data, err := filesystem.ReadFile(manifestPath)
	if err != nil {
		return nil, true, false
	}
	if len(data) == 0 || len(data) > maxWindowsManifestSize {
		return nil, false, true
	}
	var manifest windowsManifest
	if xml.Unmarshal(data, &manifest) != nil || !strings.EqualFold(strings.TrimSpace(manifest.Identity.Name), expectedWindowsPackageIdentity) {
		return nil, false, true
	}

	preferredExecutable = safeWindowsPath(preferredExecutable)
	for _, application := range manifest.Applications.Applications {
		relative := strings.TrimSpace(application.Executable)
		if relative == "" || strings.ContainsRune(relative, '\x00') || filepath.IsAbs(relative) {
			continue
		}
		relativePath := filepath.FromSlash(strings.ReplaceAll(relative, `\`, "/"))
		executableCandidates := []string{filepath.Join(root, relativePath)}
		if candidate.source != SourceWindowsStore {
			// XIASS Tools' verified portable installer copies the directory that
			// contains the packaged executable into the portable root while
			// retaining the original OpenAI.Codex manifest for identity proof.
			// Accept that one-level flattening only outside WindowsApps.
			executableCandidates = append(executableCandidates, filepath.Join(root, filepath.Base(relativePath)))
		}
		for _, executableCandidate := range executableCandidates {
			executable := safeWindowsPath(executableCandidate)
			if executable == "" || !windowsPathWithinRoot(executable, root) || !isSupportedWindowsExecutable(filepath.Base(executable)) {
				continue
			}
			if preferredExecutable != "" && !sameWindowsPath(executable, preferredExecutable) {
				continue
			}
			executableInfo, statErr := filesystem.Stat(executable)
			if statErr != nil {
				if !errors.Is(statErr, fs.ErrNotExist) {
					return nil, true, false
				}
				continue
			}
			if !executableInfo.Mode().IsRegular() {
				continue
			}
			return &windowsInstallation{
				root:          root,
				executable:    executable,
				applicationID: strings.TrimSpace(application.ID),
				source:        candidate.source,
				version:       sanitizeWindowsVersion(manifest.Identity.Version),
			}, false, false
		}
	}
	return nil, false, true
}

func (inspection *windowsInspection) addInstallation(installation *windowsInstallation) {
	if inspection == nil || installation == nil || strings.TrimSpace(installation.executable) == "" {
		return
	}
	if inspection.installation == nil {
		copy := *installation
		inspection.installation = &copy
	}
	for _, executable := range inspection.verifiedExecutables {
		if sameWindowsPath(executable, installation.executable) {
			return
		}
	}
	inspection.verifiedExecutables = append(inspection.verifiedExecutables, installation.executable)
}

func (inspection windowsInspection) matchesExecutable(executable string) bool {
	for _, verified := range inspection.verifiedExecutables {
		if sameWindowsPath(executable, verified) {
			return true
		}
	}
	return inspection.installation != nil && sameWindowsPath(executable, inspection.installation.executable)
}

func safeWindowsPath(value string) string {
	value = strings.Trim(strings.TrimSpace(value), `"`)
	if value == "" || strings.ContainsRune(value, '\x00') || !filepath.IsAbs(value) {
		return ""
	}
	return filepath.Clean(value)
}

func windowsPathWithinRoot(path, root string) bool {
	path = strings.ToLower(strings.TrimRight(filepath.Clean(path), `\/`))
	root = strings.ToLower(strings.TrimRight(filepath.Clean(root), `\/`))
	return path == root || strings.HasPrefix(path, root+string(filepath.Separator))
}

func sameWindowsPath(left, right string) bool {
	left = safeWindowsPath(left)
	right = safeWindowsPath(right)
	return left != "" && right != "" && strings.EqualFold(left, right)
}

func isSupportedWindowsExecutable(name string) bool {
	return strings.EqualFold(strings.TrimSpace(name), "Codex.exe") ||
		strings.EqualFold(strings.TrimSpace(name), "ChatGPT.exe")
}

func sanitizeWindowsVersion(value string) string {
	return windowsVersionPattern.FindString(strings.TrimSpace(value))
}

type systemWindowsPackageFinder struct{}

func (systemWindowsPackageFinder) FindPackages(ctx context.Context, packageIdentity string, limit int) ([]WindowsPackage, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !strings.EqualFold(strings.TrimSpace(packageIdentity), expectedWindowsPackageIdentity) {
		return nil, errors.New("unsupported package identity")
	}
	if limit <= 0 || limit > maxWindowsPackages {
		limit = maxWindowsPackages
	}
	powershell, err := windowsPowerShellExecutable()
	if err != nil {
		return nil, err
	}
	script := `$ErrorActionPreference='Stop'; ` +
		`$items=@(); ` +
		`Get-AppxPackage -Name 'OpenAI.Codex' -ErrorAction SilentlyContinue | Sort-Object Version -Descending | Select-Object -First ` + strconv.Itoa(limit) + ` | ForEach-Object { ` +
		`$pkg=$_; $manifest=Get-AppxPackageManifest $pkg; ` +
		`foreach($app in @($manifest.Package.Applications.Application)) { ` +
		`$relative=[string]$app.Executable; if($relative -match '(?i)(^|[\\/])(Codex|ChatGPT)\.exe$|^(Codex|ChatGPT)\.exe$') { ` +
		`$items += [pscustomobject]@{root=[string]$pkg.InstallLocation; executable=[string](Join-Path $pkg.InstallLocation $relative); version=[string]$pkg.Version; packageFamilyName=[string]$pkg.PackageFamilyName; applicationId=[string]$app.Id}; break } } }; ` +
		`if($items.Count -gt 0){$items | ConvertTo-Json -Compress}`
	command := exec.CommandContext(ctx, powershell, "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script)
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	output, err := command.Output()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	data := strings.TrimSpace(string(output))
	if data == "" {
		return nil, nil
	}
	type rawPackage struct {
		Root              string `json:"root"`
		Executable        string `json:"executable"`
		Version           string `json:"version"`
		PackageFamilyName string `json:"packageFamilyName"`
		ApplicationID     string `json:"applicationId"`
	}
	var raw []rawPackage
	if strings.HasPrefix(data, "[") {
		if err := json.Unmarshal([]byte(data), &raw); err != nil {
			return nil, err
		}
	} else {
		var single rawPackage
		if err := json.Unmarshal([]byte(data), &single); err != nil {
			return nil, err
		}
		raw = []rawPackage{single}
	}
	packages := make([]WindowsPackage, 0, len(raw))
	for _, item := range raw {
		packages = append(packages, WindowsPackage{
			Root:              item.Root,
			Executable:        item.Executable,
			Version:           item.Version,
			PackageFamilyName: item.PackageFamilyName,
			ApplicationID:     item.ApplicationID,
		})
	}
	sort.SliceStable(packages, func(i, j int) bool { return packages[i].Version > packages[j].Version })
	if len(packages) > limit {
		packages = packages[:limit]
	}
	return packages, nil
}

func windowsPowerShellExecutable() (string, error) {
	for _, name := range []string{"powershell.exe", "pwsh.exe"} {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	return "", errors.New("PowerShell is unavailable")
}
