package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"antigravity-wf-assistant/internal/codexconfig"
	"antigravity-wf-assistant/internal/codexdesktop"
	"antigravity-wf-assistant/internal/storage"
)

func TestRestoreCodexConfigurationRefusesWhileDesktopIsRunningOrUnverified(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		desktop *codexLifecycleFakeDesktop
	}{
		{name: "running", desktop: newCodexLifecycleFakeDesktop(true)},
		{name: "process inspection unavailable", desktop: func() *codexLifecycleFakeDesktop {
			desktop := newCodexLifecycleFakeDesktop(false)
			desktop.status.Warnings = []codexdesktop.Warning{codexdesktop.WarningProcessListUnavailable}
			return desktop
		}()},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("CODEX_HOME", home)
			manager := codexconfig.NewManager(home)
			original := []byte("model_provider = \"official\"\nmodel = \"original-model\"\n")
			if err := os.WriteFile(manager.ConfigPath, original, 0o600); err != nil {
				t.Fatal(err)
			}
			applied, err := manager.Apply(codexconfig.ApplyConfig{BaseURL: "https://api.xiass.com", APIKey: "restore-guard-secret", Model: "gpt-5.6-sol"})
			if err != nil {
				t.Fatal(err)
			}
			before := readBridgeTestFile(t, manager.ConfigPath)
			backupsBefore, err := manager.ListBackups()
			if err != nil || len(backupsBefore) != 1 {
				t.Fatalf("precondition backups = %#v / %v", backupsBefore, err)
			}

			app := &App{ctx: context.Background(), codexDesktopControl: testCase.desktop}
			status := app.RestoreCodexConfiguration(applied.BackupID)
			if status.OK {
				t.Fatalf("restore unexpectedly ran while desktop state was unsafe: %#v", status)
			}
			if got := readBridgeTestFile(t, manager.ConfigPath); !bytes.Equal(got, before) {
				t.Fatalf("unsafe restore changed config.toml:\n%s", got)
			}
			backupsAfter, err := manager.ListBackups()
			if err != nil || len(backupsAfter) != len(backupsBefore) {
				t.Fatalf("unsafe restore created a recovery artifact: before=%#v after=%#v err=%v", backupsBefore, backupsAfter, err)
			}
			if testCase.desktop.stopCalls != 0 || testCase.desktop.launchCalls != 0 {
				t.Fatalf("direct restore controlled Desktop: stop=%d launch=%d", testCase.desktop.stopCalls, testCase.desktop.launchCalls)
			}
			encoded, err := json.Marshal(status)
			if err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{"restore-guard-secret", home, manager.ConfigPath} {
				if strings.Contains(string(encoded), forbidden) {
					t.Fatalf("unsafe restore response leaked %q: %s", forbidden, encoded)
				}
			}
		})
	}
}

