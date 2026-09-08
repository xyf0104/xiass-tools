package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"antigravity-wf-assistant/internal/storage"
)

func decodeAntigravityStreamResponse(t *testing.T, out string) map[string]any {
	t.Helper()
	payload := strings.TrimSuffix(strings.TrimPrefix(out, "data: "), "\n\n")
	var envelope map[string]any
	if err := json.Unmarshal([]byte(payload), &envelope); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	response, ok := envelope["response"].(map[string]any)
	if !ok {
		t.Fatalf("missing Antigravity response envelope: %v", envelope)
	}
	if _, ok := envelope["metadata"].(map[string]any); !ok {
		t.Fatalf("missing Antigravity metadata envelope: %v", envelope)
	}
	if _, ok := envelope["traceId"].(string); !ok {
		t.Fatalf("missing Antigravity traceId envelope: %v", envelope)
	}
	return response
}

func TestGetModelSlug(t *testing.T) {
	cases := []struct {
		model storage.CustomModel
		want  string
	}{
		{storage.CustomModel{ExternalModelName: "claude-fable-5"}, "custom-claude-fable-5"},
		{storage.CustomModel{ExternalModelName: "gpt-5.6-sol"}, "custom-gpt-5-6-sol"},
		{storage.CustomModel{ExternalModelName: "GPT-4o"}, "custom-gpt-4o"},
		{storage.CustomModel{Name: "models/fable5"}, "custom-fable5"},
		{storage.CustomModel{ExternalModelName: "中文模型"}, "custom-model"},
	}
	for _, c := range cases {
		got := getModelSlug(c.model)
		if got != c.want {
			t.Errorf("getModelSlug(%+v) = %q, want %q", c.model, got, c.want)
		}
	}
}

func TestHealthEndpointIdentifiesCurrentProxy(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/_antigravity-wf/health", nil)
	recorder := httptest.NewRecorder()
	handleRequest(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("health status = %d", recorder.Code)
	}
	if got := recorder.Header().Get("X-Antigravity-WF"); got != "go-proxy" {
		t.Fatalf("health identity = %q", got)
	}
}

func TestGetModelPlaceholderStable(t *testing.T) {
	m := storage.CustomModel{DisplayName: "肥波"}
	first := getModelPlaceholder(m)
	second := getModelPlaceholder(m)
	if first != second {
		t.Errorf("placeholder not stable: %q vs %q", first, second)
	}
	if !strings.HasPrefix(first, "MODEL_PLACEHOLDER_M") {
		t.Errorf("bad placeholder format: %q", first)
	}
	var placeholderNumber int
	if _, err := fmt.Sscanf(first, "MODEL_PLACEHOLDER_M%d", &placeholderNumber); err != nil || placeholderNumber < 0 || placeholderNumber >= modelPlaceholderCount {
		t.Errorf("placeholder is outside the supported enum range: %q", first)
	}
	// Different display names should differ
	other := getModelPlaceholder(storage.CustomModel{DisplayName: "鸡皮提"})
	if first == other {
		t.Errorf("different models produced same placeholder %q", first)
	}
}

func TestAddAgentModelID(t *testing.T) {
	parsed := map[string]any{
		"agentModelSorts": []any{
			map[string]any{
				"displayName": "Recommended",
				"groups": []any{
					map[string]any{"modelIds": []any{"gemini-3.6-flash-high"}},
				},
			},
		},
	}
	addAgentModelID(&parsed, "custom-test-model")

	sorts := parsed["agentModelSorts"].([]any)
	group := sorts[0].(map[string]any)["groups"].([]any)[0].(map[string]any)
	ids := group["modelIds"].([]any)

	if len(ids) != 2 {
		t.Fatalf("expected 2 ids, got %d: %v", len(ids), ids)
	}
	if ids[0] != "custom-test-model" {
		t.Errorf("custom model not first: %v", ids)
	}

	// Idempotent
	addAgentModelID(&parsed, "custom-test-model")
	ids = parsed["agentModelSorts"].([]any)[0].(map[string]any)["groups"].([]any)[0].(map[string]any)["modelIds"].([]any)
	if len(ids) != 2 {
		t.Errorf("duplicate insert: %v", ids)
	}
}

func TestAllocateModelPlaceholdersAvoidsOfficialAndCustomCollisions(t *testing.T) {
	models := []storage.CustomModel{
		{Name: "first", DisplayName: "Same seed"},
		{Name: "second", DisplayName: "Same seed"},
	}
	official := map[string]any{
		"official": map[string]any{"model": getModelPlaceholder(models[0])},
	}
	assignments := allocateModelPlaceholders(models, official)
	first := assignments["first"]
	second := assignments["second"]
	if first == "" || second == "" {
		t.Fatalf("missing assignments: %v", assignments)
	}
	if first == second {
		t.Fatalf("custom models collided on %q", first)
	}
	if first == official["official"].(map[string]any)["model"] || second == official["official"].(map[string]any)["model"] {
		t.Fatalf("custom model collided with official model: %v", assignments)
	}
}

func TestToOpenAIRequestBasic(t *testing.T) {
	gemini := map[string]any{
		"systemInstruction": map[string]any{
			"parts": []any{map[string]any{"text": "You are helpful."}},
		},
		"contents": []any{
			map[string]any{
				"role":  "user",
				"parts": []any{map[string]any{"text": "Hello"}},
			},
			map[string]any{
				"role":  "model",
				"parts": []any{map[string]any{"text": "Hi there"}},
			},
		},
		"generationConfig": map[string]any{
			"maxOutputTokens": 4096,
			"temperature":     0.7,
		},
	}

	out := toOpenAIRequest(gemini, "gpt-5.6-sol")

	if out["model"] != "gpt-5.6-sol" {
		t.Errorf("model = %v", out["model"])
	}
	if out["max_tokens"] != 4096 {
		t.Errorf("max_tokens = %v", out["max_tokens"])
	}

	msgs := out["messages"].([]map[string]any)
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgs))
	}
	if msgs[0]["role"] != "system" || msgs[0]["content"] != "You are helpful." {
		t.Errorf("system message wrong: %+v", msgs[0])
	}
	if msgs[1]["role"] != "user" || msgs[1]["content"] != "Hello" {
		t.Errorf("user message wrong: %+v", msgs[1])
	}
	if msgs[2]["role"] != "assistant" {
		t.Errorf("model role not mapped to assistant: %+v", msgs[2])
	}
}

