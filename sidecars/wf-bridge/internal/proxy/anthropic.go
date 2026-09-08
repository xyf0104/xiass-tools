package proxy

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ─── Anthropic translator ────────────────────────────────────────────────────

type anthropicStreamState struct {
	toolCalls       map[int]*anthropicToolCall
	traceID         string
	responseID      string
	modelVersion    string
	finished        bool
	upstreamStarted bool
	emittedText     strings.Builder
	unsafeOutput    bool
}

type anthropicToolCall struct {
	name               string
	id                 string
	input              strings.Builder
	receivedInputDelta bool
}

type anthropicUsageTotals struct {
	input      int
	output     int
	cacheRead  int
	cacheWrite int
	seen       bool
}

// toAnthropicRequest is retained for existing tests. Runtime forwarding uses
// toAnthropicRequestWithMedia so attachment conversion failures are surfaced to
// the user instead of becoming a silently text-only request.
func toAnthropicRequest(gemini map[string]any, modelName string) map[string]any {
	out, _ := toAnthropicRequestWithMedia(gemini, modelName)
	return out
}

// toAnthropicRequestWithMedia converts Gemini parts into the Anthropic
// Messages content-block format, including base64 images and PDF documents.
func toAnthropicRequestWithMedia(gemini map[string]any, modelName string) (map[string]any, error) {
	out := map[string]any{
		"model":  modelName,
		"stream": true,
	}

	if gen, ok := gemini["generationConfig"].(map[string]any); ok {
		if v, ok := gen["maxOutputTokens"]; ok {
			out["max_tokens"] = v
		}
		if v, ok := gen["temperature"]; ok {
			out["temperature"] = v
		}
	}
	if _, ok := out["max_tokens"]; !ok {
		out["max_tokens"] = 8192
	}

	// System prompt. Cache breakpoints are applied immediately before the
	// upstream request so they can be removed and retried on providers that do
	// not implement Anthropic prompt caching.
	if si, ok := gemini["systemInstruction"].(map[string]any); ok {
		var systemText string
		parts, _ := si["parts"].([]any)
		for _, p := range parts {
			if pm, ok := p.(map[string]any); ok {
				if t, ok := pm["text"].(string); ok {
					systemText += t
				}
			}
		}
		if systemText != "" {
			out["system"] = systemText
		}
	}

	// Messages
	var messages []map[string]any
	contents, _ := gemini["contents"].([]any)
	toolCallsByID := newToolCallAssociation()
	for _, c := range contents {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		role, _ := cm["role"].(string)
		switch strings.ToLower(strings.TrimSpace(role)) {
		case "model", "assistant":
			// Gemini calls this role "model", while compatibility shims can
			// retain the OpenAI spelling "assistant" in restored history.
			// Both describe prior model output and must retain that meaning.
			role = "assistant"
		default:
			role = "user"
		}

		parts, _ := cm["parts"].([]any)
		var blocks []any

		for _, p := range parts {
			pm, ok := p.(map[string]any)
			if !ok {
				continue
			}
			if t, ok := pm["text"].(string); ok && t != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": t})
				continue
			}
			if attachment, seen, err := attachmentFromGeminiPart(pm); seen {
				if err != nil {
					return nil, err
				}
				if err := appendAnthropicAttachment(&blocks, attachment); err != nil {
					return nil, err
				}
				continue
			}
			if fc, ok := pm["functionCall"].(map[string]any); ok {
				name, _ := fc["name"].(string)
				callID := getString(fc, "id", "callId", "call_id")
				if callID == "" {
					callID = "toolu_" + name
				} else if role == "assistant" {
					toolCallsByID.rememberAssistantCall(name, callID)
				}
				blocks = append(blocks, map[string]any{
					"type":  "tool_use",
					"id":    callID,
					"name":  name,
					"input": fc["args"],
				})
			} else if fr, ok := pm["functionResponse"].(map[string]any); ok {
				name, _ := fr["name"].(string)
				respJSON, _ := json.Marshal(fr["response"])
				callID := toolCallsByID.resolveResponseID(name, getString(fr, "id", "callId", "call_id"), "toolu_"+name)
				blocks = append(blocks, map[string]any{
					"type":        "tool_result",
					"tool_use_id": callID,
					"content":     string(respJSON),
				})
			}
		}

		if len(blocks) == 0 {
			blocks = append(blocks, map[string]any{"type": "text", "text": " "})
		}

		messages = append(messages, map[string]any{
			"role":    role,
			"content": blocks,
		})
	}

	// Some Anthropic-compatible gateways reject a terminal assistant turn
	// ("assistant prefill"), even though the first-party API historically
	// accepted it. Antigravity can include an unfinished model turn when it
	// retries or resumes a generation. It is not a new user instruction, so
	// dropping only that terminal prefill is safer than inventing a synthetic
	// "continue" prompt, which can both waste tokens and repeat an answer.
	// Earlier assistant turns remain intact as conversation context.
	messages = normalizeAnthropicMessages(messages)
	out["messages"] = messages

	// Tools
	tools := geminiToolsToAnthropic(gemini)
	if len(tools) > 0 {
		out["tools"] = tools
	}

	return out, nil
}

