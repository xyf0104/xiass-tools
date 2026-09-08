package proxy

import (
	"strings"

	"antigravity-wf-assistant/internal/storage"
)

// applyOpenAIChatReasoning adds only the parameters that the selected model's
// documented Chat Completions-compatible contract understands.  This is kept
// separate from the picker: a saved configuration may have been created by an
// older build, edited by hand, or imported from another machine.
func applyOpenAIChatReasoning(request map[string]any, model *storage.CustomModel) {
	if request == nil || model == nil {
		return
	}
	profile := storage.ReasoningProfileForModel(*model)
	effort := storage.NormalizeReasoningEffort(*model, model.ReasoningEffort)
	switch profile.Strategy {
	case storage.ReasoningStrategyOpenAIAuto,
		storage.ReasoningStrategyOpenAIChatCompletions,
		storage.ReasoningStrategyOpenAIResponses:
		if effort == storage.ReasoningEffortAuto {
			return
		}
		request["reasoning_effort"] = effort
		delete(request, "temperature")
	case storage.ReasoningStrategyDeepSeekOpenAI:
		applyDeepSeekOpenAIReasoning(request, model, effort)
	}
}

// applyOpenAIResponsesReasoning is intentionally different from Chat
// Completions: GPT Responses expects the effort nested below reasoning.
func applyOpenAIResponsesReasoning(request map[string]any, model *storage.CustomModel) {
	if request == nil || model == nil {
		return
	}
	profile := storage.ReasoningProfileForModel(*model)
	if profile.Strategy != storage.ReasoningStrategyOpenAIAuto &&
		profile.Strategy != storage.ReasoningStrategyOpenAIChatCompletions &&
		profile.Strategy != storage.ReasoningStrategyOpenAIResponses {
		return
	}
	effort := storage.NormalizeReasoningEffort(*model, model.ReasoningEffort)
	if effort == storage.ReasoningEffortAuto {
		return
	}
	reasoning, _ := request["reasoning"].(map[string]any)
	if reasoning == nil {
		reasoning = map[string]any{}
	}
	reasoning["effort"] = effort
	request["reasoning"] = reasoning
	delete(request, "temperature")
}

func applyDeepSeekOpenAIReasoning(request map[string]any, model *storage.CustomModel, effort string) {
	profile := storage.ReasoningProfileForModel(*model)
	if profile.Strategy != storage.ReasoningStrategyDeepSeekOpenAI {
		return
	}
	// DeepSeek V4 defaults to thinking enabled.  Preserve that default unless
	// the user explicitly chose a switch state or a concrete effort value.
	enabled := true
	explicit := effort != storage.ReasoningEffortAuto
	if model.ThinkingEnabled != nil {
		enabled = *model.ThinkingEnabled
		explicit = true
	}
	if explicit {
		state := "enabled"
		if !enabled {
			state = "disabled"
		}
		request["thinking"] = map[string]any{"type": state}
	}
	if enabled && effort != storage.ReasoningEffortAuto {
		request["reasoning_effort"] = effort
	} else {
		delete(request, "reasoning_effort")
	}
	// DeepSeek documents that sampling parameters do not apply while thinking is
	// enabled.  Omitting them makes the wire request deterministic across raw
	// HTTP gateways instead of relying on each gateway to silently ignore them.
	if enabled {
		delete(request, "temperature")
		delete(request, "top_p")
		delete(request, "presence_penalty")
		delete(request, "frequency_penalty")
	}
}

// applyAnthropicReasoning selects modern output_config.effort, DeepSeek's
// Anthropic-compatible effort shape, or the legacy Claude thinking budget as
// appropriate for the exact model profile.  It never mixes them merely
// because the model name contains "claude".
func applyAnthropicReasoning(request map[string]any, model *storage.CustomModel) {
	if request == nil || model == nil {
		return
	}
	profile := storage.ReasoningProfileForModel(*model)
	effort := storage.NormalizeReasoningEffort(*model, model.ReasoningEffort)
	switch profile.Strategy {
	case storage.ReasoningStrategyAnthropicEffort:
		if effort != storage.ReasoningEffortAuto {
			setAnthropicOutputEffort(request, effort)
		}
		// Claude Opus 4.5 is the only currently supported profile that can use
		// output_config.effort and an explicit legacy thinking budget together.
		if profile.SupportsBudget {
			applyAnthropicLegacyBudget(request, model, profile)
		}
	case storage.ReasoningStrategyDeepSeekAnthropic:
		if effort != storage.ReasoningEffortAuto {
			setAnthropicOutputEffort(request, effort)
		}
	case storage.ReasoningStrategyAnthropicLegacyBudget:
		applyAnthropicLegacyBudget(request, model, profile)
	}
}

func setAnthropicOutputEffort(request map[string]any, effort string) {
	outputConfig, _ := request["output_config"].(map[string]any)
	if outputConfig == nil {
		outputConfig = map[string]any{}
	}
	outputConfig["effort"] = effort
	request["output_config"] = outputConfig
}

func applyAnthropicLegacyBudget(request map[string]any, model *storage.CustomModel, profile storage.ReasoningProfile) {
	enabled := true
	if model.ThinkingEnabled != nil {
		enabled = *model.ThinkingEnabled
	}
	if !enabled {
		request["thinking"] = map[string]any{"type": "disabled"}
		return
	}
	budget := storage.NormalizeReasoningBudget(profile, model.ReasoningBudgetTokens)
	if budget == 0 && model.ThinkingEnabled != nil && *model.ThinkingEnabled {
		budget = profile.DefaultBudgetTokens
	}
	if budget <= 0 {
		return
	}
	request["thinking"] = map[string]any{"type": "enabled", "budget_tokens": budget}
	delete(request, "temperature")
	if maxTokens, ok := numberAsInt(request["max_tokens"]); !ok || maxTokens <= budget {
		request["max_tokens"] = budget + 8192
	}
}

// reasoningBudget is retained for old test fixtures and old saved-model
// migrations. New request code uses the profile-aware budget above.
func reasoningBudget(effort string) int {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case storage.ReasoningEffortLow:
		return 1024
	case storage.ReasoningEffortMedium:
		return 4096
	case storage.ReasoningEffortHigh:
		return 8192
	default:
		return 0
	}
}