func TestToOpenAIRequestTools(t *testing.T) {
	gemini := map[string]any{
		"contents": []any{},
		"tools": []any{
			map[string]any{
				"functionDeclarations": []any{
					map[string]any{
						"name":        "read_file",
						"description": "Read a file",
						"parameters": map[string]any{
							"type": "OBJECT",
							"properties": map[string]any{
								"path":  map[string]any{"type": "STRING"},
								"wait":  map[string]any{"type": "BOOLEAN"},
								"lines": map[string]any{"type": "ARRAY", "items": map[string]any{"type": "INTEGER"}},
							},
							"required": []any{"path"},
						},
					},
				},
			},
		},
	}

	out := toOpenAIRequest(gemini, "gpt-4o")
	tools := out["tools"].([]map[string]any)
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	fn := tools[0]["function"].(map[string]any)
	if fn["name"] != "read_file" {
		t.Errorf("tool name = %v", fn["name"])
	}
	if tools[0]["type"] != "function" {
		t.Errorf("tool type = %v", tools[0]["type"])
	}
	params := fn["parameters"].(map[string]any)
	if params["type"] != "object" {
		t.Fatalf("root schema type was not normalized: %v", params)
	}
	properties := params["properties"].(map[string]any)
	if properties["path"].(map[string]any)["type"] != "string" || properties["wait"].(map[string]any)["type"] != "boolean" {
		t.Fatalf("nested schema types were not normalized: %v", properties)
	}
	items := properties["lines"].(map[string]any)["items"].(map[string]any)
	if items["type"] != "integer" {
		t.Fatalf("array item schema type was not normalized: %v", items)
	}
}

