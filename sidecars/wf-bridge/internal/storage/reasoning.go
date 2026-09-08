package storage

import (
	"strconv"
	"strings"
	"unicode"
)

// ReasoningProtocolStrategy describes the wire contract selected by a model
// profile. It is intentionally separate from the saved effort: the renderer
// uses it to expose only valid controls, while the request translator uses it
// to choose the corresponding provider field.
type ReasoningProtocolStrategy string

const (
	ReasoningStrategyUnsupported           ReasoningProtocolStrategy = "unsupported"
	ReasoningStrategyOpenAIAuto            ReasoningProtocolStrategy = "openai_auto"
	ReasoningStrategyOpenAIChatCompletions ReasoningProtocolStrategy = "openai_chat_completions"
	ReasoningStrategyOpenAIResponses       ReasoningProtocolStrategy = "openai_responses"
	ReasoningStrategyAnthropicEffort       ReasoningProtocolStrategy = "anthropic_effort"
	ReasoningStrategyAnthropicLegacyBudget ReasoningProtocolStrategy = "anthropic_legacy_budget"
	ReasoningStrategyDeepSeekOpenAI        ReasoningProtocolStrategy = "deepseek_openai"
	ReasoningStrategyDeepSeekAnthropic     ReasoningProtocolStrategy = "deepseek_anthropic"
)

const (
	ReasoningEffortAuto    = "auto"
	ReasoningEffortNone    = "none"
	ReasoningEffortMinimal = "minimal"
	ReasoningEffortLow     = "low"
	ReasoningEffortMedium  = "medium"
	ReasoningEffortHigh    = "high"
	ReasoningEffortXHigh   = "xhigh"
	ReasoningEffortMax     = "max"
)

// ReasoningEffortOption is deliberately UI-ready. The frontend may translate
// Label, but Value is the stable persisted and protocol-facing identifier.
type ReasoningEffortOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// ReasoningEffortMapping records documented compatibility conversions. It is
// not a list of additional selectable values. In particular, DeepSeek V4 only
// accepts high and max even though legacy user choices can be migrated safely.
type ReasoningEffortMapping struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Reason string `json:"reason,omitempty"`
}

// ReasoningProfile is the single storage-level source of truth for the model
// reasoning UI. Unknown models intentionally expose only auto; a gateway name
// alone is never evidence that every effort value is supported upstream.
//
// BudgetMaximumTokens==0 means that the provider's model/context limit is the
// upper bound. The profile still validates the protocol minimum so a malformed
// saved configuration cannot send an invalid tiny budget.
type ReasoningProfile struct {
	ModelFamily string `json:"modelFamily"`
	// Kind and Strategy intentionally carry the same stable enum. Kind keeps
	// the profile convenient for application/proxy callers that need a compact
	// switch value, while Strategy preserves the self-describing JSON name used
	// by the UI.
	Kind     ReasoningProtocolStrategy `json:"kind"`
	Strategy ReasoningProtocolStrategy `json:"strategy"`
	Options  []ReasoningEffortOption   `json:"options"`
	// SupportedEfforts mirrors Options as plain values for request translators
	// and account-pool code that do not need display labels.
	SupportedEfforts       []string `json:"supportedEfforts"`
	SupportsThinkingToggle bool     `json:"supportsThinkingToggle"`
	SupportsBudget         bool     `json:"supportsBudget"`
	// LegacyBudget reports that the family has a supported traditional
	// thinking-budget fallback. Hybrid Claude 4.5 profiles can expose modern
	// effort and this legacy capability together.
	LegacyBudget        bool                     `json:"legacyBudget"`
	BudgetMinimumTokens int                      `json:"budgetMinimumTokens,omitempty"`
	BudgetMaximumTokens int                      `json:"budgetMaximumTokens,omitempty"`
	BudgetStepTokens    int                      `json:"budgetStepTokens,omitempty"`
	DefaultBudgetTokens int                      `json:"defaultBudgetTokens,omitempty"`
	EffortMappings      []ReasoningEffortMapping `json:"effortMappings,omitempty"`
}

