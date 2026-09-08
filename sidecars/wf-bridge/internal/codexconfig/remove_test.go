package codexconfig

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func TestRemoveXIASSProviderPreservesUnmanagedTOMLByteLayout(t *testing.T) {
	manager := NewManager(t.TempDir())
	unmanagedTopLevel := `model_reasoning_effort = "ultra"
web_search = "cached"
custom_unknown = "keep exactly"`
	unmanagedMCP := `[mcp_servers.keep]
command = "keep-mcp"
args = ["--safe"]`
	unmanagedDesktop := `[desktop]
appearanceTheme = "system"
unknown_desktop_flag = true`
	unmanagedProvider := `[model_providers.other]
name = "Other provider"
base_url = "https://other.example/v1"
wire_api = "responses"`
	unmanagedNested := `[feature_flags]
enabled = true`
	original := strings.Join([]string{
		"# Preserve this user comment and every unrelated setting.",
		`model_provider = "xiass_tools"`,
		`model = "gpt-5.6-sol"`,
		`review_model = "gpt-5.6-luna"`,
		unmanagedTopLevel,
		unmanagedMCP,
		unmanagedDesktop,
		unmanagedProvider,
		`[model_providers."xiass_tools"] # XIASS Tools only`,
		`name = "XIASS Tools"`,
		`base_url = "https://api.xiass.com/v1"`,
		`experimental_bearer_token = "secret-must-leave-config"`,
		`[model_providers."xiass_tools".nested]`,
		`value = "also removed"`,
		unmanagedNested,
		"",
	}, "\n")
	// Preserve CRLF too: a disconnect must not normalize every unrelated line.
	original = strings.ReplaceAll(original, "\n", "\r\n")
	if err := os.WriteFile(manager.ConfigPath, []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}

	result, err := manager.RemoveXIASSProvider()
	if err != nil {
		t.Fatalf("RemoveXIASSProvider() error = %v", err)
	}
	if !result.Removed || !result.WasActive || result.BackupID == "" || result.ConfigSHA == "" {
		t.Fatalf("remove result = %#v", result)
	}
	written, err := os.ReadFile(manager.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, unchanged := range []string{unmanagedTopLevel, unmanagedMCP, unmanagedDesktop, unmanagedProvider, unmanagedNested} {
		want := strings.ReplaceAll(unchanged, "\n", "\r\n")
		if !strings.Contains(string(written), want) {
			t.Fatalf("unmanaged TOML block was changed or removed:\n%s", want)
		}
	}
	for _, removed := range []string{
		`model_provider = "xiass_tools"`,
		`model = "gpt-5.6-sol"`,
		`review_model = "gpt-5.6-luna"`,
		"secret-must-leave-config",
		"also removed",
		`[model_providers."xiass_tools"]`,
	} {
		if strings.Contains(string(written), removed) {
			t.Fatalf("XIASS-owned TOML remains after removal: %q\n%s", removed, written)
		}
	}
	if !strings.Contains(string(written), "\r\n") {
		t.Fatal("unmanaged CRLF line endings were normalized")
	}

	var root map[string]any
	if err := toml.Unmarshal(written, &root); err != nil {
		t.Fatalf("removed config is invalid TOML: %v", err)
	}
	for _, key := range []string{"model_provider", "model", "review_model"} {
		if _, exists := root[key]; exists {
			t.Fatalf("active selection key %q remains: %#v", key, root[key])
		}
	}
	if providers, ok := mapValue(root["model_providers"]); !ok || providers["other"] == nil {
		t.Fatalf("unmanaged provider did not survive: %#v", root["model_providers"])
	} else if _, exists := providers[DefaultProviderID]; exists {
		t.Fatalf("XIASS provider still exists: %#v", providers)
	}

	backup, err := os.ReadFile(manager.originalPath(result.BackupID))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(backup, []byte(original)) {
		t.Fatal("removal backup did not preserve original config byte-for-byte")
	}
	backups, err := manager.ListBackups()
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 || backups[0].Reason != "remove_xiass_provider" {
		t.Fatalf("removal backup metadata = %#v", backups)
	}
}

func TestRemoveXIASSProviderLeavesSelectionsAloneWhenAnotherProviderIsActive(t *testing.T) {
	manager := NewManager(t.TempDir())
	original := `model_provider = "official"
model = "gpt-official"
review_model = "gpt-review"

[model_providers.official]
name = "Official"
base_url = "https://official.example/v1"

[model_providers.xiass_tools]
name = "XIASS Tools"
base_url = "https://api.xiass.com/v1"
experimental_bearer_token = "remove-this-only"
`
	if err := os.WriteFile(manager.ConfigPath, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := manager.RemoveXIASSProvider()
	if err != nil {
		t.Fatal(err)
	}
	if !result.Removed || result.WasActive {
		t.Fatalf("inactive removal result = %#v", result)
	}
	written, err := os.ReadFile(manager.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, retained := range []string{
		`model_provider = "official"`,
		`model = "gpt-official"`,
		`review_model = "gpt-review"`,
		`[model_providers.official]`,
	} {
		if !strings.Contains(string(written), retained) {
			t.Fatalf("inactive-provider selection changed: missing %q", retained)
		}
	}
	if strings.Contains(string(written), "remove-this-only") || strings.Contains(string(written), "[model_providers.xiass_tools]") {
		t.Fatalf("XIASS provider remains:\n%s", written)
	}
}

func TestRemoveXIASSProviderIsVerifiedNoOpForOtherProviders(t *testing.T) {
	manager := NewManager(t.TempDir())
	original := []byte(`model_provider = "third_party"
model = "keep-model"

[model_providers.third_party]
name = "Do not remove"
base_url = "https://third.example/v1"
`)
	if err := os.WriteFile(manager.ConfigPath, original, 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := manager.RemoveXIASSProvider()
	if err != nil {
		t.Fatal(err)
	}
	if result.Removed || result.WasActive || result.BackupID != "" || result.ConfigSHA != "" {
		t.Fatalf("no-op remove result = %#v", result)
	}
	if got, err := os.ReadFile(manager.ConfigPath); err != nil || !bytes.Equal(got, original) {
		t.Fatalf("no-op changed config: %q / %v", got, err)
	}
	backups, err := manager.ListBackups()
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 0 {
		t.Fatalf("no-op created backups: %#v", backups)
	}
	assertNoXIASSRemovalArtifacts(t, manager)
}

func TestRemoveXIASSProviderWithoutConfigLeavesNoManagedArtifacts(t *testing.T) {
	manager := NewManager(t.TempDir())
	result, err := manager.RemoveXIASSProvider()
	if err != nil {
		t.Fatal(err)
	}
	if result.Removed || result.WasActive || result.BackupID != "" {
		t.Fatalf("missing-config removal result = %#v", result)
	}
	assertNoXIASSRemovalArtifacts(t, manager)
}

func TestRemoveXIASSProviderIsHardcodedToXIASSProviderID(t *testing.T) {
	manager := NewManagerWithOptions(t.TempDir(), ManagerOptions{ProviderID: "another_provider"})
	original := []byte(`model_provider = "another_provider"
model = "keep-model"

[model_providers.another_provider]
name = "Not XIASS Tools"
base_url = "https://another.example/v1"
`)
	if err := os.WriteFile(manager.ConfigPath, original, 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := manager.RemoveXIASSProvider()
	if err != nil {
		t.Fatal(err)
	}
	if result.Removed || result.WasActive {
		t.Fatalf("custom Manager.ProviderID was removed: %#v", result)
	}
	if got, err := os.ReadFile(manager.ConfigPath); err != nil || !bytes.Equal(got, original) {
		t.Fatalf("custom provider config changed: %q / %v", got, err)
	}
}

func TestRemoveXIASSProviderFailsClosedForUnsupportedOrInvalidTOML(t *testing.T) {
	tests := map[string]string{
		"inline provider table": `model_provider = "xiass_tools"
[model_providers]
xiass_tools = { name = "XIASS Tools", base_url = "https://api.xiass.com/v1" }
`,
		"multiline unknown value": `model_provider = "xiass_tools"
description = """
This unrelated multiline value must never be rewritten.
"""
[model_providers.xiass_tools]
name = "XIASS Tools"
`,
		"invalid TOML": `model_provider = "xiass_tools"
[model_providers.xiass_tools
`,
	}
	for name, original := range tests {
		t.Run(name, func(t *testing.T) {
			manager := NewManager(t.TempDir())
			if err := os.WriteFile(manager.ConfigPath, []byte(original), 0o600); err != nil {
				t.Fatal(err)
			}
			result, err := manager.RemoveXIASSProvider()
			if err == nil || !strings.Contains(err.Error(), "no changes were made") {
				t.Fatalf("unsafe removal error = %v", err)
			}
			if result.Removed || result.BackupID != "" {
				t.Fatalf("unsafe removal returned a mutation result: %#v", result)
			}
			if got, readErr := os.ReadFile(manager.ConfigPath); readErr != nil || string(got) != original {
				t.Fatalf("unsafe removal changed config: %q / %v", got, readErr)
			}
			backups, listErr := manager.ListBackups()
			if listErr != nil {
				t.Fatal(listErr)
			}
			if len(backups) != 0 {
				t.Fatalf("unsafe removal created backups: %#v", backups)
			}
			assertNoXIASSRemovalArtifacts(t, manager)
		})
	}
}

func assertNoXIASSRemovalArtifacts(t *testing.T, manager *Manager) {
	t.Helper()
	for _, path := range []string{filepath.Dir(manager.BackupRoot), manager.LockPath} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read-only removal preflight created managed artifact %q: %v", path, err)
		}
	}
}
