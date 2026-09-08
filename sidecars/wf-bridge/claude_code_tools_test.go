package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"antigravity-wf-assistant/internal/claudeconfig"
	"antigravity-wf-assistant/internal/storage"
)

type embeddedClaudeCodeActionStub struct {
	requests []nativeActionRequest
	result   nativeActionResult
	err      error
}

func (stub *embeddedClaudeCodeActionStub) Execute(_ context.Context, request nativeActionRequest) (nativeActionResult, error) {
	stub.requests = append(stub.requests, request)
	return stub.result, stub.err
}

func (stub *embeddedClaudeCodeActionStub) Close() {}

func TestClaudeCodeBridgeWritesOnlySettingsAndNeverSerializesToken(t *testing.T) {
	home := t.TempDir()
	configDir := filepath.Join(home, "selected-claude")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
	t.Setenv("CLAUDE_CONFIG_DIR", configDir)

	credentialsPath := filepath.Join(configDir, ".credentials.json")
	legacyPath := filepath.Join(home, ".claude.json")
	credentials := []byte(`{"credential":"must-stay-private"}`)
	legacy := []byte(`{"legacy":"must-stay-private"}`)
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(credentialsPath, credentials, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, legacy, 0o600); err != nil {
		t.Fatal(err)
	}

	secret := "sk-claude-bridge-secret-must-not-escape"
	app := &App{}
	status := app.ApplyClaudeCodeConfiguration(ClaudeCodeApplyInput{
		BaseURL:   "https://api.example.test/v1",
		AuthToken: secret,
		Model:     "claude-sonnet-4-5",
	})
	if !status.OK || !status.Snapshot.Valid || !status.Snapshot.Managed || !status.Snapshot.AuthTokenConfigured {
		t.Fatalf("unexpected apply status: %#v", status)
	}
	if len(status.Backups) != 1 || status.Backups[0].ID == "" {
		t.Fatalf("apply did not expose a verified redacted backup: %#v", status.Backups)
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) || strings.Contains(string(encoded), "must-stay-private") {
		t.Fatalf("bridge result leaked secret material: %s", encoded)
	}
	if strings.Contains(string(encoded), configDir) || strings.Contains(string(encoded), "settingsPath") || strings.Contains(string(encoded), "configDir") {
		t.Fatalf("bridge result leaked Claude settings path metadata: %s", encoded)
	}

	manager, err := claudeconfig.NewDefaultManager()
	if err != nil {
		t.Fatal(err)
	}
	settings, err := os.ReadFile(manager.SettingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(settings), secret) {
		t.Fatal("managed Claude settings did not receive the request-local token")
	}
	if got, err := os.ReadFile(credentialsPath); err != nil || string(got) != string(credentials) {
		t.Fatalf("credentials file was changed: %q, %v", got, err)
	}
	if got, err := os.ReadFile(legacyPath); err != nil || string(got) != string(legacy) {
		t.Fatalf("legacy Claude file was changed: %q, %v", got, err)
	}

	invalid := app.ApplyClaudeCodeConfiguration(ClaudeCodeApplyInput{
		BaseURL:   "https://api.example.test/v1",
		AuthToken: "Bearer " + secret,
		Model:     "claude-sonnet-4-5",
	})
	if invalid.OK {
		t.Fatalf("invalid token was accepted: %#v", invalid)
	}
	encodedInvalid, err := json.Marshal(invalid)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encodedInvalid), secret) {
		t.Fatalf("bridge error leaked request token: %s", encodedInvalid)
	}
}

