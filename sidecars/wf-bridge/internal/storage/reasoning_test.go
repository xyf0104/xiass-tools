package storage

import (
	"reflect"
	"testing"
)

func TestReasoningProfileMatchesVerifiedModelFamilies(t *testing.T) {
	tests := []struct {
		name        string
		model       CustomModel
		wantKind    ReasoningProtocolStrategy
		wantOptions []string
		wantBudget  bool
		wantToggle  bool
		wantFamily  string
	}{
		{
			name:     "GPT 5.6 Sol uses every documented GPT 5.6 effort",
			model:    CustomModel{Provider: "openai", APIStyle: "auto", ExternalModelName: "gpt-5.6-sol"},
			wantKind: ReasoningStrategyOpenAIAuto,
			wantOptions: []string{
				ReasoningEffortAuto, ReasoningEffortNone, ReasoningEffortLow,
				ReasoningEffortMedium, ReasoningEffortHigh, ReasoningEffortXHigh, ReasoningEffortMax,
			},
			wantFamily: "gpt-5.6",
		},
		{
			name:        "DeepSeek V4 Pro exposes only its documented levels",
			model:       CustomModel{Provider: "openai", APIStyle: "chat_completions", ExternalModelName: "DeepSeek-V4-Pro"},
			wantKind:    ReasoningStrategyDeepSeekOpenAI,
			wantOptions: []string{ReasoningEffortAuto, ReasoningEffortHigh, ReasoningEffortMax},
			wantToggle:  true,
			wantFamily:  "deepseek-v4",
		},
		{
			name:        "Claude Opus 4.5 supports effort plus legacy budget fallback",
			model:       CustomModel{Provider: "anthropic", APIStyle: "messages", ExternalModelName: "claude-opus-4.5-20251101"},
			wantKind:    ReasoningStrategyAnthropicEffort,
			wantOptions: []string{ReasoningEffortAuto, ReasoningEffortLow, ReasoningEffortMedium, ReasoningEffortHigh},
			wantBudget:  true,
			wantToggle:  true,
			wantFamily:  "claude-opus-4.5",
		},
		{
			name:     "Claude Opus 4.7 supports xhigh and max",
			model:    CustomModel{Provider: "anthropic", APIStyle: "messages", ExternalModelName: "claude-opus-4.7"},
			wantKind: ReasoningStrategyAnthropicEffort,
			wantOptions: []string{
				ReasoningEffortAuto, ReasoningEffortLow, ReasoningEffortMedium,
				ReasoningEffortHigh, ReasoningEffortXHigh, ReasoningEffortMax,
			},
			wantFamily: "claude-opus-4.7",
		},
		{
			name:     "Claude Sonnet 4.6 supports max but not xhigh",
			model:    CustomModel{Provider: "anthropic", APIStyle: "messages", ExternalModelName: "claude-sonnet-4.6"},
			wantKind: ReasoningStrategyAnthropicEffort,
			wantOptions: []string{
				ReasoningEffortAuto, ReasoningEffortLow, ReasoningEffortMedium,
				ReasoningEffortHigh, ReasoningEffortMax,
			},
			wantFamily: "claude-sonnet-4.6",
		},
		{
			name:        "Unknown models remain automatic",
			model:       CustomModel{Provider: "openai", APIStyle: "auto", ExternalModelName: "my-private-reasoner"},
			wantKind:    ReasoningStrategyUnsupported,
			wantOptions: []string{ReasoningEffortAuto},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			profile := ReasoningProfileForModel(test.model)
			if profile.Kind != test.wantKind || profile.Strategy != test.wantKind {
				t.Fatalf("strategy = kind:%q strategy:%q, want %q", profile.Kind, profile.Strategy, test.wantKind)
			}
			if !reflect.DeepEqual(profile.SupportedEfforts, test.wantOptions) {
				t.Fatalf("supported efforts = %v, want %v", profile.SupportedEfforts, test.wantOptions)
			}
			if profile.LegacyBudget != test.wantBudget || profile.SupportsBudget != test.wantBudget {
				t.Fatalf("budget profile = legacy:%v supported:%v, want %v", profile.LegacyBudget, profile.SupportsBudget, test.wantBudget)
			}
			if profile.SupportsThinkingToggle != test.wantToggle {
				t.Fatalf("supports thinking toggle = %v, want %v", profile.SupportsThinkingToggle, test.wantToggle)
			}
			if profile.ModelFamily != test.wantFamily {
				t.Fatalf("model family = %q, want %q", profile.ModelFamily, test.wantFamily)
			}
		})
	}
}