// ResolveModelReasoning resolves the profile for a saved or discovered model.
func ResolveModelReasoning(model CustomModel) ReasoningProfile {
	return ReasoningProfileForModel(model)
}

// ReasoningProfileForModel is the application/proxy-facing resolver. Keeping
// this short alias makes it easy for both manual models and account-pool
// discovery candidates to use the identical policy.
func ReasoningProfileForModel(model CustomModel) ReasoningProfile {
	return ResolveReasoningProfile(model.Provider, model.APIStyle, model.ExternalModelName)
}

// ResolveReasoningProfile only enables a profile when both the concrete model
// family and the route protocol are known. It deliberately does not infer
// support for generic API-compatible names such as "gpt-test" or "claude".
func ResolveReasoningProfile(provider, apiStyle, externalModelName string) ReasoningProfile {
	return completeReasoningProfile(resolveReasoningProfile(provider, apiStyle, externalModelName))
}

func resolveReasoningProfile(provider, apiStyle, externalModelName string) ReasoningProfile {
	provider = normalizedReasoningProvider(provider)
	apiStyle = normalizedReasoningAPIStyle(apiStyle)
	tokens := reasoningModelTokens(externalModelName)

	if provider == "anthropic" {
		if profile, ok := resolveClaudeReasoningProfile(tokens); ok {
			return profile
		}
		if isDeepSeekV4(tokens) {
			return deepSeekAnthropicProfile()
		}
		return unsupportedReasoningProfile()
	}

	// The current proxy routes non-Anthropic providers through the OpenAI
	// conversion path. A manually selected Messages style is therefore not a
	// valid way to claim Anthropic effort compatibility for a custom provider.
	if apiStyle == "messages" {
		return unsupportedReasoningProfile()
	}
	if isGPT56(tokens) {
		return gpt56Profile(apiStyle)
	}
	if isDeepSeekV4(tokens) {
		// DeepSeek V4's documented native surface is OpenAI-compatible Chat
		// Completions. Do not claim compatibility when a user forces Responses.
		if apiStyle == "responses" {
			return unsupportedReasoningProfile()
		}
		return deepSeekOpenAIProfile()
	}
	return unsupportedReasoningProfile()
}

func completeReasoningProfile(profile ReasoningProfile) ReasoningProfile {
	profile.Kind = profile.Strategy
	profile.LegacyBudget = profile.SupportsBudget
	profile.SupportedEfforts = make([]string, 0, len(profile.Options))
	for _, option := range profile.Options {
		profile.SupportedEfforts = append(profile.SupportedEfforts, option.Value)
	}
	return profile
}

// NormalizeReasoningEffort converts a pending UI value to a value actually
// offered by the resolved profile. Invalid, stale, and unrecognised values
// always fall back to auto rather than being forwarded optimistically.
func NormalizeReasoningEffort(model CustomModel, selected string) string {
	profile := ResolveModelReasoning(model)
	value := normalizeReasoningEffortValue(selected)
	if reasoningProfileAllows(profile, value) {
		return value
	}
	for _, mapping := range profile.EffortMappings {
		if value == mapping.From && reasoningProfileAllows(profile, mapping.To) {
			return mapping.To
		}
	}
	return ReasoningEffortAuto
}

