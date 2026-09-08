//go:build darwin

package patcher

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDarwinLauncherPrefersCloudCodeEndpointAndPreservesAPIServerURL(t *testing.T) {
	const apiServer = "https://generativelanguage.googleapis.com"
	const thirdPartyCloudCode = "https://third-party.example.invalid/cloudcode"
	source := `const args=["--api_server_url","` + apiServer + `","--cloud_code_endpoint","` + thirdPartyCloudCode + `"];`
	updated := patchDarwinCloudCodeLauncher(source)
	if !strings.Contains(updated, `"--api_server_url","`+apiServer+`"`) {
		t.Fatalf("native API server URL was not preserved: %s", updated)
	}
	if strings.Contains(updated, thirdPartyCloudCode) || !darwinLauncherHasProxyEndpoint(updated) {
		t.Fatalf("cloud code endpoint was not exclusively routed to the local proxy: %s", updated)
	}
}

func TestDarwinLauncherFallsBackToSingleLegacyAPIServerURL(t *testing.T) {
	source := `const args=["--api_server_url","https://third-party.example.invalid/cloudcode"];`
	updated := patchDarwinCloudCodeLauncher(source)
	if strings.Contains(updated, "third-party.example.invalid") || !darwinLauncherHasProxyEndpoint(updated) {
		t.Fatalf("legacy API-server-only launcher was not routed to the local proxy: %s", updated)
	}
}

func TestDarwinLauncherRejectsDuplicateManagedFlags(t *testing.T) {
	source := `const args=["--cloud_code_endpoint","https://one.invalid","--cloud_code_endpoint","https://two.invalid"];`
	if darwinLauncherHasProxyEndpoint(patchDarwinCloudCodeLauncher(source)) {
		t.Fatalf("duplicate cloud code flags must remain unsupported: %s", source)
	}
}

