package proxy

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// ─── OpenAI translator ───────────────────────────────────────────────────────

type openAIStreamState struct {
	toolCalls       map[int]map[string]any
	usage           map[string]any
	traceID         string
	responseID      string
	modelVersion    string
	finished        bool
	done            bool
	upstreamStarted bool
	emittedText     strings.Builder
	unsafeOutput    bool
}

// toOpenAIRequest is kept for existing unit tests and callers that only need
// text/tool conversion. The forwarding path uses the error-returning variant
// so an attachment can never disappear without an explicit error.
func toOpenAIRequest(gemini map[string]any, modelName string) map[string]any {
	out, _ := toOpenAIRequestWithMedia(gemini, modelName)
	return out
}

// toOpenAIRequestWithMedia converts a Gemini-style request to OpenAI Chat
// Completions. Image inputs are encoded as data URLs; textual files are kept as
// text. Arbitrary binary files are intentionally rejected here because Chat
// Completions does not offer a general file-input contract (Responses does).
func toOpenAIRequestWithMedia(gemini map[string]any, modelName string) (map[string]any, error) {
	out := map[string]any{
		"model":  modelName,
		"stream": true,
	}

	// Max tokens
	if gen, ok := gemini["generationConfig"].(map[string]any); ok {
		if v, ok := gen["maxOutputTokens"]; ok {
			out["max_tokens"] = v
		}
		if v, ok := gen["temperature"]; ok {
			out["temperature"] = v
		}
		if v, ok := gen["topP"]; ok {
			out["top_p"] = v
		}
	}
	if _, ok := out["max_tokens"]; !ok {
		out["max_tokens"] = 8192
	}

	// System instruction
	var systemContent string
	if si, ok := gemini["systemInstruction"].(map[string]any); ok {
		parts, _ := si["parts"].([]any)
		for _, p := range parts {
			if pm, ok := p.(map[string]any); ok {
				if t, ok := pm["text"].(string); ok {
					systemContent += t
				}
			}
		}
	}

	// Messages
	var messages []map[string]any
	if systemContent != "" {
		messages = append(messages, map[string]any{
			"role":    "system",
			"content": systemContent,
		})
	}

	contents, _ := gemini["contents"].([]any)
	toolCallsByID := newToolCallAssociation()
	for _, c := range contents {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		role, _ := cm["role"].(string)
		if role == "model" {
			role = "assistant"
		} else if role == "" || role == "user" {
			role = "user"
		}
		parts, _ := cm["parts"].([]any)

		var textParts []string
		var contentBlocks []any
		var toolCalls []any
		var toolResponses []any
		hasRichContent := false
		hasToolCall := false

		for _, p := range parts {
			pm, ok := p.(map[string]any)
			if !ok {
				continue
			}
			if t, ok := pm["text"].(string); ok {
				textParts = append(textParts, t)
				contentBlocks = append(contentBlocks, map[string]any{"type": "text", "text": t})
				continue
			}
			if attachment, seen, err := attachmentFromGeminiPart(pm); seen {
				if err != nil {
					return nil, err
				}
				hasRichContent = true
				if err := appendOpenAIChatAttachment(&contentBlocks, attachment); err != nil {
					return nil, err
				}
				continue
			}
			if fc, ok := pm["functionCall"].(map[string]any); ok {
				hasToolCall = true
				name, _ := fc["name"].(string)
				argsRaw, _ := json.Marshal(fc["args"])
				callID := getString(fc, "id", "callId", "call_id")
				if callID == "" {
					callID = "call_" + name
				} else if role == "assistant" {
					toolCallsByID.rememberAssistantCall(name, callID)
				}
				toolCalls = append(toolCalls, map[string]any{
					"type": "function",
					"id":   callID,
					"function": map[string]any{
						"name":      name,
						"arguments": string(argsRaw),
					},
				})
			} else if fr, ok := pm["functionResponse"].(map[string]any); ok {
				name, _ := fr["name"].(string)
				resp, _ := json.Marshal(fr["response"])
				callID := toolCallsByID.resolveResponseID(name, getString(fr, "id", "callId", "call_id"), "call_"+name)
				toolResponses = append(toolResponses, map[string]any{
					"role":         "tool",
					"tool_call_id": callID,
					"content":      string(resp),
				})
			}
		}

		if hasToolCall {
			msg := map[string]any{
				"role":       "assistant",
				"tool_calls": toolCalls,
			}
			if len(textParts) > 0 {
				if hasRichContent {
					msg["content"] = contentBlocks
				} else {
					msg["content"] = strings.Join(textParts, "\n")
				}
			}
			messages = append(messages, msg)
		} else if len(toolResponses) > 0 {
			// Tool responses are separate messages
			for _, block := range toolResponses {
				if bm, ok := block.(map[string]any); ok {
					if bm["role"] == "tool" {
						messages = append(messages, bm)
					}
				}
			}
		} else {
			if hasRichContent {
				if len(contentBlocks) == 0 {
					contentBlocks = append(contentBlocks, map[string]any{"type": "text", "text": " "})
				}
				messages = append(messages, map[string]any{"role": role, "content": contentBlocks})
			} else {
				content := strings.Join(textParts, "\n")
				if content == "" {
					content = " "
				}
				messages = append(messages, map[string]any{"role": role, "content": content})
			}
		}
	}

	out["messages"] = messages

	// Tools
	tools := geminiToolsToOpenAI(gemini)
	if len(tools) > 0 {
		out["tools"] = tools
	}

	return out, nil
}

