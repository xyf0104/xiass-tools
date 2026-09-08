package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"antigravity-wf-assistant/internal/codexconfig"
	"antigravity-wf-assistant/internal/codexdesktop"
	"antigravity-wf-assistant/internal/codexselection"
	"antigravity-wf-assistant/internal/storage"
)

// These tests are an acceptance contract for users moving from the first-party
// XIASS Codex Helper. Every filesystem location, credential, callback, and
// upstream response below is a test fixture in t.TempDir or httptest; no real
// Codex home, account, browser state, or credential is read.

func TestCodexHelperCompatibilityRestoresAndForwardMigratesKnownLegacyProviders(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		providerID string
	}{
		{name: "xiass", providerID: "xiass"},
		{name: "codex local access", providerID: "codex_local_access"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("CODEX_HOME", home)
			manager := codexconfig.NewManager(home)
			legacyKey := "compatibility-legacy-" + testCase.providerID + "-key"
			legacyConfig := []byte(`model_provider = "` + testCase.providerID + `"
model = "gpt-5.6-sol"
review_model = "gpt-5.6-terra"
model_context_window = 512000
model_auto_compact_token_limit = 460800
web_search = "cached"

[model_providers.` + testCase.providerID + `]
name = "XIASS Codex Helper fixture"
base_url = "https://compatibility.example.test/v1"
wire_api = "responses"
requires_openai_auth = false
experimental_bearer_token = "` + legacyKey + `"
http_headers = { "x-openai-actor-authorization" = "compatibility.example.test" }
supports_websockets = false
`)
			if err := os.WriteFile(manager.ConfigPath, legacyConfig, 0o600); err != nil {
				t.Fatalf("write legacy Helper fixture: %v", err)
			}

			// Applying a current provider creates a checksum-protected backup of
			// the Helper-era provider. Restoring it must then migrate only the
			// known first-party ID back to the current xiass_tools entry.
			applied, err := manager.Apply(codexconfig.ApplyConfig{
				BaseURL:                    "https://current.example.test",
				APIKey:                     "compatibility-current-key",
				Model:                      "gpt-current",
				ReviewModel:                "gpt-current-review",
				WebSearch:                  "live",
				ModelContextWindow:         372000,
				ModelAutoCompactTokenLimit: 334800,
			})
			if err != nil {
				t.Fatalf("apply current fixture provider: %v", err)
			}

			app := &App{
				ctx:                 context.Background(),
				codexDesktopControl: newCodexHelperCompatibilityDesktop(false),
			}
			status := app.RestoreCodexConfiguration(applied.BackupID)
			if !status.OK || !status.LegacyProviderMigrationCompleted || !status.LegacyProviderMigrationWasActive {
				t.Fatalf("restore/migration status = %#v", status)
			}
			if got, want := status.Snapshot.ModelProvider, codexconfig.DefaultProviderID; got != want {
				t.Fatalf("restored provider = %q, want %q", got, want)
			}
			if got, want := status.Snapshot.Model, "gpt-5.6-sol"; got != want {
				t.Fatalf("restored model = %q, want %q", got, want)
			}
			if got, want := status.Snapshot.ReviewModel, "gpt-5.6-terra"; got != want {
				t.Fatalf("restored review model = %q, want %q", got, want)
			}
			if got, want := status.Snapshot.WebSearch, "cached"; got != want {
				t.Fatalf("restored web search = %q, want %q", got, want)
			}
			if got, want := status.Snapshot.Context.ModelContextWindow, int64(512000); got != want {
				t.Fatalf("restored context window = %d, want %d", got, want)
			}
			if got, want := status.Snapshot.Context.ModelAutoCompactTokenLimit, int64(460800); got != want {
				t.Fatalf("restored compact limit = %d, want %d", got, want)
			}
			if !status.Snapshot.ManagedProviderVerified {
				t.Fatalf("migrated provider did not pass current structural verification: %#v", status.Snapshot)
			}

			written, err := os.ReadFile(manager.ConfigPath)
			if err != nil {
				t.Fatalf("read migrated config: %v", err)
			}
			if strings.Contains(string(written), `model_provider = "`+testCase.providerID+`"`) || strings.Contains(string(written), "[model_providers."+testCase.providerID+"]") {
				t.Fatalf("legacy provider identifier remained after forward migration:\n%s", written)
			}
			if !strings.Contains(string(written), `[model_providers.xiass_tools]`) {
				t.Fatalf("current provider table missing after forward migration:\n%s", written)
			}
			encoded, err := json.Marshal(status)
			if err != nil {
				t.Fatalf("marshal redacted status: %v", err)
			}
			for _, forbidden := range []string{legacyKey, home, manager.ConfigPath} {
				if strings.Contains(string(encoded), forbidden) {
					t.Fatalf("restore status leaked %q: %s", forbidden, encoded)
				}
			}
		})
	}
}

