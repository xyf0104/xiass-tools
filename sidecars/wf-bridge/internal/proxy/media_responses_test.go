package proxy

import (
	"encoding/json"
	"strings"
	"testing"

	"antigravity-wf-assistant/internal/storage"
)

func inlinePart(mimeType, data string) map[string]any {
	return map[string]any{"inlineData": map[string]any{"mimeType": mimeType, "data": data}}
}

func TestOpenAIChatKeepsImageAttachment(t *testing.T) {
	gemini := map[string]any{"contents": []any{map[string]any{
		"role": "user", "parts": []any{map[string]any{"text": "看图"}, inlinePart("image/png", "aGVsbG8=")},
	}}}
	request, err := toOpenAIRequestWithMedia(gemini, "gpt-test")
	if err != nil {
		t.Fatalf("conversion failed: %v", err)
	}
	messages := request["messages"].([]map[string]any)
	content, ok := messages[0]["content"].([]any)
	if !ok || len(content) != 2 {
		t.Fatalf("expected text and image content blocks, got %#v", messages[0]["content"])
	}
	image := content[1].(map[string]any)
	if image["type"] != "image_url" || image["image_url"].(map[string]any)["url"] != "data:image/png;base64,aGVsbG8=" {
		t.Fatalf("image attachment was not preserved: %#v", image)
	}
}

func TestOpenAIChatKeepsTextAttachment(t *testing.T) {
	gemini := map[string]any{"contents": []any{map[string]any{
		"role": "user", "parts": []any{
			map[string]any{"text": "分析附件"},
			map[string]any{"inlineData": map[string]any{"mimeType": "text/markdown", "data": "IyBUaXRsZQ==", "displayName": "notes.md"}},
		},
	}}}
	request, err := toOpenAIRequestWithMedia(gemini, "gpt-test")
	if err != nil {
		t.Fatalf("conversion failed: %v", err)
	}
	messages := request["messages"].([]map[string]any)
	content, ok := messages[0]["content"].([]any)
	if !ok || len(content) != 2 {
		t.Fatalf("expected prompt and text attachment blocks, got %#v", messages[0]["content"])
	}
	attachment := content[1].(map[string]any)
	if attachment["type"] != "text" || attachment["text"] != "[附件 notes.md]\n# Title" {
		t.Fatalf("text attachment was not preserved: %#v", attachment)
	}
}

func TestAnthropicKeepsPDF(t *testing.T) {
	gemini := map[string]any{"contents": []any{map[string]any{
		"role": "user", "parts": []any{inlinePart("application/pdf", "cGRm")},
	}}}
	request, err := toAnthropicRequestWithMedia(gemini, "claude-test")
	if err != nil {
		t.Fatalf("conversion failed: %v", err)
	}
	messages := request["messages"].([]map[string]any)
	blocks := messages[0]["content"].([]any)
	document := blocks[0].(map[string]any)
	if document["type"] != "document" || document["source"].(map[string]any)["data"] != "cGRm" {
		t.Fatalf("PDF attachment was not preserved: %#v", document)
	}
}

func TestResponsesKeepsGeneralFileAndTools(t *testing.T) {
	gemini := map[string]any{
		"contents": []any{map[string]any{
			"role": "user", "parts": []any{map[string]any{"text": "分析文件"}, inlinePart("application/pdf", "cGRm")},
		}},
		"tools": []any{map[string]any{"functionDeclarations": []any{map[string]any{
			"name": "read_file", "parameters": map[string]any{"type": "OBJECT", "properties": map[string]any{}},
		}}}},
		"webSearch":       true,
		"imageGeneration": true,
	}
	model := &storage.CustomModel{Provider: "openai", APIStyle: "responses", Capabilities: storage.ModelCapabilities{Configured: true, SupportsWebSearch: true, SupportsImageGeneration: true}}
	request, err := toOpenAIResponsesRequest(gemini, "gpt-test", model)
	if err != nil {
		t.Fatalf("conversion failed: %v", err)
	}
	input := request["input"].([]any)
	blocks := input[0].(map[string]any)["content"].([]any)
	file := blocks[1].(map[string]any)
	if file["type"] != "input_file" || file["file_data"] != "data:application/pdf;base64,cGRm" {
		t.Fatalf("file attachment was not preserved: %#v", file)
	}
	tools := request["tools"].([]map[string]any)
	if len(tools) != 3 || tools[1]["type"] != "web_search" || tools[2]["type"] != "image_generation" {
		t.Fatalf("expected function, web search and image tools: %#v", tools)
	}
}

func TestResponsesAttachmentDoesNotImplicitlyAttachHostedTools(t *testing.T) {
	gemini := map[string]any{"contents": []any{map[string]any{
		"role": "user", "parts": []any{inlinePart("application/pdf", "cGRm")},
	}}}
	model := &storage.CustomModel{Provider: "openai", APIStyle: "responses", Capabilities: storage.ModelCapabilities{Configured: true, SupportsWebSearch: true, SupportsImageGeneration: true}}
	request, err := toOpenAIResponsesRequest(gemini, "gpt-test", model)
	if err != nil {
		t.Fatal(err)
	}
	if _, sent := request["tools"]; sent {
		t.Fatalf("attachment-only request unexpectedly sent hosted tools: %#v", request["tools"])
	}
}

