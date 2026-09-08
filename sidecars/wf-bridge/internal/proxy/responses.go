package proxy

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"antigravity-wf-assistant/internal/storage"
)

const (
	responseWebSearchTool       = "web_search"
	responseImageGenerationTool = "image_generation"
)

// A Responses endpoint can be perfectly usable for text/files while not
// offering OpenAI's hosted web-search or image-generation tools. Remember a
// concrete capability rejection per upstream credential scope so subsequent
// turns do not first pay the latency of an avoidable validation error.
//
// This is deliberately process-local. An endpoint can gain a capability after
// an account or gateway is reconfigured, and a restart is a cheap reprobe.
var responsesBuiltinToolCompatibility = struct {
	sync.RWMutex
	unsupported map[string]map[string]struct{}
}{
	unsupported: map[string]map[string]struct{}{},
}

// OpenAI Responses is the upstream surface that can represent general file
// inputs, hosted web search and image generation. It is selected only when the
// user explicitly chooses Responses or when a Codex OAuth credential requires
// that surface; generic automatic routing remains on Chat Completions.
func toOpenAIResponsesRequest(gemini map[string]any, modelName string, model *storage.CustomModel) (map[string]any, error) {
	out := map[string]any{
		"model":  modelName,
		"stream": true,
		// Antigravity owns the local conversation transcript. Avoid creating a
		// second, provider-side durable conversation by default.
		"store": false,
	}
	if gen, ok := gemini["generationConfig"].(map[string]any); ok {
		if value, ok := gen["maxOutputTokens"]; ok {
			out["max_output_tokens"] = value
		}
		if value, ok := gen["temperature"]; ok {
			out["temperature"] = value
		}
		if value, ok := gen["topP"]; ok {
			out["top_p"] = value
		}
	}
	if _, ok := out["max_output_tokens"]; !ok {
		out["max_output_tokens"] = 8192
	}

	if system := geminiSystemText(gemini); system != "" {
		out["instructions"] = system
	}

	var input []any
	contents, _ := gemini["contents"].([]any)
	toolCallsByID := newToolCallAssociation()
	for _, rawContent := range contents {
		content, ok := rawContent.(map[string]any)
		if !ok {
			continue
		}
		role, _ := content["role"].(string)
		if role == "model" {
			role = "assistant"
		} else if role == "" {
			role = "user"
		}
		parts, _ := content["parts"].([]any)
		var blocks []any
		var items []any
		for _, rawPart := range parts {
			part, ok := rawPart.(map[string]any)
			if !ok {
				continue
			}
			if text, ok := part["text"].(string); ok {
				blocks = append(blocks, responseTextBlock(role, text))
				continue
			}
			if attachment, seen, err := attachmentFromGeminiPart(part); seen {
				if err != nil {
					return nil, err
				}
				if role == "assistant" {
					return nil, fmt.Errorf("不能将历史助手附件转换为 Responses 输入")
				}
				blocks = append(blocks, responseAttachmentBlock(attachment))
				continue
			}
			if functionCall, ok := part["functionCall"].(map[string]any); ok {
				name, _ := functionCall["name"].(string)
				arguments, _ := json.Marshal(functionCall["args"])
				callID := getString(functionCall, "id", "callId", "call_id")
				if callID == "" {
					callID = "call_" + name
				} else if role == "assistant" {
					toolCallsByID.rememberAssistantCall(name, callID)
				}
				items = append(items, map[string]any{
					"type": "function_call", "call_id": callID, "name": name, "arguments": string(arguments),
				})
				continue
			}
			if functionResponse, ok := part["functionResponse"].(map[string]any); ok {
				name, _ := functionResponse["name"].(string)
				output, _ := json.Marshal(functionResponse["response"])
				callID := toolCallsByID.resolveResponseID(name, getString(functionResponse, "id", "callId", "call_id"), "call_"+name)
				items = append(items, map[string]any{
					"type": "function_call_output", "call_id": callID, "output": string(output),
				})
			}
		}
		if len(blocks) > 0 {
			input = append(input, map[string]any{"role": role, "content": blocks})
		}
		input = append(input, items...)
	}
	if len(input) == 0 {
		input = append(input, map[string]any{"role": "user", "content": []any{responseTextBlock("user", " ")}})
	}
	out["input"] = input

	tools := geminiToolsToOpenAIResponses(gemini)
	capabilities := storage.EffectiveCapabilities(*model)
	requestedBuiltinTools := requestedResponsesBuiltinTools(gemini)
	if capabilities.SupportsWebSearch {
		_, requested := requestedBuiltinTools[responseWebSearchTool]
		if requested {
			tools = append(tools, map[string]any{"type": responseWebSearchTool})
		}
	}
	if capabilities.SupportsImageGeneration {
		_, requested := requestedBuiltinTools[responseImageGenerationTool]
		if requested {
			tools = append(tools, map[string]any{"type": responseImageGenerationTool})
		}
	}
	if len(tools) > 0 {
		out["tools"] = tools
	}
	return out, nil
}

