package proxy

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"antigravity-wf-assistant/internal/storage"
)

func TestDirectOpenAIImageModelUsesOnlyEnabledModelsFromCurrentSupplier(t *testing.T) {
	storage.Init(t.TempDir())
	disabled := false
	enabled := true
	selected := storage.CustomModel{
		Name: "models/sol", Provider: "openai", APIURL: "https://api.example.test", APIKey: "sol-key", ExternalModelName: "gpt-5.6-sol",
	}
	if err := storage.SaveModels([]storage.CustomModel{
		selected,
		{Name: "models/disabled-image", Provider: "openai", APIURL: selected.APIURL, APIKey: selected.APIKey, ExternalModelName: "gpt-image-2", Enabled: &disabled},
		{Name: "models/other-upstream-image", Provider: "openai", APIURL: "https://other.example.test", APIKey: "other-key", ExternalModelName: "gpt-image-2", Enabled: &enabled},
		{Name: "models/same-upstream-image", Provider: "openai", APIURL: selected.APIURL, APIKey: selected.APIKey, ExternalModelName: "gpt-image-1", Enabled: &enabled},
	}); err != nil {
		t.Fatal(err)
	}

	image := directOpenAIImageModel(&selected)
	if image == nil || image.ExternalModelName != "gpt-image-1" {
		t.Fatalf("selected image model = %#v, want enabled same-upstream gpt-image-1", image)
	}
}

func TestDirectOpenAIImageModelPrefersExactGPTImage2WithinCurrentSupplier(t *testing.T) {
	storage.Init(t.TempDir())
	enabled := true
	selected := storage.CustomModel{
		Name: "models/sol", Provider: "openai", APIURL: "https://api.example.test", APIKey: "same-key", ExternalModelName: "gpt-5.6-sol",
	}
	if err := storage.SaveModels([]storage.CustomModel{
		selected,
		{Name: "models/current-image-one", Provider: "openai", APIURL: selected.APIURL, APIKey: selected.APIKey, ExternalModelName: "gpt-image-1", Enabled: &enabled},
		{Name: "models/current-image-two", Provider: "openai", APIURL: selected.APIURL, APIKey: selected.APIKey, ExternalModelName: "gpt-image-2", Enabled: &enabled},
	}); err != nil {
		t.Fatal(err)
	}

	image := directOpenAIImageModel(&selected)
	if image == nil || image.Name != "models/current-image-two" {
		t.Fatalf("selected image model = %#v, want current supplier gpt-image-2", image)
	}
}

func TestDirectOpenAIImageModelDoesNotMergeSameEndpointDifferentKeys(t *testing.T) {
	storage.Init(t.TempDir())
	enabled := true
	selected := storage.CustomModel{
		Name: "models/sol", Provider: "openai", APIURL: "https://shared.example.test", APIKey: "chat-card-key", ExternalModelName: "gpt-5.6-sol",
	}
	if err := storage.SaveModels([]storage.CustomModel{
		selected,
		{Name: "models/current-image", Provider: "openai", APIURL: selected.APIURL, APIKey: selected.APIKey, ExternalModelName: "gpt-image-1", Enabled: &enabled},
		{Name: "models/other-card-image", Provider: "openai", APIURL: selected.APIURL, APIKey: "other-card-key", ExternalModelName: "gpt-image-2", Enabled: &enabled},
	}); err != nil {
		t.Fatal(err)
	}

	image := directOpenAIImageModel(&selected)
	if image == nil || image.Name != "models/current-image" {
		t.Fatalf("same endpoint with another key was treated as current supplier: %#v", image)
	}
}

func TestDirectOpenAIImageModelMatchesNormalizedAccountPool(t *testing.T) {
	storage.Init(t.TempDir())
	enabled := true
	selected := storage.CustomModel{
		Name: "models/sol", Provider: "openai", APIURL: "https://api.example.test", AccountIDs: []string{"primary", "backup"}, ExternalModelName: "gpt-5.6-sol",
	}
	if err := storage.SaveModels([]storage.CustomModel{
		selected,
		{Name: "models/current-pool-image", Provider: "openai", APIURL: selected.APIURL, AccountIDs: []string{" backup ", "primary", "primary"}, ExternalModelName: "gpt-image-1", Enabled: &enabled},
		{Name: "models/other-pool-image", Provider: "openai", APIURL: selected.APIURL, AccountIDs: []string{"other"}, ExternalModelName: "gpt-image-2", Enabled: &enabled},
	}); err != nil {
		t.Fatal(err)
	}

	image := directOpenAIImageModel(&selected)
	if image == nil || image.Name != "models/current-pool-image" {
		t.Fatalf("normalized current account pool was not preferred: %#v", image)
	}
}

func TestRemoteOpenAIImageFallbackIsBoundedAndImageOnly(t *testing.T) {
	client := &http.Client{Transport: modelFetchRoundTripper(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "https://cdn.example.test/generated.png" {
			t.Fatalf("remote image URL = %s", request.URL)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"image/png"}},
			Body:       io.NopCloser(strings.NewReader("image-bytes")),
			Request:    request,
		}, nil
	})}
	data, mimeType, err := downloadOpenAIImageWithClient(context.Background(), "https://cdn.example.test/generated.png", client)
	if err != nil || data != "aW1hZ2UtYnl0ZXM=" || mimeType != "image/png" {
		t.Fatalf("downloaded image = %q, %q, %v", data, mimeType, err)
	}
	if _, _, err := downloadOpenAIImageWithClient(context.Background(), "http://cdn.example.test/generated.png", client); err == nil {
		t.Fatal("plain HTTP image URL must be rejected")
	}
	for _, blocked := range []string{"127.0.0.1", "10.0.0.1", "169.254.1.2", "100.64.0.1", "::1"} {
		if isPublicRemoteImageIP(net.ParseIP(blocked)) {
			t.Fatalf("protected address %s was accepted", blocked)
		}
	}
	if !isPublicRemoteImageIP(net.ParseIP("8.8.8.8")) {
		t.Fatal("public address was rejected")
	}
}

