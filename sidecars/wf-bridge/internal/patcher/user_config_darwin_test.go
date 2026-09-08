//go:build darwin

package patcher

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newDarwinSettingsFixture(t *testing.T) (darwinTargets, string) {
	t.Helper()
	app := filepath.Join(t.TempDir(), "Antigravity.app")
	main := filepath.Join(app, "Contents", "Resources", "app", "out", "main.js")
	extension := filepath.Join(app, "Contents", "Resources", "app", "extensions", "antigravity", "dist", "extension.js")
	product := filepath.Join(app, "Contents", "Resources", "app", "product.json")
	for _, directory := range []string{filepath.Dir(main), filepath.Dir(extension)} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(main, []byte(`const setting="jetski.cloudCodeUrl";`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(extension, []byte(`const setting="jetski.cloudCodeUrl",flag="--cloud_code_endpoint";`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(product, []byte(`{"nameShort":"Antigravity","dataFolderName":".antigravity"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	configRoot := t.TempDir()
	previous := darwinUserConfigDirectory
	darwinUserConfigDirectory = func() (string, error) { return configRoot, nil }
	t.Cleanup(func() { darwinUserConfigDirectory = previous })
	return darwinTargets{app: app, kind: "ide", main: main, extensionEntry: extension}, configRoot
}

func TestDarwinTargetUserSettingsPathUsesExistingProductFolder(t *testing.T) {
	target, configRoot := newDarwinSettingsFixture(t)
	settings := filepath.Join(configRoot, ".antigravity", "User", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte("{\n  // existing user setting\n  \"workbench.colorTheme\": \"Dark\"\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	path, mode, err := darwinTargetUserSettingsPath(target)
	if err != nil {
		t.Fatal(err)
	}
	if path != settings || mode != "user-settings" {
		t.Fatalf("unexpected settings target: %q %q", path, mode)
	}
}

func TestDarwinTargetUserSettingsPathUsesApplicationNameAndActualCase(t *testing.T) {
	target, configRoot := newDarwinSettingsFixture(t)
	product := filepath.Join(target.app, "Contents", "Resources", "app", "product.json")
	if err := os.WriteFile(product, []byte(`{"applicationName":"antigravity"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(configRoot, "Antigravity", "User", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path, _, err := darwinTargetUserSettingsPath(target)
	if err != nil {
		t.Fatal(err)
	}
	if path != settings {
		t.Fatalf("settings path = %q, want existing case-preserving %q", path, settings)
	}
}

func TestDarwinTargetUserSettingsPathRejectsUnsafeProductFolder(t *testing.T) {
	target, _ := newDarwinSettingsFixture(t)
	product := filepath.Join(target.app, "Contents", "Resources", "app", "product.json")
	if err := os.WriteFile(product, []byte(`{"nameShort":"../outside","dataFolderName":"/tmp/outside"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := darwinTargetUserSettingsPath(target); err == nil {
		t.Fatal("unsafe product directory names were accepted")
	}
}

func TestDarwinCloudCodeSettingRoundTripPreservesCommentsAndOtherSettings(t *testing.T) {
	target, configRoot := newDarwinSettingsFixture(t)
	settings := filepath.Join(configRoot, "Antigravity", "User", "settings.json")
	original := "{\n  // Keep this comment\n  \"editor.fontSize\": 15,\n  \"nested\": { \"enabled\": true }\n}\n"
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}

	path, _, err := darwinTargetUserSettingsPath(target)
	if err != nil {
		t.Fatal(err)
	}
	const endpoint = "http://127.0.0.1:50999"
	plan, changed, err := prepareDarwinEnsureCloudCodeSetting(path, endpoint)
	if err != nil || !changed || plan == nil {
		t.Fatalf("ensure plan = %#v, changed=%t, err=%v", plan, changed, err)
	}
	if err := writePatchPlans([]*patchPlan{plan}); err != nil {
		t.Fatal(err)
	}
	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(updated), "// Keep this comment") || !strings.Contains(string(updated), `"editor.fontSize": 15`) {
		t.Fatalf("unrelated JSONC content changed: %s", updated)
	}
	if !darwinCloudCodeSettingIsConfigured(path, endpoint) {
		t.Fatal("endpoint setting was not written")
	}
	remove, removed, err := prepareDarwinRemoveCloudCodeSetting(path, endpoint)
	if err != nil || !removed || remove == nil {
		t.Fatalf("remove plan = %#v, removed=%t, err=%v", remove, removed, err)
	}
	if err := writePatchPlans([]*patchPlan{remove}); err != nil {
		t.Fatal(err)
	}
	restored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(restored), darwinCloudCodeSetting) || !strings.Contains(string(restored), "// Keep this comment") || !strings.Contains(string(restored), `"nested": { "enabled": true }`) {
		t.Fatalf("remove altered unrelated settings: %s", restored)
	}
}

func TestDarwinCloudCodeSettingTakesOverAndPreservesAnotherEndpointInPlan(t *testing.T) {
	target, configRoot := newDarwinSettingsFixture(t)
	settings := filepath.Join(configRoot, "Antigravity", "User", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	const foreign = "https://example.invalid/cloudcode"
	if err := os.WriteFile(settings, []byte(`{"jetski.cloudCodeUrl":`+"\""+foreign+"\"}"), 0o600); err != nil {
		t.Fatal(err)
	}
	path, _, err := darwinTargetUserSettingsPath(target)
	if err != nil {
		t.Fatal(err)
	}
	plan, changed, err := prepareDarwinEnsureCloudCodeSetting(path, "http://127.0.0.1:50999")
	if err != nil || !changed || plan == nil {
		t.Fatalf("foreign endpoint must be accepted for managed takeover, plan=%#v changed=%t err=%v", plan, changed, err)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil || !strings.Contains(string(data), foreign) {
		t.Fatalf("planning changed the active foreign endpoint: %s (%v)", data, readErr)
	}
	if !strings.Contains(string(plan.updated), "http://127.0.0.1:50999") || strings.Contains(string(plan.updated), foreign) {
		t.Fatalf("takeover plan did not replace the scoped endpoint: %s", plan.updated)
	}
}

func TestDarwinTargetUserSettingsPathRejectsUnverifiedConnectionChain(t *testing.T) {
	target, _ := newDarwinSettingsFixture(t)
	if err := os.WriteFile(target.extensionEntry, []byte(`const unrelated=true;`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := darwinTargetUserSettingsPath(target); err == nil {
		t.Fatal("unverified extension was accepted")
	}
}