func TestGetCodexConfigurationRedactsSecretMaterial(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	manager := codexconfig.NewManager(codexHome)
	secret := "sk-this-must-not-reach-the-renderer"
	legacySecret := "legacy-backup-secret-must-not-reach-the-renderer"
	content := []byte("model_provider = \"xiass_tools\"\nmodel = \"gpt-5.6-sol\"\n[model_providers.xiass_tools]\nname = \"XIASS Tools\"\nbase_url = \"https://api.xiass.com/v1\"\nexperimental_bearer_token = \"" + secret + "\"\n")
	if err := os.WriteFile(manager.ConfigPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	writeBridgeLegacyConfigBackup(t, manager, "20260830T010203.123456789Z-abcdef12", []byte("model = \"legacy\"\n[model_providers.legacy]\nexperimental_bearer_token = \""+legacySecret+"\"\n"))

	status := (&App{}).GetCodexConfiguration()
	if !status.OK || !status.Snapshot.Valid || status.Snapshot.ModelProvider != codexconfig.DefaultProviderID || len(status.LegacyBackups) != 1 {
		t.Fatalf("unexpected status: %#v", status)
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) || strings.Contains(string(encoded), legacySecret) {
		t.Fatalf("Codex status leaked secret material: %s", encoded)
	}
	if strings.Contains(string(encoded), codexHome) || strings.Contains(string(encoded), manager.ConfigPath) {
		t.Fatalf("Codex status leaked a local configuration path: %s", encoded)
	}
	if !status.Snapshot.Location.Exists || status.Snapshot.Location.CodexHome != "" || status.Snapshot.Location.ConfigPath != "" {
		t.Fatalf("renderer snapshot location = %#v, want only exists=true", status.Snapshot.Location)
	}
}

func TestCodexModelCatalogMessageDoesNotClaimResponsesInference(t *testing.T) {
	message := codexModelCatalogMessage(2)
	if !strings.Contains(message, "已发现 2 个模型 ID") || !strings.Contains(message, "尚未验证 Responses 推理") {
		t.Fatalf("catalog message = %q", message)
	}
	if strings.Contains(message, "可用模型") || strings.Contains(message, "测试通过") {
		t.Fatalf("catalog message must not claim model inference availability: %q", message)
	}
}

func TestCodexSavedAccountCandidatesDiscoverAndApplyWithoutLeakingCredentials(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	storage.Init(filepath.Join(home, "xiass-state"))

	const secret = "saved-codex-account-secret-must-not-render"
	const authSentinel = "external-codex-auth-json-must-not-be-read"
	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		authorization = request.Header.Get("Authorization")
		if request.URL.Path != "/v1/models" {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"data":[{"id":"gpt-saved-codex"}]}`))
	}))
	defer server.Close()

	account := storage.UpstreamAccount{
		ID: "saved-codex-account", Name: "团队 Codex Responses", Provider: "openai", Type: "api_key",
		APIURL: server.URL, EndpointMode: "auto", APIStyle: "responses", MessagePathMode: "auto",
		AuthMode: "bearer", APIKey: secret, Enabled: true, MaxConcurrency: 1,
	}
	if err := storage.SaveUpstreamAccount(account); err != nil {
		t.Fatal(err)
	}
	if err := storage.AddOrUpdateModel(storage.CustomModel{
		Name: "saved-codex-model", DisplayName: "GPT Saved Codex", Provider: "openai", APIStyle: "responses",
		EndpointMode: "auto", MessagePathMode: "auto", ExternalModelName: "gpt-saved-codex", AccountIDs: []string{account.ID},
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(authSentinel), 0o600); err != nil {
		t.Fatal(err)
	}

	app := &App{}
	candidates := app.GetCodexAccountCandidates()
	if !candidates.OK || len(candidates.Candidates) != 1 {
		t.Fatalf("saved-account candidates = %#v", candidates)
	}
	candidate := candidates.Candidates[0]
	if candidate.ID != account.ID || candidate.Label != account.Name || candidate.CredentialMode != "Bearer API Key" {
		t.Fatalf("candidate metadata = %#v", candidate)
	}
	if len(candidate.Models) != 1 || candidate.Models[0].ID != "gpt-saved-codex" || candidate.Models[0].DisplayName != "GPT Saved Codex" {
		t.Fatalf("candidate models = %#v", candidate.Models)
	}
	encodedCandidates, err := json.Marshal(candidates)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{secret, server.URL, authSentinel, home} {
		if strings.Contains(string(encodedCandidates), forbidden) {
			t.Fatalf("candidate response leaked %q: %s", forbidden, encodedCandidates)
		}
	}

	discovery := app.DiscoverCodexAccountModels(account.ID)
	if !discovery.OK || len(discovery.Models) != 1 || discovery.Models[0] != "gpt-saved-codex" {
		t.Fatalf("saved-account discovery = %#v", discovery)
	}
	if authorization != "Bearer "+secret {
		t.Fatalf("saved-account discovery authorization = %q", authorization)
	}
	encodedDiscovery, err := json.Marshal(discovery)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{secret, server.URL, authSentinel, home} {
		if strings.Contains(string(encodedDiscovery), forbidden) {
			t.Fatalf("discovery response leaked %q: %s", forbidden, encodedDiscovery)
		}
	}

	status := app.ApplyCodexConfigurationFromAccount(CodexApplyAccountInput{
		AccountID: account.ID, Model: "gpt-saved-codex", ReviewModel: "gpt-saved-codex", WebSearch: "cached",
		ModelContextWindow: 512000, ModelAutoCompactTokenLimit: 460800,
	})
	if !status.OK || status.Snapshot.ModelProvider != codexconfig.DefaultProviderID || !status.Snapshot.ManagedProviderVerified {
		t.Fatalf("saved-account apply status = %#v", status)
	}
	encodedStatus, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	// Codex's established, non-secret status projection includes the configured
	// provider root. It must never reveal the account credential, external
	// auth.json sentinel or local config directory.
	for _, forbidden := range []string{secret, authSentinel, home} {
		if strings.Contains(string(encodedStatus), forbidden) {
			t.Fatalf("apply response leaked %q: %s", forbidden, encodedStatus)
		}
	}
	manager := codexconfig.NewManager(home)
	written := readBridgeTestFile(t, manager.ConfigPath)
	for _, required := range []string{
		`base_url = "` + server.URL + `/v1"`,
		`experimental_bearer_token = "` + secret + `"`,
		`model = "gpt-saved-codex"`,
		`review_model = "gpt-saved-codex"`,
	} {
		if !bytes.Contains(written, []byte(required)) {
			t.Fatalf("saved account did not map %q into config:\n%s", required, written)
		}
	}
	backups, err := manager.ListBackups()
	if err != nil || len(backups) != 1 || backups[0].Reason != "apply" {
		t.Fatalf("apply backups = %#v / %v", backups, err)
	}
	if got := readBridgeTestFile(t, filepath.Join(home, "auth.json")); string(got) != authSentinel {
		t.Fatalf("saved-account apply touched external auth.json: %q", got)
	}
}

func TestCodexSavedAccountLifecycleApplyUsesNativeMapping(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	storage.Init(filepath.Join(home, "xiass-state"))
	const secret = "saved-codex-lifecycle-secret"
	account := storage.UpstreamAccount{
		ID: "saved-codex-lifecycle", Name: "Lifecycle Responses", Provider: "openai", Type: "api_key",
		APIURL: "https://gateway.example.test", EndpointMode: "auto", APIStyle: "responses", MessagePathMode: "auto",
		AuthMode: "bearer", APIKey: secret, Enabled: true,
	}
	if err := storage.SaveUpstreamAccount(account); err != nil {
		t.Fatal(err)
	}
	manager := codexconfig.NewManager(home)
	if err := os.WriteFile(manager.ConfigPath, []byte("model_provider = \"official\"\nmodel = \"previous\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	app := &App{ctx: context.Background(), codexDesktopControl: newCodexLifecycleFakeDesktop(false)}
	result := app.ApplyCodexConfigurationFromAccountWithLifecycle(CodexApplyAccountLifecycleInput{
		Account:     CodexApplyAccountInput{AccountID: account.ID, Model: "gpt-lifecycle", WebSearch: "live"},
		LaunchAfter: false,
	})
	if !result.OK || !result.Applied || result.RolledBack || result.Configuration.Snapshot.ModelProvider != codexconfig.DefaultProviderID {
		t.Fatalf("saved-account lifecycle result = %#v", result)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{secret, home, manager.ConfigPath} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("lifecycle response leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestCodexSavedAccountReuseRejectsLossyAndOAuthMappings(t *testing.T) {
	base := storage.UpstreamAccount{
		ID: "candidate", Name: "candidate", Provider: "openai", Type: "api_key", APIURL: "https://gateway.example.test",
		EndpointMode: "auto", APIStyle: "responses", MessagePathMode: "auto", AuthMode: "bearer", APIKey: "test-key", Enabled: true,
	}
	for _, testCase := range []struct {
		name   string
		mutate func(*storage.UpstreamAccount)
	}{
		{name: "paused", mutate: func(account *storage.UpstreamAccount) { account.Enabled = false }},
		{name: "non OpenAI provider", mutate: func(account *storage.UpstreamAccount) { account.Provider = "anthropic" }},
		{name: "chat completions", mutate: func(account *storage.UpstreamAccount) { account.APIStyle = "chat_completions" }},
		{name: "manual endpoint", mutate: func(account *storage.UpstreamAccount) { account.EndpointMode = "manual" }},
		{name: "manual message path", mutate: func(account *storage.UpstreamAccount) { account.MessagePathMode = "manual" }},
		{name: "x api key", mutate: func(account *storage.UpstreamAccount) { account.AuthMode = "x_api_key" }},
		{name: "custom header", mutate: func(account *storage.UpstreamAccount) {
			account.Headers = map[string]string{"X-Token": "must-not-copy"}
		}},
		{name: "generic OAuth refresh", mutate: func(account *storage.UpstreamAccount) { account.Type = "oauth" }},
		{name: "direct Codex OAuth", mutate: func(account *storage.UpstreamAccount) {
			account.Type = "oauth"
			account.OAuth = storage.OAuthConfiguration{Upstream: storage.OpenAICodexOAuthUpstream}
			account.APIURL = storage.OpenAICodexResponsesURL
		}},
		{name: "query route", mutate: func(account *storage.UpstreamAccount) {
			account.APIURL = "https://gateway.example.test/v1?tenant=private"
		}},
		{name: "non v1 path", mutate: func(account *storage.UpstreamAccount) { account.APIURL = "https://gateway.example.test/tenant" }},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			account := base
			testCase.mutate(&account)
			if codexAccountReusable(account) {
				t.Fatalf("lossy saved-account mapping was accepted: %#v", account)
			}
		})
	}
}

func TestRemoveCodexXIASSProviderIsRedactedAndDoesNotTouchAuthOrHistory(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	manager := codexconfig.NewManager(codexHome)
	providerSecret := "provider-secret-must-not-reach-the-renderer"
	authSecret := "auth-json-must-not-be-read-or-modified"
	historySentinel := "history-must-not-be-read-or-modified"
	config := []byte(`model_provider = "xiass_tools"
model = "gpt-5.6-sol"
review_model = "gpt-5.6-luna"

[mcp_servers.keep]
command = "keep"

[model_providers.xiass_tools]
name = "XIASS Tools"
base_url = "https://api.xiass.com/v1"
experimental_bearer_token = "` + providerSecret + `"

[model_providers.other]
name = "Other"
base_url = "https://other.example/v1"
`)
	if err := os.WriteFile(manager.ConfigPath, config, 0o600); err != nil {
		t.Fatal(err)
	}
	authPath := filepath.Join(codexHome, "auth.json")
	historyPath := filepath.Join(codexHome, "sessions", "preserve.jsonl")
	if err := os.WriteFile(authPath, []byte(`{"access_token":"`+authSecret+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(historyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(historyPath, []byte(historySentinel), 0o600); err != nil {
		t.Fatal(err)
	}

	status := (&App{}).RemoveCodexXIASSProvider()
	if !status.OK || status.Snapshot.ModelProvider != "" {
		t.Fatalf("remove status = %#v", status)
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{providerSecret, authSecret, historySentinel, codexHome, manager.ConfigPath} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("remove status leaked protected local data: %s", encoded)
		}
	}
	if got := readBridgeTestFile(t, authPath); !bytes.Contains(got, []byte(authSecret)) {
		t.Fatalf("auth.json changed during provider removal: %s", got)
	}
	if got := readBridgeTestFile(t, historyPath); string(got) != historySentinel {
		t.Fatalf("history changed during provider removal: %q", got)
	}
	if written := readBridgeTestFile(t, manager.ConfigPath); bytes.Contains(written, []byte(providerSecret)) || bytes.Contains(written, []byte("[model_providers.xiass_tools]")) {
		t.Fatalf("XIASS provider was not removed: %s", written)
	}
}