func TestResolveOpenAIChatCompletionsURL(t *testing.T) {
	cases := map[string]string{
		"https://api.xiass.com":                    "https://api.xiass.com/v1/chat/completions",
		"https://api.xiass.com/":                   "https://api.xiass.com/v1/chat/completions",
		"https://api.xiass.com/v1":                 "https://api.xiass.com/v1/chat/completions",
		"https://example.com/openai/v1":            "https://example.com/openai/v1/chat/completions",
		"https://example.com/v1/chat/completions":  "https://example.com/v1/chat/completions",
		"https://example.com/proxy?workspace=test": "https://example.com/proxy/chat/completions?workspace=test",
	}
	for input, want := range cases {
		if got := resolveOpenAIChatCompletionsURL(input); got != want {
			t.Errorf("resolveOpenAIChatCompletionsURL(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestResolveAnthropicMessagesURL(t *testing.T) {
	cases := map[string]string{
		"https://api.anthropic.com":                      "https://api.anthropic.com/v1/messages",
		"https://api.anthropic.com/":                     "https://api.anthropic.com/v1/messages",
		"https://api.anthropic.com/v1":                   "https://api.anthropic.com/v1/messages",
		"https://api.anthropic.com/v1/messages":          "https://api.anthropic.com/v1/messages",
		"https://api.xiass.com/v1/chat/completions":      "https://api.xiass.com/v1/messages",
		"https://example.com/proxy/v1":                   "https://example.com/proxy/v1/messages",
		"https://example.com/proxy?workspace=test":       "https://example.com/proxy/v1/messages?workspace=test",
		"https://example.com/v1/messages?workspace=test": "https://example.com/v1/messages?workspace=test",
	}
	for input, want := range cases {
		if got := resolveAnthropicMessagesURL(input); got != want {
			t.Errorf("resolveAnthropicMessagesURL(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestConvertOpenAILineToGeminiText(t *testing.T) {
	state := &openAIStreamState{}
	line := `data: {"choices":[{"delta":{"content":"Hello world"},"finish_reason":null}]}`

	out := convertOpenAILineToGemini(line, state)
	if out == "" {
		t.Fatal("empty conversion")
	}
	if !strings.HasPrefix(out, "data: ") {
		t.Errorf("missing SSE prefix: %q", out)
	}

	response := decodeAntigravityStreamResponse(t, out)
	cands := response["candidates"].([]any)
	content := cands[0].(map[string]any)["content"].(map[string]any)
	if content["role"] != "model" {
		t.Errorf("role = %v", content["role"])
	}
	parts := content["parts"].([]any)
	if parts[0].(map[string]any)["text"] != "Hello world" {
		t.Errorf("text = %v", parts[0])
	}
}

func TestConvertOpenAIToolCallAccumulation(t *testing.T) {
	state := &openAIStreamState{}

	// Tool call arrives in fragments
	convertOpenAILineToGemini(`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_read_1","function":{"name":"read_file","arguments":"{\"pa"}}]},"finish_reason":null}]}`, state)
	convertOpenAILineToGemini(`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"th\":\"a.txt\"}"}}]},"finish_reason":null}]}`, state)
	out := convertOpenAILineToGemini(`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`, state)

	if out == "" {
		t.Fatal("no output on finish")
	}
	response := decodeAntigravityStreamResponse(t, out)
	parts := response["candidates"].([]any)[0].(map[string]any)["content"].(map[string]any)["parts"].([]any)
	fc := parts[0].(map[string]any)["functionCall"].(map[string]any)
	if fc["name"] != "read_file" {
		t.Errorf("function name = %v", fc["name"])
	}
	if fc["id"] != "call_read_1" {
		t.Errorf("function call id = %v", fc["id"])
	}
	args := fc["args"].(map[string]any)
	if args["path"] != "a.txt" {
		t.Errorf("args not accumulated correctly: %v", args)
	}
}

func TestConvertOpenAIUsageCapture(t *testing.T) {
	state := &openAIStreamState{}
	convertOpenAILineToGemini(`data: {"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":50}}`, state)

	if state.usage == nil {
		t.Fatal("usage not captured")
	}
	if state.usage["prompt_tokens"] != float64(100) {
		t.Errorf("prompt_tokens = %v", state.usage["prompt_tokens"])
	}
}

func TestConvertOpenAIStopProducesFinalEnvelope(t *testing.T) {
	state := &openAIStreamState{traceID: "request-123"}
	out := convertOpenAILineToGemini(`data: {"id":"chatcmpl-1","model":"gpt-test","choices":[{"delta":{},"finish_reason":"stop"}]}`, state)
	if out == "" {
		t.Fatal("stop chunk must not be discarded")
	}
	response := decodeAntigravityStreamResponse(t, out)
	candidate := response["candidates"].([]any)[0].(map[string]any)
	if candidate["finishReason"] != "STOP" {
		t.Fatalf("finishReason = %v", candidate["finishReason"])
	}
	if response["responseId"] != "chatcmpl-1" || response["modelVersion"] != "gpt-test" {
		t.Fatalf("upstream identity was not preserved: %v", response)
	}
}

func TestConvertOpenAIDoneAndEmpty(t *testing.T) {
	state := &openAIStreamState{}
	if out := convertOpenAILineToGemini("data: [DONE]", state); out != "" {
		t.Errorf("[DONE] should produce nothing, got %q", out)
	}
	if out := convertOpenAILineToGemini("", state); out != "" {
		t.Errorf("empty line should produce nothing")
	}
	if out := convertOpenAILineToGemini("event: ping", state); out != "" {
		t.Errorf("non-data line should produce nothing")
	}
}

func TestStreamOpenAIRejectsUnrecognizedSuccessfulBody(t *testing.T) {
	resp := &http.Response{
		Header: http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
		Body:   io.NopCloser(strings.NewReader("<!doctype html><title>API dashboard</title>")),
	}
	recorder := httptest.NewRecorder()
	streamOpenAIResponse(recorder, resp, "request-empty", 1)

	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "invalid_upstream_stream") {
		t.Fatalf("unexpected error body: %s", recorder.Body.String())
	}
}

func TestStreamOpenAIAddsStopWhenUpstreamOmitsIt(t *testing.T) {
	body := `data: {"id":"chatcmpl-1","model":"gpt-test","choices":[{"delta":{"content":"hello"},"finish_reason":null}]}` + "\n\n"
	resp := &http.Response{
		Header: http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:   io.NopCloser(strings.NewReader(body)),
	}
	recorder := httptest.NewRecorder()
	streamOpenAIResponse(recorder, resp, "request-no-stop", 1)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"finishReason":"STOP"`) {
		t.Fatalf("synthetic STOP was not appended: %s", recorder.Body.String())
	}
}

func TestToAnthropicRequestCacheBreakpoints(t *testing.T) {
	gemini := map[string]any{
		"systemInstruction": map[string]any{
			"parts": []any{map[string]any{"text": "System prompt"}},
		},
		"contents": []any{
			map[string]any{"role": "user", "parts": []any{map[string]any{"text": "Q1"}}},
			map[string]any{"role": "model", "parts": []any{map[string]any{"text": "A1"}}},
			map[string]any{"role": "user", "parts": []any{map[string]any{"text": "Q2"}}},
		},
	}

	out := toAnthropicRequest(gemini, "claude-sonnet-4-6")
	breakpoints := applyAnthropicPromptCaching(out)
	if breakpoints < 2 || breakpoints > 4 {
		t.Fatalf("unexpected breakpoint count: %d", breakpoints)
	}

	// System should have cache_control
	sys := out["system"].([]any)
	sysBlock := sys[0].(map[string]any)
	if sysBlock["cache_control"] == nil {
		t.Error("system missing cache_control")
	}

	// Count total ephemeral breakpoints (Anthropic max 4)
	count := 0
	if sysBlock["cache_control"] != nil {
		count++
	}
	msgs := out["messages"].([]map[string]any)
	for _, m := range msgs {
		blocks := m["content"].([]any)
		for _, b := range blocks {
			if bm, ok := b.(map[string]any); ok && bm["cache_control"] != nil {
				count++
			}
		}
	}
	if count > 4 {
		t.Errorf("too many cache breakpoints: %d (Anthropic max 4)", count)
	}
	if count < 2 {
		t.Errorf("expected at least system + 1 message breakpoint, got %d", count)
	}
}

func TestToAnthropicRoleMapping(t *testing.T) {
	gemini := map[string]any{
		"contents": []any{
			map[string]any{"role": "user", "parts": []any{map[string]any{"text": "question"}}},
			map[string]any{"role": "model", "parts": []any{map[string]any{"text": "response"}}},
			map[string]any{"role": "user", "parts": []any{map[string]any{"text": "follow up"}}},
		},
	}
	out := toAnthropicRequest(gemini, "claude-x")
	msgs := out["messages"].([]map[string]any)
	if msgs[1]["role"] != "assistant" {
		t.Errorf("model should map to assistant, got %v", msgs[1]["role"])
	}
}

func TestAnthropicToolConversion(t *testing.T) {
	gemini := map[string]any{
		"contents": []any{},
		"tools": []any{
			map[string]any{
				"functionDeclarations": []any{
					map[string]any{
						"name":        "grep",
						"description": "Search",
						"parameters": map[string]any{
							"type":       "OBJECT",
							"properties": map[string]any{"fixed": map[string]any{"type": "BOOLEAN"}},
						},
					},
				},
			},
		},
	}
	out := toAnthropicRequest(gemini, "claude-x")
	tools := out["tools"].([]map[string]any)
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	if tools[0]["name"] != "grep" {
		t.Errorf("name = %v", tools[0]["name"])
	}
	if tools[0]["input_schema"] == nil {
		t.Error("missing input_schema (Anthropic format)")
	}
	schema := tools[0]["input_schema"].(map[string]any)
	if schema["type"] != "object" || schema["properties"].(map[string]any)["fixed"].(map[string]any)["type"] != "boolean" {
		t.Fatalf("Anthropic schema types were not normalized: %v", schema)
	}
}

func TestConvertAnthropicTextDelta(t *testing.T) {
	state := &anthropicStreamState{}
	out := convertAnthropicLineToGemini(`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"Hi"}}`, state)

	if out == "" {
		t.Fatal("empty conversion")
	}
	response := decodeAntigravityStreamResponse(t, out)
	parts := response["candidates"].([]any)[0].(map[string]any)["content"].(map[string]any)["parts"].([]any)
	if parts[0].(map[string]any)["text"] != "Hi" {
		t.Errorf("text = %v", parts[0])
	}
}

func TestConvertAnthropicToolUse(t *testing.T) {
	state := &anthropicStreamState{}

	convertAnthropicLineToGemini(`data: {"type":"content_block_start","content_block":{"type":"tool_use","id":"toolu_1","name":"view_file"}}`, state)
	convertAnthropicLineToGemini(`data: {"type":"content_block_delta","delta":{"type":"input_json_delta","partial_json":"{\"path\""}}`, state)
	convertAnthropicLineToGemini(`data: {"type":"content_block_delta","delta":{"type":"input_json_delta","partial_json":":\"x.go\"}"}}`, state)
	out := convertAnthropicLineToGemini(`data: {"type":"content_block_stop"}`, state)

	if out == "" {
		t.Fatal("no output on content_block_stop")
	}
	response := decodeAntigravityStreamResponse(t, out)
	parts := response["candidates"].([]any)[0].(map[string]any)["content"].(map[string]any)["parts"].([]any)
	fc := parts[0].(map[string]any)["functionCall"].(map[string]any)

	if fc["name"] != "view_file" {
		t.Errorf("name = %v", fc["name"])
	}
	if fc["id"] != "toolu_1" {
		t.Errorf("tool id = %v", fc["id"])
	}
	args := fc["args"].(map[string]any)
	if args["path"] != "x.go" {
		t.Errorf("args = %v", args)
	}
}

func TestConvertAnthropicToolUseWithInlineInput(t *testing.T) {
	state := &anthropicStreamState{traceID: "inline-tool"}
	convertAnthropicLineToGemini(`data: {"type":"content_block_start","content_block":{"type":"tool_use","id":"toolu_inline","name":"search","input":{"query":"天气"}}}`, state)
	out := convertAnthropicLineToGemini(`data: {"type":"content_block_stop"}`, state)
	if out == "" {
		t.Fatal("inline tool input was discarded")
	}
	response := decodeAntigravityStreamResponse(t, out)
	parts := response["candidates"].([]any)[0].(map[string]any)["content"].(map[string]any)["parts"].([]any)
	args := parts[0].(map[string]any)["functionCall"].(map[string]any)["args"].(map[string]any)
	if args["query"] != "天气" {
		t.Fatalf("inline tool arguments = %#v", args)
	}
}

func TestConvertAnthropicMalformedToolUseDoesNotEmitFunctionCall(t *testing.T) {
	state := &anthropicStreamState{traceID: "malformed-tool"}
	convertAnthropicLineToGemini(`data: {"type":"content_block_start","content_block":{"type":"tool_use","id":"toolu_bad","name":"search"}}`, state)
	if out := convertAnthropicLineToGemini(`data: {"type":"content_block_stop"}`, state); out != "" {
		response := decodeAntigravityStreamResponse(t, out)
		parts := response["candidates"].([]any)[0].(map[string]any)["content"].(map[string]any)["parts"].([]any)
		for _, part := range parts {
			if _, isCall := part.(map[string]any)["functionCall"]; isCall {
				t.Fatalf("malformed tool call was emitted: %#v", part)
			}
		}
	}
	stop := convertAnthropicLineToGemini(`data: {"type":"message_stop"}`, state)
	if stop == "" || !strings.Contains(stop, `"finishReason":"STOP"`) {
		t.Fatalf("malformed tool stream did not terminate cleanly: %s", stop)
	}
}

func TestConvertOpenAIInvalidToolArgumentsDoesNotEmitFunctionCall(t *testing.T) {
	state := &openAIStreamState{traceID: "invalid-openai-tool"}
	convertOpenAILineToGemini(`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_bad","function":{"name":"search","arguments":"not-json"}}]},"finish_reason":null}]}`, state)
	out := convertOpenAILineToGemini(`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`, state)
	if out == "" {
		t.Fatal("tool finish envelope should still be emitted")
	}
	if strings.Contains(out, "functionCall") || state.unsafeOutput {
		t.Fatalf("invalid OpenAI tool arguments were emitted: %s", out)
	}
}

func TestConvertAnthropicParallelToolUsePreservesEachID(t *testing.T) {
	state := &anthropicStreamState{}
	convertAnthropicLineToGemini(`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_first","name":"first"}}`, state)
	convertAnthropicLineToGemini(`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_second","name":"second"}}`, state)
	convertAnthropicLineToGemini(`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"value\":2}"}}`, state)
	convertAnthropicLineToGemini(`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"value\":1}"}}`, state)

	second := decodeAntigravityStreamResponse(t, convertAnthropicLineToGemini(`data: {"type":"content_block_stop","index":1}`, state))
	secondCall := second["candidates"].([]any)[0].(map[string]any)["content"].(map[string]any)["parts"].([]any)[0].(map[string]any)["functionCall"].(map[string]any)
	if secondCall["id"] != "toolu_second" || secondCall["name"] != "second" || secondCall["args"].(map[string]any)["value"] != float64(2) {
		t.Fatalf("second parallel tool call was corrupted: %#v", secondCall)
	}
	first := decodeAntigravityStreamResponse(t, convertAnthropicLineToGemini(`data: {"type":"content_block_stop","index":0}`, state))
	firstCall := first["candidates"].([]any)[0].(map[string]any)["content"].(map[string]any)["parts"].([]any)[0].(map[string]any)["functionCall"].(map[string]any)
	if firstCall["id"] != "toolu_first" || firstCall["name"] != "first" || firstCall["args"].(map[string]any)["value"] != float64(1) {
		t.Fatalf("first parallel tool call was corrupted: %#v", firstCall)
	}
}

func TestConvertAnthropicStopProducesFinalEnvelope(t *testing.T) {
	state := &anthropicStreamState{traceID: "request-456"}
	convertAnthropicLineToGemini(`data: {"type":"message_start","message":{"id":"msg_1","model":"claude-test"}}`, state)
	out := convertAnthropicLineToGemini(`data: {"type":"message_stop"}`, state)
	response := decodeAntigravityStreamResponse(t, out)
	candidate := response["candidates"].([]any)[0].(map[string]any)
	if candidate["finishReason"] != "STOP" {
		t.Fatalf("finishReason = %v", candidate["finishReason"])
	}
	if response["responseId"] != "msg_1" || response["modelVersion"] != "claude-test" {
		t.Fatalf("upstream identity was not preserved: %v", response)
	}
}

func TestStreamAnthropicRejectsUnrecognizedSuccessfulBody(t *testing.T) {
	resp := &http.Response{
		Header: http.Header{"Content-Type": []string{"application/json"}},
		Body:   io.NopCloser(strings.NewReader(`{"unexpected":"shape"}`)),
	}
	recorder := httptest.NewRecorder()
	streamAnthropicResponse(recorder, resp, "request-empty-anthropic", 1)

	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "invalid_upstream_stream") {
		t.Fatalf("unexpected error body: %s", recorder.Body.String())
	}
}

func TestCollectAnthropicUsage(t *testing.T) {
	totals := anthropicUsageTotals{}

	collectAnthropicUsage(`data: {"type":"message_start","message":{"usage":{"input_tokens":1000,"cache_read_input_tokens":800,"cache_creation_input_tokens":50}}}`, &totals)
	collectAnthropicUsage(`data: {"type":"message_delta","usage":{"output_tokens":200}}`, &totals)

	if !totals.seen {
		t.Fatal("usage not seen")
	}
	if totals.input != 1000 {
		t.Errorf("input = %d", totals.input)
	}
	if totals.output != 200 {
		t.Errorf("output = %d", totals.output)
	}
	if totals.cacheRead != 800 {
		t.Errorf("cacheRead = %d", totals.cacheRead)
	}
	if totals.cacheWrite != 50 {
		t.Errorf("cacheWrite = %d", totals.cacheWrite)
	}
}

func TestBuildFakeModelEntry(t *testing.T) {
	m := storage.CustomModel{
		DisplayName:       "肥波",
		AccountPoolLabel:  "XIASS主池",
		Description:       "test",
		ExternalModelName: "claude-fable-5",
	}
	placeholder := getModelPlaceholder(m)
	entry := buildFakeModelEntry(m, placeholder)

	if entry["displayName"] != "肥波 · XIASS主池" {
		t.Errorf("displayName = %v", entry["displayName"])
	}
	if entry["apiProvider"] != "API_PROVIDER_GOOGLE_GEMINI" {
		t.Errorf("apiProvider must mimic Google for IDE compatibility, got %v", entry["apiProvider"])
	}
	if entry["model"] != placeholder {
		t.Errorf("model placeholder mismatch")
	}
	if entry["recommended"] != true {
		t.Error("should be recommended so it surfaces in menu")
	}
	if entry["requiresImageOutputOutsideFunctionResponses"] != true {
		t.Errorf("image-capable model must request media output outside function responses: %#v", entry)
	}
}

func TestReasoningBudget(t *testing.T) {
	cases := map[string]int{
		"":       0,
		"auto":   0,
		"low":    1024,
		"medium": 4096,
		"high":   8192,
	}
	for effort, want := range cases {
		if got := reasoningBudget(effort); got != want {
			t.Errorf("reasoningBudget(%q) = %d, want %d", effort, got, want)
		}
	}
}

func TestRetryableStatusIncludesCloudflareTimeout(t *testing.T) {
	for _, code := range []int{429, 502, 503, 504, 524} {
		if !isRetryableStatus(code) {
			t.Errorf("status %d should be retryable", code)
		}
	}
	for _, code := range []int{400, 401, 500} {
		if isRetryableStatus(code) {
			t.Errorf("status %d should not be retryable", code)
		}
	}
}

func TestRetryDelay(t *testing.T) {
	if got := retryDelay(1, "2"); got != 2*time.Second {
		t.Errorf("numeric Retry-After delay = %v", got)
	}
	if got := retryDelay(1, "30"); got != 10*time.Second {
		t.Errorf("Retry-After cap = %v", got)
	}
	if got := retryDelay(2, ""); got != 500*time.Millisecond {
		t.Errorf("exponential delay = %v", got)
	}
}

func TestForwardOpenAIChatDoesNotReplayAfterPartialStreamDisconnect(t *testing.T) {
	previous := currentStreamRecoveryPolicy()
	ConfigureStreamRecovery(storage.StreamRecoverySettings{Enabled: true, MaxAttempts: 2, MaxDelaySeconds: 1})
	defer ConfigureStreamRecovery(storage.StreamRecoverySettings{
		Enabled: previous.enabled, MaxAttempts: previous.maxAttempts, MaxDelaySeconds: previous.maxDelaySeconds,
	})

	requests := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "text/event-stream")
		if requests == 1 {
			_, _ = io.WriteString(w, "data: {\"id\":\"first\",\"model\":\"gpt-test\",\"choices\":[{\"delta\":{\"content\":\"第一段\"},\"finish_reason\":null}]}\n\n")
			return // Simulate an upstream connection that ends before STOP.
		}
		_, _ = io.WriteString(w, "data: {\"id\":\"second\",\"model\":\"gpt-test\",\"choices\":[{\"delta\":{\"content\":\"第二段\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()

	model := &storage.CustomModel{Provider: "openai", APIURL: upstream.URL + "/v1", APIKey: "test-key", ExternalModelName: "gpt-test"}
	request := httptest.NewRequest(http.MethodPost, "/v1internal:streamGenerateContent", nil)
	recorder := httptest.NewRecorder()
	gemini := map[string]any{"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "请回答"}}}}}

	forwardOpenAIChat(recorder, request, model, gemini, "reconnect-test")

	if requests != 1 {
		t.Fatalf("upstream requests = %d, want 1: a partial answer must never be replayed with injected chat context", requests)
	}
	output := recorder.Body.String()
	if recorder.Code != http.StatusOK {
		t.Fatalf("downstream status = %d, body = %s", recorder.Code, output)
	}
	if !strings.Contains(output, "第一段") || strings.Contains(output, "第二段") {
		t.Fatalf("partial output should be preserved without a replay: %s", output)
	}
	if !strings.Contains(output, `"finishReason":"STOP"`) {
		t.Fatalf("partial stream has no final stop: %s", output)
	}
	if strings.Contains(output, "上游连接已自动重连") || strings.Contains(output, "上游流式连接刚刚中断") {
		t.Fatalf("recovery metadata leaked into the conversation: %s", output)
	}
}

func TestForwardAnthropicDoesNotReplayAfterPartialStreamDisconnect(t *testing.T) {
	previous := currentStreamRecoveryPolicy()
	ConfigureStreamRecovery(storage.StreamRecoverySettings{Enabled: true, MaxAttempts: 2, MaxDelaySeconds: 1})
	defer ConfigureStreamRecovery(storage.StreamRecoverySettings{Enabled: previous.enabled, MaxAttempts: previous.maxAttempts, MaxDelaySeconds: previous.maxDelaySeconds})

	requests := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg-partial\",\"model\":\"claude-test\"}}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"第一段\"}}\n\n")
		// End before message_stop to simulate a dropped upstream stream.
	}))
	defer upstream.Close()

	model := &storage.CustomModel{Provider: "anthropic", APIStyle: "messages", APIURL: upstream.URL, APIKey: "test-key", ExternalModelName: "claude-test"}
	recorder := httptest.NewRecorder()
	forwardAnthropic(recorder, httptest.NewRequest(http.MethodPost, "/v1internal:streamGenerateContent", nil), model, map[string]any{
		"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "请回答"}}}},
	}, "claude-partial-stream")

	if requests != 1 {
		t.Fatalf("Claude partial stream was replayed %d times", requests)
	}
	output := recorder.Body.String()
	if recorder.Code != http.StatusOK || strings.Count(output, "第一段") != 1 || !strings.Contains(output, `"finishReason":"STOP"`) {
		t.Fatalf("Claude partial stream did not finish safely: %d %s", recorder.Code, output)
	}
}