// NormalizeModelReasoning is used on every storage write. Keeping this at the
// persistence boundary means account discovery, manual creation, and editing
// all share the same safe migration behaviour.
func NormalizeModelReasoning(model CustomModel) CustomModel {
	profile := ResolveModelReasoning(model)
	legacySelection := normalizeReasoningEffortValue(model.ReasoningEffort)
	// Before v1.4.6, legacy Claude thinking was persisted as a generic
	// low/medium/high effort. Preserve that explicit user choice when migrating
	// to the correctly typed budget field instead of silently disabling it.
	if profile.Strategy == ReasoningStrategyAnthropicLegacyBudget && model.ReasoningBudgetTokens <= 0 && !thinkingExplicitlyDisabled(model.ThinkingEnabled) {
		if budget := legacyClaudeBudgetForEffort(legacySelection); budget > 0 {
			model.ReasoningBudgetTokens = budget
			if model.ThinkingEnabled == nil {
				enabled := true
				model.ThinkingEnabled = &enabled
			}
		}
	}
	model.ReasoningEffort = NormalizeReasoningEffort(model, model.ReasoningEffort)

	if !profile.SupportsThinkingToggle {
		model.ThinkingEnabled = nil
	}
	if !profile.SupportsBudget {
		model.ReasoningBudgetTokens = 0
		return model
	}

	if model.ThinkingEnabled != nil && !*model.ThinkingEnabled {
		model.ReasoningBudgetTokens = 0
		return model
	}
	model.ReasoningBudgetTokens = NormalizeReasoningBudget(profile, model.ReasoningBudgetTokens)
	return model
}

func thinkingExplicitlyDisabled(value *bool) bool {
	return value != nil && !*value
}

func legacyClaudeBudgetForEffort(value string) int {
	switch value {
	case ReasoningEffortLow:
		return 1024
	case ReasoningEffortMedium:
		return 4096
	case ReasoningEffortHigh:
		return 8192
	default:
		return 0
	}
}

// NormalizeReasoningBudget enforces only the model-independent protocol floor.
// A zero value means the user has not opted into a legacy budget yet and must
// remain zero; it is not silently replaced with a token-spending default.
func NormalizeReasoningBudget(profile ReasoningProfile, budget int) int {
	if !profile.SupportsBudget || budget <= 0 {
		return 0
	}
	if minimum := profile.BudgetMinimumTokens; minimum > 0 && budget < minimum {
		budget = minimum
	}
	if maximum := profile.BudgetMaximumTokens; maximum > 0 && budget > maximum {
		budget = maximum
	}
	if step := profile.BudgetStepTokens; step > 1 && budget%step != 0 {
		budget = ((budget + step - 1) / step) * step
		if maximum := profile.BudgetMaximumTokens; maximum > 0 && budget > maximum {
			budget = maximum
		}
	}
	return budget
}

func unsupportedReasoningProfile() ReasoningProfile {
	return ReasoningProfile{
		Strategy: ReasoningStrategyUnsupported,
		Options:  reasoningOptions(ReasoningEffortAuto),
	}
}

func gpt56Profile(apiStyle string) ReasoningProfile {
	strategy := ReasoningStrategyOpenAIAuto
	switch apiStyle {
	case "chat_completions":
		strategy = ReasoningStrategyOpenAIChatCompletions
	case "responses":
		strategy = ReasoningStrategyOpenAIResponses
	}
	return ReasoningProfile{
		ModelFamily: "gpt-5.6",
		Strategy:    strategy,
		Options: reasoningOptions(
			ReasoningEffortAuto,
			ReasoningEffortNone,
			ReasoningEffortLow,
			ReasoningEffortMedium,
			ReasoningEffortHigh,
			ReasoningEffortXHigh,
			ReasoningEffortMax,
		),
	}
}

func deepSeekOpenAIProfile() ReasoningProfile {
	return ReasoningProfile{
		ModelFamily:            "deepseek-v4",
		Strategy:               ReasoningStrategyDeepSeekOpenAI,
		Options:                reasoningOptions(ReasoningEffortAuto, ReasoningEffortHigh, ReasoningEffortMax),
		SupportsThinkingToggle: true,
		EffortMappings: []ReasoningEffortMapping{
			{From: ReasoningEffortLow, To: ReasoningEffortHigh, Reason: "DeepSeek V4 maps low to high"},
			{From: ReasoningEffortMedium, To: ReasoningEffortHigh, Reason: "DeepSeek V4 maps medium to high"},
			{From: ReasoningEffortXHigh, To: ReasoningEffortMax, Reason: "DeepSeek V4 maps xhigh to max"},
		},
	}
}