func TestCodexHelperCompatibilityNormalizesModelDiscoveryAndPersistsProfiles(t *testing.T) {
	const discoveryKey = "compatibility-model-discovery-key"
	var observedPath, observedAuthorization string
	catalog := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		observedPath = request.URL.EscapedPath()
		observedAuthorization = request.Header.Get("Authorization")
		if request.Method != http.MethodGet || request.URL.Path != "/v1/models" {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"data":[{"id":"gpt-5.6-terra"},{"id":"gpt-5.6-sol"},{"id":"gpt-5.6-sol"}]}`))
	}))
	defer catalog.Close()

	models, err := codexconfig.DiscoverModels(
		context.Background(),
		catalog.URL+"/v1/chat/completions?ignored=1#fragment",
		discoveryKey,
		codexconfig.ModelDiscoveryOptions{},
	)
	if err != nil {
		t.Fatalf("discover normalized Helper catalog: %v", err)
	}
	if got, want := strings.Join(models, ","), "gpt-5.6-sol,gpt-5.6-terra"; got != want {
		t.Fatalf("discovered models = %q, want %q", got, want)
	}
	if got, want := observedPath, "/v1/models"; got != want {
		t.Fatalf("model discovery path = %q, want %q", got, want)
	}
	if got, want := observedAuthorization, "Bearer "+discoveryKey; got != want {
		t.Fatalf("model discovery authorization = %q, want test credential", got)
	}

	home := t.TempDir()
	manager := codexconfig.NewManager(home)
	for _, profile := range []struct {
		name       string
		window     int64
		compact    int64
		webSearch  string
		expectedWS string
	}{
		{name: "235K live", window: 235000, compact: 211500, webSearch: "live", expectedWS: "live"},
		{name: "372K cached", window: 372000, compact: 334800, webSearch: "cached", expectedWS: "cached"},
		{name: "512K disabled", window: 512000, compact: 460800, webSearch: "disabled", expectedWS: "disabled"},
		{name: "1M legacy off", window: 1000000, compact: 900000, webSearch: "off", expectedWS: "disabled"},
		{name: "custom", window: 777000, compact: 699300, webSearch: "live", expectedWS: "live"},
	} {
		t.Run(profile.name, func(t *testing.T) {
			_, err := manager.Apply(codexconfig.ApplyConfig{
				BaseURL:                    catalog.URL + "/v1/responses",
				APIKey:                     "compatibility-profile-key",
				ProviderName:               "Helper compatibility fixture",
				Model:                      "gpt-5.6-sol",
				ReviewModel:                "gpt-5.6-terra",
				WebSearch:                  profile.webSearch,
				ModelContextWindow:         profile.window,
				ModelAutoCompactTokenLimit: profile.compact,
			})
			if err != nil {
				t.Fatalf("apply %s profile: %v", profile.name, err)
			}
			snapshot, err := manager.Inspect()
			if err != nil {
				t.Fatalf("inspect %s profile: %v", profile.name, err)
			}
			if !snapshot.ManagedProviderVerified {
				t.Fatalf("%s profile did not pass managed provider verification: %#v", profile.name, snapshot)
			}
			if got, want := snapshot.Context.ModelContextWindow, profile.window; got != want {
				t.Fatalf("%s context window = %d, want %d", profile.name, got, want)
			}
			if got, want := snapshot.Context.ModelAutoCompactTokenLimit, profile.compact; got != want {
				t.Fatalf("%s compact limit = %d, want %d", profile.name, got, want)
			}
			if got, want := snapshot.WebSearch, profile.expectedWS; got != want {
				t.Fatalf("%s web search = %q, want %q", profile.name, got, want)
			}
			if snapshot.Model != "gpt-5.6-sol" || snapshot.ReviewModel != "gpt-5.6-terra" {
				t.Fatalf("%s model choices were not preserved: %#v", profile.name, snapshot)
			}
		})
	}
}

func TestCodexHelperCompatibilityManualCallbackAndOAuthCodeRemainSeparate(t *testing.T) {
	service := codexselection.New()
	t.Cleanup(service.Close)
	selectionApp := &App{codexKeySelection: service}
	started, err := service.Begin("https://gateway.example.test")
	if err != nil {
		t.Fatalf("begin test Key selection: %v", err)
	}
	callback := codexHelperCompatibilitySelectionCallback(t, started.ConnectURL, "https://gateway.example.test/v1", "compatibility-selection-key", "Compatibility Key")
	selection := selectionApp.CompleteCodexXIASSKeySelectionManual(started.State.SessionID, callback)
	if !selection.OK || selection.Selection.Status != "ready" || selection.Selection.BaseURL != "https://gateway.example.test/v1" || selection.Selection.KeyName != "Compatibility Key" {
		t.Fatalf("manual Helper callback state = %#v", selection)
	}
	_, err = service.WithCredential(started.State.SessionID, false, func(credential codexselection.Credential) error {
		if credential.BaseURL != "https://gateway.example.test/v1" || credential.KeyName != "Compatibility Key" || string(credential.APIKey) != "compatibility-selection-key" {
			t.Fatalf("native manual-callback credential = %#v", credential)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("use manual-callback credential: %v", err)
	}
	encodedSelection, err := json.Marshal(selection)
	if err != nil {
		t.Fatalf("marshal selection status: %v", err)
	}
	for _, forbidden := range []string{"compatibility-selection-key", callback} {
		if strings.Contains(string(encodedSelection), forbidden) {
			t.Fatalf("manual callback status leaked %q: %s", forbidden, encodedSelection)
		}
	}

	// The Helper handoff expects an integrity-bound payload fragment. A generic
	// OAuth code must not accidentally be accepted as an API-key selection.
	codeOnly, err := service.Begin("https://gateway.example.test")
	if err != nil {
		t.Fatalf("begin code-separation selection: %v", err)
	}
	codeCallback := codexHelperCompatibilityCodeOnlyCallback(t, codeOnly.ConnectURL, "compatibility-oauth-code")
	codeSelection := selectionApp.CompleteCodexXIASSKeySelectionManual(codeOnly.State.SessionID, codeCallback)
	if codeSelection.OK || codeSelection.Selection.Status != "pending" {
		t.Fatalf("raw OAuth code was incorrectly accepted as a XIASS Key-selection callback: %#v", codeSelection)
	}

	// A raw authorization code has its own account OAuth flow. Keep it in an
	// isolated temporary account store and a local fake token endpoint to prove
	// the two manual completion contracts remain explicit rather than conflated.
	storage.Init(t.TempDir())
	var tokenRequest url.Values
	tokenServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/token" {
			http.NotFound(writer, request)
			return
		}
		body, readErr := io.ReadAll(request.Body)
		if readErr != nil {
			t.Fatalf("read fake token request: %v", readErr)
		}
		var parseErr error
		tokenRequest, parseErr = url.ParseQuery(string(body))
		if parseErr != nil {
			t.Fatalf("parse fake token request: %v", parseErr)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"access_token":"compatibility-access-token","expires_in":3600}`))
	}))
	defer tokenServer.Close()

	oauthApp := &App{oauthSessions: make(map[string]*pendingOAuthSession)}
	oauthStarted := oauthApp.StartOAuthAuthorization(storage.UpstreamAccount{
		Name:           "Compatibility OAuth fixture",
		Provider:       "openai",
		Type:           "oauth",
		APIURL:         tokenServer.URL,
		AuthMode:       "bearer",
		Enabled:        true,
		MaxConcurrency: 1,
		OAuth: storage.OAuthConfiguration{
			AuthorizationURL: tokenServer.URL + "/authorize",
			TokenURL:         tokenServer.URL + "/token",
			ClientID:         "compatibility-public-client",
			RedirectURI:      tokenServer.URL + "/callback",
			Scopes:           "openid profile",
		},
	})
	if !oauthStarted.OK || oauthStarted.SessionID == "" {
		t.Fatalf("begin isolated OAuth code flow: %#v", oauthStarted)
	}
	oauthCompleted := oauthApp.CompleteOAuthAuthorization(oauthStarted.SessionID, "compatibility-oauth-code")
	if !oauthCompleted.OK || oauthCompleted.AccountID == "" {
		t.Fatalf("complete isolated OAuth code flow: %#v", oauthCompleted)
	}
	if got, want := tokenRequest.Get("grant_type"), "authorization_code"; got != want {
		t.Fatalf("OAuth code grant type = %q, want %q", got, want)
	}
	if got, want := tokenRequest.Get("code"), "compatibility-oauth-code"; got != want {
		t.Fatalf("OAuth token endpoint code = %q, want %q", got, want)
	}
	encodedOAuth, err := json.Marshal(oauthCompleted)
	if err != nil {
		t.Fatalf("marshal OAuth completion: %v", err)
	}
	for _, forbidden := range []string{"compatibility-oauth-code", "compatibility-access-token", tokenServer.URL} {
		if strings.Contains(string(encodedOAuth), forbidden) {
			t.Fatalf("OAuth completion leaked %q: %s", forbidden, encodedOAuth)
		}
	}
}