func TestStreamRecoveryOnlyRetriesExplicitRejectionBeforeCommit(t *testing.T) {
	policy := streamRecoveryPolicy{enabled: true, maxAttempts: 2, maxDelaySeconds: 1}
	writer := newDownstreamSSEWriter(httptest.NewRecorder())
	if !canRetryRejectedRequest(writer, policy, 1) {
		t.Fatal("an explicit rejection before downstream output should be eligible for one safe retry")
	}
	writer.committed = true
	if canRetryRejectedRequest(writer, policy, 1) {
		t.Fatal("a committed stream must never be replayed")
	}
	writer.committed = false
	if canRetryRejectedRequest(writer, policy, 3) {
		t.Fatal("retry budget must be enforced")
	}
	policy.enabled = false
	if canRetryRejectedRequest(writer, policy, 1) {
		t.Fatal("disabled recovery must not retry an upstream rejection")
	}
}

func TestForwardOpenAIChatDoesNotReplayAfterRoleOnlyStream(t *testing.T) {
	previous := currentStreamRecoveryPolicy()
	ConfigureStreamRecovery(storage.StreamRecoverySettings{Enabled: true, MaxAttempts: 2, MaxDelaySeconds: 1})
	defer ConfigureStreamRecovery(storage.StreamRecoverySettings{Enabled: previous.enabled, MaxAttempts: previous.maxAttempts, MaxDelaySeconds: previous.maxDelaySeconds})

	requests := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"first\",\"model\":\"gpt-test\",\"choices\":[{\"delta\":{\"role\":\"assistant\"},\"finish_reason\":null}]}\n\n")
		// A role/reasoning event proves that the upstream accepted the request,
		// but it has no user-visible text to convert.
	}))
	defer upstream.Close()

	model := &storage.CustomModel{Provider: "openai", APIURL: upstream.URL + "/v1", APIKey: "test-key", ExternalModelName: "gpt-test"}
	recorder := httptest.NewRecorder()
	forwardOpenAIChat(recorder, httptest.NewRequest(http.MethodPost, "/v1internal:streamGenerateContent", nil), model, map[string]any{
		"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "请回答"}}}},
	}, "role-only")

	if requests != 1 {
		t.Fatalf("upstream requests = %d, want 1: an accepted stream must never be replayed", requests)
	}
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 for an incomplete uncommitted stream: %s", recorder.Code, recorder.Body.String())
	}
}