func deepSeekAnthropicProfile() ReasoningProfile {
	profile := deepSeekOpenAIProfile()
	profile.Strategy = ReasoningStrategyDeepSeekAnthropic
	// The Anthropic-compatible DeepSeek route has a reliable effort field but
	// no portable thinking on/off shape. Do not expose a toggle that the proxy
	// cannot faithfully serialize.
	profile.SupportsThinkingToggle = false
	return profile
}

func resolveClaudeReasoningProfile(tokens []string) (ReasoningProfile, bool) {
	family, major, minor, found := claudeModelSeries(tokens)
	if !found {
		return ReasoningProfile{}, false
	}

	if major == 5 && (family == "fable" || family == "mythos" || family == "opus" || family == "sonnet") {
		return modernClaudeProfile("claude-"+family+"-5", true), true
	}
	if family == "opus" && major == 4 {
		switch minor {
		case 8, 7:
			return modernClaudeProfile("claude-opus-4."+strconv.Itoa(minor), true), true
		case 6:
			return modernClaudeProfile("claude-opus-4.6", false), true
		case 5:
			return hybridClaudeOpus45Profile(), true
		}
	}
	if family == "sonnet" && major == 4 {
		switch minor {
		case 6:
			return modernClaudeProfile("claude-sonnet-4.6", false), true
		case 5, 4, 3, 2, 1, 0:
			return legacyClaudeProfile("claude-sonnet-4." + strconv.Itoa(minor)), true
		}
	}
	if family == "opus" && major == 4 && minor <= 5 {
		return legacyClaudeProfile("claude-opus-4." + strconv.Itoa(minor)), true
	}
	if family == "haiku" && major == 4 && minor <= 5 {
		return legacyClaudeProfile("claude-haiku-4." + strconv.Itoa(minor)), true
	}
	// These older Sonnet series are known to use the traditional explicit
	// thinking budget; older Claude names not listed here stay conservative.
	if family == "sonnet" && major == 3 && (minor == 5 || minor == 7) {
		return legacyClaudeProfile("claude-sonnet-3." + strconv.Itoa(minor)), true
	}
	return ReasoningProfile{}, false
}

func modernClaudeProfile(family string, supportsXHigh bool) ReasoningProfile {
	values := []string{ReasoningEffortAuto, ReasoningEffortLow, ReasoningEffortMedium, ReasoningEffortHigh}
	if supportsXHigh {
		values = append(values, ReasoningEffortXHigh)
	}
	// The currently documented 4.6 families support max without xhigh; every
	// other modern family reaching this helper supports both xhigh and max.
	values = append(values, ReasoningEffortMax)
	return ReasoningProfile{
		ModelFamily: family,
		Strategy:    ReasoningStrategyAnthropicEffort,
		Options:     reasoningOptions(values...),
	}
}

func legacyClaudeProfile(family string) ReasoningProfile {
	return ReasoningProfile{
		ModelFamily:            family,
		Strategy:               ReasoningStrategyAnthropicLegacyBudget,
		Options:                reasoningOptions(ReasoningEffortAuto),
		SupportsThinkingToggle: true,
		SupportsBudget:         true,
		BudgetMinimumTokens:    1024,
		BudgetStepTokens:       1024,
		DefaultBudgetTokens:    8192,
	}
}

// Claude Opus 4.5 accepts the documented low/medium/high effort values and
// still has a traditional thinking-budget fallback. Keep both capabilities in
// the profile so an older compatible gateway can choose the budget route
// without incorrectly offering xhigh or max.
func hybridClaudeOpus45Profile() ReasoningProfile {
	return ReasoningProfile{
		ModelFamily:            "claude-opus-4.5",
		Strategy:               ReasoningStrategyAnthropicEffort,
		Options:                reasoningOptions(ReasoningEffortAuto, ReasoningEffortLow, ReasoningEffortMedium, ReasoningEffortHigh),
		SupportsThinkingToggle: true,
		SupportsBudget:         true,
		BudgetMinimumTokens:    1024,
		BudgetStepTokens:       1024,
		DefaultBudgetTokens:    8192,
	}
}

