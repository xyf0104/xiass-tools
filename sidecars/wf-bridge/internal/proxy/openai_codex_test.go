package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"antigravity-wf-assistant/internal/storage"
)

func TestNormalizeOpenAICodexResponsesRequest(t *testing.T) {
	base := map[string]any{
		"store":             true,
		"stream":            false,
		"max_output_tokens": float64(8192),
		"temperature":       float64(0.7),
		"top_p":             float64(0.9),
		"stream_options":    map[string]any{"include_usage": true},
		"tools": []map[string]any{
			{"type": "function", "name": "command_status"},
			{"type": responseWebSearchTool},
			{"type": responseImageGenerationTool},
		},
		"input": []any{
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "input_text", "text": "看图并搜索"},
				map[string]any{"type": "input_image", "image_url": "data:image/png;base64,aGVsbG8="},
			}},
			map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "旧回复"}}},
			map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "预填内容"}}},
		},
	}

	got := normalizeOpenAICodexResponsesRequest(base)
	if got["store"] != false || got["stream"] != true {
		t.Fatalf("Codex stream/store contract = %#v", got)
	}
	for _, key := range []string{"max_output_tokens", "temperature", "top_p", "stream_options"} {
		if _, exists := got[key]; exists {
			t.Fatalf("Codex request still contains %q: %#v", key, got)
		}
	}
	if _, exists := base["max_output_tokens"]; !exists || base["store"] != true {
		t.Fatalf("normalizer mutated reusable base request: %#v", base)
	}

	input, _ := got["input"].([]any)
	if len(input) != 1 || responseInputRole(input[len(input)-1]) != "user" {
		t.Fatalf("terminal assistant prefill was not removed: %#v", input)
	}
	content, _ := input[0].(map[string]any)["content"].([]any)
	if len(content) != 2 || responseInputBlockType(content[1]) != "input_image" {
		t.Fatalf("image input was changed while removing prefill: %#v", input)
	}
	tools, _ := got["tools"].([]map[string]any)
	if len(tools) != 3 || tools[0]["type"] != "function" || tools[1]["type"] != responseWebSearchTool || tools[2]["type"] != responseImageGenerationTool {
		t.Fatalf("function/web/image tools were changed: %#v", tools)
	}
}

func TestNormalizeOpenAICodexResponsesRequestAddsUserForOnlyPrefill(t *testing.T) {
	got := normalizeOpenAICodexResponsesRequest(map[string]any{
		"input": []any{map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "继续"}}}},
	})
	input, _ := got["input"].([]any)
	if len(input) != 1 || responseInputRole(input[0]) != "user" || responseInputBlockType(responseInputContent(input[0])[0]) != "input_text" {
		t.Fatalf("only assistant prefill did not become a legal empty user input: %#v", input)
	}
}

func TestIsOpenAICodexOAuthModelSeesBoundAccountBeforeScheduling(t *testing.T) {
	storage.Init(t.TempDir())
	if err := storage.SaveUpstreamAccount(storage.UpstreamAccount{
		ID: "codex-account", Provider: "openai", Type: "oauth", Enabled: true,
		APIURL: "http://127.0.0.1:9876/backend-api/codex/responses", EndpointMode: "manual", APIKey: "test-token",
		OAuth: storage.OAuthConfiguration{Upstream: storage.OpenAICodexOAuthUpstream},
	}); err != nil {
		t.Fatalf("save Codex account: %v", err)
	}
	if !isOpenAICodexOAuthModel(&storage.CustomModel{Provider: "openai", AccountIDs: []string{"codex-account"}, APIStyle: "auto"}) {
		t.Fatal("bound Codex OAuth account was not detected before routing")
	}
}