func TestBoundAccountPoolRetriesPinnedAccountAfterTransientQuotaResponse(t *testing.T) {
	storage.Init(t.TempDir())

	var firstCalls, secondCalls atomic.Int32
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := firstCalls.Add(1)
		if got := r.Header.Get("Authorization"); got != "Bearer first-token" {
			t.Errorf("first account authorization = %q", got)
		}
		if call == 1 {
			w.Header().Set("X-RateLimit-Remaining-Requests", "0")
			w.Header().Set("X-RateLimit-Reset-Requests", "30s")
			w.Header().Set("Retry-After", "0")
			http.Error(w, `{"error":{"message":"quota exhausted"}}`, http.StatusTooManyRequests)
			return
		}
		w.Header().Set("X-RateLimit-Remaining-Requests", "9")
		w.Header().Set("X-RateLimit-Remaining-Tokens", "1000")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-2\",\"model\":\"gpt-test\",\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n"))
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondCalls.Add(1)
		if got := r.Header.Get("Authorization"); got != "Bearer second-token" {
			t.Errorf("second account authorization = %q", got)
		}
		w.Header().Set("X-RateLimit-Remaining-Requests", "9")
		w.Header().Set("X-RateLimit-Remaining-Tokens", "1000")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-2\",\"model\":\"gpt-test\",\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n"))
	}))
	defer second.Close()

	for _, account := range []storage.UpstreamAccount{
		{ID: "first", Name: "first", Provider: "openai", Type: "api_key", APIURL: first.URL, APIKey: "first-token", AuthMode: "bearer", Enabled: true, Priority: 1, MaxConcurrency: 1},
		{ID: "second", Name: "second", Provider: "openai", Type: "api_key", APIURL: second.URL, APIKey: "second-token", AuthMode: "bearer", Enabled: true, Priority: 2, MaxConcurrency: 1},
	} {
		if err := storage.SaveUpstreamAccount(account); err != nil {
			t.Fatal(err)
		}
	}

	previous := currentStreamRecoveryPolicy()
	ConfigureStreamRecovery(storage.StreamRecoverySettings{Enabled: true, MaxAttempts: 2, MaxDelaySeconds: 1})
	defer ConfigureStreamRecovery(storage.StreamRecoverySettings{Enabled: previous.enabled, MaxAttempts: previous.maxAttempts, MaxDelaySeconds: previous.maxDelaySeconds})

	model := &storage.CustomModel{
		Name: "models/gpt-test", DisplayName: "gpt-test", Provider: "openai", ExternalModelName: "gpt-test",
		AccountIDs: []string{"first", "second"}, APIStyle: "chat_completions",
	}
	recorder := httptest.NewRecorder()
	incoming := httptest.NewRequest(http.MethodPost, "/v1internal:generateContent", nil)
	forwardOpenAIChat(recorder, incoming, model, map[string]any{
		"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "hello"}}}},
	}, "pool-failover")

	if firstCalls.Load() != 2 || secondCalls.Load() != 0 {
		t.Fatalf("account calls = first:%d second:%d, want two attempts on the pinned first account", firstCalls.Load(), secondCalls.Load())
	}
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "ok") {
		t.Fatalf("unexpected downstream response: %d %s", recorder.Code, recorder.Body.String())
	}
	accounts, err := storage.LoadUpstreamAccounts()
	if err != nil {
		t.Fatal(err)
	}
	status := map[string]storage.UpstreamAccount{}
	for _, account := range accounts {
		status[account.ID] = account
	}
	if status["first"].CooldownUntil != "" || status["first"].FailureCount != 0 || status["first"].LastSuccessAt == "" {
		t.Fatalf("recovered account retained a cross-request blacklist: %#v", status["first"])
	}
	if quota := status["first"].Quota; !quota.Available || quota.StatusCode != http.StatusOK || quota.RequestsRemaining != "9" || quota.TokensRemaining != "1000" {
		t.Fatalf("recovered account quota was not updated from the successful retry: %#v", quota)
	}
	if status["second"].LastSuccessAt != "" || status["second"].ActiveRequests != 0 {
		t.Fatalf("second account was unexpectedly used: %#v", status["second"])
	}
}

