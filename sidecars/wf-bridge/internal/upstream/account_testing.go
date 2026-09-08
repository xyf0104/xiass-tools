package upstream

// This file implements the desktop-safe subset of XIASS's account test
// service.  It deliberately returns a replayable, redacted activity log
// rather than raw HTTP diagnostics: the desktop UI can show the same account
// test flow without ever receiving an authorization value, request header, or
// complete upstream response body.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	accountTestDefaultPrompt      = "hi"
	accountTestDefaultImagePrompt = "Generate a cute orange cat astronaut sticker on a clean pastel background."
	accountTestDefaultOpenAIModel = "gpt-5.4"
	accountTestDefaultClaudeModel = "claude-sonnet-4-5"
	accountTestImageMainModel     = "gpt-5.4-mini"
	accountTestMaxResponseBytes   = 8 << 20
	accountTestMaxTextBytes       = 16 << 10
	accountTestMaxErrorBytes      = 1200
	accountTestMaxImageDataBytes  = 5 << 20
	accountTestMaxImageURLBytes   = 8192
	accountTestMaxImages          = 2
)

// AccountTestRequest is intentionally the Wails-facing shape used by the
// account-card test UI. AccountID stays on the request even though the App
// resolves it before reaching this package, which makes one payload work for
// both an account test and a temporary, unsaved configuration test.
type AccountTestRequest struct {
	AccountID string `json:"accountId,omitempty"`
	// RequestID is an opaque, renderer-generated correlation ID. It never
	// contains credentials and lets the desktop host cancel exactly one
	// in-flight account-card probe without affecting normal proxy traffic.
	RequestID string `json:"requestId,omitempty"`
	Model     string `json:"model,omitempty"`
	Prompt    string `json:"prompt,omitempty"`
	// Mode currently accepts "default" and "compact". Unknown values fall
	// back to the normal probe instead of changing the provider request.
	Mode string `json:"mode,omitempty"`
}

// AccountTestStep is a safe, replayable terminal line. Text never contains a
// credential, request header, raw request payload, or unbounded response.
type AccountTestStep struct {
	Type string `json:"type"`
	Tone string `json:"tone"`
	Text string `json:"text"`
}

// AccountTestImage is a strictly validated image value that a desktop WebView
// may display. URL is either a bounded HTTPS/loopback URL returned by the
// configured upstream or a bounded image/* base64 data URL.
type AccountTestImage struct {
	URL      string `json:"url"`
	MIMEType string `json:"mimeType"`
}

// AccountTestResult is the detailed counterpart to the legacy TestResult.
// It is designed for an account-card modal: Steps may be replayed line by
// line, Content is a bounded final text preview, and Images contains only
// values that passed the strict image safety checks below.
type AccountTestResult struct {
	OK         bool               `json:"ok"`
	AccountID  string             `json:"accountId,omitempty"`
	RequestID  string             `json:"requestId,omitempty"`
	Model      string             `json:"model,omitempty"`
	Mode       string             `json:"mode,omitempty"`
	Message    string             `json:"message"`
	Endpoint   string             `json:"endpoint,omitempty"`
	APIStyle   string             `json:"apiStyle,omitempty"`
	StatusCode int                `json:"statusCode,omitempty"`
	ElapsedMs  int64              `json:"elapsedMs"`
	Content    string             `json:"content,omitempty"`
	Images     []AccountTestImage `json:"images,omitempty"`
	Steps      []AccountTestStep  `json:"steps"`
}

type accountTestHTTPResult struct {
	Endpoint   string
	StatusCode int
	Body       []byte
	Err        error
}

type accountTestParsedResponse struct {
	Text   string
	Err    string
	Images []AccountTestImage
}

type accountTestRunner struct {
	ctx     context.Context
	config  Config
	request AccountTestRequest
	result  AccountTestResult
	started time.Time
}

var accountTestSecretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(authorization\s*[:=]\s*(?:bearer\s+)?)\S+`),
	regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/=-]{8,}`),
	regexp.MustCompile(`(?i)(x-api-key\s*[:=]\s*)\S+`),
	regexp.MustCompile(`(?i)(api[_-]?key\s*[:=]\s*)[A-Za-z0-9._~+/=-]{8,}`),
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{8,}\b`),
}

// RunAccountTest performs a provider-aware, user-initiated probe. Direct
// OpenAI/Codex OAuth is always kept on the Responses route; it intentionally
// never falls back to Chat Completions, because a Codex OAuth token is not a
// generic platform API key. A normal OpenAI auto configuration may also fall
// back when a compatibility gateway returns a recognised protocol-shaped 400.
func RunAccountTest(ctx context.Context, config Config, request AccountTestRequest) AccountTestResult {
	runner := &accountTestRunner{
		ctx: ctx, config: config, request: request,
		started: time.Now(),
		result: AccountTestResult{
			AccountID: strings.TrimSpace(request.AccountID),
			RequestID: strings.TrimSpace(request.RequestID),
			Steps:     make([]AccountTestStep, 0, 10),
		},
	}
	runner.run()
	runner.result.ElapsedMs = time.Since(runner.started).Milliseconds()
	return runner.result
}

func (r *accountTestRunner) run() {
	if err := ValidateConfig(r.config); err != nil {
		r.fail("error", "测试配置无效："+sanitizeAccountTestText(err.Error(), accountTestMaxErrorBytes))
		return
	}

	r.request.Model = normalizeAccountTestModel(r.config, r.request.Model)
	r.request.Mode = normalizeAccountTestMode(r.request.Mode)
	r.result.Model = r.request.Model
	r.result.Mode = r.request.Mode
	r.step("start", "info", "开始测试账号连接")
	r.step("account", "muted", "账号类型："+safeAccountTestProvider(r.config))
	r.step("model", "info", "使用模型："+r.request.Model)

	if isAccountTestImageModel(r.request.Model) {
		r.runImageTest()
		return
	}

	if NormalizedProvider(r.config.Provider) == "anthropic" {
		r.runClaudeTest()
		return
	}
	r.runOpenAITest()
}

func (r *accountTestRunner) runOpenAITest() {
	prompt := normalizedAccountTestPrompt(r.request.Prompt, false)
	if prompt == accountTestDefaultPrompt {
		r.step("request", "muted", "发送测试消息：\"hi\"")
	} else {
		r.step("request", "muted", "发送自定义测试消息")
	}

	// A direct Codex OAuth account has a distinct authentication contract. It
	// must stay on Responses even if a saved legacy style claims otherwise.
	if IsOpenAICodexOAuth(r.config) {
		endpoint, err := ResolveResponsesURLForConfig(r.config)
		if err != nil {
			r.fail("error", sanitizeAccountTestText(err.Error(), accountTestMaxErrorBytes))
			return
		}
		if r.request.Mode == "compact" {
			endpoint, err = appendAccountTestPathSuffix(endpoint, "/compact")
			if err != nil {
				r.fail("error", sanitizeAccountTestText(err.Error(), accountTestMaxErrorBytes))
				return
			}
			r.step("status", "info", "正在通过 Codex Responses Compact 模式测试连接")
		} else {
			r.step("status", "info", "正在通过 Codex Responses 测试连接")
		}
		probe := r.post(endpoint, "responses", openAIResponsesTestPayload(r.request.Model, prompt, r.request.Mode))
		r.finishTextProbe(probe, "responses")
		return
	}

	style := EffectiveAPIStyle(r.config)
	if r.request.Mode == "compact" {
		style = "responses"
	}
	if style == "responses" {
		endpoint, err := ResolveResponsesURLForConfig(r.config)
		if err != nil {
			r.fail("error", sanitizeAccountTestText(err.Error(), accountTestMaxErrorBytes))
			return
		}
		if r.request.Mode == "compact" {
			endpoint, err = appendAccountTestPathSuffix(endpoint, "/compact")
			if err != nil {
				r.fail("error", sanitizeAccountTestText(err.Error(), accountTestMaxErrorBytes))
				return
			}
		}
		r.step("status", "info", "正在通过 /v1/responses 测试连接")
		probe := r.post(endpoint, "responses", openAIResponsesTestPayload(r.request.Model, prompt, r.request.Mode))
		if probe.Err == nil && probe.StatusCode >= http.StatusOK && probe.StatusCode < http.StatusMultipleChoices {
			r.finishTextProbe(probe, "responses")
			return
		}
		r.finishTextProbe(probe, "responses")
		return
	}

	endpoint, err := ResolveChatCompletionsURLForConfig(r.config)
	if err != nil {
		r.fail("error", sanitizeAccountTestText(err.Error(), accountTestMaxErrorBytes))
		return
	}
	r.step("status", "info", "正在通过 /v1/chat/completions 测试连接")
	r.finishTextProbe(r.post(endpoint, "chat_completions", openAIChatTestPayload(r.request.Model, prompt)), "chat_completions")
}

func (r *accountTestRunner) runClaudeTest() {
	prompt := normalizedAccountTestPrompt(r.request.Prompt, false)
	if prompt == accountTestDefaultPrompt {
		r.step("request", "muted", "发送测试消息：\"hi\"")
	} else {
		r.step("request", "muted", "发送自定义测试消息")
	}
	endpoints, err := ResolveAnthropicMessageCandidates(r.config)
	if err != nil {
		r.fail("error", sanitizeAccountTestText(err.Error(), accountTestMaxErrorBytes))
		return
	}
	r.step("status", "info", "正在通过 Claude Messages 测试连接")
	probe := r.post(endpoints[0], "messages", claudeMessagesTestPayload(r.request.Model, prompt))
	if probe.Err == nil && probe.StatusCode >= http.StatusOK && probe.StatusCode < http.StatusMultipleChoices {
		r.finishTextProbe(probe, "messages")
		return
	}
	if len(endpoints) > 1 && CanFallbackToChat(probe.StatusCode) {
		r.step("fallback", "warning", "Claude 标准 Messages 端点不可用，正在通过兼容路径重试")
		r.finishTextProbe(r.post(endpoints[1], "messages", claudeMessagesTestPayload(r.request.Model, prompt)), "messages")
		return
	}
	r.finishTextProbe(probe, "messages")
}

func (r *accountTestRunner) runImageTest() {
	if NormalizedProvider(r.config.Provider) != "openai" {
		r.fail("error", "当前账户类型不支持 OpenAI 图片测试")
		return
	}
	prompt := normalizedAccountTestPrompt(r.request.Prompt, true)
	r.step("request", "muted", "发送生图测试请求")

	if IsOpenAICodexOAuth(r.config) {
		endpoint, err := ResolveResponsesURLForConfig(r.config)
		if err != nil {
			r.fail("error", sanitizeAccountTestText(err.Error(), accountTestMaxErrorBytes))
			return
		}
		r.step("status", "info", "正在通过 Codex Responses 图片工具测试连接")
		r.finishImageProbe(r.post(endpoint, "responses", openAIResponsesImageTestPayload(r.request.Model, prompt)), "responses")
		return
	}

	endpoint, err := resolveAccountTestImageURL(r.config)
	if err != nil {
		r.fail("error", sanitizeAccountTestText(err.Error(), accountTestMaxErrorBytes))
		return
	}
	r.step("status", "info", "正在通过 /v1/images/generations 测试连接")
	r.finishImageProbe(r.post(endpoint, "images", openAIImagesTestPayload(r.request.Model, prompt)), "images")
}

func (r *accountTestRunner) post(endpoint, style string, body any) accountTestHTTPResult {
	encoded, err := json.Marshal(body)
	if err != nil {
		return accountTestHTTPResult{Endpoint: endpoint, Err: fmt.Errorf("无法构建测试请求")}
	}
	request, err := http.NewRequestWithContext(r.ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return accountTestHTTPResult{Endpoint: endpoint, Err: fmt.Errorf("无法创建测试请求")}
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream, application/json")
	if err := ApplyCredentials(request, r.config); err != nil {
		return accountTestHTTPResult{Endpoint: endpoint, Err: err}
	}

	client := &http.Client{Timeout: 60 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		if ctxErr := r.ctx.Err(); ctxErr != nil {
			return accountTestHTTPResult{Endpoint: endpoint, Err: ctxErr}
		}
		return accountTestHTTPResult{Endpoint: endpoint, Err: fmt.Errorf("无法连接上游：%s", safeNetworkError(err))}
	}
	defer response.Body.Close()
	bodyBytes, readErr := io.ReadAll(io.LimitReader(response.Body, accountTestMaxResponseBytes))
	if readErr != nil {
		if ctxErr := r.ctx.Err(); ctxErr != nil {
			return accountTestHTTPResult{Endpoint: endpoint, StatusCode: response.StatusCode, Body: bodyBytes, Err: ctxErr}
		}
		return accountTestHTTPResult{Endpoint: endpoint, StatusCode: response.StatusCode, Body: bodyBytes, Err: fmt.Errorf("无法读取上游响应：%s", safeNetworkError(readErr))}
	}
	return accountTestHTTPResult{Endpoint: endpoint, StatusCode: response.StatusCode, Body: bodyBytes}
}

func (r *accountTestRunner) finishTextProbe(probe accountTestHTTPResult, style string) {
	r.result.Endpoint = accountTestDisplayEndpoint(probe.Endpoint)
	r.result.APIStyle = style
	r.result.StatusCode = probe.StatusCode
	if r.finishCancelledProbe(probe) {
		return
	}
	if probe.Err != nil {
		r.fail("error", sanitizeAccountTestText(probe.Err.Error(), accountTestMaxErrorBytes))
		return
	}
	if probe.StatusCode < http.StatusOK || probe.StatusCode >= http.StatusMultipleChoices {
		r.fail("error", safeAccountTestHTTPError(probe.StatusCode, probe.Body, r.config))
		return
	}
	r.step("connected", "success", fmt.Sprintf("已连接到 API（HTTP %d）", probe.StatusCode))
	parsed := parseAccountTestResponse(probe.Body)
	if parsed.Err != "" {
		r.fail("error", parsed.Err)
		return
	}
	r.result.Content = parsed.Text
	if parsed.Text != "" {
		r.step("response", "success", "响应：\n"+parsed.Text)
	} else {
		r.step("response", "muted", "上游已接受测试请求，未返回文本内容")
	}
	r.succeed("测试完成！")
}

func (r *accountTestRunner) finishImageProbe(probe accountTestHTTPResult, style string) {
	r.result.Endpoint = accountTestDisplayEndpoint(probe.Endpoint)
	r.result.APIStyle = style
	r.result.StatusCode = probe.StatusCode
	if r.finishCancelledProbe(probe) {
		return
	}
	if probe.Err != nil {
		r.fail("error", sanitizeAccountTestText(probe.Err.Error(), accountTestMaxErrorBytes))
		return
	}
	if probe.StatusCode < http.StatusOK || probe.StatusCode >= http.StatusMultipleChoices {
		r.fail("error", safeAccountTestHTTPError(probe.StatusCode, probe.Body, r.config))
		return
	}
	r.step("connected", "success", fmt.Sprintf("已连接到 API（HTTP %d）", probe.StatusCode))
	parsed := parseAccountTestResponse(probe.Body)
	if parsed.Err != "" {
		r.fail("error", parsed.Err)
		return
	}
	if len(parsed.Images) == 0 {
		r.fail("error", "上游未返回可安全预览的测试图片")
		return
	}
	r.result.Images = parsed.Images
	if parsed.Text != "" {
		r.result.Content = parsed.Text
		r.step("response", "success", "响应：\n"+parsed.Text)
	}
	r.step("image", "success", fmt.Sprintf("已收到第 %d 张测试图片", len(parsed.Images)))
	r.succeed("测试完成！")
}

// finishCancelledProbe makes a user-initiated close/cancel distinguishable
// from an upstream failure. The context is owned by App and is also passed to
// the HTTP request, so this path aborts both a blocked connection and a slow
// streaming response rather than merely hiding the result in the renderer.
func (r *accountTestRunner) finishCancelledProbe(probe accountTestHTTPResult) bool {
	if errors.Is(probe.Err, context.Canceled) {
		r.fail("cancelled", "测试已取消")
		return true
	}
	if errors.Is(probe.Err, context.DeadlineExceeded) {
		r.fail("error", "测试请求超时")
		return true
	}
	return false
}

func (r *accountTestRunner) step(kind, tone, text string) {
	text = sanitizeAccountTestText(text, accountTestMaxTextBytes)
	if text == "" {
		return
	}
	r.result.Steps = append(r.result.Steps, AccountTestStep{Type: kind, Tone: tone, Text: text})
}

func (r *accountTestRunner) fail(kind, message string) {
	message = sanitizeAccountTestText(message, accountTestMaxErrorBytes)
	if message == "" {
		message = "账户测试失败"
	}
	r.result.OK = false
	r.result.Message = message
	r.step(kind, "error", message)
}

func (r *accountTestRunner) succeed(message string) {
	r.result.OK = true
	r.result.Message = message
	r.step("complete", "success", message)
}

func normalizeAccountTestModel(config Config, model string) string {
	model = strings.TrimSpace(strings.TrimPrefix(model, "models/"))
	if model != "" {
		return truncateAccountTestText(model, 240)
	}
	if NormalizedProvider(config.Provider) == "anthropic" {
		return accountTestDefaultClaudeModel
	}
	return accountTestDefaultOpenAIModel
}

func normalizeAccountTestMode(mode string) string {
	if strings.EqualFold(strings.TrimSpace(mode), "compact") {
		return "compact"
	}
	return "default"
}

func normalizedAccountTestPrompt(prompt string, image bool) string {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		if image {
			return accountTestDefaultImagePrompt
		}
		return accountTestDefaultPrompt
	}
	return truncateAccountTestText(prompt, 4096)
}

func safeAccountTestProvider(config Config) string {
	if IsOpenAICodexOAuth(config) {
		return "OpenAI / Codex OAuth"
	}
	switch NormalizedProvider(config.Provider) {
	case "anthropic":
		return "Claude"
	case "grok":
		return "Grok（OpenAI 兼容）"
	case "custom":
		return "兼容接口"
	default:
		return "OpenAI"
	}
}

func isAccountTestImageModel(model string) bool {
	model = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(model, "models/")))
	if model == "" {
		return false
	}
	for _, marker := range []string{
		"gpt-image", "dall-e", "imagen", "imagegen", "image-gen", "stable-diffusion", "stable_diffusion", "sdxl", "flux", "midjourney",
	} {
		if strings.Contains(model, marker) {
			return true
		}
	}
	return strings.HasPrefix(model, "image-") || model == "image" || model == "image2" || model == "image-2"
}

func openAIResponsesTestPayload(model, prompt, mode string) map[string]any {
	payload := map[string]any{
		"model": model,
		"input": []any{map[string]any{
			"role":    "user",
			"content": []any{map[string]any{"type": "input_text", "text": prompt}},
		}},
		"stream":            true,
		"max_output_tokens": 64,
	}
	if mode == "compact" {
		payload["store"] = false
	}
	return payload
}

func openAIChatTestPayload(model, prompt string) map[string]any {
	return map[string]any{
		"model": model, "stream": true, "max_tokens": 64,
		"messages": []any{map[string]any{"role": "user", "content": prompt}},
	}
}

func claudeMessagesTestPayload(model, prompt string) map[string]any {
	return map[string]any{
		"model": model, "stream": true, "max_tokens": 64,
		"messages": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": prompt}}}},
	}
}

func openAIImagesTestPayload(model, prompt string) map[string]any {
	return map[string]any{
		"model": model, "prompt": prompt, "n": 1, "response_format": "b64_json",
	}
}

// XIASS uses a Responses-capable main model while the selected image model is
// attached as the native image_generation tool. This verifies the actual image
// model without pretending an image-only model is a text Responses model.
func openAIResponsesImageTestPayload(imageModel, prompt string) map[string]any {
	return map[string]any{
		"model": accountTestImageMainModel,
		"input": []any{map[string]any{
			"type": "message", "role": "user",
			"content": []any{map[string]any{"type": "input_text", "text": prompt}},
		}},
		"tools":       []any{map[string]any{"type": "image_generation", "action": "generate", "model": imageModel, "output_format": "png"}},
		"tool_choice": map[string]any{"type": "image_generation"},
		"stream":      true,
		"store":       false,
	}
}

func resolveAccountTestImageURL(config Config) (string, error) {
	if UsesManualEndpoint(config) {
		parsed, err := validateBaseURL(config.APIURL)
		if err != nil {
			return "", err
		}
		if strings.HasSuffix(strings.TrimRight(parsed.Path, "/"), "/images/generations") {
			return manualEndpointURL(config.APIURL)
		}
	}
	return endpointURL(config.APIURL, "images/generations")
}

func appendAccountTestPathSuffix(rawURL, suffix string) (string, error) {
	parsed, err := validateBaseURL(rawURL)
	if err != nil {
		return "", err
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/" + strings.TrimLeft(suffix, "/")
	parsed.RawPath = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func parseAccountTestResponse(body []byte) accountTestParsedResponse {
	parsed := accountTestParsedResponse{Images: make([]AccountTestImage, 0, 1)}
	seenImage := make(map[string]struct{})
	finish := func() accountTestParsedResponse {
		parsed.Text = sanitizeAccountTestText(parsed.Text, accountTestMaxTextBytes)
		parsed.Err = sanitizeAccountTestText(parsed.Err, accountTestMaxErrorBytes)
		return parsed
	}
	consume := func(value any) {
		if parsed.Err != "" {
			return
		}
		if message := accountTestErrorFromValue(value); message != "" {
			parsed.Err = message
			return
		}
		if text := accountTestTextFromValue(value); text != "" {
			// SSE text arrives in deltas while a completion event can contain
			// the final assembled message. Keep the short streamed transcript
			// without duplicating a final body that already contains it.
			if parsed.Text == "" {
				parsed.Text = text
			} else if !strings.Contains(parsed.Text, text) && len(parsed.Text) < accountTestMaxTextBytes {
				parsed.Text += text
			}
		}
		collectAccountTestImages(value, false, &parsed.Images, seenImage)
	}

	var single any
	if json.Unmarshal(body, &single) == nil {
		consume(single)
		return finish()
	}
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var event any
		if json.Unmarshal([]byte(data), &event) == nil {
			consume(event)
		}
	}
	return finish()
}

func accountTestErrorFromValue(value any) string {
	object, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	eventType, _ := object["type"].(string)
	if eventType != "error" && eventType != "response.failed" && eventType != "response.incomplete" {
		if _, exists := object["error"]; !exists {
			return ""
		}
	}
	if errorValue, ok := object["error"].(map[string]any); ok {
		return accountTestMessageFromObject(errorValue)
	}
	if response, ok := object["response"].(map[string]any); ok {
		if errorValue, ok := response["error"].(map[string]any); ok {
			return accountTestMessageFromObject(errorValue)
		}
	}
	return accountTestMessageFromObject(object)
}

func accountTestMessageFromObject(object map[string]any) string {
	for _, key := range []string{"message", "detail", "error_description", "code"} {
		if value, ok := object[key].(string); ok && strings.TrimSpace(value) != "" {
			return sanitizeAccountTestText(value, accountTestMaxErrorBytes)
		}
	}
	return "上游返回测试错误"
}

func accountTestTextFromValue(value any) string {
	object, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	eventType, _ := object["type"].(string)
	if eventType == "response.output_text.delta" {
		if delta, ok := object["delta"].(string); ok {
			return delta
		}
	}
	if eventType == "content_block_delta" {
		if delta, ok := object["delta"].(map[string]any); ok {
			if text, ok := delta["text"].(string); ok {
				return text
			}
		}
	}
	if choices, ok := object["choices"].([]any); ok {
		for _, choiceValue := range choices {
			if choice, ok := choiceValue.(map[string]any); ok {
				for _, key := range []string{"delta", "message"} {
					if message, ok := choice[key].(map[string]any); ok {
						if content, ok := message["content"].(string); ok && content != "" {
							return content
						}
					}
				}
			}
		}
	}
	if content, ok := object["content"]; ok {
		if text := accountTestTextFromContent(content); text != "" {
			return text
		}
	}
	if output, ok := object["output"]; ok {
		if text := accountTestTextFromContent(output); text != "" {
			return text
		}
	}
	if response, ok := object["response"].(map[string]any); ok {
		return accountTestTextFromValue(response)
	}
	return ""
}

func accountTestTextFromContent(value any) string {
	switch current := value.(type) {
	case string:
		return current
	case []any:
		for _, item := range current {
			if text := accountTestTextFromContent(item); text != "" {
				return text
			}
		}
	case map[string]any:
		itemType, _ := current["type"].(string)
		if itemType == "image_generation_call" || itemType == "image" {
			return ""
		}
		for _, key := range []string{"text", "output_text", "content"} {
			if text, ok := current[key].(string); ok && text != "" {
				return text
			}
		}
		if nested, ok := current["content"]; ok {
			return accountTestTextFromContent(nested)
		}
	}
	return ""
}

func collectAccountTestImages(value any, imageContext bool, images *[]AccountTestImage, seen map[string]struct{}) {
	if len(*images) >= accountTestMaxImages {
		return
	}
	switch current := value.(type) {
	case []any:
		for _, item := range current {
			collectAccountTestImages(item, imageContext, images, seen)
			if len(*images) >= accountTestMaxImages {
				return
			}
		}
	case map[string]any:
		itemType, _ := current["type"].(string)
		imageHere := imageContext || itemType == "image_generation_call" || itemType == "image_generation.completed" || strings.Contains(itemType, "image_generation")
		mimeType := accountTestImageMIMEType(current)
		for _, key := range []string{"b64_json", "partial_image_b64"} {
			if encoded, ok := current[key].(string); ok {
				appendAccountTestImage(images, seen, dataURLFromAccountTestBase64(mimeType, encoded))
			}
		}
		if imageHere {
			if encoded, ok := current["result"].(string); ok {
				appendAccountTestImage(images, seen, dataURLFromAccountTestBase64(mimeType, encoded))
			}
			if remote, ok := current["url"].(string); ok {
				appendAccountTestImage(images, seen, safeAccountTestRemoteImage(remote, mimeType))
			}
		}
		for _, key := range []string{"data", "output", "content", "response", "item"} {
			if child, ok := current[key]; ok {
				collectAccountTestImages(child, imageHere || key == "data", images, seen)
			}
		}
	}
}

func accountTestImageMIMEType(value map[string]any) string {
	for _, key := range []string{"mime_type", "mimeType", "output_format", "format"} {
		if raw, ok := value[key].(string); ok {
			raw = strings.ToLower(strings.TrimSpace(raw))
			if strings.HasPrefix(raw, "image/") && allowedAccountTestImageMIME(raw) {
				return raw
			}
			if allowedAccountTestImageMIME("image/" + raw) {
				return "image/" + raw
			}
		}
	}
	return "image/png"
}

func dataURLFromAccountTestBase64(mimeType, encoded string) AccountTestImage {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" || len(encoded) > base64.StdEncoding.EncodedLen(accountTestMaxImageDataBytes) {
		return AccountTestImage{}
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(decoded) == 0 || len(decoded) > accountTestMaxImageDataBytes {
		return AccountTestImage{}
	}
	if !allowedAccountTestImageMIME(mimeType) {
		return AccountTestImage{}
	}
	return AccountTestImage{URL: "data:" + mimeType + ";base64," + encoded, MIMEType: mimeType}
}

func safeAccountTestRemoteImage(rawURL, mimeType string) AccountTestImage {
	if len(rawURL) == 0 || len(rawURL) > accountTestMaxImageURLBytes {
		return AccountTestImage{}
	}
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Host == "" || parsed.User != nil {
		return AccountTestImage{}
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname())) {
		return AccountTestImage{}
	}
	if !allowedAccountTestImageMIME(mimeType) {
		mimeType = "image/png"
	}
	return AccountTestImage{URL: parsed.String(), MIMEType: mimeType}
}

func allowedAccountTestImageMIME(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "image/png", "image/jpeg", "image/webp", "image/gif":
		return true
	default:
		return false
	}
}

func appendAccountTestImage(images *[]AccountTestImage, seen map[string]struct{}, image AccountTestImage) {
	if image.URL == "" || len(*images) >= accountTestMaxImages {
		return
	}
	if _, exists := seen[image.URL]; exists {
		return
	}
	seen[image.URL] = struct{}{}
	*images = append(*images, image)
}

func safeAccountTestHTTPError(statusCode int, body []byte, config Config) string {
	// Account testing has its own detailed response parser. Check the raw body
	// first because a gateway can echo the configured value in an unfamiliar
	// JSON field that the parser intentionally ignores.
	if containsConfiguredCredential(string(body), config) {
		return fmt.Sprintf("上游返回 HTTP %d：上游拒绝了请求（鉴权详情已隐藏）", statusCode)
	}
	parsed := parseAccountTestResponse(body)
	if parsed.Err != "" {
		return fmt.Sprintf("上游返回 HTTP %d：%s", statusCode, safeStatusDetail(parsed.Err, config))
	}
	var object map[string]any
	if json.Unmarshal(body, &object) == nil {
		if message := accountTestErrorFromValue(object); message != "" {
			return fmt.Sprintf("上游返回 HTTP %d：%s", statusCode, safeStatusDetail(message, config))
		}
	}
	return fmt.Sprintf("上游返回 HTTP %d", statusCode)
}

func accountTestDisplayEndpoint(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return ""
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	return parsed.String()
}

func sanitizeAccountTestText(value string, limit int) string {
	value = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(value, "\r", " "), "\x00", ""))
	for _, pattern := range accountTestSecretPatterns {
		value = pattern.ReplaceAllString(value, "$1[已隐藏]")
	}
	return truncateAccountTestText(value, limit)
}

func truncateAccountTestText(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	value = value[:limit]
	for !utf8.ValidString(value) && len(value) > 0 {
		value = value[:len(value)-1]
	}
	return strings.TrimSpace(value) + "…"
}
