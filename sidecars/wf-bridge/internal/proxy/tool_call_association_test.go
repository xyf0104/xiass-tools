package proxy

import (
	"testing"

	"antigravity-wf-assistant/internal/storage"
)

func toolAssociationGeminiRequest() map[string]any {
	return map[string]any{
		"contents": []any{
			map[string]any{
				"role":  "user",
				"parts": []any{map[string]any{"text": "read the project file"}},
			},
			map[string]any{
				"role": "model",
				"parts": []any{map[string]any{
					"functionCall": map[string]any{
						"id":   "call_real_read_file",
						"name": "read_file",
						"args": map[string]any{"path": "README.md"},
					},
				}},
			},
			map[string]any{
				"role": "user",
				"parts": []any{map[string]any{
					// Antigravity can omit this ID on a later request. The
					// translation must retain the original upstream call ID.
					"functionResponse": map[string]any{
						"name":     "read_file",
						"response": map[string]any{"contents": "ok"},
					},
				}},
			},
		},
	}
}

func TestOpenAIChatAssociatesMissingToolResponseID(t *testing.T) {
	request, err := toOpenAIRequestWithMedia(toolAssociationGeminiRequest(), "gpt-test")
	if err != nil {
		t.Fatalf("conversion failed: %v", err)
	}
	messages := request["messages"].([]map[string]any)
	if len(messages) != 3 {
		t.Fatalf("messages = %#v", messages)
	}
	if got := messages[2]["tool_call_id"]; got != "call_real_read_file" {
		t.Fatalf("tool_call_id = %v, want the assistant call ID", got)
	}
}

func TestOpenAIResponsesAssociatesMissingToolResponseID(t *testing.T) {
	request, err := toOpenAIResponsesRequest(toolAssociationGeminiRequest(), "gpt-test", &storage.CustomModel{})
	if err != nil {
		t.Fatalf("conversion failed: %v", err)
	}
	input := request["input"].([]any)
	if len(input) != 3 {
		t.Fatalf("input = %#v", input)
	}
	output, ok := input[2].(map[string]any)
	if !ok {
		t.Fatalf("function output = %#v", input[2])
	}
	if got := output["call_id"]; got != "call_real_read_file" {
		t.Fatalf("call_id = %v, want the assistant call ID", got)
	}
}

func TestAnthropicAssociatesMissingToolResponseID(t *testing.T) {
	request, err := toAnthropicRequestWithMedia(toolAssociationGeminiRequest(), "claude-test")
	if err != nil {
		t.Fatalf("conversion failed: %v", err)
	}
	messages := request["messages"].([]map[string]any)
	if len(messages) != 3 {
		t.Fatalf("messages = %#v", messages)
	}
	blocks := messages[2]["content"].([]any)
	result, ok := blocks[0].(map[string]any)
	if !ok {
		t.Fatalf("tool result = %#v", blocks[0])
	}
	if got := result["tool_use_id"]; got != "call_real_read_file" {
		t.Fatalf("tool_use_id = %v, want the assistant call ID", got)
	}
}

func TestToolCallAssociationPreservesExplicitAndConsumesOnlyOnce(t *testing.T) {
	association := newToolCallAssociation()
	association.rememberAssistantCall("read_file", "call_old")
	association.rememberAssistantCall("read_file", "call_new")

	if got := association.resolveResponseID("read_file", "call_explicit", "fallback"); got != "call_explicit" {
		t.Fatalf("explicit response ID = %q", got)
	}
	if got := association.resolveResponseID("read_file", "", "fallback"); got != "call_new" {
		t.Fatalf("nearest missing-ID response = %q, want call_new", got)
	}
	if got := association.resolveResponseID("read_file", "", "fallback"); got != "call_old" {
		t.Fatalf("second missing-ID response = %q, want call_old", got)
	}
	if got := association.resolveResponseID("read_file", "", "fallback"); got != "fallback" {
		t.Fatalf("unmatched missing-ID response = %q, want fallback", got)
	}
}

func TestToolCallAssociationConsumesMatchingExplicitID(t *testing.T) {
	association := newToolCallAssociation()
	association.rememberAssistantCall("read_file", "call_real")
	if got := association.resolveResponseID("read_file", "call_real", "fallback"); got != "call_real" {
		t.Fatalf("explicit response ID = %q", got)
	}
	if got := association.resolveResponseID("read_file", "", "fallback"); got != "fallback" {
		t.Fatalf("consumed call was reused as %q", got)
	}
}
