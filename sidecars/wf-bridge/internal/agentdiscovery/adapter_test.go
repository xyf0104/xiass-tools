package agentdiscovery

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"antigravity-wf-assistant/internal/agent"
)

func TestClaudeCodeDetectsCLIAndConfiguration(t *testing.T) {
	home := filepath.Join("/Users", "fixture")
	config := joinClaudeConfigPath(home)
	fixture := newFilesystemFixture(home)
	fixture.addDirectory(config)
	runner := commandFixture{
		paths:  map[string]string{"claude": filepath.Join("/opt", "homebrew", "bin", "claude")},
		output: map[string][]byte{filepath.Join("/opt", "homebrew", "bin", "claude"): []byte("2.1.8 (Claude Code)\n")},
	}
	adapter := NewClaudeCodeAdapter(Options{FileSystem: fixture, Commands: runner, Now: fixedNow})

	status, err := adapter.Detect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.State != agent.StateReady {
		t.Fatalf("state = %q, want %q (%s)", status.State, agent.StateReady, status.Message)
	}
	if status.Installation.Root != config || status.Installation.ExecutablePath == "" {
		t.Fatalf("installation = %+v, want config and CLI", status.Installation)
	}
	if status.Installation.Version != "2.1.8" {
		t.Fatalf("version = %q, want 2.1.8", status.Installation.Version)
	}
	assertDiscoveryOnly(t, status)
}

func TestClaudeCodePartialAndVersionFailureBoundaries(t *testing.T) {
	home := filepath.Join("/Users", "fixture")
	config := joinClaudeConfigPath(home)
	cli := filepath.Join("/opt", "homebrew", "bin", "claude")

	t.Run("configuration-only-is-detected", func(t *testing.T) {
		fixture := newFilesystemFixture(home)
		fixture.addDirectory(config)
		status, err := NewClaudeCodeAdapter(Options{FileSystem: fixture, Commands: commandFixture{}, Now: fixedNow}).Detect(context.Background())
		if err != nil || status.State != agent.StateDetected {
			t.Fatalf("status = %+v, err = %v; want detected without error", status, err)
		}
	})

	t.Run("failed-version-probe-is-degraded-not-fatal", func(t *testing.T) {
		fixture := newFilesystemFixture(home)
		fixture.addDirectory(config)
		status, err := NewClaudeCodeAdapter(Options{
			FileSystem: fixture,
			Commands: commandFixture{
				paths:     map[string]string{"claude": cli},
				outputErr: map[string]error{cli: errors.New("temporary command failure")},
			},
			Now: fixedNow,
		}).Detect(context.Background())
		if err != nil {
			t.Fatalf("expected non-fatal detector result, got %v", err)
		}
		if status.State != agent.StateDegraded {
			t.Fatalf("state = %q, want %q", status.State, agent.StateDegraded)
		}
	})
}

func TestClaudeCodeDetectsKnownCandidateWithoutExecutingIt(t *testing.T) {
	home := filepath.Join("/Users", "fixture")
	cli := filepath.Join(home, ".local", "bin", "claude")
	fixture := newFilesystemFixture(home)
	fixture.addDirectory(joinClaudeConfigPath(home))
	fixture.addFile(cli)

	// If discovery accidentally invokes the known-location fallback, this
	// injected failure would degrade the result. A ready state proves the
	// candidate was inspected but not executed.
	status, err := NewClaudeCodeAdapter(Options{
		FileSystem: fixture,
		Commands: commandFixture{
			outputErr: map[string]error{cli: errors.New("known fallback must not run")},
		},
		Now: fixedNow,
	}).Detect(context.Background())
	if err != nil || status.State != agent.StateReady {
		t.Fatalf("status = %+v, err = %v; want ready known-candidate discovery", status, err)
	}
	if status.Installation.ExecutablePath != cli || status.Installation.Version != "" {
		t.Fatalf("installation = %+v, want inspected candidate without version execution", status.Installation)
	}
}

