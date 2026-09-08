package patcher

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWindowsLanguageServerWithoutEmbeddedEndpointIsSupported(t *testing.T) {
	path := filepath.Join(t.TempDir(), "language_server_windows_x64.exe")
	if err := os.WriteFile(path, []byte("MZ\x00new endpoint-free layout"), 0o755); err != nil {
		t.Fatal(err)
	}
	plan, embedded, err := prepareWindowsLanguagePatch(path)
	if err != nil {
		t.Fatal(err)
	}
	if embedded || plan == nil || plan.changed {
		t.Fatalf("endpoint-free binary should be an unchanged optional plan: embedded=%t plan=%#v", embedded, plan)
	}
	patched, hasEmbedded := windowsLanguagePatchState(path)
	if !patched || hasEmbedded {
		t.Fatalf("endpoint-free binary should rely on launcher: patched=%t embedded=%t", patched, hasEmbedded)
	}
}

func TestWindowsLanguageServerEmbeddedEndpointsArePatched(t *testing.T) {
	path := filepath.Join(t.TempDir(), "language_server.exe")
	original := []byte("prefix " + windowsProductionEndpoint + " middle " + windowsSandboxEndpoint +
		" public " + windowsPublicEndpoint + " autopush " + windowsAutopushEndpoint + " suffix")
	if err := os.WriteFile(path, original, 0o755); err != nil {
		t.Fatal(err)
	}
	plan, embedded, err := prepareWindowsLanguagePatch(path)
	if err != nil {
		t.Fatal(err)
	}
	if !embedded || !plan.changed {
		t.Fatalf("expected embedded endpoints to be patched: embedded=%t changed=%t", embedded, plan.changed)
	}
	if windowsCloudCodeURLPattern.Match(plan.updated) ||
		!bytes.Contains(plan.updated, []byte(windowsBinaryProxyEndpoint)) {
		t.Fatalf("unexpected patched binary: %q", plan.updated)
	}
	if len(plan.updated) != len(original) {
		t.Fatalf("binary patch changed file length: %d != %d", len(plan.updated), len(original))
	}
}

