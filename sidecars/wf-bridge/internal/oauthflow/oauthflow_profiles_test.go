package oauthflow

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestBeginAddsSafeProviderAuthorizationParameters(t *testing.T) {
	flow, err := New(Config{
		AuthorizationURL: "https://accounts.example.test/authorize",
		TokenURL:         "https://accounts.example.test/token",
		PublicClientID:   "desktop-public-client",
		RedirectURI:      "http://127.0.0.1:38199/callback",
		Scopes:           []string{"openid", "offline_access"},
		AuthorizationParameters: map[string]string{
			"id_token_add_organizations": "true",
			"plan":                       "generic",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	authorization, err := flow.Begin()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(authorization.URL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	if got, want := query.Get("id_token_add_organizations"), "true"; got != want {
		t.Fatalf("OpenAI profile parameter = %q, want %q", got, want)
	}
	if got, want := query.Get("plan"), "generic"; got != want {
		t.Fatalf("Grok profile parameter = %q, want %q", got, want)
	}
	if query.Get("code_verifier") != "" {
		t.Fatalf("authorization URL must never expose the PKCE verifier: %s", authorization.URL)
	}
}

func TestBeginRejectsOAuthCoreParameterOverride(t *testing.T) {
	_, err := New(Config{
		AuthorizationURL: "https://accounts.example.test/authorize",
		TokenURL:         "https://accounts.example.test/token",
		PublicClientID:   "desktop-public-client",
		RedirectURI:      "http://127.0.0.1:38199/callback",
		AuthorizationParameters: map[string]string{
			"state": "attacker-controlled",
		},
	})
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("New with a reserved authorization parameter error = %v, want invalid config", err)
	}
}

func TestRefreshUsesProfileRefreshScopes(t *testing.T) {
	var received url.Values
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := request.ParseForm(); err != nil {
			t.Error(err)
			return
		}
		received = request.PostForm
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"access_token":"new-access-token"}`))
	}))
	defer server.Close()

	flow, err := New(Config{
		AuthorizationURL: server.URL + "/authorize",
		TokenURL:         server.URL + "/token",
		PublicClientID:   "desktop-public-client",
		RedirectURI:      server.URL + "/callback",
		Scopes:           []string{"openid", "profile", "offline_access"},
		RefreshScopes:    []string{"openid", "profile"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := flow.Refresh(context.Background(), "existing-refresh-token"); err != nil {
		t.Fatal(err)
	}
	if got, want := received.Get("scope"), "openid profile"; got != want {
		t.Fatalf("refresh scope = %q, want %q", got, want)
	}
}