func TestMacDesktopDiscoveryStateBoundaries(t *testing.T) {
	home := filepath.Join("/Users", "fixture")
	application := filepath.Join("/Applications", "Cursor.app")
	executable := filepath.Join(application, "Contents", "MacOS", "Cursor")
	dataPath := filepath.Join(home, "Library", "Application Support", "Cursor")
	infoPath := filepath.Join(application, "Contents", "Info.plist")

	t.Run("bundle-and-data-is-ready", func(t *testing.T) {
		fixture := newFilesystemFixture(home)
		fixture.addDirectory(application)
		fixture.addFile(executable)
		fixture.addDirectory(dataPath)
		fixture.files[infoPath] = macInfoPlist("Cursor", "0.48.7")
		status, err := NewCursorAdapter(Options{FileSystem: fixture, Now: fixedNow}).Detect(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if status.State != agent.StateReady {
			t.Fatalf("state = %q, want ready (%s)", status.State, status.Message)
		}
		if status.Installation.Version != "0.48.7" || status.Details["dataDirectory"] != dataPath {
			t.Fatalf("status = %+v, want version and data path", status)
		}
		assertDiscoveryOnly(t, status)
	})

	t.Run("local-data-only-is-detected", func(t *testing.T) {
		fixture := newFilesystemFixture(home)
		fixture.addDirectory(dataPath)
		status, err := NewCursorAdapter(Options{FileSystem: fixture, Now: fixedNow}).Detect(context.Background())
		if err != nil || status.State != agent.StateDetected {
			t.Fatalf("status = %+v, err = %v; want detected", status, err)
		}
	})

	t.Run("corrupt-bundle-is-degraded", func(t *testing.T) {
		fixture := newFilesystemFixture(home)
		fixture.addDirectory(application)
		fixture.addDirectory(dataPath)
		status, err := NewCursorAdapter(Options{FileSystem: fixture, Now: fixedNow}).Detect(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if status.State != agent.StateDegraded {
			t.Fatalf("state = %q, want degraded (%s)", status.State, status.Message)
		}
	})

	t.Run("no-artifacts-is-not-installed", func(t *testing.T) {
		fixture := newFilesystemFixture(home)
		status, err := NewWindsurfAdapter(Options{FileSystem: fixture, Now: fixedNow}).Detect(context.Background())
		if err != nil || status.State != agent.StateNotInstalled {
			t.Fatalf("status = %+v, err = %v; want not-installed", status, err)
		}
	})
}

func TestMacUnreadableLocalDataIsDegraded(t *testing.T) {
	home := filepath.Join("/Users", "fixture")
	application := filepath.Join("/Applications", "Windsurf.app")
	executable := filepath.Join(application, "Contents", "MacOS", "Windsurf")
	dataPath := filepath.Join(home, "Library", "Application Support", "Windsurf")
	fixture := newFilesystemFixture(home)
	fixture.addDirectory(application)
	fixture.addFile(executable)
	fixture.errors[dataPath] = fs.ErrPermission

	status, err := NewWindsurfAdapter(Options{FileSystem: fixture, Now: fixedNow}).Detect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.State != agent.StateDegraded {
		t.Fatalf("state = %q, want degraded for unreadable local data", status.State)
	}
}

func TestMacDesktopNativeSelectionRequiresVerifiedPublicIdentity(t *testing.T) {
	home := filepath.Join("/Users", "fixture")
	bundle := filepath.Join("/Volumes", "Developer", "Cursor.app")
	executable := filepath.Join(bundle, "Contents", "MacOS", "Cursor")
	info := filepath.Join(bundle, "Contents", "Info.plist")
	fixture := newFilesystemFixture(home)
	fixture.addDirectory(bundle)
	fixture.addFile(executable)
	fixture.files[info] = macSelectionInfoPlist("com.todesktop.230313mzl4w4u92", "Cursor", "1.4.0")

	selection, err := validateDesktopSelectionWithFileSystem(fixture, agent.CursorID, executable)
	if err != nil {
		t.Fatalf("select verified Cursor bundle: %v", err)
	}
	if selection.AgentID() != agent.CursorID || selection.Version() != "1.4.0" || selection.LaunchTarget() != bundle {
		t.Fatalf("selection = %#v, want verified Cursor bundle", selection)
	}
	encoded, err := json.Marshal(selection)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{bundle, executable, "/Volumes/Developer"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("selection JSON leaked path %q: %s", forbidden, encoded)
		}
	}

	fixture.files[info] = macSelectionInfoPlist("com.example.lookalike", "Cursor", "1.4.0")
	if _, err := validateDesktopSelectionWithFileSystem(fixture, agent.CursorID, bundle); !errors.Is(err, ErrDesktopSelectionRejected) {
		t.Fatalf("lookalike selection error = %v, want generic rejection", err)
	}
	if _, err := validateDesktopSelectionWithFileSystem(fixture, agent.WindsurfID, bundle); !errors.Is(err, ErrDesktopSelectionRejected) {
		t.Fatalf("wrong-agent selection error = %v, want generic rejection", err)
	}
}

func TestFailedClaudeProbeDoesNotPreventOtherRegistryAdapters(t *testing.T) {
	home := filepath.Join("/Users", "fixture")
	cursorFS := newFilesystemFixture(home)
	cursorApplication := filepath.Join("/Applications", "Cursor.app")
	cursorExecutable := filepath.Join(cursorApplication, "Contents", "MacOS", "Cursor")
	cursorFS.addDirectory(cursorApplication)
	cursorFS.addFile(cursorExecutable)
	cursorFS.addDirectory(filepath.Join(home, "Library", "Application Support", "Cursor"))
	windsurfFS := newFilesystemFixture(home)
	claudeFS := newFilesystemFixture(home)
	claudeFS.addDirectory(joinClaudeConfigPath(home))
	claudeCLI := filepath.Join("/opt", "homebrew", "bin", "claude")

	registry := agent.NewDefaultRegistry()
	if err := registry.Bind(NewClaudeCodeAdapter(Options{
		FileSystem: claudeFS,
		Commands: commandFixture{
			paths: map[string]string{"claude": claudeCLI},
			block: true,
		},
		VersionTimeout: time.Millisecond,
		Now:            fixedNow,
	})); err != nil {
		t.Fatal(err)
	}
	if err := registry.Bind(NewCursorAdapter(Options{FileSystem: cursorFS, Now: fixedNow})); err != nil {
		t.Fatal(err)
	}
	if err := registry.Bind(NewWindsurfAdapter(Options{FileSystem: windsurfFS, Now: fixedNow})); err != nil {
		t.Fatal(err)
	}

	aggregate := registry.DetectAll(context.Background())
	states := make(map[agent.ID]agent.State, len(aggregate.Agents))
	for _, status := range aggregate.Agents {
		states[status.AgentID] = status.State
	}
	if states[agent.ClaudeCodeID] != agent.StateDegraded {
		t.Fatalf("Claude state = %q, want degraded", states[agent.ClaudeCodeID])
	}
	if states[agent.CursorID] != agent.StateReady {
		t.Fatalf("Cursor state = %q, want ready after Claude failure", states[agent.CursorID])
	}
}

func TestClaudeVersionTimeoutIsBoundedAndDegraded(t *testing.T) {
	home := filepath.Join("/Users", "fixture")
	fixture := newFilesystemFixture(home)
	fixture.addDirectory(joinClaudeConfigPath(home))
	cli := filepath.Join("/opt", "homebrew", "bin", "claude")
	adapter := NewClaudeCodeAdapter(Options{
		FileSystem: fixture,
		Commands: commandFixture{
			paths: map[string]string{"claude": cli},
			block: true,
		},
		VersionTimeout: time.Millisecond,
		Now:            fixedNow,
	})
	status, err := adapter.Detect(context.Background())
	if err != nil || status.State != agent.StateDegraded {
		t.Fatalf("status = %+v, err = %v; want bounded degraded result", status, err)
	}
}

func assertDiscoveryOnly(t *testing.T, status agent.Status) {
	t.Helper()
	for _, capability := range status.Capabilities {
		if capability.Capability == agent.CapabilityDiscovery {
			if !capability.Available || capability.Availability != agent.CapabilityAvailable {
				t.Fatalf("discovery capability = %+v, want available", capability)
			}
			continue
		}
		if capability.Available {
			t.Fatalf("non-discovery capability unexpectedly available: %+v", capability)
		}
	}
}

var fixedNow = func() time.Time { return time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC) }