func TestWindowsDynamicProxyEndpointPatchesTextBinaryAndState(t *testing.T) {
	restoreEndpoint := setPatchProxyPortForTest(51042)
	t.Cleanup(restoreEndpoint)
	endpoint := currentPatchProxyEndpoint()
	if endpoint.Port != 51042 {
		t.Fatalf("dynamic endpoint port = %d", endpoint.Port)
	}

	languagePath := filepath.Join(t.TempDir(), "language_server.exe")
	originalLanguage := []byte("prefix " + windowsProductionEndpoint + " middle " + windowsSandboxEndpoint + " suffix")
	if err := os.WriteFile(languagePath, originalLanguage, 0o755); err != nil {
		t.Fatal(err)
	}
	languagePlan, embedded, err := prepareWindowsLanguagePatch(languagePath)
	if err != nil {
		t.Fatal(err)
	}
	if !embedded || len(languagePlan.updated) != len(originalLanguage) {
		t.Fatalf("dynamic binary patch must be embedded and length-preserving: embedded=%t %d!=%d", embedded, len(languagePlan.updated), len(originalLanguage))
	}
	if !bytes.Contains(languagePlan.updated, []byte(endpoint.Binary)) || !bytes.Contains(languagePlan.updated, []byte(endpoint.BinarySandbox)) {
		t.Fatalf("dynamic binary endpoint missing: %q", languagePlan.updated)
	}
	if err := os.WriteFile(languagePath, languagePlan.updated, 0o755); err != nil {
		t.Fatal(err)
	}
	if patched, hasEmbedded := windowsLanguagePatchState(languagePath); !patched || !hasEmbedded {
		t.Fatalf("dynamic language state = patched:%t embedded:%t", patched, hasEmbedded)
	}

	mainPath := filepath.Join(t.TempDir(), "main.js")
	mainSource := `"use strict";const u=` + windowsIDECloudCodeSetting + `;` + authEligibilityOriginal
	if err := os.WriteFile(mainPath, []byte(mainSource), 0o644); err != nil {
		t.Fatal(err)
	}
	mainPlan, err := prepareWindowsMainPatch(mainPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(mainPlan.updated, []byte(endpoint.Base)) || bytes.Contains(mainPlan.updated, []byte(windowsBaseProxyEndpoint)) {
		t.Fatalf("text patch did not use the selected endpoint: %s", mainPlan.updated)
	}
	if err := os.WriteFile(mainPath, mainPlan.updated, 0o644); err != nil {
		t.Fatal(err)
	}
	if !windowsMainPatched(mainPath) {
		t.Fatal("dynamic text patch was not recognized as patched")
	}
	if !windowsContainsKnownPatch([]byte(endpoint.Base + "/v1internal/xxxxx")) {
		t.Fatal("dynamic legacy endpoint was not protected as an existing helper patch")
	}
}

func TestWindowsExtensionPatchHandlesNestedAndOptionalCloudCodeCalls(t *testing.T) {
	path := filepath.Join(t.TempDir(), "extension.js")
	source := "/*! For license information please see extension.js.LICENSE.txt */\n" +
		`const endpoint=await service.client?.getCloudCodeUrl();args.push("--cloud_code_endpoint",endpoint);` +
		`args.push("--app_data_dir",g.$.getInstance().appDataDirectoryName);` +
		`const fallback="` + windowsAutopushEndpoint + `";`
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := prepareWindowsExtensionPatch(path)
	if err != nil {
		t.Fatal(err)
	}
	updated := string(plan.updated)
	for _, required := range []string{windowsExtensionMarker, windowsBaseProxyEndpoint, windowsSharedDataArgument} {
		if !strings.Contains(updated, required) {
			t.Fatalf("patched extension is missing %q", required)
		}
	}
	if windowsFlexibleCloudCodeCallPattern.MatchString(updated) || windowsCloudCodeURLPattern.MatchString(updated) {
		t.Fatalf("patched extension retained a remote endpoint: %s", updated)
	}
	if !windowsLauncherHasProxyEndpoint(updated) {
		t.Fatal("patched extension launcher does not point to the local proxy")
	}
}

func TestWindowsMainPatchUsesLocalCredentialsAndSharedHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "main.js")
	source := `"use strict";const u=` + windowsIDECloudCodeSetting + `;const a=["--app_data_dir",x.ideName];` + authEligibilityOriginal
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := prepareWindowsMainPatch(path)
	if err != nil {
		t.Fatal(err)
	}
	updated := string(plan.updated)
	for _, required := range []string{windowsMainMarker, windowsBaseProxyEndpoint, windowsSharedDataArgument, authEligibilityPatched} {
		if !strings.Contains(updated, required) {
			t.Fatalf("patched main is missing %q", required)
		}
	}
	for _, forbidden := range []string{windowsIDECloudCodeSetting, authEligibilityOriginal} {
		if strings.Contains(updated, forbidden) {
			t.Fatalf("patched main still contains %q", forbidden)
		}
	}
}

func TestWindowsKnownPatchDetectionIncludesEveryReleasedImageMarker(t *testing.T) {
	for _, marker := range []string{
		imagePreviewPatchV2Marker, imagePreviewPatchV3Marker,
		imagePreviewPatchV4Marker, imagePreviewPatchV5Marker,
		imagePreviewPatchV6Marker, imagePreviewPatchV7Marker,
		imagePreviewPatchMarker,
		imageGenerationUIPatchV1Marker, imageGenerationUIPatchV2Marker,
		imageGenerationUIPatchMarker,
	} {
		if !windowsContainsKnownPatch([]byte("/*" + marker + "*/")) {
			t.Fatalf("known image marker was not protected by backup detection: %s", marker)
		}
	}
}

