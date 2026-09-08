package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"antigravity-wf-assistant/internal/storage"
	"antigravity-wf-assistant/internal/upstream"
)

// A direct Images response contains base64 data, so reserve enough space for
// a complete 50 MB image plus its JSON envelope. The same limit is used by the
// attachment bridge; keeping it bounded prevents a malformed gateway response
// from exhausting the local proxy process.
const maxOpenAIImageGenerationResponseBytes int64 = (maxForwardedAttachmentBytes * 4 / 3) + (2 << 20)

// directOpenAIImageModel returns the best enabled image-only model for the
// active text model. A model from the current supplier card is always
// preferred; when that supplier has no image model, WF falls back to an enabled
// image model from a separately configured OpenAI-compatible supplier. This lets a GPT-5.6 chat
// model and gpt-image-2 live on two independent supplier cards.
//
// The selected image model always owns its endpoint, credential and account
// pool, including when it belongs to the current supplier card. Secrets are
// never copied into logs or the Antigravity response.
func directOpenAIImageModel(model *storage.CustomModel) *storage.CustomModel {
	if model == nil || !isOpenAICompatibleImageProvider(model.Provider) {
		return nil
	}
	if upstream.IsOpenAICodexOAuth(upstream.ConfigFromModel(*model)) {
		// ChatGPT Codex OAuth has a Responses-only contract; it is not an API-key
		// image endpoint and must stay on the existing Responses route.
		return nil
	}
	selectedSupplier, selectedSupplierOK := directImageSupplierIdentityFor(*model)

	candidates := []storage.CustomModel{*model}
	configured, loadErr := storage.LoadEnabledModels()
	if loadErr == nil {
		candidates = append(candidates, configured...)
	}

	type imageCandidate struct {
		model        storage.CustomModel
		endpoint     string
		sameSupplier bool
	}
	valid := make([]imageCandidate, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if !isOpenAICompatibleImageProvider(candidate.Provider) || !isDirectImageModelName(candidate.ExternalModelName) {
			continue
		}
		config := upstream.ConfigFromModel(candidate)
		if upstream.IsOpenAICodexOAuth(config) {
			continue
		}
		candidateEndpoint, err := upstream.ResolveImagesGenerationsURLForConfig(config)
		if err != nil || candidateEndpoint == "" {
			continue
		}
		key := candidateEndpoint + "\x00" + strings.TrimSpace(candidate.Name) + "\x00" + strings.TrimSpace(candidate.ExternalModelName)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		candidateSupplier, candidateSupplierOK := directImageSupplierIdentityFor(candidate)
		valid = append(valid, imageCandidate{
			model:        candidate,
			endpoint:     candidateEndpoint,
			sameSupplier: selectedSupplierOK && candidateSupplierOK && selectedSupplier.equal(candidateSupplier),
		})
	}
	if len(valid) == 0 {
		return nil
	}
	sort.SliceStable(valid, func(i, j int) bool {
		if valid[i].sameSupplier != valid[j].sameSupplier {
			return valid[i].sameSupplier
		}
		left, right := directImageModelPriority(valid[i].model.ExternalModelName), directImageModelPriority(valid[j].model.ExternalModelName)
		if left != right {
			return left < right
		}
		if valid[i].endpoint != valid[j].endpoint {
			return valid[i].endpoint < valid[j].endpoint
		}
		return strings.ToLower(valid[i].model.ExternalModelName) < strings.ToLower(valid[j].model.ExternalModelName)
	})
	selected := valid[0].model
	return &selected
}

// directImageSupplierIdentity mirrors the supplier-card boundary used by the
// renderer. Endpoint equality alone is insufficient: two suppliers may expose
// the same domain while using different API keys, account pools or headers.
// The values below are used only for in-memory equality and are never logged.
type directImageSupplierIdentity struct {
	provider     string
	endpointMode string
	endpoint     string
	accountIDs   []string
	apiKey       string
	authMode     string
	authHeader   string
	headers      []string
}

