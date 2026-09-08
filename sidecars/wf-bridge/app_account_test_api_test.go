package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"antigravity-wf-assistant/internal/storage"
	"antigravity-wf-assistant/internal/upstream"
)

func TestAccountCardDiscoveryAndDetailedTestBypassPoolEligibility(t *testing.T) {
	storage.Init(t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"gpt-card-test"}]}`))
		case "/v1/chat/completions":
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"account card OK"}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	account := storage.UpstreamAccount{
		ID: "paused-card-account", Name: "paused-card-account", Provider: "openai", Type: "api_key",
		APIURL: server.URL, APIKey: "test-account-token", AuthMode: "bearer", APIStyle: "chat_completions",
		Enabled: false, Priority: 10, MaxConcurrency: 1,
	}
	if err := storage.SaveUpstreamAccount(account); err != nil {
		t.Fatal(err)
	}
	app := &App{}

	models := app.DiscoverAccountModels(account.ID)
	if !models.OK || len(models.Models) != 1 || models.Models[0].ID != "gpt-card-test" {
		t.Fatalf("explicit account discovery failed for paused account: %#v", models)
	}
	result := app.TestUpstreamAccountDetailed(upstream.AccountTestRequest{
		AccountID: account.ID, Model: "gpt-card-test", Prompt: "hi", Mode: "default",
	})
	if !result.OK || result.Content != "account card OK" {
		t.Fatalf("explicit detailed test failed for paused account: %#v", result)
	}
}

func TestCancelDetailedAccountTestStopsInFlightRequestWithoutLeakingCredential(t *testing.T) {
	storage.Init(t.TempDir())
	requestStarted := make(chan struct{})
	requestCancelled := make(chan struct{})
	releaseServer := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		close(requestStarted)
		select {
		case <-r.Context().Done():
			close(requestCancelled)
		case <-releaseServer:
		}
	}))
	defer server.Close()
	defer close(releaseServer)

	const credential = "test-account-cancellation-secret"
	const requestID = "card-test-cancel-1"
	account := storage.UpstreamAccount{
		ID: "cancel-test-account", Name: "cancel-test-account", Provider: "openai", Type: "api_key",
		APIURL: server.URL, APIKey: credential, AuthMode: "bearer", APIStyle: "chat_completions",
		Enabled: true, Priority: 10, MaxConcurrency: 1,
	}
	if err := storage.SaveUpstreamAccount(account); err != nil {
		t.Fatal(err)
	}

	app := &App{}
	results := make(chan upstream.AccountTestResult, 1)
	go func() {
		results <- app.TestUpstreamAccountDetailed(upstream.AccountTestRequest{
			AccountID: account.ID, RequestID: requestID, Model: "gpt-card-test", Prompt: "hi",
		})
	}()
	select {
	case <-requestStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("detailed test did not reach the upstream server")
	}

	cancelled := app.CancelUpstreamAccountTest(requestID)
	if !cancelled.OK {
		t.Fatalf("cancel result = %#v", cancelled)
	}
	var result upstream.AccountTestResult
	select {
	case result = <-results:
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled detailed test did not return promptly")
	}
	if result.OK || result.RequestID != requestID || !strings.Contains(result.Message, "测试已取消") {
		t.Fatalf("unexpected cancellation result: %#v", result)
	}
	if strings.Contains(detailedAccountTestResultText(result), credential) {
		t.Fatalf("cancelled result leaked credential: %#v", result)
	}
	select {
	case <-requestCancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream request context was not cancelled")
	}
	if again := app.CancelUpstreamAccountTest(requestID); again.OK {
		t.Fatalf("completed request remained cancellable: %#v", again)
	}
}

func detailedAccountTestResultText(result upstream.AccountTestResult) string {
	parts := []string{result.Message, result.Content, result.Endpoint}
	for _, step := range result.Steps {
		parts = append(parts, step.Text)
	}
	return strings.Join(parts, "\n")
}

func TestDuplicateDetailedAccountTestRequestIDNeverReplacesActiveCancellation(t *testing.T) {
	storage.Init(t.TempDir())
	requestStarted := make(chan struct{})
	releaseServer := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		close(requestStarted)
		select {
		case <-r.Context().Done():
		case <-releaseServer:
		}
	}))
	defer server.Close()
	defer close(releaseServer)

	account := storage.UpstreamAccount{
		ID: "duplicate-test-account", Name: "duplicate-test-account", Provider: "openai", Type: "api_key",
		APIURL: server.URL, APIKey: "duplicate-test-token", AuthMode: "bearer", APIStyle: "chat_completions",
		Enabled: true, Priority: 10, MaxConcurrency: 1,
	}
	if err := storage.SaveUpstreamAccount(account); err != nil {
		t.Fatal(err)
	}

	app := &App{}
	const requestID = "same-request-id"
	firstResult := make(chan upstream.AccountTestResult, 1)
	go func() {
		firstResult <- app.TestUpstreamAccountDetailed(upstream.AccountTestRequest{
			AccountID: account.ID, RequestID: requestID, Model: "gpt-card-test",
		})
	}()
	select {
	case <-requestStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first detailed test did not reach upstream")
	}

	duplicate := app.TestUpstreamAccountDetailed(upstream.AccountTestRequest{
		AccountID: account.ID, RequestID: requestID, Model: "gpt-card-test",
	})
	if duplicate.OK || !strings.Contains(duplicate.Message, "同一测试请求") {
		t.Fatalf("duplicate request replaced active test: %#v", duplicate)
	}
	select {
	case result := <-firstResult:
		t.Fatalf("duplicate request unexpectedly stopped original test: %#v", result)
	default:
	}

	if cancelled := app.CancelUpstreamAccountTest(requestID); !cancelled.OK {
		t.Fatalf("active original test was not cancellable: %#v", cancelled)
	}
	select {
	case result := <-firstResult:
		if result.OK || !strings.Contains(result.Message, "测试已取消") {
			t.Fatalf("original test did not receive cancellation: %#v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("original test did not return after cancellation")
	}
}