// normalizeAnthropicMessages produces an alternating Messages history that is
// accepted by strict Anthropic-compatible upstreams. In particular, an
// incomplete terminal assistant prefill is omitted so the request ends with a
// user turn. This function never changes the content of retained turns.
func normalizeAnthropicMessages(messages []map[string]any) []map[string]any {
	normalized := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		role := getString(message, "role")
		if role != "assistant" {
			role = "user"
		}
		content, _ := message["content"].([]any)
		if len(content) == 0 {
			content = []any{map[string]any{"type": "text", "text": " "}}
		}

		// A strict Messages request must start with a user turn. Context
		// compaction can leave an orphaned leading assistant turn; it cannot be
		// represented faithfully in this API, so omit it rather than relabeling
		// model output as a user instruction.
		if len(normalized) == 0 && role == "assistant" {
			continue
		}

		// Consecutive role entries are valid Gemini history but are rejected by
		// a number of Anthropic-compatible implementations. Combining their
		// block arrays preserves order without fabricating any text.
		if len(normalized) > 0 && getString(normalized[len(normalized)-1], "role") == role {
			previous, _ := normalized[len(normalized)-1]["content"].([]any)
			normalized[len(normalized)-1]["content"] = append(previous, content...)
			continue
		}
		normalized = append(normalized, map[string]any{"role": role, "content": content})
	}

	if len(normalized) > 0 && getString(normalized[len(normalized)-1], "role") == "assistant" {
		normalized = normalized[:len(normalized)-1]
	}
	if len(normalized) == 0 {
		return []map[string]any{{"role": "user", "content": []any{map[string]any{"type": "text", "text": " "}}}}
	}
	return normalized
}

func appendAnthropicAttachment(blocks *[]any, attachment *geminiAttachment) error {
	if attachment.isImage() {
		*blocks = append(*blocks, map[string]any{
			"type": "image",
			"source": map[string]any{
				"type": "base64", "media_type": attachment.MimeType, "data": attachment.Data,
			},
		})
		return nil
	}
	if attachment.isPDF() {
		*blocks = append(*blocks, map[string]any{
			"type": "document",
			"source": map[string]any{
				"type": "base64", "media_type": attachment.MimeType, "data": attachment.Data,
			},
		})
		return nil
	}
	if attachment.isText() {
		text, err := attachment.text()
		if err != nil {
			return err
		}
		if attachment.Filename != "" {
			text = "[附件 " + attachment.Filename + "]\n" + text
		}
		*blocks = append(*blocks, map[string]any{"type": "text", "text": text})
		return nil
	}
	return fmt.Errorf("Anthropic Messages 不支持直接转发 %s 附件；请使用 PDF、图片或文本文件", attachment.MimeType)
}

