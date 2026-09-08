//go:build darwin

package patcher

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func newDarwinIntegrityFixture(t *testing.T) (darwinTargets, string, []byte) {
	t.Helper()
	app := filepath.Join(t.TempDir(), "Antigravity.app")
	renderer := filepath.Join(app, "Contents", "Resources", "app", "out", "jetskiAgent", "main.js")
	product := filepath.Join(app, "Contents", "Resources", "app", "product.json")
	if err := os.MkdirAll(filepath.Dir(renderer), 0o755); err != nil {
		t.Fatal(err)
	}
	original := []byte("official renderer")
	if err := os.WriteFile(renderer, original, 0o644); err != nil {
		t.Fatal(err)
	}
	metadata := map[string]any{
		"nameShort": "Antigravity",
		"checksums": map[string]string{"jetskiAgent/main.js": darwinIDEChecksum(original)},
		"other":     map[string]any{"keep": true},
	}
	data, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(product, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return darwinTargets{app: app, kind: "ide"}, renderer, original
}

func TestDarwinIDEProductChecksumPatchUpdatesOnlyTrackedRenderer(t *testing.T) {
	target, renderer, original := newDarwinIntegrityFixture(t)
	updated := []byte("patched renderer")
	plan := &patchPlan{path: renderer, original: original, updated: updated, mode: 0o644, changed: true}
	productPlan, err := prepareDarwinIDEProductChecksumPatch(target, []*patchPlan{plan})
	if err != nil || productPlan == nil || !productPlan.changed {
		t.Fatalf("product plan=%#v err=%v", productPlan, err)
	}
	if err := writePatchPlans([]*patchPlan{plan, productPlan}); err != nil {
		t.Fatal(err)
	}
	if err := verifyDarwinIDEProductChecksums(target, []string{renderer}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(darwinIDEProductPath(target))
	if err != nil {
		t.Fatal(err)
	}
	var product struct {
		Checksums map[string]string `json:"checksums"`
		Other     map[string]bool   `json:"other"`
	}
	if err := json.Unmarshal(data, &product); err != nil {
		t.Fatal(err)
	}
	if product.Checksums["jetskiAgent/main.js"] != darwinIDEChecksum(updated) || !product.Other["keep"] {
		t.Fatalf("product checksum update was not minimal: %#v", product)
	}
}

func TestDarwinIDEProductChecksumPatchSynchronizesThirdPartyChecksum(t *testing.T) {
	target, renderer, original := newDarwinIntegrityFixture(t)
	productPath := darwinIDEProductPath(target)
	data, err := os.ReadFile(productPath)
	if err != nil {
		t.Fatal(err)
	}
	var product map[string]any
	if err := json.Unmarshal(data, &product); err != nil {
		t.Fatal(err)
	}
	product["checksums"] = map[string]string{"jetskiAgent/main.js": darwinIDEChecksum([]byte("unrelated"))}
	data, err = json.Marshal(product)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(productPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	plan := &patchPlan{path: renderer, original: original, updated: []byte("patched renderer"), mode: 0o644, changed: true}
	productPlan, err := prepareDarwinIDEProductChecksumPatch(target, []*patchPlan{plan})
	if err != nil || productPlan == nil || !productPlan.changed {
		t.Fatalf("third-party checksum was not synchronized: plan=%#v err=%v", productPlan, err)
	}
	if !bytes.Contains(productPlan.updated, []byte(darwinIDEChecksum(plan.updated))) {
		t.Fatalf("synchronized product checksum is missing candidate hash: %s", productPlan.updated)
	}
}
