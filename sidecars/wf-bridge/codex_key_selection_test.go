package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"antigravity-wf-assistant/internal/codexconfig"
	"antigravity-wf-assistant/internal/codexselection"
)

func TestCodexXIASSSelectionLifecycleUsesNativeCredentialAndConsumesOnlyOnSuccess(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	service := codexselection.New()
	t.Cleanup(service.Close)
	app := &App{
		ctx:                 context.Background(),
		codexKeySelection:   service,
		codexDesktopControl: newCodexLifecycleFakeDesktop(false),
	}
	started, err := service.Begin("https://gateway.example.test")
	if err != nil {
		t.Fatal(err)
	}
	callback, state := selectionCallbackAndState(t, started.ConnectURL)
	secret := "sk-native-lifecycle-selection-secret"
	completedURL := selectionCallbackURL(callback, state, "https://gateway.example.test/v1", secret, "Lifecycle Key")
	completed := app.CompleteCodexXIASSKeySelectionManual(started.State.SessionID, completedURL)
	if !completed.OK || completed.Selection.Status != "ready" {
		t.Fatalf("manual selection completion = %+v", completed)
	}

	status := app.ApplyCodexXIASSSelectionWithLifecycle(started.State.SessionID, CodexConfigurationLifecycleInput{
		Config: codexconfig.ApplyConfig{
			BaseURL:     "https://renderer-controlled.example.test/v1",
			APIKey:      "renderer-supplied-secret",
			KeyName:     "renderer-supplied-key-name",
			Model:       "gpt-5.6-sol",
			ReviewModel: "gpt-5.6-sol",
		},
	})
	if !status.OK || !status.Applied {
		t.Fatalf("ApplyCodexXIASSSelectionWithLifecycle() = %+v", status)
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{secret, "renderer-supplied-secret", "renderer-controlled.example.test", home, "config.toml", started.ConnectURL, completedURL, started.State.SessionID, state} {
		if bytes.Contains(encoded, []byte(forbidden)) {
			t.Fatalf("lifecycle status leaked %q: %s", forbidden, encoded)
		}
	}
	written, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{secret, `base_url = "https://gateway.example.test/v1"`, `name = "Lifecycle Key"`} {
		if !strings.Contains(string(written), want) {
			t.Fatalf("written Codex config does not contain %q:\n%s", want, written)
		}
	}
	for _, forbidden := range []string{"renderer-supplied-secret", "renderer-controlled.example.test", "renderer-supplied-key-name"} {
		if strings.Contains(string(written), forbidden) {
			t.Fatalf("renderer-supplied credential field was not ignored: %q in\n%s", forbidden, written)
		}
	}
	if state := app.GetCodexXIASSKeySelectionStatus(started.State.SessionID); state.Selection.Status != "expired" {
		t.Fatalf("successful lifecycle apply did not consume selection: %+v", state)
	}
	if repeated := app.ApplyCodexXIASSSelectionWithLifecycle(started.State.SessionID, CodexConfigurationLifecycleInput{Config: codexconfig.ApplyConfig{Model: "gpt-5.6-sol"}}); repeated.OK {
		t.Fatalf("consumed lifecycle selection unexpectedly applied again: %+v", repeated)
	}
}

func TestCodexXIASSSelectionLifecycleKeepsCredentialForExplicitRetryAfterFailure(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	service := codexselection.New()
	t.Cleanup(service.Close)
	app := &App{
		ctx:                 context.Background(),
		codexKeySelection:   service,
		codexDesktopControl: newCodexLifecycleFakeDesktop(true),
	}
	started, err := service.Begin("https://gateway.example.test")
	if err != nil {
		t.Fatal(err)
	}
	callback, state := selectionCallbackAndState(t, started.ConnectURL)
	secret := "sk-retryable-selection-secret"
	completedURL := selectionCallbackURL(callback, state, "https://gateway.example.test/v1", secret, "Retry Key")
	completed := app.CompleteCodexXIASSKeySelectionManual(started.State.SessionID, completedURL)
	if !completed.OK {
		t.Fatalf("manual selection completion = %+v", completed)
	}

	status := app.ApplyCodexXIASSSelectionWithLifecycle(started.State.SessionID, CodexConfigurationLifecycleInput{
		Config: codexconfig.ApplyConfig{Model: "gpt-5.6-sol"},
		// No lifecycle confirmation: a running desktop must remain untouched.
	})
	if status.OK || status.Applied || status.DesktopStopped {
		t.Fatalf("unconfirmed lifecycle selection unexpectedly changed state: %+v", status)
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{secret, home, "config.toml", started.ConnectURL, completedURL, started.State.SessionID, state} {
		if bytes.Contains(encoded, []byte(forbidden)) {
			t.Fatalf("failed lifecycle status leaked %q: %s", forbidden, encoded)
		}
	}
	if state := app.GetCodexXIASSKeySelectionStatus(started.State.SessionID); state.Selection.Status != "ready" {
		t.Fatalf("failed lifecycle apply consumed retryable selection: %+v", state)
	}
	if _, err := os.Stat(filepath.Join(home, "config.toml")); !os.IsNotExist(err) {
		t.Fatalf("unconfirmed lifecycle selection wrote config.toml: %v", err)
	}
}

func TestCodexXIASSSelectionAppliesNativeOnlyKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	service := codexselection.New()
	t.Cleanup(service.Close)
	app := &App{codexKeySelection: service}
	started, err := service.Begin("https://gateway.example.test")
	if err != nil {
		t.Fatal(err)
	}
	callback, state := selectionCallbackAndState(t, started.ConnectURL)
	secret := "sk-native-selection-secret"
	completed := app.CompleteCodexXIASSKeySelectionManual(started.State.SessionID, selectionCallbackURL(callback, state, "https://gateway.example.test/v1", secret, "Primary Key"))
	if !completed.OK || completed.Selection.Status != "ready" {
		t.Fatalf("manual selection completion = %+v", completed)
	}

	status := app.ApplyCodexXIASSSelection(started.State.SessionID, codexconfig.ApplyConfig{
		ProviderName: "Project gateway",
		Model:        "gpt-5.6-sol",
		ReviewModel:  "gpt-5.6-sol",
		WebSearch:    "cached",
	})
	if !status.OK {
		t.Fatalf("ApplyCodexXIASSSelection() = %+v", status)
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(secret)) {
		t.Fatalf("configuration status leaked selection API key: %s", encoded)
	}
	written, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{secret, `base_url = "https://gateway.example.test/v1"`, `name = "Project gateway"`, `web_search = "cached"`} {
		if !strings.Contains(string(written), want) {
			t.Fatalf("written Codex config does not contain %q:\n%s", want, written)
		}
	}
	if repeated := app.ApplyCodexXIASSSelection(started.State.SessionID, codexconfig.ApplyConfig{Model: "gpt-5.6-sol"}); repeated.OK {
		t.Fatalf("consumed selection unexpectedly applied again: %+v", repeated)
	}
}

func TestExitCleanupClosesCodexKeySelectionListener(t *testing.T) {
	service := codexselection.New()
	started, err := service.Begin("https://gateway.example.test")
	if err != nil {
		t.Fatal(err)
	}
	callback, _ := selectionCallbackAndState(t, started.ConnectURL)
	app := &App{codexKeySelection: service}
	app.releaseExitResources()
	connection, err := net.DialTimeout("tcp", callback.Host, 200*time.Millisecond)
	if err == nil {
		connection.Close()
		t.Fatalf("Codex selection listener %s still accepts connections after exit cleanup", callback.Host)
	}
}

func selectionCallbackAndState(t *testing.T, connectURL string) (*url.URL, string) {
	t.Helper()
	parsed, err := url.Parse(connectURL)
	if err != nil {
		t.Fatal(err)
	}
	callback, err := url.Parse(parsed.Query().Get("callback"))
	if err != nil {
		t.Fatal(err)
	}
	state := parsed.Query().Get("state")
	if state == "" {
		t.Fatal("missing selection callback state")
	}
	return callback, state
}

func selectionCallbackURL(callback *url.URL, state, baseURL, apiKey, keyName string) string {
	payload, err := json.Marshal(map[string]string{"base_url": baseURL, "api_key": apiKey, "key_name": keyName})
	if err != nil {
		panic(err)
	}
	result := *callback
	result.Fragment = url.Values{
		"state":   []string{state},
		"payload": []string{base64.RawURLEncoding.EncodeToString(payload)},
	}.Encode()
	return result.String()
}
