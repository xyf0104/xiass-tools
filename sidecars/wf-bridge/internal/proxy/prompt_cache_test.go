package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"antigravity-wf-assistant/internal/storage"
)

func promptCacheFixture() (*storage.CustomModel, map[string]any) {
	return &storage.CustomModel{
			Name: "models/gpt", Provider: "openai", APIKey: "secret-a",
			APIURL: "https://example.com/v1/chat/completions", ExternalModelName: "gpt-5.6-sol",
		}, map[string]any{
			"systemInstruction": map[string]any{"parts": []any{map[string]any{"text": "stable system prompt"}}},
			"contents": []any{
				map[string]any{"role": "user", "parts": []any{map[string]any{"text": "first turn"}}},
				map[string]any{"role": "model", "parts": []any{map[string]any{"text": "answer"}}},
			},
		}
}

func TestPromptCacheKeyStableAndCredentialScoped(t *testing.T) {
	model, gemini := promptCacheFixture()
	first := buildPromptCacheKey(model, gemini)
	if first != buildPromptCacheKey(model, gemini) || strings.Contains(first, model.APIKey) {
		t.Fatalf("unexpected cache key: %s", first)
	}
	copyModel := *model
	copyModel.APIKey = "secret-b"
	if first == buildPromptCacheKey(&copyModel, gemini) {
		t.Fatal("cache keys were not isolated by credential")
	}
}

func TestOpenAIPromptCachingAndStrip(t *testing.T) {
	model, gemini := promptCacheFixture()
	defer clearPromptCacheCompatibility("openai", model)
	request := map[string]any{"messages": []map[string]any{
		{"role": "system", "content": "stable system prompt"},
		{"role": "user", "content": "question"},
	}}
	result := applyOpenAIPromptCaching(request, model, gemini)
	if !result.enabled || !result.explicit || !strings.HasPrefix(result.key, "antigravity:") {
		t.Fatalf("unexpected cache result: %+v", result)
	}
	if _, ok := request["prompt_cache_options"]; !ok {
		t.Fatal("GPT-5.6 did not receive prompt_cache_options")
	}
	stripOpenAIPromptCaching(request)
	if _, ok := request["prompt_cache_key"]; ok {
		t.Fatal("prompt_cache_key was not stripped")
	}
	messages := request["messages"].([]map[string]any)
	if messages[0]["content"] != "stable system prompt" {
		t.Fatalf("system content was not restored: %#v", messages[0]["content"])
	}
}

func TestOpenAIReasoningAndCacheValues(t *testing.T) {
	for _, name := range []string{"gpt-5.6", "gpt_5_7", "gpt-6"} {
		if !supportsOpenAIExplicitCaching(name) {
			t.Errorf("expected explicit cache support for %s", name)
		}
	}
	for _, name := range []string{"gpt-5.5", "o3", "claude-opus"} {
		if supportsOpenAIExplicitCaching(name) {
			t.Errorf("unexpected explicit cache support for %s", name)
		}
	}
}

func TestAnthropicPromptCachingUsesAtMostFourBreakpoints(t *testing.T) {
	request := map[string]any{
		"system": "stable system prompt",
		"tools":  []map[string]any{{"name": "read"}, {"name": "write"}},
		"messages": []map[string]any{
			{"role": "user", "content": []any{map[string]any{"type": "text", "text": "one"}}},
			{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": "1"}}},
			{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "1"}}},
		},
	}
	if got := applyAnthropicPromptCaching(request); got != 4 {
		t.Fatalf("breakpoints = %d, want 4", got)
	}
	data, _ := json.Marshal(request)
	if strings.Count(string(data), "cache_control") != 4 {
		t.Fatalf("unexpected cache controls: %s", data)
	}
	stripAnthropicPromptCaching(request)
	data, _ = json.Marshal(request)
	if strings.Contains(string(data), "cache_control") {
		t.Fatalf("cache controls were not stripped: %s", data)
	}
}

func TestUnsupportedCacheResponseDetection(t *testing.T) {
	if !isUnsupportedCacheResponse(400, "unknown parameter prompt_cache_key") ||
		!isUnsupportedCacheResponse(422, "cache_control is unsupported") ||
		isUnsupportedCacheResponse(400, "invalid temperature") ||
		isUnsupportedCacheResponse(503, "prompt_cache_key failed") {
		t.Fatal("cache fallback detection mismatch")
	}
}

