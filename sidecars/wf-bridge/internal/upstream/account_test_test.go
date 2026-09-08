package upstream

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"antigravity-wf-assistant/internal/storage"
)

func TestRunAccountTestDirectCodexOAuthNeverFallsBackToChat(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/backend-api/codex/responses" {
			t.Fatalf("unexpected direct OAuth path: %s", r.URL.Path)
		}
		if got := r.Header.Get("chatgpt-account-id"); got != "account-test" {
			t.Fatalf("missing ChatGPT account header: %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-oauth-token" {
			t.Fatalf("unexpected OAuth authorization header")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"message":"route unavailable Authorization: Bearer test-oauth-token"}}`))
	}))
	defer server.Close()

	result := RunAccountTest(context.Background(), Config{
		Provider: "openai", APIURL: server.URL + "/backend-api/codex/responses", EndpointMode: "manual",
		APIKey: "test-oauth-token", AuthMode: "bearer", OAuthUpstream: storage.OpenAICodexOAuthUpstream, ChatGPTAccountID: "account-test",
	}, AccountTestRequest{AccountID: "account-id", Model: "gpt-5.6-sol"})

	if result.OK || result.APIStyle != "responses" || result.StatusCode != http.StatusNotFound {
		t.Fatalf("unexpected direct OAuth result: %#v", result)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("direct OAuth request count = %d, want 1; it must not fall back to Chat", got)
	}
	if strings.Contains(accountTestResultText(result), "test-oauth-token") {
		t.Fatalf("detailed result leaked OAuth credential: %#v", result)
	}
}

func TestRunAccountTestOpenAIAutoUsesChatDirectly(t *testing.T) {
	var responsesCalls, chatCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/responses":
			responsesCalls.Add(1)
			http.NotFound(w, r)
		case "/v1/chat/completions":
			chatCalls.Add(1)
			if r.Header.Get("Authorization") != "Bearer api-test-token" {
				t.Fatal("Chat fallback did not preserve configured auth")
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"OK\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result := RunAccountTest(context.Background(), Config{
		Provider: "openai", APIURL: server.URL, APIKey: "api-test-token", AuthMode: "bearer", APIStyle: "auto",
	}, AccountTestRequest{AccountID: "openai-account", Model: "gpt-test", Prompt: "hello"})

	if !result.OK || result.APIStyle != "chat_completions" || result.Content != "OK" {
		t.Fatalf("unexpected fallback result: %#v", result)
	}
	if responsesCalls.Load() != 0 || chatCalls.Load() != 1 {
		t.Fatalf("automatic test calls responses=%d chat=%d, want 0/1", responsesCalls.Load(), chatCalls.Load())
	}
	if strings.Contains(accountTestResultText(result), "Responses") {
		t.Fatalf("automatic Chat test unexpectedly mentioned Responses: %#v", result.Steps)
	}
}

func TestRunAccountTestExplicitResponsesDoesNotFallbackForGateway400(t *testing.T) {
	var responsesCalls, chatCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/responses":
			responsesCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Upstream request failed","type":"upstream_error"}}`))
		case "/v1/chat/completions":
			chatCalls.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"Chat OK\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result := RunAccountTest(context.Background(), Config{
		Provider: "openai", APIURL: server.URL, APIKey: "api-test-token", AuthMode: "bearer", APIStyle: "responses",
	}, AccountTestRequest{AccountID: "openai-account", Model: "gpt-5.6-sol"})

	if result.OK || result.APIStyle != "responses" || result.StatusCode != http.StatusBadRequest {
		t.Fatalf("explicit Responses error was hidden: %#v", result)
	}
	if responsesCalls.Load() != 1 || chatCalls.Load() != 0 {
		t.Fatalf("explicit Responses calls responses=%d chat=%d, want 1/0", responsesCalls.Load(), chatCalls.Load())
	}
}

