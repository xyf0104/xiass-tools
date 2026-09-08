package codexconfig

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const originalConfig = `model_provider = "official"
model = "gpt-old"
model_reasoning_effort = "ultra"
web_search = "cached"

[mcp_servers.example]
command = "example-mcp"

[desktop]
appearanceTheme = "system"

[model_providers.official]
name = "Official"
base_url = "https://example.com/v1"
wire_api = "responses"
requires_openai_auth = true
`

func TestNormalizeBaseURL(t *testing.T) {
	tests := map[string]string{
		"remote host":        "api.xiass.com",
		"remote explicit":    "https://api.xiass.com/",
		"root query":         "https://api.xiass.com?ignored=true#fragment",
		"full chat endpoint": "https://api.xiass.com/v1/chat/completions",
		"loopback host":      "127.0.0.1:54843",
		"loopback IPv6":      "[::1]:54843/v1/",
	}
	wants := map[string]string{
		"remote host":        "https://api.xiass.com/v1",
		"remote explicit":    "https://api.xiass.com/v1",
		"root query":         "https://api.xiass.com/v1",
		"full chat endpoint": "https://api.xiass.com/v1",
		"loopback host":      "http://127.0.0.1:54843/v1",
		"loopback IPv6":      "http://[::1]:54843/v1",
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := NormalizeBaseURL(input)
			if err != nil {
				t.Fatal(err)
			}
			if got != wants[name] {
				t.Fatalf("NormalizeBaseURL(%q) = %q, want %q", input, got, wants[name])
			}
		})
	}
	for _, input := range []string{
		"http://api.xiass.com",
		"ftp://127.0.0.1:54843",
		"https://user:secret@api.xiass.com",
		"http://localhost.example.com",
	} {
		if _, err := NormalizeBaseURL(input); err == nil {
			t.Fatalf("unsafe base URL was accepted: %q", input)
		}
	}
}

func TestDefaultCodexHomeHonorsConfiguredAbsoluteOrRelativePath(t *testing.T) {
	configured := filepath.Join(t.TempDir(), "configured-codex")
	t.Setenv("CODEX_HOME", configured)
	got, err := DefaultCodexHome()
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.Abs(configured)
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Clean(want) {
		t.Fatalf("DefaultCodexHome() = %q, want %q", got, filepath.Clean(want))
	}

	t.Setenv("CODEX_HOME", "invalid\npath")
	if _, err := DefaultCodexHome(); err == nil {
		t.Fatal("invalid CODEX_HOME was accepted")
	}
}