func geminiToolsToAnthropic(gemini map[string]any) []map[string]any {
	rawTools, ok := gemini["tools"].([]any)
	if !ok {
		return nil
	}
	var result []map[string]any
	for _, rt := range rawTools {
		tm, ok := rt.(map[string]any)
		if !ok {
			continue
		}
		fds, _ := tm["functionDeclarations"].([]any)
		for _, fd := range fds {
			fdm, ok := fd.(map[string]any)
			if !ok {
				continue
			}
			name, _ := fdm["name"].(string)
			desc, _ := fdm["description"].(string)
			params, _ := fdm["parameters"].(map[string]any)
			if params == nil {
				params = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			params = normalizeJSONSchema(params).(map[string]any)
			result = append(result, map[string]any{
				"name":         name,
				"description":  desc,
				"input_schema": params,
			})
		}
	}
	return result
}

// collectAnthropicUsage accumulates token usage from Anthropic SSE events.
func collectAnthropicUsage(line string, totals *anthropicUsageTotals) {
	if !strings.HasPrefix(line, "data: ") {
		return
	}
	payload := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
	if payload == "" || payload == "[DONE]" {
		return
	}
	var event map[string]any
	if err := json.Unmarshal([]byte(payload), &event); err != nil {
		return
	}

	extract := func(usage map[string]any) {
		totals.seen = true
		if v, ok := usage["input_tokens"].(float64); ok {
			totals.input += int(v)
		}
		if v, ok := usage["output_tokens"].(float64); ok {
			totals.output += int(v)
		}
		if v, ok := usage["cache_read_input_tokens"].(float64); ok {
			totals.cacheRead += int(v)
		}
		if v, ok := usage["cache_creation_input_tokens"].(float64); ok {
			totals.cacheWrite += int(v)
		}
	}

	if msg, ok := event["message"].(map[string]any); ok {
		if usage, ok := msg["usage"].(map[string]any); ok {
			extract(usage)
		}
	}
	if usage, ok := event["usage"].(map[string]any); ok {
		extract(usage)
	}
}

// convertAnthropicLineToGemini converts an Anthropic SSE event to Gemini format.
func convertAnthropicLineToGemini(line string, state *anthropicStreamState) string {
	if !strings.HasPrefix(line, "data: ") {
		return ""
	}
	payload := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
	if payload == "" {
		return ""
	}
	if payload == "[DONE]" {
		state.upstreamStarted = true
		return ""
	}

	var event map[string]any
	if err := json.Unmarshal([]byte(payload), &event); err != nil {
		return ""
	}
	// Message-start, thinking, tool and usage events are all proof that the
	// upstream accepted this generation, even when they carry no visible text.
	state.upstreamStarted = true

	eventType, _ := event["type"].(string)
	var parts []map[string]any
	if message, ok := event["message"].(map[string]any); ok {
		if responseID, ok := message["id"].(string); ok && responseID != "" {
			state.responseID = responseID
		}
		if modelVersion, ok := message["model"].(string); ok && modelVersion != "" {
			state.modelVersion = modelVersion
		}
	}

	switch eventType {
	case "content_block_start":
		if block, ok := event["content_block"].(map[string]any); ok {
			if block["type"] == "tool_use" {
				if state.toolCalls == nil {
					state.toolCalls = map[int]*anthropicToolCall{}
				}
				index := anthropicContentBlockIndex(event)
				name, _ := block["name"].(string)
				callID, _ := block["id"].(string)
				if callID == "" {
					callID = fmt.Sprintf("toolu_%d_%s", index, name)
				}
				call := &anthropicToolCall{name: name, id: callID}
				// Some Anthropic-compatible gateways put the complete tool input
				// on content_block_start and omit input_json_delta entirely.
				// Preserve that object instead of later emitting an empty args map.
				if initial, ok := marshalFunctionCallArgs(block["input"]); ok {
					call.input.WriteString(initial)
				}
				state.toolCalls[index] = call
			}
		}

	case "content_block_delta":
		if delta, ok := event["delta"].(map[string]any); ok {
			deltaType, _ := delta["type"].(string)
			if deltaType == "text_delta" {
				if text, ok := delta["text"].(string); ok && text != "" {
					state.emittedText.WriteString(text)
					parts = append(parts, map[string]any{"text": text})
				}
			} else if deltaType == "input_json_delta" {
				if partial, ok := delta["partial_json"].(string); ok {
					if call := state.toolCalls[anthropicContentBlockIndex(event)]; call != nil {
						// An initial input object is a complete snapshot. If the
						// provider subsequently starts streaming deltas, those
						// deltas are the authoritative JSON and must replace it,
						// not be concatenated after "{}".
						if !call.receivedInputDelta {
							call.input.Reset()
							call.receivedInputDelta = true
						}
						call.input.WriteString(partial)
					}
				}
			}
		}

	case "content_block_stop":
		index := anthropicContentBlockIndex(event)
		if call := state.toolCalls[index]; call != nil && call.name != "" {
			if args, ok := decodeFunctionCallArgs(call.input.String()); ok {
				parts = append(parts, map[string]any{
					"functionCall": map[string]any{
						"id":   call.id,
						"name": call.name,
						"args": args,
					},
				})
				state.unsafeOutput = true
			} else {
				traceDroppedFunctionCall("anthropic", state.traceID, call.id, call.name, call.input.String())
			}
			delete(state.toolCalls, index)
		}

	case "message_stop":
		state.finished = true
		response := map[string]any{
			"candidates": []any{
				map[string]any{
					"content":      map[string]any{"role": "model", "parts": []any{}},
					"finishReason": "STOP",
				},
			},
		}
		if state.responseID != "" {
			response["responseId"] = state.responseID
		}
		if state.modelVersion != "" {
			response["modelVersion"] = state.modelVersion
		}
		return encodeAntigravityStreamEvent(response, state.traceID)
	}

	if len(parts) == 0 {
		return ""
	}

	response := map[string]any{
		"candidates": []any{
			map[string]any{
				"content": map[string]any{
					"role":  "model",
					"parts": parts,
				},
			},
		},
	}
	if state.responseID != "" {
		response["responseId"] = state.responseID
	}
	if state.modelVersion != "" {
		response["modelVersion"] = state.modelVersion
	}
	return encodeAntigravityStreamEvent(response, state.traceID)
}

func anthropicContentBlockIndex(event map[string]any) int {
	if index, ok := numberAsInt(event["index"]); ok && index >= 0 {
		return index
	}
	return 0
}