type codexHelperCompatibilityDesktop struct {
	status codexdesktop.ControlStatus
}

func newCodexHelperCompatibilityDesktop(running bool) *codexHelperCompatibilityDesktop {
	return &codexHelperCompatibilityDesktop{status: codexdesktop.ControlStatus{
		Discovered: true,
		Running:    running,
		CanSelect:  true,
		CanLaunch:  !running,
		CanStop:    running,
		CanRestart: running,
	}}
}

func (desktop *codexHelperCompatibilityDesktop) Status(context.Context) codexdesktop.ControlStatus {
	return desktop.status
}

func (desktop *codexHelperCompatibilityDesktop) SelectPath(context.Context, string) (codexdesktop.ControlStatus, error) {
	return desktop.status, nil
}

func (desktop *codexHelperCompatibilityDesktop) Launch(context.Context) (codexdesktop.ControlStatus, error) {
	desktop.status.Running = true
	desktop.status.CanLaunch = false
	desktop.status.CanStop = true
	desktop.status.CanRestart = true
	return desktop.status, nil
}

func (desktop *codexHelperCompatibilityDesktop) Stop(context.Context, string) (codexdesktop.ControlStatus, error) {
	desktop.status.Running = false
	desktop.status.CanLaunch = true
	desktop.status.CanStop = false
	desktop.status.CanRestart = false
	return desktop.status, nil
}

