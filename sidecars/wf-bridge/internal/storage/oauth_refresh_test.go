package storage

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func oauthRefreshTestConfiguration(serverURL string) OAuthConfiguration {
	return OAuthConfiguration{
		AuthorizationURL: serverURL + "/authorize",
		TokenURL:         serverURL + "/token",
		ClientID:         "wf-public-client",
		RedirectURI:      serverURL + "/callback",
		Scopes:           "openid offline_access",
		RefreshScopes:    "openid",
	}
}

func saveOAuthRefreshTestAccount(t *testing.T, account UpstreamAccount) {
	t.Helper()
	if err := SaveUpstreamAccount(account); err != nil {
		t.Fatalf("save OAuth test account: %v", err)
	}
}

func TestAcquireAccountForModelRefreshesNearExpiryOAuthToken(t *testing.T) {
	Init(t.TempDir())

	var refreshCalls atomic.Int32
	tokenServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/token" {
			t.Errorf("request path = %q, want /token", request.URL.Path)
		}
		if request.Method != http.MethodPost {
			t.Errorf("request method = %q, want POST", request.Method)
		}
		if err := request.ParseForm(); err != nil {
			t.Errorf("parse token form: %v", err)
		}
		if got := request.Form.Get("grant_type"); got != "refresh_token" {
			t.Errorf("grant_type = %q, want refresh_token", got)
		}
		if got := request.Form.Get("client_id"); got != "wf-public-client" {
			t.Errorf("client_id = %q, want wf-public-client", got)
		}
		if got := request.Form.Get("refresh_token"); got != "old-refresh-token" {
			t.Errorf("refresh_token = %q, want old-refresh-token", got)
		}
		if got := request.Form.Get("scope"); got != "openid" {
			t.Errorf("scope = %q, want provider refresh scopes", got)
		}
		refreshCalls.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"access_token":"new-access-token","refresh_token":"new-refresh-token","token_type":"Bearer","expires_in":3600}`))
	}))
	defer tokenServer.Close()

	saveOAuthRefreshTestAccount(t, UpstreamAccount{
		ID:             "oauth-near-expiry",
		Name:           "OAuth near expiry",
		Provider:       "openai",
		Type:           "oauth",
		APIURL:         "https://api.example.test",
		APIKey:         "old-access-token",
		Credentials:    map[string]any{"refresh_token": "old-refresh-token"},
		OAuth:          oauthRefreshTestConfiguration(tokenServer.URL),
		AuthExpiresAt:  time.Now().Add(time.Minute).UTC().Format(time.RFC3339),
		AuthMode:       "bearer",
		Enabled:        true,
		MaxConcurrency: 1,
	})

	model := CustomModel{Provider: "openai", AccountIDs: []string{"oauth-near-expiry"}}
	selected, lease, err := AcquireAccountForModel(model, nil)
	if err != nil {
		t.Fatalf("acquire refreshed OAuth account: %v", err)
	}
	if lease == nil || lease.ID != "oauth-near-expiry" {
		t.Fatalf("lease = %#v, want refreshed OAuth account", lease)
	}
	lease.Finish(http.StatusOK, "", "")
	if selected.APIKey != "new-access-token" {
		t.Fatalf("selected API key = %q, want rotated access token", selected.APIKey)
	}
	if got := refreshCalls.Load(); got != 1 {
		t.Fatalf("refresh requests = %d, want 1", got)
	}

	stored, err := GetUpstreamAccount("oauth-near-expiry")
	if err != nil {
		t.Fatal(err)
	}
	if stored.APIKey != "new-access-token" || accountCredential(stored.Credentials, "refresh_token") != "new-refresh-token" {
		t.Fatalf("rotated credentials were not persisted: %#v", stored)
	}
	expiresAt, ok := parseAccountTime(stored.AuthExpiresAt)
	if !ok || time.Until(expiresAt) < 45*time.Minute {
		t.Fatalf("refreshed auth expiry = %q, want approximately one hour", stored.AuthExpiresAt)
	}
	if stored.Identity.Source != "OAuth 刷新响应" {
		t.Fatalf("identity source = %q, want OAuth refresh marker", stored.Identity.Source)
	}
}

func TestAcquireAccountForModelRejectsExpiredOAuthWithoutRefreshToken(t *testing.T) {
	Init(t.TempDir())
	saveOAuthRefreshTestAccount(t, UpstreamAccount{
		ID:             "oauth-expired",
		Name:           "OAuth expired",
		Provider:       "openai",
		Type:           "oauth",
		APIURL:         "https://api.example.test",
		APIKey:         "expired-access-token",
		AuthExpiresAt:  time.Now().Add(-time.Minute).UTC().Format(time.RFC3339),
		AuthMode:       "bearer",
		Enabled:        true,
		MaxConcurrency: 1,
	})

	model := CustomModel{Provider: "openai", AccountIDs: []string{"oauth-expired"}}
	_, lease, err := AcquireAccountForModel(model, nil)
	if err == nil {
		t.Fatal("expired OAuth account without a refresh token was scheduled")
	}
	if lease != nil {
		t.Fatalf("lease = %#v, want no lease for expired OAuth account", lease)
	}

	stored, err := GetUpstreamAccount("oauth-expired")
	if err != nil {
		t.Fatal(err)
	}
	if accountUsable(stored, time.Now()) {
		t.Fatalf("expired OAuth account remained usable: %#v", stored)
	}
	if stored.FailureCount != 1 || stored.CooldownUntil == "" || stored.LastError == "" {
		t.Fatalf("refresh failure did not mark account health: %#v", stored)
	}
	cooldown, ok := parseAccountTime(stored.CooldownUntil)
	if !ok || !cooldown.After(time.Now()) {
		t.Fatalf("cooldown = %q, want future OAuth refresh cooldown", stored.CooldownUntil)
	}
}

func TestAcquireAccountForModelUsesDirectCredentialWhenBoundPoolIsUnavailable(t *testing.T) {
	Init(t.TempDir())
	saveOAuthRefreshTestAccount(t, UpstreamAccount{
		ID: "temporarily-disabled", Name: "Temporarily disabled", Provider: "openai", Type: "api_key",
		APIURL: "https://account.example.test", APIKey: "account-token", AuthMode: "bearer", Enabled: true, MaxConcurrency: 1,
	})
	if err := SetUpstreamAccountEnabled("temporarily-disabled", false); err != nil {
		t.Fatalf("disable account: %v", err)
	}

	model := CustomModel{
		Provider: "openai", APIURL: "https://direct.example.test", APIKey: "legacy-direct-token",
		AccountIDs: []string{"temporarily-disabled"},
	}
	selected, lease, err := AcquireAccountForModel(model, nil)
	if err != nil {
		t.Fatalf("acquire direct fallback: %v", err)
	}
	if lease != nil || selected.APIKey != "legacy-direct-token" || selected.APIURL != "https://direct.example.test" {
		t.Fatalf("selected direct fallback = %#v, lease = %#v", selected, lease)
	}

	if err := SetUpstreamAccountEnabled("temporarily-disabled", true); err != nil {
		t.Fatalf("re-enable account: %v", err)
	}
	selected, lease, err = AcquireAccountForModel(model, nil)
	if err != nil {
		t.Fatalf("acquire healthy account: %v", err)
	}
	if lease == nil || lease.ID != "temporarily-disabled" || selected.APIKey != "account-token" || selected.APIURL != "https://account.example.test" {
		t.Fatalf("healthy pool account did not take precedence: selected = %#v, lease = %#v", selected, lease)
	}
	lease.Finish(http.StatusOK, "", "")
}

func TestAcquireAccountForModelUsesDirectCredentialWhenPoolStorageCannotBeRead(t *testing.T) {
	Init(t.TempDir())
	if err := os.WriteFile(accountsFile, []byte(`{"accounts":`), 0o600); err != nil {
		t.Fatalf("corrupt account storage for fallback test: %v", err)
	}
	model := CustomModel{
		Provider: "openai", APIURL: "https://direct.example.test", APIKey: "legacy-direct-token",
		AccountIDs: []string{"missing-or-unreadable-account"},
	}
	selected, lease, err := AcquireAccountForModel(model, nil)
	if err != nil {
		t.Fatalf("direct fallback after account storage read error: %v", err)
	}
	if lease != nil || selected.APIKey != model.APIKey || selected.APIURL != model.APIURL {
		t.Fatalf("storage-error fallback = %#v, lease = %#v", selected, lease)
	}
}

func TestAcquireAccountForModelUsesPeerWhileRefreshFailureCoolsDown(t *testing.T) {
	Init(t.TempDir())

	var refreshCalls atomic.Int32
	tokenServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		refreshCalls.Add(1)
		http.Error(writer, `{"error":"temporarily unavailable"}`, http.StatusServiceUnavailable)
	}))
	defer tokenServer.Close()

	saveOAuthRefreshTestAccount(t, UpstreamAccount{
		ID:             "oauth-failing",
		Name:           "OAuth failing",
		Provider:       "openai",
		Type:           "oauth",
		APIURL:         "https://api.example.test",
		APIKey:         "old-access-token",
		Credentials:    map[string]any{"refresh_token": "old-refresh-token"},
		OAuth:          oauthRefreshTestConfiguration(tokenServer.URL),
		AuthExpiresAt:  time.Now().Add(time.Minute).UTC().Format(time.RFC3339),
		AuthMode:       "bearer",
		Enabled:        true,
		Priority:       1,
		MaxConcurrency: 1,
	})
	saveOAuthRefreshTestAccount(t, UpstreamAccount{
		ID: "pool-peer", Name: "pool peer", Provider: "openai", Type: "api_key", APIURL: "https://api.example.test",
		APIKey: "peer-token", AuthMode: "bearer", Enabled: true, Priority: 2, MaxConcurrency: 1,
	})

	model := CustomModel{Provider: "openai", AccountIDs: []string{"oauth-failing", "pool-peer"}}
	selected, lease, err := AcquireAccountForModel(model, nil)
	if err != nil {
		t.Fatalf("acquire pool peer after OAuth refresh failure: %v", err)
	}
	if lease == nil || lease.ID != "pool-peer" || selected.APIKey != "peer-token" {
		t.Fatalf("selected = %#v, lease = %#v; want usable pool peer", selected, lease)
	}
	lease.Finish(http.StatusOK, "", "")

	failed, err := GetUpstreamAccount("oauth-failing")
	if err != nil {
		t.Fatal(err)
	}
	if failed.FailureCount != 1 || failed.CooldownUntil == "" {
		t.Fatalf("failed OAuth account has no cooldown: %#v", failed)
	}

	selected, lease, err = AcquireAccountForModel(model, nil)
	if err != nil {
		t.Fatalf("reacquire pool peer during OAuth cooldown: %v", err)
	}
	if lease == nil || lease.ID != "pool-peer" || selected.APIKey != "peer-token" {
		t.Fatalf("selected = %#v, lease = %#v; want pool peer during cooldown", selected, lease)
	}
	lease.Finish(http.StatusOK, "", "")
	if got := refreshCalls.Load(); got != 1 {
		t.Fatalf("refresh requests during cooldown = %d, want 1", got)
	}
}

func TestAcquireAccountForModelReportsTemporaryCooldownWithRetryTime(t *testing.T) {
	Init(t.TempDir())
	now := time.Now().UTC()
	saveOAuthRefreshTestAccount(t, UpstreamAccount{
		ID: "cooling", Name: "cooling", Provider: "openai", Type: "api_key",
		APIURL: "https://example.test", APIKey: "token", AuthMode: "bearer", Enabled: true, MaxConcurrency: 1,
		CooldownUntil: now.Add(2 * time.Second).Format(time.RFC3339),
	})

	_, lease, err := AcquireAccountForModel(CustomModel{Provider: "openai", AccountIDs: []string{"cooling"}}, nil)
	if err == nil || lease != nil {
		t.Fatalf("selected cooling account: lease=%#v err=%v", lease, err)
	}
	delay, temporary := AccountPoolRetryAfter(err)
	if !temporary || delay <= 0 || delay > 3*time.Second {
		t.Fatalf("temporary cooldown delay = %v, temporary=%v, err=%v", delay, temporary, err)
	}
}

func TestEnsureAccountAccessTokenCoalescesConcurrentRefreshes(t *testing.T) {
	Init(t.TempDir())

	var refreshCalls atomic.Int32
	refreshStarted := make(chan struct{})
	releaseRefresh := make(chan struct{})
	tokenServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if refreshCalls.Add(1) == 1 {
			close(refreshStarted)
		}
		<-releaseRefresh
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"access_token":"coalesced-access-token","expires_in":3600}`))
	}))
	defer tokenServer.Close()

	saveOAuthRefreshTestAccount(t, UpstreamAccount{
		ID:             "oauth-concurrent",
		Name:           "OAuth concurrent",
		Provider:       "openai",
		Type:           "oauth",
		APIURL:         "https://api.example.test",
		APIKey:         "old-access-token",
		Credentials:    map[string]any{"refresh_token": "old-refresh-token"},
		OAuth:          oauthRefreshTestConfiguration(tokenServer.URL),
		AuthExpiresAt:  time.Now().Add(time.Minute).UTC().Format(time.RFC3339),
		AuthMode:       "bearer",
		Enabled:        true,
		MaxConcurrency: 1,
	})

	const callers = 8
	start := make(chan struct{})
	errs := make(chan error, callers)
	var ready sync.WaitGroup
	var callersDone sync.WaitGroup
	ready.Add(callers)
	callersDone.Add(callers)
	for range callers {
		go func() {
			defer callersDone.Done()
			ready.Done()
			<-start
			errs <- EnsureAccountAccessToken(context.Background(), "oauth-concurrent")
		}()
	}
	ready.Wait()
	close(start)
	select {
	case <-refreshStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("OAuth refresh did not reach the token endpoint")
	}
	// Give all callers a chance to join the in-flight refresh before releasing
	// the test endpoint. A second request would prove the single-flight guard
	// is ineffective.
	time.Sleep(25 * time.Millisecond)
	close(releaseRefresh)
	callersDone.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent token refresh failed: %v", err)
		}
	}
	if got := refreshCalls.Load(); got != 1 {
		t.Fatalf("concurrent refresh requests = %d, want 1", got)
	}
}
