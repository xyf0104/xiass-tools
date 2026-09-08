//go:build darwin

package codexdesktop

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDiscoverAcceptsVerifiedChatGPTBundleAndReportsRunning(t *testing.T) {
	filesystem := newFakeFileSystem("/Users/alice")
	bundle := "/Applications/ChatGPT.app"
	executable := addMacBundle(filesystem, bundle, expectedBundleIdentifier, "ChatGPT", "1.24.3")
	now := time.Date(2026, 8, 30, 10, 0, 0, 0, time.FixedZone("CST", 8*60*60))

	status := New(Options{
		FileSystem:   filesystem,
		Processes:    fakeProcessLister{processes: []Process{{Executable: executable}}},
		BundleFinder: fakeBundleFinder{},
		Now:          func() time.Time { return now },
	}).Discover(context.Background())

	if status.State != StateRunning || !status.Running {
		t.Fatalf("state = %#v, want running", status)
	}
	if !status.Installation.Present || status.Installation.Source != SourceSystemApplications {
		t.Fatalf("installation = %#v, want verified system application", status.Installation)
	}
	if !status.Installation.ExecutableVerified || status.Installation.Version != "1.24.3" {
		t.Fatalf("installation = %#v, want executable and public version", status.Installation)
	}
	if !status.CheckedAt.Equal(now.UTC()) {
		t.Fatalf("checked at = %s, want %s", status.CheckedAt, now.UTC())
	}

	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("marshal status: %v", err)
	}
	if strings.Contains(string(encoded), "/Applications/") || strings.Contains(string(encoded), "/Users/alice") || strings.Contains(string(encoded), executable) {
		t.Fatalf("status leaked a local path: %s", encoded)
	}
}

func TestDiscoverRejectsWrongBundleIdentifier(t *testing.T) {
	filesystem := newFakeFileSystem("/Users/alice")
	addMacBundle(filesystem, "/Applications/Codex.app", "com.example.codex", "Codex", "1.0.0")
	lister := &recordingProcessLister{}

	status := New(Options{FileSystem: filesystem, Processes: lister, BundleFinder: fakeBundleFinder{}}).Discover(context.Background())

	if status.State != StateDegraded {
		t.Fatalf("state = %q, want %q", status.State, StateDegraded)
	}
	if status.Installation.Present || status.Running {
		t.Fatalf("unexpected installation state: %#v", status)
	}
	if !containsWarning(status.Warnings, WarningInvalidInstallation) {
		t.Fatalf("warnings = %v, want invalid installation", status.Warnings)
	}
	if lister.calls != 0 {
		t.Fatalf("process lister called %d times for an unverified bundle", lister.calls)
	}
}

func TestDiscoverUsesUserApplicationsAndBoundsProcessList(t *testing.T) {
	filesystem := newFakeFileSystem("/Users/alice")
	bundle := filepath.Join("/Users/alice", "Applications", "Codex.app")
	addMacBundle(filesystem, bundle, expectedBundleIdentifier, "Codex", "2.0.0")
	lister := &recordingProcessLister{err: errors.New("unavailable")}

	status := New(Options{
		FileSystem:     filesystem,
		Processes:      lister,
		BundleFinder:   fakeBundleFinder{},
		ProcessTimeout: 50 * time.Millisecond,
	}).Discover(context.Background())

	if status.State != StateDegraded {
		t.Fatalf("state = %q, want %q", status.State, StateDegraded)
	}
	if status.Installation.Source != SourceUserApplications || !status.Installation.ExecutableVerified {
		t.Fatalf("installation = %#v, want verified user application", status.Installation)
	}
	if !containsWarning(status.Warnings, WarningProcessListUnavailable) {
		t.Fatalf("warnings = %v, want process list warning", status.Warnings)
	}
	if !lister.sawDeadline {
		t.Fatal("process lister did not receive a timeout context")
	}
}