func (desktop *codexHelperCompatibilityDesktop) Restart(context.Context, string) (codexdesktop.ControlStatus, error) {
	return desktop.status, nil
}

func codexHelperCompatibilitySelectionCallback(t *testing.T, connectURL, baseURL, apiKey, keyName string) string {
	t.Helper()
	connect, err := url.Parse(connectURL)
	if err != nil {
		t.Fatalf("parse test connect URL: %v", err)
	}
	callback, err := url.Parse(connect.Query().Get("callback"))
	if err != nil {
		t.Fatalf("parse test callback URL: %v", err)
	}
	state := connect.Query().Get("state")
	if state == "" || callback.Host == "" {
		t.Fatalf("incomplete test handoff URL: %s", connectURL)
	}
	payload, err := json.Marshal(map[string]string{
		"base_url": baseURL,
		"api_key":  apiKey,
		"key_name": keyName,
	})
	if err != nil {
		t.Fatalf("encode test selection payload: %v", err)
	}
	callback.RawQuery = ""
	callback.Fragment = url.Values{
		"state":   []string{state},
		"payload": []string{base64.RawURLEncoding.EncodeToString(payload)},
	}.Encode()
	return callback.String()
}

func codexHelperCompatibilityCodeOnlyCallback(t *testing.T, connectURL, code string) string {
	t.Helper()
	connect, err := url.Parse(connectURL)
	if err != nil {
		t.Fatalf("parse test connect URL: %v", err)
	}
	callback, err := url.Parse(connect.Query().Get("callback"))
	if err != nil {
		t.Fatalf("parse test callback URL: %v", err)
	}
	callback.RawQuery = url.Values{
		"code":  []string{code},
		"state": []string{connect.Query().Get("state")},
	}.Encode()
	callback.Fragment = ""
	return callback.String()
}
