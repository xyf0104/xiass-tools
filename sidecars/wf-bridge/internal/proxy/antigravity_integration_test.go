package proxy

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"antigravity-wf-assistant/internal/storage"
)

func setupAntigravityIntegrationModel(t *testing.T, model storage.CustomModel) {
	t.Helper()
	dir := t.TempDir()
	storage.Init(dir)
	InitTrace(dir)
	if err := storage.SaveModels([]storage.CustomModel{model}); err != nil {
		t.Fatalf("save custom model: %v", err)
	}
}

func setupAntigravityIntegrationModels(t *testing.T, models ...storage.CustomModel) {
	t.Helper()
	dir := t.TempDir()
	storage.Init(dir)
	InitTrace(dir)
	if err := storage.SaveModels(models); err != nil {
		t.Fatalf("save custom models: %v", err)
	}
}

func antigravityRequest(modelID, requestID string, request map[string]any) *http.Request {
	return antigravityRequestAtPath("/v1internal:streamGenerateContent", modelID, requestID, request)
}

func antigravityRequestAtPath(path, modelID, requestID string, request map[string]any) *http.Request {
	payload, _ := json.Marshal(map[string]any{
		"model": modelID, "requestId": requestID, "request": request,
	})
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func textTurn(text string) map[string]any {
	return map[string]any{"contents": []any{map[string]any{
		"role": "user", "parts": []any{map[string]any{"text": text}},
	}}}
}

func writeOpenAIChatStream(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = io.WriteString(w, `data: {"id":"chat-integration","model":"gpt-integration","choices":[{"delta":{"content":"`+text+`"},"finish_reason":null}]}`+"\n\n")
	_, _ = io.WriteString(w, `data: {"id":"chat-integration","model":"gpt-integration","choices":[{"delta":{},"finish_reason":"stop"}]}`+"\n\n")
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
}

func TestAntigravityOpenAITextUsesChatCompletions(t *testing.T) {
	var received map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("OpenAI text path = %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("missing OpenAI authorization: %q", r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatal(err)
		}
		writeOpenAIChatStream(w, "text-ok")
	}))
	defer upstream.Close()

	model := storage.CustomModel{Name: "models/integration-gpt", Provider: "openai", APIURL: upstream.URL, APIKey: "test-key", ExternalModelName: "gpt-integration", APIStyle: "auto"}
	setupAntigravityIntegrationModel(t, model)
	recorder := httptest.NewRecorder()
	handleRequest(recorder, antigravityRequest(model.Name, "text-request", textTurn("hello")))

	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "text-ok") {
		t.Fatalf("downstream text response = %d %s", recorder.Code, recorder.Body.String())
	}
	messages, _ := received["messages"].([]any)
	if len(messages) != 1 || messages[0].(map[string]any)["content"] != "hello" {
		t.Fatalf("Antigravity text was not converted once into Chat Completions: %#v", received)
	}
}

func TestAntigravityOpenAIImageInputUsesChatCompletions(t *testing.T) {
	var received map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("OpenAI image input path = %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatal(err)
		}
		writeOpenAIChatStream(w, "vision-ok")
	}))
	defer upstream.Close()

	model := storage.CustomModel{Name: "models/integration-image", Provider: "openai", APIURL: upstream.URL, APIKey: "test-key", ExternalModelName: "gpt-integration", APIStyle: "auto"}
	setupAntigravityIntegrationModel(t, model)
	request := textTurn("describe image")
	request["contents"].([]any)[0].(map[string]any)["parts"] = []any{
		map[string]any{"text": "describe image"},
		map[string]any{"inlineData": map[string]any{"mimeType": "image/png", "data": "aGVsbG8="}},
	}
	recorder := httptest.NewRecorder()
	handleRequest(recorder, antigravityRequest(model.Name, "image-input-request", request))

	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "vision-ok") {
		t.Fatalf("downstream image response = %d %s", recorder.Code, recorder.Body.String())
	}
	messages, _ := received["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("Chat messages = %#v", received)
	}
	blocks, _ := messages[0].(map[string]any)["content"].([]any)
	imageURL, _ := blocks[1].(map[string]any)["image_url"].(map[string]any)
	if len(blocks) != 2 || blocks[1].(map[string]any)["type"] != "image_url" || imageURL["url"] != "data:image/png;base64,aGVsbG8=" {
		t.Fatalf("image was not preserved as a Chat image_url: %#v", messages)
	}
}

