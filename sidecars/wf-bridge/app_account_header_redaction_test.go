package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"antigravity-wf-assistant/internal/storage"
)

func TestUpstreamAccountViewsRedactAndPreserveAdditionalHeaders(t *testing.T) {
	dir := t.TempDir()
	storage.Init(filepath.Join(dir, "xiass-state"))
	app := &App{storageDir: dir}

	account := storage.UpstreamAccount{
		ID: "header-account", Name: "Header account", Provider: "openai", Type: "api_key",
		APIURL: "https://gateway.example.test", EndpointMode: "auto", APIStyle: "chat_completions",
		MessagePathMode: "auto", AuthMode: "bearer", APIKey: "account-secret", Enabled: true,
		Headers: map[string]string{"X-Client-Version": "xiass-private-header-value"},
	}
	if result := app.SaveUpstreamAccount(account); !result.OK {
		t.Fatalf("could not save account: %#v", result)
	}

	views := app.GetUpstreamAccounts()
	if len(views) != 1 || !views[0].HasPrivateHeaders || views[0].Headers != nil {
		t.Fatalf("redacted account view = %#v", views)
	}
	encoded, err := json.Marshal(views)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"X-Client-Version", "xiass-private-header-value", "account-secret"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("account view leaked %q: %s", forbidden, encoded)
		}
	}

	// The redacted view returns a nil header map. Saving that edit must retain
	// the native-side headers instead of treating the missing field as delete.
	edit := views[0].UpstreamAccount
	edit.Name = "Edited header account"
	if result := app.SaveUpstreamAccount(edit); !result.OK {
		t.Fatalf("could not save redacted edit: %#v", result)
	}
	stored, err := storage.GetUpstreamAccount(account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := stored.Headers["X-Client-Version"]; got != "xiass-private-header-value" {
		t.Fatalf("redacted edit did not retain native header: %#v", stored.Headers)
	}

	// An explicitly supplied empty map is the intentional clear operation.
	edit.Headers = map[string]string{}
	if result := app.SaveUpstreamAccount(edit); !result.OK {
		t.Fatalf("could not clear headers: %#v", result)
	}
	stored, err = storage.GetUpstreamAccount(account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Headers) != 0 {
		t.Fatalf("explicit header clear was not persisted: %#v", stored.Headers)
	}
}

func TestUpstreamAccountRejectsSensitiveAdditionalHeaders(t *testing.T) {
	storage.Init(t.TempDir())
	result := (&App{}).SaveUpstreamAccount(storage.UpstreamAccount{
		Name: "sensitive header", Provider: "openai", Type: "api_key", APIURL: "https://gateway.example.test",
		EndpointMode: "auto", APIStyle: "chat_completions", MessagePathMode: "auto", AuthMode: "bearer",
		APIKey: "account-secret", Enabled: true, Headers: map[string]string{"Authorization": "Bearer should-not-be-stored"},
	})
	if result.OK || !strings.Contains(result.Message, "认证信息") || strings.Contains(result.Message, "should-not-be-stored") {
		t.Fatalf("sensitive additional header was not safely rejected: %#v", result)
	}
}