func responsesBuiltinToolCompatibilityKey(model *storage.CustomModel) string {
	return promptCacheCompatibilityKey("responses-builtin-tools", model)
}

func knownUnsupportedResponsesBuiltinTools(model *storage.CustomModel) map[string]struct{} {
	key := responsesBuiltinToolCompatibilityKey(model)
	if key == "" {
		return nil
	}
	responsesBuiltinToolCompatibility.RLock()
	known := responsesBuiltinToolCompatibility.unsupported[key]
	result := make(map[string]struct{}, len(known))
	for tool := range known {
		result[tool] = struct{}{}
	}
	responsesBuiltinToolCompatibility.RUnlock()
	return result
}

func rememberUnsupportedResponsesBuiltinTools(model *storage.CustomModel, tools map[string]struct{}) {
	if len(tools) == 0 {
		return
	}
	key := responsesBuiltinToolCompatibilityKey(model)
	if key == "" {
		return
	}
	responsesBuiltinToolCompatibility.Lock()
	if responsesBuiltinToolCompatibility.unsupported[key] == nil {
		responsesBuiltinToolCompatibility.unsupported[key] = map[string]struct{}{}
	}
	for tool := range tools {
		responsesBuiltinToolCompatibility.unsupported[key][tool] = struct{}{}
	}
	responsesBuiltinToolCompatibility.Unlock()
}

func clearResponsesBuiltinToolCompatibility(model *storage.CustomModel) {
	key := responsesBuiltinToolCompatibilityKey(model)
	if key == "" {
		return
	}
	responsesBuiltinToolCompatibility.Lock()
	delete(responsesBuiltinToolCompatibility.unsupported, key)
	responsesBuiltinToolCompatibility.Unlock()
}

// responseRequestForModel returns a shallow request copy with only the
// built-in Responses tools known to be unavailable for this selected account
// removed. User-provided function tools are never silently stripped.
func responseRequestForModel(base map[string]any, model *storage.CustomModel) (map[string]any, []string) {
	return responseRequestWithoutBuiltinTools(base, knownUnsupportedResponsesBuiltinTools(model))
}

func responseRequestWithoutBuiltinTools(base map[string]any, unavailable map[string]struct{}) (map[string]any, []string) {
	if len(unavailable) == 0 {
		return base, nil
	}
	rawTools, ok := base["tools"].([]map[string]any)
	if !ok || len(rawTools) == 0 {
		return base, nil
	}
	filtered := make([]map[string]any, 0, len(rawTools))
	removed := make([]string, 0, len(unavailable))
	for _, tool := range rawTools {
		kind, _ := tool["type"].(string)
		if _, omit := unavailable[kind]; omit && isResponsesBuiltinTool(kind) {
			removed = append(removed, kind)
			continue
		}
		filtered = append(filtered, tool)
	}
	if len(removed) == 0 {
		return base, nil
	}
	request := make(map[string]any, len(base))
	for key, value := range base {
		request[key] = value
	}
	if len(filtered) == 0 {
		delete(request, "tools")
	} else {
		request["tools"] = filtered
	}
	return request, removed
}

func responseBuiltinToolsInRequest(request map[string]any) map[string]struct{} {
	rawTools, _ := request["tools"].([]map[string]any)
	tools := make(map[string]struct{}, 2)
	for _, tool := range rawTools {
		kind, _ := tool["type"].(string)
		if isResponsesBuiltinTool(kind) {
			tools[kind] = struct{}{}
		}
	}
	return tools
}

func isResponsesBuiltinTool(kind string) bool {
	return kind == responseWebSearchTool || kind == responseImageGenerationTool
}

func responseBuiltinToolNames(tools map[string]struct{}) []string {
	names := make([]string, 0, len(tools))
	for tool := range tools {
		names = append(names, tool)
	}
	sort.Strings(names)
	return names
}

