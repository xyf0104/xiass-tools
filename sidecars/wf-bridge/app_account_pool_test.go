package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"antigravity-wf-assistant/internal/storage"
	"antigravity-wf-assistant/internal/upstream"
)

func saveModelTestAccount(t *testing.T, id, provider, token string) {
	t.Helper()
	account := storage.UpstreamAccount{
		ID: id, Name: id, Provider: provider, Type: "api_key", APIURL: "https://api.example.test",
		APIKey: token, AuthMode: "bearer", Enabled: true, Priority: 10, MaxConcurrency: 1,
	}
	if provider == "anthropic" {
		account.AuthMode = "x_api_key"
		account.APIStyle = "messages"
	}
	if err := storage.SaveUpstreamAccount(account); err != nil {
		t.Fatalf("save account %s: %v", id, err)
	}
}

func TestAccountBoundModelSavesWithoutCopiedModelCredential(t *testing.T) {
	storage.Init(t.TempDir())
	saveModelTestAccount(t, "first", "openai", "token-one")
	saveModelTestAccount(t, "second", "openai", "token-two")

	app := &App{}
	result := app.SaveModel(storage.CustomModel{
		Name: "models/account-bound", DisplayName: "account-bound", Provider: "openai", ExternalModelName: "gpt-test",
		AccountIDs: []string{"first", "second"}, APIStyle: "auto",
	})
	if !result.OK {
		t.Fatalf("account-bound model was rejected without a direct API key: %+v", result)
	}
	models, err := storage.LoadModels()
	if err != nil || len(models) != 1 {
		t.Fatalf("saved models = %#v, %v", models, err)
	}
	if len(models[0].AccountIDs) != 2 || models[0].AccountIDs[0] != "first" || models[0].AccountIDs[1] != "second" {
		t.Fatalf("model did not retain its complete account pool: %#v", models[0].AccountIDs)
	}
	if models[0].APIKey != "" {
		t.Fatal("model copied an account credential into model storage")
	}
}

func TestAccountBoundDiscoveryUsesPrimaryAndBatchImportRetainsPool(t *testing.T) {
	storage.Init(t.TempDir())
	saveModelTestAccount(t, "first", "openai", "token-one")
	saveModelTestAccount(t, "second", "openai", "token-two")

	app := &App{}
	config := upstream.Config{Provider: "openai", UpstreamName: "XIASS 代理", AccountIDs: []string{"first", "second"}}
	resolved, err := app.resolveUpstreamConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.AccountID != "first" || resolved.APIKey != "token-one" {
		t.Fatalf("discovery/test did not use the first selected account: %+v", resolved)
	}

	result := app.AddDiscoveredModels(config, []string{"gpt-one", "gpt-two"})
	if !result.OK || result.Added != 2 {
		t.Fatalf("batch import failed: %+v", result)
	}
	models, err := storage.LoadModels()
	if err != nil || len(models) != 2 {
		t.Fatalf("imported models = %#v, %v", models, err)
	}
	for _, model := range models {
		if model.UpstreamName != "XIASS 代理" {
			t.Fatalf("%s lost upstream name: %q", model.Name, model.UpstreamName)
		}
		if len(model.AccountIDs) != 2 || model.AccountIDs[0] != "first" || model.AccountIDs[1] != "second" {
			t.Fatalf("%s lost bound accounts: %#v", model.Name, model.AccountIDs)
		}
		if model.APIKey != "" {
			t.Fatalf("%s copied the primary account credential", model.Name)
		}
	}
}