func TestClaudeCodeBridgeUsesEmptySerializedBackupArrays(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "empty-claude"))

	status := (&App{}).GetClaudeCodeConfiguration()
	if !status.OK || status.Backups == nil || status.LegacyBackups == nil {
		t.Fatalf("empty bridge state = %#v", status)
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"backups":[]`) || !strings.Contains(string(encoded), `"legacyBackups":[]`) {
		t.Fatalf("empty backup arrays were not serialized: %s", encoded)
	}
}

func TestClaudeGatewayBridgeUsesOnlyRequestLocalCredential(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude"))

	storedSecret := "stored-claude-secret-must-not-be-used"
	requestSecret := "request-claude-secret-must-not-escape"
	app := &App{}
	if status := app.ApplyClaudeCodeConfiguration(ClaudeCodeApplyInput{
		BaseURL:        "https://stored.example.test",
		CredentialMode: "auth_token",
		Credential:     storedSecret,
		Model:          "claude-stored",
	}); !status.OK {
		t.Fatalf("could not create test config: %#v", status)
	}

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer "+requestSecret {
			t.Errorf("gateway used an unexpected authorization header: %q", request.Header.Get("Authorization"))
		}
		if request.URL.Path != "/v1/models" {
			t.Errorf("gateway path = %q", request.URL.Path)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"data":[{"id":"claude-bridge-test","display_name":"Bridge Test"}]}`))
	}))
	defer server.Close()

	status := app.DiscoverClaudeCodeGatewayModels(ClaudeCodeGatewayRequestInput{
		BaseURL:        server.URL,
		CredentialMode: "auth_token",
		Credential:     requestSecret,
	})
	if !status.OK || status.HTTPStatus != http.StatusOK || len(status.Models) != 1 || status.Models[0].ID != "claude-bridge-test" || status.Models[0].DisplayName != "Bridge Test" {
		t.Fatalf("unexpected discovery status: %#v", status)
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	for _, sensitive := range []string{storedSecret, requestSecret, server.URL} {
		if strings.Contains(string(encoded), sensitive) {
			t.Fatalf("gateway bridge result leaked request-local data %q: %s", sensitive, encoded)
		}
	}

	helperStatus := app.DiscoverClaudeCodeGatewayModels(ClaudeCodeGatewayRequestInput{
		BaseURL:        server.URL,
		CredentialMode: "api_key_helper",
		Credential:     requestSecret,
	})
	if helperStatus.OK || strings.Contains(helperStatus.Message, requestSecret) || strings.Contains(helperStatus.Message, server.URL) {
		t.Fatalf("helper discovery did not stay safely unavailable: %#v", helperStatus)
	}
}

