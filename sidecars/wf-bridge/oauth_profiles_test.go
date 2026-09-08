package main

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"antigravity-wf-assistant/internal/oauthflow"
	"antigravity-wf-assistant/internal/storage"
	"antigravity-wf-assistant/internal/upstream"
)

func TestOAuthProfilesArePublicAndReviewedPresetReplacesHiddenOverrides(t *testing.T) {
	app := &App{}
	profiles := app.GetOAuthProviderProfiles()
	if len(profiles) == 0 {
		t.Fatal("expected built-in OAuth profiles")
	}

	encoded, err := json.Marshal(profiles)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"access_token", "refresh_token", "client_secret", "\"apiKey\"", "\"credentials\""} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("OAuth profile payload must not expose %q", forbidden)
		}
	}

	// The profile list must be cloned: modifying data returned to one renderer
	// must not mutate the presets observed by another renderer.
	profiles[0].AuthorizationParameters["mutated"] = "true"
	for _, profile := range app.GetOAuthProviderProfiles() {
		if profile.AuthorizationParameters["mutated"] != "" {
			t.Fatal("OAuth profile presets leaked a mutable authorization-parameter map")
		}
	}

	draft := storage.UpstreamAccount{
		APIURL: "https://gateway.example.test/v1", EndpointMode: "manual", APIStyle: "chat_completions",
		MessagePathMode: "compat", AuthMode: "custom_header", AuthHeader: "X-Old-Token",
		Headers: map[string]string{"X-Old-Route": "must-be-cleared"}, QuotaURL: "https://gateway.example.test/usage",
		APIKey: "must-not-return-to-renderer",
		Credentials: map[string]any{
			"refresh_token": "must-not-return-to-renderer",
		},
		OAuth: storage.OAuthConfiguration{
			AuthorizationURL: "https://custom.example.test/authorize",
			TokenURL:         "https://custom.example.test/token",
			ClientID:         "my-public-client-id",
			RedirectURI:      "http://127.0.0.1:39001/callback",
			Scopes:           "custom scope",
		},
	}
	applied := app.ApplyOAuthProviderProfile("openai-codex", draft)
	if !applied.OK {
		t.Fatalf("apply profile result = %+v", applied)
	}
	if applied.Account.APIKey != "" || applied.Account.Credentials != nil {
		t.Fatal("applied OAuth draft leaked existing account credentials")
	}
	profile, found := oauthProviderProfile("openai-codex")
	if !found {
		t.Fatal("OpenAI/Codex OAuth profile missing")
	}
	if got, want := applied.Account.APIURL, profile.APIURL; got != want {
		t.Fatalf("reviewed API route = %q, want %q", got, want)
	}
	if applied.Account.EndpointMode != profile.EndpointMode || applied.Account.APIStyle != profile.APIStyle || applied.Account.MessagePathMode != "auto" || applied.Account.AuthMode != profile.AuthMode {
		t.Fatalf("reviewed request contract was not applied: %#v", applied.Account)
	}
	if applied.Account.AuthHeader != "" || applied.Account.QuotaURL != "" || applied.Account.Headers == nil || len(applied.Account.Headers) != 0 {
		t.Fatalf("hidden route/header state was not explicitly cleared: %#v", applied.Account)
	}
	if got, want := applied.Account.OAuth.AuthorizationURL, profile.OAuth.AuthorizationURL; got != want {
		t.Fatalf("authorization URL = %q, want reviewed %q", got, want)
	}
	if got, want := applied.Account.OAuth.TokenURL, profile.OAuth.TokenURL; got != want {
		t.Fatalf("token URL = %q, want reviewed %q", got, want)
	}
	if got, want := applied.Account.OAuth.ClientID, profile.OAuth.ClientID; got != want {
		t.Fatalf("client ID = %q, want reviewed %q", got, want)
	}
	if got, want := applied.Account.OAuth.RedirectURI, profile.OAuth.RedirectURI; got != want {
		t.Fatalf("redirect URI = %q, want reviewed %q", got, want)
	}
	if got, want := applied.Account.OAuth.Scopes, profile.OAuth.Scopes; got != want {
		t.Fatalf("scopes = %q, want reviewed %q", got, want)
	}
	if got, want := applied.Account.OAuth.RefreshScopes, profile.RefreshScopes; got != want {
		t.Fatalf("refresh scopes = %q, want reviewed %q", got, want)
	}
}

