package storage

import (
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestModelStoreUsesPrivateAtomicFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	Init(dir)
	model := CustomModel{
		Name: "models/test", DisplayName: "Test", Provider: "openai",
		APIKey: "secret", APIURL: "https://example.com/v1", ExternalModelName: "gpt-test",
	}
	if err := SaveModels([]CustomModel{model}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "custom_models.json"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("model file permissions = %o, want 600", info.Mode().Perm())
	}
	loaded, err := LoadModels()
	if err != nil || len(loaded) != 1 || loaded[0].APIKey != model.APIKey {
		t.Fatalf("round trip failed: %+v, %v", loaded, err)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, ".custom-models-*"))
	if len(matches) != 0 {
		t.Fatalf("temporary files were left behind: %v", matches)
	}
}

func TestEmptyDisplayNameUsesUpstreamModelName(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	Init(dir)
	model := CustomModel{
		Name: "models/gpt-test", Provider: "openai", APIKey: "secret",
		APIURL: "https://example.com/v1", ExternalModelName: "gpt-test",
	}
	if err := AddOrUpdateModel(model); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadModels()
	if err != nil || len(loaded) != 1 {
		t.Fatalf("load failed: %+v, %v", loaded, err)
	}
	if loaded[0].DisplayName != "gpt-test" {
		t.Fatalf("displayName = %q, want upstream model name", loaded[0].DisplayName)
	}
}

func TestUpstreamNameRoundTripsAsTrimmedPresentationMetadata(t *testing.T) {
	dir := t.TempDir()
	Init(dir)
	model := NewDiscoveredModel("openai", "https://example.com/v1", "secret", "gpt-test")
	model.UpstreamName = "  XIASS 主线路  "
	if err := AddOrUpdateModel(model); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadModels()
	if err != nil || len(loaded) != 1 {
		t.Fatalf("load failed: %#v, %v", loaded, err)
	}
	if loaded[0].UpstreamName != "XIASS 主线路" {
		t.Fatalf("upstreamName = %q", loaded[0].UpstreamName)
	}
}

func TestVisibleModelDisplayNameUsesSupplierSuffixForDirectModel(t *testing.T) {
	model := NewDiscoveredModel("openai", "https://api.example.test", "secret", "gpt-5.6-sol")
	model.UpstreamName = "无风"
	if got, want := VisibleModelDisplayName(model), "gpt-5.6-sol · 无风"; got != want {
		t.Fatalf("visible display name = %q, want %q", got, want)
	}
	if model.DisplayName != "gpt-5.6-sol" || model.ExternalModelName != "gpt-5.6-sol" {
		t.Fatalf("visible suffix polluted routing fields: %#v", model)
	}
}

func TestVisibleModelDisplayNamePrefersLiveAccountPoolLabel(t *testing.T) {
	model := NewDiscoveredModel("openai", "https://api.example.test", "secret", "gpt-5.6-sol")
	model.UpstreamName = "旧供应商名称"
	model.AccountPoolLabel = "无风"
	if got, want := VisibleModelDisplayName(model), "gpt-5.6-sol · 无风"; got != want {
		t.Fatalf("visible display name = %q, want %q", got, want)
	}
}

func TestLoadedModelDisplayNameIncludesCurrentAccountPoolNamesWithoutPersistingThem(t *testing.T) {
	dir := t.TempDir()
	Init(dir)
	for _, account := range []UpstreamAccount{
		{ID: "primary", Name: "XIASS主池", Provider: "openai", Type: "api_key", APIKey: "secret-one", Enabled: true},
		{ID: "backup", Name: "备用供应商", Provider: "openai", Type: "api_key", APIKey: "secret-two", Enabled: true},
	} {
		if err := SaveUpstreamAccount(account); err != nil {
			t.Fatal(err)
		}
	}
	model := NewDiscoveredModel("openai", "https://api.example.test", "", "gpt-5.6-sol")
	model.AccountIDs = []string{"primary", "backup"}
	model.AccountPoolLabel = "不应写入磁盘"
	if err := SaveModels([]CustomModel{model}); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "custom_models.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "accountPoolLabel") || strings.Contains(string(raw), "不应写入磁盘") {
		t.Fatalf("runtime account label leaked into model file: %s", raw)
	}

	loaded, err := LoadModels()
	if err != nil || len(loaded) != 1 {
		t.Fatalf("load failed: %#v, %v", loaded, err)
	}
	if loaded[0].AccountPoolLabel != "XIASS主池 / 备用供应商" {
		t.Fatalf("accountPoolLabel = %q", loaded[0].AccountPoolLabel)
	}
	if got := VisibleModelDisplayName(loaded[0]); got != "gpt-5.6-sol · XIASS主池 / 备用供应商" {
		t.Fatalf("visible model name = %q", got)
	}
	if loaded[0].DisplayName != "gpt-5.6-sol" || loaded[0].ExternalModelName != "gpt-5.6-sol" {
		t.Fatalf("real model fields were changed: %#v", loaded[0])
	}

	updated, err := GetUpstreamAccount("primary")
	if err != nil {
		t.Fatal(err)
	}
	updated.Name = "XIASS生产池"
	if err := SaveUpstreamAccount(updated); err != nil {
		t.Fatal(err)
	}
	loaded, err = LoadModels()
	if err != nil || VisibleModelDisplayName(loaded[0]) != "gpt-5.6-sol · XIASS生产池 / 备用供应商" {
		t.Fatalf("renamed account was not reflected: %#v, %v", loaded, err)
	}
}

