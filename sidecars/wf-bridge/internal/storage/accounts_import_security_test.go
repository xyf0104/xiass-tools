package storage

import "testing"

func TestImportUpstreamAccountsStripsForeignClientAndSessionState(t *testing.T) {
	Init(t.TempDir())
	result := ImportUpstreamAccounts(importAccountJSON(t, map[string]any{
		"name":     "safe OAuth import",
		"platform": "openai",
		"type":     "oauth",
		"credentials": map[string]any{
			"access_token":  "approved-access-token",
			"refresh_token": "approved-refresh-token",
			"id_token":      "approved-id-token",
			"client_id":     "approved-public-client",
			"expires_at":    float64(1_900_000_000),
			"client_secret": "must-not-be-persisted",
			"cookie":        "must-not-be-persisted",
			"cookies":       map[string]any{"sid": "must-not-be-persisted"},
			"session":       "must-not-be-persisted",
			"session_token": "must-not-be-persisted",
			"browser_state": map[string]any{"profile": "must-not-be-persisted"},
			"device_id":     "must-not-be-persisted",
		},
		"headers": map[string]any{
			"X-Client-Version": "safe-metadata",
			"Cookie":           "must-not-be-persisted",
			"Authorization":    "Bearer must-not-be-persisted",
			"X-API-Key":        "must-not-be-persisted",
		},
	}))
	if !result.OK || result.Added != 1 {
		t.Fatalf("import result = %#v, want one imported account", result)
	}

	accounts, err := LoadUpstreamAccounts()
	if err != nil || len(accounts) != 1 {
		t.Fatalf("load accounts = %#v, %v", accounts, err)
	}
	account := accounts[0]
	if got, want := account.EffectiveAPIKey(), "approved-access-token"; got != want {
		t.Fatalf("effective API key = %q, want %q", got, want)
	}
	for key, want := range map[string]string{
		"access_token":  "approved-access-token",
		"refresh_token": "approved-refresh-token",
		"id_token":      "approved-id-token",
		"client_id":     "approved-public-client",
	} {
		if got, _ := account.Credentials[key].(string); got != want {
			t.Fatalf("approved credential %s = %q, want %q", key, got, want)
		}
	}
	for _, forbidden := range []string{
		"client_secret", "cookie", "cookies", "session", "session_token", "browser_state", "device_id",
	} {
		if _, found := account.Credentials[forbidden]; found {
			t.Fatalf("foreign credential field %q was persisted: %#v", forbidden, account.Credentials)
		}
	}
	if got, want := account.Headers["X-Client-Version"], "safe-metadata"; got != want {
		t.Fatalf("safe metadata header = %q, want %q", got, want)
	}
	for _, forbidden := range []string{"Cookie", "Authorization", "X-API-Key"} {
		if _, found := account.Headers[forbidden]; found {
			t.Fatalf("sensitive imported header %q was persisted: %#v", forbidden, account.Headers)
		}
	}
}
