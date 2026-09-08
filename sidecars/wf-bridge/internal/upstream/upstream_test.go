package upstream

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"antigravity-wf-assistant/internal/storage"
)

func TestDiscoverModelsUsesConfiguredAuth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		if got := r.URL.Query().Get("client_version"); got != "" {
			t.Errorf("ordinary API model discovery unexpectedly sent client_version = %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer token" {
			t.Errorf("authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-a"},{"id":"claude-b"}]}`))
	}))
	defer server.Close()

	result := DiscoverModels(context.Background(), Config{Provider: "openai", APIURL: server.URL + "/v1", APIKey: "token", AuthMode: "bearer"})
	if !result.OK || len(result.Models) != 2 || result.Models[0].ID != "claude-b" {
		t.Fatalf("unexpected discovery result: %#v", result)
	}
}

func TestDiscoverModelsRedactsUpstreamCredentialEchoes(t *testing.T) {
	const bearerSecret = "oauth-bearer-secret-value"
	const apiKeySecret = "api-key-secret-value"
	const xAPIKeySecret = "x-api-key-secret-value"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"request rejected: Authorization: Bearer ` + bearerSecret + `; api_key=` + apiKeySecret + `; x-api-key: ` + xAPIKeySecret + `"}}`))
	}))
	defer server.Close()

	result := DiscoverModels(context.Background(), Config{Provider: "openai", APIURL: server.URL + "/v1", APIKey: "configured-token", AuthMode: "bearer"})
	if result.OK || result.StatusCode != http.StatusBadRequest {
		t.Fatalf("unexpected discovery result: %#v", result)
	}
	for _, secret := range []string{bearerSecret, apiKeySecret, xAPIKeySecret} {
		if strings.Contains(result.Message, secret) {
			t.Fatalf("discovery error leaked credential %q: %q", secret, result.Message)
		}
	}
	if !strings.Contains(result.Message, "HTTP 400") || !strings.Contains(result.Message, "鉴权详情已隐藏") {
		t.Fatalf("discovery error did not retain a safe diagnostic: %q", result.Message)
	}
}

func TestDiscoverModelsRedactsBareConfiguredCredentialValues(t *testing.T) {
	const apiKey = "bare-configured-api-secret"
	const customHeaderValue = "bare-configured-header-secret"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"gateway echo: ` + apiKey + ` / ` + customHeaderValue + `"}}`))
	}))
	defer server.Close()

	result := DiscoverModels(context.Background(), Config{
		Provider: "openai", APIURL: server.URL + "/v1", APIKey: apiKey, AuthMode: "bearer",
		Headers: map[string]string{"X-Tenant-Secret": customHeaderValue},
	})
	if result.OK || result.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unexpected discovery result: %#v", result)
	}
	for _, secret := range []string{apiKey, customHeaderValue} {
		if strings.Contains(result.Message, secret) {
			t.Fatalf("bare configured credential leaked %q: %q", secret, result.Message)
		}
	}
	if !strings.Contains(result.Message, "鉴权详情已隐藏") {
		t.Fatalf("bare credential echo was not redacted: %q", result.Message)
	}
}

func TestOpenAICodexOAuthDiscoveryUsesDirectManifestAndCodexHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/codex/models" {
			http.NotFound(w, r)
			return
		}
		if got, want := r.URL.Query().Get("client_version"), openAICodexVersion; got != want {
			t.Errorf("client_version = %q, want %q", got, want)
		}
		if got, want := r.Header.Get("Authorization"), "Bearer oauth-access-token"; got != want {
			t.Errorf("authorization = %q, want %q", got, want)
		}
		if got, want := r.Header.Get("chatgpt-account-id"), "acct-chatgpt"; got != want {
			t.Errorf("chatgpt-account-id = %q, want %q", got, want)
		}
		if got, want := r.Header.Get("Originator"), "codex_cli_rs"; got != want {
			t.Errorf("originator = %q, want %q", got, want)
		}
		if got, want := r.Header.Get("Version"), openAICodexVersion; got != want {
			t.Errorf("version = %q, want %q", got, want)
		}
		if got, want := r.Header.Get("User-Agent"), openAICodexUserAgent; got != want {
			t.Errorf("user-agent = %q, want %q", got, want)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[{"slug":"gpt-5.4","display_name":"GPT-5.4"},{"slug":"gpt-5.4-mini","display_name":"GPT-5.4 Mini"}]}`))
	}))
	defer server.Close()

	result := DiscoverModels(context.Background(), Config{
		Provider: "openai", APIURL: server.URL + "/backend-api/codex/responses", APIKey: "oauth-access-token",
		EndpointMode: "manual", APIStyle: "responses", AuthMode: "bearer",
		OAuthUpstream: storage.OpenAICodexOAuthUpstream, ChatGPTAccountID: "acct-chatgpt",
	})
	if !result.OK || len(result.Models) != 2 || result.Endpoint != server.URL+"/backend-api/codex/models?client_version="+openAICodexVersion {
		t.Fatalf("unexpected direct OAuth discovery result: %#v", result)
	}
	if got, want := result.Models[0], (ModelInfo{ID: "gpt-5.4", Name: "GPT-5.4"}); got != want {
		t.Errorf("first manifest model = %#v, want %#v", got, want)
	}
	if got, want := result.Models[1], (ModelInfo{ID: "gpt-5.4-mini", Name: "GPT-5.4 Mini"}); got != want {
		t.Errorf("second manifest model = %#v, want %#v", got, want)
	}
}