func TestConnectionFailureBeforeRequestWriteRetriesAndCompletes(t *testing.T) {
	previousPolicy := currentStreamRecoveryPolicy()
	ConfigureStreamRecovery(storage.StreamRecoverySettings{Enabled: true, MaxAttempts: 1, MaxDelaySeconds: 1})
	defer ConfigureStreamRecovery(storage.StreamRecoverySettings{
		Enabled: previousPolicy.enabled, MaxAttempts: previousPolicy.maxAttempts, MaxDelaySeconds: previousPolicy.maxDelaySeconds,
	})

	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl-safe-retry\",\"model\":\"gpt-test\",\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n")
	}))
	defer upstreamServer.Close()

	originalTransport := http.DefaultTransport
	var transportCalls atomic.Int32
	http.DefaultTransport = modelFetchRoundTripper(func(request *http.Request) (*http.Response, error) {
		if transportCalls.Add(1) == 1 {
			// Returning before delegating means net/http never invokes
			// WroteRequest; the proxy can prove no upstream request was sent.
			return nil, fmt.Errorf("temporary dial failure before request write")
		}
		return originalTransport.RoundTrip(request)
	})
	defer func() { http.DefaultTransport = originalTransport }()

	model := &storage.CustomModel{
		Provider: "openai", APIStyle: "chat_completions", APIURL: upstreamServer.URL,
		APIKey: "test-key", ExternalModelName: "gpt-test",
	}
	recorder := httptest.NewRecorder()
	forwardOpenAIChat(recorder, httptest.NewRequest(http.MethodPost, "/v1internal:streamGenerateContent", nil), model, map[string]any{
		"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "hello"}}}},
	}, "safe-transport-retry")

	if transportCalls.Load() != 2 {
		t.Fatalf("transport attempts = %d, want one safe retry", transportCalls.Load())
	}
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"text":"ok"`) {
		t.Fatalf("safe connection retry did not complete normally: %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestTransientProvider403RetriesSameDirectUpstreamThenCompletes(t *testing.T) {
	previous := currentStreamRecoveryPolicy()
	ConfigureStreamRecovery(storage.StreamRecoverySettings{Enabled: true, MaxAttempts: 2, MaxDelaySeconds: 1})
	defer ConfigureStreamRecovery(storage.StreamRecoverySettings{Enabled: previous.enabled, MaxAttempts: previous.maxAttempts, MaxDelaySeconds: previous.maxDelaySeconds})

	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"type":"error","error":{"type":"permission_error","message":"The request is prohibited due to a violation of provider Terms Of Service.","error_type":"permission_denied"},"metadata":{"provider_name":null}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg-ok\",\"model\":\"claude-test\"}}\n\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"恢复成功\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer upstream.Close()

	model := &storage.CustomModel{Provider: "anthropic", APIStyle: "messages", APIURL: upstream.URL, APIKey: "test-key", ExternalModelName: "claude-test"}
	recorder := httptest.NewRecorder()
	forwardAnthropic(recorder, httptest.NewRequest(http.MethodPost, "/v1internal:streamGenerateContent", nil), model, map[string]any{
		"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "hello"}}}},
	}, "provider-403-recovery")

	if calls.Load() != 2 {
		t.Fatalf("upstream calls = %d, want one safe retry", calls.Load())
	}
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "恢复成功") || !strings.Contains(recorder.Body.String(), `"finishReason":"STOP"`) {
		t.Fatalf("unexpected recovered response: %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestPersistentTransientProvider403EndsTurnWithoutAgentError(t *testing.T) {
	previous := currentStreamRecoveryPolicy()
	ConfigureStreamRecovery(storage.StreamRecoverySettings{Enabled: true, MaxAttempts: 2, MaxDelaySeconds: 1})
	defer ConfigureStreamRecovery(storage.StreamRecoverySettings{Enabled: previous.enabled, MaxAttempts: previous.maxAttempts, MaxDelaySeconds: previous.maxDelaySeconds})

	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"permission_error","message":"The request is prohibited due to a violation of provider Terms Of Service.","error_type":"permission_denied"},"metadata":{"provider_name":null}}`)
	}))
	defer upstream.Close()

	model := &storage.CustomModel{Provider: "anthropic", APIStyle: "messages", APIURL: upstream.URL, APIKey: "test-key", ExternalModelName: "claude-test"}
	recorder := httptest.NewRecorder()
	forwardAnthropic(recorder, httptest.NewRequest(http.MethodPost, "/v1internal:streamGenerateContent", nil), model, map[string]any{
		"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "hello"}}}},
	}, "provider-403-stop")

	if calls.Load() != 3 {
		t.Fatalf("upstream calls = %d, want initial request plus two bounded retries", calls.Load())
	}
	output := recorder.Body.String()
	if recorder.Code != http.StatusOK || !strings.Contains(output, "第三方上游暂时没有可用线路") || !strings.Contains(output, `"finishReason":"STOP"`) {
		t.Fatalf("temporary failure did not become a completed Antigravity turn: %d %s", recorder.Code, output)
	}
}