func TestWindowsASARLauncherPatchDoesNotRequireBinaryEndpoint(t *testing.T) {
	root := &asarNode{Files: map[string]*asarNode{}}
	fixture := &asarArchive{root: root}
	path := filepath.Join(t.TempDir(), "app.asar")
	if err := fixture.write(path, map[string][]byte{
		"package.json":               []byte(`{"version":"2.0.0"}`),
		"dist/main.js":               []byte(`"use strict";` + authEligibilityOriginal),
		"dist/languageServer.js":     []byte(`const args=["--api_server_url","https://generativelanguage.googleapis.com","--cloud_code_endpoint","` + windowsProductionEndpoint + `"]`),
		"out/jetskiAgent/main.js":    []byte(imagePreviewOriginalRendererFixture()),
		"dist/unrelated-renderer.js": []byte(imagePreviewOriginalRendererFixture()),
	}); err != nil {
		t.Fatal(err)
	}
	candidate, err := prepareWindowsASARCandidate(path, path)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(candidate)
	if !windowsASARPatched(candidate) {
		t.Fatal("candidate was not recognized as a complete Windows ASAR patch")
	}
	archive, err := readASAR(candidate)
	if err != nil {
		t.Fatal(err)
	}
	launcher, _ := archive.readFile("dist/languageServer.js")
	main, _ := archive.readFile("dist/main.js")
	if !bytes.Contains(launcher, []byte(windowsBaseProxyEndpoint)) || bytes.Contains(launcher, []byte(windowsProductionEndpoint)) {
		t.Fatalf("launcher endpoint was not patched: %s", launcher)
	}
	if !bytes.Contains(launcher, []byte(`"--api_server_url","https://generativelanguage.googleapis.com"`)) {
		t.Fatalf("native Gemini API endpoint was changed: %s", launcher)
	}
	if !bytes.Contains(main, []byte(authEligibilityPatched)) || bytes.Contains(main, []byte(authEligibilityOriginal)) {
		t.Fatal("local credential eligibility branch was not patched")
	}
	preview, err := archive.readFile("out/jetskiAgent/main.js")
	if err != nil || !bytes.Contains(preview, []byte(imagePreviewPatchMarker)) {
		t.Fatalf("ASAR image-preview renderer was not patched: %v", err)
	}
}

func TestWindowsLauncherPrefersCloudCodeEndpointAndPreservesAPIServerURL(t *testing.T) {
	const apiServer = "https://generativelanguage.googleapis.com"
	const thirdPartyCloudCode = "https://third-party.example.invalid/cloudcode"
	source := `const args=["--api_server_url","` + apiServer + `","--cloud_code_endpoint","` + thirdPartyCloudCode + `"];`
	updated := patchWindowsCloudCodeSource(source)
	if !strings.Contains(updated, `"--api_server_url","`+apiServer+`"`) {
		t.Fatalf("native API server URL was not preserved: %s", updated)
	}
	if strings.Contains(updated, thirdPartyCloudCode) || !windowsLauncherHasProxyEndpoint(updated) {
		t.Fatalf("cloud code endpoint was not exclusively routed to the local proxy: %s", updated)
	}
}

func TestWindowsLauncherFallsBackToSingleLegacyAPIServerURL(t *testing.T) {
	source := `const args=["--api_server_url","https://third-party.example.invalid/cloudcode"];`
	updated := patchWindowsCloudCodeSource(source)
	if strings.Contains(updated, "third-party.example.invalid") || !windowsLauncherHasProxyEndpoint(updated) {
		t.Fatalf("legacy API-server-only launcher was not routed to the local proxy: %s", updated)
	}
}

func TestWindowsLauncherRejectsDuplicateManagedFlags(t *testing.T) {
	source := `const args=["--cloud_code_endpoint","https://one.invalid","--cloud_code_endpoint","https://two.invalid"];`
	if windowsLauncherHasProxyEndpoint(patchWindowsCloudCodeSource(source)) {
		t.Fatalf("duplicate cloud code flags must remain unsupported: %s", source)
	}
}
