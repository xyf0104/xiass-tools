//go:build darwin

package patcher

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"antigravity-wf-assistant/internal/storage"
)

const (
	darwinDiscoveryCommandTimeout = 2 * time.Second
	darwinDiscoveryCacheTTL       = 2 * time.Minute
	darwinInstallStateFileName    = "antigravity-install-state.json"
)

// Discovery caches only structurally inspected bundle targets. Patch/support
// state is deliberately rebuilt by buildDarwinStatus, so a two-minute cache
// can never turn an old renderer rule into a connected result. Every reuse is
// additionally guarded by metadata fingerprints for the bundle identity,
// executable, product metadata, renderer, ASAR and Language Server files.
var darwinDiscoveryCache = struct {
	sync.Mutex
	targets         []darwinTargets
	fingerprints    map[string]string
	rootFingerprint string
	at              time.Time
	deep            bool
}{fingerprints: map[string]string{}}

// Keep the process expression deliberately narrow: the captured bundle must
// itself have an Antigravity/Agent Window-looking name.  A parent directory
// named "Antigravity" must never make an unrelated Electron app a patch
// candidate.
var runningAppPattern = regexp.MustCompile(`(?i)(/[^\n]*?(?:antigravity|agent[^\n/]*window)[^\n/]*\.app)/Contents/`)

func locateDarwinInstallations() []darwinTargets {
	return locateDarwinInstallationsCached(false, false)
}

// locateDarwinInstallationsQuick performs no process/Spotlight discovery and
// never opens app.asar. A recent, metadata-valid full discovery is preferred;
// otherwise only standard roots, saved successful paths and explicit recovery
// paths are inspected using the lightweight bundle shape validator.
func locateDarwinInstallationsQuick() []darwinTargets {
	if targets, ok := cachedDarwinInstallations(false); ok {
		return targets
	}
	candidates, explicit := darwinAppCandidates(false)
	return selectDarwinInstallationsQuick(candidates, explicit, trustedDarwinInstallLocation)
}

func refreshDarwinInstallations() []darwinTargets {
	return locateDarwinInstallationsCached(true, true)
}

func locateDarwinInstallationsCached(includeDeep, force bool) []darwinTargets {
	if !force {
		if targets, ok := cachedDarwinInstallations(includeDeep); ok {
			return targets
		}
	}
	candidates, explicit := darwinAppCandidates(includeDeep)
	targets := selectDarwinInstallations(candidates, explicit, trustedDarwinInstallLocation)
	cacheDarwinInstallations(targets, includeDeep)
	return cloneDarwinTargets(targets)
}

// selectDarwinInstallations separates candidate gathering from structural
// selection.  It is intentionally conservative: only user-supplied explicit
// paths may bypass the normal trusted-location/identity gates.  Process and
// Spotlight discoveries remain ordinary discoveries, so a stray copy cannot
// cause a standard installation to be patched as well.
func selectDarwinInstallations(candidates []string, explicit map[string]bool, isTrusted func(string) bool) []darwinTargets {
	seen := map[string]bool{}
	var targets []darwinTargets
	var deferredCustomLocations []string
	for _, candidate := range candidates {
		candidate = normalizeAppBundlePath(candidate)
		canonical := darwinCanonicalAppKey(candidate)
		if candidate == "" || seen[canonical] {
			continue
		}
		seen[canonical] = true
		if !explicit[candidate] && !isTrusted(candidate) {
			deferredCustomLocations = append(deferredCustomLocations, candidate)
			continue
		}
		if !explicit[candidate] && !darwinTrustedCandidateHasExpectedIdentity(candidate) {
			continue
		}
		if target, ok := inspectDarwinApp(candidate); ok {
			targets = append(targets, target)
		}
	}
	// Process and Spotlight results outside standard roots are accepted only
	// when both the bundle identifier and strict product structure match. Keep
	// them alongside standard installs: users can legitimately have the IDE and
	// Antigravity 2.0 on separate volumes or in versioned application folders.
	for _, candidate := range deferredCustomLocations {
		if !darwinBundleIdentifierIsAntigravity(candidate) {
			continue
		}
		if target, ok := inspectDarwinApp(candidate); ok {
			targets = append(targets, target)
		}
	}
	sort.SliceStable(targets, func(i, j int) bool {
		left, right := darwinCandidateRank(targets[i].app), darwinCandidateRank(targets[j].app)
		if left != right {
			return left < right
		}
		if targets[i].kind != targets[j].kind {
			return targets[i].kind < targets[j].kind
		}
		return targets[i].app < targets[j].app
	})
	return targets
}