func TestTransientGatewayModelPool404WaitsForRefillThenCompletes(t *testing.T) {
	previous := currentStreamRecoveryPolicy()
	ConfigureStreamRecovery(storage.StreamRecoverySettings{Enabled: true, MaxAttempts: 2, MaxDelaySeconds: 1})
	defer ConfigureStreamRecovery(storage.StreamRecoverySettings{Enabled: previous.enabled, MaxAttempts: previous.maxAttempts, MaxDelaySeconds: previous.maxDelaySeconds})

	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":{"message":"Model gpt-test is not supported by any configured account in this group","type":"model_not_found"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl-refilled\",\"model\":\"gpt-test\",\"choices\":[{\"delta\":{\"content\":\"补号后恢复\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()

	model := &storage.CustomModel{Provider: "openai", APIStyle: "chat_completions", APIURL: upstream.URL, APIKey: "test-key", ExternalModelName: "gpt-test"}
	recorder := httptest.NewRecorder()
	forwardOpenAIChat(recorder, httptest.NewRequest(http.MethodPost, "/v1internal:streamGenerateContent", nil), model, map[string]any{
		"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "hello"}}}},
	}, "model-pool-refill")

	if calls.Load() != 2 {
		t.Fatalf("upstream calls = %d, want one safe retry after pool refill", calls.Load())
	}
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "补号后恢复") || !strings.Contains(recorder.Body.String(), `"finishReason":"STOP"`) {
		t.Fatalf("unexpected recovered response: %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestRejectedRetryAfterUsesBoundedProviderRefillDelays(t *testing.T) {
	provider403 := `{"error":{"type":"permission_error","message":"provider Terms Of Service","error_type":"permission_denied"}}`
	pool404 := `{"error":{"message":"not supported by any configured account in this group","type":"model_not_found"}}`
	permanent403 := `{"error":{"message":"insufficient balance","type":"billing_error"}}`
	cases := []struct {
		name                     string
		status, reconnect        int
		body, upstream, expected string
	}{
		{name: "provider first", status: http.StatusForbidden, reconnect: 1, body: provider403, expected: "1.500"},
		{name: "provider second", status: http.StatusForbidden, reconnect: 2, body: provider403, expected: "3.000"},
		{name: "empty model pool", status: http.StatusNotFound, reconnect: 1, body: pool404, expected: "1.500"},
		{name: "rate limit", status: http.StatusTooManyRequests, reconnect: 1, expected: "2.000"},
		{name: "gateway second", status: http.StatusBadGateway, reconnect: 2, expected: "2.000"},
		{name: "explicit header", status: http.StatusForbidden, reconnect: 1, body: provider403, upstream: "7", expected: "7"},
		{name: "permanent balance", status: http.StatusForbidden, reconnect: 1, body: permanent403, expected: ""},
		{name: "unknown model", status: http.StatusNotFound, reconnect: 1, body: `{"error":{"message":"model not found"}}`, expected: ""},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := rejectedRetryAfter(test.status, test.body, test.upstream, test.reconnect); got != test.expected {
				t.Fatalf("rejectedRetryAfter() = %q, want %q", got, test.expected)
			}
		})
	}
}

