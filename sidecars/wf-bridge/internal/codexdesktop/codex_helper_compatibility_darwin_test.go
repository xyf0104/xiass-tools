//go:build darwin

package codexdesktop

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// This fixture verifies the bounded public App-shape contract used by the
// former XIASS Codex Helper. It uses the existing fake filesystem and process
// lister; no actual /Applications bundle or local process is inspected.
func TestCodexHelperCompatibilityDiscoversVerifiedMacDesktopFixture(t *testing.T) {
	filesystem := newFakeFileSystem("/Users/compatibility")
	bundle := "/Applications/Codex.app"
	executable := addMacBundle(filesystem, bundle, expectedBundleIdentifier, "Codex", "26.8.1")

	status := New(Options{
		FileSystem:   filesystem,
		Processes:    fakeProcessLister{processes: []Process{{Executable: executable}}},
		BundleFinder: fakeBundleFinder{},
	}).Discover(context.Background())

	if status.State != StateRunning || !status.Running {
		t.Fatalf("fixture state = %#v, want verified running desktop", status)
	}
	if !status.Installation.Present || status.Installation.Source != SourceSystemApplications || !status.Installation.ExecutableVerified || status.Installation.Version != "26.8.1" {
		t.Fatalf("fixture installation = %#v, want verified system Codex app", status.Installation)
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("marshal redacted discovery result: %v", err)
	}
	for _, forbidden := range []string{bundle, executable, "/Users/compatibility"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("desktop discovery leaked fixture path %q: %s", forbidden, encoded)
		}
	}
}