func TestDirectImageGenerationUnaryResponseNeverUsesSSEForAltQuery(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/v1internal:generateContent?alt=sse", nil)
	recorder := httptest.NewRecorder()
	writeDirectImageGenerationResponse(recorder, request, "image-request", "gpt-image-2", "image/png", "aW1hZ2U=")

	if recorder.Code != http.StatusOK || !strings.HasPrefix(recorder.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("unary image response = status %d, content-type %q", recorder.Code, recorder.Header().Get("Content-Type"))
	}
	body := strings.TrimSpace(recorder.Body.String())
	if strings.HasPrefix(body, "data:") || !strings.HasPrefix(body, "{") || !strings.Contains(body, `"inlineData"`) {
		t.Fatalf("unary generateContent returned a non-JSON image envelope: %s", body)
	}
}

func TestDirectImageGenerationStreamingResponseUsesSSE(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/v1internal:streamGenerateContent?alt=sse", nil)
	recorder := httptest.NewRecorder()
	writeDirectImageGenerationResponse(recorder, request, "image-request", "gpt-image-2", "image/png", "aW1hZ2U=")

	if recorder.Code != http.StatusOK || !strings.HasPrefix(recorder.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("streaming image response = status %d, content-type %q", recorder.Code, recorder.Header().Get("Content-Type"))
	}
	if body := recorder.Body.String(); !strings.HasPrefix(body, "data: ") || !strings.Contains(body, `"inlineData"`) {
		t.Fatalf("streamGenerateContent did not return an SSE image envelope: %s", body)
	}
}

func TestDirectOpenAIImageModelFallsBackToSeparateEnabledSupplier(t *testing.T) {
	storage.Init(t.TempDir())
	enabled := true
	selected := storage.CustomModel{
		Name: "models/sol", Provider: "openai", APIURL: "https://chat.example.test", APIKey: "chat-key", ExternalModelName: "gpt-5.6-sol",
	}
	image := storage.CustomModel{
		Name: "models/image", Provider: "openai", APIURL: "https://images.example.test", APIKey: "image-key", ExternalModelName: "gpt-image-2", Enabled: &enabled,
	}
	if err := storage.SaveModels([]storage.CustomModel{selected, image}); err != nil {
		t.Fatal(err)
	}

	resolved := directOpenAIImageModel(&selected)
	if resolved == nil || resolved.Name != image.Name || resolved.APIURL != image.APIURL {
		t.Fatalf("cross-supplier image model = %#v, want %#v", resolved, image)
	}
	execution := directOpenAIImageExecutionModel(&selected, resolved)
	if execution == nil || execution.APIKey != "image-key" || execution.APIURL != image.APIURL {
		t.Fatalf("cross-supplier image execution did not retain its own credentials: %#v", execution)
	}
}

func TestOpenAIImageDataFromResponseSupportsOpenAIBase64Shape(t *testing.T) {
	data, mimeType, err := openAIImageDataFromResponse([]byte(`{"data":[{"b64_json":"aGVsbG8=","mime_type":"image/webp"}]}`))
	if err != nil || data != "aGVsbG8=" || mimeType != "image/webp" {
		t.Fatalf("parsed image = %q, %q, %v", data, mimeType, err)
	}

	data, mimeType, err = openAIImageDataFromResponse([]byte(`{"data":[{"url":"data:image/png;base64,aW1hZ2U="}]}`))
	if err != nil || data != "aW1hZ2U=" || mimeType != "image/png" {
		t.Fatalf("parsed data URL image = %q, %q, %v", data, mimeType, err)
	}
	if _, _, err := openAIImageDataFromResponse([]byte(`{"data":[{"url":"data:text/plain;base64,aGVsbG8="}]}`)); err == nil || !strings.Contains(err.Error(), "Base64") {
		t.Fatalf("non-image data URL must be rejected, err = %v", err)
	}
	if _, _, err := openAIImageDataFromResponse([]byte(`{"data":[{"url":"https://example.test/generated.png"}]}`)); err == nil || !strings.Contains(err.Error(), "Base64") {
		t.Fatalf("URL-only response should require requested Base64 output, err = %v", err)
	}
}

func TestSanitizeImageGenerationPromptExcludesAntigravityMCPRules(t *testing.T) {
	gemini := map[string]any{
		"systemInstruction": map[string]any{"parts": []any{map[string]any{
			"text": "<mcp>rules: call command_status and inspect the workspace</mcp>",
		}}},
		"contents": []any{map[string]any{
			"role":  "user",
			"parts": []any{map[string]any{"text": "<system>MCP rules: never send this to an image model</system>\n用户请求：生成一张橙色宇航猫图片"}},
		}},
	}
	prompt := directImageGenerationPrompt(gemini)
	if prompt != "生成一张橙色宇航猫图片" {
		t.Fatalf("image prompt included internal instructions: %q", prompt)
	}
	if strings.Contains(strings.ToLower(prompt), "mcp") || strings.Contains(strings.ToLower(prompt), "command_status") {
		t.Fatalf("image prompt leaked Antigravity MCP rules: %q", prompt)
	}
}