func TestPermanentUpstreamRejectionsEndTurnWithoutAutomaticReplay(t *testing.T) {
	cases := []struct {
		name       string
		statusCode int
		body       string
		want       string
	}{
		{name: "insufficient balance", statusCode: http.StatusForbidden, body: `{"error":{"message":"insufficient balance","type":"billing_error"}}`, want: "余额、额度或权限不足"},
		{name: "model route missing", statusCode: http.StatusNotFound, body: `{"error":{"message":"Model \\"gpt-test\\" not found","type":"model_not_found"}}`, want: "没有为当前账户配置所选模型"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(test.statusCode)
				_, _ = io.WriteString(w, test.body)
			}))
			defer upstream.Close()

			model := &storage.CustomModel{Provider: "openai", APIStyle: "chat_completions", APIURL: upstream.URL, APIKey: "test-key", ExternalModelName: "gpt-test"}
			recorder := httptest.NewRecorder()
			forwardOpenAIChat(recorder, httptest.NewRequest(http.MethodPost, "/v1internal:streamGenerateContent", nil), model, map[string]any{
				"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "hello"}}}},
			}, "permanent-rejection")

			if calls.Load() != 1 {
				t.Fatalf("upstream calls = %d, permanent rejection must not be replayed", calls.Load())
			}
			if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), test.want) || !strings.Contains(recorder.Body.String(), `"finishReason":"STOP"`) {
				t.Fatalf("permanent rejection did not produce one actionable completed turn: %d %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestBoundAccountPermanentFailureDoesNotSwitchAccountsOrReturnHTTP503(t *testing.T) {
	storage.Init(t.TempDir())
	previous := currentStreamRecoveryPolicy()
	ConfigureStreamRecovery(storage.StreamRecoverySettings{Enabled: true, MaxAttempts: 2, MaxDelaySeconds: 1})
	defer ConfigureStreamRecovery(storage.StreamRecoverySettings{Enabled: previous.enabled, MaxAttempts: previous.maxAttempts, MaxDelaySeconds: previous.maxDelaySeconds})

	var firstCalls, secondCalls atomic.Int32
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstCalls.Add(1)
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":{"message":"insufficient balance","type":"billing_error"}}`)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondCalls.Add(1)
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":{"message":"insufficient balance","type":"billing_error"}}`)
	}))
	defer second.Close()

	for _, account := range []storage.UpstreamAccount{
		{ID: "first-exhausted", Name: "first", Provider: "openai", Type: "api_key", APIURL: first.URL, APIKey: "first-token", AuthMode: "bearer", Enabled: true, Priority: 1, MaxConcurrency: 1},
		{ID: "second-exhausted", Name: "second", Provider: "openai", Type: "api_key", APIURL: second.URL, APIKey: "second-token", AuthMode: "bearer", Enabled: true, Priority: 2, MaxConcurrency: 1},
	} {
		if err := storage.SaveUpstreamAccount(account); err != nil {
			t.Fatal(err)
		}
	}

	model := &storage.CustomModel{Provider: "openai", APIStyle: "chat_completions", ExternalModelName: "gpt-test", AccountIDs: []string{"first-exhausted", "second-exhausted"}}
	recorder := httptest.NewRecorder()
	forwardOpenAIChat(recorder, httptest.NewRequest(http.MethodPost, "/v1internal:streamGenerateContent", nil), model, map[string]any{
		"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "hello"}}}},
	}, "pool-exhausted")

	if firstCalls.Load() != 1 || secondCalls.Load() != 0 {
		t.Fatalf("pool calls = first:%d second:%d, permanent failure must stay on the selected account", firstCalls.Load(), secondCalls.Load())
	}
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "余额、额度或权限不足") || !strings.Contains(recorder.Body.String(), `"finishReason":"STOP"`) {
		t.Fatalf("exhausted pool terminated Antigravity instead of completing the turn: %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestCleanPatchedPath(t *testing.T) {
	legacyToken := legacyProxyProductToken()
	cases := map[string]string{
		"/v1internal/antigravity-wf/v1internal:streamGenerateContent":                  "/v1internal:streamGenerateContent",
		"/v1internal/antigravity-wf/v1internal/cascadeNuxes":                           "/v1internal/cascadeNuxes",
		"/v1internal/wfproxy/v1internal:generateContent":                               "/v1internal:generateContent",
		"/v1internal/wfproxy/v1internal/cascadeNuxes":                                  "/v1internal/cascadeNuxes",
		"/v1internal/wfproxy-sandbox/v1internal:fetchAvailableModels":                  "/v1internal:fetchAvailableModels",
		"/v1internal/wfproxy-sandbox/v1internal/cascadeNuxes":                          "/v1internal/cascadeNuxes",
		"/v1internal/antigravity-" + legacyToken + "/v1internal:streamGenerateContent": "/v1internal:streamGenerateContent",
		"/v1internal/antigravity-" + legacyToken + "/v1internal/cascadeNuxes":          "/v1internal/cascadeNuxes",
		"/v1internal/" + legacyToken + "xxx/v1internal:generateContent":                "/v1internal:generateContent",
		"/v1internal/" + legacyToken + "xxx/v1internal/cascadeNuxes":                   "/v1internal/cascadeNuxes",
		"/v1internal/" + legacyToken + "xxx-sandbox/v1internal:fetchAvailableModels":   "/v1internal:fetchAvailableModels",
		"/v1internal/" + legacyToken + "xxx-sandbox/v1internal/cascadeNuxes":           "/v1internal/cascadeNuxes",
		"/v1internal/xxxxxxxxxxxx/v1internal:fetchAvailableModels":                     "/v1internal:fetchAvailableModels",
		"/v1internal/xxxxxxxxxxxx/not-an-antigravity-api":                              "/v1internal/xxxxxxxxxxxx/not-an-antigravity-api",
		"/v1internal:retrieveUserQuota":                                                "/v1internal:retrieveUserQuota",
	}
	for input, want := range cases {
		if got := cleanPatchedPath(input); got != want {
			t.Errorf("cleanPatchedPath(%q) = %q, want %q", input, got, want)
		}
	}
}