func TestDirectModelVisibleNameDoesNotGainAccountSuffix(t *testing.T) {
	Init(t.TempDir())
	model := NewDiscoveredModel("openai", "https://api.example.test", "secret", "gpt-direct")
	if err := SaveModels([]CustomModel{model}); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadModels()
	if err != nil || len(loaded) != 1 {
		t.Fatalf("load failed: %#v, %v", loaded, err)
	}
	if loaded[0].AccountPoolLabel != "" || VisibleModelDisplayName(loaded[0]) != "gpt-direct" {
		t.Fatalf("direct model gained a pool suffix: %#v", loaded[0])
	}
}

func TestTransientAccountFailureIsEligibleAgainOnNextRequest(t *testing.T) {
	Init(t.TempDir())
	if err := SaveUpstreamAccount(UpstreamAccount{
		ID: "transient", Name: "瞬时线路", Provider: "openai", Type: "api_key",
		APIURL: "https://api.example.test", APIKey: "secret", Enabled: true, MaxConcurrency: 1,
	}); err != nil {
		t.Fatal(err)
	}
	model := CustomModel{Provider: "openai", AccountIDs: []string{"transient"}}
	_, lease, err := AcquireAccountForModel(model, nil)
	if err != nil || lease == nil {
		t.Fatalf("first acquire failed: lease=%#v err=%v", lease, err)
	}
	lease.Finish(http.StatusServiceUnavailable, "30", "temporary gateway outage")
	stored, err := GetUpstreamAccount("transient")
	if err != nil {
		t.Fatal(err)
	}
	if stored.CooldownUntil != "" || stored.FailureCount != 1 || stored.LastError == "" {
		t.Fatalf("transient health should be recorded without a blacklist: %#v", stored)
	}
	_, nextLease, err := AcquireAccountForModel(model, nil)
	if err != nil || nextLease == nil || nextLease.ID != "transient" {
		t.Fatalf("next independent request did not probe the account again: lease=%#v err=%v", nextLease, err)
	}
	nextLease.Finish(http.StatusOK, "", "")
}

func TestLoadEnabledModelsKeepsLegacyModelsAndFiltersExplicitlyDisabledModels(t *testing.T) {
	Init(t.TempDir())
	disabled := false
	if err := SaveModels([]CustomModel{
		{Name: "models/legacy", ExternalModelName: "legacy"},
		{Name: "models/disabled", ExternalModelName: "disabled", Enabled: &disabled},
	}); err != nil {
		t.Fatal(err)
	}

	models, err := LoadEnabledModels()
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].Name != "models/legacy" {
		t.Fatalf("enabled models = %#v, want only legacy model", models)
	}
}

func TestMergeDiscoveredAccountModelsOnlyPoolsEquivalentUpstreams(t *testing.T) {
	Init(t.TempDir())
	first := NewDiscoveredModel("openai", "https://api.example.test", "", "gpt-pool")
	first.DisplayName = "Keep this model label"
	first.APIKey = "legacy-direct-model-token"
	first.AccountIDs = []string{"first"}
	first.APIStyle = "responses"
	first.Capabilities = ModelCapabilities{Configured: true, SupportsImages: true}

	secondEndpoint := NewDiscoveredModel("openai", "https://other.example.test", "", "gpt-pool")
	secondEndpoint.AccountIDs = []string{"other-endpoint"}
	secondProvider := NewDiscoveredModel("anthropic", "https://api.example.test", "", "gpt-pool")
	secondProvider.AccountIDs = []string{"other-provider"}

	initial, err := MergeDiscoveredAccountModels([]CustomModel{first, secondEndpoint, secondProvider})
	if err != nil || initial.Added != 3 || initial.Bound != 0 {
		t.Fatalf("initial merge = %+v, %v", initial, err)
	}

	sameUpstream := NewDiscoveredModel("openai", "https://api.example.test/", "", "gpt-pool")
	sameUpstream.AccountIDs = []string{"second", "first"}
	sameUpstream.APIStyle = "responses"
	merged, err := MergeDiscoveredAccountModels([]CustomModel{sameUpstream})
	if err != nil || merged.Added != 0 || merged.Bound != 1 || merged.Unchanged != 0 {
		t.Fatalf("matching upstream merge = %+v, %v", merged, err)
	}

	models, err := LoadModels()
	if err != nil || len(models) != 3 {
		t.Fatalf("models = %#v, %v", models, err)
	}
	for _, model := range models {
		if model.Provider == "openai" && strings.TrimRight(model.APIURL, "/") == "https://api.example.test" {
			if model.DisplayName != "Keep this model label" || model.APIStyle != "responses" {
				t.Fatalf("existing model configuration was overwritten: %#v", model)
			}
			if len(model.AccountIDs) != 2 || model.AccountIDs[0] != "first" || model.AccountIDs[1] != "second" {
				t.Fatalf("matching model pool = %#v", model.AccountIDs)
			}
			if model.APIKey != "legacy-direct-model-token" {
				t.Fatalf("pool merge removed the existing direct credential: %#v", model)
			}
		}
	}
}

