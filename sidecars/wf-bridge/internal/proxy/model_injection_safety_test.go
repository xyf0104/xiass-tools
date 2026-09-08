package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"antigravity-wf-assistant/internal/storage"
)

type unknownModelShapeRoundTripper []byte

func (body unknownModelShapeRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(body)),
		Request:    request,
	}, nil
}

// An unknown successful payload is not permission to invent a new Google
// model-list schema. The proxy must leave it byte-for-byte equivalent after
// JSON round-tripping and explain why injection was skipped.
func TestInjectCustomModelsLeavesUnknownResponseShapeUntouched(t *testing.T) {
	restore := replaceModelRouteAssignmentsForTest(modelRouteAssignments{
		placeholders: map[string]string{"models/previous": "MODEL_PLACEHOLDER_M9"},
		slugs:        map[string]string{"models/previous": "custom-previous"},
	})
	t.Cleanup(restore)

	parsed := map[string]any{
		"response": map[string]any{
			"catalog": []any{map[string]any{"id": "native-model"}},
		},
	}
	before, err := json.Marshal(parsed)
	if err != nil {
		t.Fatal(err)
	}
	models := []storage.CustomModel{{
		Name: "models/custom-test", DisplayName: "Custom test", ExternalModelName: "custom-test",
	}}

	summary := injectCustomModels(parsed, models)
	after, err := json.Marshal(parsed)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("unknown response was modified:\n got %s\nwant %s", after, before)
	}
	if !summary.unsupportedShape || len(summary.containers) != 0 || len(summary.indexPaths) != 0 {
		t.Fatalf("unknown shape was not diagnosed safely: %+v", summary)
	}
	assignments := snapshotModelRouteAssignments()
	placeholder := assignments.placeholders["models/previous"]
	placeholderCount := len(assignments.placeholders)
	slug := assignments.slugs["models/previous"]
	slugCount := len(assignments.slugs)
	if placeholder != "MODEL_PLACEHOLDER_M9" || placeholderCount != 1 || slug != "custom-previous" || slugCount != 1 {
		t.Fatalf("unknown shape changed active routing assignments: placeholders=%q/%d slugs=%q/%d", placeholder, placeholderCount, slug, slugCount)
	}
	err = validateModelInjection(parsed, models, summary)
	if err == nil || !strings.Contains(err.Error(), "未知 Antigravity 协议") {
		t.Fatalf("missing explicit compatibility diagnostic: %v", err)
	}
}

