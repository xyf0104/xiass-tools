package storage

import "testing"

func TestSaveUpstreamAccountRejectsRawCredentialPayloadTypes(t *testing.T) {
	cases := []string{
		"auth_json", "auth.json", "auth-json", "oauth_json", "oauth-json",
		"credential_json", "credential-json", "credentials_json", "json_import",
		"json-import", "raw_json", "account_json", "json", "import",
		"refresh_token", "refresh-token", "refreshToken", "mobile_rt", "mobile-rt", "mobileRT",
	}
	for _, accountType := range cases {
		accountType := accountType
		t.Run(accountType, func(t *testing.T) {
			Init(t.TempDir())
			err := SaveUpstreamAccount(UpstreamAccount{
				Name:     "must not be saved",
				Provider: "openai",
				Type:     accountType,
				APIURL:   "https://api.example.test",
				APIKey:   "not-a-valid-direct-credential",
			})
			if err == nil {
				t.Fatalf("SaveUpstreamAccount accepted raw credential type %q", accountType)
			}
			accounts, loadErr := LoadUpstreamAccounts()
			if loadErr != nil {
				t.Fatalf("LoadUpstreamAccounts: %v", loadErr)
			}
			if len(accounts) != 0 {
				t.Fatalf("raw credential type %q was persisted: %#v", accountType, accounts)
			}
		})
	}
}

func TestImportUpstreamAccountsPromotesRawJSONOAuthTypes(t *testing.T) {
	for _, accountType := range []string{"auth_json", "auth.json", "oauth_json", "credential_json", "json_import"} {
		accountType := accountType
		t.Run(accountType, func(t *testing.T) {
			Init(t.TempDir())
			result := ImportUpstreamAccounts(importAccountJSON(t, map[string]any{
				"name":     "imported OAuth JSON",
				"provider": "openai",
				"type":     accountType,
				"api_url":  "https://api.example.test",
				"credentials": map[string]any{
					"access_token":  "imported-access-token",
					"refresh_token": "imported-refresh-token",
				},
			}))
			if !result.OK || result.Added != 1 {
				t.Fatalf("import result = %#v, want one imported OAuth account", result)
			}
			accounts, err := LoadUpstreamAccounts()
			if err != nil || len(accounts) != 1 {
				t.Fatalf("LoadUpstreamAccounts = %#v, %v", accounts, err)
			}
			account := accounts[0]
			if account.Type != "oauth" {
				t.Fatalf("imported account type = %q, want oauth", account.Type)
			}
			if account.EffectiveAPIKey() != "imported-access-token" {
				t.Fatalf("imported effective API key = %q, want access token", account.EffectiveAPIKey())
			}
			if got, _ := account.Credentials["refresh_token"].(string); got != "imported-refresh-token" {
				t.Fatalf("canonical refresh token = %q, want imported refresh token", got)
			}
		})
	}
}

func TestImportUpstreamAccountsPromotesRawJSONAPIKeyTypes(t *testing.T) {
	for _, accountType := range []string{"auth_json", "auth.json", "oauth_json", "credential_json", "json_import"} {
		accountType := accountType
		t.Run(accountType, func(t *testing.T) {
			Init(t.TempDir())
			result := ImportUpstreamAccounts(importAccountJSON(t, map[string]any{
				"name": "imported API-key JSON", "provider": "openai", "type": accountType,
				"api_url": "https://api.example.test", "api_key": "imported-api-key",
			}))
			if !result.OK || result.Added != 1 {
				t.Fatalf("import result = %#v, want one imported API-key account", result)
			}
			accounts, err := LoadUpstreamAccounts()
			if err != nil || len(accounts) != 1 {
				t.Fatalf("LoadUpstreamAccounts = %#v, %v", accounts, err)
			}
			if account := accounts[0]; account.Type != "api_key" || account.EffectiveAPIKey() != "imported-api-key" {
				t.Fatalf("imported API-key account = %#v", account)
			}
		})
	}
}
