package storage

import (
	"strings"
	"testing"
)

func saveProtocolPoolAccount(t *testing.T, account UpstreamAccount) {
	t.Helper()
	if err := SaveUpstreamAccount(account); err != nil {
		t.Fatalf("save account %q: %v", account.ID, err)
	}
}

func TestAcquireAccountForModelSkipsLegacyMixedProtocolAccounts(t *testing.T) {
	Init(t.TempDir())
	saveProtocolPoolAccount(t, UpstreamAccount{
		ID: "wrong-provider", Name: "wrong provider", Provider: "anthropic", Type: "api_key",
		APIURL: "https://anthropic.example.test", APIStyle: "messages", APIKey: "wrong-provider-key",
		AuthMode: "x_api_key", Enabled: true, Priority: 0, MaxConcurrency: 1,
	})
	saveProtocolPoolAccount(t, UpstreamAccount{
		ID: "wrong-style", Name: "wrong style", Provider: "openai", Type: "api_key",
		APIURL: "https://responses.example.test", APIStyle: "responses", APIKey: "wrong-style-key",
		AuthMode: "bearer", Enabled: true, Priority: 1, MaxConcurrency: 1,
	})
	saveProtocolPoolAccount(t, UpstreamAccount{
		ID: "chat-peer", Name: "chat peer", Provider: "openai", Type: "api_key",
		// A different gateway URL remains eligible: it implements the same Chat
		// Completions request protocol as the selected model.
		APIURL: "https://chat-gateway.example.test", APIStyle: "chat_completions", APIKey: "chat-peer-key",
		AuthMode: "bearer", Enabled: true, Priority: 2, MaxConcurrency: 1,
	})

	model := CustomModel{
		Provider: "openai", APIURL: "https://model-gateway.example.test", APIStyle: "chat_completions",
		ExternalModelName: "gpt-route-pool", AccountIDs: []string{"wrong-provider", "wrong-style", "chat-peer"},
	}
	selected, lease, err := AcquireAccountForModel(model, nil)
	if err != nil {
		t.Fatalf("acquire compatible peer: %v", err)
	}
	if lease == nil || lease.ID != "chat-peer" {
		t.Fatalf("lease = %#v, want same-protocol account after mixed legacy pool filtering", lease)
	}
	if selected.APIKey != "chat-peer-key" || selected.APIURL != "https://chat-gateway.example.test" {
		t.Fatalf("selected same-protocol peer was not applied: %#v", selected)
	}
	lease.Finish(200, "", "")

	// An older mixed pool with a direct model credential remains usable: the
	// incompatible account is ignored and the pre-existing direct path wins.
	direct := model
	direct.AccountIDs = []string{"wrong-provider", "wrong-style"}
	direct.APIKey = "direct-fallback-key"
	direct.APIURL = "https://direct.example.test"
	selected, lease, err = AcquireAccountForModel(direct, nil)
	if err != nil {
		t.Fatalf("direct fallback after incompatible legacy pool: %v", err)
	}
	if lease != nil || selected.APIKey != "direct-fallback-key" || selected.APIURL != "https://direct.example.test" {
		t.Fatalf("incompatible pool did not fall back to direct credential: selected=%#v lease=%#v", selected, lease)
	}

	withoutFallback := model
	withoutFallback.AccountIDs = []string{"wrong-provider", "wrong-style"}
	_, lease, err = AcquireAccountForModel(withoutFallback, nil)
	if err == nil || !strings.Contains(err.Error(), "请求协议不兼容") {
		t.Fatalf("mixed legacy pool error = %v, want explicit protocol incompatibility", err)
	}
	if lease != nil {
		t.Fatalf("incompatible pool unexpectedly reserved a lease: %#v", lease)
	}
}
