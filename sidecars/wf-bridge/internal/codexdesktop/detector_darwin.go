//go:build darwin

package codexdesktop

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	expectedBundleIdentifier = "com.openai.codex"
	maxInfoPlistSize         = 1 << 20
	maxSpotlightCandidates   = 32
	defaultSpotlightTimeout  = 2 * time.Second
)

var publicVersionPattern = regexp.MustCompile(`\d+(?:\.\d+){0,3}`)

type macCandidate struct {
	bundle string
	source Source
}

type macInstallation struct {
	bundle     string
	executable string
	source     Source
	version    string
}

type macInspection struct {
	installation           *macInstallation
	verifiedExecutables    []string
	environmentUnavailable bool
	inspectionUnavailable  bool
	invalidInstallation    bool
}

// Discover checks the conventional public application bundle paths and also
// performs a bounded Spotlight lookup for the exact built-in bundle identifier.
// A fixed path remains the deterministic lifecycle target, but every verified
// Spotlight result participates in the running-state check so a nonstandard
// active bundle cannot be missed. A bundle is never trusted merely because
// Spotlight found it: its public Info.plist and declared executable must pass
// the same structural checks as a fixed path.
func (detector *Detector) Discover(ctx context.Context) Status {
	status := Status{CheckedAt: detector.now()}
	if ctx == nil {
		ctx = context.Background()
	}
	inspection := detector.inspectMacInstallation(ctx)
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

	processContext, cancel := context.WithTimeout(ctx, detector.processTimeout())
	defer cancel()
	processes, err := detector.processLister().List(processContext)
	if err != nil {
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

func (detector *Detector) inspectMacInstallation(ctx context.Context) macInspection {
	return inspectMacInstallation(ctx, detector.fileSystem(), detector.bundleFinder(), detector.spotlightTimeout())
}

func (detector *Detector) bundleFinder() BundleFinder {
	if detector != nil && detector.options.BundleFinder != nil {
		return detector.options.BundleFinder
	}
	return systemBundleFinder{}
}

func (detector *Detector) spotlightTimeout() time.Duration {
	return defaultSpotlightTimeout
}

func inspectMacInstallation(ctx context.Context, filesystem FileSystem, finder BundleFinder, spotlightTimeout time.Duration) macInspection {
	inspection := macInspection{}
	candidates := []macCandidate{
		{bundle: "/Applications/Codex.app", source: SourceSystemApplications},
		{bundle: "/Applications/ChatGPT.app", source: SourceSystemApplications},
	}

	home, err := filesystem.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		inspection.environmentUnavailable = true
	} else {
		candidates = append(candidates,
			macCandidate{bundle: filepath.Join(home, "Applications", "Codex.app"), source: SourceUserApplications},
			macCandidate{bundle: filepath.Join(home, "Applications", "ChatGPT.app"), source: SourceUserApplications},
		)
	}

	for _, candidate := range candidates {
		installation, unavailable, invalid := inspectMacCandidate(filesystem, candidate)
		if installation != nil {
			inspection.addInstallation(installation)
			continue
		}
		inspection.inspectionUnavailable = inspection.inspectionUnavailable || unavailable
		inspection.invalidInstallation = inspection.invalidInstallation || invalid
	}
	spotlightCandidates, unavailable := findMacSpotlightCandidates(ctx, finder, spotlightTimeout, candidates)
	inspection.inspectionUnavailable = inspection.inspectionUnavailable || unavailable
	for _, candidate := range spotlightCandidates {
		installation, candidateUnavailable, invalid := inspectMacCandidate(filesystem, candidate)
		if installation != nil {
			// Candidates are sorted before this loop, so addInstallation keeps
			// the first one as the deterministic lifecycle target while retaining
			// every verified executable for the read-only running-state check.
			inspection.addInstallation(installation)
		}
		inspection.inspectionUnavailable = inspection.inspectionUnavailable || candidateUnavailable
		inspection.invalidInstallation = inspection.invalidInstallation || invalid
	}
	return inspection
}

func (inspection *macInspection) addInstallation(installation *macInstallation) {
	if inspection == nil || installation == nil || strings.TrimSpace(installation.executable) == "" {
		return
	}
	if inspection.installation == nil {
		copy := *installation
		inspection.installation = &copy
	}
	for _, executable := range inspection.verifiedExecutables {
		if sameMacPath(executable, installation.executable) {
			return
		}
	}
	inspection.verifiedExecutables = append(inspection.verifiedExecutables, installation.executable)
}

func (inspection macInspection) matchesExecutable(executable string) bool {
	for _, verified := range inspection.verifiedExecutables {
		if sameMacPath(executable, verified) {
			return true
		}
	}
	return inspection.installation != nil && sameMacPath(executable, inspection.installation.executable)
}

func findMacSpotlightCandidates(ctx context.Context, finder BundleFinder, timeout time.Duration, existing []macCandidate) ([]macCandidate, bool) {
	if finder == nil {
		return nil, true
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout <= 0 {
		timeout = defaultSpotlightTimeout
	}
	lookupContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	paths, err := finder.FindBundles(lookupContext, expectedBundleIdentifier, maxSpotlightCandidates)
	if err != nil || lookupContext.Err() != nil {
		return nil, true
	}

	seen := make(map[string]struct{}, len(existing)+len(paths))
	for _, candidate := range existing {
		key := macCandidatePathKey(candidate.bundle)
		if key != "" {
			seen[key] = struct{}{}
		}
	}
	validPaths := make([]string, 0, len(paths))
	for _, path := range paths {
		path = safeMacSpotlightBundlePath(path)
		if path == "" {
			continue
		}
		key := macCandidatePathKey(path)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		validPaths = append(validPaths, path)
	}
	sort.Strings(validPaths)
	if len(validPaths) > maxSpotlightCandidates {
		validPaths = validPaths[:maxSpotlightCandidates]
	}
	candidates := make([]macCandidate, 0, len(validPaths))
	for _, path := range validPaths {
		candidates = append(candidates, macCandidate{bundle: path, source: SourcePublicDiscovery})
	}
	return candidates, false
}

func safeMacSpotlightBundlePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" || strings.ContainsRune(path, '\x00') || !filepath.IsAbs(path) {
		return ""
	}
	path = filepath.Clean(path)
	if !strings.EqualFold(filepath.Ext(path), ".app") || filepath.Base(path) == "." {
		return ""
	}
	return path
}