func TestAntigravityExplicitResponsesImageGenerationReturnsImage(t *testing.T) {
	var received map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("OpenAI image generation path = %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"type":"response.output_item.done","item":{"type":"image_generation_call","result":"aW1hZ2U="}}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"id":"resp-image-output","model":"gpt-integration"}}`+"\n\n")
	}))
	defer upstream.Close()

	model := storage.CustomModel{Name: "models/integration-image-gen", Provider: "openai", APIURL: upstream.URL, APIKey: "test-key", ExternalModelName: "gpt-integration", APIStyle: "responses"}
	setupAntigravityIntegrationModel(t, model)
	request := textTurn("generate an image")
	request["generationConfig"] = map[string]any{"responseModalities": []any{"TEXT", "IMAGE"}}
	recorder := httptest.NewRecorder()
	handleRequest(recorder, antigravityRequest(model.Name, "image-generation-request", request))

	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "inlineData") || !strings.Contains(recorder.Body.String(), "aW1hZ2U=") {
		t.Fatalf("image generation result was not converted for Antigravity: %d %s", recorder.Code, recorder.Body.String())
	}
	tools, _ := received["tools"].([]any)
	foundImageGeneration := false
	for _, raw := range tools {
		if raw.(map[string]any)["type"] == responseImageGenerationTool {
			foundImageGeneration = true
		}
	}
	if !foundImageGeneration {
		t.Fatalf("Responses request did not include image_generation: %#v", received)
	}
}

func TestAntigravityOpenAIImageGenerationUsesSameUpstreamImageModel(t *testing.T) {
	var received map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/images/generations" {
			t.Fatalf("dedicated image route = %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer image-key" {
			t.Fatalf("dedicated image route used the wrong supplier credential: %q", r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"b64_json":"iVBORw0KGgo=","mime_type":"image/png"}]}`)
	}))
	defer upstream.Close()

	textModel := storage.CustomModel{
		Name: "models/integration-sol", Provider: "openai", APIURL: upstream.URL,
		APIKey: "test-key", ExternalModelName: "gpt-5.6-sol", APIStyle: "auto",
	}
	imageModel := storage.CustomModel{
		Name: "models/integration-image-2", Provider: "openai", APIURL: upstream.URL,
		APIKey: "image-key", ExternalModelName: "gpt-image-2", APIStyle: "auto",
	}
	setupAntigravityIntegrationModels(t, textModel, imageModel)
	request := textTurn("draw a tiny orange astronaut")
	request["generationConfig"] = map[string]any{"responseModalities": []any{"TEXT", "IMAGE"}}
	recorder := httptest.NewRecorder()
	handleRequest(recorder, antigravityRequest(textModel.Name, "dedicated-image-request", request))

	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "inlineData") || !strings.Contains(recorder.Body.String(), "iVBORw0KGgo=") {
		t.Fatalf("dedicated image response = %d %s", recorder.Code, recorder.Body.String())
	}
	if received["model"] != "gpt-image-2" || received["prompt"] != "draw a tiny orange astronaut" {
		t.Fatalf("dedicated image payload = %#v", received)
	}
}

func TestAntigravityOpenAIImageGenerationFallsBackToSeparateSupplier(t *testing.T) {
	var received map[string]any
	imageUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/images/generations" {
			t.Fatalf("cross-supplier image route = %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer image-key" {
			t.Fatalf("cross-supplier image route used the wrong credential: %q", r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"b64_json":"Y3Jvc3Mtc3VwcGxpZXItaW1hZ2U=","mime_type":"image/png"}]}`)
	}))
	defer imageUpstream.Close()
	chatUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("image generation must not be sent to the chat-only supplier: %s", r.URL.Path)
	}))
	defer chatUpstream.Close()

	textModel := storage.CustomModel{
		Name: "models/cross-sol", Provider: "openai", APIURL: chatUpstream.URL,
		APIKey: "chat-key", ExternalModelName: "gpt-5.6-sol", APIStyle: "auto",
	}
	imageModel := storage.CustomModel{
		Name: "models/cross-image-2", Provider: "openai", APIURL: imageUpstream.URL,
		APIKey: "image-key", ExternalModelName: "gpt-image-2", APIStyle: "auto",
	}
	setupAntigravityIntegrationModels(t, textModel, imageModel)
	request := textTurn("draw a rainbow spacecraft")
	request["generationConfig"] = map[string]any{"responseModalities": []any{"TEXT", "IMAGE"}}
	recorder := httptest.NewRecorder()
	handleRequest(recorder, antigravityRequest(textModel.Name, "cross-supplier-image-request", request))

	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "inlineData") || !strings.Contains(recorder.Body.String(), "Y3Jvc3Mtc3VwcGxpZXItaW1hZ2U=") {
		t.Fatalf("cross-supplier image response = %d %s", recorder.Code, recorder.Body.String())
	}
	if received["model"] != "gpt-image-2" || received["prompt"] != "draw a rainbow spacecraft" {
		t.Fatalf("cross-supplier image payload = %#v", received)
	}
}