// selectDarwinInstallationsQuick mirrors the identity boundary of the full
// selector but uses only file existence and plist metadata. Saved paths never
// become implicit authority: a non-standard saved bundle still has to present
// an Antigravity bundle identifier and a known IDE/Agent directory shape.
func selectDarwinInstallationsQuick(candidates []string, explicit map[string]bool, isTrusted func(string) bool) []darwinTargets {
	seen := map[string]bool{}
	targets := make([]darwinTargets, 0)
	for _, candidate := range candidates {
		candidate = normalizeAppBundlePath(candidate)
		canonical := darwinCanonicalAppKey(candidate)
		if candidate == "" || seen[canonical] {
			continue
		}
		seen[canonical] = true
		trusted := isTrusted(candidate)
		if !explicit[candidate] {
			if trusted {
				if !darwinTrustedCandidateHasExpectedIdentity(candidate) {
					continue
				}
			} else if !darwinBundleIdentifierIsAntigravity(candidate) {
				continue
			}
		}
		if target, ok := inspectDarwinAppQuick(candidate); ok {
			targets = append(targets, target)
		}
	}
	sort.SliceStable(targets, func(i, j int) bool {
		left, right := darwinCandidateRank(targets[i].app), darwinCandidateRank(targets[j].app)
		if left != right {
			return left < right
		}
		if targets[i].kind != targets[j].kind {
			return targets[i].kind < targets[j].kind
		}
		return targets[i].app < targets[j].app
	})
	return targets
}

func darwinAppCandidates(includeDeep bool) ([]string, map[string]bool) {
	var candidates []string
	explicit := map[string]bool{}
	addExplicit := func(value string) {
		for _, item := range filepath.SplitList(value) {
			if path := normalizeAppBundlePath(strings.TrimSpace(item)); path != "" {
				candidates = append(candidates, path)
				explicit[path] = true
			}
		}
	}
	addExplicit(os.Getenv("ANTIGRAVITY_APP_PATH"))
	addExplicit(os.Getenv("ANTIGRAVITY_APP_PATHS"))
	// Explicit paths are also the test and recovery boundary. If the caller
	// supplies one, never silently fall back to a different system install.
	if len(explicit) > 0 {
		return candidates, explicit
	}

	// Paths recorded only after a successful connection are the first normal
	// candidates. They are hints, not trust grants: both selectors revalidate
	// the current bundle identifier and product structure before returning one.
	candidates = append(candidates, darwinSavedInstallPaths()...)

	roots := []string{"/Applications", "/System/Applications"}
	if home, err := os.UserHomeDir(); err == nil {
		roots = append(roots, filepath.Join(home, "Applications"))
	}
	for _, root := range roots {
		candidates = append(candidates, darwinApplicationsInRoot(root)...)
	}

	if !includeDeep {
		return candidates, explicit
	}

	if output, err := darwinDiscoveryOutput("ps", "-axo", "command="); err == nil {
		for _, match := range runningAppPattern.FindAllStringSubmatch(string(output), -1) {
			if len(match) > 1 {
				path := normalizeAppBundlePath(match[1])
				if path != "" {
					// A process path is useful discovery evidence, not an explicit
					// user instruction.  Keep the same safety gates as Spotlight.
					candidates = append(candidates, path)
				}
			}
		}
	}
	if path, err := exec.LookPath("mdfind"); err == nil {
		query := `(kMDItemContentType == "com.apple.application-bundle"c) && (kMDItemFSName == "*Antigravity*.app"c || kMDItemFSName == "*Agent*Window*.app"c)`
		if output, err := darwinDiscoveryOutput(path, query); err == nil {
			for _, line := range strings.Split(string(output), "\n") {
				if line = strings.TrimSpace(line); line != "" {
					candidates = append(candidates, line)
				}
			}
		}
	}
	return candidates, explicit
}

func darwinSavedInstallPaths() []string {
	dir := strings.TrimSpace(storage.StorageDir())
	if dir == "" {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(dir, darwinInstallStateFileName))
	if err != nil {
		return nil
	}
	return darwinSavedInstallPathsFromData(data)
}

