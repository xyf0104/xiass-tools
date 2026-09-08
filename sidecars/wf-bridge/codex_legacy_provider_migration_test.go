package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"antigravity-wf-assistant/internal/codexconfig"
)

func TestGetCodexConfigurationOnlyReportsSafeLegacyMigrationEligibility(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	manager := codexconfig.NewManager(home)
	secret := "legacy-key-must-not-leak-from-read-only-status"
	original := []byte(`model_provider = "xiass"
model = "gpt-5.6-sol"
[model_providers.xiass]
experimental_bearer_token = "` + secret + `"
`)
	if err := os.WriteFile(manager.ConfigPath, original, 0o600); err != nil {
		t.Fatal(err)
	}

	status := (&App{}).GetCodexConfiguration()
	if !status.OK || !status.Snapshot.LegacyProviderMigration.Available || status.Snapshot.LegacyProviderMigration.ProviderID != "xiass" || !status.Snapshot.LegacyProviderMigration.WasActive {
		t.Fatalf("legacy migration read-only status = %#v", status)
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{secret, home, manager.ConfigPath} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("read-only migration status leaked protected data: %s", encoded)
		}
	}
	if got, err := os.ReadFile(manager.ConfigPath); err != nil || !bytes.Equal(got, original) {
		t.Fatalf("opening Codex configuration mutated legacy config: %q / %v", got, err)
	}
	if _, err := os.Lstat(filepath.Dir(manager.BackupRoot)); !os.IsNotExist(err) {
		t.Fatalf("read-only status created migration artifacts: %v", err)
	}
}

func TestMigrateCodexLegacyProviderBridgeIsRedactedAndConfigOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	manager := codexconfig.NewManager(home)
	secret := "bridge-legacy-key-must-not-leak"
	authSentinel := "bridge-auth-must-not-be-read"
	historySentinel := "bridge-history-must-not-be-read"
	if err := os.WriteFile(manager.ConfigPath, []byte(`model_provider = "xiass"
model = "gpt-5.6-sol"
review_model = "gpt-5.6-luna"
[model_providers.xiass]
experimental_bearer_token = "`+secret+`"
[model_providers.other]
experimental_bearer_token = "other-secret"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	authPath := filepath.Join(home, "auth.json")
	historyPath := filepath.Join(home, "sessions", "bridge-sentinel.jsonl")
	if err := os.WriteFile(authPath, []byte(authSentinel), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(historyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(historyPath, []byte(historySentinel), 0o600); err != nil {
		t.Fatal(err)
	}
	// If the migration bridge accidentally uses the broad configuration refresh
	// it would enumerate this invalid history root and report a failure.
	historyRoot := filepath.Join(filepath.Dir(manager.BackupRoot), "history-backups")
	if err := os.MkdirAll(filepath.Dir(historyRoot), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(historyRoot, []byte("must-not-enumerate-history"), 0o600); err != nil {
		t.Fatal(err)
	}

	status := (&App{}).MigrateCodexLegacyProvider()
	if !status.OK || !status.LegacyProviderMigrationCompleted || !status.LegacyProviderMigrationWasActive || status.Snapshot.ModelProvider != codexconfig.DefaultProviderID {
		t.Fatalf("bridge migration status = %#v", status)
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{secret, authSentinel, historySentinel, home, manager.ConfigPath, "other-secret"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("bridge migration leaked protected data: %s", encoded)
		}
	}
	if len(status.HistoryBackups) != 0 || len(status.LegacyBackups) != 0 {
		t.Fatalf("config-only migration exposed history state: %#v", status)
	}
	if got, err := os.ReadFile(authPath); err != nil || string(got) != authSentinel {
		t.Fatalf("migration changed auth.json: %q / %v", got, err)
	}
	if got, err := os.ReadFile(historyPath); err != nil || string(got) != historySentinel {
		t.Fatalf("migration changed history: %q / %v", got, err)
	}
	if got, err := os.ReadFile(historyRoot); err != nil || string(got) != "must-not-enumerate-history" {
		t.Fatalf("migration touched history backup root: %q / %v", got, err)
	}
}

func TestMigrateCodexLegacyProviderBridgeReturnsGenericFailure(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	manager := codexconfig.NewManager(home)
	secret := "unsupported-legacy-provider-secret"
	original := []byte(`model_provider = "xiass"
note = """
unsupported TOML must not be disclosed
"""
[model_providers.xiass]
experimental_bearer_token = "` + secret + `"
`)
	if err := os.WriteFile(manager.ConfigPath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	status := (&App{}).MigrateCodexLegacyProvider()
	if status.OK || status.LegacyProviderMigrationCompleted {
		t.Fatalf("unsafe bridge migration unexpectedly succeeded: %#v", status)
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{secret, home, manager.ConfigPath, "unsupported TOML"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("bridge error leaked protected content: %s", encoded)
		}
	}
	if got, err := os.ReadFile(manager.ConfigPath); err != nil || !bytes.Equal(got, original) {
		t.Fatalf("unsafe bridge migration changed config: %q / %v", got, err)
	}
}