func TestMergeDiscoveredAccountModelsSeparatesIncompatibleRouteContracts(t *testing.T) {
	Init(t.TempDir())
	base := NewDiscoveredModel("openai", "https://api.example.test", "", "gpt-route-pool")
	base.APIStyle = "responses"
	base.AccountIDs = []string{"responses-primary"}
	if result, err := MergeDiscoveredAccountModels([]CustomModel{base}); err != nil || result.Added != 1 {
		t.Fatalf("save base route = %+v, %v", result, err)
	}

	// Each candidate shares the same provider, endpoint, and upstream model ID,
	// but changes a field that can alter the request route. None may be added to
	// the base account pool.
	incompatible := []struct {
		name    string
		account string
		adjust  func(*CustomModel)
	}{
		{
			name: "API style", account: "chat-peer",
			adjust: func(model *CustomModel) { model.APIStyle = "chat_completions" },
		},
		{
			name: "message path", account: "compat-peer",
			adjust: func(model *CustomModel) { model.MessagePathMode = "compat" },
		},
		{
			name: "endpoint mode", account: "manual-peer",
			adjust: func(model *CustomModel) { model.EndpointMode = "manual" },
		},
	}
	for _, test := range incompatible {
		candidate := NewDiscoveredModel("openai", "https://api.example.test", "", "gpt-route-pool")
		candidate.APIStyle = "responses"
		candidate.AccountIDs = []string{test.account}
		test.adjust(&candidate)
		result, err := MergeDiscoveredAccountModels([]CustomModel{candidate})
		if err != nil || result.Added != 1 || result.Bound != 0 {
			t.Fatalf("%s route merge = %+v, %v", test.name, result, err)
		}
	}

	matching := NewDiscoveredModel("openai", "https://api.example.test/", "", "gpt-route-pool")
	matching.APIStyle = "responses"
	matching.AccountIDs = []string{"responses-peer"}
	result, err := MergeDiscoveredAccountModels([]CustomModel{matching})
	if err != nil || result.Added != 0 || result.Bound != 1 {
		t.Fatalf("matching route merge = %+v, %v", result, err)
	}

	models, err := LoadModels()
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 4 {
		t.Fatalf("model count = %d, want 4 distinct route contracts", len(models))
	}
	seenNames := make(map[string]struct{}, len(models))
	var pooled *CustomModel
	for index := range models {
		model := &models[index]
		if _, exists := seenNames[model.Name]; exists {
			t.Fatalf("route-separated models reused internal name %q", model.Name)
		}
		seenNames[model.Name] = struct{}{}
		for _, accountID := range model.AccountIDs {
			if accountID == "responses-primary" {
				pooled = model
			}
		}
	}
	if pooled == nil {
		t.Fatalf("base route model not found in %#v", models)
	}
	if pooled.APIStyle != "responses" || pooled.EndpointMode != "auto" || pooled.MessagePathMode != "auto" {
		t.Fatalf("base route contract was changed: %#v", pooled)
	}
	if len(pooled.AccountIDs) != 2 || pooled.AccountIDs[0] != "responses-primary" || pooled.AccountIDs[1] != "responses-peer" {
		t.Fatalf("matching account was not the only pool addition: %#v", pooled.AccountIDs)
	}
}

func TestDefaultCapabilitiesExposeCompleteChatSurface(t *testing.T) {
	capabilities := DefaultCapabilities("openai", "gpt-5")
	if !capabilities.SupportsImages || !capabilities.SupportsFiles || capabilities.SupportsAudio || capabilities.SupportsVideo ||
		!capabilities.SupportsToolCalls || !capabilities.SupportsThinking || capabilities.SupportsWebSearch || !capabilities.SupportsImageGeneration {
		t.Fatalf("normal chat model must receive the full default surface: %+v", capabilities)
	}
	if len(capabilities.SupportedMimeTypes) == 0 {
		t.Fatal("full capability profile must advertise attachment MIME types")
	}
}