func darwinSavedInstallPathsFromData(data []byte) []string {
	var state struct {
		Schema  int `json:"schema"`
		Targets []struct {
			AppPath string `json:"appPath"`
		} `json:"targets"`
	}
	if json.Unmarshal(data, &state) != nil || state.Schema != 1 {
		return nil
	}
	paths := make([]string, 0, len(state.Targets))
	seen := make(map[string]bool, len(state.Targets))
	for _, target := range state.Targets {
		path := normalizeAppBundlePath(target.AppPath)
		key := darwinCanonicalAppKey(path)
		if path == "" || seen[key] {
			continue
		}
		seen[key] = true
		paths = append(paths, path)
	}
	return paths
}

// darwinApplicationsInRoot performs only a one-level directory read.  It
// avoids brittle Glob case patterns (which miss names such as
// "Google ANTIGRAVITY.app" on case-sensitive volumes) without recursively
// scanning a user's disk.
func darwinApplicationsInRoot(root string) []string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	paths := make([]string, 0)
	for _, entry := range entries {
		if !entry.IsDir() || !darwinAppNameLooksLikeAntigravity(entry.Name()) {
			continue
		}
		paths = append(paths, filepath.Join(root, entry.Name()))
	}
	sort.Strings(paths)
	return paths
}

func darwinAppNameLooksLikeAntigravity(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	if !strings.HasSuffix(lower, ".app") {
		return false
	}
	return strings.Contains(lower, "antigravity") ||
		(strings.Contains(lower, "agent") && strings.Contains(lower, "window"))
}

func darwinBundleIdentifierIsAntigravity(path string) bool {
	return strings.Contains(strings.ToLower(darwinBundleValue(path, "CFBundleIdentifier")), "antigravity")
}

func darwinTrustedCandidateHasExpectedIdentity(path string) bool {
	return darwinAppNameLooksLikeAntigravity(filepath.Base(path)) || darwinBundleIdentifierIsAntigravity(path)
}

func darwinDiscoveryOutput(name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), darwinDiscoveryCommandTimeout)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).Output()
}