func directImageSupplierIdentityFor(model storage.CustomModel) (directImageSupplierIdentity, bool) {
	config := upstream.ConfigFromModel(model)
	endpoint, err := upstream.ResolveImagesGenerationsURLForConfig(config)
	if err != nil || endpoint == "" {
		return directImageSupplierIdentity{}, false
	}
	mode := upstream.NormalizedEndpointMode(config.EndpointMode)
	if upstream.UsesManualEndpoint(config) {
		mode = "manual"
	}
	accountIDs := normalizedDirectImageAccountIDs(model.AccountIDs)
	identity := directImageSupplierIdentity{
		provider:     upstream.NormalizedProvider(model.Provider),
		endpointMode: mode,
		endpoint:     endpoint,
		accountIDs:   accountIDs,
	}
	if len(accountIDs) == 0 {
		identity.apiKey = strings.TrimSpace(model.APIKey)
		identity.authMode = strings.ToLower(strings.TrimSpace(model.AuthMode))
		if identity.authMode == "" {
			identity.authMode = "bearer"
		}
		identity.authHeader = strings.ToLower(strings.TrimSpace(model.AuthHeader))
		identity.headers = normalizedDirectImageHeaders(model.Headers)
	}
	return identity, true
}

func normalizedDirectImageAccountIDs(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func normalizedDirectImageHeaders(values map[string]string) []string {
	result := make([]string, 0, len(values))
	for name, value := range values {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" {
			continue
		}
		result = append(result, name+"\x00"+value)
	}
	sort.Strings(result)
	return result
}

func (left directImageSupplierIdentity) equal(right directImageSupplierIdentity) bool {
	if left.provider != right.provider || left.endpointMode != right.endpointMode || left.endpoint != right.endpoint ||
		left.apiKey != right.apiKey || left.authMode != right.authMode || left.authHeader != right.authHeader ||
		len(left.accountIDs) != len(right.accountIDs) || len(left.headers) != len(right.headers) {
		return false
	}
	for index := range left.accountIDs {
		if left.accountIDs[index] != right.accountIDs[index] {
			return false
		}
	}
	for index := range left.headers {
		if left.headers[index] != right.headers[index] {
			return false
		}
	}
	return true
}

func directOpenAIImageExecutionModel(_ *storage.CustomModel, imageModel *storage.CustomModel) *storage.CustomModel {
	// The image model owns the image request, including its credential and
	// account pool. Two independently configured supplier cards can share the
	// same base URL while using different keys; endpoint equality alone must
	// never make WF borrow the chat model's credential.
	return imageModel
}

func isOpenAICompatibleImageProvider(provider string) bool {
	switch upstream.NormalizedProvider(provider) {
	case "openai", "grok", "custom":
		return true
	default:
		return false
	}
}

func isDirectImageModelName(value string) bool {
	value = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(value, "models/")))
	if value == "" {
		return false
	}
	for _, marker := range []string{
		"gpt-image", "dall-e", "imagen", "imagegen", "image-gen", "stable-diffusion", "stable_diffusion", "sdxl", "flux", "midjourney",
	} {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return strings.HasPrefix(value, "image-") || value == "image" || value == "image2" || value == "image-2"
}

func directImageModelPriority(value string) int {
	value = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(value, "models/")))
	switch {
	case value == "gpt-image-2" || value == "image-2" || value == "image2":
		return 0
	case strings.HasPrefix(value, "gpt-image"):
		return 10
	case strings.HasPrefix(value, "image-") || value == "image":
		return 20
	case strings.Contains(value, "dall-e"):
		return 30
	case strings.Contains(value, "imagen"):
		return 40
	case strings.Contains(value, "flux"):
		return 50
	case strings.Contains(value, "stable") || strings.Contains(value, "sdxl"):
		return 60
	default:
		return 100
	}
}

func requestsDirectImageGeneration(gemini map[string]any) bool {
	if requested, _ := gemini["wfNativeImageGeneration"].(bool); requested {
		return true
	}
	_, requested := requestedResponsesBuiltinTools(gemini)[responseImageGenerationTool]
	return requested
}