func TestClaudeCodeRendererSnapshotProjectsDiscoveryCompatibilityWithoutSettingsData(t *testing.T) {
	result := claudeCodeRendererSnapshot(claudeconfig.Snapshot{
		GatewayModelDiscoveryEnabled: true,
		GatewayModelDiscoveryBlocked: true,
	})
	if !result.GatewayModelDiscoveryEnabled || !result.GatewayModelDiscoveryBlocked {
		t.Fatalf("renderer snapshot = %#v", result)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"gatewayModelDiscoveryBlocked":true`) || strings.Contains(string(encoded), "CLAUDE_CODE_USE_") || strings.Contains(string(encoded), "NONESSENTIAL") {
		t.Fatalf("renderer snapshot exposed provider configuration: %s", encoded)
	}
}

func TestClaudeCodeReusesCompatibleSavedAccountWithoutRenderingCredential(t *testing.T) {
	home := t.TempDir()
	configDir := filepath.Join(home, "claude")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
	t.Setenv("CLAUDE_CONFIG_DIR", configDir)
	storage.Init(filepath.Join(home, "xiass-state"))

	foreignCredentials := filepath.Join(configDir, ".credentials.json")
	foreignData := []byte(`{"credential":"must-not-be-read-or-changed"}`)
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(foreignCredentials, foreignData, 0o600); err != nil {
		t.Fatal(err)
	}

	gateway := httptest.NewServer(http.NotFoundHandler())
	defer gateway.Close()
	const secret = "saved-claude-account-secret-must-not-render"
	account := storage.UpstreamAccount{
		ID: "saved-claude-account", Name: "团队 Claude 账户", Provider: "anthropic", Type: "api_key",
		APIURL: gateway.URL, EndpointMode: "auto", APIStyle: "messages", MessagePathMode: "auto",
		AuthMode: "x_api_key", APIKey: secret, Enabled: true, MaxConcurrency: 1,
	}
	if err := storage.SaveUpstreamAccount(account); err != nil {
		t.Fatal(err)
	}
	if err := storage.AddOrUpdateModel(storage.CustomModel{
		Name: "saved-claude-model", DisplayName: "Claude Team", Provider: "anthropic", APIStyle: "messages",
		ExternalModelName: "claude-test-model", AccountIDs: []string{account.ID},
	}); err != nil {
		t.Fatal(err)
	}

	app := &App{}
	candidates := app.GetClaudeCodeAccountCandidates()
	if !candidates.OK || len(candidates.Candidates) != 1 {
		t.Fatalf("saved-account candidates = %#v", candidates)
	}
	candidate := candidates.Candidates[0]
	if candidate.ID != account.ID || candidate.Label != account.Name || candidate.CredentialMode != "API Key" {
		t.Fatalf("candidate metadata = %#v", candidate)
	}
	if len(candidate.Models) != 1 || candidate.Models[0].ID != "claude-test-model" || candidate.Models[0].DisplayName != "Claude Team" {
		t.Fatalf("candidate models = %#v", candidate.Models)
	}
	encodedCandidates, err := json.Marshal(candidates)
	if err != nil {
		t.Fatal(err)
	}
	for _, sensitive := range []string{secret, gateway.URL, "must-not-be-read-or-changed"} {
		if strings.Contains(string(encodedCandidates), sensitive) {
			t.Fatalf("candidate response leaked %q: %s", sensitive, encodedCandidates)
		}
	}

	status := app.ApplyClaudeCodeConfigurationFromAccount(ClaudeCodeApplyAccountInput{
		AccountID: account.ID, Model: "claude-test-model", EnableGatewayModelDiscovery: true,
	})
	if !status.OK || !status.Snapshot.Managed || status.Snapshot.CredentialMode != "api_key" || !status.Snapshot.CredentialConfigured {
		t.Fatalf("account apply status = %#v", status)
	}
	encodedStatus, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encodedStatus), secret) || strings.Contains(string(encodedStatus), "must-not-be-read-or-changed") {
		t.Fatalf("account apply result leaked a credential: %s", encodedStatus)
	}

	manager, err := claudeconfig.NewDefaultManager()
	if err != nil {
		t.Fatal(err)
	}
	settings, err := os.ReadFile(manager.SettingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(settings), `"ANTHROPIC_API_KEY": "`+secret+`"`) || !strings.Contains(string(settings), `"model": "claude-test-model"`) {
		t.Fatalf("settings did not receive the selected account mapping: %s", settings)
	}
	if got, err := os.ReadFile(foreignCredentials); err != nil || string(got) != string(foreignData) {
		t.Fatalf("foreign Claude credential file changed: %q, %v", got, err)
	}
}

func TestEmbeddedClaudeCodeCandidatesUseOnlyHostProjection(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
	storage.Init(filepath.Join(home, "legacy-helper-state"))
	if err := storage.SaveUpstreamAccount(storage.UpstreamAccount{
		ID: "legacy-helper-account", Name: "legacy helper must not be used", Provider: "anthropic", Type: "api_key",
		APIURL: "https://legacy.example.test", EndpointMode: "auto", APIStyle: "messages", MessagePathMode: "auto",
		AuthMode: "x_api_key", APIKey: "legacy-helper-secret", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	hostProjection := ClaudeCodeAccountCandidatesStatus{
		OK: true,
		Candidates: []ClaudeCodeAccountCandidate{
			{
				ID: "tauri_claude_account", Label: "XIASS Claude", CredentialMode: "OAuth",
				Models: []ClaudeCodeAccountCandidateModel{
					{ID: "claude-host-model", DisplayName: "Claude Host Model"},
					{ID: "not valid whitespace", DisplayName: "must be removed"},
				},
			},
		},
	}
	payload, err := json.Marshal(hostProjection)
	if err != nil {
		t.Fatal(err)
	}
	stub := &embeddedClaudeCodeActionStub{result: nativeActionResult{OK: true, Value: string(payload)}}
	app := &App{embeddedMode: true, ctx: context.Background(), nativeActions: stub}

	status := app.GetClaudeCodeAccountCandidates()
	if !status.OK || len(status.Candidates) != 1 {
		t.Fatalf("embedded candidates = %#v", status)
	}
	if got := status.Candidates[0]; got.ID != "tauri_claude_account" || got.Label != "XIASS Claude" || len(got.Models) != 1 || got.Models[0].ID != "claude-host-model" {
		t.Fatalf("embedded host projection = %#v", got)
	}
	if len(stub.requests) != 1 || stub.requests[0].Kind != nativeActionClaudeCodeCandidates || stub.requests[0].AccountID != "" || stub.requests[0].Model != "" {
		t.Fatalf("unexpected embedded host request: %#v", stub.requests)
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"legacy-helper-account", "legacy-helper-secret", "legacy.example.test"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("embedded candidate response leaked legacy helper state %q: %s", forbidden, encoded)
		}
	}
}

func TestEmbeddedClaudeCodeAccountApplyPreservesHostCredentialAndWritesOnlyModel(t *testing.T) {
	home := t.TempDir()
	configDir := filepath.Join(home, "claude")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
	t.Setenv("CLAUDE_CONFIG_DIR", configDir)
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	const hostCredential = "host-owned-credential-must-not-render"
	original := []byte(`{"model":"old-model","env":{"ANTHROPIC_AUTH_TOKEN":"` + hostCredential + `","KEEP_ME":"preserved"},"other":{"value":true}}`)
	if err := os.WriteFile(filepath.Join(configDir, "settings.json"), original, 0o600); err != nil {
		t.Fatal(err)
	}
	stub := &embeddedClaudeCodeActionStub{result: nativeActionResult{OK: true, Value: embeddedClaudeCodeCurrentMarkerWarning}}
	app := &App{embeddedMode: true, ctx: context.Background(), nativeActions: stub}

	status := app.ApplyClaudeCodeConfigurationFromAccount(ClaudeCodeApplyAccountInput{
		AccountID: "tauri_claude_account", Model: "claude-host-model",
	})
	if !status.OK || status.Snapshot.Model != "claude-host-model" || !status.Snapshot.AuthTokenConfigured {
		t.Fatalf("embedded account apply = %#v", status)
	}
	if status.Message != embeddedClaudeCodeCurrentMarkerWarning {
		t.Fatalf("committed host warning was not preserved: %#v", status)
	}
	if len(stub.requests) != 1 {
		t.Fatalf("embedded host requests = %#v", stub.requests)
	}
	request := stub.requests[0]
	if request.Kind != nativeActionClaudeCodeApply || request.AccountID != "tauri_claude_account" || request.Model != "claude-host-model" || request.URL != "" {
		t.Fatalf("embedded account apply request = %#v", request)
	}
	written, err := os.ReadFile(filepath.Join(configDir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(written), `"ANTHROPIC_AUTH_TOKEN": "`+hostCredential+`"`) || !strings.Contains(string(written), `"KEEP_ME": "preserved"`) || !strings.Contains(string(written), `"model": "claude-host-model"`) {
		t.Fatalf("embedded account model selection altered host-owned settings: %s", written)
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), hostCredential) || strings.Contains(string(encoded), configDir) {
		t.Fatalf("embedded account status leaked host material: %s", encoded)
	}
}

func TestEmbeddedClaudeCodeAccountApplyRollsBackModelWhenHostRejects(t *testing.T) {
	home := t.TempDir()
	configDir := filepath.Join(home, "claude")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
	t.Setenv("CLAUDE_CONFIG_DIR", configDir)
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"model":"old-model","env":{"KEEP_ME":"preserved"}}`)
	if err := os.WriteFile(filepath.Join(configDir, "settings.json"), original, 0o600); err != nil {
		t.Fatal(err)
	}
	stub := &embeddedClaudeCodeActionStub{result: nativeActionResult{OK: false}}
	app := &App{embeddedMode: true, ctx: context.Background(), nativeActions: stub}

	status := app.ApplyClaudeCodeConfigurationFromAccount(ClaudeCodeApplyAccountInput{
		AccountID: "tauri_claude_account", Model: "claude-host-model",
	})
	if status.OK || !strings.Contains(status.Message, "已恢复") {
		t.Fatalf("host rejection should restore staged model: %#v", status)
	}
	after, err := os.ReadFile(filepath.Join(configDir, "settings.json"))
	if err != nil || string(after) != string(original) {
		t.Fatalf("host rejection did not restore the original model settings: %q / %v", after, err)
	}
	if len(stub.requests) != 1 || stub.requests[0].Kind != nativeActionClaudeCodeApply {
		t.Fatalf("unexpected host action sequence: %#v", stub.requests)
	}
}