type filesystemFixture struct {
	home    string
	homeErr error
	env     map[string]string
	entries map[string]fixtureFileInfo
	files   map[string][]byte
	errors  map[string]error
}

func newFilesystemFixture(home string) *filesystemFixture {
	return &filesystemFixture{
		home:    home,
		env:     map[string]string{},
		entries: map[string]fixtureFileInfo{},
		files:   map[string][]byte{},
		errors:  map[string]error{},
	}
}

func (fixture *filesystemFixture) addDirectory(path string) {
	fixture.entries[filepath.Clean(path)] = fixtureFileInfo{name: filepath.Base(path), directory: true}
}

func (fixture *filesystemFixture) addFile(path string) {
	fixture.entries[filepath.Clean(path)] = fixtureFileInfo{name: filepath.Base(path)}
}

func (fixture *filesystemFixture) Stat(path string) (fs.FileInfo, error) {
	path = filepath.Clean(path)
	if err, exists := fixture.errors[path]; exists {
		return nil, err
	}
	if info, exists := fixture.entries[path]; exists {
		return info, nil
	}
	return nil, fs.ErrNotExist
}

func (fixture *filesystemFixture) ReadFile(path string) ([]byte, error) {
	path = filepath.Clean(path)
	if err, exists := fixture.errors[path]; exists {
		return nil, err
	}
	data, exists := fixture.files[path]
	if !exists {
		return nil, fs.ErrNotExist
	}
	return append([]byte(nil), data...), nil
}