func TestNativeImageGenerationUsesLastCustomOpenAIModelForSameTrajectory(t *testing.T) {
	resetImageGenerationSourcesForTest()
	t.Cleanup(resetImageGenerationSourcesForTest)

	var imageRequest map[string]any
	var chatCalls, imageCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/chat/completions":
			chatCalls.Add(1)
			writeOpenAIChatStream(w, "planner-ok")
		case "/v1/images/generations":
			imageCalls.Add(1)
			if r.Header.Get("Authorization") != "Bearer image-key" {
				t.Fatalf("native image route used the wrong image supplier credential: %q", r.Header.Get("Authorization"))
			}
			if err := json.NewDecoder(r.Body).Decode(&imageRequest); err != nil {
				t.Fatal(err)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":[{"b64_json":"aW1hZ2UtZnJvbS10cmFqZWN0b3J5","mime_type":"image/png"}]}`)
		default:
			t.Fatalf("unexpected upstream path %s", r.URL.Path)
		}
	}))
	defer upstream.Close()

	sol := storage.CustomModel{
		Name: "models/sol", Provider: "openai", APIURL: upstream.URL,
		APIKey: "sol-key", ExternalModelName: "gpt-5.6-sol", APIStyle: "auto",
	}
	image := storage.CustomModel{
		Name: "models/image-2", Provider: "openai", APIURL: upstream.URL,
		APIKey: "image-key", ExternalModelName: "gpt-image-2", APIStyle: "auto",
	}
	setupAntigravityIntegrationModels(t, sol, image)

	trajectoryID := "41701638-bcd6-4314-ad62-5f3ecfc7e9b9"
	first := httptest.NewRecorder()
	handleRequest(first, antigravityRequest(sol.Name, "agent/agent-id/1785736368613/"+trajectoryID+"/20", textTurn("plan an illustration")))
	if first.Code != http.StatusOK || chatCalls.Load() != 1 {
		t.Fatalf("source agent request = %d, chat calls = %d, body = %s", first.Code, chatCalls.Load(), first.Body.String())
	}

	second := httptest.NewRecorder()
	handleRequest(second, antigravityRequestAtPath("/v1internal:generateContent", "gemini-3.1-flash-image", "image_gen/1785736374865/"+trajectoryID+"/21", textTurn("draw a bright orange paper airplane")))
	if second.Code != http.StatusOK || !strings.Contains(second.Body.String(), "aW1hZ2UtZnJvbS10cmFqZWN0b3J5") {
		t.Fatalf("native image response = %d %s", second.Code, second.Body.String())
	}
	if !strings.HasPrefix(second.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("native image content type = %q, want unary JSON", second.Header().Get("Content-Type"))
	}
	var nativeEnvelope map[string]any
	if err := json.Unmarshal(second.Body.Bytes(), &nativeEnvelope); err != nil {
		t.Fatalf("native image response is not JSON: %v", err)
	}
	response, _ := nativeEnvelope["response"].(map[string]any)
	candidates, _ := response["candidates"].([]any)
	if len(candidates) != 1 {
		t.Fatalf("native image candidates = %#v", nativeEnvelope)
	}
	content, _ := candidates[0].(map[string]any)["content"].(map[string]any)
	parts, _ := content["parts"].([]any)
	if len(parts) != 1 {
		t.Fatalf("native image parts = %#v", nativeEnvelope)
	}
	inline, _ := parts[0].(map[string]any)["inlineData"].(map[string]any)
	if inline["data"] != "aW1hZ2UtZnJvbS10cmFqZWN0b3J5" {
		t.Fatalf("native image inline data = %#v", inline)
	}
	if imageCalls.Load() != 1 || chatCalls.Load() != 1 {
		t.Fatalf("native image route calls: chat=%d image=%d", chatCalls.Load(), imageCalls.Load())
	}
	if imageRequest["model"] != "gpt-image-2" || imageRequest["prompt"] != "draw a bright orange paper airplane" {
		t.Fatalf("native image request = %#v", imageRequest)
	}
}