func TestSyncUpstreamAccountModelsMergesMatchingAccountsWithoutDuplicates(t *testing.T) {
	storage.Init(t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-pool-one"},{"id":"gpt-pool-two"}]}`))
	}))
	defer server.Close()

	for _, account := range []storage.UpstreamAccount{
		{ID: "pool-first", Name: "pool-first", Provider: "openai", Type: "api_key", APIURL: server.URL, APIKey: "first-token", AuthMode: "bearer", Enabled: true, Priority: 10, MaxConcurrency: 1},
		{ID: "pool-second", Name: "pool-second", Provider: "openai", Type: "api_key", APIURL: server.URL, APIKey: "second-token", AuthMode: "bearer", Enabled: true, Priority: 10, MaxConcurrency: 1},
	} {
		if err := storage.SaveUpstreamAccount(account); err != nil {
			t.Fatal(err)
		}
	}

	app := &App{}
	first := app.SyncUpstreamAccountModels("pool-first")
	if !first.OK || first.Added != 2 || first.Bound != 0 {
		t.Fatalf("first account sync = %+v", first)
	}
	second := app.SyncUpstreamAccountModels("pool-second")
	if !second.OK || second.Added != 0 || second.Bound != 2 {
		t.Fatalf("second account sync did not merge into pool: %+v", second)
	}
	again := app.SyncUpstreamAccountModels("pool-second")
	if !again.OK || again.Added != 0 || again.Bound != 0 || again.Unchanged != 2 {
		t.Fatalf("repeat sync created changes: %+v", again)
	}

	models, err := storage.LoadModels()
	if err != nil || len(models) != 2 {
		t.Fatalf("synced models = %#v, %v", models, err)
	}
	for _, model := range models {
		if len(model.AccountIDs) != 2 || model.AccountIDs[0] != "pool-first" || model.AccountIDs[1] != "pool-second" {
			t.Fatalf("%s account pool = %#v", model.ExternalModelName, model.AccountIDs)
		}
		if model.APIKey != "" {
			t.Fatalf("%s copied an account credential into model storage", model.ExternalModelName)
		}
	}
}

func TestSyncUpstreamAccountModelsRejectsStaleAccountChange(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		mutate func(t *testing.T, accountID string)
	}{
		{
			name: "deleted",
			mutate: func(t *testing.T, accountID string) {
				t.Helper()
				if err := storage.DeleteUpstreamAccount(accountID); err != nil {
					t.Fatalf("delete account during discovery: %v", err)
				}
			},
		},
		{
			name: "route changed",
			mutate: func(t *testing.T, accountID string) {
				t.Helper()
				account, err := storage.GetUpstreamAccount(accountID)
				if err != nil {
					t.Fatalf("load account during discovery: %v", err)
				}
				account.APIURL = "https://replacement.example.test"
				if err := storage.SaveUpstreamAccount(account); err != nil {
					t.Fatalf("change account route during discovery: %v", err)
				}
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			storage.Init(t.TempDir())
			started := make(chan struct{})
			release := make(chan struct{})
			var startedOnce sync.Once
			var releaseOnce sync.Once
			releaseDiscovery := func() { releaseOnce.Do(func() { close(release) }) }
			defer releaseDiscovery()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/models" {
					http.NotFound(w, r)
					return
				}
				startedOnce.Do(func() { close(started) })
				<-release
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"data":[{"id":"stale-model"}]}`))
			}))
			defer server.Close()

			const accountID = "sync-target"
			if err := storage.SaveUpstreamAccount(storage.UpstreamAccount{
				ID: accountID, Name: accountID, Provider: "openai", Type: "api_key", APIURL: server.URL,
				APIKey: "sync-token", AuthMode: "bearer", Enabled: true, MaxConcurrency: 1,
			}); err != nil {
				t.Fatal(err)
			}

			app := &App{}
			results := make(chan BatchModelResult, 1)
			go func() { results <- app.SyncUpstreamAccountModels(accountID) }()
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("model discovery did not reach the upstream")
			}
			testCase.mutate(t, accountID)
			releaseDiscovery()

			var result BatchModelResult
			select {
			case result = <-results:
			case <-time.After(2 * time.Second):
				t.Fatal("model sync did not complete")
			}
			if result.OK || !strings.Contains(result.Message, "账户在同步期间已删除或连接配置发生变化") {
				t.Fatalf("stale sync result = %+v", result)
			}
			models, err := storage.LoadModels()
			if err != nil || len(models) != 0 {
				t.Fatalf("stale sync wrote models = %#v, %v", models, err)
			}
		})
	}
}

func TestAccountPoolRejectsMixedProviders(t *testing.T) {
	storage.Init(t.TempDir())
	saveModelTestAccount(t, "openai", "openai", "token-one")
	saveModelTestAccount(t, "claude", "anthropic", "token-two")

	app := &App{}
	if _, err := app.resolveUpstreamConfig(upstream.Config{Provider: "openai", AccountIDs: []string{"openai", "claude"}}); err == nil {
		t.Fatal("mixed provider pool was accepted for discovery/test")
	}
	result := app.SaveModel(storage.CustomModel{
		Name: "models/mixed", Provider: "openai", ExternalModelName: "gpt-test", AccountIDs: []string{"openai", "claude"},
	})
	if result.OK {
		t.Fatalf("mixed provider pool was saved: %+v", result)
	}
}
