package codexconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func TestMigrateLegacyProviderPreservesOnlySafeConfigurationSemantics(t *testing.T) {
	home := t.TempDir()
	manager := legacyProviderMigrationTestManager(home)
	secret := "legacy-provider-secret-must-never-cross-a-result"
	authSentinel := "auth-json-must-not-be-read"
	historySentinel := "history-must-not-be-read"
	unmanaged := strings.Join([]string{
		`[model_providers.external]`,
		`name = "External Provider"`,
		`base_url = "https://third-party.invalid/v1"`,
		`experimental_bearer_token = "third-party-secret-must-remain-opaque"`,
		`[mcp_servers.keep]`,
		`command = "external-command"`,
		`args = ["--keep"]`,
	}, "\n")
	original := strings.Join([]string{
		"# Preserve every unrelated byte.",
		`model_provider = "xiass" # old first-party provider`,
		`model = "gpt-5.6-sol"`,
		`review_model = "gpt-5.6-luna"`,
		`model_context_window = 512000`,
		`model_auto_compact_token_limit = 460800`,
		`web_search = "cached"`,
		unmanaged,
		`[model_providers."xiass"] # historical XIASS provider`,
		`name = "Historical XIASS"`,
		`base_url = "https://api.xiass.example/v1"`,
		`wire_api = "responses"`,
		`experimental_bearer_token = "` + secret + `"`,
		`http_headers = { "x-upstream" = "opaque-header" }`,
		`[model_providers."xiass".nested]`,
		`mode = "keep"`,
		"",
	}, "\n")
	original = strings.ReplaceAll(original, "\n", "\r\n")
	if err := os.WriteFile(manager.ConfigPath, []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}
	authPath := filepath.Join(home, "auth.json")
	historyPath := filepath.Join(home, "sessions", "migration-untouched.jsonl")
	if err := os.WriteFile(authPath, []byte(authSentinel), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(historyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(historyPath, []byte(historySentinel), 0o600); err != nil {
		t.Fatal(err)
	}

	status, err := manager.InspectLegacyProviderMigration()
	if err != nil || !status.Available || status.ProviderID != "xiass" || !status.WasActive {
		t.Fatalf("safe migration status = %#v / %v", status, err)
	}
	encodedStatus, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encodedStatus), secret) || strings.Contains(string(encodedStatus), home) {
		t.Fatalf("migration status leaked protected material: %s", encodedStatus)
	}

	result, err := manager.MigrateLegacyProvider()
	if err != nil {
		t.Fatalf("MigrateLegacyProvider() error = %v", err)
	}
	if !result.Migrated || result.ProviderID != "xiass" || !result.WasActive || result.BackupID == "" || result.ConfigSHA == "" {
		t.Fatalf("migration result = %#v", result)
	}
	encodedResult, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{secret, authSentinel, historySentinel, home, manager.ConfigPath} {
		if strings.Contains(string(encodedResult), forbidden) {
			t.Fatalf("migration result leaked protected material: %s", encodedResult)
		}
	}

	written, err := os.ReadFile(manager.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`model_provider = "xiass_tools" # old first-party provider`,
		`model = "gpt-5.6-sol"`,
		`review_model = "gpt-5.6-luna"`,
		`model_context_window = 512000`,
		`model_auto_compact_token_limit = 460800`,
		`web_search = "cached"`,
		strings.ReplaceAll(unmanaged, "\n", "\r\n"),
		`[model_providers.xiass_tools] # historical XIASS provider`,
		secret,
		`[model_providers.xiass_tools.nested]`,
	} {
		if !strings.Contains(string(written), expected) {
			t.Fatalf("migrated config missing %q:\n%s", expected, written)
		}
	}
	if strings.Contains(string(written), `[model_providers."xiass"]`) {
		t.Fatalf("legacy provider heading remained:\n%s", written)
	}
	if !strings.Contains(string(written), "\r\n") {
		t.Fatal("migration unexpectedly normalized CRLF content")
	}

	var root map[string]any
	if err := toml.Unmarshal(written, &root); err != nil {
		t.Fatalf("migrated config is invalid TOML: %v", err)
	}
	if got := stringValue(root["model_provider"]); got != DefaultProviderID {
		t.Fatalf("model_provider = %q, want %q", got, DefaultProviderID)
	}
	providers, ok := mapValue(root["model_providers"])
	if !ok || providers[DefaultProviderID] == nil {
		t.Fatalf("current provider is missing: %#v", root["model_providers"])
	}
	if _, exists := providers["xiass"]; exists {
		t.Fatalf("legacy provider remains: %#v", providers)
	}
	if got, err := os.ReadFile(authPath); err != nil || string(got) != authSentinel {
		t.Fatalf("auth.json was read or changed: %q / %v", got, err)
	}
	if got, err := os.ReadFile(historyPath); err != nil || string(got) != historySentinel {
		t.Fatalf("history was read or changed: %q / %v", got, err)
	}
	backup, err := os.ReadFile(manager.originalPath(result.BackupID))
	if err != nil || !bytes.Equal(backup, []byte(original)) {
		t.Fatalf("migration backup did not preserve original bytes: %v", err)
	}
	backups, err := manager.ListBackups()
	if err != nil || len(backups) != 1 || backups[0].Reason != "migrate_legacy_provider" {
		t.Fatalf("migration backup metadata = %#v / %v", backups, err)
	}
}