func TestNativeImageGenerationFallsBackAcrossSuppliersForSameTrajectory(t *testing.T) {
	resetImageGenerationSourcesForTest()
	t.Cleanup(resetImageGenerationSourcesForTest)

	var chatCalls, imageCalls atomic.Int32
	chatUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chatCalls.Add(1)
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer chat-key" {
			t.Fatalf("chat request used wrong route or credential: %s %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		writeOpenAIChatStream(w, "planner-ok")
	}))
	defer chatUpstream.Close()
	imageUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		imageCalls.Add(1)
		if r.URL.Path != "/v1/images/generations" || r.Header.Get("Authorization") != "Bearer image-key" {
			t.Fatalf("image request used wrong route or credential: %s %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"b64_json":"bmF0aXZlLWNyb3NzLXN1cHBsaWVy","mime_type":"image/png"}]}`)
	}))
	defer imageUpstream.Close()

	sol := storage.CustomModel{
		Name: "models/cross-native-sol", Provider: "openai", APIURL: chatUpstream.URL,
		APIKey: "chat-key", ExternalModelName: "gpt-5.6-sol", APIStyle: "auto",
	}
	image := storage.CustomModel{
		Name: "models/cross-native-image", Provider: "openai", APIURL: imageUpstream.URL,
		APIKey: "image-key", ExternalModelName: "gpt-image-2", APIStyle: "auto",
	}
	setupAntigravityIntegrationModels(t, sol, image)

	trajectoryID := "3cb41e69-dd17-4da8-a8eb-10d669737608"
	first := httptest.NewRecorder()
	handleRequest(first, antigravityRequest(sol.Name, "agent/agent-id/1785736368613/"+trajectoryID+"/20", textTurn("plan an illustration")))
	if first.Code != http.StatusOK || chatCalls.Load() != 1 {
		t.Fatalf("cross-supplier source turn = %d, chat calls = %d, body = %s", first.Code, chatCalls.Load(), first.Body.String())
	}

	second := httptest.NewRecorder()
	handleRequest(second, antigravityRequestAtPath(
		"/v1internal:generateContent",
		"gemini-3.1-flash-image",
		"image_gen/1785736374865/"+trajectoryID+"/21",
		textTurn("draw a blue moon"),
	))
	if second.Code != http.StatusOK || !strings.Contains(second.Body.String(), "bmF0aXZlLWNyb3NzLXN1cHBsaWVy") {
		t.Fatalf("cross-supplier native image response = %d %s", second.Code, second.Body.String())
	}
	if chatCalls.Load() != 1 || imageCalls.Load() != 1 {
		t.Fatalf("cross-supplier native calls: chat=%d image=%d", chatCalls.Load(), imageCalls.Load())
	}
}

func TestNativeImageGenerationKeepingCustomModelIDUsesImagesEndpoint(t *testing.T) {
	resetImageGenerationSourcesForTest()
	t.Cleanup(resetImageGenerationSourcesForTest)

	var imageRequest map[string]any
	var chatCalls, imageCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/chat/completions":
			chatCalls.Add(1)
			writeOpenAIChatStream(w, "wrong-route")
		case "/v1/images/generations":
			imageCalls.Add(1)
			if err := json.NewDecoder(r.Body).Decode(&imageRequest); err != nil {
				t.Fatal(err)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":[{"b64_json":"aW1hZ2UtZnJvbS1zdGFuZGFsb25l","mime_type":"image/png"}]}`)
		default:
			t.Fatalf("unexpected upstream path %s", r.URL.Path)
		}
	}))
	defer upstream.Close()

	sol := storage.CustomModel{
		Name: "models/sol", Provider: "openai", APIURL: upstream.URL,
		APIKey: "sol-key", ExternalModelName: "gpt-5.6-sol", APIStyle: "auto",
	}
	image := storage.CustomModel{
		Name: "models/image-2", Provider: "openai", APIURL: upstream.URL,
		APIKey: "unused-image-key", ExternalModelName: "gpt-image-2", APIStyle: "auto",
	}
	setupAntigravityIntegrationModels(t, sol, image)

	trajectoryID := "5031ac44-20ec-4169-92d8-3e2043445369"
	// Antigravity 2.6 can keep a custom slug in the image tool request even
	// though the selected chat turn is carried by the preceding agent request.
	// Record that authoritative turn without creating an unrelated chat call in
	// this endpoint-specific regression.
	rememberImageGenerationSource("agent/agent-id/1786382105000/"+trajectoryID+"/13", &sol)
	requestID := "image_gen/1786382105504/" + trajectoryID + "/14"
	recorder := httptest.NewRecorder()
	handleRequest(recorder, antigravityRequestAtPath(
		"/v1internal:generateContent",
		sol.Name,
		requestID,
		textTurn("draw a bright orange paper airplane"),
	))

	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "aW1hZ2UtZnJvbS1zdGFuZGFsb25l") {
		t.Fatalf("native custom-id image response = %d %s", recorder.Code, recorder.Body.String())
	}
	if !strings.HasPrefix(recorder.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("native custom-id content type = %q, want unary JSON", recorder.Header().Get("Content-Type"))
	}
	if chatCalls.Load() != 0 || imageCalls.Load() != 1 {
		t.Fatalf("native custom-id route calls: chat=%d image=%d", chatCalls.Load(), imageCalls.Load())
	}
	if imageRequest["model"] != "gpt-image-2" || imageRequest["prompt"] != "draw a bright orange paper airplane" {
		t.Fatalf("native custom-id image request = %#v", imageRequest)
	}
}

