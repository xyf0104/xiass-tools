package proxy

import (
	"strings"

	"antigravity-wf-assistant/internal/storage"
	"antigravity-wf-assistant/internal/upstream"
)

// isOpenAICodexOAuthModel identifies both an already-selected runtime account
// and a persisted model bound to a Codex OAuth account. The latter matters
// before acquireAttemptModel runs: routing a normal text turn to Chat
// Completions first would send a Codex access token to the wrong API surface.
func isOpenAICodexOAuthModel(model *storage.CustomModel) bool {
	if model == nil {
		return false
	}
	if upstream.IsOpenAICodexOAuth(upstream.ConfigFromModel(*model)) {
		return true
	}
	if len(model.AccountIDs) == 0 {
		return false
	}

	accounts, err := storage.LoadUpstreamAccounts()
	if err != nil {
		// The normal account-pool attempt will return the underlying storage
		// error with a useful user-facing message. Do not manufacture an OAuth
		// classification from incomplete account data here.
		return false
	}
	bound := make(map[string]struct{}, len(model.AccountIDs))
	for _, id := range model.AccountIDs {
		if id = strings.TrimSpace(id); id != "" {
			bound[id] = struct{}{}
		}
	}
	for _, account := range accounts {
		if _, ok := bound[account.ID]; ok && account.IsOpenAICodexOAuth() {
			return true
		}
	}
	return false
}

// openAICodexResponsesConversionModel is an ephemeral conversion-only copy.
// A model can have been saved by an older WF release as Chat-only before an
// OpenAI/Codex OAuth account was bound to it. The selected account still must
// receive the full Responses representation (images, function tools, web
// search and image generation), regardless of those stale picker flags.
func openAICodexResponsesConversionModel(model *storage.CustomModel) *storage.CustomModel {
	if model == nil || !isOpenAICodexOAuthModel(model) {
		return model
	}
	converted := *model
	converted.Provider = "openai"
	converted.APIStyle = "responses"
	converted.Capabilities = storage.DefaultCapabilitiesForAPIStyle("openai", converted.ExternalModelName, "responses")
	return &converted
}

// normalizeOpenAICodexResponsesRequest applies the deliberately narrow
// contract accepted by ChatGPT's Codex Responses endpoint. It copies only the
// top-level map: nested image inputs, function tools, web tools and tool
// schemas remain exactly as converted from Antigravity.
func normalizeOpenAICodexResponsesRequest(base map[string]any) map[string]any {
	request := make(map[string]any, len(base))
	for key, value := range base {
		request[key] = value
	}

	// Codex's internal Responses endpoint rejects several Chat-compatible
	// controls. These are endpoint restrictions, not model capabilities.
	request["store"] = false
	request["stream"] = true
	delete(request, "max_output_tokens")
	delete(request, "temperature")
	delete(request, "top_p")
	delete(request, "stream_options")

	normalizeOpenAICodexInput(request)
	return request
}

// normalizeOpenAICodexInput removes only a trailing assistant message. That
// is an assistant prefill, which Codex explicitly rejects. Earlier assistant
// history and every function call/output remain part of the conversation.
func normalizeOpenAICodexInput(request map[string]any) {
	input, ok := request["input"].([]any)
	if !ok {
		return
	}
	end := len(input)
	for end > 0 && isOpenAICodexAssistantMessage(input[end-1]) {
		end--
	}
	if end == len(input) && end > 0 {
		return
	}

	cleaned := append([]any(nil), input[:end]...)
	if len(cleaned) == 0 {
		cleaned = append(cleaned, map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{"type": "input_text", "text": " "},
			},
		})
	}
	request["input"] = cleaned
}

func isOpenAICodexAssistantMessage(value any) bool {
	message, ok := value.(map[string]any)
	if !ok {
		return false
	}
	role, _ := message["role"].(string)
	return strings.EqualFold(strings.TrimSpace(role), "assistant")
}
