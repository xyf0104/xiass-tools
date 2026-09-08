//go:build darwin

package codexdesktop

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestControllerManualSelectionIsRedactedAndRequiresVerifiedBundle(t *testing.T) {
	filesystem := newFakeFileSystem("/Users/alice")
	bundle := "/Volumes/Tools/ChatGPT.app"
	executable := addMacBundle(filesystem, bundle, expectedBundleIdentifier, "ChatGPT", "26.825.32147")
	lister := &mutableMacProcessLister{}
	controller := newControllerForTest(New(Options{FileSystem: filesystem, Processes: lister, BundleFinder: fakeBundleFinder{}}), controlOperations{
		launch: func(context.Context, desktopTarget) error { return nil },
		stop:   func(context.Context, desktopTarget) error { return nil },
	})

	status, err := controller.SelectPath(context.Background(), filepath.Join(bundle, "Contents", "MacOS", "ChatGPT"))
	if err != nil {
		t.Fatalf("select verified bundle: %v", err)
	}
	if !status.Selected || !status.Discovered || status.Installation.Source != SourceManualSelection || !status.Installation.ExecutableVerified {
		t.Fatalf("selection status = %#v", status)
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("marshal selection status: %v", err)
	}
	if strings.Contains(string(encoded), bundle) || strings.Contains(string(encoded), executable) || strings.Contains(string(encoded), "/Volumes/Tools") {
		t.Fatalf("control status leaked selected path: %s", encoded)
	}

	lookalike := "/Volumes/Tools/NotCodex.app"
	addMacBundle(filesystem, lookalike, expectedBundleIdentifier, "ChatGPT", "1.0.0")
	_, err = controller.SelectPath(context.Background(), lookalike)
	if !errors.Is(err, ErrSelectionRejected) {
		t.Fatalf("invalid app selection error = %v, want generic rejection", err)
	}
	if strings.Contains(err.Error(), lookalike) {
		t.Fatalf("selection error leaked a local path: %q", err)
	}
}

func TestControllerLifecycleRequiresConfirmationAndRevalidatesBeforeAction(t *testing.T) {
	filesystem := newFakeFileSystem("/Users/alice")
	bundle := "/Volumes/Tools/Codex.app"
	executable := addMacBundle(filesystem, bundle, expectedBundleIdentifier, "Codex", "2.0.0")
	lister := &mutableMacProcessLister{processes: []Process{{Executable: executable}}}
	var stopCalls int
	var launchCalls int
	controller := newControllerForTest(New(Options{FileSystem: filesystem, Processes: lister, BundleFinder: fakeBundleFinder{}}), controlOperations{
		stop: func(context.Context, desktopTarget) error {
			stopCalls++
			lister.Set(nil)
			return nil
		},
		launch: func(_ context.Context, target desktopTarget) error {
			launchCalls++
			lister.Set([]Process{{Executable: target.executable}})
			return nil
		},
	})
	if _, err := controller.SelectPath(context.Background(), bundle); err != nil {
		t.Fatalf("select verified bundle: %v", err)
	}
	if _, err := controller.Stop(context.Background(), "not-confirmed"); !errors.Is(err, ErrConfirmationRequired) {
		t.Fatalf("stop error = %v, want confirmation requirement", err)
	}
	if stopCalls != 0 {
		t.Fatalf("stop callback called %d times without confirmation", stopCalls)
	}

	status, err := controller.Restart(context.Background(), LifecycleConfirmation)
	if err != nil {
		t.Fatalf("restart verified app: %v", err)
	}
	if stopCalls != 1 || launchCalls != 1 || !status.Running {
		t.Fatalf("restart calls=(stop %d, launch %d), status=%#v", stopCalls, launchCalls, status)
	}

	// A bundle whose metadata changes after selection must not be launched.
	filesystem.contents[filepath.Join(bundle, "Contents", "Info.plist")] = []byte("<plist><dict><key>CFBundleIdentifier</key><string>com.example.lookalike</string></dict></plist>")
	lister.Set(nil)
	_, err = controller.Launch(context.Background())
	if !errors.Is(err, ErrNoVerifiedInstallation) {
		t.Fatalf("launch after invalidation error = %v, want no verified installation", err)
	}
	if launchCalls != 1 {
		t.Fatalf("launch callback called after bundle invalidation: %d", launchCalls)
	}
}