// TestLiveNativeImageGeneration exercises the same unary image_gen request
// emitted by Antigravity against an explicitly supplied local configuration.
// It is opt-in because it contacts a real upstream and may consume image
// generation quota. Credentials are loaded from the config file and are never
// printed or placed on the command line.
func TestLiveNativeImageGeneration(t *testing.T) {
	configDir := strings.TrimSpace(os.Getenv("WF_REAL_CONFIG_DIR"))
	if configDir == "" {
		t.Skip("WF_REAL_CONFIG_DIR is not set")
	}
	storage.Init(configDir)
	InitTrace(t.TempDir())
	models, err := storage.LoadEnabledModels()
	if err != nil {
		t.Fatalf("load enabled models: %v", err)
	}
	var textModel *storage.CustomModel
	for index := range models {
		candidate := models[index]
		if isOpenAICompatibleImageProvider(candidate.Provider) && !isDirectImageModelName(candidate.ExternalModelName) && directOpenAIImageModel(&candidate) != nil {
			textModel = &candidate
			break
		}
	}
	if textModel == nil {
		t.Fatal("no enabled text model with an available custom image model")
	}

	resetImageGenerationSourcesForTest()
	t.Cleanup(resetImageGenerationSourcesForTest)
	trajectoryID := "live-antigravity-image-test"
	rememberImageGenerationSource("agent/live-test/1786382105000/"+trajectoryID+"/13", textModel)
	requestID := "image_gen/1786382105504/" + trajectoryID + "/14"
	recorder := httptest.NewRecorder()
	handleRequest(recorder, antigravityRequestAtPath(
		"/v1internal:generateContent",
		textModel.Name,
		requestID,
		textTurn("Draw one small orange circle centered on a plain white background."),
	))
	if recorder.Code != http.StatusOK {
		t.Fatalf("live image request failed: HTTP %d: %s", recorder.Code, strings.TrimSpace(recorder.Body.String()))
	}
	if !strings.HasPrefix(recorder.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("live image content type = %q", recorder.Header().Get("Content-Type"))
	}
	var envelope map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("live image response is not JSON: %v", err)
	}
	response, _ := envelope["response"].(map[string]any)
	candidates, _ := response["candidates"].([]any)
	if len(candidates) != 1 {
		t.Fatal("live image response has no candidate")
	}
	content, _ := candidates[0].(map[string]any)["content"].(map[string]any)
	parts, _ := content["parts"].([]any)
	if len(parts) != 1 {
		t.Fatal("live image response has no media part")
	}
	inline, _ := parts[0].(map[string]any)["inlineData"].(map[string]any)
	encoded, _ := inline["data"].(string)
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(decoded) == 0 {
		t.Fatalf("live image response contains invalid Base64: %v", err)
	}
	detected := http.DetectContentType(decoded)
	if !strings.HasPrefix(detected, "image/") {
		t.Fatalf("live image payload is %q, not an image", detected)
	}
	t.Logf("live Antigravity image route passed: model=%s bytes=%d mime=%s", textModel.ExternalModelName, len(decoded), detected)
}