func TestRemoveCodexXIASSProviderReturnsGenericFailureForUnsupportedTOML(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	manager := codexconfig.NewManager(codexHome)
	secret := "unsupported-provider-secret-must-not-leak"
	original := []byte(`model_provider = "xiass_tools"
note = """
unrelated multiline setting
"""
[model_providers.xiass_tools]
experimental_bearer_token = "` + secret + `"
`)
	if err := os.WriteFile(manager.ConfigPath, original, 0o600); err != nil {
		t.Fatal(err)
	}

	status := (&App{}).RemoveCodexXIASSProvider()
	if status.OK {
		t.Fatalf("unsupported configuration unexpectedly removed: %#v", status)
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) || strings.Contains(string(encoded), codexHome) || strings.Contains(string(encoded), "multiline") {
		t.Fatalf("unsupported removal error leaked config details: %s", encoded)
	}
	if got := readBridgeTestFile(t, manager.ConfigPath); !bytes.Equal(got, original) {
		t.Fatalf("unsupported removal changed config: %s", got)
	}
}

func TestRemoveCodexXIASSProviderDoesNotEnumerateHistoryBackups(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	manager := codexconfig.NewManager(codexHome)
	if err := os.WriteFile(manager.ConfigPath, []byte(`model_provider = "xiass_tools"
[model_providers.xiass_tools]
name = "XIASS Tools"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	// A regular file where history-backups would normally be makes the generic
	// GetCodexConfiguration history-listing path fail. The provider removal
	// status must remain config-only and therefore succeed without touching it.
	historyRoot := filepath.Join(filepath.Dir(manager.BackupRoot), "history-backups")
	if err := os.MkdirAll(filepath.Dir(historyRoot), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(historyRoot, []byte("do-not-enumerate-history-backups"), 0o600); err != nil {
		t.Fatal(err)
	}

	status := (&App{}).RemoveCodexXIASSProvider()
	if !status.OK || status.Snapshot.ModelProvider != "" {
		t.Fatalf("config-only removal status = %#v", status)
	}
	if len(status.HistoryBackups) != 0 || len(status.LegacyBackups) != 0 {
		t.Fatalf("removal unexpectedly returned history state: %#v", status)
	}
	if got := readBridgeTestFile(t, historyRoot); string(got) != "do-not-enumerate-history-backups" {
		t.Fatalf("removal touched history backup root: %q", got)
	}
}

func TestGetCodexConfigurationReturnsEmptyLegacyArrayAndSafeWarning(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	manager := codexconfig.NewManager(home)
	if err := os.WriteFile(manager.ConfigPath, []byte("model_provider = \"openai\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	status := (&App{}).GetCodexConfiguration()
	if !status.OK || status.LegacyBackups == nil || len(status.LegacyBackups) != 0 || status.LegacyBackupWarning != "" {
		t.Fatalf("empty legacy state = %#v", status)
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"legacyBackups":[]`) {
		t.Fatalf("empty legacy backup array was not serialized: %s", encoded)
	}

	if runtime.GOOS == "windows" {
		return
	}
	unsafeTarget := t.TempDir()
	if err := os.MkdirAll(filepath.Join(unsafeTarget, "backups"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(unsafeTarget, filepath.Join(home, "xiass-helper")); err != nil {
		t.Fatal(err)
	}
	status = (&App{}).GetCodexConfiguration()
	if !status.OK || status.LegacyBackups == nil || len(status.LegacyBackups) != 0 || status.LegacyBackupWarning == "" {
		t.Fatalf("unsafe legacy root affected normal Codex status: %#v", status)
	}
	encoded, err = json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), unsafeTarget) {
		t.Fatalf("legacy warning leaked a filesystem path: %s", encoded)
	}
}

func TestImportCodexLegacyConfigBackupIsRedactedAndDoesNotTouchActiveConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	manager := codexconfig.NewManager(home)
	active := []byte("model_provider = \"openai\"\nmodel = \"gpt-active\"\n")
	if err := os.WriteFile(manager.ConfigPath, active, 0o600); err != nil {
		t.Fatal(err)
	}
	legacyID := "20260830T020304.123456789Z-bcdef123"
	legacySecret := "legacy-import-bridge-secret"
	writeBridgeLegacyConfigBackup(t, manager, legacyID, []byte("model = \"legacy\"\n[model_providers.legacy]\nexperimental_bearer_token = \""+legacySecret+"\"\n"))

	app := &App{}
	invalid := app.ImportCodexLegacyConfigBackup("../not-a-backup")
	if invalid.OK {
		t.Fatalf("invalid legacy ID unexpectedly succeeded: %#v", invalid)
	}
	if got := readBridgeTestFile(t, manager.ConfigPath); !bytes.Equal(got, active) {
		t.Fatal("invalid legacy import changed active config.toml")
	}
	encodedInvalid, err := json.Marshal(invalid)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encodedInvalid), legacySecret) {
		t.Fatalf("invalid import response leaked a secret: %s", encodedInvalid)
	}

	status := app.ImportCodexLegacyConfigBackup(legacyID)
	if !status.OK || !hasBridgeConfigurationBackup(status.Backups, "imported_legacy_config") {
		t.Fatalf("legacy configuration import status = %#v", status)
	}
	if got := readBridgeTestFile(t, manager.ConfigPath); !bytes.Equal(got, active) {
		t.Fatal("successful legacy import changed active config.toml")
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), legacySecret) {
		t.Fatalf("successful import response leaked a secret: %s", encoded)
	}
}

