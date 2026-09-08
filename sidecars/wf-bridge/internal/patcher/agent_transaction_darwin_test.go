//go:build darwin

package patcher

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestMutableDarwinAgentApplyRestoreWhenFixturePresent performs the complete
// production transaction only against an explicitly supplied disposable full
// application copy. It must never point at /Applications: the source fixture
// remains read-only and is copied by the caller before enabling this test.
func TestMutableDarwinAgentApplyRestoreWhenFixturePresent(t *testing.T) {
	app := strings.TrimSpace(os.Getenv("ANTIGRAVITY_WF_MUTABLE_TEST_DARWIN_AGENT_ROOT"))
	if app == "" {
		t.Skip("set ANTIGRAVITY_WF_MUTABLE_TEST_DARWIN_AGENT_ROOT to a disposable full Antigravity 2.app copy")
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
		t.Fatalf("mutable Agent fixture must not be a system installation: %s", realApp)
	}
	if _, err := exec.LookPath("codesign"); err != nil {
		t.Skip("codesign is unavailable")
	}
	if output, err := exec.Command("codesign", "--verify", "--deep", "--strict", realApp).CombinedOutput(); err != nil {
		t.Fatalf("disposable source app is not an intact vendor fixture: %s: %v", output, err)
	}

	target, ok := inspectDarwinApp(realApp)
	if !ok || target.kind != "agent" {
		t.Fatalf("disposable source app was not discovered as an Agent: ok=%t target=%+v", ok, target)
	}
	supported, _, reason := darwinTargetConnectionSupport(target)
	if !supported {
		t.Fatalf("disposable source app failed production support gates: %s", reason)
	}
	plistPath := filepath.Join(realApp, "Contents", "Info.plist")
	paths := []string{plistPath, target.asar, target.language}
	original := make(map[string][]byte, len(paths))
	for _, path := range paths {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		original[path] = data
	}

	t.Setenv("ANTIGRAVITY_WF_BACKUP_DIR", t.TempDir())
	t.Setenv("ANTIGRAVITY_WF_SKIP_CODESIGN", "")
	message, err := applyDarwinASARPatch(target)
	if err != nil {
		t.Fatalf("complete Agent transaction failed: %v", err)
	}
	if !strings.Contains(message, "补丁应用成功") {
		t.Fatalf("unexpected apply result: %s", message)
	}
	if err := verifyDarwinAgentASARIntegrity(target); err != nil {
		t.Fatalf("patched ElectronAsarIntegrity mismatch: %v", err)
	}
	if hasArchive, patched := darwinAgentEmbeddedUIPatchState(target.language); !hasArchive || !patched {
		t.Fatalf("patched Language Server did not expose both Agent UI markers: archive=%t patched=%t", hasArchive, patched)
	}
	if output, err := exec.Command("codesign", "--verify", "--strict", target.language).CombinedOutput(); err != nil {
		t.Fatalf("patched Language Server signature is invalid: %s: %v", output, err)
	}

	if _, err := restoreDarwinPatch(target); err != nil {
		t.Fatalf("complete Agent restore failed: %v", err)
	}
	for _, path := range paths {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if !bytes.Equal(data, original[path]) {
			t.Fatalf("restore was not byte-exact: %s", path)
		}
	}
	if output, err := exec.Command("codesign", "--verify", "--deep", "--strict", realApp).CombinedOutput(); err != nil {
		t.Fatalf("restored vendor application signature is invalid: %s: %v", output, err)
	}
}
