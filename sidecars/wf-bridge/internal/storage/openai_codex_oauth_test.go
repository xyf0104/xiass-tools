package storage

import "testing"

func TestOpenAICodexOAuthAccountBecomesDirectRuntimeModel(t *testing.T) {
	account := UpstreamAccount{
		ID:       "openai-oauth",
		Provider: "openai",
		Type:     "oauth",
		APIURL:   "https://api.xiass.com",
		APIKey:   "oauth-access-token",
		OAuth:    OAuthConfiguration{Upstream: OpenAICodexOAuthUpstream},
		Identity: AccountIdentity{ChatGPTAccountID: "chatgpt-account"},
	}
	model := account.ToModel(CustomModel{Provider: "openai", APIURL: "https://api.xiass.com", APIStyle: "auto"})
	if got, want := model.APIURL, OpenAICodexResponsesURL; got != want {
		t.Fatalf("direct OAuth model APIURL = %q, want %q", got, want)
	}
	if model.EndpointMode != "manual" || model.APIStyle != "responses" || model.AuthMode != "bearer" {
		t.Fatalf("direct OAuth model request shape = %#v", model)
	}
	if model.RuntimeOAuthUpstream != OpenAICodexOAuthUpstream || model.RuntimeChatGPTAccountID != "chatgpt-account" {
		t.Fatalf("direct OAuth model runtime metadata = %#v", model)
	}
}

func TestOpenAICodexQuotaSnapshotFillsOnlyMissingDisplayIdentity(t *testing.T) {
	Init(t.TempDir())
	if err := SaveUpstreamAccount(UpstreamAccount{
		ID: "openai-oauth", Name: "OpenAI OAuth", Provider: "openai", Type: "oauth",
		APIURL: OpenAICodexResponsesURL, APIKey: "oauth-access-token", Enabled: true,
		OAuth:    OAuthConfiguration{Upstream: OpenAICodexOAuthUpstream},
		Identity: AccountIdentity{Source: "OAuth 授权响应"},
	}); err != nil {
		t.Fatalf("save OAuth account: %v", err)
	}
	if err := SaveQuotaSnapshot("openai-oauth", QuotaSnapshot{
		Available: true, Source: "OpenAI / Codex OAuth 用量接口", Email: "person@example.test",
		Plan: "free", AccountID: "chatgpt-account", Windows: []QuotaWindow{{Label: "5h", UsedPercent: 12}},
	}); err != nil {
		t.Fatalf("save quota snapshot: %v", err)
	}
	account, err := GetUpstreamAccount("openai-oauth")
	if err != nil {
		t.Fatal(err)
	}
	if account.Identity.Email != "person@example.test" || account.Identity.Plan != "free" || account.Identity.ChatGPTAccountID != "chatgpt-account" {
		t.Fatalf("quota display identity = %#v", account.Identity)
	}
	if account.Identity.Source != "OAuth 授权响应" {
		t.Fatalf("quota must not replace OAuth identity source: %q", account.Identity.Source)
	}
	if len(account.Quota.Windows) != 1 || account.Quota.Windows[0].Label != "5h" {
		t.Fatalf("stored quota = %#v", account.Quota)
	}

	if err := SaveQuotaSnapshot("openai-oauth", QuotaSnapshot{Available: true, Email: "other@example.test", Plan: "pro", AccountID: "other-account"}); err != nil {
		t.Fatalf("save second quota snapshot: %v", err)
	}
	account, err = GetUpstreamAccount("openai-oauth")
	if err != nil {
		t.Fatal(err)
	}
	if account.Identity.Email != "person@example.test" || account.Identity.Plan != "free" || account.Identity.ChatGPTAccountID != "chatgpt-account" {
		t.Fatalf("quota overwrote verified display identity: %#v", account.Identity)
	}
}