// forwardOpenAIImagesGeneration performs one dedicated OpenAI Images request
// and converts its base64 result back into Antigravity's native streaming
// envelope. It never replays an uncertain network request: retry/failover is
// limited to a concrete non-2xx rejection, where the upstream proves that no
// image generation started.
func forwardOpenAIImagesGeneration(w http.ResponseWriter, incoming *http.Request, model *storage.CustomModel, imageModel *storage.CustomModel, gemini map[string]any, requestID string) {
	if model == nil || imageModel == nil || strings.TrimSpace(imageModel.ExternalModelName) == "" {
		http.Error(w, "未找到可用的自定义图片模型", http.StatusBadRequest)
		return
	}
	executionModel := directOpenAIImageExecutionModel(model, imageModel)
	prompt := directImageGenerationPrompt(gemini)
	requestBody := map[string]any{
		"model":           strings.TrimSpace(imageModel.ExternalModelName),
		"prompt":          prompt,
		"n":               1,
		"response_format": "b64_json",
	}
	encoded, err := json.Marshal(requestBody)
	if err != nil {
		http.Error(w, "无法构建图片生成请求", http.StatusBadRequest)
		return
	}

	policy := currentStreamRecoveryPolicy()
	excludedAccounts := map[string]struct{}{}
	pinnedAccountID := ""
	reconnects := 0
	client := &http.Client{Timeout: upstreamStreamTimeout}
	for attempt := 1; ; attempt++ {
		attemptModel, lease, err := acquireAttemptModel(executionModel, excludedAccounts, pinnedAccountID)
		if err != nil {
			trace("images-account-pool-error", map[string]any{"requestId": requestID, "attempt": attempt, "message": err.Error()})
			http.Error(w, accountPoolError("图片模型", err), http.StatusServiceUnavailable)
			return
		}
		if pinnedAccountID == "" && lease != nil {
			pinnedAccountID = lease.ID
		}
		config := upstream.ConfigFromModel(*attemptModel)
		if upstream.IsOpenAICodexOAuth(config) {
			releaseAttemptSuccess(lease)
			http.Error(w, "当前官方 OAuth 账户不支持专用图片模型接口", http.StatusBadRequest)
			return
		}
		endpoint, err := upstream.ResolveImagesGenerationsURLForConfig(config)
		if err != nil {
			releaseAttemptSuccess(lease)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		req, err := http.NewRequestWithContext(incoming.Context(), http.MethodPost, endpoint, bytes.NewReader(encoded))
		if err != nil {
			releaseAttemptSuccess(lease)
			http.Error(w, "无法创建图片生成请求", http.StatusBadGateway)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		if err := upstream.ApplyCredentials(req, config); err != nil {
			releaseAttemptSuccess(lease)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		trace("images-upstream-request", map[string]any{
			"requestId": requestID, "attempt": attempt, "imageModel": imageModel.ExternalModelName,
			"accountId": func() string {
				if lease == nil {
					return ""
				}
				return lease.ID
			}(),
		})
		req, writeTrace := traceUpstreamRequestWrite(req)
		resp, err := client.Do(req)
		if err != nil {
			trace("images-upstream-error", map[string]any{"requestId": requestID, "attempt": attempt, "message": err.Error()})
			if incoming.Context().Err() != nil {
				releaseAttemptFailure(lease, 0, "", err.Error())
				return
			}
			reconnects++
			if canRetryUnsentTransportFailureBeforeResponse(policy, reconnects, writeTrace) {
				if lease != nil {
					lease.Release()
				}
				if waitForRejectedRequestRetry(incoming.Context(), policy, "images", requestID, "connection-before-write", "", reconnects) {
					continue
				}
			}
			releaseAttemptFailure(lease, 0, "", err.Error())
			http.Error(w, "无法确认上游是否已接收图片生成请求；为避免重复扣费，只有请求写出前的连接失败会自动重试", http.StatusBadGateway)
			return
		}
		observeAttemptQuota(lease, "images", resp)
		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
			resp.Body.Close()
			retryAfter := resp.Header.Get("Retry-After")
			failureDetail := string(errBody)
			if strings.TrimSpace(failureDetail) == "" {
				failureDetail = fmt.Sprintf("图片模型请求失败（HTTP %d）", resp.StatusCode)
			}
			trace("images-upstream-error-response", map[string]any{"requestId": requestID, "attempt": attempt, "statusCode": resp.StatusCode, "body": failureDetail[:min(len(failureDetail), 500)]})
			if shouldFailOverAccount(lease, resp.StatusCode, failureDetail) {
				reconnects++
				mayRetry := policy.enabled && reconnects <= policy.maxAttempts
				if mayRetry && shouldRetrySameAccount(lease, resp.StatusCode, failureDetail) {
					if lease != nil {
						lease.Release()
					}
				} else {
					releaseAttemptFailure(lease, resp.StatusCode, retryAfter, failureDetail)
					excludeFailedAttempt(excludedAccounts, lease)
				}
				// A non-2xx response proves the current account rejected the request
				// before generation, so retrying that same account is safe.
				if mayRetry && waitForRejectedRequestRetry(incoming.Context(), policy, "images", requestID, fmt.Sprintf("http-%d", resp.StatusCode), rejectedRetryAfter(resp.StatusCode, failureDetail, retryAfter, reconnects), reconnects) {
					continue
				}
				writeImageGenerationHTTPError(w, resp.StatusCode)
				return
			}
			releaseAttemptFailure(lease, resp.StatusCode, retryAfter, failureDetail)
			writeImageGenerationHTTPError(w, resp.StatusCode)
			return
		}

		body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxOpenAIImageGenerationResponseBytes+1))
		resp.Body.Close()
		if readErr != nil || int64(len(body)) > maxOpenAIImageGenerationResponseBytes {
			releaseAttemptSuccess(lease)
			trace("images-upstream-invalid-response", map[string]any{"requestId": requestID, "attempt": attempt, "reason": "body-too-large-or-unreadable"})
			http.Error(w, "上游图片响应无法读取或超过大小限制", http.StatusBadGateway)
			return
		}
		imageData, mimeType, err := openAIImageDataFromResponse(body)
		if err != nil {
			if remoteURL := openAIImageRemoteURL(body); remoteURL != "" {
				imageData, mimeType, err = downloadOpenAIImage(incoming.Context(), remoteURL)
			}
		}
		if err != nil {
			releaseAttemptSuccess(lease)
			trace("images-upstream-invalid-response", map[string]any{"requestId": requestID, "attempt": attempt, "reason": err.Error()})
			http.Error(w, "上游图片响应未返回可安全嵌入的图片数据", http.StatusBadGateway)
			return
		}
		releaseAttemptSuccess(lease)
		writeDirectImageGenerationResponse(w, incoming, requestID, imageModel.ExternalModelName, mimeType, imageData)
		trace("images-upstream-success", map[string]any{"requestId": requestID, "attempt": attempt, "imageModel": imageModel.ExternalModelName})
		return
	}
}