func TestHandleFetchAvailableModelsDiagnosesUnknownShapeWithoutInjecting(t *testing.T) {
	stateDir := t.TempDir()
	storage.Init(stateDir)
	InitTrace(stateDir)
	if err := storage.SaveModels([]storage.CustomModel{{
		Name: "models/custom-test", DisplayName: "Custom test", ExternalModelName: "custom-test",
	}}); err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"response":{"catalog":[{"id":"native-model"}]}}`)
	recorder := httptest.NewRecorder()
	handleFetchAvailableModelsWithClient(
		recorder,
		httptest.NewRequest(http.MethodPost, "/v1internal:fetchAvailableModels", nil),
		&http.Client{Transport: unknownModelShapeRoundTripper(payload)},
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	root := response["response"].(map[string]any)
	if _, found := root["models"]; found {
		t.Fatalf("unknown response unexpectedly gained models: %#v", response)
	}
	if _, found := root["agentModelSorts"]; found {
		t.Fatalf("unknown response unexpectedly gained picker indexes: %#v", response)
	}
	if diagnostics := GetDiagnostics(); !strings.Contains(diagnostics.LastError, "未知 Antigravity 协议") {
		t.Fatalf("missing dashboard-compatible diagnostic: %+v", diagnostics)
	}
}

// A models container alone is not evidence that a particular Language Server
// understands agentModelSorts. In particular, Antigravity 2.x can apply a
// later visibility/proto filter after this HTTP response. Never synthesize a
// picker field for that unknown path: preserve both the native body and a
// previously proven route mapping instead.
func TestHandleFetchAvailableModelsFailsClosedWithoutKnownPickerIndex(t *testing.T) {
	restore := replaceModelRouteAssignmentsForTest(modelRouteAssignments{
		placeholders: map[string]string{"models/previous": "MODEL_PLACEHOLDER_M9"},
		slugs:        map[string]string{"models/previous": "custom-previous"},
	})
	t.Cleanup(restore)

	stateDir := t.TempDir()
	storage.Init(stateDir)
	InitTrace(stateDir)
	if err := storage.SaveModels([]storage.CustomModel{{
		Name: "models/custom-test", DisplayName: "Custom test", ExternalModelName: "custom-test",
	}}); err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"models":{"native":{"model":"MODEL_GEMINI_NATIVE"}}}`)
	recorder := httptest.NewRecorder()
	handleFetchAvailableModelsWithClient(
		recorder,
		httptest.NewRequest(http.MethodPost, "/v1internal:fetchAvailableModels", nil),
		&http.Client{Transport: unknownModelShapeRoundTripper(payload)},
	)
	if recorder.Code != http.StatusOK || !bytes.Equal(recorder.Body.Bytes(), payload) {
		t.Fatalf("missing-picker injection changed native response: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if diagnostics := GetDiagnostics(); !strings.Contains(diagnostics.LastError, "模型选择索引") {
		t.Fatalf("missing picker-index diagnostic: %+v", diagnostics)
	}
	assignments := snapshotModelRouteAssignments()
	if len(assignments.placeholders) != 1 || assignments.placeholders["models/previous"] != "MODEL_PLACEHOLDER_M9" ||
		len(assignments.slugs) != 1 || assignments.slugs["models/previous"] != "custom-previous" {
		t.Fatalf("missing-picker response replaced active assignments: %+v", assignments)
	}
}