func normalizeAppBundlePath(path string) string {
	path = strings.Trim(strings.TrimSpace(path), `"`)
	if path == "" {
		return ""
	}
	lower := strings.ToLower(path)
	if index := strings.Index(lower, ".app/"); index >= 0 {
		path = path[:index+4]
	}
	if !strings.HasSuffix(strings.ToLower(path), ".app") {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return filepath.Clean(abs)
}

func darwinCanonicalAppKey(path string) string {
	if path == "" {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(path)
}

func trustedDarwinInstallLocation(path string) bool {
	if strings.HasPrefix(path, "/Applications/") || strings.HasPrefix(path, "/System/Applications/") {
		return true
	}
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(path, filepath.Join(home, "Applications")+string(os.PathSeparator)) {
		return true
	}
	return false
}

func darwinCandidateRank(path string) int {
	if strings.HasPrefix(path, "/Applications/") {
		return 0
	}
	if strings.HasPrefix(path, "/System/Applications/") {
		return 1
	}
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(path, filepath.Join(home, "Applications")+string(os.PathSeparator)) {
		return 2
	}
	return 3
}

func cloneDarwinTargets(targets []darwinTargets) []darwinTargets {
	return append([]darwinTargets(nil), targets...)
}

func invalidateDarwinDiscoveryCache() {
	darwinDiscoveryCache.Lock()
	darwinDiscoveryCache.targets = nil
	darwinDiscoveryCache.fingerprints = map[string]string{}
	darwinDiscoveryCache.rootFingerprint = ""
	darwinDiscoveryCache.at = time.Time{}
	darwinDiscoveryCache.deep = false
	darwinDiscoveryCache.Unlock()
}

func cacheDarwinInstallations(targets []darwinTargets, deep bool) {
	fingerprints := make(map[string]string, len(targets))
	for _, target := range targets {
		fingerprints[darwinCanonicalAppKey(target.app)] = darwinTargetMetadataFingerprint(target)
	}
	darwinDiscoveryCache.Lock()
	darwinDiscoveryCache.targets = cloneDarwinTargets(targets)
	darwinDiscoveryCache.fingerprints = fingerprints
	darwinDiscoveryCache.rootFingerprint = darwinDiscoveryRootFingerprint()
	darwinDiscoveryCache.at = time.Now()
	darwinDiscoveryCache.deep = deep
	darwinDiscoveryCache.Unlock()
}

func cachedDarwinInstallations(requireDeep bool) ([]darwinTargets, bool) {
	// Explicit recovery paths are a hard, mutable caller boundary. Reinspect
	// them on every call rather than allowing a previous environment value to
	// leak through the cache.
	if strings.TrimSpace(os.Getenv("ANTIGRAVITY_APP_PATH")) != "" ||
		strings.TrimSpace(os.Getenv("ANTIGRAVITY_APP_PATHS")) != "" {
		return nil, false
	}
	darwinDiscoveryCache.Lock()
	if len(darwinDiscoveryCache.targets) == 0 || time.Since(darwinDiscoveryCache.at) >= darwinDiscoveryCacheTTL ||
		(requireDeep && !darwinDiscoveryCache.deep) {
		darwinDiscoveryCache.Unlock()
		return nil, false
	}
	targets := cloneDarwinTargets(darwinDiscoveryCache.targets)
	fingerprints := make(map[string]string, len(darwinDiscoveryCache.fingerprints))
	for key, value := range darwinDiscoveryCache.fingerprints {
		fingerprints[key] = value
	}
	rootFingerprint := darwinDiscoveryCache.rootFingerprint
	darwinDiscoveryCache.Unlock()

	if rootFingerprint != darwinDiscoveryRootFingerprint() {
		return nil, false
	}
	for index, cached := range targets {
		// Re-read bundle identity and minimum structure every time. This makes a
		// remembered path harmless after replacement by an unrelated .app while
		// avoiding the expensive ASAR/renderer content scan on the quick path.
		current, ok := inspectDarwinAppQuick(cached.app)
		if !ok || current.kind != cached.kind ||
			(!trustedDarwinInstallLocation(cached.app) && !darwinBundleIdentifierIsAntigravity(cached.app)) {
			return nil, false
		}
		key := darwinCanonicalAppKey(cached.app)
		if fingerprints[key] == "" || fingerprints[key] != darwinTargetMetadataFingerprint(current) {
			return nil, false
		}
		// Use freshly read product metadata even when the file fingerprint is
		// unchanged (for example on a restored filesystem timestamp).
		current.name = cached.name
		targets[index] = current
	}
	return targets, true
}

func darwinDiscoveryRootFingerprint() string {
	paths := []string{"/Applications", "/System/Applications"}
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, "Applications"))
	}
	if dir := strings.TrimSpace(storage.StorageDir()); dir != "" {
		paths = append(paths, filepath.Join(dir, darwinInstallStateFileName))
	}
	sort.Strings(paths)
	var fingerprint strings.Builder
	for _, path := range paths {
		darwinAppendPathMetadata(&fingerprint, path)
	}
	return fingerprint.String()
}

func darwinTargetMetadataFingerprint(target darwinTargets) string {
	paths := []string{
		filepath.Join(target.app, "Contents", "Info.plist"),
		filepath.Join(target.app, "Contents", "MacOS"),
		darwinAppExecutablePath(target.app),
		target.main, target.asar, target.extensionEntry, target.language,
		filepath.Join(target.app, "Contents", "Resources", "app", "product.json"),
		filepath.Join(target.app, "Contents", "Resources", "app", "out"),
	}
	paths = append(paths, darwinImagePreviewRendererPaths(target)...)
	// Do not open app.asar while validating the discovery cache. The archive
	// itself is fingerprinted above; optional unpacked renderers have fixed
	// relative paths and can be stat'ed directly without parsing its header.
	if target.kind == "agent" && target.asar != "" {
		for _, relative := range imagePreviewASARRelativePaths {
			paths = append(paths, filepath.Join(target.asar+".unpacked", filepath.FromSlash(relative)))
		}
	}
	sort.Strings(paths)
	var fingerprint strings.Builder
	fmt.Fprintf(&fingerprint, "kind=%s;version=%s;bundle=%s;", target.kind, darwinBundleProductVersion(target.app), darwinBundleValue(target.app, "CFBundleIdentifier"))
	for _, path := range paths {
		if strings.TrimSpace(path) != "" {
			darwinAppendPathMetadata(&fingerprint, path)
		}
	}
	return fingerprint.String()
}