func TestOAuthLoginProfilesExposeOnlyEndToEndSupportedProviders(t *testing.T) {
	profiles := (&App{}).GetOAuthLoginProfiles()
	byID := make(map[string]OAuthLoginProfile, len(profiles))
	for _, profile := range profiles {
		byID[profile.ID] = profile
	}

	if _, found := byID["openai-codex"]; !found {
		t.Fatal("expected user-visible OpenAI/Codex OAuth profile")
	}
	for _, identifier := range []string{"grok-cli", "claude-code", "gemini-google", "antigravity", "custom"} {
		if _, found := byID[identifier]; found {
			t.Fatalf("OAuth profile without an end-to-end native runtime %q must not be presented as a one-click login", identifier)
		}
	}

	if profile := byID["openai-codex"]; profile.Available != oauthProfileReady || !profile.AutomaticCallback || profile.ManualCompletionRequired || profile.RequiresClientID {
		t.Fatalf("OpenAI/Codex login mode = %+v", profile)
	}
	for _, profile := range (&App{}).GetOAuthProviderProfiles() {
		if profile.ID == "openai-codex" || profile.ID == "antigravity" || profile.ID == "custom" {
			continue
		}
		if profile.Available != oauthProfileUnavailable {
			t.Fatalf("incomplete native OAuth runtime %q availability = %q, want unavailable", profile.ID, profile.Available)
		}
		if profile.APIURL == upstream.DefaultXIASSBaseURL {
			t.Fatalf("incomplete native OAuth profile %q must not describe the generic XIASS API as its provider runtime", profile.ID)
		}
	}
}

func TestPublicOAuthProfilesUseXIASSBranding(t *testing.T) {
	for _, profile := range (&App{}).GetOAuthProviderProfiles() {
		visible := strings.Join([]string{profile.Name, profile.Description, profile.Message}, " ")
		if strings.Contains(visible, "WF") || strings.Contains(visible, "BYOK") {
			t.Fatalf("public OAuth profile %q contains retired branding: %q", profile.ID, visible)
		}
	}
}

func TestOAuthProfileAuthorizationSupportIsAllowListed(t *testing.T) {
	tests := []struct {
		name      string
		available string
		want      bool
	}{
		{name: "automatic callback", available: oauthProfileReady, want: true},
		{name: "manual callback", available: oauthProfileManual, want: true},
		{name: "user supplied client", available: oauthProfileNeedsClientID, want: true},
		{name: "advanced custom only", available: oauthProfileCustomOnly, want: false},
		{name: "explicitly unavailable", available: oauthProfileUnavailable, want: false},
		{name: "future unknown mode", available: "experimental", want: false},
		{name: "missing mode", available: "", want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			profile := OAuthProviderProfile{Available: test.available}
			if got := oauthProfileSupportsAuthorization(profile); got != test.want {
				t.Fatalf("authorization support for %q = %t, want %t", test.available, got, test.want)
			}
		})
	}

	if got := oauthProfileUnsupportedMessage(OAuthProviderProfile{}); !strings.Contains(got, "高级自定义 OAuth") {
		t.Fatalf("fallback message = %q", got)
	}
	if got := oauthProfileUnsupportedMessage(OAuthProviderProfile{Message: "use an owned client"}); got != "use an owned client" {
		t.Fatalf("profile-specific unavailable message = %q", got)
	}
}

func TestProfileAuthorizationUsesPKCEAndSafeProviderParameters(t *testing.T) {
	storage.Init(t.TempDir())
	app := &App{}
	started := app.BeginOAuthProviderAuthorization("openai-codex", storage.UpstreamAccount{
		APIURL: "https://gateway.example.test/v1",
		OAuth:  storage.OAuthConfiguration{RedirectURI: "http://127.0.0.1:0/callback"},
	})
	defer app.stopOAuthLoopbacks()
	if !started.OK || started.SessionID == "" || started.AuthorizationURL == "" {
		t.Fatalf("begin profile OAuth result = %+v", started)
	}
	profile, _ := oauthProviderProfile("openai-codex")
	if got, want := started.RedirectURI, profile.OAuth.RedirectURI; got != want {
		t.Fatalf("loopback redirect URI = %q, want reviewed preset %q", got, want)
	}

	parsed, err := url.Parse(started.AuthorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	if got, want := query.Get("id_token_add_organizations"), "true"; got != want {
		t.Fatalf("OpenAI authorization parameter = %q, want %q", got, want)
	}
	if got, want := query.Get("codex_cli_simplified_flow"), "true"; got != want {
		t.Fatalf("Codex authorization parameter = %q, want %q", got, want)
	}
	if query.Get("state") == "" || query.Get("code_challenge") == "" || query.Get("code_challenge_method") != "S256" {
		t.Fatalf("authorization URL omitted required PKCE data: %s", started.AuthorizationURL)
	}
	if query.Get("code_verifier") != "" {
		t.Fatalf("authorization URL leaked PKCE verifier: %s", started.AuthorizationURL)
	}

	statusJSON, err := json.Marshal(app.GetOAuthAuthorizationStatus(started.SessionID))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"access_token", "refresh_token", "code_verifier"} {
		if strings.Contains(string(statusJSON), forbidden) {
			t.Fatalf("pending OAuth status leaked %q", forbidden)
		}
	}
}