func TestResponsesAddsOnlyTheHostedToolRequestedByThisTurn(t *testing.T) {
	model := &storage.CustomModel{Provider: "openai", APIStyle: "responses", Capabilities: storage.ModelCapabilities{Configured: true, SupportsWebSearch: true, SupportsImageGeneration: true}}
	web, err := toOpenAIResponsesRequest(map[string]any{"webSearch": true}, "gpt-test", model)
	if err != nil {
		t.Fatal(err)
	}
	if !responsesRequestHasTool(web, responseWebSearchTool) || responsesRequestHasTool(web, responseImageGenerationTool) {
		t.Fatalf("web-search turn sent the wrong hosted tools: %#v", web["tools"])
	}
	image, err := toOpenAIResponsesRequest(map[string]any{"imageGeneration": true}, "gpt-test", model)
	if err != nil {
		t.Fatal(err)
	}
	if responsesRequestHasTool(image, responseWebSearchTool) || !responsesRequestHasTool(image, responseImageGenerationTool) {
		t.Fatalf("image-generation turn sent the wrong hosted tools: %#v", image["tools"])
	}
}

func responsesRequestHasTool(request map[string]any, wanted string) bool {
	tools, _ := request["tools"].([]map[string]any)
	for _, tool := range tools {
		if kind, _ := tool["type"].(string); kind == wanted {
			return true
		}
	}
	return false
}

func TestResponsesStreamTextAndImage(t *testing.T) {
	state := &openAIResponsesStreamState{traceID: "test-trace"}
	textEvent := `data: {"type":"response.output_text.delta","response_id":"resp_1","delta":"你好"}`
	converted := convertOpenAIResponsesLineToGemini(textEvent, state)
	if !strings.Contains(converted, "你好") || !strings.Contains(converted, "test-trace") {
		t.Fatalf("text event conversion failed: %s", converted)
	}
	imageEvent := `data: {"type":"response.output_item.done","item":{"type":"image_generation_call","result":"aW1hZ2U="}}`
	converted = convertOpenAIResponsesLineToGemini(imageEvent, state)
	if !strings.Contains(converted, `"inlineData"`) || !strings.Contains(converted, "aW1hZ2U=") {
		t.Fatalf("image event conversion failed: %s", converted)
	}
	completed := convertOpenAIResponsesLineToGemini(`data: {"type":"response.completed","response":{"id":"resp_1","model":"gpt-test","usage":{"input_tokens":3,"output_tokens":2}}}`, state)
	var envelope map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSuffix(strings.TrimPrefix(completed, "data: "), "\n\n")), &envelope); err != nil {
		t.Fatalf("invalid completion envelope: %v", err)
	}
	finish := envelope["response"].(map[string]any)["candidates"].([]any)[0].(map[string]any)["finishReason"]
	if finish != "STOP" {
		t.Fatalf("expected STOP, got %#v", finish)
	}
}

func TestResponsesCompletedOnlyTextIsForwardedOnce(t *testing.T) {
	state := &openAIResponsesStreamState{traceID: "completed-only"}
	completed := convertOpenAIResponsesLineToGemini(`data: {"type":"response.completed","response":{"id":"resp_2","model":"gpt-test","output":[{"type":"message","content":[{"type":"output_text","text":"完整回复"}]}]}}`, state)
	if !strings.Contains(completed, "完整回复") || !strings.Contains(completed, `"finishReason":"STOP"`) {
		t.Fatalf("completed-only response was not converted: %s", completed)
	}

	state = &openAIResponsesStreamState{traceID: "dedupe-final"}
	first := convertOpenAIResponsesLineToGemini(`data: {"type":"response.output_text.delta","delta":"已经"}`, state)
	last := convertOpenAIResponsesLineToGemini(`data: {"type":"response.completed","response":{"output":[{"type":"message","content":[{"type":"output_text","text":"已经回复"}]}]}}`, state)
	if !strings.Contains(first, "已经") || !strings.Contains(last, "回复") || strings.Contains(last, "已经回复") {
		t.Fatalf("final response duplicated streamed text: first=%s last=%s", first, last)
	}
}

func TestResponsesInvalidFunctionArgumentsAreDropped(t *testing.T) {
	state := &openAIResponsesStreamState{traceID: "invalid-responses-tool"}
	convertOpenAIResponsesLineToGemini(`data: {"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_bad","name":"search","arguments":"not-json"}}`, state)
	out := convertOpenAIResponsesLineToGemini(`data: {"type":"response.function_call_arguments.done","call_id":"call_bad"}`, state)
	if out != "" {
		if strings.Contains(out, "functionCall") || state.unsafeOutput {
			t.Fatalf("invalid Responses tool arguments were emitted: %s", out)
		}
	}
	completed := convertOpenAIResponsesLineToGemini(`data: {"type":"response.completed","response":{"output":[]}}`, state)
	if completed == "" || !strings.Contains(completed, `"finishReason":"STOP"`) {
		t.Fatalf("invalid Responses tool stream did not terminate cleanly: %s", completed)
	}
}