func TestEmbeddedClaudeCodeApplyHostMessageWhitelist(t *testing.T) {
	if got := embeddedClaudeCodeApplyHostMessage(embeddedClaudeCodeAppliedMessage); got != embeddedClaudeCodeFallbackApplyMessage {
		t.Fatalf("normal host completion message = %q", got)
	}
	if got := embeddedClaudeCodeApplyHostMessage(embeddedClaudeCodeCurrentMarkerWarning); got != embeddedClaudeCodeCurrentMarkerWarning {
		t.Fatalf("marker warning was not preserved: %q", got)
	}
	if got := embeddedClaudeCodeApplyHostMessage("credential=must-not-render"); got != embeddedClaudeCodeFallbackApplyMessage {
		t.Fatalf("untrusted host message crossed the boundary: %q", got)
	}
}

func TestClaudeCodeAccountReuseRefusesLossyOrPausedMappings(t *testing.T) {
	base := storage.UpstreamAccount{
		ID: "candidate", Name: "candidate", Provider: "anthropic", Type: "api_key", APIURL: "https://gateway.example.test",
		EndpointMode: "auto", APIStyle: "messages", MessagePathMode: "auto", AuthMode: "x_api_key", APIKey: "test-key", Enabled: true,
	}
	for _, testCase := range []struct {
		name   string
		mutate func(*storage.UpstreamAccount)
	}{
		{name: "paused", mutate: func(account *storage.UpstreamAccount) { account.Enabled = false }},
		{name: "manual endpoint", mutate: func(account *storage.UpstreamAccount) { account.EndpointMode = "manual" }},
		{name: "compatibility path", mutate: func(account *storage.UpstreamAccount) { account.MessagePathMode = "compat" }},
		{name: "custom header", mutate: func(account *storage.UpstreamAccount) {
			account.AuthMode = "custom_header"
			account.Headers = map[string]string{"X-Token": "value"}
		}},
		{name: "openai chat", mutate: func(account *storage.UpstreamAccount) {
			account.Provider = "openai"
			account.APIStyle = "chat_completions"
			account.AuthMode = "bearer"
		}},
		{name: "anthropic chat", mutate: func(account *storage.UpstreamAccount) {
			account.APIStyle = "chat_completions"
		}},
		{name: "openai messages", mutate: func(account *storage.UpstreamAccount) {
			account.Provider = "openai"
		}},
		{name: "oauth token", mutate: func(account *storage.UpstreamAccount) {
			account.Type = "oauth"
			account.AuthMode = "bearer"
		}},
		{name: "refresh token", mutate: func(account *storage.UpstreamAccount) {
			account.Type = "refresh_token"
			account.AuthMode = "bearer"
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			account := base
			testCase.mutate(&account)
			if claudeCodeAccountReusable(account) {
				t.Fatalf("lossy account mapping was accepted: %#v", account)
			}
		})
	}
}

func TestClaudeCodeCompatibleModelRequiresAnthropicMessagesPair(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		model storage.CustomModel
		want  bool
	}{
		{name: "anthropic messages", model: storage.CustomModel{Provider: "anthropic", APIStyle: "messages"}, want: true},
		{name: "anthropic chat", model: storage.CustomModel{Provider: "anthropic", APIStyle: "chat_completions"}, want: false},
		{name: "openai messages", model: storage.CustomModel{Provider: "openai", APIStyle: "messages"}, want: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := claudeCodeCompatibleModel(testCase.model); got != testCase.want {
				t.Fatalf("claudeCodeCompatibleModel(%#v) = %t, want %t", testCase.model, got, testCase.want)
			}
		})
	}
}