func TestUnavailableNativeOAuthProfilesCannotStartAuthorization(t *testing.T) {
	storage.Init(t.TempDir())
	app := &App{}
	for _, profileID := range []string{"grok-cli", "claude-code", "gemini-google"} {
		started := app.BeginOAuthProviderAuthorization(profileID, storage.UpstreamAccount{
			APIURL: "https://gateway.example.test/v1",
			OAuth:  storage.OAuthConfiguration{ClientID: "user-owned-public-client"},
		})
		if started.OK || started.SessionID != "" || strings.TrimSpace(started.Message) == "" {
			t.Fatalf("unavailable profile %q unexpectedly started authorization: %+v", profileID, started)
		}
	}
}

func TestOpenAICodexProfileUsesReviewedHexPKCEFormat(t *testing.T) {
	profile, found := oauthProviderProfile("openai-codex")
	if !found {
		t.Fatal("OpenAI Codex profile was not found")
	}
	if got, want := profile.PKCEVerifierFormat, oauthflow.PKCEVerifierFormatOpenAIHex; got != want {
		t.Fatalf("OpenAI profile verifier format = %q, want %q", got, want)
	}
	if got, want := profile.APIURL, storage.OpenAICodexResponsesURL; got != want {
		t.Fatalf("OpenAI profile endpoint = %q, want direct Codex route %q", got, want)
	}
	if profile.EndpointMode != "manual" || profile.APIStyle != "responses" || profile.OAuth.Upstream != storage.OpenAICodexOAuthUpstream {
		t.Fatalf("OpenAI profile direct transport = %#v", profile)
	}
	encoded, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "openai_hex") {
		t.Fatalf("native PKCE format must not be renderer-controlled: %s", encoded)
	}
}

func TestReviewedProfileIgnoresHiddenExternalRedirectOverride(t *testing.T) {
	storage.Init(t.TempDir())
	app := &App{}
	started := app.BeginOAuthProviderAuthorization("openai-codex", storage.UpstreamAccount{
		APIURL: "https://gateway.example.test/v1",
		OAuth:  storage.OAuthConfiguration{RedirectURI: "https://desktop.example.test/oauth/callback"},
	})
	defer app.stopOAuthLoopbacks()
	if !started.OK {
		t.Fatalf("begin profile OAuth with external redirect = %+v", started)
	}
	if !started.AutomaticCallback || started.ManualCompletionRequired {
		t.Fatalf("reviewed profile completion mode = %+v, want automatic", started)
	}
	profile, _ := oauthProviderProfile("openai-codex")
	if got, want := started.RedirectURI, profile.OAuth.RedirectURI; got != want {
		t.Fatalf("redirect URI = %q, want reviewed preset %q", got, want)
	}
	app.oauthMu.Lock()
	callback := app.oauthLoopbacks[started.SessionID]
	app.oauthMu.Unlock()
	if callback == nil {
		t.Fatal("reviewed profile did not start its local loopback listener")
	}
}

func TestProfileLoopbackPortCollisionFallsBackToManualCompletion(t *testing.T) {
	storage.Init(t.TempDir())
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	redirectURI := "http://" + occupied.Addr().String() + "/callback"
	profile := OAuthProviderProfile{
		ID: "test-loopback-collision", Name: "Test", Provider: "openai", APIURL: "https://gateway.example.test",
		APIStyle: "responses", AuthMode: "bearer", AutoLoopback: true,
		OAuth: storage.OAuthConfiguration{
			AuthorizationURL: "https://auth.example.test/authorize", TokenURL: "https://auth.example.test/token",
			ClientID: "public-client", RedirectURI: redirectURI, Scopes: "openid",
		},
	}
	app := &App{}
	started := app.beginProfileOAuthAuthorization(profile, storage.UpstreamAccount{
		Provider: "openai", Type: "oauth", APIURL: "https://gateway.example.test", APIStyle: "responses", AuthMode: "bearer",
		OAuth: profile.OAuth,
	})
	if !started.OK {
		t.Fatalf("port-collision login must still start: %+v", started)
	}
	if started.AutomaticCallback || !started.ManualCompletionRequired {
		t.Fatalf("port-collision completion mode = %+v, want manual fallback", started)
	}
	if got, want := started.RedirectURI, redirectURI; got != want {
		t.Fatalf("fallback redirect URI = %q, want original registered URI %q", got, want)
	}
	if !strings.Contains(started.Message, "端口被占用") {
		t.Fatalf("fallback message = %q, want clear port-collision guidance", started.Message)
	}
	app.oauthMu.Lock()
	callback := app.oauthLoopbacks[started.SessionID]
	app.oauthMu.Unlock()
	if callback != nil {
		t.Fatal("port-collision fallback unexpectedly retained a loopback listener")
	}
}