// rejectedResponsesBuiltinTools recognises an explicit validation rejection
// for one of our optional hosted tools. It intentionally ignores generic
// function-schema errors: stripping web/image tools cannot repair those and
// retrying would obscure the real error.
func rejectedResponsesBuiltinTools(statusCode int, body string, request map[string]any) map[string]struct{} {
	if statusCode != 400 && statusCode != 422 {
		return nil
	}
	available := responseBuiltinToolsInRequest(request)
	if len(available) == 0 {
		return nil
	}
	message := strings.ToLower(body)
	if !responsesFeatureRejected(message) {
		return nil
	}
	rejected := make(map[string]struct{}, len(available))
	for tool := range available {
		if responsesBuiltinToolMentioned(message, tool) {
			rejected[tool] = struct{}{}
		}
	}
	if len(rejected) > 0 {
		return rejected
	}
	// Some OpenAI-compatible gateways reject the hosted-tool class without
	// including a specific type. That is still safe to downgrade because the
	// HTTP validation response proves no generation started.
	if strings.Contains(message, "built-in tool") || strings.Contains(message, "builtin tool") ||
		strings.Contains(message, "tools are not supported") || strings.Contains(message, "tool use is not supported") ||
		strings.Contains(message, "does not support tools") || strings.Contains(message, "tool calling is not supported") {
		return available
	}
	return nil
}

func responsesBuiltinToolMentioned(message, tool string) bool {
	switch tool {
	case responseWebSearchTool:
		return strings.Contains(message, "web_search") || strings.Contains(message, "web-search") || strings.Contains(message, "web search")
	case responseImageGenerationTool:
		return strings.Contains(message, "image_generation") || strings.Contains(message, "image-generation") || strings.Contains(message, "image generation")
	default:
		return false
	}
}

func responsesFeatureRejected(message string) bool {
	for _, phrase := range []string{
		"unsupported", "not supported", "does not support", "unrecognized", "unknown tool", "unknown type",
		"not available", "unavailable", "not enabled", "disabled", "invalid tool type", "invalid type",
	} {
		if strings.Contains(message, phrase) {
			return true
		}
	}
	return false
}

func geminiSystemText(gemini map[string]any) string {
	instruction, _ := gemini["systemInstruction"].(map[string]any)
	parts, _ := instruction["parts"].([]any)
	var text strings.Builder
	for _, rawPart := range parts {
		part, _ := rawPart.(map[string]any)
		if value, ok := part["text"].(string); ok {
			text.WriteString(value)
		}
	}
	return text.String()
}

func responseTextBlock(role, text string) map[string]any {
	if role == "assistant" {
		return map[string]any{"type": "output_text", "text": text}
	}
	return map[string]any{"type": "input_text", "text": text}
}

func responseAttachmentBlock(attachment *geminiAttachment) map[string]any {
	if attachment.isImage() {
		return map[string]any{"type": "input_image", "image_url": attachment.dataURL(), "detail": "auto"}
	}
	block := map[string]any{
		"type": "input_file", "file_data": attachment.dataURL(),
	}
	if attachment.Filename != "" {
		block["filename"] = attachment.Filename
	}
	if attachment.isPDF() {
		block["detail"] = "auto"
	}
	return block
}