func macCandidatePathKey(path string) string {
	path = safeMacSpotlightBundlePath(path)
	if path == "" {
		return ""
	}
	// Do not fold case here: APFS volumes may be case-sensitive, and two
	// distinct application bundles must not cause one another to be skipped.
	return path
}

func inspectMacCandidate(filesystem FileSystem, candidate macCandidate) (*macInstallation, bool, bool) {
	bundleInfo, err := filesystem.Stat(candidate.bundle)
	if err != nil {
		if isNotExist(err) {
			return nil, false, false
		}
		return nil, true, false
	}
	if !bundleInfo.IsDir() {
		return nil, false, true
	}

	infoPath := filepath.Join(candidate.bundle, "Contents", "Info.plist")
	data, err := filesystem.ReadFile(infoPath)
	if err != nil {
		if isNotExist(err) {
			return nil, false, true
		}
		return nil, true, false
	}
	if len(data) > maxInfoPlistSize {
		return nil, false, true
	}
	metadata, ok := publicPlistStrings(data)
	if !ok || metadata["CFBundleIdentifier"] != expectedBundleIdentifier {
		return nil, false, true
	}

	executableName := strings.TrimSpace(metadata["CFBundleExecutable"])
	if !safeMacExecutableName(executableName) {
		return nil, false, true
	}
	executablePath := filepath.Join(candidate.bundle, "Contents", "MacOS", executableName)
	executableInfo, err := filesystem.Stat(executablePath)
	if err != nil {
		if isNotExist(err) {
			return nil, false, true
		}
		return nil, true, false
	}
	if !executableInfo.Mode().IsRegular() || executableInfo.Mode().Perm()&0111 == 0 {
		return nil, false, true
	}

	version := publicVersion(metadata["CFBundleShortVersionString"])
	if version == "" {
		version = publicVersion(metadata["CFBundleVersion"])
	}
	return &macInstallation{
		bundle:     candidate.bundle,
		executable: executablePath,
		source:     candidate.source,
		version:    version,
	}, false, false
}

func isNotExist(err error) bool {
	return errors.Is(err, fs.ErrNotExist)
}

func safeMacExecutableName(name string) bool {
	return name != "" && name != "." && name != ".." &&
		!strings.ContainsAny(name, "/\\\x00") && filepath.Base(name) == name
}

func sameMacPath(left, right string) bool {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	if left == "" || right == "" {
		return false
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

func publicVersion(value string) string {
	return publicVersionPattern.FindString(strings.TrimSpace(value))
}

func publicPlistStrings(data []byte) (map[string]string, bool) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	values := make(map[string]string)
	pendingKey := ""
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return values, true
		}
		if err != nil {
			return nil, false
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		switch start.Name.Local {
		case "key":
			var key string
			if err := decoder.DecodeElement(&key, &start); err != nil {
				return nil, false
			}
			pendingKey = strings.TrimSpace(key)
		case "string":
			var value string
			if err := decoder.DecodeElement(&value, &start); err != nil {
				return nil, false
			}
			if pendingKey != "" {
				values[pendingKey] = strings.TrimSpace(value)
				pendingKey = ""
			}
		default:
			pendingKey = ""
		}
	}
}