func TestDiscoverReportsRunningWhenASecondVerifiedFixedBundleIsActive(t *testing.T) {
	filesystem := newFakeFileSystem("/Users/alice")
	selectedExecutable := addMacBundle(filesystem, "/Applications/Codex.app", expectedBundleIdentifier, "Codex", "26.8.1")
	runningExecutable := addMacBundle(filesystem, "/Applications/ChatGPT.app", expectedBundleIdentifier, "ChatGPT", "26.8.2")

	status := New(Options{
		FileSystem:   filesystem,
		Processes:    fakeProcessLister{processes: []Process{{Executable: runningExecutable}}},
		BundleFinder: fakeBundleFinder{},
	}).Discover(context.Background())

	if status.State != StateRunning || !status.Running {
		t.Fatalf("state = %#v, want running second verified fixed bundle", status)
	}
	// The conventional Codex.app target remains deterministic for lifecycle
	// actions even though ChatGPT.app is the process that is currently active.
	if status.Installation.Source != SourceSystemApplications || status.Installation.Version != "26.8.1" {
		t.Fatalf("installation = %#v, want first verified fixed target", status.Installation)
	}
	if selectedExecutable == runningExecutable {
		t.Fatal("test fixture did not create distinct verified executables")
	}
}

func TestDiscoverReportsRunningForNonstandardSpotlightBundleWhenFixedTargetExists(t *testing.T) {
	filesystem := newFakeFileSystem("/Users/alice")
	selectedExecutable := addMacBundle(filesystem, "/Applications/Codex.app", expectedBundleIdentifier, "Codex", "26.8.1")
	runningBundle := "/Volumes/Managed Apps/ChatGPT.app"
	runningExecutable := addMacBundle(filesystem, runningBundle, expectedBundleIdentifier, "ChatGPT", "26.8.2")
	finder := &recordingBundleFinder{paths: []string{runningBundle}}

	status := New(Options{
		FileSystem:   filesystem,
		Processes:    fakeProcessLister{processes: []Process{{Executable: runningExecutable}}},
		BundleFinder: finder,
	}).Discover(context.Background())

	if status.State != StateRunning || !status.Running {
		t.Fatalf("state = %#v, want running nonstandard Spotlight bundle", status)
	}
	// The standard fixed installation remains the deterministic lifecycle
	// target, but it cannot hide a separately running verified bundle.
	if status.Installation.Source != SourceSystemApplications || status.Installation.Version != "26.8.1" {
		t.Fatalf("installation = %#v, want deterministic fixed target", status.Installation)
	}
	if finder.bundleIdentifier != expectedBundleIdentifier || finder.limit != maxSpotlightCandidates || !finder.sawDeadline {
		t.Fatalf("Spotlight request = %#v, want bounded exact lookup even with fixed target", finder)
	}
	if selectedExecutable == runningExecutable {
		t.Fatal("test fixture did not create distinct verified executables")
	}
}

func TestDiscoverUsesBoundedSpotlightFallbackAndRedactsFoundBundle(t *testing.T) {
	filesystem := newFakeFileSystem("/Users/alice")
	bundle := "/Volumes/Developer Tools/Codex Preview.app"
	selectedExecutable := addMacBundle(filesystem, bundle, expectedBundleIdentifier, "Codex", "26.8.1")
	runningBundle := "/Volumes/Y Other/Codex.app"
	runningExecutable := addMacBundle(filesystem, runningBundle, expectedBundleIdentifier, "ChatGPT", "26.8.2")
	invalidAfterVerified := "/Volumes/Z Untrusted/Codex.app"
	addMacBundle(filesystem, invalidAfterVerified, "com.example.not-codex", "Codex", "1.0.0")
	finder := &recordingBundleFinder{paths: []string{
		bundle,
		runningBundle,
		invalidAfterVerified,
	}}

	status := New(Options{
		FileSystem:   filesystem,
		Processes:    fakeProcessLister{processes: []Process{{Executable: runningExecutable}}},
		BundleFinder: finder,
	}).Discover(context.Background())

	if status.State != StateRunning || !status.Running {
		t.Fatalf("state = %#v, want running Spotlight-discovered installation", status)
	}
	if status.Installation.Source != SourcePublicDiscovery || !status.Installation.ExecutableVerified {
		t.Fatalf("installation = %#v, want verified public discovery source", status.Installation)
	}
	if status.Installation.Version != "26.8.1" {
		t.Fatalf("installation = %#v, want first sorted Spotlight target while another bundle is running", status.Installation)
	}
	if finder.bundleIdentifier != expectedBundleIdentifier || finder.limit != maxSpotlightCandidates || !finder.sawDeadline {
		t.Fatalf("Spotlight request = %#v, want exact built-in identifier, fixed limit, and timeout", finder)
	}
	if filesystem.reads[filepath.Join(invalidAfterVerified, "Contents", "Info.plist")] == 0 {
		t.Fatal("Spotlight result after the selected bundle was not structurally validated")
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("marshal status: %v", err)
	}
	for _, forbidden := range []string{bundle, selectedExecutable, runningBundle, runningExecutable, "/Volumes/Developer Tools"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("status leaked Spotlight path: %s", encoded)
		}
	}
}