func TestMigrateLegacyProviderIsHardcodedToExactFirstPartyIDs(t *testing.T) {
	for _, providerID := range []string{"xiass-copy", "codex_local_access_legacy", "XIASS", "third-party-xiass"} {
		t.Run(providerID, func(t *testing.T) {
			home := t.TempDir()
			manager := legacyProviderMigrationTestManager(home)
			original := []byte(`model_provider = "` + providerID + `"
model = "keep"
[model_providers."` + providerID + `"]
experimental_bearer_token = "must-not-be-adopted"
`)
			if err := os.WriteFile(manager.ConfigPath, original, 0o600); err != nil {
				t.Fatal(err)
			}
			status, err := manager.InspectLegacyProviderMigration()
			if err != nil || status.Available {
				t.Fatalf("exact-ID status = %#v / %v", status, err)
			}
			result, err := manager.MigrateLegacyProvider()
			if err != nil || result.Migrated || result.BackupID != "" {
				t.Fatalf("exact-ID migration = %#v / %v", result, err)
			}
			if got, err := os.ReadFile(manager.ConfigPath); err != nil || !bytes.Equal(got, original) {
				t.Fatalf("non-first-party Provider changed: %q / %v", got, err)
			}
			assertNoLegacyProviderMigrationArtifacts(t, manager)
		})
	}
}

func TestMigrateLegacyProviderNoOpAndUnsafeFormsNeverCreateManagedArtifacts(t *testing.T) {
	tests := map[string]struct {
		content string
		wantErr bool
	}{
		"no current or legacy provider": {
			content: `model_provider = "official"
[model_providers.official]
name = "Official"
`,
		},
		"two legacy providers are ambiguous": {
			content: `model_provider = "xiass"
[model_providers.xiass]
experimental_bearer_token = "first"
[model_providers.codex_local_access]
experimental_bearer_token = "second"
`,
			wantErr: true,
		},
		"inline legacy provider is unsupported": {
			content: `model_provider = "xiass"
[model_providers]
xiass = { experimental_bearer_token = "must-not-be-copied" }
`,
			wantErr: true,
		},
		"unrelated multiline value is unsafe": {
			content: `model_provider = "xiass"
note = """
do not rewrite unknown TOML
"""
[model_providers.xiass]
experimental_bearer_token = "must-not-be-copied"
`,
			wantErr: true,
		},
		"invalid TOML": {
			content: `model_provider = "xiass"
[model_providers.xiass
`,
			wantErr: true,
		},
	}
	for name, testCase := range tests {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			manager := legacyProviderMigrationTestManager(home)
			original := []byte(testCase.content)
			if err := os.WriteFile(manager.ConfigPath, original, 0o600); err != nil {
				t.Fatal(err)
			}
			result, err := manager.MigrateLegacyProvider()
			if testCase.wantErr && err == nil {
				t.Fatalf("unsafe migration unexpectedly succeeded: %#v", result)
			}
			if !testCase.wantErr && err != nil {
				t.Fatalf("no-op migration error = %v", err)
			}
			if result.Migrated || result.BackupID != "" {
				t.Fatalf("non-migration result = %#v", result)
			}
			if got, readErr := os.ReadFile(manager.ConfigPath); readErr != nil || !bytes.Equal(got, original) {
				t.Fatalf("unsafe/no-op config changed: %q / %v", got, readErr)
			}
			assertNoLegacyProviderMigrationArtifacts(t, manager)
		})
	}
}