func TestOpenAICodexOAuthQuotaUsesWHAMWithoutConfiguredQuotaURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/wham/usage" {
			http.NotFound(w, r)
			return
		}
		if got, want := r.Header.Get("Authorization"), "Bearer oauth-access-token"; got != want {
			t.Errorf("authorization = %q, want %q", got, want)
		}
		if got, want := r.Header.Get("chatgpt-account-id"), "acct-chatgpt"; got != want {
			t.Errorf("chatgpt-account-id = %q, want %q", got, want)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"email":"person@example.test", "account_id":"acct-chatgpt", "plan_type":"free",
			"rate_limit":{"allowed":true,"limit_reached":false,
			"primary_window":{"used_percent":12.5,"limit_window_seconds":18000,"reset_after_seconds":120},
			"secondary_window":{"used_percent":44,"limit_window_seconds":604800,"reset_after_seconds":3600}}
		}`))
	}))
	defer server.Close()

	result := FetchQuota(context.Background(), Config{
		Provider: "openai", APIURL: server.URL + "/backend-api/codex/responses", APIKey: "oauth-access-token",
		EndpointMode: "manual", APIStyle: "responses", AuthMode: "bearer",
		OAuthUpstream: storage.OpenAICodexOAuthUpstream, ChatGPTAccountID: "acct-chatgpt",
	}, "")
	if !result.OK || result.Endpoint != server.URL+"/backend-api/wham/usage" {
		t.Fatalf("unexpected direct OAuth quota result: %#v", result)
	}
	if got, want := result.Snapshot.Plan, "free"; got != want {
		t.Fatalf("plan = %q, want %q", got, want)
	}
	if len(result.Snapshot.Windows) != 2 || result.Snapshot.Windows[0].Label != "5h" || result.Snapshot.Windows[1].Label != "7d" {
		t.Fatalf("quota windows = %#v, want 5h and 7d", result.Snapshot.Windows)
	}
}

func TestAutoModelTestUsesChatWithoutProbingResponses(t *testing.T) {
	var responsesCalls, chatCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/responses":
			responsesCalls.Add(1)
			http.NotFound(w, r)
		case "/v1/chat/completions":
			chatCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"chatcmpl-test"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result := TestModel(context.Background(), Config{Provider: "openai", APIURL: server.URL + "/v1", APIKey: "token", APIStyle: "auto"}, "gpt-test")
	if !result.OK || result.APIStyle != "chat_completions" {
		t.Fatalf("expected automatic Chat test, got %#v", result)
	}
	if responsesCalls.Load() != 0 || chatCalls.Load() != 1 {
		t.Fatalf("automatic model test calls responses=%d chat=%d, want 0/1", responsesCalls.Load(), chatCalls.Load())
	}
}

func TestExplicitResponsesModelTestDoesNotFallbackForGenericGateway400(t *testing.T) {
	var chatCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/responses":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Upstream request failed"}}`))
		case "/v1/chat/completions":
			chatCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"chatcmpl-test"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result := TestModel(context.Background(), Config{Provider: "openai", APIURL: server.URL + "/v1", APIKey: "token", APIStyle: "responses"}, "gpt-5.6-sol")
	if result.OK || result.APIStyle != "responses" || result.StatusCode != http.StatusBadRequest || chatCalls.Load() != 0 {
		t.Fatalf("explicit Responses error was hidden by Chat fallback: %#v", result)
	}
}