func darwinAppendPathMetadata(fingerprint *strings.Builder, path string) {
	info, err := os.Stat(path)
	if err != nil {
		fmt.Fprintf(fingerprint, "%s|missing;", filepath.Clean(path))
		return
	}
	fmt.Fprintf(fingerprint, "%s|%d|%d|%d|%t;", filepath.Clean(path), info.Size(), info.ModTime().UnixNano(), info.Mode(), info.IsDir())
}

// inspectDarwinAppQuick intentionally does not read app.asar or renderer
// contents. It establishes only enough verified shape for the first dashboard
// paint and launch-target resolution; full status/refresh/apply perform the
// authoritative compatibility and connected-state checks.
func inspectDarwinAppQuick(appPath string) (darwinTargets, bool) {
	info, err := os.Stat(appPath)
	if err != nil || !info.IsDir() {
		return darwinTargets{}, false
	}
	resources := filepath.Join(appPath, "Contents", "Resources")
	unpacked := filepath.Join(resources, "app")
	mainPath := existingFile(filepath.Join(unpacked, "out", "main.js"))
	extensionRoot := filepath.Join(unpacked, "extensions", "antigravity")
	extensionEntry := existingFile(filepath.Join(extensionRoot, "dist", "extension.js"))
	languagePath := locateDarwinLanguageServer(filepath.Join(extensionRoot, "bin"))
	extensionDir := extensionRoot
	if extensionEntry == "" {
		extensionDir = ""
	}
	name := strings.TrimSuffix(filepath.Base(appPath), filepath.Ext(appPath))
	version := darwinBundleProductVersion(appPath)
	if mainPath != "" && (extensionEntry != "" || languagePath != "") {
		return darwinTargets{
			app: appPath, name: name, kind: "ide", version: version,
			main: mainPath, extension: extensionDir, extensionEntry: extensionEntry, language: languagePath,
		}, true
	}

	asarPath := existingFile(filepath.Join(resources, "app.asar"))
	if asarPath == "" {
		return darwinTargets{}, false
	}
	for _, binDir := range []string{filepath.Join(resources, "bin"), filepath.Join(resources, "app.asar.unpacked", "bin")} {
		if languagePath = locateDarwinLanguageServer(binDir); languagePath != "" {
			break
		}
	}
	return darwinTargets{app: appPath, name: name, kind: "agent", version: version, asar: asarPath, language: languagePath}, true
}

func inspectDarwinApp(appPath string) (darwinTargets, bool) {
	info, err := os.Stat(appPath)
	if err != nil || !info.IsDir() {
		return darwinTargets{}, false
	}
	resources := filepath.Join(appPath, "Contents", "Resources")
	unpacked := filepath.Join(resources, "app")
	mainPath := existingFile(filepath.Join(unpacked, "out", "main.js"))
	extensionRoot := filepath.Join(unpacked, "extensions", "antigravity")
	extensionEntry := existingFile(filepath.Join(extensionRoot, "dist", "extension.js"))
	// A few unpacked releases ship the Language Server under the extension
	// directory but no independently patchable extension.js.  Do not clear
	// extensionRoot before checking bin/, otherwise the lookup accidentally
	// falls back to a relative "bin" in the helper's current directory and
	// incorrectly reports a supported installation as missing.
	languagePath := locateDarwinLanguageServer(filepath.Join(extensionRoot, "bin"))
	extensionDir := extensionRoot
	if extensionEntry == "" {
		extensionDir = ""
	}
	// Newer IDE releases can move the Language Server into an Electron entry
	// script.  An unpacked IDE remains a supported target without a separate
	// binary only when its independently patchable extension entry is present;
	// applyDarwinPatch still verifies both source shapes before writing.
	if mainPath != "" && (extensionEntry != "" || languagePath != "") {
		return darwinTargets{
			app: appPath, name: strings.TrimSuffix(filepath.Base(appPath), filepath.Ext(appPath)),
			kind: "ide", version: darwinBundleProductVersion(appPath),
			main: mainPath, extension: extensionDir, extensionEntry: extensionEntry, language: languagePath,
		}, true
	}

	asarPath := existingFile(filepath.Join(resources, "app.asar"))
	if asarPath == "" {
		return darwinTargets{}, false
	}
	for _, binDir := range []string{filepath.Join(resources, "bin"), filepath.Join(resources, "app.asar.unpacked", "bin")} {
		if languagePath = locateDarwinLanguageServer(binDir); languagePath != "" {
			break
		}
	}
	// A packaged Agent may carry the whole launch path in app.asar.  Only
	// accept the no-binary variant when both known entries are present and the
	// launcher contains a vendor/current helper endpoint that the ASAR patcher
	// can prove it knows how to replace.  Unknown archives are deliberately not
	// surfaced as patch targets.
	if languagePath == "" && !darwinASARHasSupportedEntrypoints(asarPath) {
		return darwinTargets{}, false
	}
	return darwinTargets{
		app: appPath, name: strings.TrimSuffix(filepath.Base(appPath), filepath.Ext(appPath)),
		kind: "agent", version: darwinBundleProductVersion(appPath),
		asar: asarPath, language: languagePath,
	}, true
}