func reasoningOptions(values ...string) []ReasoningEffortOption {
	options := make([]ReasoningEffortOption, 0, len(values))
	for _, value := range values {
		options = append(options, ReasoningEffortOption{Value: value, Label: reasoningEffortLabel(value)})
	}
	return options
}

func reasoningEffortLabel(value string) string {
	switch value {
	case ReasoningEffortAuto:
		return "自动"
	case ReasoningEffortNone:
		return "无"
	case ReasoningEffortMinimal:
		return "最小"
	case ReasoningEffortLow:
		return "低"
	case ReasoningEffortMedium:
		return "中"
	case ReasoningEffortHigh:
		return "高"
	case ReasoningEffortXHigh:
		return "超高"
	case ReasoningEffortMax:
		return "最大"
	default:
		return value
	}
}

func reasoningProfileAllows(profile ReasoningProfile, value string) bool {
	for _, option := range profile.Options {
		if option.Value == value {
			return true
		}
	}
	return false
}

func normalizeReasoningEffortValue(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "", ReasoningEffortAuto:
		return ReasoningEffortAuto
	case ReasoningEffortNone, ReasoningEffortMinimal, ReasoningEffortLow, ReasoningEffortMedium, ReasoningEffortHigh, ReasoningEffortXHigh, ReasoningEffortMax:
		return value
	default:
		return ReasoningEffortAuto
	}
}

func normalizedReasoningProvider(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "anthropic":
		return "anthropic"
	case "grok":
		return "grok"
	case "custom":
		return "custom"
	default:
		return "openai"
	}
}

func normalizedReasoningAPIStyle(apiStyle string) string {
	switch strings.ToLower(strings.TrimSpace(apiStyle)) {
	case "chat_completions", "responses", "messages", "auto":
		return strings.ToLower(strings.TrimSpace(apiStyle))
	default:
		return "auto"
	}
}

func reasoningModelTokens(value string) []string {
	value = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(value), "models/"))
	return strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

func isGPT56(tokens []string) bool {
	for index, token := range tokens {
		if token == "gpt" && index+2 < len(tokens) && tokens[index+1] == "5" && tokens[index+2] == "6" {
			return true
		}
		if token == "gpt5" && index+1 < len(tokens) && tokens[index+1] == "6" {
			return true
		}
	}
	return false
}

func isDeepSeekV4(tokens []string) bool {
	for index, token := range tokens {
		if token != "deepseek" {
			continue
		}
		if index+1 >= len(tokens) {
			return false
		}
		return tokens[index+1] == "v4" || (tokens[index+1] == "v" && index+2 < len(tokens) && tokens[index+2] == "4") || tokens[index+1] == "4"
	}
	return false
}

func claudeModelSeries(tokens []string) (family string, major, minor int, found bool) {
	claudeIndex := -1
	for index, token := range tokens {
		if token == "claude" {
			claudeIndex = index
			break
		}
	}
	if claudeIndex < 0 {
		return "", 0, 0, false
	}
	for index, token := range tokens {
		if token != "fable" && token != "mythos" && token != "opus" && token != "sonnet" && token != "haiku" {
			continue
		}
		if index+1 < len(tokens) {
			if value, ok := reasoningNumber(tokens[index+1]); ok && value <= 9 {
				minor := 0
				if index+2 < len(tokens) {
					if next, nextOK := reasoningNumber(tokens[index+2]); nextOK && next <= 9 {
						minor = next
					}
				}
				return token, value, minor, true
			}
		}
		if index >= 2 {
			if candidateMajor, majorOK := reasoningNumber(tokens[index-2]); majorOK && candidateMajor <= 9 {
				if candidateMinor, minorOK := reasoningNumber(tokens[index-1]); minorOK && candidateMinor <= 9 {
					return token, candidateMajor, candidateMinor, true
				}
			}
		}
		if index >= 1 {
			if value, ok := reasoningNumber(tokens[index-1]); ok && value <= 9 {
				return token, value, 0, true
			}
		}
	}
	return "", 0, 0, false
}

func reasoningNumber(value string) (int, bool) {
	if value == "" {
		return 0, false
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 {
		return 0, false
	}
	return parsed, true
}