func (fixture *filesystemFixture) UserHomeDir() (string, error) { return fixture.home, fixture.homeErr }
func (fixture *filesystemFixture) Getenv(key string) string     { return fixture.env[key] }

type fixtureFileInfo struct {
	name      string
	directory bool
}

func (info fixtureFileInfo) Name() string { return info.name }
func (info fixtureFileInfo) Size() int64  { return 0 }
func (info fixtureFileInfo) Mode() fs.FileMode {
	if info.directory {
		return fs.ModeDir | 0o755
	}
	return 0o644
}
func (info fixtureFileInfo) ModTime() time.Time { return time.Time{} }
func (info fixtureFileInfo) IsDir() bool        { return info.directory }
func (info fixtureFileInfo) Sys() any           { return nil }

type commandFixture struct {
	paths     map[string]string
	lookErr   map[string]error
	output    map[string][]byte
	outputErr map[string]error
	block     bool
}

func (fixture commandFixture) LookPath(name string) (string, error) {
	if err := fixture.lookErr[name]; err != nil {
		return "", err
	}
	if path, exists := fixture.paths[name]; exists {
		return path, nil
	}
	return "", exec.ErrNotFound
}

func (fixture commandFixture) Output(ctx context.Context, name string, _ ...string) ([]byte, error) {
	if fixture.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if err := fixture.outputErr[name]; err != nil {
		return nil, err
	}
	return append([]byte(nil), fixture.output[name]...), nil
}

func macInfoPlist(executable, version string) []byte {
	return []byte(`<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
<key>CFBundleExecutable</key><string>` + executable + `</string>
<key>CFBundleShortVersionString</key><string>` + version + `</string>
</dict></plist>`)
}

func macSelectionInfoPlist(identifier, executable, version string) []byte {
	return []byte(`<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
<key>CFBundleIdentifier</key><string>` + identifier + `</string>
<key>CFBundleExecutable</key><string>` + executable + `</string>
<key>CFBundleShortVersionString</key><string>` + version + `</string>
</dict></plist>`)
}