func TestOpenAICacheValidationErrorRetriesWithoutCacheFields(t *testing.T) {
	var bodies []map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		bodies = append(bodies, body)
		if len(bodies) == 1 {
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = writer.Write([]byte(`{"error":{"message":"unknown parameter prompt_cache_key"}}`))
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("data: {\"id\":\"chatcmpl-cache\",\"model\":\"gpt-5.6-sol\",\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n"))
		_, _ = writer.Write([]byte("data: [DONE]\n\n"))
	}))
	defer upstream.Close()

	model, gemini := promptCacheFixture()
	model.APIURL = upstream.URL + "/v1/chat/completions"
	defer clearPromptCacheCompatibility("openai", model)
	recorder := httptest.NewRecorder()
	incoming := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/generate", nil)
	forwardOpenAI(recorder, incoming, model, gemini, "cache-fallback-openai")

	if len(bodies) != 2 {
		t.Fatalf("upstream requests = %d, want 2", len(bodies))
	}
	if _, ok := bodies[0]["prompt_cache_key"]; !ok {
		t.Fatal("first request did not contain prompt_cache_key")
	}
	if _, ok := bodies[1]["prompt_cache_key"]; ok {
		t.Fatal("fallback request still contained prompt_cache_key")
	}
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"text":"ok"`) {
		t.Fatalf("unexpected downstream response: %d %s", recorder.Code, recorder.Body.String())
	}

	// A compatible fallback must be remembered for the rest of the app run.
	// Otherwise every new user turn would first send a rejected cache payload.
	secondRecorder := httptest.NewRecorder()
	secondIncoming := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/generate", nil)
	forwardOpenAI(secondRecorder, secondIncoming, model, gemini, "cache-compatibility-openai")
	if len(bodies) != 3 {
		t.Fatalf("upstream requests after remembered fallback = %d, want 3", len(bodies))
	}
	if _, ok := bodies[2]["prompt_cache_key"]; ok {
		t.Fatal("known-incompatible upstream was probed for prompt caching again")
	}
	if secondRecorder.Code != http.StatusOK || !strings.Contains(secondRecorder.Body.String(), `"text":"ok"`) {
		t.Fatalf("unexpected second downstream response: %d %s", secondRecorder.Code, secondRecorder.Body.String())
	}
}

func TestAnthropicCacheValidationErrorRetriesWithoutCacheFields(t *testing.T) {
	var serializedBodies [][]byte
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(body)
		serializedBodies = append(serializedBodies, data)
		if len(serializedBodies) == 1 {
			writer.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = writer.Write([]byte(`{"error":{"message":"cache_control is unsupported"}}`))
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg-cache\",\"model\":\"claude-test\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n"))
		_, _ = writer.Write([]byte("data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n"))
		_, _ = writer.Write([]byte("data: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer upstream.Close()

	_, gemini := promptCacheFixture()
	model := &storage.CustomModel{
		Name: "models/claude", Provider: "anthropic", APIKey: "secret",
		APIURL: upstream.URL + "/v1/messages", ExternalModelName: "claude-test",
	}
	defer clearPromptCacheCompatibility("anthropic", model)
	recorder := httptest.NewRecorder()
	incoming := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/generate", nil)
	forwardAnthropic(recorder, incoming, model, gemini, "cache-fallback-anthropic")

	if len(serializedBodies) != 2 {
		t.Fatalf("upstream requests = %d, want 2", len(serializedBodies))
	}
	if !strings.Contains(string(serializedBodies[0]), "cache_control") {
		t.Fatal("first request did not contain cache_control")
	}
	if strings.Contains(string(serializedBodies[1]), "cache_control") {
		t.Fatal("fallback request still contained cache_control")
	}
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"text":"ok"`) {
		t.Fatalf("unexpected downstream response: %d %s", recorder.Code, recorder.Body.String())
	}

	secondRecorder := httptest.NewRecorder()
	secondIncoming := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/generate", nil)
	forwardAnthropic(secondRecorder, secondIncoming, model, gemini, "cache-compatibility-anthropic")
	if len(serializedBodies) != 3 {
		t.Fatalf("upstream requests after remembered fallback = %d, want 3", len(serializedBodies))
	}
	if strings.Contains(string(serializedBodies[2]), "cache_control") {
		t.Fatal("known-incompatible Anthropic upstream was probed for prompt caching again")
	}
	if secondRecorder.Code != http.StatusOK || !strings.Contains(secondRecorder.Body.String(), `"text":"ok"`) {
		t.Fatalf("unexpected second downstream response: %d %s", secondRecorder.Code, secondRecorder.Body.String())
	}
}