func TestLoopbackOAuthCallbackExchangesOnlyMatchingStateAndRedactsStatus(t *testing.T) {
	storage.Init(t.TempDir())
	var tokenCalls atomic.Int32
	tokenServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		tokenCalls.Add(1)
		if err := request.ParseForm(); err != nil {
			t.Error(err)
			return
		}
		if got, want := request.PostForm.Get("code"), "good-code"; got != want {
			t.Errorf("authorization code = %q, want %q", got, want)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"access_token":"secret-access-token","refresh_token":"secret-refresh-token","expires_in":3600}`))
	}))
	defer tokenServer.Close()

	flow, err := oauthflow.New(oauthflow.Config{
		AuthorizationURL: tokenServer.URL + "/authorize",
		TokenURL:         tokenServer.URL + "/token",
		PublicClientID:   "test-public-client",
		RedirectURI:      "http://127.0.0.1:39002/callback",
		Scopes:           []string{"openid"},
	})
	if err != nil {
		t.Fatal(err)
	}
	authorization, err := flow.Begin()
	if err != nil {
		t.Fatal(err)
	}

	app := &App{
		oauthSessions: map[string]*pendingOAuthSession{
			authorization.SessionID: {
				flow: flow,
				account: storage.UpstreamAccount{
					ID: "loopback-account", Name: "loopback", Provider: "openai", Type: "oauth",
					APIURL: tokenServer.URL, APIStyle: "responses", AuthMode: "bearer", Enabled: true,
					MaxConcurrency: 1,
					OAuth: storage.OAuthConfiguration{
						AuthorizationURL: tokenServer.URL + "/authorize", TokenURL: tokenServer.URL + "/token",
						ClientID: "test-public-client", RedirectURI: "http://127.0.0.1:39002/callback", Scopes: "openid",
					},
				},
				state: authorization.State, expires: time.Now().Add(time.Minute),
			},
		},
		oauthResults:   make(map[string]oauthAuthorizationRecord),
		oauthLoopbacks: make(map[string]*oauthLoopbackListener),
	}

	handler := app.loopbackOAuthCallbackHandler(authorization.SessionID, &oauthLoopbackListener{path: "/callback"})
	invalid := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:39002/callback?code=bad-code&state=wrong-state", nil)
	invalid.RemoteAddr = "127.0.0.1:49002"
	invalidResponse := httptest.NewRecorder()
	handler.ServeHTTP(invalidResponse, invalid)
	if got, want := invalidResponse.Code, http.StatusBadRequest; got != want {
		t.Fatalf("invalid-state callback status = %d, want %d", got, want)
	}
	if got := tokenCalls.Load(); got != 0 {
		t.Fatalf("invalid-state callback called token endpoint %d time(s)", got)
	}

	validURL := "http://127.0.0.1:39002/callback?code=good-code&state=" + url.QueryEscape(authorization.State)
	valid := httptest.NewRequest(http.MethodGet, validURL, nil)
	valid.RemoteAddr = "127.0.0.1:49002"
	validResponse := httptest.NewRecorder()
	handler.ServeHTTP(validResponse, valid)
	if got, want := validResponse.Code, http.StatusOK; got != want {
		t.Fatalf("valid loopback callback status = %d, want %d: %s", got, want, validResponse.Body.String())
	}
	if got := tokenCalls.Load(); got != 1 {
		t.Fatalf("valid loopback callback called token endpoint %d time(s), want 1", got)
	}

	duplicateResponse := httptest.NewRecorder()
	handler.ServeHTTP(duplicateResponse, valid)
	if got, want := duplicateResponse.Code, http.StatusOK; got != want {
		t.Fatalf("duplicate callback status = %d, want %d: %s", got, want, duplicateResponse.Body.String())
	}
	if got := tokenCalls.Load(); got != 1 {
		t.Fatalf("duplicate callback exchanged token %d time(s), want exactly one", got)
	}

	stored, err := storage.GetUpstreamAccount("loopback-account")
	if err != nil {
		t.Fatal(err)
	}
	if stored.EffectiveAPIKey() != "secret-access-token" || credentialText(stored.Credentials, "refresh_token") != "secret-refresh-token" {
		t.Fatal("OAuth callback did not persist exchanged credentials")
	}
	statusJSON, err := json.Marshal(app.GetOAuthAuthorizationStatus(authorization.SessionID))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"secret-access-token", "secret-refresh-token"} {
		if strings.Contains(string(statusJSON), secret) {
			t.Fatalf("completed OAuth status leaked credential %q", secret)
		}
	}
}