func geminiToolsToOpenAIResponses(gemini map[string]any) []map[string]any {
	rawTools, _ := gemini["tools"].([]any)
	var result []map[string]any
	for _, rawTool := range rawTools {
		tool, _ := rawTool.(map[string]any)
		declarations, _ := tool["functionDeclarations"].([]any)
		for _, rawDeclaration := range declarations {
			declaration, _ := rawDeclaration.(map[string]any)
			name, _ := declaration["name"].(string)
			if name == "" {
				continue
			}
			parameters, _ := declaration["parameters"].(map[string]any)
			if parameters == nil {
				parameters = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			parameters = normalizeJSONSchema(parameters).(map[string]any)
			result = append(result, map[string]any{
				"type": "function", "name": name, "description": getString(declaration, "description"), "parameters": parameters,
			})
		}
	}
	return result
}

type openAIResponsesStreamState struct {
	toolCalls       map[string]*openAIResponsesToolCall
	usage           map[string]any
	traceID         string
	responseID      string
	modelVersion    string
	finished        bool
	done            bool
	upstreamStarted bool
	failureMessage  string
	emittedText     strings.Builder
	unsafeOutput    bool
}

type openAIResponsesToolCall struct {
	name    string
	args    strings.Builder
	emitted bool
}

func convertOpenAIResponsesLineToGemini(line string, state *openAIResponsesStreamState) string {
	if !strings.HasPrefix(line, "data: ") {
		return ""
	}
	payload := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
	if payload == "" {
		return ""
	}
	if payload == "[DONE]" {
		state.done = true
		state.upstreamStarted = true
		return ""
	}
	var event map[string]any
	if err := json.Unmarshal([]byte(payload), &event); err != nil {
		return ""
	}
	// Do not confuse an event that has no visible text with an unstarted
	// request. Responses can first emit reasoning, tool, image or metadata
	// events; replaying after any of them can duplicate billable work.
	state.upstreamStarted = true
	if state.toolCalls == nil {
		state.toolCalls = map[string]*openAIResponsesToolCall{}
	}
	if response, ok := event["response"].(map[string]any); ok {
		captureResponsesMetadata(response, state)
	}
	if id, ok := event["response_id"].(string); ok && id != "" {
		state.responseID = id
	}

	eventType, _ := event["type"].(string)
	var parts []any
	switch eventType {
	case "response.output_text.delta":
		if delta, ok := event["delta"].(string); ok && delta != "" {
			state.emittedText.WriteString(delta)
			parts = append(parts, map[string]any{"text": delta})
		}
	case "response.output_text.done", "response.content_part.done":
		parts = append(parts, responseFinalTextParts(getString(event, "text", "delta"), state)...)
	case "response.output_item.added":
		captureResponsesOutputItem(event["item"], state)
	case "response.function_call_arguments.delta":
		callID := getString(event, "call_id", "item_id")
		if callID != "" {
			call := state.toolCalls[callID]
			if call == nil {
				call = &openAIResponsesToolCall{}
				state.toolCalls[callID] = call
			}
			if delta, ok := event["delta"].(string); ok {
				call.args.WriteString(delta)
			}
		}
	case "response.function_call_arguments.done":
		callID := getString(event, "call_id", "item_id")
		if callID != "" {
			if call := state.toolCalls[callID]; call != nil {
				if args, ok := event["arguments"].(string); ok && args != "" && call.args.Len() == 0 {
					call.args.WriteString(args)
				}
				parts = append(parts, responseToolCallPart(callID, call, state))
			}
		}
	case "response.output_item.done":
		parts = append(parts, responseCompletedOutputItem(event["item"], state)...)
	case "response.completed":
		if response, ok := event["response"].(map[string]any); ok {
			parts = append(parts, responseCompletedOutputItems(response["output"], state)...)
		}
		state.finished = true
		return responsesEvent(parts, "STOP", state)
	case "response.incomplete":
		if response, ok := event["response"].(map[string]any); ok {
			parts = append(parts, responseCompletedOutputItems(response["output"], state)...)
		}
		state.finished = true
		return responsesEvent(parts, "MAX_TOKENS", state)
	case "response.failed":
		state.failureMessage = responsesFailureMessage(event)
		return ""
	}
	if len(parts) == 0 {
		return ""
	}
	return responsesEvent(parts, "", state)
}

func captureResponsesMetadata(response map[string]any, state *openAIResponsesStreamState) {
	if value, ok := response["id"].(string); ok && value != "" {
		state.responseID = value
	}
	if value, ok := response["model"].(string); ok && value != "" {
		state.modelVersion = value
	}
	if usage, ok := response["usage"].(map[string]any); ok {
		state.usage = usage
	}
}

func captureResponsesOutputItem(raw any, state *openAIResponsesStreamState) {
	item, _ := raw.(map[string]any)
	if item["type"] != "function_call" {
		return
	}
	callID := getString(item, "call_id", "id")
	if callID == "" {
		return
	}
	call := state.toolCalls[callID]
	if call == nil {
		call = &openAIResponsesToolCall{}
		state.toolCalls[callID] = call
	}
	call.name = getString(item, "name")
	if arguments, ok := item["arguments"].(string); ok && arguments != "" && call.args.Len() == 0 {
		call.args.WriteString(arguments)
	}
}

func responseCompletedOutputItem(raw any, state *openAIResponsesStreamState) []any {
	item, _ := raw.(map[string]any)
	if item == nil {
		return nil
	}
	switch item["type"] {
	case "message":
		return responseCompletedMessageText(item["content"], state)
	case "output_text", "text":
		return responseFinalTextParts(getString(item, "text", "content"), state)
	case "function_call":
		captureResponsesOutputItem(item, state)
		callID := getString(item, "call_id", "id")
		if call := state.toolCalls[callID]; call != nil {
			return []any{responseToolCallPart(callID, call, state)}
		}
	case "image_generation_call":
		if result, ok := item["result"].(string); ok && result != "" {
			data, mimeType, err := normaliseAttachmentData(result, "image/png")
			if err == nil {
				state.unsafeOutput = true
				return []any{map[string]any{"inlineData": map[string]any{"mimeType": normaliseMimeType(mimeType), "data": data}}}
			}
		}
	}
	return nil
}

func responseCompletedOutputItems(raw any, state *openAIResponsesStreamState) []any {
	items, _ := raw.([]any)
	parts := make([]any, 0, len(items))
	for _, item := range items {
		parts = append(parts, responseCompletedOutputItem(item, state)...)
	}
	return parts
}

func responseCompletedMessageText(raw any, state *openAIResponsesStreamState) []any {
	blocks, _ := raw.([]any)
	parts := make([]any, 0, len(blocks))
	for _, rawBlock := range blocks {
		block, _ := rawBlock.(map[string]any)
		if block == nil {
			continue
		}
		switch getString(block, "type") {
		case "output_text", "text":
			parts = append(parts, responseFinalTextParts(getString(block, "text", "content"), state)...)
		}
	}
	return parts
}

// responseFinalTextParts appends only text that has not already been sent as
// a delta. Many compatible Responses endpoints send both delta events and a
// full final message, so blindly forwarding the latter duplicates replies.
func responseFinalTextParts(fullText string, state *openAIResponsesStreamState) []any {
	if fullText == "" {
		return nil
	}
	missing := unstreamedResponseText(state.emittedText.String(), fullText)
	if missing == "" {
		return nil
	}
	state.emittedText.WriteString(missing)
	return []any{map[string]any{"text": missing}}
}

func unstreamedResponseText(emitted, complete string) string {
	if complete == "" {
		return ""
	}
	if emitted == "" {
		return complete
	}
	if strings.HasPrefix(complete, emitted) {
		return complete[len(emitted):]
	}
	if strings.HasSuffix(emitted, complete) {
		return ""
	}
	limit := len(emitted)
	if len(complete) < limit {
		limit = len(complete)
	}
	for overlap := limit; overlap > 0; overlap-- {
		if strings.HasSuffix(emitted, complete[:overlap]) {
			return complete[overlap:]
		}
	}
	// Ambiguous final text is intentionally ignored rather than duplicated.
	return ""
}

func responsesFailureMessage(event map[string]any) string {
	for _, raw := range []any{event["error"], event["response"]} {
		value, _ := raw.(map[string]any)
		if message := getString(value, "message", "error"); message != "" {
			return message
		}
		if nested, ok := value["error"].(map[string]any); ok {
			if message := getString(nested, "message"); message != "" {
				return message
			}
		}
	}
	return "Responses 上游流返回 failed 事件"
}

func responseToolCallPart(callID string, call *openAIResponsesToolCall, state *openAIResponsesStreamState) any {
	if call == nil || call.emitted {
		return nil
	}
	arguments, ok := decodeFunctionCallArgs(call.args.String())
	if call.name == "" || !ok {
		traceDroppedFunctionCall("responses", state.traceID, callID, call.name, call.args.String())
		return nil
	}
	call.emitted = true
	state.unsafeOutput = true
	return map[string]any{"functionCall": map[string]any{"id": callID, "name": call.name, "args": arguments}}
}

func responsesEvent(parts []any, finishReason string, state *openAIResponsesStreamState) string {
	filtered := make([]any, 0, len(parts))
	for _, part := range parts {
		if part != nil {
			filtered = append(filtered, part)
		}
	}
	if len(filtered) == 0 && finishReason == "" {
		return ""
	}
	response := map[string]any{
		"candidates": []any{map[string]any{
			"content": map[string]any{"role": "model", "parts": filtered}, "finishReason": finishReason,
		}},
	}
	if state.responseID != "" {
		response["responseId"] = state.responseID
	}
	if state.modelVersion != "" {
		response["modelVersion"] = state.modelVersion
	}
	if state.usage != nil {
		input, _ := state.usage["input_tokens"].(float64)
		output, _ := state.usage["output_tokens"].(float64)
		response["usageMetadata"] = map[string]any{
			"promptTokenCount": int(input), "candidatesTokenCount": int(output), "totalTokenCount": int(input + output),
		}
	}
	return encodeAntigravityStreamEvent(response, state.traceID)
}

func responsesFinishEvent(reason string, state *openAIResponsesStreamState) string {
	return responsesEvent(nil, reason, state)
}