func TestForwardOpenAICodexOAuthUsesResponsesAndPreservesNativeInputs(t *testing.T) {
	var received map[string]any
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/backend-api/codex/responses" {
			t.Errorf("Codex request path = %q, want Responses route", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("OpenAI-Beta"); got != "responses=experimental" {
			t.Errorf("OpenAI-Beta = %q", got)
		}
		if got := r.Header.Get("chatgpt-account-id"); got != "acct-test" {
			t.Errorf("chatgpt-account-id = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"id":"resp-codex","model":"gpt-test","output":[{"type":"message","content":[{"type":"output_text","text":"codex-ok"}]}]}}`+"\n\n")
	}))
	defer server.Close()

	model := &storage.CustomModel{
		Provider: "openai", APIURL: server.URL + "/backend-api/codex/responses", EndpointMode: "manual",
		APIKey: "test-token", ExternalModelName: "gpt-test", APIStyle: "auto",
		RuntimeOAuthUpstream: storage.OpenAICodexOAuthUpstream, RuntimeChatGPTAccountID: "acct-test",
	}
	request := map[string]any{
		"generationConfig": map[string]any{"maxOutputTokens": 222, "temperature": 0.2, "topP": 0.8, "responseModalities": []any{"TEXT", "IMAGE"}},
		"webSearch":        true,
		"contents": []any{
			map[string]any{"role": "user", "parts": []any{
				map[string]any{"text": "请识图并联网"},
				inlinePart("image/png", "aGVsbG8="),
			}},
			map[string]any{"role": "model", "parts": []any{map[string]any{"text": "这里是被错误预填的助手内容"}}},
		},
		"tools": []any{map[string]any{"functionDeclarations": []any{map[string]any{
			"name": "command_status", "parameters": map[string]any{"type": "object", "properties": map[string]any{}},
		}}}},
	}
	recorder := httptest.NewRecorder()
	forwardOpenAI(recorder, httptest.NewRequest(http.MethodPost, "/generate", nil), model, request, "codex-responses")

	if requests != 1 {
		t.Fatalf("Codex upstream requests = %d, want exactly one Responses call", requests)
	}
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "codex-ok") {
		t.Fatalf("downstream response = %d %s", recorder.Code, recorder.Body.String())
	}
	if received["store"] != false || received["stream"] != true {
		t.Fatalf("Codex body store/stream = %#v", received)
	}
	for _, key := range []string{"max_output_tokens", "temperature", "top_p", "stream_options"} {
		if _, exists := received[key]; exists {
			t.Fatalf("Codex body unexpectedly contains %s: %#v", key, received)
		}
	}
	input, _ := received["input"].([]any)
	if len(input) != 1 || responseInputRole(input[len(input)-1]) != "user" {
		t.Fatalf("Codex body kept terminal assistant prefill: %#v", input)
	}
	blocks := responseInputContent(input[0])
	if len(blocks) != 2 || responseInputBlockType(blocks[1]) != "input_image" {
		t.Fatalf("Codex body lost image input: %#v", input)
	}
	if !responseBodyHasTool(received, "function") || !responseBodyHasTool(received, responseWebSearchTool) || !responseBodyHasTool(received, responseImageGenerationTool) {
		t.Fatalf("Codex body lost function/web/image tools: %#v", received["tools"])
	}
}

