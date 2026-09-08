package proxy

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"antigravity-wf-assistant/internal/storage"
	"github.com/andybalholm/brotli"
)

// modelFetchRoundTripper keeps fetchAvailableModels tests entirely local.
type modelFetchRoundTripper func(*http.Request) (*http.Response, error)

func (fn modelFetchRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func newModelFetchTestClient(transport http.RoundTripper) *http.Client {
	return &http.Client{Transport: transport}
}

func TestInjectCustomModelsKeepsWrappedNativeShapeAndPickerIndexes(t *testing.T) {
	storage.Init(t.TempDir())
	model := storage.NewDiscoveredModel("openai", "https://api.example.test", "secret", "gpt-custom")
	parsed := map[string]any{
		"response": map[string]any{
			"models": []any{map[string]any{
				"name": "models/native", "model": "MODEL_PLACEHOLDER_M0", "displayName": "Native",
			}},
			"agentModelSorts": []any{map[string]any{
				"groups": []any{map[string]any{"modelIds": []any{"native-model"}}},
			}},
		},
	}

	summary := injectCustomModels(parsed, []storage.CustomModel{model})
	if err := validateModelInjection(parsed, []storage.CustomModel{model}, summary); err != nil {
		t.Fatal(err)
	}
	if summary.customCount != 1 || len(summary.indexPaths) == 0 {
		t.Fatalf("unexpected injection summary: %+v", summary)
	}
	root := parsed["response"].(map[string]any)
	entries := root["models"].([]any)
	found := false
	modelSlug := summary.assignments.slugs[modelPlaceholderKey(model)]
	modelPlaceholder := summary.assignments.placeholders[modelPlaceholderKey(model)]
	for _, raw := range entries {
		entry, _ := raw.(map[string]any)
		if entry["name"] == "models/"+modelSlug {
			found = true
			if entry["displayName"] != "gpt-custom" || entry["supportsImages"] != true || entry["supportsAudio"] != false || entry["supportsVideo"] != false || entry["supportsWebSearch"] != false || entry["supportsImageGeneration"] != true {
				t.Fatalf("injected capability/name fields are incomplete: %#v", entry)
			}
		}
	}
	if !found {
		t.Fatalf("custom model was not injected into wrapped array: %#v", entries)
	}
	if !responseIndexesModel(collectModelResponseRoots(parsed), modelSlug, modelPlaceholder) {
		t.Fatal("custom model was not added to a picker index")
	}
}

func TestDecodeModelResponseEncodings(t *testing.T) {
	plain := []byte(`{"models":{"native":{"model":"MODEL_PLACEHOLDER_M1"}}}`)
	cases := []struct {
		name     string
		encoding string
		encode   func([]byte) []byte
	}{
		{name: "identity", encoding: "identity", encode: func(value []byte) []byte { return value }},
		{name: "gzip", encoding: "gzip", encode: func(value []byte) []byte {
			var out bytes.Buffer
			writer := gzip.NewWriter(&out)
			_, _ = writer.Write(value)
			_ = writer.Close()
			return out.Bytes()
		}},
		{name: "zlib", encoding: "deflate", encode: func(value []byte) []byte {
			var out bytes.Buffer
			writer := zlib.NewWriter(&out)
			_, _ = writer.Write(value)
			_ = writer.Close()
			return out.Bytes()
		}},
		{name: "raw-deflate", encoding: "deflate", encode: func(value []byte) []byte {
			var out bytes.Buffer
			writer, _ := flate.NewWriter(&out, flate.DefaultCompression)
			_, _ = writer.Write(value)
			_ = writer.Close()
			return out.Bytes()
		}},
		{name: "brotli", encoding: "br", encode: func(value []byte) []byte {
			var out bytes.Buffer
			writer := brotli.NewWriter(&out)
			_, _ = writer.Write(value)
			_ = writer.Close()
			return out.Bytes()
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			decoded, err := decodeModelResponse(testCase.encode(plain), testCase.encoding)
			if err != nil {
				t.Fatal(err)
			}
			var parsed map[string]any
			if err := json.Unmarshal(decoded, &parsed); err != nil {
				t.Fatal(err)
			}
			if parsed["models"] == nil {
				t.Fatalf("decoded response lost models: %s", decoded)
			}
		})
	}
}

func TestInjectCustomModelsSupportsMapAndEverySortGroup(t *testing.T) {
	models := []storage.CustomModel{{
		Name: "models/gpt-test", DisplayName: "gpt-5.6-sol", UpstreamName: "无风", ExternalModelName: "gpt-test",
	}}
	parsed := map[string]any{
		"models": map[string]any{
			"official": map[string]any{"model": "MODEL_PLACEHOLDER_M1"},
		},
		"agentModelSorts": []any{
			map[string]any{"groups": []any{
				map[string]any{"modelIds": []any{"official"}},
				map[string]any{"modelIds": []any{"other"}},
			}},
			map[string]any{"groups": []any{
				map[string]any{"modelIds": []any{"fast"}},
			}},
		},
	}

	summary := injectCustomModels(parsed, models)
	if summary.customCount != 1 || summary.officialCount != 1 {
		t.Fatalf("unexpected injection summary: %+v", summary)
	}
	modelMap := parsed["models"].(map[string]any)
	entry, ok := modelMap["custom-gpt-test"].(map[string]any)
	if !ok || entry["displayName"] != "gpt-5.6-sol · 无风" {
		t.Fatalf("custom map entry missing: %v", modelMap)
	}
	for _, rawSort := range parsed["agentModelSorts"].([]any) {
		for _, rawGroup := range rawSort.(map[string]any)["groups"].([]any) {
			ids := rawGroup.(map[string]any)["modelIds"].([]any)
			if len(ids) == 0 || ids[0] != "custom-gpt-test" {
				t.Fatalf("custom model missing from sort group: %v", ids)
			}
		}
	}
}

func TestInjectCustomModelsSupportsArrayAndAlternateContainer(t *testing.T) {
	models := []storage.CustomModel{{
		Name: "models/claude-test", DisplayName: "gpt-5.6-sol", UpstreamName: "无风", ExternalModelName: "claude-test",
	}}
	parsed := map[string]any{
		"availableModels": []any{map[string]any{
			"name": "models/official", "displayName": "Official",
		}},
		"agentModelSorts": []any{map[string]any{"groups": []any{
			map[string]any{"modelIds": []any{"official"}},
		}}},
	}

	summary := injectCustomModels(parsed, models)
	if summary.customCount != 1 || summary.officialCount != 1 {
		t.Fatalf("unexpected injection summary: %+v", summary)
	}
	entries := parsed["availableModels"].([]any)
	if len(entries) != 2 {
		t.Fatalf("expected injected and official array models: %v", entries)
	}
	custom := entries[0].(map[string]any)
	if custom["name"] != "models/custom-claude-test" || custom["displayName"] != "gpt-5.6-sol · 无风" {
		t.Fatalf("unexpected custom array entry: %v", custom)
	}
	sorts := parsed["agentModelSorts"].([]any)
	ids := sorts[0].(map[string]any)["groups"].([]any)[0].(map[string]any)["modelIds"].([]any)
	if len(ids) != 2 || ids[0] != "custom-claude-test" || ids[1] != "official" {
		t.Fatalf("custom sort was not updated: %v", ids)
	}
}

func TestInjectCustomModelsSupportsNestedResponseIndexesAndSlugCollisions(t *testing.T) {
	models := []storage.CustomModel{
		{Name: "models/a", DisplayName: "First", ExternalModelName: "Same Model"},
		{Name: "models/b", DisplayName: "Second", ExternalModelName: "same-model"},
	}
	parsed := map[string]any{
		"response": map[string]any{
			"models": map[string]any{
				"official": map[string]any{"model": "MODEL_PLACEHOLDER_M1"},
			},
			"agentModelSorts": []any{map[string]any{"groups": []any{
				map[string]any{"modelIds": []any{"official"}},
			}}},
			"battleModeModelSorts": []any{map[string]any{"groups": []any{
				map[string]any{"modelIds": []any{"official"}},
			}}},
			"tieredModelIds": map[string]any{"recommended": []any{"official"}},
		},
	}

	summary := injectCustomModels(parsed, models)
	if summary.customCount != 2 {
		t.Fatalf("unexpected injection summary: %+v", summary)
	}
	if err := validateModelInjection(parsed, models, summary); err != nil {
		t.Fatal(err)
	}
	if len(summary.customSlugs) != 2 || summary.customSlugs[0] == summary.customSlugs[1] {
		t.Fatalf("slug collision was not resolved: %v", summary.customSlugs)
	}
	response := parsed["response"].(map[string]any)
	modelMap := response["models"].(map[string]any)
	for _, slug := range summary.customSlugs {
		if _, ok := modelMap[slug]; !ok {
			t.Fatalf("nested model %s missing: %v", slug, modelMap)
		}
	}
	for _, key := range []string{"agentModelSorts", "battleModeModelSorts"} {
		ids := response[key].([]any)[0].(map[string]any)["groups"].([]any)[0].(map[string]any)["modelIds"].([]any)
		if len(ids) < 3 || ids[0] != summary.customSlugs[0] || ids[1] != summary.customSlugs[1] {
			t.Fatalf("%s was not updated: %v", key, ids)
		}
	}
	tiered := response["tieredModelIds"].(map[string]any)["recommended"].([]any)
	if len(tiered) < 3 || tiered[0] != summary.customSlugs[0] || tiered[1] != summary.customSlugs[1] {
		t.Fatalf("tiered model index was not updated: %v", tiered)
	}
	if len(summary.containers) != 1 || summary.containers[0] != "response.models:map" {
		t.Fatalf("nested response path was not diagnosed: %v", summary.containers)
	}
}

func TestInjectCustomModelsAddsOnlyDirectOpenAIImageModelsToImageGenerationIndex(t *testing.T) {
	imageModel := storage.CustomModel{
		Name: "models/image", DisplayName: "Image model", ExternalModelName: "gpt-image-2",
		Provider: "openai", APIStyle: "responses",
		Capabilities: storage.ModelCapabilities{
			Configured: true, SupportsImages: true, SupportsFiles: true,
			SupportsToolCalls: true, SupportsImageGeneration: true,
		},
	}
	textModel := storage.CustomModel{
		Name: "models/text", DisplayName: "Text model", ExternalModelName: "gpt-text",
		Provider: "openai", APIStyle: "chat_completions",
		Capabilities: storage.ModelCapabilities{
			Configured: true, SupportsImages: true, SupportsFiles: true, SupportsToolCalls: true,
		},
	}
	incompatibleImageModel := storage.CustomModel{
		Name: "models/foreign-image", DisplayName: "Foreign image model", ExternalModelName: "dall-e-3",
		Provider: "anthropic", APIStyle: "messages",
		Capabilities: storage.ModelCapabilities{
			Configured: true, SupportsImages: true, SupportsImageGeneration: true,
		},
	}
	parsed := map[string]any{
		"models": map[string]any{
			"native-image": map[string]any{"model": "MODEL_PLACEHOLDER_M1"},
		},
		"agentModelSorts": []any{map[string]any{
			"groups": []any{map[string]any{"modelIds": []any{"native-image"}}},
		}},
		"imageGenerationModelIds": []any{"native-image"},
	}

	modelsToInject := []storage.CustomModel{imageModel, textModel, incompatibleImageModel}
	summary := injectCustomModels(parsed, modelsToInject)
	if err := validateModelInjection(parsed, modelsToInject, summary); err != nil {
		t.Fatal(err)
	}

	imageSlug := summary.assignments.slugs[modelPlaceholderKey(imageModel)]
	textSlug := summary.assignments.slugs[modelPlaceholderKey(textModel)]
	incompatibleImageSlug := summary.assignments.slugs[modelPlaceholderKey(incompatibleImageModel)]
	imageIDs := parsed["imageGenerationModelIds"].([]any)
	if len(imageIDs) != 2 || imageIDs[0] != imageSlug || imageIDs[1] != "native-image" {
		t.Fatalf("image-generation index = %#v, want only %q plus native model", imageIDs, imageSlug)
	}
	for _, id := range imageIDs {
		if id == textSlug || id == incompatibleImageSlug {
			t.Fatalf("non-direct-image model leaked into image-generation index: %#v", imageIDs)
		}
	}
	if len(summary.assignments.nativeImageModelIDs) != 1 || summary.assignments.nativeImageModelIDs[0] != "native-image" {
		t.Fatalf("native image route was not captured: %#v", summary.assignments.nativeImageModelIDs)
	}

	models := parsed["models"].(map[string]any)
	if models[imageSlug].(map[string]any)["requiresImageOutputOutsideFunctionResponses"] != true {
		t.Fatalf("image model is missing image-output presentation capability: %#v", models[imageSlug])
	}
	if models[textSlug].(map[string]any)["requiresImageOutputOutsideFunctionResponses"] != true {
		t.Fatalf("OpenAI Chat model must expose the proxy's dedicated Images bridge: %#v", models[textSlug])
	}
	imageIndexModified := false
	for _, path := range summary.indexPaths {
		if path == "imageGenerationModelIds" {
			imageIndexModified = true
			break
		}
	}
	if !imageIndexModified {
		t.Fatalf("image-generation index update was not diagnosed: %#v", summary.indexPaths)
	}
}

func TestInjectCustomModelsDoesNotCreateUnknownImageGenerationIndex(t *testing.T) {
	model := storage.CustomModel{
		Name: "models/image", DisplayName: "Image model", ExternalModelName: "gpt-image-2",
		Provider: "openai", APIStyle: "responses",
		Capabilities: storage.ModelCapabilities{Configured: true, SupportsImageGeneration: true},
	}
	parsed := map[string]any{"models": map[string]any{}}
	injectCustomModels(parsed, []storage.CustomModel{model})
	if _, exists := parsed["imageGenerationModelIds"]; exists {
		t.Fatalf("older model response unexpectedly gained an image-generation index: %#v", parsed)
	}
}

func TestInjectCustomModelsLeavesUnknownImageGenerationIndexShapeUntouched(t *testing.T) {
	imageModel := storage.CustomModel{
		Name: "models/image", DisplayName: "Image model", ExternalModelName: "gpt-image-2",
		Provider: "openai", APIStyle: "responses",
		Capabilities: storage.ModelCapabilities{Configured: true, SupportsImageGeneration: true},
	}
	unknownIndex := map[string]any{"differentProto": []any{"native-image"}}
	parsed := map[string]any{
		"models": map[string]any{"native-image": map[string]any{"model": "MODEL_PLACEHOLDER_M1"}},
		"agentModelSorts": []any{map[string]any{
			"groups": []any{map[string]any{"modelIds": []any{"native-image"}}},
		}},
		"imageGenerationModelIds": unknownIndex,
	}

	summary := injectCustomModels(parsed, []storage.CustomModel{imageModel})
	if err := validateModelInjection(parsed, []storage.CustomModel{imageModel}, summary); err != nil {
		t.Fatal(err)
	}
	gotIndex, ok := parsed["imageGenerationModelIds"].(map[string]any)
	if !ok {
		t.Fatalf("unknown image-generation index was replaced: %#v", parsed["imageGenerationModelIds"])
	}
	if got := gotIndex["differentProto"].([]any); len(got) != 1 || got[0] != "native-image" {
		t.Fatalf("unknown image-generation index was modified: %#v", gotIndex)
	}
	for _, path := range summary.indexPaths {
		if path == "imageGenerationModelIds" {
			t.Fatalf("unknown image-generation index was treated as a recognized schema: %#v", summary.indexPaths)
		}
	}
}

func TestValidateModelInjectionRejectsMissingPickerIndex(t *testing.T) {
	models := []storage.CustomModel{{Name: "models/test", DisplayName: "Test", ExternalModelName: "test"}}
	parsed := map[string]any{"models": map[string]any{}}
	summary := injectCustomModels(parsed, models)
	if _, exists := parsed["agentModelSorts"]; exists {
		t.Fatalf("injection invented agentModelSorts for an unknown picker protocol: %#v", parsed)
	}
	if err := validateModelInjection(parsed, models, summary); err == nil {
		t.Fatal("expected validation failure for model without a picker index")
	}
}

func TestHandleFetchAvailableModelsInjectsIntoCompressedJSONWithoutChangingNativeModels(t *testing.T) {
	stateDir := t.TempDir()
	storage.Init(stateDir)
	InitTrace(stateDir)
	custom := storage.CustomModel{
		Name: "models/gpt-wf", DisplayName: "GPT WF", Description: "custom upstream",
		Provider: "openai", ExternalModelName: "gpt-wf",
	}
	if err := storage.SaveModels([]storage.CustomModel{custom}); err != nil {
		t.Fatal(err)
	}

	native := map[string]any{
		"name":             "models/native-gemini",
		"displayName":      "Native Gemini",
		"supportsImages":   true,
		"nativeCapability": map[string]any{"keep": true},
	}
	payload, err := json.Marshal(map[string]any{
		"response": map[string]any{
			"availableModels": []any{native},
			"agentModelSorts": []any{map[string]any{
				"displayName": "Native",
				"groups":      []any{map[string]any{"modelIds": []any{"native-gemini"}}},
			}},
			"imageGenerationModelIds": []any{"native-gemini"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	client := newModelFetchTestClient(modelFetchRoundTripper(func(request *http.Request) (*http.Response, error) {
		if got, want := request.URL.String(), googleBaseURL+"/v1internal:fetchAvailableModels"; got != want {
			t.Fatalf("upstream URL = %q, want %q", got, want)
		}
		if got := request.Header.Get("Accept-Encoding"); got != "identity" {
			t.Fatalf("upstream Accept-Encoding = %q, want identity", got)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type":     []string{"application/json"},
				"Content-Encoding": []string{"gzip"},
				"X-Native-Model":   []string{"preserved"},
			},
			Body:    io.NopCloser(bytes.NewReader(compressed.Bytes())),
			Request: request,
		}, nil
	}))

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1internal:fetchAvailableModels", strings.NewReader(`{"client":"antigravity"}`))
	handleFetchAvailableModelsWithClient(recorder, request, client)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("decoded response must not retain Content-Encoding, got %q", got)
	}
	if got := recorder.Header().Get("X-Native-Model"); got != "preserved" {
		t.Fatalf("native response header = %q", got)
	}

	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	root := response["response"].(map[string]any)
	entries := root["availableModels"].([]any)
	if len(entries) != 2 {
		t.Fatalf("availableModels count = %d, want custom + native: %#v", len(entries), entries)
	}
	injected := entries[0].(map[string]any)
	if injected["name"] != "models/custom-gpt-wf" || injected["displayName"] != "GPT WF" {
		t.Fatalf("unexpected injected model: %#v", injected)
	}
	if injected["supportsImages"] != true || injected["supportsAudio"] != false || injected["supportsVideo"] != false || injected["supportsToolCalls"] != true {
		t.Fatalf("custom model capability declaration is incomplete: %#v", injected)
	}
	if injected["requiresImageOutputOutsideFunctionResponses"] != true {
		t.Fatalf("image-capable model is missing image-output presentation capability: %#v", injected)
	}
	nativeAfter := entries[1].(map[string]any)
	if nativeAfter["displayName"] != "Native Gemini" || nativeAfter["nativeCapability"].(map[string]any)["keep"] != true {
		t.Fatalf("native model was modified: %#v", nativeAfter)
	}
	ids := root["agentModelSorts"].([]any)[0].(map[string]any)["groups"].([]any)[0].(map[string]any)["modelIds"].([]any)
	if len(ids) != 2 || ids[0] != "custom-gpt-wf" || ids[1] != "native-gemini" {
		t.Fatalf("picker indexes = %#v", ids)
	}
	imageIDs := root["imageGenerationModelIds"].([]any)
	if len(imageIDs) != 1 || imageIDs[0] != "native-gemini" {
		t.Fatalf("native image-generation indexes were modified = %#v", imageIDs)
	}
	assignments := snapshotModelRouteAssignments()
	if len(assignments.nativeImageModelIDs) != 1 || assignments.nativeImageModelIDs[0] != "native-gemini" {
		t.Fatalf("native image model routing was not committed: %#v", assignments.nativeImageModelIDs)
	}

	diagnostics := GetDiagnostics()
	if diagnostics.LastInjectedModelCount != 1 || diagnostics.LastModelShape != "response.availableModels:array" || diagnostics.LastError != "" {
		t.Fatalf("unexpected model diagnostics: %+v", diagnostics)
	}
}

func TestHandleFetchAvailableModelsForwardsUnusableUpstreamResponsesUntouched(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		encoding string
		body     []byte
	}{
		{name: "upstream-error", status: http.StatusTooManyRequests, encoding: "gzip", body: []byte(`quota exhausted`)},
		{name: "invalid-json", status: http.StatusOK, body: []byte(`{"models":`)},
		{name: "invalid-gzip", status: http.StatusOK, encoding: "gzip", body: []byte(`not a gzip stream`)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stateDir := t.TempDir()
			storage.Init(stateDir)
			InitTrace(stateDir)
			if err := storage.SaveModels([]storage.CustomModel{{Name: "models/should-not-appear", DisplayName: "Should not appear", ExternalModelName: "should-not-appear"}}); err != nil {
				t.Fatal(err)
			}

			client := newModelFetchTestClient(modelFetchRoundTripper(func(request *http.Request) (*http.Response, error) {
				header := http.Header{"Content-Type": []string{"application/json"}, "X-Upstream-Error": []string{"kept"}}
				if test.encoding != "" {
					header.Set("Content-Encoding", test.encoding)
				}
				return &http.Response{
					StatusCode: test.status,
					Header:     header,
					Body:       io.NopCloser(bytes.NewReader(test.body)),
					Request:    request,
				}, nil
			}))

			recorder := httptest.NewRecorder()
			handleFetchAvailableModelsWithClient(recorder, httptest.NewRequest(http.MethodPost, "/v1internal:fetchAvailableModels", nil), client)

			if recorder.Code != test.status {
				t.Fatalf("status = %d, want %d", recorder.Code, test.status)
			}
			if !bytes.Equal(recorder.Body.Bytes(), test.body) {
				t.Fatalf("raw upstream body changed: got %q, want %q", recorder.Body.Bytes(), test.body)
			}
			if got := recorder.Header().Get("X-Upstream-Error"); got != "kept" {
				t.Fatalf("upstream header = %q", got)
			}
			if got := recorder.Header().Get("Content-Encoding"); got != test.encoding {
				t.Fatalf("content encoding = %q, want %q", got, test.encoding)
			}
			diagnostics := GetDiagnostics()
			if diagnostics.LastError == "" || diagnostics.LastModelStatusCode != test.status {
				t.Fatalf("unusable response was not diagnosed: %+v", diagnostics)
			}
		})
	}
}

func TestDisabledModelsAreNotInjectedOrRoutable(t *testing.T) {
	stateDir := t.TempDir()
	storage.Init(stateDir)
	InitTrace(stateDir)
	disabled := false
	models := []storage.CustomModel{
		{Name: "models/enabled", DisplayName: "Enabled", ExternalModelName: "enabled"},
		{Name: "models/disabled", DisplayName: "Disabled", ExternalModelName: "disabled", Enabled: &disabled},
	}
	if err := storage.SaveModels(models); err != nil {
		t.Fatal(err)
	}
	if found := findModel("models/disabled"); found != nil {
		t.Fatalf("disabled model remained routable: %#v", found)
	}
	if found := findModel("models/enabled"); found == nil {
		t.Fatal("enabled model was not routable")
	}

	payload := []byte(`{"models":{},"agentModelSorts":[{"groups":[{"modelIds":[]}]}]}`)
	client := newModelFetchTestClient(modelFetchRoundTripper(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewReader(payload)),
			Request:    request,
		}, nil
	}))
	recorder := httptest.NewRecorder()
	handleFetchAvailableModelsWithClient(recorder, httptest.NewRequest(http.MethodPost, "/v1internal:fetchAvailableModels", nil), client)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "disabled") || !strings.Contains(recorder.Body.String(), "enabled") {
		t.Fatalf("disabled model leaked into picker: %s", recorder.Body.String())
	}
}

func TestJSONShapeRedactsValues(t *testing.T) {
	shape, err := json.Marshal(jsonShape(map[string]any{
		"displayName": "secret model name",
		"model":       "MODEL_PLACEHOLDER_M42",
		"recommended": true,
	}, 0))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(shape, []byte("secret model name")) || bytes.Contains(shape, []byte("M42")) {
		t.Fatalf("shape leaked values: %s", shape)
	}
}
