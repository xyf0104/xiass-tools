//go:build darwin

package patcher

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDarwinApplicationsInRootRecognizesCommonNamesAcrossCaseStyles(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{
		"Google ANTIGRAVITY 2.0.APP",
		"Agent Window.app",
		"Firefox.app",
	} {
		if err := os.Mkdir(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	want := []string{
		filepath.Join(root, "Agent Window.app"),
		filepath.Join(root, "Google ANTIGRAVITY 2.0.APP"),
	}
	if got := darwinApplicationsInRoot(root); !reflect.DeepEqual(got, want) {
		t.Fatalf("one-level application discovery = %#v, want %#v", got, want)
	}
}

func TestDarwinRunningAppPatternKeepsTheBundleNameBoundary(t *testing.T) {
	if matches := runningAppPattern.FindAllStringSubmatch(
		"/tmp/Antigravity backups/Other.app/Contents/MacOS/Electron", -1,
	); len(matches) != 0 {
		t.Fatalf("parent directory name unexpectedly made another app a candidate: %#v", matches)
	}
	matches := runningAppPattern.FindAllStringSubmatch(
		"/Applications/Google Antigravity 2.0.app/Contents/MacOS/Electron", -1,
	)
	if len(matches) != 1 || len(matches[0]) < 2 || matches[0][1] != "/Applications/Google Antigravity 2.0.app" {
		t.Fatalf("valid running app was not discovered: %#v", matches)
	}
}

func TestSelectDarwinInstallationsDoesNotTreatRuntimePathAsExplicit(t *testing.T) {
	root := t.TempDir()
	trusted := filepath.Join(root, "Antigravity IDE.app")
	custom := filepath.Join(root, "Antigravity copy.app")
	writeDarwinUnpackedDiscoveryFixture(t, trusted, "")
	writeDarwinUnpackedDiscoveryFixture(t, custom, "com.google.antigravity")

	trustedPath := normalizeAppBundlePath(trusted)
	trustOnlyTrusted := func(path string) bool { return path == trustedPath }
	targets := selectDarwinInstallations([]string{custom, trusted}, nil, trustOnlyTrusted)
	if len(targets) != 2 || targets[0].app != trustedPath || targets[1].app != normalizeAppBundlePath(custom) {
		t.Fatalf("a verified custom install should remain available alongside a standard install: %+v", targets)
	}

	// A valid active custom installation remains discoverable when it is the
	// only installation.  It is accepted because its bundle identifier, not
	// merely its filename, confirms the product identity.
	targets = selectDarwinInstallations([]string{custom}, nil, func(string) bool { return false })
	if len(targets) != 1 || targets[0].app != normalizeAppBundlePath(custom) {
		t.Fatalf("sole valid custom install was not discovered: %+v", targets)
	}
}

func TestSelectDarwinInstallationsHonorsExplicitRecoveryPath(t *testing.T) {
	app := filepath.Join(t.TempDir(), "Renamed Recovery Build.app")
	writeDarwinUnpackedDiscoveryFixture(t, app, "")
	normalized := normalizeAppBundlePath(app)
	targets := selectDarwinInstallations(
		[]string{app},
		map[string]bool{normalized: true},
		func(string) bool { return false },
	)
	if len(targets) != 1 || targets[0].app != normalized {
		t.Fatalf("explicit recovery path was not honored: %+v", targets)
	}
}

func TestInspectDarwinUnpackedIDEFindsLanguageServerWithoutExtensionEntry(t *testing.T) {
	app := filepath.Join(t.TempDir(), "Antigravity IDE.app")
	main := filepath.Join(app, "Contents", "Resources", "app", "out", "main.js")
	language := filepath.Join(app, "Contents", "Resources", "app", "extensions", "antigravity", "bin", "language_server_macos_x64")
	for _, path := range []string{main, language} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(main, []byte(`"use strict";const endpoint="`+productionEndpoint+`";`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(language, []byte("MZ\x00"+sandboxEndpoint), 0o755); err != nil {
		t.Fatal(err)
	}

	target, ok := inspectDarwinApp(app)
	if !ok || target.kind != "ide" || target.language != language || target.extensionEntry != "" {
		t.Fatalf("IDE with standalone language server was not identified safely: %+v, ok=%t", target, ok)
	}
}

func TestLocateDarwinLanguageServerPrefersOfficialStandaloneName(t *testing.T) {
	bin := t.TempDir()
	official := filepath.Join(bin, "language_server")
	legacy := filepath.Join(bin, "language_server_macos_x64")
	for _, path := range []string{official, legacy} {
		if err := os.WriteFile(path, []byte("Mach-O fixture"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if got := locateDarwinLanguageServer(bin); got != official {
		t.Fatalf("language server = %q, want official standalone path %q", got, official)
	}
}

func TestOfficialDarwinAgentStructureWhenFixturePresent(t *testing.T) {
	app := strings.TrimSpace(os.Getenv("ANTIGRAVITY_WF_TEST_DARWIN_AGENT_ROOT"))
	if app == "" {
		t.Skip("set ANTIGRAVITY_WF_TEST_DARWIN_AGENT_ROOT to an official read-only Antigravity 2.0.app")
	}
	target, ok := inspectDarwinApp(app)
	if !ok || target.kind != "agent" {
		t.Fatalf("official standalone app was not detected as Agent: ok=%t target=%+v", ok, target)
	}
	if filepath.Base(target.language) != "language_server" {
		t.Fatalf("official Agent language server = %q, want language_server", target.language)
	}
	if !darwinASARHasSupportedEntrypoints(target.asar) {
		t.Fatalf("official Agent ASAR launch chain was not recognized: %s", target.asar)
	}
}

func TestInstalledDarwinAgentConnectionSupportWhenFixturePresent(t *testing.T) {
	app := strings.TrimSpace(os.Getenv("ANTIGRAVITY_WF_TEST_DARWIN_AGENT_ROOT"))
	if app == "" {
		t.Skip("set ANTIGRAVITY_WF_TEST_DARWIN_AGENT_ROOT to a read-only Antigravity 2.0.app")
	}
	target, ok := inspectDarwinApp(app)
	if !ok || target.kind != "agent" {
		t.Fatalf("installed standalone app was not detected as Agent: ok=%t target=%+v", ok, target)
	}
	supported, mode, reason := darwinTargetConnectionSupport(target)
	if !supported || mode != "asar-language-server" {
		t.Fatalf("installed Agent failed production connection gates: supported=%t mode=%q reason=%s", supported, mode, reason)
	}
}

func TestInspectDarwinAppUsesBundleProductVersionInsteadOfInternalPackageVersion(t *testing.T) {
	ideApp := filepath.Join(t.TempDir(), "Antigravity IDE.app")
	writeDarwinUnpackedDiscoveryFixture(t, ideApp, "com.google.antigravity-ide")
	ideRoot := filepath.Join(ideApp, "Contents", "Resources", "app")
	if err := os.WriteFile(filepath.Join(ideRoot, "package.json"), []byte(`{"version":"1.107.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	writeDarwinProductVersionPlist(t, ideApp, "com.google.antigravity-ide", "2.5.5")
	ide, ok := inspectDarwinApp(ideApp)
	if !ok || ide.kind != "ide" || ide.version != "2.5.5" {
		t.Fatalf("IDE product version must come from Info.plist, not package.json: ok=%t target=%+v", ok, ide)
	}

	agentApp := filepath.Join(t.TempDir(), "Antigravity 2.app")
	resources := filepath.Join(agentApp, "Contents", "Resources")
	if err := os.MkdirAll(filepath.Join(resources, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	archive := &asarArchive{root: &asarNode{Files: map[string]*asarNode{}}}
	if err := archive.write(filepath.Join(resources, "app.asar"), map[string][]byte{
		"package.json":           []byte(`{"version":"1.107.0"}`),
		"dist/main.js":           []byte(`"use strict";const keep=true;`),
		"dist/languageServer.js": []byte(`const endpoint="` + productionEndpoint + `";`),
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(resources, "bin", "language_server"), []byte("Mach-O fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDarwinProductVersionPlist(t, agentApp, "com.google.antigravity", "2.8.1")
	agent, ok := inspectDarwinApp(agentApp)
	if !ok || agent.kind != "agent" || agent.version != "2.8.1" {
		t.Fatalf("Agent product version must come from Info.plist, not package.json: ok=%t target=%+v", ok, agent)
	}
}

func TestNormalizeAppBundlePathHandlesQuotedProcessPath(t *testing.T) {
	app := filepath.Join(t.TempDir(), "Antigravity IDE.app")
	input := `"` + filepath.Join(app, "Contents", "MacOS", "Electron") + `"`
	if got, want := normalizeAppBundlePath(input), filepath.Clean(app); got != want {
		t.Fatalf("quoted app process path = %q, want %q", got, want)
	}
}

func TestDarwinCanonicalAppKeyResolvesSymlinkForDeduplication(t *testing.T) {
	root := t.TempDir()
	realApp := filepath.Join(root, "Antigravity IDE.app")
	if err := os.MkdirAll(realApp, 0o755); err != nil {
		t.Fatal(err)
	}
	linkApp := filepath.Join(root, "Antigravity Link.app")
	if err := os.Symlink(realApp, linkApp); err != nil {
		t.Fatal(err)
	}
	if got, want := darwinCanonicalAppKey(normalizeAppBundlePath(linkApp)), darwinCanonicalAppKey(realApp); got != want {
		t.Fatalf("symlink bundle path = %q, want canonical %q", got, want)
	}
}

func writeDarwinUnpackedDiscoveryFixture(t *testing.T, app, bundleIdentifier string) {
	t.Helper()
	main := filepath.Join(app, "Contents", "Resources", "app", "out", "main.js")
	extension := filepath.Join(app, "Contents", "Resources", "app", "extensions", "antigravity", "dist", "extension.js")
	for _, path := range []string{main, extension} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(main, []byte(`"use strict";const endpoint="`+productionEndpoint+`";`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(extension, []byte(`const endpoint=await service.getCloudCodeUrl();`), 0o644); err != nil {
		t.Fatal(err)
	}
	if bundleIdentifier == "" {
		return
	}
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict><key>CFBundleIdentifier</key><string>` + bundleIdentifier + `</string></dict></plist>`
	path := filepath.Join(app, "Contents", "Info.plist")
	if err := os.WriteFile(path, []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeDarwinProductVersionPlist(t *testing.T, app, bundleIdentifier, version string) {
	t.Helper()
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>` +
		`<key>CFBundleIdentifier</key><string>` + bundleIdentifier + `</string>` +
		`<key>CFBundleShortVersionString</key><string>` + version + `</string>` +
		`<key>CFBundleVersion</key><string>` + version + `</string>` +
		`</dict></plist>`
	path := filepath.Join(app, "Contents", "Info.plist")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}
}