func TestDefaultCapabilitiesKeepNonChatModelsConservative(t *testing.T) {
	capabilities := DefaultCapabilities("openai", "text-embedding-3-large")
	if capabilities.SupportsImages || capabilities.SupportsFiles || capabilities.SupportsToolCalls || capabilities.SupportsWebSearch || capabilities.SupportsImageGeneration {
		t.Fatalf("non-chat model must not advertise chat capabilities: %+v", capabilities)
	}
}

func TestEffectiveCapabilitiesDoNotReExposeUnsupportedLegacyMediaOrTools(t *testing.T) {
	legacy := CustomModel{
		Provider: "anthropic", APIStyle: "messages", ExternalModelName: "claude-test",
		Capabilities: ModelCapabilities{
			Configured: true, SupportsImages: true, SupportsFiles: true, SupportsAudio: true, SupportsVideo: true,
			SupportsWebSearch: true, SupportsImageGeneration: true,
			SupportedMimeTypes: []string{"image/png", "application/pdf", "audio/mpeg", "video/mp4"},
		},
	}
	capabilities := EffectiveCapabilities(legacy)
	if capabilities.SupportsAudio || capabilities.SupportsVideo || capabilities.SupportsWebSearch || capabilities.SupportsImageGeneration {
		t.Fatalf("Claude Messages must not advertise unsupported media or Responses tools: %+v", capabilities)
	}
	for _, mimeType := range capabilities.SupportedMimeTypes {
		if strings.HasPrefix(mimeType, "audio/") || strings.HasPrefix(mimeType, "video/") {
			t.Fatalf("legacy unsupported MIME type survived migration: %q", mimeType)
		}
	}
}

func TestEffectiveCapabilitiesKeepChatImageGenerationWithoutHostedResponsesWebSearch(t *testing.T) {
	capabilities := EffectiveCapabilities(CustomModel{
		Provider: "openai", APIStyle: "auto", ExternalModelName: "gpt-test",
		Capabilities: ModelCapabilities{Configured: true, SupportsWebSearch: true, SupportsImageGeneration: true},
	})
	if capabilities.SupportsWebSearch || !capabilities.SupportsImageGeneration {
		t.Fatalf("OpenAI automatic routing must use Chat plus the direct Images bridge: %+v", capabilities)
	}

	chatOnly := EffectiveCapabilities(CustomModel{
		Provider: "openai", APIStyle: "chat_completions", ExternalModelName: "gpt-test",
		Capabilities: ModelCapabilities{Configured: true, SupportsWebSearch: true, SupportsImageGeneration: true},
	})
	if chatOnly.SupportsWebSearch || !chatOnly.SupportsImageGeneration {
		t.Fatalf("Chat must hide hosted web search but keep the direct Images bridge: %+v", chatOnly)
	}

	responses := EffectiveCapabilities(CustomModel{
		Provider: "openai", APIStyle: "responses", ExternalModelName: "gpt-test",
		Capabilities: ModelCapabilities{Configured: true, SupportsWebSearch: true, SupportsImageGeneration: true},
	})
	if !responses.SupportsWebSearch || !responses.SupportsImageGeneration {
		t.Fatalf("explicit Responses must retain hosted web search and image generation: %+v", responses)
	}
}

func TestAttachmentCapabilitiesMatchConfiguredProtocol(t *testing.T) {
	tests := []struct {
		name     string
		model    CustomModel
		wantsPDF bool
	}{
		{name: "OpenAI Chat", model: CustomModel{Provider: "openai", APIStyle: "chat_completions", ExternalModelName: "gpt-test"}},
		{name: "OpenAI auto uses Chat", model: CustomModel{Provider: "openai", APIStyle: "auto", ExternalModelName: "gpt-test"}},
		{name: "OpenAI Responses", model: CustomModel{Provider: "openai", APIStyle: "responses", ExternalModelName: "gpt-test"}, wantsPDF: true},
		{name: "Claude Messages", model: CustomModel{Provider: "anthropic", APIStyle: "messages", ExternalModelName: "claude-test"}, wantsPDF: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			capabilities := EffectiveCapabilities(test.model)
			hasPDF, hasSVG := false, false
			for _, mimeType := range capabilities.SupportedMimeTypes {
				hasPDF = hasPDF || mimeType == "application/pdf"
				hasSVG = hasSVG || mimeType == "image/svg+xml"
			}
			if hasPDF != test.wantsPDF {
				t.Fatalf("PDF capability = %v, want %v: %+v", hasPDF, test.wantsPDF, capabilities)
			}
			if hasSVG {
				t.Fatalf("unsupported SVG vision capability was advertised: %+v", capabilities)
			}
		})
	}
}