func TestMigrateLegacyProviderRefusesWhileCodexSafetyGuardRejects(t *testing.T) {
	home := t.TempDir()
	manager := NewManagerWithOptions(home, ManagerOptions{
		HistoryWriteGuard: func() error { return ErrCodexHistoryWriteUnsafe },
	})
	original := []byte(`model_provider = "codex_local_access"
[model_providers.codex_local_access]
experimental_bearer_token = "guarded-secret"
`)
	if err := os.WriteFile(manager.ConfigPath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := manager.MigrateLegacyProvider()
	if !errors.Is(err, ErrCodexHistoryWriteUnsafe) || result.Migrated || result.BackupID != "" {
		t.Fatalf("guarded migration = %#v / %v", result, err)
	}
	if got, readErr := os.ReadFile(manager.ConfigPath); readErr != nil || !bytes.Equal(got, original) {
		t.Fatalf("guarded migration changed config: %q / %v", got, readErr)
	}
	assertNoLegacyProviderMigrationArtifacts(t, manager)
}

func TestMigrateLegacyProviderRollsBackAfterPostWriteFailure(t *testing.T) {
	home := t.TempDir()
	writeCalls := 0
	manager := NewManagerWithOptions(home, ManagerOptions{
		HistoryWriteGuard: func() error { return nil },
		legacyProviderMigrationWrite: func(path string, data []byte, mode os.FileMode) error {
			writeCalls++
			if err := writeFileAtomic(path, data, mode); err != nil {
				return err
			}
			return errors.New("simulated post-write migration failure")
		},
	})
	original := []byte(`model_provider = "xiass"
model = "gpt-5.6-sol"
[model_providers.xiass]
experimental_bearer_token = "rollback-secret"
`)
	if err := os.WriteFile(manager.ConfigPath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := manager.MigrateLegacyProvider()
	var mutationErr *MutationError
	if !errors.As(err, &mutationErr) || mutationErr.RollbackErr != nil || result.Migrated || writeCalls != 1 {
		t.Fatalf("rollback migration = %#v / %#v / calls=%d", result, err, writeCalls)
	}
	if got, readErr := os.ReadFile(manager.ConfigPath); readErr != nil || !bytes.Equal(got, original) {
		t.Fatalf("rollback did not restore original: %q / %v", got, readErr)
	}
	backups, listErr := manager.ListBackups()
	if listErr != nil || len(backups) != 1 || backups[0].Reason != "migrate_legacy_provider" {
		t.Fatalf("rollback backup metadata = %#v / %v", backups, listErr)
	}
}

func legacyProviderMigrationTestManager(home string) *Manager {
	return NewManagerWithOptions(home, ManagerOptions{HistoryWriteGuard: func() error { return nil }})
}

func assertNoLegacyProviderMigrationArtifacts(t *testing.T, manager *Manager) {
	t.Helper()
	for _, path := range []string{filepath.Dir(manager.BackupRoot), manager.LockPath} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read-only migration preflight created managed artifact %q: %v", path, err)
		}
	}
}