func TestRunAccountTestOpenAIAutoReportsChatSemantic400(t *testing.T) {
	var chatCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/chat/completions" {
			chatCalls.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"assistant-prefill final message is not supported; last message must be user","type":"invalid_request_error"}}`))
	}))
	defer server.Close()

	result := RunAccountTest(context.Background(), Config{
		Provider: "openai", APIURL: server.URL, APIKey: "api-test-token", AuthMode: "bearer", APIStyle: "auto",
	}, AccountTestRequest{AccountID: "openai-account", Model: "gpt-5.6-sol"})

	if result.OK || result.StatusCode != http.StatusBadRequest || result.APIStyle != "chat_completions" || chatCalls.Load() != 1 {
		t.Fatalf("automatic Chat semantic 400 was not reported: %#v; chat calls=%d", result, chatCalls.Load())
	}
}

func TestRunAccountTestClaudeUsesCompatibleMessagesFallback(t *testing.T) {
	var standardCalls, compatCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("x-api-key"); got != "claude-test-token" {
			t.Fatal("Claude test did not preserve x-api-key auth")
		}
		switch r.URL.Path {
		case "/v1/messages":
			standardCalls.Add(1)
			http.NotFound(w, r)
		case "/v1/chat/messages":
			compatCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"Claude OK"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result := RunAccountTest(context.Background(), Config{
		Provider: "anthropic", APIURL: server.URL, APIKey: "claude-test-token", AuthMode: "x_api_key", MessagePathMode: "auto",
	}, AccountTestRequest{AccountID: "claude-account", Model: "claude-test", Prompt: "hi"})

	if !result.OK || result.APIStyle != "messages" || result.Content != "Claude OK" {
		t.Fatalf("unexpected Claude test result: %#v", result)
	}
	if standardCalls.Load() != 1 || compatCalls.Load() != 1 {
		t.Fatalf("Claude fallback calls standard=%d compat=%d", standardCalls.Load(), compatCalls.Load())
	}
	if !strings.Contains(accountTestResultText(result), "兼容路径重试") {
		t.Fatalf("Claude fallback log is missing: %#v", result.Steps)
	}
}

func TestRunAccountTestOpenAIImageReturnsOnlyValidatedPreview(t *testing.T) {
	png := []byte{0x89, 0x50, 0x4e, 0x47}
	encoded := base64.StdEncoding.EncodeToString(png)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/images/generations" {
			http.NotFound(w, r)
			return
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload["model"] != "gpt-image-2" || payload["prompt"] != "draw a test cat" {
			t.Fatalf("unexpected image test payload: %#v", payload)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"b64_json":"` + encoded + `","revised_prompt":"draw a test cat"}]}`))
	}))
	defer server.Close()

	result := RunAccountTest(context.Background(), Config{
		Provider: "openai", APIURL: server.URL, APIKey: "image-test-token", AuthMode: "bearer",
	}, AccountTestRequest{AccountID: "image-account", Model: "gpt-image-2", Prompt: "draw a test cat"})

	if !result.OK || result.APIStyle != "images" || len(result.Images) != 1 {
		t.Fatalf("unexpected image test result: %#v", result)
	}
	if !strings.HasPrefix(result.Images[0].URL, "data:image/png;base64,") || result.Images[0].MIMEType != "image/png" {
		t.Fatalf("unsafe/unexpected image result: %#v", result.Images[0])
	}
}

func TestAccountTestRecognizesSupportedImageModelNames(t *testing.T) {
	for _, model := range []string{
		"gpt-image-2", "dall-e-3", "imagen-4", "imagegen-v3", "stable-diffusion-xl", "sdxl-turbo", "flux-pro", "midjourney-v7", "models/image-2",
	} {
		if !isAccountTestImageModel(model) {
			t.Errorf("image model %q was not recognised", model)
		}
	}
	for _, model := range []string{"gpt-5.6-sol", "claude-opus-5", "text-embedding-3-large", ""} {
		if isAccountTestImageModel(model) {
			t.Errorf("non-image model %q was misclassified", model)
		}
	}
}

func TestAccountTestResponseRedactsSecretsAndRejectsUnsafeImageURLs(t *testing.T) {
	result := parseAccountTestResponse([]byte(`{"choices":[{"message":{"content":"Authorization: Bearer super-secret-token-value"}}]}`))
	if strings.Contains(result.Text, "super-secret-token-value") || !strings.Contains(result.Text, "[已隐藏]") {
		t.Fatalf("text response was not redacted: %q", result.Text)
	}
	if image := safeAccountTestRemoteImage("javascript:alert(1)", "image/png"); image.URL != "" {
		t.Fatalf("unsafe URL was accepted: %#v", image)
	}
	if image := dataURLFromAccountTestBase64("text/html", base64.StdEncoding.EncodeToString([]byte("no"))); image.URL != "" {
		t.Fatalf("unsafe MIME was accepted: %#v", image)
	}
}

func TestRunAccountTestRedactsBareConfiguredCredentialValues(t *testing.T) {
	const apiKey = "account-test-bare-api-secret"
	const customHeaderValue = "account-test-header-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"gateway echoed ` + apiKey + ` and ` + customHeaderValue + `"}}`))
	}))
	defer server.Close()

	result := RunAccountTest(context.Background(), Config{
		Provider: "openai", APIURL: server.URL, APIKey: apiKey, AuthMode: "custom_header", AuthHeader: "X-Token",
		Headers: map[string]string{"X-Workspace-Secret": customHeaderValue},
	}, AccountTestRequest{AccountID: "redaction-account", Model: "gpt-test"})
	if result.OK || result.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unexpected account test result: %#v", result)
	}
	text := accountTestResultText(result)
	for _, secret := range []string{apiKey, customHeaderValue} {
		if strings.Contains(text, secret) {
			t.Fatalf("account test leaked configured credential %q: %#v", secret, result)
		}
	}
	if !strings.Contains(text, "鉴权详情已隐藏") {
		t.Fatalf("account test did not report a safe redaction: %#v", result)
	}
}

func accountTestResultText(result AccountTestResult) string {
	parts := []string{result.Message, result.Content, result.Endpoint}
	for _, step := range result.Steps {
		parts = append(parts, step.Text)
	}
	return strings.Join(parts, "\n")
}