func TestDiscoverSpotlightRejectsInvalidBundlesAndHonorsCandidateLimit(t *testing.T) {
	filesystem := newFakeFileSystem("/Users/alice")
	paths := make([]string, 0, maxSpotlightCandidates+1)
	for index := 0; index < maxSpotlightCandidates; index++ {
		bundle := filepath.Join("/Volumes/Invalid", strings.Repeat("x", index+1)+".app")
		addMacBundle(filesystem, bundle, "com.example.not-codex", "Codex", "1.0.0")
		paths = append(paths, bundle)
	}
	validAfterLimit := "/Volumes/Valid After Limit/Codex.app"
	addMacBundle(filesystem, validAfterLimit, expectedBundleIdentifier, "Codex", "1.0.0")
	paths = append(paths, validAfterLimit)

	finder := &recordingBundleFinder{paths: paths}
	status := New(Options{
		FileSystem:   filesystem,
		Processes:    &recordingProcessLister{},
		BundleFinder: finder,
	}).Discover(context.Background())

	if status.Installation.Present || status.Running || status.State != StateDegraded {
		t.Fatalf("over-limit / invalid Spotlight paths produced a trusted install: %#v", status)
	}
	if !containsWarning(status.Warnings, WarningInvalidInstallation) {
		t.Fatalf("warnings = %v, want invalid public installation", status.Warnings)
	}
	if finder.limit != maxSpotlightCandidates {
		t.Fatalf("Spotlight limit = %d, want %d", finder.limit, maxSpotlightCandidates)
	}
}

func TestDiscoverSpotlightFailureIsRedactedDegradedState(t *testing.T) {
	filesystem := newFakeFileSystem("/Users/alice")
	status := New(Options{
		FileSystem:   filesystem,
		Processes:    &recordingProcessLister{},
		BundleFinder: fakeBundleFinder{err: errors.New("private lookup failure /Users/alice/.secret")},
	}).Discover(context.Background())

	if status.State != StateDegraded || status.Installation.Present {
		t.Fatalf("status = %#v, want redacted degraded discovery", status)
	}
	if !containsWarning(status.Warnings, WarningInspectionUnavailable) {
		t.Fatalf("warnings = %v, want inspection warning", status.Warnings)
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("marshal status: %v", err)
	}
	if strings.Contains(string(encoded), "/Users/alice/.secret") {
		t.Fatalf("status leaked a lookup error: %s", encoded)
	}
}

func TestSystemBundleFinderUsesOnlyTheBuiltInExactIdentifier(t *testing.T) {
	if query := spotlightBundleQuery(expectedBundleIdentifier); query != "kMDItemCFBundleIdentifier == 'com.openai.codex'" {
		t.Fatalf("Spotlight query = %q, want fixed exact bundle identifier", query)
	}
	if _, err := (systemBundleFinder{}).FindBundles(context.Background(), "com.example.untrusted", 1); err == nil {
		t.Fatal("system Spotlight finder accepted a caller-controlled bundle identifier")
	}
}

