//go:build darwin

package patcher

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestOfficialDarwinIDEImageChecksumCandidateWhenFixturePresent validates the
// complete read-only IDE transaction against an official mounted App Bundle.
// It never writes to the bundle: renderer and product.json candidates remain
// in memory, while the source bytes are re-read at the end to prove that the
// fixture was not modified.
func TestOfficialDarwinIDEImageChecksumCandidateWhenFixturePresent(t *testing.T) {
	root := os.Getenv("ANTIGRAVITY_WF_TEST_IDE_APP_ROOT")
	if root == "" {
		t.Skip("official IDE renderer fixture is not configured")
	}
	appPath := filepath.Clean(filepath.Join(root, "..", "..", ".."))
	target := darwinTargets{app: appPath, kind: "ide"}
	paths := []string{
		filepath.Join(root, "out", "jetskiAgent", "main.js"),
		filepath.Join(root, "out", "vs", "workbench", "workbench.desktop.main.js"),
	}

	originals := make(map[string][]byte, len(paths)+1)
	plans := make([]*patchPlan, 0, len(paths))
	for _, path := range paths {
		original, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		originals[path] = original
		plan, ready, err := prepareDarwinSafeImageRendererPlan(path)
		if err != nil || plan == nil || !ready || !plan.changed {
			t.Fatalf("official IDE renderer candidate is unavailable: path=%s ready=%t plan=%#v err=%v", path, ready, plan, err)
		}
		previewReady := bytes.Contains(plan.updated, []byte(imagePreviewPatchMarker)) ||
			bytes.Contains(plan.updated, []byte(imagePreviewNativeCompatibleMarker))
		if !previewReady || !bytes.Contains(plan.updated, []byte(imageGenerationUIPatchMarker)) ||
			!bytes.Contains(plan.updated, []byte(imageGenerationDedupePatchMarker)) {
			t.Fatalf("official IDE renderer candidate is missing current image markers: %s", path)
		}
		second, result := patchImagePreviewRenderer(string(plan.updated))
		if !result.Recognized || result.Changed || second != string(plan.updated) {
			t.Fatalf("official IDE renderer candidate is not idempotent: %s %#v", path, result)
		}
		plans = append(plans, plan)
	}

	productPath := filepath.Join(root, "product.json")
	productOriginal, err := os.ReadFile(productPath)
	if err != nil {
		t.Fatal(err)
	}
	originals[productPath] = productOriginal
	var official darwinIDEProductIntegrity
	if err := json.Unmarshal(productOriginal, &official); err != nil {
		t.Fatal(err)
	}
	for _, plan := range plans {
		relative, err := filepath.Rel(filepath.Join(root, "out"), plan.path)
		if err != nil {
			t.Fatal(err)
		}
		key := filepath.ToSlash(relative)
		expected, tracked := official.Checksums[key]
		if !tracked {
			t.Fatalf("official product.json does not track renderer %s", key)
		}
		if expected != darwinIDEChecksum(plan.original) {
			t.Fatalf("official product.json checksum is invalid before patch: %s", key)
		}
	}

	productPlan, err := prepareDarwinIDEProductChecksumPatch(target, plans)
	if err != nil || productPlan == nil || !productPlan.changed {
		t.Fatalf("official product.json candidate was not produced: plan=%#v err=%v", productPlan, err)
	}
	var candidate darwinIDEProductIntegrity
	if err := json.Unmarshal(productPlan.updated, &candidate); err != nil {
		t.Fatal(err)
	}
	for _, plan := range plans {
		relative, err := filepath.Rel(filepath.Join(root, "out"), plan.path)
		if err != nil {
			t.Fatal(err)
		}
		key := filepath.ToSlash(relative)
		if candidate.Checksums[key] != darwinIDEChecksum(plan.updated) {
			t.Fatalf("candidate product.json checksum does not match patched renderer: %s", key)
		}
	}
	if bytes.Equal(productPlan.original, productPlan.updated) || !strings.Contains(string(productPlan.updated), "\"checksums\"") {
		t.Fatal("candidate product.json did not preserve a valid checksum document")
	}

	for path, expected := range originals {
		actual, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(actual, expected) {
			t.Fatalf("read-only official IDE validation modified %s", path)
		}
	}
}