func TestLiveOpenAIChatTextAndVision(t *testing.T) {
	configDir := strings.TrimSpace(os.Getenv("WF_REAL_CONFIG_DIR"))
	if configDir == "" {
		t.Skip("WF_REAL_CONFIG_DIR is not set")
	}
	storage.Init(configDir)
	InitTrace(t.TempDir())
	models, err := storage.LoadEnabledModels()
	if err != nil {
		t.Fatalf("load enabled models: %v", err)
	}
	var model *storage.CustomModel
	for index := range models {
		candidate := models[index]
		style := strings.ToLower(strings.TrimSpace(candidate.APIStyle))
		if isOpenAICompatibleImageProvider(candidate.Provider) && !isDirectImageModelName(candidate.ExternalModelName) && style != "responses" && style != "messages" {
			model = &candidate
			break
		}
	}
	if model == nil {
		t.Skip("no enabled OpenAI Chat model")
	}

	textRecorder := httptest.NewRecorder()
	handleRequest(textRecorder, antigravityRequest(model.Name, "live-chat-text", textTurn("Reply with exactly WF_OK.")))
	if textRecorder.Code != http.StatusOK || !strings.Contains(textRecorder.Body.String(), `"text":`) || !strings.Contains(textRecorder.Body.String(), `"finishReason":"STOP"`) {
		t.Fatalf("live Chat text request failed: HTTP %d: %s", textRecorder.Code, strings.TrimSpace(textRecorder.Body.String()))
	}

	vision := textTurn("Reply briefly after confirming that the attached image is visible.")
	fixture := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			fixture.Set(x, y, color.NRGBA{R: 244, G: 145, B: 66, A: 255})
		}
	}
	var fixturePNG bytes.Buffer
	if err := png.Encode(&fixturePNG, fixture); err != nil {
		t.Fatalf("encode live vision fixture: %v", err)
	}
	vision["contents"].([]any)[0].(map[string]any)["parts"] = []any{
		map[string]any{"text": "Reply briefly after confirming that the attached image is visible."},
		map[string]any{"inlineData": map[string]any{
			"mimeType": "image/png",
			"data":     base64.StdEncoding.EncodeToString(fixturePNG.Bytes()),
		}},
	}
	visionRecorder := httptest.NewRecorder()
	handleRequest(visionRecorder, antigravityRequest(model.Name, "live-chat-vision", vision))
	if visionRecorder.Code != http.StatusOK || !strings.Contains(visionRecorder.Body.String(), `"text":`) || !strings.Contains(visionRecorder.Body.String(), `"finishReason":"STOP"`) {
		t.Fatalf("live Chat vision request failed: HTTP %d: %s", visionRecorder.Code, strings.TrimSpace(visionRecorder.Body.String()))
	}
	t.Logf("live Antigravity Chat text and vision passed: model=%s", model.ExternalModelName)
}

func TestNativeAgentSwitchClearsRememberedImageSourceForItsTrajectory(t *testing.T) {
	resetImageGenerationSourcesForTest()
	t.Cleanup(resetImageGenerationSourcesForTest)

	model := storage.CustomModel{
		Name: "models/gpt-image-source", Provider: "openai", APIURL: "https://example.invalid",
		APIKey: "test-key", ExternalModelName: "gpt-image-2", APIStyle: "auto",
	}
	setupAntigravityIntegrationModel(t, model)

	trajectoryA := "0f3d1f6f-6caa-4cdd-a7bc-957c40358148"
	trajectoryB := "c1f5fa72-7fa8-4d5d-ac51-d5f9b13398d1"
	customAgentA := "agent/agent-id/1785736368613/" + trajectoryA + "/20"
	customAgentB := "agent/agent-id/1785736368613/" + trajectoryB + "/20"
	nativeImageA := "image_gen/1785736374865/" + trajectoryA + "/21"
	nativeImageB := "image_gen/1785736374865/" + trajectoryB + "/21"
	nativeGeminiA := "agent/agent-id/1785736380000/" + trajectoryA + "/22"

	selected, customMatched, nativeImageSource := resolveGenerationModel(model.Name, customAgentA)
	if selected == nil || !customMatched || nativeImageSource {
		t.Fatalf("custom source resolution = model:%#v custom:%t native:%t", selected, customMatched, nativeImageSource)
	}
	rememberImageGenerationSource(customAgentA, selected)
	rememberImageGenerationSource(customAgentB, selected)

	selected, customMatched, nativeImageSource = resolveGenerationModel("gemini-3.1-flash-image", nativeImageA)
	if selected == nil || customMatched || !nativeImageSource || selected.ExternalModelName != "gpt-image-2" {
		t.Fatalf("remembered custom image source was not available: model:%#v custom:%t native:%t", selected, customMatched, nativeImageSource)
	}

	selected, customMatched, nativeImageSource = resolveGenerationModel("gemini-3.6-flash", nativeGeminiA)
	if selected != nil || customMatched || nativeImageSource {
		t.Fatalf("native Gemini agent request should pass through and clear only its source: model:%#v custom:%t native:%t", selected, customMatched, nativeImageSource)
	}
	selected, customMatched, nativeImageSource = resolveGenerationModel("gemini-3.1-flash-image", nativeImageA)
	if selected != nil || customMatched || nativeImageSource {
		t.Fatalf("native image request incorrectly reused GPT after Gemini switch: model:%#v custom:%t native:%t", selected, customMatched, nativeImageSource)
	}
	selected, customMatched, nativeImageSource = resolveGenerationModel(model.Name, nativeImageA)
	if selected != nil || customMatched || nativeImageSource {
		t.Fatalf("stale custom image-tool slug overrode the current Gemini selection: model:%#v custom:%t native:%t", selected, customMatched, nativeImageSource)
	}

	selected, customMatched, nativeImageSource = resolveGenerationModel("gemini-3.1-flash-image", nativeImageB)
	if selected == nil || customMatched || !nativeImageSource || selected.ExternalModelName != "gpt-image-2" {
		t.Fatalf("separate trajectory was incorrectly cleared: model:%#v custom:%t native:%t", selected, customMatched, nativeImageSource)
	}
}