// imageGenerationModelIds is an execution directory rather than a model-picker
// index. A newer/unknown response that exposes only this field is not enough
// evidence to inject a model, even when it has the expected flat string-list
// type. The original response and already-proven route assignments must win.
func TestHandleFetchAvailableModelsFailsClosedWhenOnlyImageGenerationIndexExists(t *testing.T) {
	restore := replaceModelRouteAssignmentsForTest(modelRouteAssignments{
		placeholders: map[string]string{"models/previous": "MODEL_PLACEHOLDER_M9"},
		slugs:        map[string]string{"models/previous": "custom-previous"},
	})
	t.Cleanup(restore)

	stateDir := t.TempDir()
	storage.Init(stateDir)
	InitTrace(stateDir)
	if err := storage.SaveModels([]storage.CustomModel{{
		Name: "models/custom-image", DisplayName: "Custom image", Provider: "openai", ExternalModelName: "gpt-image-2",
	}}); err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"models":{"native":{"model":"MODEL_GEMINI_NATIVE"}},"imageGenerationModelIds":["native-image"]}`)
	recorder := httptest.NewRecorder()
	handleFetchAvailableModelsWithClient(
		recorder,
		httptest.NewRequest(http.MethodPost, "/v1internal:fetchAvailableModels", nil),
		&http.Client{Transport: unknownModelShapeRoundTripper(payload)},
	)
	if recorder.Code != http.StatusOK || !bytes.Equal(recorder.Body.Bytes(), payload) {
		t.Fatalf("image-directory-only injection changed native response: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if diagnostics := GetDiagnostics(); !strings.Contains(diagnostics.LastError, "模型选择索引") {
		t.Fatalf("image directory was incorrectly accepted as a picker index: %+v", diagnostics)
	}
	assignments := snapshotModelRouteAssignments()
	if len(assignments.placeholders) != 1 || assignments.placeholders["models/previous"] != "MODEL_PLACEHOLDER_M9" ||
		len(assignments.slugs) != 1 || assignments.slugs["models/previous"] != "custom-previous" {
		t.Fatalf("image-directory-only response replaced active assignments: %+v", assignments)
	}
}

// A familiar key with an unknown value type is also an unknown picker
// protocol. In particular, do not overwrite a map-valued field with a guessed
// legacy sorter array just because it happens to be named agentModelSorts.
func TestHandleFetchAvailableModelsFailsClosedForUnknownPickerFieldType(t *testing.T) {
	restore := replaceModelRouteAssignmentsForTest(modelRouteAssignments{
		placeholders: map[string]string{"models/previous": "MODEL_PLACEHOLDER_M9"},
		slugs:        map[string]string{"models/previous": "custom-previous"},
	})
	t.Cleanup(restore)

	stateDir := t.TempDir()
	storage.Init(stateDir)
	InitTrace(stateDir)
	if err := storage.SaveModels([]storage.CustomModel{{
		Name: "models/custom-test", DisplayName: "Custom test", ExternalModelName: "custom-test",
	}}); err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"models":{"native":{"model":"MODEL_GEMINI_NATIVE"}},"agentModelSorts":{"differentProto":true}}`)
	recorder := httptest.NewRecorder()
	handleFetchAvailableModelsWithClient(
		recorder,
		httptest.NewRequest(http.MethodPost, "/v1internal:fetchAvailableModels", nil),
		&http.Client{Transport: unknownModelShapeRoundTripper(payload)},
	)
	if recorder.Code != http.StatusOK || !bytes.Equal(recorder.Body.Bytes(), payload) {
		t.Fatalf("unknown picker field changed native response: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if diagnostics := GetDiagnostics(); !strings.Contains(diagnostics.LastError, "模型选择索引") {
		t.Fatalf("unknown picker field was not diagnosed: %+v", diagnostics)
	}
	assignments := snapshotModelRouteAssignments()
	if len(assignments.placeholders) != 1 || assignments.placeholders["models/previous"] != "MODEL_PLACEHOLDER_M9" ||
		len(assignments.slugs) != 1 || assignments.slugs["models/previous"] != "custom-previous" {
		t.Fatalf("unknown picker field replaced active assignments: %+v", assignments)
	}
}