func TestCanFallbackToChatResponseKeepsSemantic400Visible(t *testing.T) {
	if !CanFallbackToChatResponse(http.StatusBadRequest, `{"error":{"message":"Upstream request failed"}}`) {
		t.Fatal("generic compatibility-gateway 400 was not recognised")
	}
	if CanFallbackToChatResponse(http.StatusBadRequest, `{"error":{"message":"assistant-prefill final message is not supported; last message must be user"}}`) {
		t.Fatal("semantic validation 400 was incorrectly classified as endpoint incompatibility")
	}
	if CanFallbackToChatResponse(http.StatusUnauthorized, `{"error":{"message":"Upstream request failed"}}`) {
		t.Fatal("authentication failure must never switch API contracts")
	}
}

func TestCustomHeaderAuthentication(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "https://example.com/v1/models", nil)
	err := ApplyCredentials(request, Config{
		Provider: "custom", APIURL: "https://example.com/v1", APIKey: "secret", AuthMode: "custom_header", AuthHeader: "X-Token",
		Headers: map[string]string{"X-Client": "WF"},
	})
	if err != nil {
		t.Fatalf("ApplyCredentials failed: %v", err)
	}
	if request.Header.Get("X-Token") != "secret" || request.Header.Get("X-Client") != "WF" {
		t.Fatalf("headers not applied: %#v", request.Header)
	}
}

func TestManualEndpointIsPreservedExactly(t *testing.T) {
	config := Config{
		Provider:     "openai",
		APIURL:       "https://gateway.example.com/custom/chat?workspace=wf",
		EndpointMode: "manual",
	}

	for name, resolve := range map[string]func(Config) (string, error){
		"chat":      ResolveChatCompletionsURLForConfig,
		"responses": ResolveResponsesURLForConfig,
	} {
		got, err := resolve(config)
		if err != nil {
			t.Fatalf("%s resolver returned error: %v", name, err)
		}
		if want := config.APIURL; got != want {
			t.Errorf("%s manual endpoint = %q, want %q", name, got, want)
		}
	}
}

func TestResolveImagesGenerationsURLUsesDedicatedSiblingRoute(t *testing.T) {
	for name, config := range map[string]Config{
		"base":          {Provider: "openai", APIURL: "https://gateway.example.com"},
		"chat endpoint": {Provider: "openai", APIURL: "https://gateway.example.com/v1/chat/completions"},
		"responses":     {Provider: "openai", APIURL: "https://gateway.example.com/v1/responses", EndpointMode: "manual"},
	} {
		got, err := ResolveImagesGenerationsURLForConfig(config)
		if err != nil {
			t.Fatalf("%s resolver returned error: %v", name, err)
		}
		if want := "https://gateway.example.com/v1/images/generations"; got != want {
			t.Errorf("%s image endpoint = %q, want %q", name, got, want)
		}
	}

	manual := Config{Provider: "openai", APIURL: "https://gateway.example.com/custom/images/generations?tenant=wf", EndpointMode: "manual"}
	got, err := ResolveImagesGenerationsURLForConfig(manual)
	if err != nil {
		t.Fatal(err)
	}
	if got != manual.APIURL {
		t.Fatalf("explicit manual image endpoint = %q, want %q", got, manual.APIURL)
	}
}

func TestAnthropicEndpointModes(t *testing.T) {
	base := Config{Provider: "anthropic", APIURL: "https://api.example.com"}

	auto, err := ResolveAnthropicMessageCandidates(base)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"https://api.example.com/v1/messages", "https://api.example.com/v1/chat/messages"}; !reflect.DeepEqual(auto, want) {
		t.Fatalf("automatic Claude candidates = %#v, want %#v", auto, want)
	}

	compat, err := ResolveAnthropicMessageCandidates(Config{Provider: "anthropic", APIURL: base.APIURL, MessagePathMode: "compat"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"https://api.example.com/v1/chat/messages"}; !reflect.DeepEqual(compat, want) {
		t.Fatalf("compatible Claude candidates = %#v, want %#v", compat, want)
	}

	manualURL := "https://api.example.com/claude/messages-v2?tenant=wf"
	manual, err := ResolveAnthropicMessageCandidates(Config{Provider: "anthropic", APIURL: manualURL, EndpointMode: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{manualURL}; !reflect.DeepEqual(manual, want) {
		t.Fatalf("manual Claude candidates = %#v, want %#v", manual, want)
	}
}