func TestNormalizeReasoningEffortOnlyKeepsSupportedValues(t *testing.T) {
	deepSeek := CustomModel{Provider: "openai", APIStyle: "auto", ExternalModelName: "deepseek-v4-pro"}
	if got := NormalizeReasoningEffort(deepSeek, ReasoningEffortLow); got != ReasoningEffortHigh {
		t.Fatalf("DeepSeek low = %q, want high", got)
	}
	if got := NormalizeReasoningEffort(deepSeek, ReasoningEffortMedium); got != ReasoningEffortHigh {
		t.Fatalf("DeepSeek medium = %q, want high", got)
	}
	if got := NormalizeReasoningEffort(deepSeek, ReasoningEffortXHigh); got != ReasoningEffortMax {
		t.Fatalf("DeepSeek xhigh = %q, want max", got)
	}
	if got := NormalizeReasoningEffort(deepSeek, ReasoningEffortNone); got != ReasoningEffortAuto {
		t.Fatalf("DeepSeek none = %q, want auto", got)
	}

	gpt := CustomModel{Provider: "openai", APIStyle: "responses", ExternalModelName: "gpt-5.6-sol"}
	if got := NormalizeReasoningEffort(gpt, ReasoningEffortMinimal); got != ReasoningEffortAuto {
		t.Fatalf("GPT 5.6 minimal = %q, want auto", got)
	}
	if got := NormalizeReasoningEffort(gpt, ReasoningEffortMax); got != ReasoningEffortMax {
		t.Fatalf("GPT 5.6 max = %q, want max", got)
	}

	sonnet46 := CustomModel{Provider: "anthropic", APIStyle: "messages", ExternalModelName: "claude-sonnet-4.6"}
	if got := NormalizeReasoningEffort(sonnet46, ReasoningEffortXHigh); got != ReasoningEffortAuto {
		t.Fatalf("Claude Sonnet 4.6 xhigh = %q, want auto", got)
	}
	if got := NormalizeReasoningEffort(sonnet46, ReasoningEffortMax); got != ReasoningEffortMax {
		t.Fatalf("Claude Sonnet 4.6 max = %q, want max", got)
	}

	unknown := CustomModel{Provider: "openai", APIStyle: "auto", ExternalModelName: "unverified-model"}
	if got := NormalizeReasoningEffort(unknown, ReasoningEffortHigh); got != ReasoningEffortAuto {
		t.Fatalf("unknown high = %q, want auto", got)
	}
}

func TestNormalizeModelReasoningPersistsSafeSelections(t *testing.T) {
	Init(t.TempDir())
	falseValue := false
	if err := SaveModels([]CustomModel{
		{
			Name: "models/gpt", Provider: "openai", APIStyle: "auto", ExternalModelName: "gpt-5.6-sol",
			ReasoningEffort: ReasoningEffortMinimal,
		},
		{
			Name: "models/deepseek", Provider: "openai", APIStyle: "auto", ExternalModelName: "deepseek-v4-pro",
			ReasoningEffort: ReasoningEffortXHigh, ThinkingEnabled: &falseValue,
		},
		{
			Name: "models/claude", Provider: "anthropic", APIStyle: "messages", ExternalModelName: "claude-sonnet-4.5",
			ReasoningEffort: ReasoningEffortHigh, ReasoningBudgetTokens: 1500,
		},
		{
			Name: "models/claude-migrated", Provider: "anthropic", APIStyle: "messages", ExternalModelName: "claude-sonnet-4.5",
			ReasoningEffort: ReasoningEffortMedium,
		},
	}); err != nil {
		t.Fatal(err)
	}

	models, err := LoadModels()
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 4 {
		t.Fatalf("model count = %d, want 4", len(models))
	}
	if models[0].ReasoningEffort != ReasoningEffortAuto {
		t.Fatalf("GPT unsupported minimal persisted as %q, want auto", models[0].ReasoningEffort)
	}
	if models[1].ReasoningEffort != ReasoningEffortMax {
		t.Fatalf("DeepSeek xhigh persisted as %q, want max", models[1].ReasoningEffort)
	}
	if models[1].ThinkingEnabled == nil || *models[1].ThinkingEnabled {
		t.Fatalf("DeepSeek thinking toggle was not preserved as false: %#v", models[1].ThinkingEnabled)
	}
	if models[2].ReasoningEffort != ReasoningEffortAuto {
		t.Fatalf("legacy Claude effort persisted as %q, want auto", models[2].ReasoningEffort)
	}
	if models[2].ReasoningBudgetTokens != 2048 {
		t.Fatalf("legacy Claude budget = %d, want rounded protocol-safe 2048", models[2].ReasoningBudgetTokens)
	}
	if models[3].ReasoningEffort != ReasoningEffortAuto || models[3].ReasoningBudgetTokens != 4096 {
		t.Fatalf("migrated legacy Claude = %#v, want automatic effort and a 4096 budget", models[3])
	}
	if models[3].ThinkingEnabled == nil || !*models[3].ThinkingEnabled {
		t.Fatalf("migrated legacy Claude must retain its enabled thinking setting: %#v", models[3].ThinkingEnabled)
	}
}

func TestLegacyClaudeBudgetNormalization(t *testing.T) {
	profile := ReasoningProfileForModel(CustomModel{Provider: "anthropic", APIStyle: "messages", ExternalModelName: "claude-sonnet-4.5"})
	if profile.Kind != ReasoningStrategyAnthropicLegacyBudget || !profile.SupportsBudget || !profile.LegacyBudget {
		t.Fatalf("legacy profile = %#v", profile)
	}
	if got := NormalizeReasoningBudget(profile, 1); got != 1024 {
		t.Fatalf("minimum budget = %d, want 1024", got)
	}
	if got := NormalizeReasoningBudget(profile, 1025); got != 2048 {
		t.Fatalf("rounded budget = %d, want 2048", got)
	}
}