func TestGeminiImageGenerationRestoresNativeModelFromStaleCustomIndex(t *testing.T) {
	resetImageGenerationSourcesForTest()
	t.Cleanup(resetImageGenerationSourcesForTest)

	model := storage.CustomModel{
		Name: "models/gpt-text", Provider: "openai", APIURL: "https://example.invalid",
		APIKey: "test-key", ExternalModelName: "gpt-5.6-sol", APIStyle: "auto",
	}
	setupAntigravityIntegrationModel(t, model)
	restoreAssignments := replaceModelRouteAssignmentsForTest(modelRouteAssignments{
		placeholders:        map[string]string{},
		slugs:               map[string]string{},
		nativeImageModelIDs: []string{"gemini-3.1-flash-image"},
	})
	t.Cleanup(restoreAssignments)

	trajectoryID := "e04c09bc-2713-45e6-8e9f-d074f8b3e850"
	requestID := "image_gen/1786384733053/" + trajectoryID + "/8"
	req := map[string]any{
		"model": model.Name, "requestId": requestID,
		"request": textTurn("generate natively with Gemini"),
	}
	selected, customMatched, nativeImageSource := resolveGenerationModel(model.Name, requestID)
	if selected != nil || customMatched || nativeImageSource {
		t.Fatalf("stale image-tool slug must not select GPT without a custom agent source: %#v %t %t", selected, customMatched, nativeImageSource)
	}
	restoredModel, changed, err := restoreNativeImageGenerationRequestModel(req, model.Name, requestID)
	if err != nil || !changed || restoredModel != "gemini-3.1-flash-image" {
		t.Fatalf("native Gemini image route was not restored: model=%q changed=%t err=%v", restoredModel, changed, err)
	}
	if req["model"] != "gemini-3.1-flash-image" {
		t.Fatalf("outer model was not restored to Gemini: %#v", req)
	}
	request := req["request"].(map[string]any)
	parts := request["contents"].([]any)[0].(map[string]any)["parts"].([]any)
	if parts[0].(map[string]any)["text"] != "generate natively with Gemini" {
		t.Fatalf("Gemini route repair changed the user prompt: %#v", req)
	}
}

func TestSelectedImageModelUsesImagesEndpointWithoutGenerationConfig(t *testing.T) {
	var received map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/images/generations" {
			t.Fatalf("selected image model path = %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"b64_json":"c2VsZWN0ZWQtaW1hZ2U=","mime_type":"image/png"}]}`)
	}))
	defer upstream.Close()

	image := storage.CustomModel{
		Name: "models/gpt-image-2", Provider: "openai", APIURL: upstream.URL,
		APIKey: "image-key", ExternalModelName: "gpt-image-2", APIStyle: "auto",
	}
	setupAntigravityIntegrationModel(t, image)
	recorder := httptest.NewRecorder()
	handleRequest(recorder, antigravityRequest(image.Name, "selected-image-model", textTurn("draw an orange kite")))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "c2VsZWN0ZWQtaW1hZ2U=") {
		t.Fatalf("selected image model response = %d %s", recorder.Code, recorder.Body.String())
	}
	if received["model"] != "gpt-image-2" || received["prompt"] != "draw an orange kite" {
		t.Fatalf("selected image payload = %#v", received)
	}
}