func TestImportCodexLegacyHistoryBackupRefreshesVisibleBackupWithoutMutatingActiveData(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	manager := codexconfig.NewManager(home)
	legacyID, sessionPath, databasePath := writeBridgeLegacyHistoryBackup(t, manager)
	activeConfig := readBridgeTestFile(t, manager.ConfigPath)
	activeSession := readBridgeTestFile(t, sessionPath)
	activeDatabase := readBridgeTestFile(t, databasePath)

	status := (&App{}).ImportCodexLegacyHistoryBackup(legacyID)
	if !status.OK || len(status.HistoryBackups) != 1 {
		t.Fatalf("legacy history import status = %#v", status)
	}
	if got := readBridgeTestFile(t, manager.ConfigPath); !bytes.Equal(got, activeConfig) {
		t.Fatal("history import changed active config.toml")
	}
	if got := readBridgeTestFile(t, sessionPath); !bytes.Equal(got, activeSession) {
		t.Fatal("history import changed active session data")
	}
	if got := readBridgeTestFile(t, databasePath); !bytes.Equal(got, activeDatabase) {
		t.Fatal("history import changed active SQLite data")
	}

	invalid := (&App{}).ImportCodexLegacyHistoryBackup("../invalid")
	if invalid.OK {
		t.Fatalf("invalid legacy history ID unexpectedly succeeded: %#v", invalid)
	}
	if got := readBridgeTestFile(t, manager.ConfigPath); !bytes.Equal(got, activeConfig) {
		t.Fatal("invalid history import changed active config.toml")
	}
}

