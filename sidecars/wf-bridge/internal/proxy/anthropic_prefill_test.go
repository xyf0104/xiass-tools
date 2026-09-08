package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"antigravity-wf-assistant/internal/storage"
)

func TestAnthropicRequestDropsOnlyTerminalAssistantPrefill(t *testing.T) {
	gemini := map[string]any{
		"contents": []any{
			map[string]any{"role": "user", "parts": []any{map[string]any{"text": "first question"}}},
			map[string]any{"role": "model", "parts": []any{map[string]any{"text": "first answer"}}},
			map[string]any{"role": "user", "parts": []any{map[string]any{"text": "second question"}}},
			// Antigravity can retain this unfinished local prefill during a
			// resume/retry. Strict Messages gateways reject it as the final turn.
			map[string]any{"role": "model", "parts": []any{map[string]any{"text": "unfinished prefill"}}},
		},
	}

	request, err := toAnthropicRequestWithMedia(gemini, "claude-test")
	if err != nil {
		t.Fatal(err)
	}
	messages := request["messages"].([]map[string]any)
	if len(messages) != 3 {
		t.Fatalf("messages = %#v, want three retained turns", messages)
	}
	for index, want := range []string{"user", "assistant", "user"} {
		if got := messages[index]["role"]; got != want {
			t.Fatalf("messages[%d].role = %q, want %q", index, got, want)
		}
	}
	previous := messages[1]["content"].([]any)[0].(map[string]any)["text"]
	if previous != "first answer" {
		t.Fatalf("earlier assistant context was changed: %#v", messages)
	}
	if last := messages[len(messages)-1]["role"]; last != "user" {
		t.Fatalf("strict upstream requires a user final message, got %#v", messages)
	}
}

func TestAnthropicRequestCombinesConsecutiveGeminiRoles(t *testing.T) {
	gemini := map[string]any{
		"contents": []any{
			map[string]any{"role": "user", "parts": []any{map[string]any{"text": "first"}}},
			map[string]any{"role": "user", "parts": []any{map[string]any{"text": "second"}}},
		},
	}

	request, err := toAnthropicRequestWithMedia(gemini, "claude-test")
	if err != nil {
		t.Fatal(err)
	}
	messages := request["messages"].([]map[string]any)
	if len(messages) != 1 || messages[0]["role"] != "user" {
		t.Fatalf("consecutive user turns were not combined: %#v", messages)
	}
	if got := len(messages[0]["content"].([]any)); got != 2 {
		t.Fatalf("combined content blocks = %d, want 2", got)
	}
}

func TestAnthropicRequestPreservesAssistantAliasAndDropsOrphanedLeadingPrefill(t *testing.T) {
	gemini := map[string]any{
		"contents": []any{
			// A pruned/restored history can start on an assistant turn. Strict
			// Messages gateways reject that shape, so it must not be relabelled as
			// a user instruction.
			map[string]any{"role": "assistant", "parts": []any{map[string]any{"text": "orphaned answer"}}},
			map[string]any{"role": "user", "parts": []any{map[string]any{"text": "actual question"}}},
			// Some compatibility paths use the OpenAI spelling for past output.
			map[string]any{"role": "assistant", "parts": []any{map[string]any{"text": "past answer"}}},
			map[string]any{"role": "user", "parts": []any{map[string]any{"text": "follow up"}}},
		},
	}

	request, err := toAnthropicRequestWithMedia(gemini, "claude-test")
	if err != nil {
		t.Fatal(err)
	}
	messages := request["messages"].([]map[string]any)
	if len(messages) != 3 {
		t.Fatalf("messages = %#v, want user/assistant/user", messages)
	}
	for index, want := range []string{"user", "assistant", "user"} {
		if got := messages[index]["role"]; got != want {
			t.Fatalf("messages[%d].role = %q, want %q", index, got, want)
		}
	}
	if got := messages[1]["content"].([]any)[0].(map[string]any)["text"]; got != "past answer" {
		t.Fatalf("assistant alias was not preserved: %#v", messages)
	}
}

func TestForwardAnthropicSatisfiesStrictNoAssistantPrefillGateway(t *testing.T) {
	requests := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/v1/messages" {
			t.Fatalf("Messages endpoint = %q", r.URL.Path)
		}
		var received map[string]any
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatal(err)
		}
		messages, _ := received["messages"].([]any)
		if len(messages) == 0 {
			http.Error(w, "messages are required", http.StatusBadRequest)
			return
		}
		if first, _ := messages[0].(map[string]any); first["role"] != "user" {
			http.Error(w, "first message must be user", http.StatusBadRequest)
			return
		}
		if last, _ := messages[len(messages)-1].(map[string]any); last["role"] == "assistant" {
			http.Error(w, "assistant-prefill final message is not supported; last message must be user", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"message_start\",\"message\":{\"id\":\"strict-prefill\",\"model\":\"claude-test\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"message_stop\"}\n\n")
	}))
	defer upstream.Close()

	model := &storage.CustomModel{Provider: "anthropic", APIURL: upstream.URL, APIKey: "test-key", ExternalModelName: "claude-test"}
	gemini := map[string]any{"contents": []any{
		map[string]any{"role": "user", "parts": []any{map[string]any{"text": "question"}}},
		map[string]any{"role": "model", "parts": []any{map[string]any{"text": "unfinished prefill"}}},
	}}
	recorder := httptest.NewRecorder()
	forwardAnthropic(recorder, httptest.NewRequest(http.MethodPost, "/generate", nil), model, gemini, "strict-prefill")

	if requests != 1 {
		t.Fatalf("upstream requests = %d, want 1", requests)
	}
	if recorder.Code != http.StatusOK || recorder.Body.Len() == 0 {
		t.Fatalf("strict gateway response = %d %s", recorder.Code, recorder.Body.String())
	}
}
