package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"antigravity-wf-assistant/internal/storage"
	"antigravity-wf-assistant/internal/upstream"
)

type explicitAccountAction struct {
	name string
	run  func(*App, string) (bool, string)
}

func explicitAccountActions() []explicitAccountAction {
	return []explicitAccountAction{
		{name: "discover", run: func(app *App, accountID string) (bool, string) {
			result := app.DiscoverAccountModels(accountID)
			return result.OK, result.Message
		}},
		{name: "sync", run: func(app *App, accountID string) (bool, string) {
			result := app.SyncUpstreamAccountModels(accountID)
			return result.OK, result.Message
		}},
		{name: "legacy test", run: func(app *App, accountID string) (bool, string) {
			result := app.TestUpstreamAccount(accountID, "gpt-refresh-test")
			return result.OK, result.Message
		}},
		{name: "detailed test", run: func(app *App, accountID string) (bool, string) {
			result := app.TestUpstreamAccountDetailed(upstream.AccountTestRequest{
				AccountID: accountID, Model: "gpt-refresh-test", Prompt: "hi",
			})
			return result.OK, result.Message
		}},
		{name: "quota", run: func(app *App, accountID string) (bool, string) {
			result := app.RefreshUpstreamAccountQuota(accountID)
			return result.OK, result.Message
		}},
	}
}

func saveExplicitOAuthActionAccount(t *testing.T, serverURL string) storage.UpstreamAccount {
	t.Helper()
	account := storage.UpstreamAccount{
		ID: "explicit-oauth", Name: "Explicit OAuth", Provider: "openai", Type: "oauth",
		APIURL: serverURL, APIKey: "old-access-token", APIStyle: "chat_completions", AuthMode: "bearer",
		Credentials: map[string]any{"refresh_token": "old-refresh-token"},
		OAuth: storage.OAuthConfiguration{
			AuthorizationURL: serverURL + "/authorize", TokenURL: serverURL + "/token",
			ClientID: "wf-public-client", RedirectURI: serverURL + "/callback", Scopes: "openid offline_access",
		},
		QuotaURL: serverURL + "/quota", AuthExpiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339),
		Enabled: true, MaxConcurrency: 1,
	}
	if err := storage.SaveUpstreamAccount(account); err != nil {
		t.Fatalf("save explicit OAuth account: %v", err)
	}
	return account
}

func writeExplicitActionResponse(t *testing.T, writer http.ResponseWriter, request *http.Request, wantToken string) {
	t.Helper()
	if got := request.Header.Get("Authorization"); got != "Bearer "+wantToken {
		t.Errorf("Authorization = %q, want refreshed bearer token", got)
	}
	writer.Header().Set("Content-Type", "application/json")
	switch request.URL.Path {
	case "/v1/models":
		_, _ = writer.Write([]byte(`{"data":[{"id":"gpt-refresh-test"}]}`))
	case "/v1/chat/completions":
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	case "/quota":
		_, _ = writer.Write([]byte(`{"requests_remaining":42}`))
	default:
		http.NotFound(writer, request)
	}
}

func TestExplicitAccountActionsRefreshNearExpiryOAuthToken(t *testing.T) {
	for _, action := range explicitAccountActions() {
		t.Run(action.name, func(t *testing.T) {
			storage.Init(t.TempDir())
			var refreshCalls atomic.Int32
			var upstreamCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path == "/token" {
					refreshCalls.Add(1)
					if err := request.ParseForm(); err != nil {
						t.Errorf("parse refresh request: %v", err)
					}
					if got := request.Form.Get("refresh_token"); got != "old-refresh-token" {
						t.Errorf("refresh_token = %q", got)
					}
					writer.Header().Set("Content-Type", "application/json")
					_, _ = writer.Write([]byte(`{"access_token":"new-access-token","refresh_token":"new-refresh-token","expires_in":3600}`))
					return
				}
				upstreamCalls.Add(1)
				writeExplicitActionResponse(t, writer, request, "new-access-token")
			}))
			defer server.Close()

			account := saveExplicitOAuthActionAccount(t, server.URL)
			ok, message := action.run(&App{}, account.ID)
			if !ok {
				t.Fatalf("explicit action failed after OAuth refresh: %s", message)
			}
			if got := refreshCalls.Load(); got != 1 {
				t.Fatalf("refresh calls = %d, want 1", got)
			}
			if got := upstreamCalls.Load(); got != 1 {
				t.Fatalf("upstream calls = %d, want 1", got)
			}
			stored, err := storage.GetUpstreamAccount(account.ID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.APIKey != "new-access-token" {
				t.Fatalf("stored access token = %q, want refreshed token", stored.APIKey)
			}
		})
	}
}

func TestExplicitAccountActionsStopWhenOAuthRefreshFails(t *testing.T) {
	for _, action := range explicitAccountActions() {
		t.Run(action.name, func(t *testing.T) {
			storage.Init(t.TempDir())
			var upstreamCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path == "/token" {
					http.Error(writer, `{"error":"temporarily_unavailable"}`, http.StatusServiceUnavailable)
					return
				}
				upstreamCalls.Add(1)
				writeExplicitActionResponse(t, writer, request, "old-access-token")
			}))
			defer server.Close()

			account := saveExplicitOAuthActionAccount(t, server.URL)
			ok, message := action.run(&App{}, account.ID)
			if ok || !strings.Contains(message, "OAuth 访问令牌刷新失败") {
				t.Fatalf("refresh failure result = ok:%v message:%q", ok, message)
			}
			if got := upstreamCalls.Load(); got != 0 {
				t.Fatalf("upstream called %d times after refresh failure", got)
			}
		})
	}
}

func TestExplicitAccountActionLeavesAPIKeyAccountUnchanged(t *testing.T) {
	storage.Init(t.TempDir())
	var upstreamCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		upstreamCalls.Add(1)
		writeExplicitActionResponse(t, writer, request, "stable-api-key")
	}))
	defer server.Close()
	account := storage.UpstreamAccount{
		ID: "explicit-api-key", Name: "API key", Provider: "openai", Type: "api_key",
		APIURL: server.URL, APIKey: "stable-api-key", APIStyle: "chat_completions", AuthMode: "bearer",
		Enabled: true, MaxConcurrency: 1,
	}
	if err := storage.SaveUpstreamAccount(account); err != nil {
		t.Fatal(err)
	}
	result := (&App{}).DiscoverAccountModels(account.ID)
	if !result.OK || upstreamCalls.Load() != 1 {
		t.Fatalf("API-key discovery changed by OAuth preflight: %#v, calls=%d", result, upstreamCalls.Load())
	}
	stored, err := storage.GetUpstreamAccount(account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.APIKey != account.APIKey || stored.Type != account.Type {
		t.Fatalf("API-key account changed: %#v", stored)
	}
}