func TestDarwinPatchApplyStatusAndRestore(t *testing.T) {
	appPath := filepath.Join(t.TempDir(), "Antigravity.app")
	mainPath := filepath.Join(appPath, "Contents", "Resources", "app", "out", "main.js")
	extensionPath := filepath.Join(appPath, "Contents", "Resources", "app", "extensions", "antigravity")
	extensionEntry := filepath.Join(extensionPath, "dist", "extension.js")
	languagePath := filepath.Join(extensionPath, "bin", "language_server_macos_x64")
	for _, dir := range []string{filepath.Dir(mainPath), filepath.Dir(extensionEntry), filepath.Dir(languagePath)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	originalMain := []byte("before " + productionEndpoint + " " + authEligibilityOriginal + " after")
	originalLanguage := []byte("binary\x00" + sandboxEndpoint + "\x00tail")
	if err := os.WriteFile(mainPath, originalMain, 0o644); err != nil {
		t.Fatal(err)
	}
	originalExtension := []byte("/*! For license information please see extension.js.LICENSE.txt */\nconst a=await x.getCloudCodeUrl(),b=await y.getCloudCodeUrl();")
	if err := os.WriteFile(extensionEntry, originalExtension, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(languagePath, originalLanguage, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("ANTIGRAVITY_APP_PATH", appPath)
	t.Setenv("ANTIGRAVITY_WF_BACKUP_DIR", t.TempDir())
	t.Setenv("ANTIGRAVITY_WF_SKIP_CODESIGN", "1")

	targets := locateDarwinTargets()
	if targets.main != mainPath || targets.language != languagePath {
		t.Fatalf("unexpected targets: %+v", targets)
	}
	if out := darwinStatus(targets); strings.Contains(out, "ide_patched=true") {
		t.Fatalf("fresh fixture unexpectedly patched: %s", out)
	}
	if _, err := applyDarwinPatch(targets); err != nil {
		t.Fatal(err)
	}
	if out := darwinStatus(targets); !strings.Contains(out, "ide_patched=true") {
		t.Fatalf("patched fixture not detected: %s", out)
	}

	patchedLanguage, err := os.ReadFile(languagePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(patchedLanguage) != len(originalLanguage) {
		t.Fatalf("binary patch changed file size: %d != %d", len(patchedLanguage), len(originalLanguage))
	}
	if !bytes.Contains(patchedLanguage, []byte(binarySandboxProxyEndpoint)) {
		t.Fatal("sandbox endpoint was not replaced")
	}
	patchedMain, err := os.ReadFile(mainPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(patchedMain, []byte(authEligibilityPatched)) || bytes.Contains(patchedMain, []byte(authEligibilityOriginal)) {
		t.Fatal("local credential auth-state patch was not applied")
	}
	patchedExtension, _ := os.ReadFile(extensionEntry)
	if !bytes.Contains(patchedExtension, []byte(darwinExtensionMarker)) || bytes.Contains(patchedExtension, []byte("getCloudCodeUrl()")) {
		t.Fatal("extension cloud code calls were not patched")
	}

	if _, err := restoreDarwinPatch(targets); err != nil {
		t.Fatal(err)
	}
	restoredMain, _ := os.ReadFile(mainPath)
	restoredExtension, _ := os.ReadFile(extensionEntry)
	restoredLanguage, _ := os.ReadFile(languagePath)
	if !bytes.Equal(restoredMain, originalMain) || !bytes.Equal(restoredExtension, originalExtension) || !bytes.Equal(restoredLanguage, originalLanguage) {
		t.Fatal("restore did not reproduce the original files exactly")
	}
}

func TestDarwinDynamicProxyEndpointPatchesTextBinaryAndState(t *testing.T) {
	restoreEndpoint := setPatchProxyPortForTest(51042)
	t.Cleanup(restoreEndpoint)
	endpoint := currentPatchProxyEndpoint()

	mainPath := filepath.Join(t.TempDir(), "main.js")
	mainOriginal := []byte(`"use strict";const endpoint="` + productionEndpoint + `";` + authEligibilityOriginal)
	if err := os.WriteFile(mainPath, mainOriginal, 0o644); err != nil {
		t.Fatal(err)
	}
	mainPlan, err := prepareDarwinMainPatch(mainPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(mainPlan.updated, []byte(endpoint.Text)) || bytes.Contains(mainPlan.updated, []byte(textProxyEndpoint)) {
		t.Fatalf("dynamic main endpoint missing: %s", mainPlan.updated)
	}
	if err := os.WriteFile(mainPath, mainPlan.updated, 0o644); err != nil {
		t.Fatal(err)
	}
	if !darwinMainPatched(mainPath) {
		t.Fatal("dynamic main patch was not recognized as patched")
	}

	languagePath := filepath.Join(t.TempDir(), "language_server_macos_x64")
	languageOriginal := []byte("binary\x00" + sandboxEndpoint + "\x00tail")
	if err := os.WriteFile(languagePath, languageOriginal, 0o755); err != nil {
		t.Fatal(err)
	}
	languagePlan, err := preparePatch(languagePath, []byteReplacement{
		{old: []byte(productionEndpoint), new: []byte(endpoint.Binary)},
		{old: []byte(sandboxEndpoint), new: []byte(endpoint.BinarySandbox)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(languagePlan.updated) != len(languageOriginal) {
		t.Fatalf("dynamic Language Server patch changed byte length: %d != %d", len(languagePlan.updated), len(languageOriginal))
	}
	if !bytes.Contains(languagePlan.updated, []byte(endpoint.BinarySandbox)) {
		t.Fatalf("dynamic Language Server endpoint missing: %q", languagePlan.updated)
	}
	if !containsKnownDarwinPatch([]byte(endpoint.Base + "/v1internal/xxxxx")) {
		t.Fatal("dynamic legacy endpoint was not protected as an existing helper patch")
	}
}

func TestDarwinPortMigrationRebuildsEveryEndpointFromCurrentState(t *testing.T) {
	restoreEndpoint := setPatchProxyPortForTest(51042)
	t.Cleanup(restoreEndpoint)
	endpoint := currentPatchProxyEndpoint()
	appPath := filepath.Join(t.TempDir(), "Antigravity IDE.app")
	mainPath := filepath.Join(appPath, "Contents", "Resources", "app", "out", "main.js")
	extensionPath := filepath.Join(appPath, "Contents", "Resources", "app", "extensions", "antigravity", "dist", "extension.js")
	languagePath := filepath.Join(appPath, "Contents", "Resources", "app", "extensions", "antigravity", "bin", "language_server_macos_x64")
	for _, path := range []string{mainPath, extensionPath, languagePath} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cleanMain := []byte(`"use strict";const endpoint="` + productionEndpoint + `";` + authEligibilityOriginal)
	cleanExtension := []byte(`const endpoint=await service.getCloudCodeUrl();`)
	cleanLanguage := []byte("binary\x00" + sandboxEndpoint + "\x00tail")
	if err := os.WriteFile(mainPath, cleanMain, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(extensionPath, cleanExtension, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(languagePath, cleanLanguage, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANTIGRAVITY_WF_BACKUP_DIR", t.TempDir())
	t.Setenv("ANTIGRAVITY_WF_SKIP_CODESIGN", "1")
	for _, path := range []string{mainPath, extensionPath, languagePath} {
		if err := writeFileBackup(path); err != nil {
			t.Fatal(err)
		}
	}

	// Emulate P: a previously valid helper install whose endpoint is no longer
	// available. The migration backs up P itself before moving it to Q.
	legacyMain := []byte(`"use strict";` + "\n// " + darwinExtensionMarker + `
const endpoint="` + textProxyEndpoint + `";` + authEligibilityPatched)
	legacyExtension := []byte("// " + darwinExtensionMarker + "\nconst endpoint=\"" + baseProxyEndpoint + "\";")
	legacyLanguage := []byte("binary\x00" + binarySandboxProxyEndpoint + "\x00tail")
	if err := os.WriteFile(mainPath, legacyMain, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(extensionPath, legacyExtension, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(languagePath, legacyLanguage, 0o755); err != nil {
		t.Fatal(err)
	}
	target := darwinTargets{app: appPath, name: "Antigravity IDE", kind: "ide", main: mainPath, extensionEntry: extensionPath, language: languagePath}
	if _, err := applyDarwinPatch(target); err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		path string
		want string
	}{
		{mainPath, endpoint.Text},
		{extensionPath, endpoint.Base},
		{languagePath, endpoint.BinarySandbox},
	} {
		data, err := os.ReadFile(check.path)
		if err != nil || !bytes.Contains(data, []byte(check.want)) {
			t.Fatalf("%s did not migrate to selected endpoint %q: %v", check.path, check.want, err)
		}
	}
	assertFileEquals(t, backupPath(mainPath), legacyMain)
	assertFileEquals(t, backupPath(extensionPath), legacyExtension)
	assertFileEquals(t, backupPath(languagePath), legacyLanguage)
}

func TestDarwinUnpackedImagePreviewPatchApplyAndRestore(t *testing.T) {
	appPath := filepath.Join(t.TempDir(), "Antigravity.app")
	appRoot := filepath.Join(appPath, "Contents", "Resources", "app")
	mainPath := filepath.Join(appRoot, "out", "main.js")
	extensionEntry := filepath.Join(appRoot, "extensions", "antigravity", "dist", "extension.js")
	languagePath := filepath.Join(appRoot, "extensions", "antigravity", "bin", "language_server_macos_x64")
	for _, path := range append([]string{mainPath, extensionEntry, languagePath}, imagePreviewRendererPaths(appRoot)...) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Create all known renderer files before obtaining the target-specific
	// paths. The main bundle also carries the ordinary endpoint/auth patch.
	originals := map[string][]byte{
		mainPath:       []byte(`"use strict";const endpoint="` + productionEndpoint + `";` + authEligibilityOriginal + imagePreviewOriginalRendererFixture()),
		extensionEntry: []byte("/*! For license information please see extension.js.LICENSE.txt */\nconst endpoint=await service.getCloudCodeUrl();"),
		languagePath:   []byte("binary\x00" + sandboxEndpoint + "\x00tail"),
	}
	for index, relative := range imagePreviewRendererRelativePaths[1:] {
		path := filepath.Join(appRoot, filepath.FromSlash(relative))
		if index == 0 {
			originals[path] = []byte(imagePreviewOriginalRendererFixture())
		} else {
			// A future renderer must not prevent the standard endpoint patch
			// or cause a broad rewrite of an unknown bundle.
			originals[path] = []byte(`const futureRenderer={generatedMedia:"different-shape"};`)
		}
	}
	for path, data := range originals {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0o644)
		if path == languagePath {
			mode = 0o755
		}
		if err := os.WriteFile(path, data, mode); err != nil {
			t.Fatal(err)
		}
	}

	t.Setenv("ANTIGRAVITY_APP_PATH", appPath)
	t.Setenv("ANTIGRAVITY_APP_PATHS", "")
	t.Setenv("ANTIGRAVITY_WF_BACKUP_DIR", t.TempDir())
	t.Setenv("ANTIGRAVITY_WF_SKIP_CODESIGN", "1")
	targets := locateDarwinInstallations()
	if len(targets) != 1 || targets[0].kind != "ide" {
		t.Fatalf("unexpected target: %+v", targets)
	}
	if _, err := applyDarwinPatch(targets[0]); err != nil {
		t.Fatal(err)
	}
	for _, path := range imagePreviewRendererPaths(appRoot) {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		knownRenderer := path == mainPath || strings.Contains(filepath.ToSlash(path), "/jetskiAgent/main.js")
		if knownRenderer && !bytes.Contains(data, []byte(imagePreviewPatchMarker)) {
			t.Fatalf("renderer was not patched: %s", path)
		}
		if !knownRenderer && bytes.Contains(data, []byte(imagePreviewPatchMarker)) {
			t.Fatalf("unknown renderer was unexpectedly rewritten: %s", path)
		}
		if knownRenderer {
			if _, err := os.Stat(backupPath(path)); err != nil {
				t.Fatalf("renderer backup is missing for %s: %v", path, err)
			}
		}
	}
	if _, err := restoreDarwinPatch(targets[0]); err != nil {
		t.Fatal(err)
	}
	for path, original := range originals {
		assertFileEquals(t, path, original)
	}
}

func TestDarwinCodesignRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("clang"); err != nil {
		t.Skip("clang is unavailable")
	}
	if _, err := exec.LookPath("codesign"); err != nil {
		t.Skip("codesign is unavailable")
	}

	appPath := filepath.Join(t.TempDir(), "Antigravity.app")
	contentsPath := filepath.Join(appPath, "Contents")
	mainPath := filepath.Join(contentsPath, "Resources", "app", "out", "main.js")
	extensionPath := filepath.Join(contentsPath, "Resources", "app", "extensions", "antigravity")
	extensionEntry := filepath.Join(extensionPath, "dist", "extension.js")
	languagePath := filepath.Join(extensionPath, "bin", "language_server_macos_x64")
	electronPath := filepath.Join(contentsPath, "MacOS", "Electron")
	for _, dir := range []string{filepath.Dir(mainPath), filepath.Dir(extensionEntry), filepath.Dir(languagePath), filepath.Dir(electronPath)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	originalMain := []byte("const endpoint = '" + productionEndpoint + "';" + authEligibilityOriginal)
	if err := os.WriteFile(mainPath, originalMain, 0o644); err != nil {
		t.Fatal(err)
	}
	originalExtension := []byte("/*! For license information please see extension.js.LICENSE.txt */\nconst a=await x.getCloudCodeUrl(),b=await y.getCloudCodeUrl();")
	if err := os.WriteFile(extensionEntry, originalExtension, 0o644); err != nil {
		t.Fatal(err)
	}
	compileFixtureBinary(t, electronPath, "fixture")
	compileFixtureBinary(t, languagePath, sandboxEndpoint)
	infoPlist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleExecutable</key><string>Electron</string>
<key>CFBundleIdentifier</key><string>test.antigravity</string>
<key>CFBundlePackageType</key><string>APPL</string>
</dict></plist>`
	if err := os.WriteFile(filepath.Join(contentsPath, "Info.plist"), []byte(infoPlist), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestCommand(t, "codesign", "--force", "--sign", "-", languagePath)
	runTestCommand(t, "codesign", "--force", "--deep", "--sign", "-", appPath)
	runTestCommand(t, "codesign", "--verify", "--deep", "--strict", appPath)

	originalLanguage, _ := os.ReadFile(languagePath)
	originalElectron, _ := os.ReadFile(electronPath)
	codeResourcesPath := filepath.Join(contentsPath, "_CodeSignature", "CodeResources")
	originalCodeResources, _ := os.ReadFile(codeResourcesPath)

	t.Setenv("ANTIGRAVITY_APP_PATH", appPath)
	t.Setenv("ANTIGRAVITY_WF_BACKUP_DIR", t.TempDir())
	t.Setenv("ANTIGRAVITY_WF_SKIP_CODESIGN", "")
	targets := locateDarwinTargets()
	if _, err := applyDarwinPatch(targets); err != nil {
		t.Fatal(err)
	}
	// The bundle resource seal is intentionally stale while patched so the
	// top-level executable keeps its original code identity and Keychain access.
	runTestCommand(t, "codesign", "--verify", "--strict", languagePath)
	assertFileEquals(t, electronPath, originalElectron)
	assertFileEquals(t, codeResourcesPath, originalCodeResources)

	if _, err := restoreDarwinPatch(targets); err != nil {
		t.Fatal(err)
	}
	runTestCommand(t, "codesign", "--verify", "--deep", "--strict", appPath)

	assertFileEquals(t, mainPath, originalMain)
	assertFileEquals(t, extensionEntry, originalExtension)
	assertFileEquals(t, languagePath, originalLanguage)
	assertFileEquals(t, electronPath, originalElectron)
	assertFileEquals(t, codeResourcesPath, originalCodeResources)
}

func compileFixtureBinary(t *testing.T, outputPath, marker string) {
	t.Helper()
	source := fmt.Sprintf("#include <stdio.h>\nint main(void) { puts(%q); return 0; }\n", marker)
	cmd := exec.Command("clang", "-x", "c", "-", "-o", outputPath)
	cmd.Stdin = strings.NewReader(source)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clang failed: %s: %v", out, err)
	}
}

func runTestCommand(t *testing.T, name string, args ...string) {
	t.Helper()
	if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
		t.Fatalf("%s %v failed: %s: %v", name, args, out, err)
	}
}

func assertFileEquals(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s was not restored byte-for-byte", path)
	}
}

func TestWriteBackupPreservesFirstOriginal(t *testing.T) {
	t.Setenv("ANTIGRAVITY_WF_BACKUP_DIR", t.TempDir())
	sourcePath := filepath.Join(t.TempDir(), "main.js")
	first := []byte("first-original")
	if err := writeBackup(sourcePath, first); err != nil {
		t.Fatal(err)
	}
	if err := writeBackup(sourcePath, []byte("later-patched-version")); err != nil {
		t.Fatal(err)
	}
	assertFileEquals(t, backupPath(sourcePath), first)
}

func TestCurrentBackupRotatesAfterApplicationUpdate(t *testing.T) {
	backupDir := t.TempDir()
	t.Setenv("ANTIGRAVITY_WF_BACKUP_DIR", backupDir)
	sourcePath := filepath.Join(t.TempDir(), "main.js")
	first := []byte("version-one-original")
	second := []byte("version-two-original")
	if err := os.WriteFile(sourcePath, first, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeCurrentBackup(sourcePath, first); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sourcePath, second, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeCurrentBackup(sourcePath, second); err != nil {
		t.Fatal(err)
	}
	assertFileEquals(t, backupPath(sourcePath), second)
	matches, _ := filepath.Glob(filepath.Join(backupDir, "main.js-*.previous-*.bak"))
	if len(matches) != 1 {
		t.Fatalf("previous backup was not archived: %v", matches)
	}
	assertFileEquals(t, matches[0], first)
}

func TestDarwinBackupMigrationPreservesAndRepairsLanguageServerExecutableMode(t *testing.T) {
	for _, test := range []struct {
		name       string
		activeMode os.FileMode
	}{
		{name: "preserves executable destination", activeMode: 0o755},
		{name: "repairs v1.5.2 permission regression", activeMode: 0o600},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			active := filepath.Join(root, "language_server")
			backup := filepath.Join(root, "language_server.clean.bak")
			if err := os.WriteFile(active, []byte("binary\x00"+legacyBinaryProxyEndpoint+"\x00tail"), test.activeMode); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(backup, []byte("binary\x00"+productionEndpoint+"\x00tail"), 0o600); err != nil {
				t.Fatal(err)
			}
			plan, _, err := prepareDarwinLanguagePatch(backup)
			if err != nil {
				t.Fatal(err)
			}
			if err := bindDarwinPatchPlanDestination(plan, active, true); err != nil {
				t.Fatal(err)
			}
			if err := writePatchPlans([]*patchPlan{plan}); err != nil {
				t.Fatal(err)
			}
			if err := verifyDarwinExecutable(active); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(active)
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != 0o755 {
				t.Fatalf("language_server mode=%04o want=0755", got)
			}
			data, err := os.ReadFile(active)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(data, []byte(legacyBinaryProxyEndpoint)) || !bytes.Contains(data, []byte(binaryProxyEndpoint)) {
				t.Fatalf("language_server endpoint was not migrated: %q", data)
			}
		})
	}
}

func TestFinishingPartialPatchCapturesCurrentRestorePoint(t *testing.T) {
	t.Setenv("ANTIGRAVITY_WF_BACKUP_DIR", t.TempDir())
	sourcePath := filepath.Join(t.TempDir(), "main.js")
	clean := []byte("const endpoint='" + productionEndpoint + "';")
	partial := []byte("const endpoint='" + textProxyEndpoint + "'; dynamic-app-data")
	if err := os.WriteFile(sourcePath, partial, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeBackup(sourcePath, clean); err != nil {
		t.Fatal(err)
	}
	plan := &patchPlan{path: sourcePath, original: partial, updated: append([]byte(nil), partial...), mode: 0o644, changed: true}
	if err := saveApplyBackups([]*patchPlan{plan}, nil); err != nil {
		t.Fatal(err)
	}
	assertFileEquals(t, backupPath(sourcePath), partial)
}

func TestDarwinKnownPatchDetectionIncludesEveryReleasedImageMarker(t *testing.T) {
	for _, marker := range []string{
		imagePreviewPatchV2Marker, imagePreviewPatchV3Marker,
		imagePreviewPatchV4Marker, imagePreviewPatchV5Marker,
		imagePreviewPatchV6Marker, imagePreviewPatchV7Marker,
		imagePreviewPatchMarker,
		imageGenerationUIPatchV1Marker, imageGenerationUIPatchV2Marker,
		imageGenerationUIPatchMarker, imageGenerationDedupePatchMarker,
	} {
		if !containsKnownDarwinPatch([]byte("/*" + marker + "*/")) {
			t.Fatalf("known image marker was not protected by backup detection: %s", marker)
		}
	}
}

func TestDarwinLegacyImageMarkerCapturesCurrentRestorePoint(t *testing.T) {
	markers := []string{
		imagePreviewPatchV2Marker, imagePreviewPatchV3Marker,
		imagePreviewPatchV4Marker, imagePreviewPatchV5Marker,
		imagePreviewPatchV6Marker, imagePreviewPatchV7Marker,
		imagePreviewPatchMarker,
		imageGenerationUIPatchV1Marker, imageGenerationUIPatchV2Marker,
		imageGenerationUIPatchMarker,
	}
	for _, marker := range markers {
		t.Run(marker, func(t *testing.T) {
			t.Setenv("ANTIGRAVITY_WF_BACKUP_DIR", t.TempDir())
			sourcePath := filepath.Join(t.TempDir(), "renderer.js")
			clean := []byte("clean renderer source")
			legacy := []byte("/*" + marker + "*/ legacy renderer source")
			if err := writeBackup(sourcePath, clean); err != nil {
				t.Fatal(err)
			}
			plan := &patchPlan{path: sourcePath, original: legacy, updated: append([]byte(nil), legacy...), mode: 0o644, changed: true}
			if err := saveApplyBackups([]*patchPlan{plan}, nil); err != nil {
				t.Fatal(err)
			}
			assertFileEquals(t, backupPath(sourcePath), legacy)
		})
	}
}

func TestDarwinLegacyImageMarkerWithoutBackupCreatesCurrentSnapshot(t *testing.T) {
	t.Setenv("ANTIGRAVITY_WF_BACKUP_DIR", t.TempDir())
	sourcePath := filepath.Join(t.TempDir(), "renderer.js")
	legacy := []byte("/*" + imagePreviewPatchV6Marker + "*/ legacy renderer source")
	plan := &patchPlan{path: sourcePath, original: legacy, updated: append([]byte(nil), legacy...), mode: 0o644, changed: true}
	if err := saveApplyBackups([]*patchPlan{plan}, nil); err != nil {
		t.Fatalf("legacy renderer current-state backup failed: %v", err)
	}
	assertFileEquals(t, backupPath(sourcePath), legacy)
}

func TestDarwinExplicitPathNeverFallsBack(t *testing.T) {
	t.Setenv("ANTIGRAVITY_APP_PATH", filepath.Join(t.TempDir(), "Missing.app"))
	t.Setenv("ANTIGRAVITY_APP_PATHS", "")
	if targets := locateDarwinInstallations(); len(targets) != 0 {
		t.Fatalf("explicit missing path fell back to another install: %+v", targets)
	}
}

func TestDarwinASARApplyStatusAndRestore(t *testing.T) {
	appPath := filepath.Join(t.TempDir(), "Antigravity 2.0.app")
	resources := filepath.Join(appPath, "Contents", "Resources")
	asarPath := filepath.Join(resources, "app.asar")
	languagePath := filepath.Join(resources, "bin", "language_server_macos_x64")
	if err := os.MkdirAll(filepath.Dir(languagePath), 0o755); err != nil {
		t.Fatal(err)
	}
	fixture := &asarArchive{root: &asarNode{Files: map[string]*asarNode{}}}
	originalMain := []byte("\"use strict\";\nconst endpoint=\"" + productionEndpoint + "\";")
	originalLauncher := []byte("args.push('--cloud_code_endpoint','" + productionEndpoint + "')")
	if err := fixture.write(asarPath, map[string][]byte{
		"dist/main.js":               originalMain,
		"dist/languageServer.js":     originalLauncher,
		"out/jetskiAgent/main.js":    []byte(imagePreviewOriginalRendererFixture()),
		"dist/unrelated-renderer.js": []byte(imagePreviewOriginalRendererFixture()),
	}); err != nil {
		t.Fatal(err)
	}
	originalPlist := writeDarwinAgentIntegrityFixture(t, appPath, asarPath)
	originalASAR, _ := os.ReadFile(asarPath)
	originalLanguage := []byte("binary\x00" + sandboxEndpoint + "\x00tail")
	if err := os.WriteFile(languagePath, originalLanguage, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("ANTIGRAVITY_APP_PATH", appPath)
	t.Setenv("ANTIGRAVITY_APP_PATHS", "")
	t.Setenv("ANTIGRAVITY_WF_BACKUP_DIR", t.TempDir())
	t.Setenv("ANTIGRAVITY_WF_SKIP_CODESIGN", "1")
	targets := locateDarwinInstallations()
	if len(targets) != 1 || targets[0].kind != "agent" || targets[0].asar != asarPath {
		t.Fatalf("unexpected ASAR target: %+v", targets)
	}
	if _, err := applyDarwinPatch(targets[0]); err != nil {
		t.Fatal(err)
	}
	if !darwinASARPatched(asarPath) {
		t.Fatal("patched ASAR was not detected")
	}
	patchedArchive, err := readASAR(asarPath)
	if err != nil {
		t.Fatal(err)
	}
	previewData, err := patchedArchive.readFile("out/jetskiAgent/main.js")
	if err != nil || !bytes.Contains(previewData, []byte(imagePreviewPatchMarker)) {
		t.Fatalf("ASAR image-preview renderer was not patched: %v", err)
	}
	if _, _, _, patched := darwinTargetPatchState(targets[0]); !patched {
		t.Fatal("ASAR target was not fully patched")
	}
	if _, err := restoreDarwinPatch(targets[0]); err != nil {
		t.Fatal(err)
	}
	restoredASAR, _ := os.ReadFile(asarPath)
	restoredLanguage, _ := os.ReadFile(languagePath)
	if !bytes.Equal(restoredASAR, originalASAR) || !bytes.Equal(restoredLanguage, originalLanguage) {
		t.Fatal("ASAR installation was not restored byte-for-byte")
	}
	assertFileEquals(t, filepath.Join(appPath, "Contents", "Info.plist"), originalPlist)
}

// TestDarwinASARImagePreviewV3UpgradeCapturesCurrentRestorePoint covers an
// installation with the earlier v3 renderer and no dependency on vendor
// bytes. Restore returns to the exact v3/third-party state present at upgrade.
func TestDarwinASARImagePreviewV3UpgradeCapturesCurrentRestorePoint(t *testing.T) {
	root := t.TempDir()
	appPath := filepath.Join(root, "Antigravity 2.0.app")
	resources := filepath.Join(appPath, "Contents", "Resources")
	asarPath := filepath.Join(resources, "app.asar")
	languagePath := filepath.Join(resources, "bin", "language_server_macos_x64")
	if err := os.MkdirAll(filepath.Dir(languagePath), 0o755); err != nil {
		t.Fatal(err)
	}

	cleanArchive := &asarArchive{root: &asarNode{Files: map[string]*asarNode{}}}
	if err := cleanArchive.write(asarPath, map[string][]byte{
		"dist/main.js":               []byte(`"use strict";const endpoint="` + productionEndpoint + `";`),
		"dist/languageServer.js":     []byte(`const endpoint="` + productionEndpoint + `";`),
		"out/jetskiAgent/main.js":    []byte(imagePreviewOriginalRendererFixture()),
		"dist/unrelated-renderer.js": []byte(`const untouched=true;`),
	}); err != nil {
		t.Fatal(err)
	}
	cleanPlist := writeDarwinAgentIntegrityFixture(t, appPath, asarPath)
	cleanASAR, err := os.ReadFile(asarPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(languagePath, []byte("binary\x00"+binarySandboxProxyEndpoint+"\x00tail"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("ANTIGRAVITY_WF_BACKUP_DIR", t.TempDir())
	t.Setenv("ANTIGRAVITY_WF_SKIP_CODESIGN", "1")
	// Seed an older backup to prove the next upgrade rotates it to history.
	if err := writeBackup(asarPath, cleanASAR); err != nil {
		t.Fatal(err)
	}

	// S1: emulate the old helper's fully endpoint-patched ASAR with its v3
	// image renderer.
	legacyArchive, err := readASAR(asarPath)
	if err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(resources, "legacy.app.asar")
	if err := legacyArchive.write(legacyPath, map[string][]byte{
		"dist/main.js":            []byte(`"use strict";` + "\n// " + darwinASARMarker + `\nconst endpoint="` + textProxyEndpoint + `";`),
		"dist/languageServer.js":  []byte(`const endpoint="` + baseProxyEndpoint + `";`),
		"out/jetskiAgent/main.js": []byte(imagePreviewV3RendererFixture()),
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(legacyPath, asarPath); err != nil {
		t.Fatal(err)
	}
	legacyASAR, err := os.ReadFile(asarPath)
	if err != nil {
		t.Fatal(err)
	}
	if !darwinASARPatched(asarPath) || !imagePreviewASARArchiveNeedsPatch(asarPath) {
		t.Fatal("legacy v3 ASAR fixture was not recognized as an upgrade candidate")
	}
	assertFileEquals(t, backupPath(asarPath), cleanASAR)

	target := darwinTargets{
		app: appPath, name: "Antigravity 2.0", kind: "agent", asar: asarPath, language: languagePath,
	}
	if _, err := applyDarwinPatch(target); err != nil {
		t.Fatal(err)
	}
	if _, _, _, patched := darwinTargetPatchState(target); !patched {
		t.Fatal("v4-migrated ASAR target was not reported as fully patched")
	}
	// The current v3 archive becomes this upgrade's exact restore point.
	assertFileEquals(t, backupPath(asarPath), legacyASAR)
	upgraded, err := readASAR(asarPath)
	if err != nil {
		t.Fatal(err)
	}
	renderer, err := upgraded.readFile("out/jetskiAgent/main.js")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(renderer, []byte(imagePreviewPatchMarker)) || bytes.Contains(renderer, []byte(imagePreviewPatchV3Marker)) {
		t.Fatalf("v3 renderer was not upgraded to v4: %s", renderer)
	}

	if _, err := restoreDarwinPatch(target); err != nil {
		t.Fatal(err)
	}
	assertFileEquals(t, asarPath, legacyASAR)
	assertFileEquals(t, filepath.Join(appPath, "Contents", "Info.plist"), cleanPlist)
}

// TestDarwinASARLegacyWFUpgradeWhenPlistTracksPatchedArchive covers released
// helper builds that updated ElectronAsarIntegrity to their already-patched
// app.asar. That active archive is a supported upgrade and restore input.
func TestDarwinASARLegacyWFUpgradeWhenPlistTracksPatchedArchive(t *testing.T) {
	root := t.TempDir()
	appPath := filepath.Join(root, "Antigravity 2.0.app")
	resources := filepath.Join(appPath, "Contents", "Resources")
	asarPath := filepath.Join(resources, "app.asar")
	languagePath := filepath.Join(resources, "bin", "language_server")
	if err := os.MkdirAll(filepath.Dir(languagePath), 0o755); err != nil {
		t.Fatal(err)
	}

	cleanArchive := &asarArchive{root: &asarNode{Files: map[string]*asarNode{}}}
	if err := cleanArchive.write(asarPath, map[string][]byte{
		"dist/main.js":            []byte(`"use strict";const endpoint="` + productionEndpoint + `";`),
		"dist/languageServer.js":  []byte(`args.push("--cloud_code_endpoint","` + productionEndpoint + `")`),
		"out/jetskiAgent/main.js": []byte(imagePreviewOriginalRendererFixture() + ";" + imageGenerationUIRendererFixture()),
	}); err != nil {
		t.Fatal(err)
	}
	writeDarwinAgentIntegrityFixture(t, appPath, asarPath)
	cleanASAR, err := os.ReadFile(asarPath)
	if err != nil {
		t.Fatal(err)
	}
	writeDarwinAgentLanguageServerUIFixture(t, languagePath)

	t.Setenv("ANTIGRAVITY_WF_BACKUP_DIR", t.TempDir())
	t.Setenv("ANTIGRAVITY_WF_SKIP_CODESIGN", "1")
	// The synthetic Language Server reuses the Go test Mach-O and therefore
	// contains patch-marker constants from the test binary itself. Seed its
	// fixture backup so that this test also proves the next upgrade replaces it
	// with the then-active state.
	cleanLanguage, err := os.ReadFile(languagePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(backupPath(languagePath), cleanLanguage, 0o600); err != nil {
		t.Fatal(err)
	}
	target := darwinTargets{
		app: appPath, name: "Antigravity 2.0", kind: "agent", version: "2.6.0",
		asar: asarPath, language: languagePath,
	}
	if _, err := applyDarwinASARPatch(target); err != nil {
		t.Fatalf("initial WF patch failed: %v", err)
	}
	assertFileEquals(t, backupPath(asarPath), cleanASAR)

	// Emulate an older WF release whose active ASAR uses the historical marker
	// and whose Info.plist was synchronised to that patched archive (state B).
	vendorArchive, err := readASAR(backupPath(asarPath))
	if err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(resources, "legacy.app.asar")
	if err := vendorArchive.write(legacyPath, map[string][]byte{
		"dist/main.js": []byte(`"use strict";` + "\n// " + legacyDarwinASARMarker +
			`\nconst endpoint="` + legacyTextProxyEndpoint + `";`),
		"dist/languageServer.js": []byte(`args.push("--cloud_code_endpoint","` + legacyBinaryProxyEndpoint + `")`),
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(legacyPath, asarPath); err != nil {
		t.Fatal(err)
	}
	legacyHash, err := darwinASARHeaderHash(asarPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appPath, "Contents", "Info.plist"), darwinIntegrityPlist(legacyHash), 0o644); err != nil {
		t.Fatal(err)
	}
	if !darwinASARContainsKnownPatch(asarPath) {
		t.Fatal("legacy WF archive was not recognized as a managed upgrade input")
	}
	legacyASAR, err := os.ReadFile(asarPath)
	if err != nil {
		t.Fatal(err)
	}
	legacyPlist := darwinIntegrityPlist(legacyHash)
	if supported, _, reason := darwinTargetConnectionSupport(target); !supported {
		t.Fatalf("verified legacy WF application was incorrectly rejected: %s", reason)
	}

	if _, err := applyDarwinASARPatch(target); err != nil {
		t.Fatalf("legacy WF upgrade failed: %v", err)
	}
	if !darwinASARPatched(asarPath) {
		t.Fatal("legacy WF archive was not upgraded to the current patch")
	}
	if err := verifyDarwinAgentASARIntegrity(target); err != nil {
		t.Fatalf("upgraded archive and Info.plist do not match: %v", err)
	}
	activeArchive, err := readASAR(asarPath)
	if err != nil {
		t.Fatal(err)
	}
	activeMain, err := activeArchive.readFile("dist/main.js")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(activeMain, []byte(legacyDarwinASARMarker)) || !bytes.Contains(activeMain, []byte(darwinASARMarker)) {
		t.Fatalf("legacy marker was not replaced by the current WF marker: %s", activeMain)
	}
	assertFileEquals(t, backupPath(asarPath), legacyASAR)

	if _, err := restoreDarwinPatch(target); err != nil {
		t.Fatal(err)
	}
	assertFileEquals(t, asarPath, legacyASAR)
	assertFileEquals(t, filepath.Join(appPath, "Contents", "Info.plist"), legacyPlist)
}

// TestDarwinASARImagePreviewUpgradeRollbackKeepsActiveArchive protects the
// other half of an upgrade: if a late step fails after app.asar has already
// been replaced, the transaction must restore the pre-upgrade v3 archive (S1)
// and keep that S1 state as the current user-visible restore point.
func TestDarwinASARImagePreviewUpgradeRollbackKeepsActiveArchive(t *testing.T) {
	root := t.TempDir()
	appPath := filepath.Join(root, "Antigravity 2.0.app")
	resources := filepath.Join(appPath, "Contents", "Resources")
	asarPath := filepath.Join(resources, "app.asar")
	languagePath := filepath.Join(resources, "bin", "language_server_macos_x64")
	if err := os.MkdirAll(filepath.Dir(languagePath), 0o755); err != nil {
		t.Fatal(err)
	}

	cleanArchive := &asarArchive{root: &asarNode{Files: map[string]*asarNode{}}}
	if err := cleanArchive.write(asarPath, map[string][]byte{
		"dist/main.js":            []byte(`"use strict";const endpoint="` + productionEndpoint + `";`),
		"dist/languageServer.js":  []byte(`const endpoint="` + productionEndpoint + `";`),
		"out/jetskiAgent/main.js": []byte(imagePreviewOriginalRendererFixture()),
	}); err != nil {
		t.Fatal(err)
	}
	cleanPlist := writeDarwinAgentIntegrityFixture(t, appPath, asarPath)
	cleanASAR, err := os.ReadFile(asarPath)
	if err != nil {
		t.Fatal(err)
	}

	t.Setenv("ANTIGRAVITY_WF_BACKUP_DIR", t.TempDir())
	t.Setenv("ANTIGRAVITY_WF_SKIP_CODESIGN", "1")
	if err := writeBackup(asarPath, cleanASAR); err != nil {
		t.Fatal(err)
	}
	clean, err := readASAR(asarPath)
	if err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(resources, "legacy.app.asar")
	if err := clean.write(legacyPath, map[string][]byte{
		"dist/main.js":            []byte(`"use strict";` + "\n// " + darwinASARMarker + `\nconst endpoint="` + textProxyEndpoint + `";`),
		"dist/languageServer.js":  []byte(`const endpoint="` + baseProxyEndpoint + `";`),
		"out/jetskiAgent/main.js": []byte(imagePreviewV3RendererFixture()),
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(legacyPath, asarPath); err != nil {
		t.Fatal(err)
	}
	legacyASAR, err := os.ReadFile(asarPath)
	if err != nil {
		t.Fatal(err)
	}
	originalLanguage := []byte("binary\x00" + sandboxEndpoint + "\x00tail")
	if err := os.WriteFile(languagePath, originalLanguage, 0o755); err != nil {
		t.Fatal(err)
	}

	previousSigner := signDarwinLanguageServer
	signDarwinLanguageServer = func(string) error {
		return fmt.Errorf("synthetic post-ASAR signing failure")
	}
	t.Cleanup(func() { signDarwinLanguageServer = previousSigner })

	target := darwinTargets{
		app: appPath, name: "Antigravity 2.0", kind: "agent", asar: asarPath, language: languagePath,
	}
	if _, err := applyDarwinPatch(target); err == nil || !strings.Contains(err.Error(), "synthetic post-ASAR signing failure") {
		t.Fatalf("expected injected post-ASAR failure, got %v", err)
	}
	assertFileEquals(t, asarPath, legacyASAR)
	assertFileEquals(t, backupPath(asarPath), legacyASAR)
	assertFileEquals(t, languagePath, originalLanguage)
	assertFileEquals(t, filepath.Join(appPath, "Contents", "Info.plist"), cleanPlist)
}

// TestDarwinASARUnpackedImagePreviewPatchApplyAndRestore verifies the package
// layout used when a known renderer is declared `unpacked` in app.asar. It is
// deliberately an end-to-end apply/status/restore test: archive.write must
// preserve the manifest node, the external renderer must receive v4, and both
// files must return byte-for-byte to their initial state.
func TestDarwinASARUnpackedImagePreviewPatchApplyAndRestore(t *testing.T) {
	root := t.TempDir()
	appPath := filepath.Join(root, "Antigravity 2.0.app")
	resources := filepath.Join(appPath, "Contents", "Resources")
	asarPath := filepath.Join(resources, "app.asar")
	languagePath := filepath.Join(resources, "bin", "language_server_macos_x64")
	externalRendererPath := filepath.Join(asarPath+".unpacked", "dist", "jetskiAgent", "main.js")
	if err := os.MkdirAll(filepath.Dir(languagePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(externalRendererPath), 0o755); err != nil {
		t.Fatal(err)
	}

	// Seed the archive with the renderer explicitly marked unpacked. Supplying
	// only the packed files to write() keeps the external entry out of the ASAR
	// payload, as a real Electron package does.
	fixture := &asarArchive{root: &asarNode{Files: map[string]*asarNode{
		"dist": {Files: map[string]*asarNode{
			"jetskiAgent": {Files: map[string]*asarNode{
				"main.js": {Unpacked: true},
			}},
		}},
	}}}
	if err := fixture.write(asarPath, map[string][]byte{
		"dist/main.js":           []byte(`"use strict";const endpoint="` + productionEndpoint + `";`),
		"dist/languageServer.js": []byte(`const endpoint="` + productionEndpoint + `";`),
	}); err != nil {
		t.Fatal(err)
	}
	originalPlist := writeDarwinAgentIntegrityFixture(t, appPath, asarPath)
	originalASAR, err := os.ReadFile(asarPath)
	if err != nil {
		t.Fatal(err)
	}
	originalRenderer := []byte(imagePreviewOriginalRendererFixture())
	if err := os.WriteFile(externalRendererPath, originalRenderer, 0o644); err != nil {
		t.Fatal(err)
	}
	originalLanguage := []byte("binary\x00" + sandboxEndpoint + "\x00tail")
	if err := os.WriteFile(languagePath, originalLanguage, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("ANTIGRAVITY_WF_BACKUP_DIR", t.TempDir())
	t.Setenv("ANTIGRAVITY_WF_SKIP_CODESIGN", "1")
	target := darwinTargets{
		app: appPath, name: "Antigravity 2.0", kind: "agent", asar: asarPath, language: languagePath,
	}
	if _, err := applyDarwinPatch(target); err != nil {
		t.Fatal(err)
	}
	if _, _, _, patched := darwinTargetPatchState(target); !patched {
		t.Fatal("ASAR target with unpacked image renderer was not reported as fully patched")
	}
	status := buildDarwinStatus([]darwinTargets{target})
	if status.AgentPatched == nil || !*status.AgentPatched || len(status.Targets) != 1 || !status.Targets[0].Patched {
		t.Fatalf("unpacked renderer patch state was not reflected in status: %+v", status)
	}

	archive, err := readASAR(asarPath)
	if err != nil {
		t.Fatal(err)
	}
	node, err := archive.node("dist/jetskiAgent/main.js")
	if err != nil {
		t.Fatal(err)
	}
	if !node.Unpacked || node.Size != nil {
		t.Fatalf("renderer manifest changed from unpacked to packed: %+v", node)
	}
	patchedRenderer, err := os.ReadFile(externalRendererPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(patchedRenderer, []byte(imagePreviewPatchMarker)) || bytes.Contains(patchedRenderer, []byte(imagePreviewPatchV3Marker)) {
		t.Fatalf("external unpacked renderer did not receive v4: %s", patchedRenderer)
	}
	assertFileEquals(t, backupPath(asarPath), originalASAR)
	assertFileEquals(t, backupPath(externalRendererPath), originalRenderer)

	if _, err := restoreDarwinPatch(target); err != nil {
		t.Fatal(err)
	}
	assertFileEquals(t, asarPath, originalASAR)
	assertFileEquals(t, externalRendererPath, originalRenderer)
	assertFileEquals(t, languagePath, originalLanguage)
	assertFileEquals(t, filepath.Join(appPath, "Contents", "Info.plist"), originalPlist)
}

func TestMergeDarwinHistoryBacksUpAndNeverOverwrites(t *testing.T) {
	base := t.TempDir()
	t.Setenv("ANTIGRAVITY_WF_GEMINI_DIR", base)
	source := filepath.Join(base, "antigravity-ide")
	target := filepath.Join(base, "antigravity")
	for _, dir := range []string{filepath.Join(source, "conversations"), filepath.Join(target, "conversations")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(source, "conversations", "new.json"), []byte("source-new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "conversations", "same.json"), []byte("source-value"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "conversations", "same.json"), []byte("target-value"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := mergeDarwinHistory(); err != nil {
		t.Fatal(err)
	}
	assertFileEquals(t, filepath.Join(target, "conversations", "new.json"), []byte("source-new"))
	assertFileEquals(t, filepath.Join(target, "conversations", "same.json"), []byte("target-value"))
	assertFileEquals(t, filepath.Join(base, "antigravity-ide.antigravity-wf-backup", "conversations", "same.json"), []byte("source-value"))
}

func TestSyncDarwinHistoryDiscoversAllLegacyDirectories(t *testing.T) {
	base := t.TempDir()
	t.Setenv("ANTIGRAVITY_WF_GEMINI_DIR", base)
	target := filepath.Join(base, "antigravity")
	sources := []string{
		filepath.Join(base, "antigravity-ide"),
		filepath.Join(base, "antigravity-2.0"),
	}
	for _, dir := range append(sources, target) {
		if err := os.MkdirAll(filepath.Join(dir, "conversations"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(sources[0], "conversations", "ide.pb"), []byte("ide"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sources[1], "conversations", "agent2.pb"), []byte("agent2"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sources[0], "conversations", "active.db-wal"), []byte("transient"), 0o600); err != nil {
		t.Fatal(err)
	}

	summary, err := syncDarwinHistory()
	if err != nil {
		t.Fatal(err)
	}
	if summary.sources != 2 || summary.copied != 2 {
		t.Fatalf("unexpected summary: %+v", summary)
	}
	assertFileEquals(t, filepath.Join(target, "conversations", "ide.pb"), []byte("ide"))
	assertFileEquals(t, filepath.Join(target, "conversations", "agent2.pb"), []byte("agent2"))
	if _, err := os.Stat(filepath.Join(target, "conversations", "active.db-wal")); !os.IsNotExist(err) {
		t.Fatalf("transient SQLite file should not be restored: %v", err)
	}
	for _, source := range sources {
		if info, err := os.Stat(source + ".antigravity-wf-backup"); err != nil || !info.IsDir() {
			t.Fatalf("backup missing for %s: %v", source, err)
		}
	}

	second, err := syncDarwinHistory()
	if err != nil {
		t.Fatal(err)
	}
	if second.copied != 0 {
		t.Fatalf("history sync must be idempotent: %+v", second)
	}
}

func TestRunDarwinSyncHistoryDoesNotRequireInstalledApplication(t *testing.T) {
	base := t.TempDir()
	t.Setenv("ANTIGRAVITY_WF_GEMINI_DIR", base)
	message, err := runDarwin("sync-history")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(message, "历史会话检查完成") {
		t.Fatalf("unexpected message: %q", message)
	}
}

func TestDarwinPatchAgainstInstalledVersionFixture(t *testing.T) {
	if os.Getenv("ANTIGRAVITY_WF_TEST_INSTALLED") != "1" {
		t.Skip("set ANTIGRAVITY_WF_TEST_INSTALLED=1 for the installed-version fixture")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	installedExtension := "/Applications/Antigravity.app/Contents/Resources/app/extensions/antigravity/dist/extension.js"
	installedMainBackup := filepath.Join(home, ".antigravity-byok", "backups", "main.js-5b315d1067653b1a.bak")
	installedLanguageBackup := filepath.Join(home, ".antigravity-byok", "backups", "language_server_macos_x64-40bf34d68e27fffb.bak")
	for _, path := range []string{installedExtension, installedMainBackup, installedLanguageBackup} {
		if existingFile(path) == "" {
			t.Skipf("installed fixture source is unavailable: %s", path)
		}
	}

	appPath := filepath.Join(t.TempDir(), "Antigravity.app")
	mainPath := filepath.Join(appPath, "Contents", "Resources", "app", "out", "main.js")
	extensionPath := filepath.Join(appPath, "Contents", "Resources", "app", "extensions", "antigravity", "dist", "extension.js")
	languagePath := filepath.Join(appPath, "Contents", "Resources", "app", "extensions", "antigravity", "bin", "language_server_macos_x64")
	for _, pair := range [][2]string{
		{installedMainBackup, mainPath}, {installedExtension, extensionPath}, {installedLanguageBackup, languagePath},
	} {
		info, err := os.Stat(pair[0])
		if err != nil {
			t.Fatal(err)
		}
		if err := copyFileAtomic(pair[0], pair[1], info.Mode()); err != nil {
			t.Fatal(err)
		}
	}
	originalMain, _ := fileSHA256(mainPath)
	originalExtension, _ := fileSHA256(extensionPath)
	originalLanguage, _ := fileSHA256(languagePath)

	t.Setenv("ANTIGRAVITY_APP_PATH", appPath)
	t.Setenv("ANTIGRAVITY_APP_PATHS", "")
	t.Setenv("ANTIGRAVITY_WF_BACKUP_DIR", t.TempDir())
	t.Setenv("ANTIGRAVITY_WF_GEMINI_DIR", t.TempDir())
	t.Setenv("ANTIGRAVITY_WF_SKIP_CODESIGN", "1")
	targets := locateDarwinInstallations()
	if len(targets) != 1 {
		t.Fatalf("fixture discovery returned %+v", targets)
	}
	if _, err := applyDarwinPatch(targets[0]); err != nil {
		t.Fatal(err)
	}
	if _, _, _, patched := darwinTargetPatchState(targets[0]); !patched {
		t.Fatal("installed-version fixture was not fully patched")
	}
	if _, err := restoreDarwinPatch(targets[0]); err != nil {
		t.Fatal(err)
	}
	restoredMain, _ := fileSHA256(mainPath)
	restoredExtension, _ := fileSHA256(extensionPath)
	restoredLanguage, _ := fileSHA256(languagePath)
	if restoredMain != originalMain || restoredExtension != originalExtension || restoredLanguage != originalLanguage {
		t.Fatal("installed-version fixture was not restored byte-for-byte")
	}
}