func appendOpenAIChatAttachment(blocks *[]any, attachment *geminiAttachment) error {
	if attachment.isImage() {
		*blocks = append(*blocks, map[string]any{
			"type":      "image_url",
			"image_url": map[string]any{"url": attachment.dataURL()},
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
	if attachment.isPDF() {
		return fmt.Errorf("PDF 附件需要明确将该模型的 API 模式设为 Responses")
	}
	return fmt.Errorf("%s 附件需要明确将该模型的 API 模式设为 Responses", attachment.MimeType)
}

func geminiToolsToOpenAI(gemini map[string]any) []map[string]any {
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
				"type": "function",
				"function": map[string]any{
					"name":        name,
					"description": desc,
					"parameters":  params,
				},
			})
		}
	}
	return result
}

// convertOpenAILineToGemini converts a single SSE line from OpenAI to Gemini format.
func convertOpenAILineToGemini(line string, state *openAIStreamState) string {
	if state.toolCalls == nil {
		state.toolCalls = map[int]map[string]any{}
	}
	if !strings.HasPrefix(line, "data: ") {
		return ""
	}
	payload := strings.TrimPrefix(line, "data: ")
	payload = strings.TrimSpace(payload)
	if payload == "[DONE]" {
		state.done = true
		state.upstreamStarted = true
		return ""
	}
	if payload == "" {
		return ""
	}

	var chunk map[string]any
	if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
		return ""
	}
	// A valid upstream event means the request may already have started model
	// work, even if it only carries a role, reasoning, or usage update. Never
	// replay such a request after the stream ends unexpectedly.
	state.upstreamStarted = true
	if _, failed := chunk["error"]; failed {
		return ""
	}
	if responseID, ok := chunk["id"].(string); ok && responseID != "" {
		state.responseID = responseID
	}
	if modelVersion, ok := chunk["model"].(string); ok && modelVersion != "" {
		state.modelVersion = modelVersion
	}

	// Capture usage
	if usage, ok := chunk["usage"].(map[string]any); ok {
		state.usage = usage
	}

	choices, _ := chunk["choices"].([]any)
	if len(choices) == 0 {
		return ""
	}

	choice, _ := choices[0].(map[string]any)
	delta, _ := choice["delta"].(map[string]any)

	var parts []map[string]any

	// Text content
	if text, ok := delta["content"].(string); ok && text != "" {
		state.emittedText.WriteString(text)
		parts = append(parts, map[string]any{"text": text})
	}

	// Tool calls
	toolCallsRaw, _ := delta["tool_calls"].([]any)
	for _, tcRaw := range toolCallsRaw {
		tc, ok := tcRaw.(map[string]any)
		if !ok {
			continue
		}
		idx := 0
		if idxF, ok := tc["index"].(float64); ok {
			idx = int(idxF)
		}
		existing := state.toolCalls[idx]
		if existing == nil {
			existing = map[string]any{"id": "", "name": "", "arguments": ""}
			state.toolCalls[idx] = existing
		}
		if callID, ok := tc["id"].(string); ok && callID != "" {
			existing["id"] = callID
		}
		if fn, ok := tc["function"].(map[string]any); ok {
			if name, ok := fn["name"].(string); ok && name != "" {
				existing["name"] = name
			}
			if args, ok := fn["arguments"].(string); ok {
				existing["arguments"] = existing["arguments"].(string) + args
			}
		}
	}

	finishReason, _ := choice["finish_reason"].(string)
	if finishReason != "" {
		state.finished = true
	}
	if finishReason == "tool_calls" || finishReason == "stop" {
		indexes := make([]int, 0, len(state.toolCalls))
		for index := range state.toolCalls {
			indexes = append(indexes, index)
		}
		sort.Ints(indexes)
		for _, index := range indexes {
			tc := state.toolCalls[index]
			name, _ := tc["name"].(string)
			if name == "" {
				continue
			}
			arguments, _ := tc["arguments"].(string)
			argsMap, validArgs := decodeFunctionCallArgs(arguments)
			if !validArgs {
				traceDroppedFunctionCall("openai", state.traceID, getString(tc, "id"), name, arguments)
				continue
			}
			functionCall := map[string]any{"name": name, "args": argsMap}
			if callID, _ := tc["id"].(string); callID != "" {
				functionCall["id"] = callID
			}
			parts = append(parts, map[string]any{
				"functionCall": functionCall,
			})
			state.unsafeOutput = true
		}
		state.toolCalls = map[int]map[string]any{}
	}

	if len(parts) == 0 && finishReason == "" {
		return ""
	}

	response := map[string]any{
		"candidates": []any{
			map[string]any{
				"content": map[string]any{
					"role":  "model",
					"parts": parts,
				},
				"finishReason": finishReasonToGemini(finishReason),
			},
		},
	}
	if state.responseID != "" {
		response["responseId"] = state.responseID
	}
	if state.modelVersion != "" {
		response["modelVersion"] = state.modelVersion
	}
	if state.usage != nil {
		prompt, _ := state.usage["prompt_tokens"].(float64)
		completion, _ := state.usage["completion_tokens"].(float64)
		response["usageMetadata"] = map[string]any{
			"promptTokenCount":     int(prompt),
			"candidatesTokenCount": int(completion),
			"totalTokenCount":      int(prompt + completion),
		}
	}
	return encodeAntigravityStreamEvent(response, state.traceID)
}

func finishReasonToGemini(r string) string {
	switch r {
	case "stop":
		return "STOP"
	case "tool_calls":
		return "STOP"
	case "length":
		return "MAX_TOKENS"
	default:
		return ""
	}
}