func writeDirectImageGenerationResponse(w http.ResponseWriter, incoming *http.Request, requestID, modelName, mimeType, imageData string) {
	response := map[string]any{
		"candidates": []any{map[string]any{
			"content": map[string]any{
				"role": "model",
				"parts": []any{map[string]any{"inlineData": map[string]any{
					"mimeType": mimeType,
					"data":     imageData,
				}}},
			},
			"finishReason": "STOP",
		}},
	}
	if strings.TrimSpace(modelName) != "" {
		response["modelVersion"] = modelName
	}
	// The RPC method is the wire-format contract. Some Antigravity builds append
	// alt=sse to the unary generateContent request even though their image tool
	// still decodes one JSON/proto response. Returning an SSE frame there starts
	// the body with "data:", which protojson reports as "invalid value data".
	// Only the explicit streaming RPC may receive an SSE envelope.
	if incoming != nil && strings.Contains(cleanPatchedPath(incoming.URL.Path), ":streamGenerateContent") {
		trace("images-downstream-response", map[string]any{
			"requestId": requestID, "format": "sse", "model": modelName,
			"mimeType": mimeType, "base64Length": len(imageData),
		})
		newDownstreamSSEWriter(w).write(encodeAntigravityStreamEvent(response, requestID))
		return
	}

	// Native image generation uses v1internal:generateContent, which is unary
	// rather than SSE. The same internal envelope is returned as JSON so the
	// Cortex generate-image handler can read inlineData from response.candidates.
	trace("images-downstream-response", map[string]any{
		"requestId": requestID, "format": "json", "model": modelName,
		"mimeType": mimeType, "base64Length": len(imageData),
	})
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(antigravityResponseEnvelope(response, requestID))
}

func writeImageGenerationHTTPError(w http.ResponseWriter, statusCode int) {
	if statusCode < http.StatusBadRequest || statusCode > 599 {
		statusCode = http.StatusBadGateway
	}
	http.Error(w, fmt.Sprintf("上游图片模型请求失败（HTTP %d）", statusCode), statusCode)
}

func directImageGenerationPrompt(gemini map[string]any) string {
	contents, _ := gemini["contents"].([]any)
	for index := len(contents) - 1; index >= 0; index-- {
		content, _ := contents[index].(map[string]any)
		role := strings.ToLower(strings.TrimSpace(getString(content, "role")))
		if role != "" && role != "user" {
			continue
		}
		parts, _ := content["parts"].([]any)
		var text strings.Builder
		for _, rawPart := range parts {
			part, _ := rawPart.(map[string]any)
			if value, ok := part["text"].(string); ok && strings.TrimSpace(value) != "" {
				if text.Len() > 0 {
					text.WriteString("\n")
				}
				text.WriteString(strings.TrimSpace(value))
			}
		}
		if prompt := sanitizeImageGenerationPrompt(text.String()); prompt != "" {
			return prompt
		}
	}
	return "Generate an image based on the user's current request."
}

