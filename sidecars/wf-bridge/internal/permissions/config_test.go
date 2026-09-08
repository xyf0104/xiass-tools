package permissions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestApplyAndDisablePreservesExistingGrants(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	statePath := filepath.Join(dir, "state.json")
	backupPath := filepath.Join(dir, "backup.json")
	initial := `{"userSettings":{"autoExecutionPolicy":"CASCADE_COMMANDS_AUTO_EXECUTION_ASK","globalPermissionGrants":{"allow":["read_file(C:\\\\work)","command(custom-existing)"]}}}`
	if err := os.WriteFile(configPath, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	m := NewWithPaths(configPath, statePath, backupPath)

	status, err := m.Apply(Settings{Enabled: true, Mode: "development"})
	if err != nil {
		t.Fatal(err)
	}
	if !status.Enabled || len(status.ManagedGrants) != len(developmentGrants) {
		t.Fatalf("unexpected status: %+v", status)
	}
	config := readTestConfig(t, configPath)
	settings := config["userSettings"].(map[string]any)
	if settings["autoExecutionPolicy"] != eagerPolicy {
		t.Fatalf("policy = %v", settings["autoExecutionPolicy"])
	}
	allow := stringSlice(settings["globalPermissionGrants"].(map[string]any)["allow"])
	if !contains(allow, "command(custom-existing)") || !contains(allow, "command(go)") {
		t.Fatalf("allow = %#v", allow)
	}
	if _, err := os.Stat(backupPath); err != nil {
		t.Fatalf("backup missing: %v", err)
	}

	if _, err := m.Apply(Settings{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	config = readTestConfig(t, configPath)
	settings = config["userSettings"].(map[string]any)
	if settings["autoExecutionPolicy"] != "CASCADE_COMMANDS_AUTO_EXECUTION_ASK" {
		t.Fatalf("policy not restored: %v", settings["autoExecutionPolicy"])
	}
	allow = stringSlice(settings["globalPermissionGrants"].(map[string]any)["allow"])
	if !contains(allow, "command(custom-existing)") || contains(allow, "command(go)") {
		t.Fatalf("allow after disable = %#v", allow)
	}
}

func TestEnableCreatesMissingConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config", "config.json")
	m := NewWithPaths(configPath, filepath.Join(dir, "state.json"), configPath+".backup")
	if _, err := m.Apply(Settings{Enabled: true, Mode: "all"}); err != nil {
		t.Fatal(err)
	}
	config := readTestConfig(t, configPath)
	settings := config["userSettings"].(map[string]any)
	allow := stringSlice(settings["globalPermissionGrants"].(map[string]any)["allow"])
	if settings["autoExecutionPolicy"] != eagerPolicy || !contains(allow, "command(*)") {
		t.Fatalf("missing created permission settings: %#v", settings)
	}
}

func TestSwitchModesOnlyReplacesManagedRules(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{"userSettings":{"globalPermissionGrants":{"allow":["command(existing)"]}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m := NewWithPaths(configPath, filepath.Join(dir, "state.json"), filepath.Join(dir, "backup.json"))
	if _, err := m.Apply(Settings{Enabled: true, Mode: "all"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Apply(Settings{Enabled: true, Mode: "custom", CustomRules: []string{" command(make test) ", "command(make test)"}}); err != nil {
		t.Fatal(err)
	}
	config := readTestConfig(t, configPath)
	settings := config["userSettings"].(map[string]any)
	allow := stringSlice(settings["globalPermissionGrants"].(map[string]any)["allow"])
	if contains(allow, "command(*)") || !contains(allow, "command(existing)") || !contains(allow, "command(make test)") {
		t.Fatalf("allow = %#v", allow)
	}
}

func TestRejectsInvalidCustomRule(t *testing.T) {
	if _, err := grantsFor(Settings{Mode: "custom", CustomRules: []string{"write_file(*)"}}); err == nil {
		t.Fatal("expected invalid custom rule error")
	}
	if _, err := grantsFor(Settings{Mode: "custom", CustomRules: []string{"command(*)"}}); err == nil {
		t.Fatal("expected command wildcard to require all mode")
	}
}

func TestDisableBeforeEnableDoesNotChangeConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	initial := []byte(`{"userSettings":{"autoExecutionPolicy":"CASCADE_COMMANDS_AUTO_EXECUTION_ASK","globalPermissionGrants":{"allow":["command(existing)"]}}}`)
	if err := os.WriteFile(configPath, initial, 0o600); err != nil {
		t.Fatal(err)
	}
	m := NewWithPaths(configPath, filepath.Join(dir, "state.json"), filepath.Join(dir, "backup.json"))
	if _, err := m.Apply(Settings{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(initial) {
		t.Fatalf("config changed before feature was enabled:\n%s", got)
	}
}

func readTestConfig(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