func TestControllerCancelledContextDoesNotLaunch(t *testing.T) {
	filesystem := newFakeFileSystem("/Users/alice")
	bundle := "/Volumes/Tools/ChatGPT.app"
	addMacBundle(filesystem, bundle, expectedBundleIdentifier, "ChatGPT", "1.0.0")
	var launches int
	controller := newControllerForTest(New(Options{FileSystem: filesystem, Processes: &mutableMacProcessLister{}, BundleFinder: fakeBundleFinder{}}), controlOperations{
		launch: func(context.Context, desktopTarget) error { launches++; return nil },
		stop:   func(context.Context, desktopTarget) error { return nil },
	})
	if _, err := controller.SelectPath(context.Background(), bundle); err != nil {
		t.Fatalf("select verified bundle: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := controller.Launch(ctx); !errors.Is(err, ErrLifecycleUnavailable) {
		t.Fatalf("cancelled launch error = %v", err)
	}
	if launches != 0 {
		t.Fatalf("launch callback invoked with cancelled context: %d", launches)
	}
}

func TestControllerDoesNotRetargetLifecycleToSeparatelyRunningSpotlightBundle(t *testing.T) {
	filesystem := newFakeFileSystem("/Users/alice")
	selectedBundle := "/Applications/Codex.app"
	addMacBundle(filesystem, selectedBundle, expectedBundleIdentifier, "Codex", "26.8.1")
	runningBundle := "/Volumes/Managed/ChatGPT.app"
	runningExecutable := addMacBundle(filesystem, runningBundle, expectedBundleIdentifier, "ChatGPT", "26.8.2")
	lister := &mutableMacProcessLister{processes: []Process{{Executable: runningExecutable}}}
	var launches, stops int
	controller := newControllerForTest(New(Options{
		FileSystem:   filesystem,
		Processes:    lister,
		BundleFinder: fakeBundleFinder{paths: []string{runningBundle}},
	}), controlOperations{
		launch: func(context.Context, desktopTarget) error { launches++; return nil },
		stop:   func(context.Context, desktopTarget) error { stops++; return nil },
	})

	status := controller.Status(context.Background())
	if !status.Running || status.CanLaunch || status.CanStop || status.CanRestart {
		t.Fatalf("controller status = %#v, want global running with no retargetable lifecycle action", status)
	}
	if status.Installation.Source != SourceSystemApplications || status.Installation.Version != "26.8.1" {
		t.Fatalf("controller installation = %#v, want deterministic fixed target", status.Installation)
	}
	if _, err := controller.Launch(context.Background()); !errors.Is(err, ErrDesktopAlreadyRunning) {
		t.Fatalf("launch error = %v, want running protection", err)
	}
	if _, err := controller.Stop(context.Background(), LifecycleConfirmation); !errors.Is(err, ErrDesktopNotRunning) {
		t.Fatalf("stop error = %v, want lifecycle target mismatch protection", err)
	}
	if launches != 0 || stops != 0 {
		t.Fatalf("lifecycle operation retargeted a separately running bundle: launches=%d stops=%d", launches, stops)
	}
}

type mutableMacProcessLister struct {
	mu        sync.Mutex
	processes []Process
}

func (lister *mutableMacProcessLister) List(context.Context) ([]Process, error) {
	lister.mu.Lock()
	defer lister.mu.Unlock()
	return append([]Process(nil), lister.processes...), nil
}

func (lister *mutableMacProcessLister) Set(processes []Process) {
	lister.mu.Lock()
	defer lister.mu.Unlock()
	lister.processes = append([]Process(nil), processes...)
}