func TestRestoreCodexConfigurationForwardMigratesImportedActiveLegacyProviderWithoutLeakingSecrets(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	manager := codexconfig.NewManager(home)
	activeConfig := []byte("model_provider = \"openai\"\nmodel = \"before-restore\"\n")
	if err := os.WriteFile(manager.ConfigPath, activeConfig, 0o600); err != nil {
		t.Fatal(err)
	}

	legacyID := "20260830T030405.123456789Z-cdef1234"
	legacySecret := "restored-legacy-provider-secret-must-not-leak"
	legacyConfig := []byte(`model_provider = "codex_local_access"
model = "gpt-5.6-sol"
review_model = "gpt-5.6-luna"
model_context_window = 372000
model_auto_compact_token_limit = 334800
web_search = "live"

[model_providers.codex_local_access]
name = "XIASS API"
base_url = "https://api.xiass.com/v1"
wire_api = "responses"
requires_openai_auth = false
experimental_bearer_token = "` + legacySecret + `"
http_headers = { "x-openai-actor-authorization" = "api.xiass.com" }
supports_websockets = false
`)
	writeBridgeLegacyConfigBackup(t, manager, legacyID, legacyConfig)

	authPath := filepath.Join(home, "auth.json")
	historyPath := filepath.Join(home, "sessions", "restore-sentinel.jsonl")
	if err := os.WriteFile(authPath, []byte("auth-must-not-be-read-or-modified"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(historyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(historyPath, []byte("history-must-not-be-read-or-modified"), 0o600); err != nil {
		t.Fatal(err)
	}

	imported := (&App{}).ImportCodexLegacyConfigBackup(legacyID)
	if !imported.OK {
		t.Fatalf("ImportCodexLegacyConfigBackup() = %#v", imported)
	}
	var importedID string
	for _, backup := range imported.Backups {
		if backup.Reason == "imported_legacy_config" {
			importedID = backup.ID
			break
		}
	}
	if importedID == "" {
		t.Fatalf("imported legacy backup not listed: %#v", imported.Backups)
	}

	app := &App{ctx: context.Background(), codexDesktopControl: newCodexLifecycleFakeDesktop(false)}
	status := app.RestoreCodexConfiguration(importedID)
	if !status.OK || !status.LegacyProviderMigrationCompleted || !status.LegacyProviderMigrationWasActive || status.Snapshot.ModelProvider != codexconfig.DefaultProviderID {
		t.Fatalf("restore-and-forward-migrate status = %#v", status)
	}
	written := readBridgeTestFile(t, manager.ConfigPath)
	if !bytes.Contains(written, []byte(`model_provider = "xiass_tools"`)) || bytes.Contains(written, []byte("codex_local_access")) || !bytes.Contains(written, []byte(legacySecret)) {
		t.Fatalf("restored legacy provider was not safely forward-migrated: %s", written)
	}
	if got := readBridgeTestFile(t, authPath); string(got) != "auth-must-not-be-read-or-modified" {
		t.Fatalf("restore migration changed auth sentinel: %q", got)
	}
	if got := readBridgeTestFile(t, historyPath); string(got) != "history-must-not-be-read-or-modified" {
		t.Fatalf("restore migration changed history sentinel: %q", got)
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{legacySecret, home, manager.ConfigPath, "auth-must-not-be-read-or-modified", "history-must-not-be-read-or-modified"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("restore migration response leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestRestoreCodexConfigurationLegacyMigrationFailureRestoresSafetyBackup(t *testing.T) {
	fake := &codexRestoreForwardMigrationFake{
		restoreResults: []codexconfig.RestoreResult{
			{RestoredBackupID: "selected-backup", SafetyBackupID: "pre-restore-backup"},
			{RestoredBackupID: "pre-restore-backup", SafetyBackupID: "rollback-safety-backup"},
		},
		legacyMigrationStatus: codexconfig.LegacyProviderMigrationStatus{
			Available: true,
			WasActive: true,
		},
		migrationErr: fmt.Errorf("test-only migration failure"),
	}

	outcome, err := restoreCodexConfigurationWithLegacyForwardMigration(fake, "selected-backup")
	if err != nil {
		t.Fatalf("restore helper error = %v", err)
	}
	if !outcome.LegacyProviderMigrationFailed || !outcome.MigrationRollbackWasSuccessful || outcome.LegacyProviderMigrated {
		t.Fatalf("restore helper outcome = %#v", outcome)
	}
	if fake.migrateCalls != 1 || strings.Join(fake.restoreIDs, ",") != "selected-backup,pre-restore-backup" {
		t.Fatalf("restore/migration sequence = restores=%#v migrations=%d", fake.restoreIDs, fake.migrateCalls)
	}
}

func TestRestoreCodexConfigurationDoesNotAutoMigrateInactiveLegacyProvider(t *testing.T) {
	fake := &codexRestoreForwardMigrationFake{
		restoreResults: []codexconfig.RestoreResult{{RestoredBackupID: "selected-backup", SafetyBackupID: "pre-restore-backup"}},
		legacyMigrationStatus: codexconfig.LegacyProviderMigrationStatus{
			Available: true,
			WasActive: false,
		},
	}

	outcome, err := restoreCodexConfigurationWithLegacyForwardMigration(fake, "selected-backup")
	if err != nil {
		t.Fatalf("restore helper error = %v", err)
	}
	if outcome.LegacyProviderMigrationFailed || outcome.LegacyProviderMigrated || outcome.MigrationRollbackWasSuccessful {
		t.Fatalf("inactive legacy provider outcome = %#v", outcome)
	}
	if fake.migrateCalls != 0 || len(fake.restoreIDs) != 1 || fake.restoreIDs[0] != "selected-backup" {
		t.Fatalf("inactive legacy provider unexpectedly mutated: restores=%#v migrations=%d", fake.restoreIDs, fake.migrateCalls)
	}
}

func TestCodexConfigurationModelCount(t *testing.T) {
	if got := formatCodexModelCount(42); got != "42" {
		t.Fatalf("model count = %q", got)
	}
}

func writeBridgeLegacyConfigBackup(t *testing.T, manager *codexconfig.Manager, id string, data []byte) {
	t.Helper()
	directory := filepath.Join(manager.CodexHome, "xiass-helper", "backups", id)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	manifest := map[string]any{
		"version":          1,
		"id":               id,
		"reason":           "apply",
		"created_at":       "2026-08-30T01:02:03.123456789Z",
		"config_path":      manager.ConfigPath,
		"original_existed": true,
		"original_mode":    0o600,
		"original_sha256":  fmtSHA256(sum),
	}
	writeBridgeJSON(t, filepath.Join(directory, "manifest.json"), manifest)
	if err := os.WriteFile(filepath.Join(directory, "config.toml"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeBridgeLegacyHistoryBackup(t *testing.T, manager *codexconfig.Manager) (string, string, string) {
	t.Helper()
	if err := os.WriteFile(manager.ConfigPath, []byte("model_provider = \"xiass_tools\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sessionPath := filepath.Join(manager.CodexHome, "sessions", "legacy-bridge.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionPath), 0o700); err != nil {
		t.Fatal(err)
	}
	metadata, err := json.Marshal(map[string]any{
		"type": "session_meta",
		"payload": map[string]any{
			"id":             "legacy-bridge-thread",
			"model_provider": "openai",
			"cwd":            filepath.Join(manager.CodexHome, "workspace"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sessionPath, append(metadata, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(manager.CodexHome, "state_5.sqlite")
	database, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`CREATE TABLE threads (
		id TEXT PRIMARY KEY,
		rollout_path TEXT NOT NULL,
		model_provider TEXT NOT NULL,
		has_user_event INTEGER NOT NULL DEFAULT 1,
		cwd TEXT NOT NULL DEFAULT ''
	)`); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if _, err := database.Exec("INSERT INTO threads (id, rollout_path, model_provider) VALUES (?, ?, ?)", "legacy-bridge-thread", "legacy-bridge.jsonl", "openai"); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	repairer := codexconfig.NewHistoryRepairerWithManager(manager)
	result, err := repairer.RepairCurrentProvider()
	if err != nil {
		t.Fatal(err)
	}
	currentDirectory := filepath.Join(repairer.BackupRoot, result.BackupID)
	legacyDirectory := filepath.Join(manager.CodexHome, "xiass-helper", "history-backups", result.BackupID)
	if err := os.MkdirAll(filepath.Join(legacyDirectory, "database"), 0o700); err != nil {
		t.Fatal(err)
	}
	copyBridgeTestFile(t, filepath.Join(currentDirectory, "database", "state_5.sqlite"), filepath.Join(legacyDirectory, "database", "state_5.sqlite"))
	manifestData := readBridgeTestFile(t, filepath.Join(currentDirectory, "manifest.json"))
	var manifest map[string]any
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest["managed_by"] = "XIASS Codex Helper history repair"
	writeBridgeJSON(t, filepath.Join(legacyDirectory, "manifest.json"), manifest)
	if err := os.RemoveAll(currentDirectory); err != nil {
		t.Fatal(err)
	}
	return result.BackupID, sessionPath, databasePath
}

func hasBridgeConfigurationBackup(backups []codexconfig.BackupInfo, reason string) bool {
	for _, backup := range backups {
		if backup.Reason == reason {
			return true
		}
	}
	return false
}

func readBridgeTestFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func copyBridgeTestFile(t *testing.T, source, destination string) {
	t.Helper()
	data := readBridgeTestFile(t, source)
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeBridgeJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func fmtSHA256(sum [sha256.Size]byte) string {
	return strings.ToLower(fmt.Sprintf("%x", sum))
}

type codexRestoreForwardMigrationFake struct {
	restoreResults        []codexconfig.RestoreResult
	restoreErrs           []error
	restoreIDs            []string
	legacyMigrationStatus codexconfig.LegacyProviderMigrationStatus
	legacyMigrationErr    error
	migration             codexconfig.LegacyProviderMigrationResult
	migrationErr          error
	migrateCalls          int
}

func (fake *codexRestoreForwardMigrationFake) Restore(backupID string) (codexconfig.RestoreResult, error) {
	index := len(fake.restoreIDs)
	fake.restoreIDs = append(fake.restoreIDs, backupID)
	if index < len(fake.restoreErrs) && fake.restoreErrs[index] != nil {
		return codexconfig.RestoreResult{}, fake.restoreErrs[index]
	}
	if index >= len(fake.restoreResults) {
		return codexconfig.RestoreResult{}, fmt.Errorf("unexpected Restore(%q)", backupID)
	}
	return fake.restoreResults[index], nil
}

func (fake *codexRestoreForwardMigrationFake) InspectLegacyProviderMigration() (codexconfig.LegacyProviderMigrationStatus, error) {
	return fake.legacyMigrationStatus, fake.legacyMigrationErr
}

func (fake *codexRestoreForwardMigrationFake) MigrateLegacyProvider() (codexconfig.LegacyProviderMigrationResult, error) {
	fake.migrateCalls++
	return fake.migration, fake.migrationErr
}
