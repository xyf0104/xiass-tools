package proxy

import (
	"testing"

	"antigravity-wf-assistant/internal/storage"
)

func TestApplyOpenAIChatReasoningUsesGPT56SupportedEffort(t *testing.T) {
	request := map[string]any{"temperature": 0.7}
	model := &storage.CustomModel{
		Provider: "openai", APIStyle: "chat_completions", ExternalModelName: "gpt-5.6-sol", ReasoningEffort: "max",
	}
	applyOpenAIChatReasoning(request, model)
	if got := request["reasoning_effort"]; got != "max" {
		t.Fatalf("reasoning_effort = %#v, want max", got)
	}
	if _, exists := request["temperature"]; exists {
		t.Fatal("GPT reasoning request retained incompatible temperature")
	}
}

func TestApplyOpenAIResponsesReasoningUsesNestedGPT56Effort(t *testing.T) {
	request := map[string]any{"temperature": 0.4, "reasoning": map[string]any{"summary": "auto"}}
	model := &storage.CustomModel{
		Provider: "openai", APIStyle: "responses", ExternalModelName: "gpt-5.6-terra", ReasoningEffort: "xhigh",
	}
	applyOpenAIResponsesReasoning(request, model)
	reasoning, ok := request["reasoning"].(map[string]any)
	if !ok || reasoning["effort"] != "xhigh" || reasoning["summary"] != "auto" {
		t.Fatalf("reasoning payload = %#v", request["reasoning"])
	}
	if _, exists := request["temperature"]; exists {
		t.Fatal("GPT Responses reasoning request retained incompatible temperature")
	}
}

func TestApplyDeepSeekV4MapsAndControlsThinking(t *testing.T) {
	enabled := true
	request := map[string]any{"temperature": 0.7, "top_p": 0.9}
	model := &storage.CustomModel{
		Provider: "openai", APIStyle: "chat_completions", ExternalModelName: "deepseek-v4-pro", ReasoningEffort: "xhigh", ThinkingEnabled: &enabled,
	}
	applyOpenAIChatReasoning(request, model)
	if got := request["reasoning_effort"]; got != "max" {
		t.Fatalf("DeepSeek mapped effort = %#v, want max", got)
	}
	thinking, ok := request["thinking"].(map[string]any)
	if !ok || thinking["type"] != "enabled" {
		t.Fatalf("DeepSeek thinking payload = %#v", request["thinking"])
	}
	if _, exists := request["temperature"]; exists {
		t.Fatal("DeepSeek thinking request retained temperature")
	}
	if _, exists := request["top_p"]; exists {
		t.Fatal("DeepSeek thinking request retained top_p")
	}

	disabled := false
	disabledRequest := map[string]any{"temperature": 0.7}
	model.ThinkingEnabled = &disabled
	applyOpenAIChatReasoning(disabledRequest, model)
	thinking, ok = disabledRequest["thinking"].(map[string]any)
	if !ok || thinking["type"] != "disabled" {
		t.Fatalf("DeepSeek disabled thinking payload = %#v", disabledRequest["thinking"])
	}
	if _, exists := disabledRequest["reasoning_effort"]; exists {
		t.Fatal("DeepSeek disabled thinking still sent reasoning_effort")
	}
}

func TestApplyAnthropicReasoningUsesModelSpecificContract(t *testing.T) {
	modern := &storage.CustomModel{
		Provider: "anthropic", APIStyle: "messages", ExternalModelName: "claude-opus-4.7", ReasoningEffort: "xhigh",
	}
	modernRequest := map[string]any{"temperature": 0.4}
	applyAnthropicReasoning(modernRequest, modern)
	outputConfig, ok := modernRequest["output_config"].(map[string]any)
	if !ok || outputConfig["effort"] != "xhigh" {
		t.Fatalf("modern Claude output_config = %#v", modernRequest["output_config"])
	}
	if _, exists := modernRequest["thinking"]; exists {
		t.Fatal("modern Claude must not receive legacy thinking budget")
	}

	unsupported := &storage.CustomModel{
		Provider: "anthropic", APIStyle: "messages", ExternalModelName: "claude-sonnet-4.6", ReasoningEffort: "xhigh",
	}
	unsupportedRequest := map[string]any{}
	applyAnthropicReasoning(unsupportedRequest, unsupported)
	if _, exists := unsupportedRequest["output_config"]; exists {
		t.Fatal("Claude Sonnet 4.6 must not receive unsupported xhigh")
	}

	legacy := &storage.CustomModel{
		Provider: "anthropic", APIStyle: "messages", ExternalModelName: "claude-sonnet-3.7", ReasoningBudgetTokens: 4096,
	}
	legacyRequest := map[string]any{"max_tokens": 2048, "temperature": 0.4}
	applyAnthropicReasoning(legacyRequest, legacy)
	thinking, ok := legacyRequest["thinking"].(map[string]any)
	if !ok || thinking["type"] != "enabled" || thinking["budget_tokens"] != 4096 {
		t.Fatalf("legacy Claude thinking payload = %#v", legacyRequest["thinking"])
	}
	if got, _ := numberAsInt(legacyRequest["max_tokens"]); got != 12288 {
		t.Fatalf("legacy Claude max_tokens = %d, want 12288", got)
	}
}

func TestApplyAnthropicReasoningSupportsOpus45EffortAndBudget(t *testing.T) {
	model := &storage.CustomModel{
		Provider: "anthropic", APIStyle: "messages", ExternalModelName: "claude-opus-4.5", ReasoningEffort: "medium", ReasoningBudgetTokens: 4096,
	}
	request := map[string]any{"max_tokens": 8192}
	applyAnthropicReasoning(request, model)
	outputConfig, ok := request["output_config"].(map[string]any)
	if !ok || outputConfig["effort"] != "medium" {
		t.Fatalf("Opus 4.5 output_config = %#v", request["output_config"])
	}
	thinking, ok := request["thinking"].(map[string]any)
	if !ok || thinking["budget_tokens"] != 4096 {
		t.Fatalf("Opus 4.5 thinking = %#v", request["thinking"])
	}
}
