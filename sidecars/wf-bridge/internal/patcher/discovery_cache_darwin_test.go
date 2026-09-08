//go:build darwin

package patcher

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDarwinSavedNonStandardPathIsRevalidatedBeforeDiscovery(t *testing.T) {
	invalidateDarwinDiscoveryCache()
	t.Cleanup(invalidateDarwinDiscoveryCache)
	root := t.TempDir()
	app := filepath.Join(root, "Renamed Customer Install.app")
	writeDarwinUnpackedDiscoveryFixture(t, app, "com.google.antigravity-ide")
	data, err := json.Marshal(map[string]any{
		"schema":  1,
		"targets": []map[string]string{{"kind": "ide", "appPath": app}},
	})
	if err != nil {
		t.Fatal(err)
	}
	paths := darwinSavedInstallPathsFromData(data)
	if len(paths) != 1 || paths[0] != filepath.Clean(app) {
		t.Fatalf("saved paths = %#v, want %q", paths, app)
	}
	targets := selectDarwinInstallations(paths, nil, func(string) bool { return false })
	if len(targets) != 1 || targets[0].app != filepath.Clean(app) || targets[0].kind != "ide" {
		t.Fatalf("verified saved non-standard app was not discovered: %+v", targets)
	}

	// Replacing the remembered path with another bundle must revoke the hint.
	writeDarwinProductVersionPlist(t, app, "com.example.unrelated", "9.9.9")
	if targets := selectDarwinInstallations(paths, nil, func(string) bool { return false }); len(targets) != 0 {
		t.Fatalf("saved path bypassed current bundle identity validation: %+v", targets)
	}
}

func TestDarwinQuickInspectionDoesNotOpenASAR(t *testing.T) {
	app := filepath.Join(t.TempDir(), "Antigravity 2.0.app")
	resources := filepath.Join(app, "Contents", "Resources")
	if err := os.MkdirAll(resources, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(resources, "app.asar"), []byte("not an ASAR archive"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeDarwinProductVersionPlist(t, app, "com.google.antigravity", "2.6.0")
	if target, ok := inspectDarwinAppQuick(app); !ok || target.kind != "agent" || target.version != "2.6.0" {
		t.Fatalf("quick structural inspection unexpectedly parsed/rejected ASAR: ok=%t target=%+v", ok, target)
	}
	if target, ok := inspectDarwinApp(app); ok {
		t.Fatalf("full inspection accepted the corrupt ASAR: %+v", target)
	}
}

func TestDarwinDiscoveryCacheInvalidatesOnBundleVersionChange(t *testing.T) {
	invalidateDarwinDiscoveryCache()
	t.Cleanup(invalidateDarwinDiscoveryCache)
	app := filepath.Join(t.TempDir(), "Antigravity IDE.app")
	writeDarwinUnpackedDiscoveryFixture(t, app, "com.google.antigravity-ide")
	writeDarwinProductVersionPlist(t, app, "com.google.antigravity-ide", "2.5.5")
	target, ok := inspectDarwinApp(app)
	if !ok {
		t.Fatal("version cache fixture was not discovered")
	}
	cacheDarwinInstallations([]darwinTargets{target}, false)
	if cached, ok := cachedDarwinInstallations(false); !ok || len(cached) != 1 || cached[0].version != "2.5.5" {
		t.Fatalf("unchanged version did not reuse discovery cache: ok=%t targets=%+v", ok, cached)
	}

	writeDarwinProductVersionPlist(t, app, "com.google.antigravity-ide", "2.6.0")
	if cached, ok := cachedDarwinInstallations(false); ok {
		t.Fatalf("bundle version change reused stale discovery cache: %+v", cached)
	}
	current, ok := inspectDarwinAppQuick(app)
	if !ok || current.version != "2.6.0" {
		t.Fatalf("updated bundle version was not read from Info.plist: ok=%t target=%+v", ok, current)
	}
}

func TestDarwinRendererMetadataInvalidatesCacheAndOldRuleNeverReportsConnected(t *testing.T) {
	invalidateDarwinDiscoveryCache()
	t.Cleanup(invalidateDarwinDiscoveryCache)
	endpoint := currentPatchProxyEndpoint().Base
	fixture := newDarwinConnectionFixture(t, "{\n  \"jetski.cloudCodeUrl\": \""+endpoint+"\"\n}\n")
	writeDarwinProductVersionPlist(t, fixture.target.app, "com.google.antigravity-ide", "2.1.0")
	if _, err := applyDarwinSafeIDETarget(fixture.target); err != nil {
		t.Fatalf("prepare current connected fixture: %v", err)
	}
	currentStatus := buildDarwinStatus([]darwinTargets{fixture.target})
	if len(currentStatus.Targets) != 1 || !currentStatus.Targets[0].Patched {
		t.Fatalf("current renderer was not connected before cache test: %+v", currentStatus)
	}
	cacheDarwinInstallations([]darwinTargets{fixture.target}, false)
	if _, ok := cachedDarwinInstallations(false); !ok {
		t.Fatal("unchanged renderer did not reuse discovery cache")
	}

	current, err := os.ReadFile(fixture.rendererPath)
	if err != nil {
		t.Fatal(err)
	}
	legacy := strings.Replace(string(current), imageGenerationDedupePatchMarker, imageGenerationDedupePatchV5Marker, 1)
	if legacy == string(current) {
		t.Fatal("failed to construct legacy renderer")
	}
	if err := os.WriteFile(fixture.rendererPath, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	if cached, ok := cachedDarwinInstallations(false); ok {
		t.Fatalf("renderer metadata change reused stale discovery cache: %+v", cached)
	}
	oldStatus := buildDarwinStatus([]darwinTargets{fixture.target})
	if len(oldStatus.Targets) != 1 || oldStatus.Targets[0].Patched || oldStatus.IDEPatched == nil || *oldStatus.IDEPatched {
		t.Fatalf("old image rule was incorrectly reported connected: %+v", oldStatus)
	}
}

func TestDarwinBundleProductVersionFallsBackToBundleVersionOnly(t *testing.T) {
	app := filepath.Join(t.TempDir(), "Antigravity IDE.app")
	writeDarwinUnpackedDiscoveryFixture(t, app, "com.google.antigravity-ide")
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleIdentifier</key><string>com.google.antigravity-ide</string>
<key>CFBundleVersion</key><string>2.7.3</string>
</dict></plist>`
	if err := os.WriteFile(filepath.Join(app, "Contents", "Info.plist"), []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}
	if version := darwinBundleProductVersion(app); version != "2.7.3" {
		t.Fatalf("bundle version fallback = %q, want 2.7.3", version)
	}
	target, ok := inspectDarwinApp(app)
	if !ok || target.version != "2.7.3" {
		t.Fatalf("inspection did not expose CFBundleVersion fallback: ok=%t target=%+v", ok, target)
	}
}
