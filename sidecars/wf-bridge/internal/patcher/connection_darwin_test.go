//go:build darwin

package patcher

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type darwinConnectionFixture struct {
	target            darwinTargets
	configRoot        string
	settingsPath      string
	mainPath          string
	extensionPath     string
	rendererPath      string
	productPath       string
	originalMain      []byte
	originalExtension []byte
	originalRenderer  []byte
	originalProduct   []byte
}

func newDarwinConnectionFixture(t *testing.T, settings string) darwinConnectionFixture {
	t.Helper()

	appPath := filepath.Join(t.TempDir(), "Antigravity.app")
	appRoot := filepath.Join(appPath, "Contents", "Resources", "app")
	mainPath := filepath.Join(appRoot, "out", "main.js")
	extensionPath := filepath.Join(appRoot, "extensions", "antigravity", "dist", "extension.js")
	rendererPath := filepath.Join(appRoot, "out", "jetskiAgent", "main.js")
	productPath := filepath.Join(appRoot, "product.json")
	for _, path := range []string{mainPath, extensionPath, rendererPath, productPath} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	originalMain := []byte(`const setting="jetski.cloudCodeUrl"; const keepMain=true;`)
	originalExtension := []byte(`const setting="jetski.cloudCodeUrl",flag="--cloud_code_endpoint"; const keepExtension=true;`)
	// A current IDE renderer owns both the generated-image prompt card and the
	// Markdown/artifact image component. Include both so the connection
	// transaction exercises the complete v6 dedupe upgrade instead of the
	// deliberately fail-closed partial-renderer path.
	originalRenderer := []byte(imagePreviewOriginalRendererFixture() + ";" + imageGenerationUIRendererFixture() + ";" + imageArtifactMarkdownRendererFixture())
	product := map[string]any{
		"nameShort":      "Antigravity",
		"dataFolderName": ".antigravity",
		"checksums": map[string]string{
			"jetskiAgent/main.js": darwinIDEChecksum(originalRenderer),
		},
		"unrelated": map[string]any{"keep": true},
	}
	originalProduct, err := json.MarshalIndent(product, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	for path, data := range map[string][]byte{
		mainPath:      originalMain,
		extensionPath: originalExtension,
		rendererPath:  originalRenderer,
		productPath:   originalProduct,
	} {
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	configRoot := t.TempDir()
	settingsPath := filepath.Join(configRoot, "Antigravity", "User", "settings.json")
	if settings != "" {
		if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(settingsPath, []byte(settings), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	previousConfigDirectory := darwinUserConfigDirectory
	darwinUserConfigDirectory = func() (string, error) { return configRoot, nil }
	t.Cleanup(func() { darwinUserConfigDirectory = previousConfigDirectory })

	previousProcessList := darwinProcessList
	darwinProcessList = func() ([]byte, error) { return nil, nil }
	t.Cleanup(func() { darwinProcessList = previousProcessList })

	t.Setenv("ANTIGRAVITY_WF_BACKUP_DIR", t.TempDir())
	return darwinConnectionFixture{
		target: darwinTargets{
			app: appPath, name: "Antigravity IDE", kind: "ide", version: "2.1.0",
			main: mainPath, extensionEntry: extensionPath,
		},
		configRoot: configRoot, settingsPath: settingsPath,
		mainPath: mainPath, extensionPath: extensionPath,
		rendererPath: rendererPath, productPath: productPath,
		originalMain: originalMain, originalExtension: originalExtension,
		originalRenderer: originalRenderer, originalProduct: originalProduct,
	}
}

func readDarwinConnectionTestFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func assertDarwinConnectionTestBytes(t *testing.T, path string, expected []byte) {
	t.Helper()
	actual := readDarwinConnectionTestFile(t, path)
	if !bytes.Equal(actual, expected) {
		t.Fatalf("%s changed unexpectedly\nwant: %s\n got: %s", path, expected, actual)
	}
}

func TestDarwinConnectionSupportAndStatusDescribeEachExactTarget(t *testing.T) {
	fixture := newDarwinConnectionFixture(t, "{\n  \"editor.fontSize\": 15\n}\n")
	unsupportedAgent := darwinTargets{
		app:  filepath.Join(t.TempDir(), "Antigravity 2.0.app"),
		name: "Unknown Agent", kind: "agent", version: "9.9.9",
	}

	supported, mode, reason := darwinTargetConnectionSupport(fixture.target)
	if !supported || mode != "user-settings" || reason != "" {
		t.Fatalf("supported IDE was described incorrectly: supported=%t mode=%q reason=%q", supported, mode, reason)
	}
	supported, mode, reason = darwinTargetConnectionSupport(unsupportedAgent)
	if supported || mode != "" || !strings.Contains(reason, "app.asar") {
		t.Fatalf("unknown Agent must be fail-closed: supported=%t mode=%q reason=%q", supported, mode, reason)
	}
	supported, mode, reason = darwinTargetConnectionSupport(darwinTargets{name: "Mystery", kind: "other"})
	if supported || mode != "" || !strings.Contains(reason, "未识别") {
		t.Fatalf("unknown target kind must be fail-closed: supported=%t mode=%q reason=%q", supported, mode, reason)
	}

	status := buildDarwinStatus([]darwinTargets{fixture.target, unsupportedAgent})
	if len(status.Targets) != 2 {
		t.Fatalf("unexpected target status count: %+v", status.Targets)
	}
	if !status.Targets[0].Supported || status.Targets[0].ConnectionMode != "user-settings" || status.Targets[0].Reason != "" {
		t.Fatalf("supported TargetStatus fields are incomplete: %+v", status.Targets[0])
	}
	if status.Targets[1].Supported || status.Targets[1].ConnectionMode != "" || !strings.Contains(status.Targets[1].Reason, "app.asar") {
		t.Fatalf("unsupported TargetStatus fields are incomplete: %+v", status.Targets[1])
	}
}

func TestDarwinIDEStatusRequiresCurrentImageRendererAsWellAsEndpoint(t *testing.T) {
	endpoint := currentPatchProxyEndpoint().Base
	fixture := newDarwinConnectionFixture(t, "{\n  \"jetski.cloudCodeUrl\": \""+endpoint+"\"\n}\n")

	status := buildDarwinStatus([]darwinTargets{fixture.target})
	if len(status.Targets) != 1 || status.Targets[0].Patched || status.IDEPatched == nil || *status.IDEPatched {
		t.Fatalf("configured endpoint with an old renderer was incorrectly reported as connected: %+v", status)
	}

	if _, err := applyDarwinSafeIDETarget(fixture.target); err != nil {
		t.Fatalf("upgrade current IDE renderer: %v", err)
	}
	status = buildDarwinStatus([]darwinTargets{fixture.target})
	if len(status.Targets) != 1 || !status.Targets[0].Patched || status.IDEPatched == nil || !*status.IDEPatched {
		t.Fatalf("configured endpoint with the current v6 renderer was not reported as connected: %+v", status)
	}
}

func TestDarwinUnknownAgentDoesNotBlockSupportedIDE(t *testing.T) {
	fixture := newDarwinConnectionFixture(t, "{\n  // user setting\n  \"editor.fontSize\": 15\n}\n")
	unsupportedAgent := darwinTargets{
		app:  filepath.Join(t.TempDir(), "Antigravity 2.0.app"),
		name: "Unknown Agent", kind: "agent",
	}

	message, err := applyDarwinTargetsForKind([]darwinTargets{unsupportedAgent, fixture.target}, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(message, "Unknown Agent 未连接") || !strings.Contains(message, "Antigravity IDE 已安全连接") {
		t.Fatalf("combined result did not report both skipped and connected targets: %s", message)
	}
	if !darwinCloudCodeSettingIsConfigured(fixture.settingsPath, currentPatchProxyEndpoint().Base) {
		t.Fatal("supported IDE was not connected after the unknown Agent was skipped")
	}
}

func TestDarwinRunningTargetCausesZeroWrites(t *testing.T) {
	fixture := newDarwinConnectionFixture(t, "")
	previousProcessList := darwinProcessList
	darwinProcessList = func() ([]byte, error) {
		return []byte(fixture.target.app + "/Contents/MacOS/Antigravity --type=renderer"), nil
	}
	t.Cleanup(func() { darwinProcessList = previousProcessList })

	_, err := applyDarwinTargetsForKind([]darwinTargets{fixture.target}, "ide")
	if err == nil || !strings.Contains(err.Error(), "完全退出") {
		t.Fatalf("live target must be rejected before writing, got %v", err)
	}
	if _, statErr := os.Stat(fixture.settingsPath); !os.IsNotExist(statErr) {
		t.Fatalf("settings were created for a running application: %v", statErr)
	}
	assertDarwinConnectionTestBytes(t, fixture.mainPath, fixture.originalMain)
	assertDarwinConnectionTestBytes(t, fixture.extensionPath, fixture.originalExtension)
	assertDarwinConnectionTestBytes(t, fixture.rendererPath, fixture.originalRenderer)
	assertDarwinConnectionTestBytes(t, fixture.productPath, fixture.originalProduct)
	for _, path := range []string{fixture.rendererPath, fixture.productPath} {
		if _, statErr := os.Stat(backupPath(path)); !os.IsNotExist(statErr) {
			t.Fatalf("backup was created before the process guard for %s: %v", path, statErr)
		}
	}
}

func TestDarwinIDEConnectionCommitsSettingsRendererAndProductTogether(t *testing.T) {
	fixture := newDarwinConnectionFixture(t, "{\n  // keep me\n  \"editor.fontSize\": 15\n}\n")

	message, err := applyDarwinSafeIDETarget(fixture.target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(message, "已安全连接本地代理") || !strings.Contains(message, "checksum 已同步") {
		t.Fatalf("unexpected success message: %s", message)
	}
	settings := readDarwinConnectionTestFile(t, fixture.settingsPath)
	if !bytes.Contains(settings, []byte("// keep me")) || !bytes.Contains(settings, []byte(`"editor.fontSize": 15`)) ||
		!darwinCloudCodeSettingIsConfigured(fixture.settingsPath, currentPatchProxyEndpoint().Base) {
		t.Fatalf("settings transaction lost user content or endpoint: %s", settings)
	}
	renderer := readDarwinConnectionTestFile(t, fixture.rendererPath)
	for _, marker := range []string{imagePreviewPatchMarker, imageGenerationUIPatchMarker} {
		if !bytes.Contains(renderer, []byte(marker)) {
			t.Fatalf("renderer transaction is missing %q", marker)
		}
	}
	if err := verifyDarwinIDEProductChecksums(fixture.target, []string{fixture.rendererPath}); err != nil {
		t.Fatalf("committed product checksum does not cover the renderer: %v", err)
	}
	assertDarwinConnectionTestBytes(t, fixture.mainPath, fixture.originalMain)
	assertDarwinConnectionTestBytes(t, fixture.extensionPath, fixture.originalExtension)
	for _, path := range []string{fixture.rendererPath, fixture.productPath} {
		if _, statErr := os.Stat(backupPath(path)); statErr != nil {
			t.Fatalf("pre-upgrade backup is missing for %s: %v", path, statErr)
		}
	}
}

func TestDarwinUnknownOptionalImageRendererDoesNotBlockIDEConnection(t *testing.T) {
	fixture := newDarwinConnectionFixture(t, "{\n  \"editor.fontSize\": 15\n}\n")
	unknownRenderer := []byte(`const futureImageRenderer={generatedMedia:"unknown-layout"};`)
	product := map[string]any{
		"nameShort":      "Antigravity",
		"dataFolderName": ".antigravity",
		"checksums": map[string]string{
			"jetskiAgent/main.js": darwinIDEChecksum(unknownRenderer),
		},
	}
	productBytes, err := json.MarshalIndent(product, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.rendererPath, unknownRenderer, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.productPath, productBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	message, err := applyDarwinSafeIDETarget(fixture.target)
	if err != nil {
		t.Fatalf("optional unknown renderer blocked the IDE endpoint: %v", err)
	}
	if !strings.Contains(message, "已安全连接本地代理") || !strings.Contains(message, "已跳过可选界面增强") {
		t.Fatalf("unexpected compatibility message: %s", message)
	}
	if !darwinCloudCodeSettingIsConfigured(fixture.settingsPath, currentPatchProxyEndpoint().Base) {
		t.Fatal("official endpoint setting was not connected")
	}
	assertDarwinConnectionTestBytes(t, fixture.rendererPath, unknownRenderer)
	assertDarwinConnectionTestBytes(t, fixture.productPath, productBytes)
	for _, path := range []string{fixture.rendererPath, fixture.productPath} {
		if existingFile(backupPath(path)) != "" {
			t.Fatalf("unchanged optional image file received a misleading backup: %s", path)
		}
	}
}

func TestDarwinConnectionRollbackRestoresRendererProductAndDeletesNewSettings(t *testing.T) {
	fixture := newDarwinConnectionFixture(t, "")
	endpoint := currentPatchProxyEndpoint().Base

	rendererPlan, ready, err := prepareDarwinSafeImageRendererPlan(fixture.rendererPath)
	if err != nil || !ready || rendererPlan == nil || !rendererPlan.changed {
		t.Fatalf("renderer plan=%#v ready=%t err=%v", rendererPlan, ready, err)
	}
	productPlan, err := prepareDarwinIDEProductChecksumPatch(fixture.target, []*patchPlan{rendererPlan})
	if err != nil || productPlan == nil || !productPlan.changed {
		t.Fatalf("product plan=%#v err=%v", productPlan, err)
	}
	settingsPlan, changed, err := prepareDarwinEnsureCloudCodeSetting(fixture.settingsPath, endpoint)
	if err != nil || !changed || settingsPlan == nil {
		t.Fatalf("settings plan=%#v changed=%t err=%v", settingsPlan, changed, err)
	}
	plans := []*patchPlan{rendererPlan, productPlan, settingsPlan}
	snapshots, err := snapshotDarwinPatchTargets(plans)
	if err != nil {
		t.Fatal(err)
	}

	// A regular file cannot be used as a directory. Appending this deterministic
	// late plan makes writePatchPlans fail only after all three real transaction
	// members have been written, without requiring a production-only test hook.
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("block"), 0o600); err != nil {
		t.Fatal(err)
	}
	lateFailure := &patchPlan{
		path: filepath.Join(blocker, "late.js"), original: nil, updated: []byte("must fail"), mode: 0o600, changed: true,
	}
	writeErr := writePatchPlans(append(plans, lateFailure))
	if writeErr == nil {
		t.Fatal("late transaction write unexpectedly succeeded")
	}
	if err := restoreDarwinPatchSnapshots(snapshots); err != nil {
		t.Fatalf("transaction rollback failed: %v (original failure: %v)", err, writeErr)
	}

	assertDarwinConnectionTestBytes(t, fixture.rendererPath, fixture.originalRenderer)
	assertDarwinConnectionTestBytes(t, fixture.productPath, fixture.originalProduct)
	if _, statErr := os.Stat(fixture.settingsPath); !os.IsNotExist(statErr) {
		t.Fatalf("new settings file survived the failed transaction: %v", statErr)
	}
}

func TestDarwinConnectionBacksUpTakesOverAndRestoresThirdPartyEndpoint(t *testing.T) {
	const foreignEndpoint = "https://other-proxy.example/cloudcode"
	fixture := newDarwinConnectionFixture(t, "{\n  \"jetski.cloudCodeUrl\": \""+foreignEndpoint+"\",\n  \"editor.fontSize\": 16\n}\n")
	originalSettings := readDarwinConnectionTestFile(t, fixture.settingsPath)

	_, err := applyDarwinSafeIDETarget(fixture.target)
	if err != nil {
		t.Fatalf("foreign endpoint takeover failed: %v", err)
	}
	if !darwinCloudCodeSettingIsConfigured(fixture.settingsPath, currentPatchProxyEndpoint().Base) {
		t.Fatal("third-party setting was not replaced by the local proxy")
	}
	assertDarwinConnectionTestBytes(t, backupPath(fixture.settingsPath), originalSettings)
	if _, err := restoreDarwinSafeIDETarget(fixture.target); err != nil {
		t.Fatalf("restore third-party setting: %v", err)
	}
	assertDarwinConnectionTestBytes(t, fixture.settingsPath, originalSettings)
}

func TestDarwinOldImagePatchWithoutOfficialBackupUpgradesCurrentState(t *testing.T) {
	fixture := newDarwinConnectionFixture(t, "")
	patched, result := patchImagePreviewRenderer(string(fixture.originalRenderer))
	if !result.Changed {
		t.Fatalf("fixture did not produce a current image patch: %#v", result)
	}
	legacy := strings.Replace(patched, imagePreviewPatchMarker, imagePreviewPatchV6Marker, 1)
	if legacy == patched {
		t.Fatal("failed to create legacy image renderer fixture")
	}
	if err := os.WriteFile(fixture.rendererPath, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	legacyRenderer := readDarwinConnectionTestFile(t, fixture.rendererPath)
	_, err := applyDarwinSafeIDETarget(fixture.target)
	if err != nil {
		t.Fatalf("legacy renderer upgrade failed: %v", err)
	}
	assertDarwinConnectionTestBytes(t, backupPath(fixture.rendererPath), legacyRenderer)
	current := readDarwinConnectionTestFile(t, fixture.rendererPath)
	if !bytes.Contains(current, []byte(imagePreviewPatchMarker)) || bytes.Contains(current, []byte(imagePreviewPatchV6Marker)) {
		t.Fatalf("legacy renderer was not upgraded to the current image patch: %s", current)
	}
	if _, err := restoreDarwinSafeIDETarget(fixture.target); err != nil {
		t.Fatal(err)
	}
	assertDarwinConnectionTestBytes(t, fixture.rendererPath, legacyRenderer)
}

func TestDarwinRestoreTouchesOnlyAssistantOwnedState(t *testing.T) {
	fixture := newDarwinConnectionFixture(t, "{\n  // user-owned\n  \"editor.fontSize\": 17\n}\n")
	if _, err := applyDarwinSafeIDETarget(fixture.target); err != nil {
		t.Fatal(err)
	}

	const foreignEndpoint = "https://user-owned.example/cloudcode"
	settings := readDarwinConnectionTestFile(t, fixture.settingsPath)
	settings = bytes.Replace(settings, []byte(currentPatchProxyEndpoint().Base), []byte(foreignEndpoint), 1)
	if err := os.WriteFile(fixture.settingsPath, settings, 0o600); err != nil {
		t.Fatal(err)
	}
	userFile := filepath.Join(fixture.configRoot, "Antigravity", "User", "globalStorage", "models.json")
	if err := os.MkdirAll(filepath.Dir(userFile), 0o755); err != nil {
		t.Fatal(err)
	}
	userFileData := []byte(`{"models":["keep-me"]}`)
	if err := os.WriteFile(userFile, userFileData, 0o600); err != nil {
		t.Fatal(err)
	}

	message, err := restoreDarwinSafeIDETarget(fixture.target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(message, "未删除其他用户设置") {
		t.Fatalf("unexpected restore result: %s", message)
	}
	restoredSettings := readDarwinConnectionTestFile(t, fixture.settingsPath)
	if !bytes.Contains(restoredSettings, []byte(foreignEndpoint)) ||
		!bytes.Contains(restoredSettings, []byte(`"editor.fontSize": 17`)) ||
		!bytes.Contains(restoredSettings, []byte("// user-owned")) {
		t.Fatalf("restore removed user-owned settings: %s", restoredSettings)
	}
	assertDarwinConnectionTestBytes(t, fixture.rendererPath, fixture.originalRenderer)
	assertDarwinConnectionTestBytes(t, fixture.productPath, fixture.originalProduct)
	assertDarwinConnectionTestBytes(t, userFile, userFileData)
}

func TestRunDarwinApplyAgentRoutesOnlyToVerifiedAgent(t *testing.T) {
	appPath := filepath.Join(t.TempDir(), "Antigravity 2.0.app")
	resources := filepath.Join(appPath, "Contents", "Resources")
	asarPath := filepath.Join(resources, "app.asar")
	languagePath := filepath.Join(resources, "bin", "language_server")
	t.Setenv("ANTIGRAVITY_WF_BACKUP_DIR", t.TempDir())
	if err := os.MkdirAll(resources, 0o755); err != nil {
		t.Fatal(err)
	}
	archive := &asarArchive{root: &asarNode{Files: map[string]*asarNode{}}}
	if err := archive.write(asarPath, map[string][]byte{
		"dist/main.js":            []byte(`"use strict"; const endpoint="` + productionEndpoint + `";`),
		"dist/languageServer.js":  []byte(`const args=["--api_server_url","https://generativelanguage.googleapis.com","--cloud_code_endpoint","` + productionEndpoint + `"];`),
		"out/jetskiAgent/main.js": []byte(imagePreviewOriginalRendererFixture() + ";" + imageGenerationUIRendererFixture()),
	}); err != nil {
		t.Fatal(err)
	}
	activeHash, err := darwinASARHeaderHash(asarPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appPath, "Contents", "Info.plist"), darwinIntegrityPlist(activeHash), 0o644); err != nil {
		t.Fatal(err)
	}
	writeDarwinAgentLanguageServerUIFixture(t, languagePath)
	languageData, err := os.ReadFile(languagePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(backupPath(languagePath), languageData, 0o600); err != nil {
		t.Fatal(err)
	}

	previousProcessList := darwinProcessList
	darwinProcessList = func() ([]byte, error) { return nil, nil }
	t.Cleanup(func() { darwinProcessList = previousProcessList })
	t.Setenv("ANTIGRAVITY_APP_PATH", appPath)
	t.Setenv("ANTIGRAVITY_APP_PATHS", "")
	t.Setenv("ANTIGRAVITY_WF_SKIP_CODESIGN", "1")

	message, err := runDarwin("apply-agent")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(message, "请保持本工具运行") || !darwinASARPatched(asarPath) {
		t.Fatalf("apply-agent did not route to the verified Agent transaction: %s", message)
	}
	if _, err := runDarwin("apply-ide"); err == nil || !strings.Contains(err.Error(), "未找到独立 IDE") {
		t.Fatalf("apply-ide unexpectedly selected the Agent target: %v", err)
	}
}

func TestDarwinAgentTakesOverAndRestoresThirdPartyLauncherWithoutOfficialBackup(t *testing.T) {
	appPath := filepath.Join(t.TempDir(), "Antigravity 2.0.app")
	resources := filepath.Join(appPath, "Contents", "Resources")
	asarPath := filepath.Join(resources, "app.asar")
	languagePath := filepath.Join(resources, "bin", "language_server")
	if err := os.MkdirAll(resources, 0o755); err != nil {
		t.Fatal(err)
	}
	archive := &asarArchive{root: &asarNode{Files: map[string]*asarNode{}}}
	const thirdPartyEndpoint = "https://third-party.example.invalid/cloudcode"
	if err := archive.write(asarPath, map[string][]byte{
		"dist/main.js":            []byte(`"use strict"; const thirdPartyWrapper=true;`),
		"dist/languageServer.js":  []byte(`const args=["--cloud_code_endpoint","` + thirdPartyEndpoint + `"];`),
		"out/jetskiAgent/main.js": []byte(imagePreviewOriginalRendererFixture() + ";" + imageGenerationUIRendererFixture()),
	}); err != nil {
		t.Fatal(err)
	}
	thirdPartyASAR := readDarwinConnectionTestFile(t, asarPath)
	thirdPartyHash, err := darwinASARHeaderHash(asarPath)
	if err != nil {
		t.Fatal(err)
	}
	plistPath := filepath.Join(appPath, "Contents", "Info.plist")
	thirdPartyPlist := darwinIntegrityPlist(thirdPartyHash)
	if err := os.WriteFile(plistPath, thirdPartyPlist, 0o644); err != nil {
		t.Fatal(err)
	}
	writeDarwinAgentLanguageServerUIFixture(t, languagePath)

	t.Setenv("ANTIGRAVITY_WF_BACKUP_DIR", t.TempDir())
	t.Setenv("ANTIGRAVITY_WF_SKIP_CODESIGN", "1")
	target := darwinTargets{
		app: appPath, name: "Third-party Antigravity 2.0", kind: "agent",
		asar: asarPath, language: languagePath,
	}
	if supported, _, reason := darwinTargetConnectionSupport(target); !supported {
		t.Fatalf("third-party literal launcher was not accepted: %s", reason)
	}
	if _, err := applyDarwinASARPatch(target); err != nil {
		t.Fatalf("third-party Agent takeover failed: %v", err)
	}
	patchedArchive, err := readASAR(asarPath)
	if err != nil {
		t.Fatal(err)
	}
	launcher, err := patchedArchive.readFile("dist/languageServer.js")
	if err != nil || bytes.Contains(launcher, []byte(thirdPartyEndpoint)) ||
		!bytes.Contains(launcher, []byte(currentPatchProxyEndpoint().Base)) {
		t.Fatalf("third-party launcher was not scoped to the local proxy: %s (%v)", launcher, err)
	}
	assertDarwinConnectionTestBytes(t, backupPath(asarPath), thirdPartyASAR)
	assertDarwinConnectionTestBytes(t, backupPath(plistPath), thirdPartyPlist)
	if _, err := restoreDarwinPatch(target); err != nil {
		t.Fatalf("restore third-party Agent state: %v", err)
	}
	assertDarwinConnectionTestBytes(t, asarPath, thirdPartyASAR)
	assertDarwinConnectionTestBytes(t, plistPath, thirdPartyPlist)
}

func TestDarwinProcessListFailureStopsBeforeAnyWrite(t *testing.T) {
	fixture := newDarwinConnectionFixture(t, "")
	previousProcessList := darwinProcessList
	darwinProcessList = func() ([]byte, error) { return nil, errors.New("ps unavailable") }
	t.Cleanup(func() { darwinProcessList = previousProcessList })

	_, err := applyDarwinTargetsForKind([]darwinTargets{fixture.target}, "ide")
	if err == nil || !strings.Contains(err.Error(), "运行状态失败") {
		t.Fatalf("process-list failure must be fail-closed, got %v", err)
	}
	if _, statErr := os.Stat(fixture.settingsPath); !os.IsNotExist(statErr) {
		t.Fatalf("settings were created after process-list failure: %v", statErr)
	}
	assertDarwinConnectionTestBytes(t, fixture.rendererPath, fixture.originalRenderer)
	assertDarwinConnectionTestBytes(t, fixture.productPath, fixture.originalProduct)
}