func containsWarning(warnings []Warning, wanted Warning) bool {
	for _, warning := range warnings {
		if warning == wanted {
			return true
		}
	}
	return false
}

type fakeFileSystem struct {
	home     string
	homeErr  error
	entries  map[string]fs.FileInfo
	contents map[string][]byte
	reads    map[string]int
}

func newFakeFileSystem(home string) *fakeFileSystem {
	return &fakeFileSystem{
		home:     home,
		entries:  make(map[string]fs.FileInfo),
		contents: make(map[string][]byte),
		reads:    make(map[string]int),
	}
}

func (filesystem *fakeFileSystem) Stat(path string) (fs.FileInfo, error) {
	if entry, ok := filesystem.entries[filepath.Clean(path)]; ok {
		return entry, nil
	}
	return nil, fs.ErrNotExist
}

func (filesystem *fakeFileSystem) ReadFile(path string) ([]byte, error) {
	path = filepath.Clean(path)
	filesystem.reads[path]++
	if data, ok := filesystem.contents[path]; ok {
		return append([]byte(nil), data...), nil
	}
	return nil, fs.ErrNotExist
}

func (filesystem *fakeFileSystem) UserHomeDir() (string, error) {
	return filesystem.home, filesystem.homeErr
}

func (*fakeFileSystem) Getenv(string) string { return "" }

func addMacBundle(filesystem *fakeFileSystem, bundle, identifier, executable, version string) string {
	bundle = filepath.Clean(bundle)
	filesystem.entries[bundle] = fakeFileInfo{name: filepath.Base(bundle), mode: fs.ModeDir | 0755}
	infoPath := filepath.Join(bundle, "Contents", "Info.plist")
	filesystem.contents[infoPath] = []byte("<?xml version=\"1.0\"?><plist><dict>" +
		"<key>CFBundleIdentifier</key><string>" + identifier + "</string>" +
		"<key>CFBundleExecutable</key><string>" + executable + "</string>" +
		"<key>CFBundleShortVersionString</key><string>" + version + "</string>" +
		"</dict></plist>")
	executablePath := filepath.Join(bundle, "Contents", "MacOS", executable)
	filesystem.entries[executablePath] = fakeFileInfo{name: executable, mode: 0755}
	return executablePath
}

type fakeFileInfo struct {
	name string
	mode fs.FileMode
}

func (info fakeFileInfo) Name() string       { return info.name }
func (info fakeFileInfo) Size() int64        { return 0 }
func (info fakeFileInfo) Mode() fs.FileMode  { return info.mode }
func (info fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (info fakeFileInfo) IsDir() bool        { return info.mode.IsDir() }
func (info fakeFileInfo) Sys() any           { return nil }

type fakeProcessLister struct {
	processes []Process
	err       error
}

func (lister fakeProcessLister) List(context.Context) ([]Process, error) {
	return lister.processes, lister.err
}

type recordingProcessLister struct {
	calls       int
	err         error
	sawDeadline bool
}

type fakeBundleFinder struct {
	paths []string
	err   error
}

func (finder fakeBundleFinder) FindBundles(context.Context, string, int) ([]string, error) {
	return append([]string(nil), finder.paths...), finder.err
}

type recordingBundleFinder struct {
	paths            []string
	err              error
	bundleIdentifier string
	limit            int
	sawDeadline      bool
}

func (finder *recordingBundleFinder) FindBundles(ctx context.Context, bundleIdentifier string, limit int) ([]string, error) {
	finder.bundleIdentifier = bundleIdentifier
	finder.limit = limit
	_, finder.sawDeadline = ctx.Deadline()
	return append([]string(nil), finder.paths...), finder.err
}

func (lister *recordingProcessLister) List(ctx context.Context) ([]Process, error) {
	lister.calls++
	_, lister.sawDeadline = ctx.Deadline()
	return nil, lister.err
}