func TestApplyInspectAndRestorePreservesUserConfiguration(t *testing.T) {
	home := t.TempDir()
	manager := NewManager(home)
	if err := os.WriteFile(manager.ConfigPath, []byte(originalConfig), 0o640); err != nil {
		t.Fatal(err)
	}
	result, err := manager.Apply(ApplyConfig{
		BaseURL:                    "api.xiass.com/v1/chat/completions",
		APIKey:                     "sk-test-1234567890",
		KeyName:                    "无风的 XIASS",
		Model:                      "gpt-5.6-sol",
		ReviewModel:                "gpt-5.6-luna",
		WebSearch:                  "cached",
		ModelContextWindow:         512000,
		ModelAutoCompactTokenLimit: 460800,
	})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if result.BackupID == "" || result.ConfigSHA == "" || result.ProviderID != DefaultProviderID {
		t.Fatalf("incomplete apply result: %+v", result)
	}
	written, err := os.ReadFile(manager.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(written)
	for _, expected := range []string{
		`model_provider = "xiass_tools"`,
		`model = "gpt-5.6-sol"`,
		`review_model = "gpt-5.6-luna"`,
		`web_search = "cached"`,
		`name = "无风的 XIASS"`,
		`base_url = "https://api.xiass.com/v1"`,
		`experimental_bearer_token = "sk-test-1234567890"`,
		`[mcp_servers.example]`,
		`[model_providers.official]`,
		`model_reasoning_effort = "ultra"`,
	} {
		if !strings.Contains(text, expected) {
			t.Errorf("written config omitted %q", expected)
		}
	}
	if strings.Count(text, "[model_providers.xiass_tools]") != 1 {
		t.Fatalf("managed provider was duplicated:\n%s", text)
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(manager.ConfigPath); err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("config mode = %v / %v, want 0600", info, err)
		}
	}

	snapshot, err := manager.Verify()
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Valid || snapshot.ModelProvider != DefaultProviderID || snapshot.Model != "gpt-5.6-sol" || snapshot.WebSearch != "cached" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if got := strings.Join(snapshot.ConfiguredModels, ","); got != "gpt-5.6-luna,gpt-5.6-sol" {
		t.Fatalf("configured models = %q", got)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "sk-test-1234567890") {
		t.Fatalf("inspection leaked API key: %s", encoded)
	}

	backup, err := os.ReadFile(manager.originalPath(result.BackupID))
	if err != nil {
		t.Fatal(err)
	}
	if string(backup) != originalConfig {
		t.Fatal("backup did not preserve original config byte-for-byte")
	}
	restored, err := manager.Restore(result.BackupID)
	if err != nil {
		t.Fatalf("Restore() error = %v", err)
	}
	if restored.SafetyBackupID == "" {
		t.Fatal("restore did not create a safety backup")
	}
	configAfterRestore, err := os.ReadFile(manager.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(configAfterRestore) != originalConfig {
		t.Fatal("restore did not restore original bytes")
	}
}

func TestInspectManagedProviderVerificationRequiresCompleteActiveSemantics(t *testing.T) {
	home := t.TempDir()
	manager := NewManager(home)
	secret := "managed-provider-secret-must-not-appear-in-status"
	if _, err := manager.Apply(ApplyConfig{
		BaseURL:                    "https://api.xiass.com",
		APIKey:                     secret,
		Model:                      "gpt-5.6-sol",
		ReviewModel:                "gpt-5.6-luna",
		WebSearch:                  "live",
		ModelContextWindow:         372000,
		ModelAutoCompactTokenLimit: 334800,
	}); err != nil {
		t.Fatal(err)
	}
	valid, err := os.ReadFile(manager.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name      string
		content   string
		wantIssue ManagedProviderIssue
	}{
		{name: "fully managed provider", content: string(valid), wantIssue: ManagedProviderIssueNone},
		{name: "inactive provider is not verified", content: strings.Replace(string(valid), `model_provider = "xiass_tools"`, `model_provider = "official"`, 1), wantIssue: ManagedProviderIssueInactive},
		{name: "missing bearer", content: strings.Replace(string(valid), `experimental_bearer_token = "`+secret+`"`, `experimental_bearer_token = ""`, 1), wantIssue: ManagedProviderIssueBearer},
		{name: "wrong wire api", content: strings.Replace(string(valid), `wire_api = "responses"`, `wire_api = "chat"`, 1), wantIssue: ManagedProviderIssueWireAPI},
		{name: "wrong actor header", content: strings.Replace(string(valid), `"x-openai-actor-authorization" = "api.xiass.com"`, `"x-openai-actor-authorization" = "wrong.example"`, 1), wantIssue: ManagedProviderIssueHeaders},
		{name: "extra header is not managed", content: strings.Replace(string(valid), `http_headers = { "x-openai-actor-authorization" = "api.xiass.com" }`, `http_headers = { "x-openai-actor-authorization" = "api.xiass.com", "x-extra" = "opaque" }`, 1), wantIssue: ManagedProviderIssueHeaders},
		{name: "missing explicit context", content: strings.Replace(string(valid), "model_context_window = 372000\n", "", 1), wantIssue: ManagedProviderIssueContext},
		{name: "invalid web search", content: strings.Replace(string(valid), `web_search = "live"`, `web_search = "unsupported"`, 1), wantIssue: ManagedProviderIssueWebSearch},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if err := os.WriteFile(manager.ConfigPath, []byte(testCase.content), 0o600); err != nil {
				t.Fatal(err)
			}
			snapshot, err := manager.Inspect()
			if err != nil || !snapshot.Valid {
				t.Fatalf("Inspect() = %#v / %v", snapshot, err)
			}
			wantVerified := testCase.wantIssue == ManagedProviderIssueNone
			if snapshot.ManagedProviderVerified != wantVerified || snapshot.ManagedProviderIssue != testCase.wantIssue {
				t.Fatalf("managed provider state = verified:%v issue:%q, want verified:%v issue:%q", snapshot.ManagedProviderVerified, snapshot.ManagedProviderIssue, wantVerified, testCase.wantIssue)
			}
			encoded, err := json.Marshal(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), secret) {
				t.Fatalf("managed provider status leaked bearer token: %s", encoded)
			}
		})
	}
}