func TestForwardOpenAIChatGuardReroutesPersistedCodexAccountWithLegacyCapabilities(t *testing.T) {
	storage.Init(t.TempDir())
	if err := storage.SaveUpstreamAccount(storage.UpstreamAccount{
		ID: "codex-guard", Provider: "openai", Type: "oauth", Enabled: true, APIKey: "codex-token",
		APIURL: "https://chatgpt.com/backend-api/codex/responses", EndpointMode: "manual",
		OAuth: storage.OAuthConfiguration{Upstream: storage.OpenAICodexOAuthUpstream},
	}); err != nil {
		t.Fatalf("save Codex account: %v", err)
	}

	var calls int
	var received map[string]any
	originalTransport := http.DefaultTransport
	http.DefaultTransport = modelFetchRoundTripper(func(request *http.Request) (*http.Response, error) {
		calls++
		if request.URL.Host != "chatgpt.com" || request.URL.Path != "/backend-api/codex/responses" {
			t.Errorf("Codex guard request target = %s", request.URL)
		}
		if err := json.NewDecoder(request.Body).Decode(&received); err != nil {
			t.Error(err)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader(
				`data: {"type":"response.completed","response":{"id":"resp-guard","model":"gpt-test","output":[{"type":"message","content":[{"type":"output_text","text":"guard-ok"}]}]}}` + "\n\n",
			)),
			Request: request,
		}, nil
	})
	defer func() { http.DefaultTransport = originalTransport }()

	// This is a legacy saved model: its picker data said Chat-only before a
	// Codex OAuth account was bound. The runtime guard must route and convert
	// it as Responses without losing the current turn's native features.
	model := &storage.CustomModel{
		Provider: "openai", APIURL: "https://gateway.invalid/v1/chat/completions", APIKey: "legacy-key",
		ExternalModelName: "gpt-test", APIStyle: "chat_completions", AccountIDs: []string{"codex-guard"},
		Capabilities: storage.ModelCapabilities{Configured: true, SupportsImages: true, SupportsFiles: true, SupportsToolCalls: true},
	}
	request := map[string]any{
		"generationConfig": map[string]any{"responseModalities": []any{"TEXT", "IMAGE"}},
		"webSearch":        true,
		"contents": []any{
			map[string]any{"role": "user", "parts": []any{map[string]any{"text": "看图并搜索"}, inlinePart("image/png", "aGVsbG8=")}},
			map[string]any{"role": "model", "parts": []any{map[string]any{"text": "unfinished assistant prefill"}}},
		},
		"tools": []any{map[string]any{"functionDeclarations": []any{map[string]any{
			"name": "command_status", "parameters": map[string]any{"type": "object", "properties": map[string]any{}},
		}}}},
	}
	recorder := httptest.NewRecorder()
	forwardOpenAIChat(recorder, httptest.NewRequest(http.MethodPost, "/generate", nil), model, request, "codex-chat-guard")

	if calls != 1 {
		t.Fatalf("Codex guard requests = %d, want one Responses request", calls)
	}
	if _, sentChatShape := received["messages"]; sentChatShape {
		t.Fatalf("Codex account received Chat Completions payload: %#v", received)
	}
	if len(responseInputContent(mustFirstResponseInput(t, received))) != 2 || !responseBodyHasTool(received, responseWebSearchTool) || !responseBodyHasTool(received, responseImageGenerationTool) || !responseBodyHasTool(received, "function") {
		t.Fatalf("legacy Codex model lost native Responses features: %#v", received)
	}
	input, _ := received["input"].([]any)
	if len(input) != 1 || responseInputRole(input[len(input)-1]) != "user" {
		t.Fatalf("legacy Codex model retained terminal assistant prefill: %#v", input)
	}
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "guard-ok") {
		t.Fatalf("Codex guard downstream response = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestForwardOpenAICodexOAuthNeverFallsBackToChatCompletions(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		http.Error(w, "Responses unavailable", http.StatusNotFound)
	}))
	defer server.Close()

	model := &storage.CustomModel{
		Provider: "openai", APIURL: server.URL + "/backend-api/codex/responses", EndpointMode: "manual",
		APIKey: "test-token", ExternalModelName: "gpt-test", APIStyle: "auto", RuntimeOAuthUpstream: storage.OpenAICodexOAuthUpstream,
	}
	recorder := httptest.NewRecorder()
	forwardOpenAI(recorder, httptest.NewRequest(http.MethodPost, "/generate", nil), model, textTurn("ordinary Codex turn"), "codex-no-chat-fallback")

	if len(paths) != 1 || paths[0] != "/backend-api/codex/responses" {
		t.Fatalf("Codex paths = %#v, want one Responses request and no Chat fallback", paths)
	}
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("Codex 404 status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func responseInputRole(raw any) string {
	message, _ := raw.(map[string]any)
	role, _ := message["role"].(string)
	return role
}

func responseInputContent(raw any) []any {
	message, _ := raw.(map[string]any)
	content, _ := message["content"].([]any)
	return content
}

func responseInputBlockType(raw any) string {
	block, _ := raw.(map[string]any)
	kind, _ := block["type"].(string)
	return kind
}

func mustFirstResponseInput(t *testing.T, request map[string]any) any {
	t.Helper()
	input, _ := request["input"].([]any)
	if len(input) == 0 {
		t.Fatalf("Responses input is missing: %#v", request)
	}
	return input[0]
}

func responseBodyHasTool(request map[string]any, wanted string) bool {
	tools, _ := request["tools"].([]any)
	for _, raw := range tools {
		tool, _ := raw.(map[string]any)
		if tool["type"] == wanted {
			return true
		}
	}
	return false
}
