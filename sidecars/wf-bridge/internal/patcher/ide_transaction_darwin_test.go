//go:build darwin

package patcher

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestInstalledDarwinIDEUpgradePlansWhenRequested is read-only. It verifies
// that an IDE previously modified by an older WF release can be rebuilt from
// its retained clean historical backups without touching /Applications.
func TestInstalledDarwinIDEUpgradePlansWhenRequested(t *testing.T) {
	app := strings.TrimSpace(os.Getenv("ANTIGRAVITY_WF_TEST_DARWIN_IDE_ROOT"))
	if app == "" {
		t.Skip("set ANTIGRAVITY_WF_TEST_DARWIN_IDE_ROOT to an installed IDE for read-only upgrade-plan validation")
	}
	target, ok := inspectDarwinApp(app)
	if !ok || target.kind != "ide" {
		t.Fatalf("installed fixture was not discovered as an IDE: ok=%t target=%+v", ok, target)
	}
	var plans []*patchPlan
	for _, path := range darwinImageGenerationUIRendererPaths(target) {
		plan, ready, err := prepareDarwinSafeImageRendererPlan(path)
		if err != nil {
			t.Fatal(err)
		}
		if !ready {
			t.Fatalf("installed renderer has no safe upgrade plan: %s", path)
		}
		if plan.changed && containsKnownDarwinPatch(plan.original) {
			t.Fatalf("upgrade plan did not recover vendor renderer bytes: %s", path)
		}
		plans = append(plans, plan)
	}
	if len(plans) == 0 {
		t.Fatal("installed IDE exposes no verified image renderer")
	}
	productPlan, err := prepareDarwinIDEProductChecksumPatch(target, plans)
	if err != nil {
		t.Fatal(err)
	}
	if productPlan != nil && containsKnownDarwinPatch(productPlan.original) {
		t.Fatal("product checksum upgrade plan did not recover canonical bytes")
	}
}

// TestMutableDarwinIDEApplyRestoreWhenFixturePresent validates a disposable
// IDE fixture assembled from a real version's clean renderer/product bytes.
// The fixture may be minimal, but its minified renderer and checksum data are
// real; settings are redirected to a test-only Application Support directory.
func TestMutableDarwinIDEApplyRestoreWhenFixturePresent(t *testing.T) {
	app := strings.TrimSpace(os.Getenv("ANTIGRAVITY_WF_MUTABLE_TEST_DARWIN_IDE_ROOT"))
	if app == "" {
		t.Skip("set ANTIGRAVITY_WF_MUTABLE_TEST_DARWIN_IDE_ROOT to a disposable clean IDE fixture")
	}
	app, err := filepath.Abs(app)
	if err != nil {
		t.Fatal(err)
	}
	realApp, err := filepath.EvalSymlinks(app)
	if err != nil {
		t.Fatal(err)
	}
	if realApp == "/Applications" || strings.HasPrefix(realApp, "/Applications/") ||
		realApp == "/System/Applications" || strings.HasPrefix(realApp, "/System/Applications/") {
		t.Fatalf("mutable IDE fixture must not be a system installation: %s", realApp)
	}

	target, ok := inspectDarwinApp(realApp)
	if !ok || target.kind != "ide" {
		t.Fatalf("fixture was not discovered as an IDE: ok=%t target=%+v", ok, target)
	}
	configRoot := t.TempDir()
	previousConfigDirectory := darwinUserConfigDirectory
	darwinUserConfigDirectory = func() (string, error) { return configRoot, nil }
	t.Cleanup(func() { darwinUserConfigDirectory = previousConfigDirectory })
	backupDir := strings.TrimSpace(os.Getenv("ANTIGRAVITY_WF_MUTABLE_TEST_DARWIN_IDE_BACKUP_DIR"))
	if backupDir == "" {
		backupDir = t.TempDir()
	}
	t.Setenv("ANTIGRAVITY_WF_BACKUP_DIR", backupDir)

	renderers := darwinImageGenerationUIRendererPaths(target)
	if len(renderers) == 0 {
		t.Fatal("real IDE fixture has no supported renderer paths")
	}
	paths := append(append([]string(nil), renderers...), darwinIDEProductPath(target))
	expectedRestore := make(map[string][]byte, len(paths))
	var preparedRendererPlans []*patchPlan
	for _, path := range paths {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		expectedRestore[path] = data
	}
	if err := verifyDarwinIDEProductChecksums(target, renderers); err != nil {
		t.Fatalf("clean fixture checksum mismatch: %v", err)
	}
	for _, path := range renderers {
		plan, ready, planErr := prepareDarwinSafeImageRendererPlan(path)
		if planErr != nil || !ready {
			t.Fatalf("IDE fixture renderer has no safe plan: %s ready=%t err=%v", path, ready, planErr)
		}
		preparedRendererPlans = append(preparedRendererPlans, plan)
		expectedRestore[path] = plan.original
	}
	if productPlan, planErr := prepareDarwinIDEProductChecksumPatch(target, preparedRendererPlans); planErr != nil {
		t.Fatal(planErr)
	} else if productPlan != nil {
		expectedRestore[productPlan.path] = productPlan.original
	}

	message, err := applyDarwinSafeIDETarget(target)
	if err != nil {
		t.Fatalf("IDE connection transaction failed: %v", err)
	}
	if !strings.Contains(message, "已安全连接本地代理") {
		t.Fatalf("unexpected IDE apply result: %s", message)
	}
	settingsPath := darwinSettingsPathForStatus(target)
	if !darwinCloudCodeSettingIsConfigured(settingsPath, currentPatchProxyEndpoint().Base) {
		t.Fatal("IDE user-level endpoint setting was not applied")
	}
	for _, path := range renderers {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if !darwinImageRendererReady(data) {
			t.Fatalf("IDE renderer did not receive the complete image UI patch: %s", path)
		}
	}
	if err := verifyDarwinIDEProductChecksums(target, renderers); err != nil {
		t.Fatalf("patched fixture checksum mismatch: %v", err)
	}

	if _, err := restoreDarwinSafeIDETarget(target); err != nil {
		t.Fatalf("IDE restore transaction failed: %v", err)
	}
	for _, path := range paths {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if !bytes.Equal(data, expectedRestore[path]) {
			t.Fatalf("IDE restore was not byte-exact: %s", path)
		}
	}
	if darwinCloudCodeSettingIsConfigured(settingsPath, currentPatchProxyEndpoint().Base) {
		t.Fatal("IDE restore retained the helper-owned endpoint setting")
	}
	if err := verifyDarwinIDEProductChecksums(target, renderers); err != nil {
		t.Fatalf("restored fixture checksum mismatch: %v", err)
	}
}