// sanitizeImageGenerationPrompt deliberately operates only on the selected
// user turn. Antigravity carries MCP/rules/tool instructions in
// systemInstruction and occasionally wraps them in XML-like blocks when a
// renderer replays a turn. Those instructions are for the chat agent, not for
// an image model, and must never be forwarded as an image prompt or increase
// the image request's token/charge. Explicit user-request wrappers take
// precedence; otherwise only known instruction blocks are removed.
func sanitizeImageGenerationPrompt(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	for _, marker := range []string{"original user request:", "user request:", "user message:", "用户请求：", "用户消息："} {
		lower := strings.ToLower(value)
		if index := strings.LastIndex(lower, marker); index >= 0 {
			if candidate := strings.TrimSpace(value[index+len(marker):]); candidate != "" {
				value = candidate
				break
			}
		}
	}
	for _, block := range []struct{ open, close string }{
		{"<system", "</system>"},
		{"<developer", "</developer>"},
		{"<rules", "</rules>"},
		{"<mcp", "</mcp>"},
		{"[system]", "[/system]"},
		{"[developer]", "[/developer]"},
		{"[rules]", "[/rules]"},
		{"[mcp]", "[/mcp]"},
	} {
		value = removeDelimitedImageInstruction(value, block.open, block.close)
	}
	return trimImageGenerationPrompt(strings.TrimSpace(value))
}

func removeDelimitedImageInstruction(value, open, close string) string {
	for {
		lower := strings.ToLower(value)
		start := strings.Index(lower, strings.ToLower(open))
		if start < 0 {
			return value
		}
		closeOffset := strings.Index(lower[start+len(open):], strings.ToLower(close))
		if closeOffset < 0 {
			return strings.TrimSpace(value[:start])
		}
		end := start + len(open) + closeOffset + len(close)
		value = value[:start] + "\n" + value[end:]
	}
}

func trimImageGenerationPrompt(value string) string {
	value = strings.TrimSpace(value)
	const maximumRunes = 4096
	runes := []rune(value)
	if len(runes) > maximumRunes {
		return strings.TrimSpace(string(runes[:maximumRunes]))
	}
	return value
}

func openAIImageDataFromResponse(body []byte) (string, string, error) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", "", fmt.Errorf("图片响应不是 JSON")
	}
	for _, key := range []string{"data", "images", "output", "results"} {
		items, _ := payload[key].([]any)
		for _, rawItem := range items {
			item, _ := rawItem.(map[string]any)
			if data, mimeType, ok := imageDataFromValue(item); ok {
				return data, mimeType, nil
			}
		}
	}
	if data, mimeType, ok := imageDataFromValue(payload); ok {
		return data, mimeType, nil
	}
	return "", "", fmt.Errorf("未找到 Base64 图片数据")
}

func imageDataFromValue(value map[string]any) (string, string, bool) {
	if value == nil {
		return "", "", false
	}
	mimeType := directImageMimeType(getString(value, "mime_type", "mimeType", "content_type", "contentType", "format"))
	for _, key := range []string{"b64_json", "b64Json", "base64", "image_base64", "imageBase64", "result"} {
		raw, _ := value[key].(string)
		if strings.TrimSpace(raw) == "" {
			continue
		}
		data, detectedMime, err := normaliseAttachmentData(raw, mimeType)
		if err != nil {
			return "", "", false
		}
		if detectedMime != "" {
			mimeType = directImageMimeType(detectedMime)
		}
		return data, mimeType, true
	}
	for _, key := range []string{"url", "image_url", "imageUrl"} {
		raw, _ := value[key].(string)
		if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(raw)), "data:") {
			continue
		}
		data, detectedMime, err := normaliseAttachmentData(raw, mimeType)
		if err != nil || !strings.HasPrefix(strings.ToLower(strings.TrimSpace(detectedMime)), "image/") {
			continue
		}
		return data, directImageMimeType(detectedMime), true
	}
	return "", "", false
}

func directImageMimeType(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "image/png"
	}
	if !strings.Contains(value, "/") {
		value = "image/" + strings.TrimPrefix(value, ".")
	}
	switch value {
	case "image/png", "image/jpeg", "image/webp", "image/gif":
		return value
	case "image/jpg":
		return "image/jpeg"
	default:
		return "image/png"
	}
}
