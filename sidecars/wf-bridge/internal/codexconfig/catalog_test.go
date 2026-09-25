package codexconfig

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCatalogApplyPreservesOriginalAndRepairsRealDirectory(t *testing.T) {
	home := t.TempDir()
	m := NewManager(home)
	original := []byte(`{"custom_metadata":true,"models":[{"slug":"private-model","custom":42},{"slug":"gpt-5.6-sol","supports_search_tool":true,"base_instructions":"local instructions"}]}`)
	source := filepath.Join(home, "original.json")
	if err := os.WriteFile(source, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.ConfigPath, []byte("model_catalog_json = 'original.json'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	input := ApplyConfig{BaseURL: "https://example.test", APIKey: "test-only", Model: "gpt-6-sol"}
	result, err := m.Apply(input)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := m.Inspect()
	if err != nil {
		t.Fatal(err)
	}
	ids := strings.Join(snapshot.ConfiguredModels, ",")
	for _, id := range []string{"private-model", "gpt-6-sol", "gpt-6-luna"} {
		if !strings.Contains(ids, id) {
			t.Fatalf("missing configured model %s", id)
		}
	}
	written, _ := os.ReadFile(m.ConfigPath)
	if bytes.Contains(written, []byte("models = [")) {
		t.Fatal("provider models is not a catalog")
	}
	if _, err := m.Apply(input); err != nil {
		t.Fatal(err)
	}
	again, _ := os.ReadFile(m.ConfigPath)
	if !bytes.Equal(written, again) {
		t.Fatal("second apply changed catalog path")
	}
	stillOriginal, _ := os.ReadFile(source)
	if !bytes.Equal(original, stillOriginal) {
		t.Fatal("modified original catalog")
	}
	if _, err := m.Restore(result.BackupID); err != nil {
		t.Fatal(err)
	}
	restored, _ := os.ReadFile(m.ConfigPath)
	if !bytes.Contains(restored, []byte("original.json")) {
		t.Fatal("restore lost original catalog pointer")
	}
}

func TestCatalogInspectNeverInventsModels(t *testing.T) {
	home := t.TempDir()
	m := NewManager(home)
	if err := os.WriteFile(m.ConfigPath, []byte("model = 'actual-model'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := m.Inspect()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(snapshot.ConfiguredModels, []string{"actual-model"}) {
		t.Fatalf("invented configured models: %v", snapshot.ConfiguredModels)
	}
}

func TestMalformedCatalogStopsBeforeConfigWrite(t *testing.T) {
	home := t.TempDir()
	m := NewManager(home)
	config := []byte("model_catalog_json = 'bad.json'\n")
	if err := os.WriteFile(m.ConfigPath, config, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "bad.json"), []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Apply(ApplyConfig{BaseURL: "https://example.test", APIKey: "test-only"}); err == nil {
		t.Fatal("accepted malformed catalog")
	}
	after, _ := os.ReadFile(m.ConfigPath)
	if !bytes.Equal(after, config) {
		t.Fatal("config changed on failure")
	}
}