func TestAntigravityClaudeTextAndImageUseMessages(t *testing.T) {
	var received map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Fatalf("Claude path = %s", r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "claude-key" || r.Header.Get("anthropic-version") == "" {
			t.Fatalf("Claude credentials were not applied: %#v", r.Header)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"type":"message_start","message":{"id":"msg-integration","model":"claude-integration"}}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"claude-ok"}}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"type":"message_stop"}`+"\n\n")
	}))
	defer upstream.Close()

	model := storage.CustomModel{Name: "models/integration-claude", Provider: "anthropic", APIURL: upstream.URL, APIKey: "claude-key", ExternalModelName: "claude-integration", APIStyle: "messages", AuthMode: "x_api_key"}
	setupAntigravityIntegrationModel(t, model)
	request := textTurn("describe image")
	request["contents"].([]any)[0].(map[string]any)["parts"] = []any{
		map[string]any{"text": "describe image"},
		map[string]any{"inlineData": map[string]any{"mimeType": "image/jpeg", "data": "aGVsbG8="}},
	}
	recorder := httptest.NewRecorder()
	handleRequest(recorder, antigravityRequest(model.Name, "claude-image-request", request))

	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "claude-ok") {
		t.Fatalf("downstream Claude response = %d %s", recorder.Code, recorder.Body.String())
	}
	messages, _ := received["messages"].([]any)
	blocks, _ := messages[0].(map[string]any)["content"].([]any)
	if len(blocks) != 2 || blocks[1].(map[string]any)["type"] != "image" {
		t.Fatalf("image was not preserved in Claude Messages payload: %#v", messages)
	}
}

func TestAntigravityClaudePDFUsesMessages(t *testing.T) {
	var received map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Fatalf("Claude PDF path = %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"type":"message_start","message":{"id":"msg-pdf","model":"claude-integration"}}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"pdf-ok"}}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"type":"message_stop"}`+"\n\n")
	}))
	defer upstream.Close()

	model := storage.CustomModel{Name: "models/integration-claude-pdf", Provider: "anthropic", APIURL: upstream.URL, APIKey: "claude-key", ExternalModelName: "claude-integration", APIStyle: "messages", AuthMode: "x_api_key"}
	setupAntigravityIntegrationModel(t, model)
	request := textTurn("summarize pdf")
	request["contents"].([]any)[0].(map[string]any)["parts"] = []any{
		map[string]any{"text": "summarize pdf"},
		map[string]any{"inlineData": map[string]any{"mimeType": "application/pdf", "data": "cGRm"}},
	}
	recorder := httptest.NewRecorder()
	handleRequest(recorder, antigravityRequest(model.Name, "claude-pdf-request", request))

	if recorder.Code != http.StatusOK || strings.Count(recorder.Body.String(), "pdf-ok") != 1 {
		t.Fatalf("downstream Claude PDF response = %d %s", recorder.Code, recorder.Body.String())
	}
	messages, _ := received["messages"].([]any)
	blocks, _ := messages[0].(map[string]any)["content"].([]any)
	if len(blocks) != 2 || blocks[1].(map[string]any)["type"] != "document" {
		t.Fatalf("PDF was not preserved in Claude Messages payload: %#v", messages)
	}
}

func TestAntigravityOverlappingDuplicateDoesNotCreateSecondUpstreamGeneration(t *testing.T) {
	var upstreamCalls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if upstreamCalls.Add(1) == 1 {
			close(started)
		}
		<-release
		writeOpenAIChatStream(w, "once")
	}))
	defer upstream.Close()

	model := storage.CustomModel{Name: "models/integration-dedupe", Provider: "openai", APIURL: upstream.URL, APIKey: "test-key", ExternalModelName: "gpt-integration", APIStyle: "chat_completions"}
	setupAntigravityIntegrationModel(t, model)
	request := textTurn("only one generation")
	firstRecorder := httptest.NewRecorder()
	firstDone := make(chan struct{})
	go func() {
		handleRequest(firstRecorder, antigravityRequest(model.Name, "attempt-one", request))
		close(firstDone)
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("first upstream generation did not start")
	}

	secondRecorder := httptest.NewRecorder()
	handleRequest(secondRecorder, antigravityRequest(model.Name, "attempt-two", request))
	if secondRecorder.Code != http.StatusConflict {
		t.Fatalf("overlapping request was not suppressed: %d %s", secondRecorder.Code, secondRecorder.Body.String())
	}
	if upstreamCalls.Load() != 1 {
		t.Fatalf("duplicate request reached upstream %d times", upstreamCalls.Load())
	}
	close(release)
	select {
	case <-firstDone:
	case <-time.After(5 * time.Second):
		t.Fatal("first upstream generation did not finish")
	}
	if strings.Count(firstRecorder.Body.String(), `"text":"once"`) != 1 {
		t.Fatalf("first generation emitted duplicate assistant text: %s", firstRecorder.Body.String())
	}
}