func TestApplyPreservesLegacyProviderUntilExplicitMigration(t *testing.T) {
	home := t.TempDir()
	manager := NewManager(home)
	legacy := `model_provider = "xiass"

[model_providers.xiass]
name = "Old XIASS"
base_url = "https://old.example.com/v1"
experimental_bearer_token = "old-secret"

[model_providers.third_party]
name = "Do not alter"
base_url = "https://third.example.com/v1"
`
	if err := os.WriteFile(manager.ConfigPath, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Apply(ApplyConfig{BaseURL: "https://api.xiass.com", APIKey: "new-secret"}); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(manager.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(written), "old-secret") || !strings.Contains(string(written), "[model_providers.xiass]") {
		t.Fatal("ordinary save unexpectedly changed a legacy provider")
	}
	if !strings.Contains(string(written), "[model_providers.third_party]") {
		t.Fatal("unrelated provider was changed")
	}
}

func TestApplyReplacesQuotedManagedProviderAndNestedSections(t *testing.T) {
	manager := NewManager(t.TempDir())
	original := `model_provider = "xiass_tools"

[model_providers."xiass_tools"] # a previous valid representation
name = "Previous"
base_url = "https://previous.example/v1"
experimental_bearer_token = "previous-secret"

[model_providers."xiass_tools".nested]
value = "must be removed with the provider"

[model_providers.keep]
name = "Keep"
base_url = "https://keep.example/v1"
`
	if err := os.WriteFile(manager.ConfigPath, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Apply(ApplyConfig{BaseURL: "https://api.xiass.com", APIKey: "new-secret"}); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(manager.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(written), "previous-secret") || strings.Contains(string(written), "must be removed") {
		t.Fatalf("quoted managed provider was not fully replaced:\n%s", written)
	}
	if !strings.Contains(string(written), "[model_providers.keep]") {
		t.Fatal("unrelated provider was removed")
	}
}

func TestApplyRejectsInvalidConfigAndSymlink(t *testing.T) {
	home := t.TempDir()
	manager := NewManager(home)
	invalid := []byte("[broken\nvalue = true\n")
	if err := os.WriteFile(manager.ConfigPath, invalid, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Apply(ApplyConfig{BaseURL: "https://api.xiass.com", APIKey: "test-key"}); err == nil || !strings.Contains(err.Error(), "existing config.toml is invalid") {
		t.Fatalf("invalid config apply error = %v", err)
	}
	after, err := os.ReadFile(manager.ConfigPath)
	if err != nil || string(after) != string(invalid) {
		t.Fatalf("invalid config changed: %q / %v", after, err)
	}
	if runtime.GOOS == "windows" {
		return
	}
	target := filepath.Join(home, "target.toml")
	if err := os.WriteFile(target, []byte(originalConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(manager.ConfigPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, manager.ConfigPath); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Apply(ApplyConfig{BaseURL: "https://api.xiass.com", APIKey: "test-key"}); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("symlink config apply error = %v", err)
	}
}

func TestRestoreRejectsTamperedBackup(t *testing.T) {
	manager := NewManager(t.TempDir())
	if err := os.WriteFile(manager.ConfigPath, []byte(originalConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := manager.Apply(ApplyConfig{BaseURL: "https://api.xiass.com", APIKey: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(manager.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manager.originalPath(result.BackupID), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Restore(result.BackupID); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("tampered backup restore error = %v", err)
	}
	after, err := os.ReadFile(manager.ConfigPath)
	if err != nil || string(before) != string(after) {
		t.Fatalf("config changed after failed restore: %q / %v", after, err)
	}
}

func TestRestoreRemovesNewConfigAndDeleteBackupIsConstrained(t *testing.T) {
	manager := NewManager(t.TempDir())
	result, err := manager.Apply(ApplyConfig{BaseURL: "https://api.xiass.com", APIKey: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Restore(result.BackupID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(manager.ConfigPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("config exists after restoring absent original: %v", err)
	}
	for _, invalidID := range []string{"../outside", "..", `..\\outside`} {
		if err := manager.DeleteBackup(invalidID); err == nil {
			t.Fatalf("backup deletion accepted a traversal ID: %q", invalidID)
		}
	}
	if err := manager.DeleteBackup(result.BackupID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(manager.BackupRoot, result.BackupID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backup directory remains after deletion: %v", err)
	}
}

func TestDiscoverModelsUsesNormalizedOpenAIEndpoint(t *testing.T) {
	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-5.6-sol"},{"id":"gpt-5.6-luna"},{"id":"gpt-5.6-sol"},{"id":"bad\nmodel"}]}`))
	}))
	defer server.Close()
	models, err := DiscoverModels(context.Background(), server.URL, "local-key", ModelDiscoveryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if authorization != "Bearer local-key" {
		t.Fatalf("authorization = %q", authorization)
	}
	if got := strings.Join(models, ","); got != "gpt-5.6-luna,gpt-5.6-sol" {
		t.Fatalf("models = %q", got)
	}
}

func TestDiscoverModelsDoesNotFollowRedirects(t *testing.T) {
	redirectTargetHits := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		redirectTargetHits++
		_, _ = w.Write([]byte(`{"data":[{"id":"unexpected"}]}`))
	}))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer server.Close()
	if _, err := DiscoverModels(nil, server.URL, "local-key", ModelDiscoveryOptions{}); err == nil || !strings.Contains(err.Error(), "HTTP 302") {
		t.Fatalf("redirecting model endpoint error = %v", err)
	}
	if redirectTargetHits != 0 {
		t.Fatalf("model discovery followed redirect %d time(s)", redirectTargetHits)
	}
}

func TestOperationLockSerializesAndIgnoresStaleContents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "locks", "config.lock")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("99999999\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	release, err := AcquireOperationLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := AcquireOperationLock(path); err == nil {
		t.Fatal("second operation acquired a held lock")
	}
}

func TestWorkspaceStateRepairUsesBackupAndIsIdempotent(t *testing.T) {
	manager := NewManager(t.TempDir())
	statePath := filepath.Join(manager.CodexHome, ".codex-global-state.json")
	state := map[string]any{
		"active-workspace-roots": []any{`\Users\wufeng\Desktop\codex`},
		"local-projects": map[string]any{
			"project-root": map[string]any{"name": `\Users\wufeng\Desktop\codex`, "rootPaths": []any{}},
		},
		"thread-workspace-root-hints": map[string]any{"thread-a": `\Users\wufeng\Desktop\codex`},
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := manager.RepairWorkspaceStateForOS("darwin")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Scanned || !result.Updated || result.ProjectCount != 1 || result.BackupID == "" {
		t.Fatalf("workspace repair = %+v", result)
	}
	if err := verifyWorkspaceState(statePath, "darwin"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(manager.BackupRoot), workspaceStateBackupDirectory, result.BackupID, ".codex-global-state.json")); err != nil {
		t.Fatalf("workspace backup missing: %v", err)
	}
	second, err := manager.RepairWorkspaceStateForOS("darwin")
	if err != nil {
		t.Fatal(err)
	}
	if second.Updated || second.BackupID != "" {
		t.Fatalf("workspace repair was not idempotent: %+v", second)
	}
}