// darwinASARHasSupportedEntrypoints is a discovery-time safety gate for the
// packaged Agent layout that no longer ships a standalone language_server.
// It mirrors prepareDarwinASARCandidate's two required entries without
// attempting a write: main.js must be the known CommonJS/managed entry and
// languageServer.js must still expose either the vendor endpoint or a
// previously managed local endpoint.  This prevents a broad "any app.asar"
// fallback while allowing a verified entry-script-only release to be patched.
func darwinASARHasSupportedEntrypoints(asarPath string) bool {
	archive, err := readASAR(asarPath)
	if err != nil {
		return false
	}
	main, err := archive.readFile("dist/main.js")
	if err != nil {
		return false
	}
	launcher, err := archive.readFile("dist/languageServer.js")
	if err != nil {
		return false
	}
	managedMain := bytes.Contains(main, []byte(darwinASARMarker)) || bytes.Contains(main, []byte(legacyDarwinASARMarker))
	if !managedMain && !bytes.HasPrefix(main, []byte(`"use strict";`)) {
		return false
	}
	// The launcher may contain the vendor URL, an old WF endpoint, or a literal
	// third-party endpoint. Exactly one verified flag must be convertible to the
	// current local proxy without replacing unrelated URLs.
	return darwinLauncherHasProxyEndpoint(patchDarwinCloudCodeLauncher(string(launcher)))
}

func darwinBundleValue(appPath, key string) string {
	plist := filepath.Join(appPath, "Contents", "Info.plist")
	if output, err := darwinDiscoveryOutput("/usr/bin/plutil", "-extract", key, "raw", "-o", "-", plist); err == nil {
		return strings.TrimSpace(string(output))
	}
	return ""
}

// Product version comes exclusively from the application bundle. Internal
// Electron/VS Code package.json or product.json versions describe framework
// components and must never be presented as the Antigravity release.
func darwinBundleProductVersion(appPath string) string {
	if version := strings.TrimSpace(darwinBundleValue(appPath, "CFBundleShortVersionString")); version != "" {
		return version
	}
	return strings.TrimSpace(darwinBundleValue(appPath, "CFBundleVersion"))
}

func locateDarwinLanguageServer(binDir string) string {
	if binDir == "" {
		return ""
	}
	// Official standalone Antigravity 2.0 v2.3.1 packages the executable as
	// Contents/Resources/bin/language_server. Older IDE/Agent builds used
	// architecture-suffixed language_server_macos_x64/arm64 names. Recognize
	// both exact families, but never broaden this to arbitrary files in bin/.
	var matches []string
	if exact := existingFile(filepath.Join(binDir, "language_server")); exact != "" {
		matches = append(matches, exact)
	}
	legacy, _ := filepath.Glob(filepath.Join(binDir, "language_server_macos*"))
	matches = append(matches, legacy...)
	sort.Strings(matches)
	for _, path := range matches {
		if filepath.Base(path) == "language_server" && existingFile(path) != "" {
			return path
		}
	}
	preferred := "x64"
	if runtime.GOARCH == "arm64" {
		preferred = "arm64"
	}
	for _, path := range matches {
		if strings.Contains(filepath.Base(path), preferred) && existingFile(path) != "" {
			return path
		}
	}
	for _, path := range matches {
		if existingFile(path) != "" {
			return path
		}
	}
	return ""
}