// Validation is deliberately fail-closed. For example, if an older IDE only
// exposes 151 valid placeholder enum values but the user has more enabled
// models, forwarding a partially altered map would give the Language Server
// mismatched picker data. The native response must remain usable instead.
func TestHandleFetchAvailableModelsForwardsOriginalWhenInjectionValidationFails(t *testing.T) {
	stateDir := t.TempDir()
	storage.Init(stateDir)
	InitTrace(stateDir)
	models := make([]storage.CustomModel, 0, modelPlaceholderCount+1)
	for index := 0; index <= modelPlaceholderCount; index++ {
		name := fmt.Sprintf("models/overflow-%d", index)
		models = append(models, storage.CustomModel{Name: name, DisplayName: name, ExternalModelName: name})
	}
	if err := storage.SaveModels(models); err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"models":{},"agentModelSorts":[{"groups":[{"modelIds":[]}]}]}`)
	recorder := httptest.NewRecorder()
	handleFetchAvailableModelsWithClient(
		recorder,
		httptest.NewRequest(http.MethodPost, "/v1internal:fetchAvailableModels", nil),
		newModelFetchTestClient(modelFetchRoundTripper(func(request *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(bytes.NewReader(payload)), Request: request}, nil
		})),
	)
	if recorder.Code != http.StatusOK || !bytes.Equal(recorder.Body.Bytes(), payload) {
		t.Fatalf("failed injection changed native response: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if diagnostics := GetDiagnostics(); !strings.Contains(diagnostics.LastError, "只为") {
		t.Fatalf("missing validation diagnostic: %+v", diagnostics)
	}
}

// A valid picker can remain open while a later fetchAvailableModels response
// uses an unsupported shape or runs out of enum placeholders.  The failed
// probe must preserve its existing slug/placeholder mapping so an already
// selected custom model cannot be routed to a different upstream model.
func TestHandleFetchAvailableModelsValidationFailureKeepsExistingRouteAssignments(t *testing.T) {
	restore := replaceModelRouteAssignmentsForTest(modelRouteAssignments{
		placeholders: map[string]string{"models/previous": "MODEL_PLACEHOLDER_M9"},
		slugs:        map[string]string{"models/previous": "custom-previous"},
	})
	t.Cleanup(restore)

	stateDir := t.TempDir()
	storage.Init(stateDir)
	InitTrace(stateDir)
	models := make([]storage.CustomModel, 0, modelPlaceholderCount+1)
	for index := 0; index <= modelPlaceholderCount; index++ {
		name := fmt.Sprintf("models/overflow-%d", index)
		models = append(models, storage.CustomModel{Name: name, DisplayName: name, ExternalModelName: name})
	}
	if err := storage.SaveModels(models); err != nil {
		t.Fatal(err)
	}

	payload := []byte(`{"models":{},"agentModelSorts":[{"groups":[{"modelIds":[]}]}]}`)
	recorder := httptest.NewRecorder()
	handleFetchAvailableModelsWithClient(
		recorder,
		httptest.NewRequest(http.MethodPost, "/v1internal:fetchAvailableModels", nil),
		newModelFetchTestClient(modelFetchRoundTripper(func(request *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(bytes.NewReader(payload)), Request: request}, nil
		})),
	)
	if recorder.Code != http.StatusOK || !bytes.Equal(recorder.Body.Bytes(), payload) {
		t.Fatalf("failed injection changed native response: status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	assignments := snapshotModelRouteAssignments()
	if len(assignments.placeholders) != 1 || assignments.placeholders["models/previous"] != "MODEL_PLACEHOLDER_M9" ||
		len(assignments.slugs) != 1 || assignments.slugs["models/previous"] != "custom-previous" {
		t.Fatalf("failed injection replaced active routing assignments: %+v", assignments)
	}
}

// A new Antigravity / Language Server build can legitimately expand its
// native enum set. If it claims an enum already shown in an open custom-model
// picker, silently changing that custom model's enum would route a stale
// selection to the wrong upstream. The full response must fail closed and the
// active route must stay untouched.
func TestHandleFetchAvailableModelsFailsClosedWhenOfficialModelClaimsExistingPlaceholder(t *testing.T) {
	restore := replaceModelRouteAssignmentsForTest(modelRouteAssignments{
		placeholders: map[string]string{"models/current": "MODEL_PLACEHOLDER_M9"},
		slugs:        map[string]string{"models/current": "custom-current"},
	})
	t.Cleanup(restore)

	stateDir := t.TempDir()
	storage.Init(stateDir)
	InitTrace(stateDir)
	if err := storage.SaveModels([]storage.CustomModel{{
		Name: "models/current", DisplayName: "Current", ExternalModelName: "current",
	}}); err != nil {
		t.Fatal(err)
	}

	payload := []byte(`{"models":{"official":{"model":"MODEL_PLACEHOLDER_M9"}},"agentModelSorts":[{"groups":[{"modelIds":["official"]}]}]}`)
	recorder := httptest.NewRecorder()
	handleFetchAvailableModelsWithClient(
		recorder,
		httptest.NewRequest(http.MethodPost, "/v1internal:fetchAvailableModels", nil),
		newModelFetchTestClient(modelFetchRoundTripper(func(request *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(bytes.NewReader(payload)),
				Request:    request,
			}, nil
		})),
	)

	if recorder.Code != http.StatusOK || !bytes.Equal(recorder.Body.Bytes(), payload) {
		t.Fatalf("placeholder collision changed native response: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	assignments := snapshotModelRouteAssignments()
	if len(assignments.placeholders) != 1 || assignments.placeholders["models/current"] != "MODEL_PLACEHOLDER_M9" ||
		len(assignments.slugs) != 1 || assignments.slugs["models/current"] != "custom-current" {
		t.Fatalf("placeholder collision replaced active routing assignments: %+v", assignments)
	}
	if diagnostics := GetDiagnostics(); !strings.Contains(diagnostics.LastError, "路由一致性") {
		t.Fatalf("missing route-consistency diagnostic: %+v", diagnostics)
	}
}

// A second, valid model-list response may come from another detected
// Antigravity installation. It must retain route identifiers already visible
// in an earlier picker instead of reassigning the same custom model.
func TestInjectCustomModelsKeepsExistingAssignmentsAcrossValidResponses(t *testing.T) {
	model := storage.CustomModel{Name: "models/custom-test", DisplayName: "Custom test", ExternalModelName: "custom-test"}
	restore := replaceModelRouteAssignmentsForTest(modelRouteAssignments{
		placeholders: map[string]string{model.Name: "MODEL_PLACEHOLDER_M9"},
		slugs:        map[string]string{model.Name: "custom-stable"},
	})
	t.Cleanup(restore)

	parsed := map[string]any{
		"models": map[string]any{"native": map[string]any{"model": "MODEL_GEMINI_NATIVE"}},
		"agentModelSorts": []any{map[string]any{
			"groups": []any{map[string]any{"modelIds": []any{"native"}}},
		}},
	}
	summary := injectCustomModels(parsed, []storage.CustomModel{model})
	if err := validateModelInjection(parsed, []storage.CustomModel{model}, summary); err != nil {
		t.Fatalf("valid response rejected: %v", err)
	}
	if got := summary.assignments.slugs[model.Name]; got != "custom-stable" {
		t.Fatalf("slug = %q, want existing custom-stable", got)
	}
	if got := summary.assignments.placeholders[model.Name]; got != "MODEL_PLACEHOLDER_M9" {
		t.Fatalf("placeholder = %q, want existing MODEL_PLACEHOLDER_M9", got)
	}
	commitModelRouteAssignments(summary.assignments)
	assignments := snapshotModelRouteAssignments()
	if assignments.slugs[model.Name] != "custom-stable" || assignments.placeholders[model.Name] != "MODEL_PLACEHOLDER_M9" {
		t.Fatalf("successful response changed stable assignments: %+v", assignments)
	}
}

// A newer native response which claims an already visible placeholder cannot
// safely be remapped while another picker may still send that enum. The proxy
// must reject injection and preserve the last proven mapping.
func TestInjectCustomModelsFailsClosedOnExistingPlaceholderCollision(t *testing.T) {
	model := storage.CustomModel{Name: "models/custom-test", DisplayName: "Custom test", ExternalModelName: "custom-test"}
	restore := replaceModelRouteAssignmentsForTest(modelRouteAssignments{
		placeholders: map[string]string{model.Name: "MODEL_PLACEHOLDER_M9"},
		slugs:        map[string]string{model.Name: "custom-stable"},
	})
	t.Cleanup(restore)

	parsed := map[string]any{
		"models": map[string]any{"native": map[string]any{"model": "MODEL_PLACEHOLDER_M9"}},
		"agentModelSorts": []any{map[string]any{
			"groups": []any{map[string]any{"modelIds": []any{"native"}}},
		}},
	}
	summary := injectCustomModels(parsed, []storage.CustomModel{model})
	if summary.assignmentErr == nil {
		t.Fatalf("existing placeholder collision was not diagnosed: %+v", summary)
	}
	if err := validateModelInjection(parsed, []storage.CustomModel{model}, summary); err == nil || !strings.Contains(err.Error(), "占位符冲突") {
		t.Fatalf("collision must fail closed, got %v", err)
	}
	assignments := snapshotModelRouteAssignments()
	if assignments.slugs[model.Name] != "custom-stable" || assignments.placeholders[model.Name] != "MODEL_PLACEHOLDER_M9" {
		t.Fatalf("collision changed active assignments: %+v", assignments)
	}
}
