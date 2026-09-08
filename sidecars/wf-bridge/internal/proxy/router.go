package proxy

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"antigravity-wf-assistant/internal/storage"
	"antigravity-wf-assistant/internal/upstream"
)

const (
	googleHost    = "daily-cloudcode-pa.googleapis.com"
	googleBaseURL = "https://daily-cloudcode-pa.googleapis.com"
	maxRetries    = 3
	// Antigravity 1.23.x only recognises placeholder enum values M0-M150.
	// Unknown values are decoded as MODEL_UNSPECIFIED and disappear from the
	// built-in model picker.
	modelPlaceholderCount = 151
)

// Model-list injection and generation routing share these assignments.  They
// must change together: Antigravity may send a placeholder enum while older
// UI state still uses the corresponding slug.  A failed compatibility probe
// must therefore never replace either half of the currently active mapping.
var (
	modelAssignmentsMu            sync.RWMutex
	modelInjectionTransactionMu   sync.Mutex
	allocatedPlaceholders         = map[string]string{}
	allocatedSlugs                = map[string]string{}
	nativeImageGenerationModelIDs []string
)

type modelRouteAssignments struct {
	placeholders        map[string]string
	slugs               map[string]string
	nativeImageModelIDs []string
}

func copyModelRouteAssignmentMap(source map[string]string) map[string]string {
	if len(source) == 0 {
		return map[string]string{}
	}
	copy := make(map[string]string, len(source))
	for key, value := range source {
		copy[key] = value
	}
	return copy
}

// commitModelRouteAssignments publishes a fully validated model-list mapping
// in one critical section.  Callers must not invoke it for an unknown or
// partially injected response.
func commitModelRouteAssignments(assignments modelRouteAssignments) {
	if assignments.placeholders == nil || assignments.slugs == nil {
		return
	}
	modelAssignmentsMu.Lock()
	allocatedPlaceholders = copyModelRouteAssignmentMap(assignments.placeholders)
	allocatedSlugs = copyModelRouteAssignmentMap(assignments.slugs)
	nativeImageGenerationModelIDs = append([]string(nil), assignments.nativeImageModelIDs...)
	modelAssignmentsMu.Unlock()
}

func snapshotModelRouteAssignments() modelRouteAssignments {
	modelAssignmentsMu.RLock()
	defer modelAssignmentsMu.RUnlock()
	return modelRouteAssignments{
		placeholders:        copyModelRouteAssignmentMap(allocatedPlaceholders),
		slugs:               copyModelRouteAssignmentMap(allocatedSlugs),
		nativeImageModelIDs: append([]string(nil), nativeImageGenerationModelIDs...),
	}
}

func currentNativeImageGenerationModelID() string {
	assignments := snapshotModelRouteAssignments()
	for _, modelID := range assignments.nativeImageModelIDs {
		if modelID = strings.TrimSpace(modelID); modelID != "" {
			return modelID
		}
	}
	return ""
}

// replaceModelRouteAssignmentsForTest isolates package-level routing state for
// regression tests.  It is deliberately not used by production code.
func replaceModelRouteAssignmentsForTest(assignments modelRouteAssignments) func() {
	previous := snapshotModelRouteAssignments()
	commitModelRouteAssignments(assignments)
	return func() { commitModelRouteAssignments(previous) }
}

// getModelSlug returns a stable routing slug for a model.
func getModelSlug(m storage.CustomModel) string {
	slug, _ := modelRouteFor(m, snapshotModelRouteAssignments())
	return slug
}

func baseModelSlug(m storage.CustomModel) string {
	src := m.ExternalModelName
	if src == "" {
		src = m.Name
	}
	src = strings.TrimPrefix(src, "models/")
	var b strings.Builder
	for _, r := range strings.ToLower(src) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		slug = "model"
	}
	return "custom-" + slug
}

func modelPlaceholderKey(m storage.CustomModel) string {
	if m.Name != "" {
		return m.Name
	}
	if m.ExternalModelName != "" {
		return m.ExternalModelName
	}
	return m.DisplayName
}

func modelPlaceholderHash(m storage.CustomModel) uint32 {
	src := strings.ToLower(m.DisplayName)
	if src == "" {
		src = strings.ToLower(modelPlaceholderKey(m))
	}
	var h uint32 = 5381
	for _, c := range src {
		h = (h << 5) + h + uint32(c)
	}
	return h
}

// getModelPlaceholder returns the placeholder allocated during the latest
// model-list injection, with a valid deterministic fallback for unit-level
// conversion calls.
func getModelPlaceholder(m storage.CustomModel) string {
	_, placeholder := modelRouteFor(m, snapshotModelRouteAssignments())
	return placeholder
}

// modelRouteFor resolves both identifiers from one assignment snapshot. A
// routing decision must never combine a slug from one injected picker with a
// placeholder from a later picker refresh.
func modelRouteFor(m storage.CustomModel, assignments modelRouteAssignments) (slug, placeholder string) {
	key := modelPlaceholderKey(m)
	slug = assignments.slugs[key]
	if slug == "" {
		slug = baseModelSlug(m)
	}
	placeholder = assignments.placeholders[key]
	if placeholder == "" {
		placeholder = fmt.Sprintf("MODEL_PLACEHOLDER_M%d", modelPlaceholderHash(m)%modelPlaceholderCount)
	}
	return slug, placeholder
}

// allocateModelPlaceholders selects valid enum values that do not collide with
// models already present in Google's response.  It is intentionally pure:
// assignments are published only after the complete injected response has
// passed picker validation.
func allocateModelPlaceholders(models []storage.CustomModel, officialModels map[string]any) map[string]string {
	assignments, _ := allocateModelPlaceholdersWithExisting(models, officialModels, modelRouteAssignments{})
	return assignments
}

// allocateModelPlaceholdersWithExisting preserves the live enum mapping for
// models that can still coexist with the current native response. Reassigning
// an already visible placeholder would make a stale picker select a different
// upstream model. If Google's newest response claims that enum instead, the
// caller must fail closed rather than silently pick another one.
func allocateModelPlaceholdersWithExisting(models []storage.CustomModel, officialModels map[string]any, existing modelRouteAssignments) (map[string]string, error) {
	used := make(map[string]struct{})
	for _, raw := range officialModels {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if modelID, ok := entry["model"].(string); ok && modelID != "" {
			used[modelID] = struct{}{}
		}
	}

	ordered := append([]storage.CustomModel(nil), models...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return modelPlaceholderKey(ordered[i]) < modelPlaceholderKey(ordered[j])
	})

	assignments := make(map[string]string, len(ordered))
	for _, model := range ordered {
		key := modelPlaceholderKey(model)
		placeholder := existing.placeholders[key]
		if placeholder == "" {
			continue
		}
		if !isSupportedModelPlaceholder(placeholder) {
			return nil, fmt.Errorf("已激活模型 %s 使用了无效占位符 %s", key, placeholder)
		}
		if _, collision := used[placeholder]; collision {
			return nil, fmt.Errorf("原生模型响应与已激活模型 %s 的占位符冲突", key)
		}
		assignments[key] = placeholder
		used[placeholder] = struct{}{}
	}

	for _, model := range ordered {
		key := modelPlaceholderKey(model)
		if assignments[key] != "" {
			continue
		}
		start := int(modelPlaceholderHash(model) % modelPlaceholderCount)
		for offset := 0; offset < modelPlaceholderCount; offset++ {
			candidate := fmt.Sprintf("MODEL_PLACEHOLDER_M%d", (start+offset)%modelPlaceholderCount)
			if _, exists := used[candidate]; exists {
				continue
			}
			assignments[key] = candidate
			used[candidate] = struct{}{}
			break
		}
	}

	return assignments, nil
}

func isSupportedModelPlaceholder(value string) bool {
	const prefix = "MODEL_PLACEHOLDER_M"
	if !strings.HasPrefix(value, prefix) {
		return false
	}
	index, err := strconv.Atoi(strings.TrimPrefix(value, prefix))
	return err == nil && index >= 0 && index < modelPlaceholderCount
}

// buildFakeModelEntry builds the JSON entry injected into the model list.
func buildFakeModelEntry(m storage.CustomModel, placeholder string) map[string]any {
	capabilities := storage.EffectiveCapabilities(m)
	mimeTypes := make(map[string]any, len(capabilities.SupportedMimeTypes))
	for _, mimeType := range capabilities.SupportedMimeTypes {
		mimeTypes[mimeType] = true
	}
	entry := map[string]any{
		"displayName":                  modelDisplayName(m),
		"description":                  m.Description,
		"recommended":                  true,
		"maxTokens":                    1048576,
		"maxOutputTokens":              65536,
		"tokenizerType":                "LLAMA_WITH_SPECIAL",
		"model":                        placeholder,
		"apiProvider":                  "API_PROVIDER_GOOGLE_GEMINI",
		"modelProvider":                "MODEL_PROVIDER_GOOGLE",
		"supportsCumulativeContext":    true,
		"supportsEstimateTokenCounter": true,
		"supportsImages":               capabilities.SupportsImages,
		"supportsAudio":                false,
		"supportsVideo":                false,
		"supportsFiles":                capabilities.SupportsFiles,
		"supportsToolCalls":            capabilities.SupportsToolCalls,
		"supportsThinking":             capabilities.SupportsThinking,
		"supportsWebSearch":            capabilities.SupportsWebSearch,
		"supportsImageGeneration":      capabilities.SupportsImageGeneration,
		// Newer Antigravity language servers use this ModelDetails capability
		// when they turn a native image-generation result into chat media. The
		// value must stay coupled to the real, proxy-supported image capability:
		// declaring it for an ordinary text model makes the IDE offer a tool that
		// cannot produce an attachment, while omitting it can leave a valid image
		// result visible only to the tool runner instead of the conversation.
		"requiresImageOutputOutsideFunctionResponses": capabilities.SupportsImageGeneration,
		"supportedMimeTypes":                          mimeTypes,
	}
	// The exact field names are version-dependent in Antigravity. Keep the
	// canonical fields above, and provide these aliases for IDE builds that use
	// the newer capability schema. Unknown keys are ignored by older builds.
	entry["supportsTools"] = capabilities.SupportsToolCalls
	entry["supportsFileInput"] = capabilities.SupportsFiles
	return entry
}

// handleFetchAvailableModels proxies fetchAvailableModels and injects custom models.
func handleFetchAvailableModelsLegacy(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), r.Method, googleBaseURL+"/v1internal:fetchAvailableModels", bytes.NewReader(body))
	if err != nil {
		http.Error(w, err.Error(), 502)
		return
	}
	for k, vs := range r.Header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	req.Header.Set("Host", googleHost)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, err.Error(), 502)
		return
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, err.Error(), 502)
		return
	}

	// Decompress if needed
	var decoded []byte
	enc := resp.Header.Get("Content-Encoding")
	if enc == "gzip" {
		gr, err := gzip.NewReader(bytes.NewReader(respBody))
		if err == nil {
			decoded, _ = io.ReadAll(gr)
			gr.Close()
		}
	}
	if decoded == nil {
		decoded = respBody
	}

	var parsed map[string]any
	if err := json.Unmarshal(decoded, &parsed); err != nil {
		// Forward raw on parse failure
		for k, vs := range resp.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		w.Write(respBody)
		return
	}

	models, _ := storage.LoadEnabledModels()
	injectedCount := 0
	officialCount := 0
	var injectedNames []string

	// Inject into modelMap (map shape)
	modelMapRaw, hasMap := parsed["models"]
	if hasMap {
		if modelMap, ok := modelMapRaw.(map[string]any); ok {
			officialCount = len(modelMap)
			placeholders := allocateModelPlaceholders(models, modelMap)
			for _, m := range models {
				placeholder := placeholders[modelPlaceholderKey(m)]
				if placeholder == "" {
					continue
				}
				slug := getModelSlug(m)
				modelMap[slug] = buildFakeModelEntry(m, placeholder)
				addAgentModelID(&parsed, slug)
				injectedCount++
				injectedNames = append(injectedNames, modelDisplayName(m))
			}
		}
	} else {
		// Build map from scratch if missing
		newMap := map[string]any{}
		placeholders := allocateModelPlaceholders(models, newMap)
		for _, m := range models {
			placeholder := placeholders[modelPlaceholderKey(m)]
			if placeholder == "" {
				continue
			}
			slug := getModelSlug(m)
			newMap[slug] = buildFakeModelEntry(m, placeholder)
			addAgentModelID(&parsed, slug)
			injectedCount++
			injectedNames = append(injectedNames, modelDisplayName(m))
		}
		if len(newMap) > 0 {
			parsed["models"] = newMap
		}
	}

	out, _ := json.Marshal(parsed)
	outHeaders := make(http.Header)
	for k, vs := range resp.Header {
		if strings.ToLower(k) != "content-encoding" &&
			strings.ToLower(k) != "content-length" &&
			strings.ToLower(k) != "transfer-encoding" {
			outHeaders[k] = vs
		}
	}
	outHeaders.Set("Content-Type", "application/json; charset=utf-8")
	for k, vs := range outHeaders {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	w.Write(out)

	trace("models-injected", map[string]any{
		"officialCount": officialCount,
		"customCount":   injectedCount,
		"customNames":   injectedNames,
	})
}

func addAgentModelID(parsed *map[string]any, modelID string) {
	sorts, _ := (*parsed)["agentModelSorts"].([]any)
	if len(sorts) == 0 {
		sorts = []any{map[string]any{
			"displayName": "Custom",
			"groups":      []any{map[string]any{"modelIds": []any{}}},
		}}
	}
	sort0, ok := sorts[0].(map[string]any)
	if !ok {
		return
	}
	groups, _ := sort0["groups"].([]any)
	if len(groups) == 0 {
		groups = []any{map[string]any{"modelIds": []any{}}}
	}
	group0, ok := groups[0].(map[string]any)
	if !ok {
		return
	}
	ids, _ := group0["modelIds"].([]any)
	for _, id := range ids {
		if id == modelID {
			return
		}
	}
	group0["modelIds"] = append([]any{modelID}, ids...)
	groups[0] = group0
	sort0["groups"] = groups
	sorts[0] = sort0
	(*parsed)["agentModelSorts"] = sorts
}

// findModel returns the custom model matching a model ID or placeholder.
func findModel(modelID string) *storage.CustomModel {
	models, _ := storage.LoadEnabledModels()
	assignments := snapshotModelRouteAssignments()
	for _, m := range models {
		slug, placeholder := modelRouteFor(m, assignments)
		if modelID == m.Name || modelID == m.ExternalModelName ||
			modelID == slug || modelID == "models/"+slug || modelID == placeholder ||
			modelID == "models/"+placeholder ||
			modelID == strings.TrimPrefix(m.Name, "models/") {
			mc := m
			return &mc
		}
	}
	return nil
}

// resolveGenerationModel keeps an image source only for the internal image
// subrequest that follows a compatible custom-model turn. A normal native
// agent turn is an explicit model switch for that trajectory, so it clears a
// previously remembered custom source before being passed through to Gemini.
func resolveGenerationModel(modelID, requestID string) (customModel *storage.CustomModel, customMatched, nativeImageSource bool) {
	// The image tool's model field is not the user's selected chat model. In
	// Antigravity 2.6 it can even contain the first helper-injected image-capable
	// slug. The trajectory's preceding agent turn is the only authoritative
	// source: a remembered custom turn routes to the preferred enabled custom
	// image model; a native/Gemini turn has no source and remains Google-native.
	if isNativeImageGenerationRequestID(requestID) {
		if source := imageGenerationSourceForRequest(requestID); source != nil {
			return source, false, true
		}
		return nil, false, false
	}
	customModel = findModel(modelID)
	customMatched = customModel != nil
	if customMatched {
		return customModel, true, false
	}
	forgetImageGenerationSource(requestID)
	return nil, false, false
}

// restoreNativeImageGenerationRequestModel repairs a stale picker response
// from an older helper build. Those builds prepended custom slugs to Google's
// global imageGenerationModelIds list, so after switching to Gemini the native
// image tool could still send a custom slug. With no remembered custom source,
// rewrite only that outer routing ID to the native image model captured from
// Google's unmodified list. No prompt, media, tool or credential field changes.
func restoreNativeImageGenerationRequestModel(req map[string]any, modelID, requestID string) (string, bool, error) {
	if !isNativeImageGenerationRequestID(requestID) || findModel(modelID) == nil {
		return modelID, false, nil
	}
	nativeModelID := currentNativeImageGenerationModelID()
	if nativeModelID == "" {
		return modelID, false, fmt.Errorf("原生 Gemini 图片模型目录尚未刷新；请完全退出并重新打开 Antigravity 后重试")
	}
	if _, exists := req["model"]; exists {
		req["model"] = nativeModelID
	}
	if _, exists := req["modelId"]; exists {
		req["modelId"] = nativeModelID
	}
	return nativeModelID, true, nil
}

// handleGenerate routes a streamGenerateContent request. cleanPath is the
// already-normalised path, with any patcher prefix removed.
func handleGenerate(w http.ResponseWriter, r *http.Request, cleanPath string) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "invalid json", 400)
		return
	}

	modelID, _ := req["model"].(string)
	if modelID == "" {
		if mid, ok := req["modelId"].(string); ok {
			modelID = mid
		}
	}

	requestID, _ := req["requestId"].(string)
	generationRequestID := requestID
	if requestID == "" {
		requestID = fmt.Sprintf("%d", time.Now().UnixNano())
	}
	customModel, customMatched, nativeImageSource := resolveGenerationModel(modelID, requestID)

	trace("generation-request", map[string]any{
		"requestId":         requestID,
		"model":             modelID,
		"customMatched":     customMatched,
		"nativeImageSource": nativeImageSource,
	})

	if customModel == nil {
		if restoredModelID, restored, restoreErr := restoreNativeImageGenerationRequestModel(req, modelID, requestID); restoreErr != nil {
			http.Error(w, restoreErr.Error(), http.StatusConflict)
			return
		} else if restored {
			body, err = json.Marshal(req)
			if err != nil {
				http.Error(w, "恢复原生 Gemini 图片路由失败", http.StatusBadRequest)
				return
			}
			trace("native-image-model-restored", map[string]any{
				"requestId": requestID, "staleModel": modelID, "nativeModel": restoredModelID,
			})
		}
		// Passthrough to Google
		passthroughRequest(w, r, body, cleanPath)
		return
	}

	geminiReq, _ := req["request"].(map[string]any)
	if geminiReq == nil {
		geminiReq = req
	}
	if nativeImageSource {
		// The remembered custom agent turn keeps this internal image tool request
		// on the dedicated custom Images route. A Gemini turn never reaches this
		// branch and remains on Google's native image path.
		geminiReq["wfNativeImageGeneration"] = true
	} else {
		rememberImageGenerationSource(requestID, customModel)
	}
	// The IDE can address the same saved custom model through its display name,
	// slug or placeholder. Guard the canonical saved name so an overlapping
	// retry using another alias cannot create a second upstream generation.
	guardModelID := modelID
	if customModel.Name != "" {
		guardModelID = customModel.Name
	} else if customModel.ExternalModelName != "" {
		guardModelID = customModel.ExternalModelName
	}
	releaseGeneration, accepted := reserveGeneration(guardModelID, generationRequestID, geminiReq)
	if !accepted {
		trace("generation-duplicate-suppressed", map[string]any{
			"requestId": requestID, "model": modelID,
		})
		http.Error(w, "相同请求仍在处理中，已阻止重复上游调用", http.StatusConflict)
		return
	}
	defer releaseGeneration()

	if customModel.Provider == "anthropic" {
		forwardAnthropic(w, r, customModel, geminiReq, requestID)
	} else {
		forwardOpenAI(w, r, customModel, geminiReq, requestID)
	}
}

// forwardOpenAI chooses the configured API surface. Antigravity's ordinary
// OpenAI-compatible contract is Chat Completions, so both automatic and Chat
// modes stay on Chat for text, vision and function tools. Responses is used
// only when the user explicitly selected it or the credential is Codex OAuth.
// Dedicated image generation is intercepted above the Chat/Responses choice
// and continues to use the configured Images API.
func forwardOpenAI(w http.ResponseWriter, incoming *http.Request, m *storage.CustomModel, geminiReq map[string]any, requestID string) {
	// A Codex OAuth access token is valid only against ChatGPT's Responses
	// surface. Account-pool metadata is copied into the selected model later,
	// so inspect the model binding before choosing the initial route too.
	if isOpenAICodexOAuthModel(m) {
		forwardOpenAIResponses(w, incoming, m, geminiReq, requestID, false, nil)
		return
	}
	// Route an explicit image-generation turn to the preferred enabled image
	// model. The current supplier is preferred; if it has no image model, an
	// independently configured image supplier provides its own endpoint and
	// credentials. A directly selected image-only model always uses this route.
	directImageRequest := requestsDirectImageGeneration(geminiReq)
	directImageModelSelected := isDirectImageModelName(m.ExternalModelName)
	if directImageRequest || directImageModelSelected {
		imageModel := directOpenAIImageModel(m)
		if directImageModelSelected {
			imageModel = m
		}
		if imageModel != nil {
			forwardOpenAIImagesGeneration(w, incoming, m, imageModel, geminiReq, requestID)
			return
		}
		if directImageModelSelected {
			http.Error(w, "当前图片模型没有可用的 Images API 配置", http.StatusBadRequest)
			return
		}
	}
	config := upstream.ConfigFromModel(*m)
	style := upstream.EffectiveAPIStyle(config)
	if style == "responses" {
		forwardOpenAIResponses(w, incoming, m, geminiReq, requestID, false, nil)
		return
	}
	forwardOpenAIChat(w, incoming, m, geminiReq, requestID)
}

// requestedResponsesBuiltinTools identifies a concrete request to invoke a
// hosted Responses tool. It intentionally does not infer intent from a model
// capability or user prompt text: capabilities are only advertised to the
// IDE, while every outbound hosted tool must be requested by this turn.
func requestedResponsesBuiltinTools(gemini map[string]any) map[string]struct{} {
	requested := make(map[string]struct{}, 2)
	collectRequestedResponsesBuiltinTools(gemini, requested)
	for _, key := range []string{"generationConfig", "toolConfig", "responseConfig", "wfConfig"} {
		if config, ok := gemini[key].(map[string]any); ok {
			collectRequestedResponsesBuiltinTools(config, requested)
		}
	}
	collectRequestedNativeResponsesTools(gemini["tools"], requested)
	return requested
}

func collectRequestedResponsesBuiltinTools(config map[string]any, requested map[string]struct{}) {
	for key, value := range config {
		switch normalisedResponsesFeatureKey(key) {
		case "websearch", "websearchretrieval", "googlesearch", "googlesearchretrieval", "urlcontext":
			if responseFeatureEnabled(value) {
				requested[responseWebSearchTool] = struct{}{}
			}
		case "imagegeneration", "imagegenerationconfig", "imagegen", "generateimage", "wfnativeimagegeneration":
			if responseFeatureEnabled(value) {
				requested[responseImageGenerationTool] = struct{}{}
			}
		case "responsemodalities", "modalities":
			if responseOutputMediaRequested(value) {
				requested[responseImageGenerationTool] = struct{}{}
			}
		}
	}
}

func collectRequestedNativeResponsesTools(raw any, requested map[string]struct{}) {
	tools, _ := raw.([]any)
	for _, rawTool := range tools {
		tool, _ := rawTool.(map[string]any)
		if tool == nil {
			continue
		}
		collectRequestedResponsesBuiltinTools(tool, requested)
		switch normalisedResponsesFeatureKey(getString(tool, "type")) {
		case "websearch", "websearchpreview", "websearchretrieval", "googlesearch", "googlesearchretrieval", "urlcontext":
			requested[responseWebSearchTool] = struct{}{}
		case "imagegeneration", "imagegen", "generateimage":
			requested[responseImageGenerationTool] = struct{}{}
		}
	}
}

// explicitResponsesFeatureMap accepts the small set of explicit feature
// fields emitted by native clients and our own local integration. It does not
// inspect free text or function declaration names, so a normal IDE terminal
// tool schema still uses the lower-cost Chat Completions path.
func explicitResponsesFeatureMap(config map[string]any) bool {
	for key, value := range config {
		switch normalisedResponsesFeatureKey(key) {
		case "wfuseresponses", "useresponses", "responsesapi", "responseapi":
			if responseFeatureEnabled(value) {
				return true
			}
		case "websearch", "websearchretrieval", "googlesearch", "googlesearchretrieval", "urlcontext",
			"imagegeneration", "imagegenerationconfig", "imagegen", "generateimage", "wfnativeimagegeneration":
			if responseFeatureEnabled(value) {
				return true
			}
		case "responsemodalities", "modalities":
			if responseOutputMediaRequested(value) {
				return true
			}
		}
	}
	return false
}

func nativeResponsesToolRequested(raw any) bool {
	requested := make(map[string]struct{}, 2)
	collectRequestedNativeResponsesTools(raw, requested)
	return len(requested) > 0
}

func normalisedResponsesFeatureKey(key string) string {
	key = strings.ToLower(strings.TrimSpace(key))
	key = strings.NewReplacer("_", "", "-", "", " ", "").Replace(key)
	return key
}

func isNativeResponsesToolType(kind string) bool {
	switch normalisedResponsesFeatureKey(kind) {
	case "websearch", "websearchpreview", "websearchretrieval", "googlesearch", "googlesearchretrieval",
		"urlcontext", "imagegeneration", "imagegen", "generateimage":
		return true
	default:
		return false
	}
}

func responseFeatureEnabled(value any) bool {
	switch value := value.(type) {
	case nil:
		return false
	case bool:
		return value
	case string:
		value = strings.ToLower(strings.TrimSpace(value))
		return value != "" && value != "false" && value != "none" && value != "disabled" && value != "off"
	case []any:
		return len(value) > 0
	case map[string]any:
		// Native Gemini tool declarations commonly use an empty object, for
		// example {"googleSearch": {}}. The field's presence is the request.
		return true
	default:
		return true
	}
}

func responseOutputMediaRequested(value any) bool {
	var visit func(any) bool
	visit = func(current any) bool {
		switch current := current.(type) {
		case string:
			return strings.Contains(strings.ToLower(current), "image")
		case []any:
			for _, item := range current {
				if visit(item) {
					return true
				}
			}
		case map[string]any:
			for _, item := range current {
				if visit(item) {
					return true
				}
			}
		}
		return false
	}
	return visit(value)
}

func forwardOpenAIChat(w http.ResponseWriter, incoming *http.Request, m *storage.CustomModel, geminiReq map[string]any, requestID string) {
	baseRequest, err := toOpenAIRequestWithMedia(geminiReq, m.ExternalModelName)
	if err != nil {
		trace("openai-input-error", map[string]any{"requestId": requestID, "message": err.Error()})
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	applyOpenAIChatReasoning(baseRequest, m)
	baseRequest["stream"] = true
	baseRequest["stream_options"] = map[string]any{"include_usage": true}
	cache := applyOpenAIPromptCaching(baseRequest, m, geminiReq)
	cacheEnabled := cache.enabled

	policy := currentStreamRecoveryPolicy()
	writer := newDownstreamSSEWriter(w)
	client := &http.Client{Timeout: upstreamStreamTimeout}
	requestBody := baseRequest
	lastModelVersion, lastResponseID := "", ""
	reconnects := 0
	cacheFallbackUsed := false
	excludedAccounts := map[string]struct{}{}
	pinnedAccountID := ""
	lastRejectedStatus := 0
	lastRejectedBody := ""

	for attempt := 1; ; attempt++ {
		attemptModel, lease, err := acquireAttemptModel(m, excludedAccounts, pinnedAccountID)
		if err != nil {
			trace("openai-account-pool-error", map[string]any{"requestId": requestID, "attempt": attempt, "message": err.Error()})
			if lastRejectedStatus != 0 && !writer.committed {
				if isRetryableStatus(lastRejectedStatus) || isTransientProviderRejection(lastRejectedStatus, lastRejectedBody) || isTransientModelPoolRejection(lastRejectedStatus, lastRejectedBody) {
					writeRecoverableTurnStop(writer, "openai", requestID, lastModelVersion, "当前账户的第三方上游暂时不可用，本轮未生成内容。请稍后再发送一次；新请求会重新探测该账户。", reconnects)
				} else {
					writeRejectedTurnStop(writer, "openai", requestID, lastModelVersion, lastRejectedStatus, lastRejectedBody)
				}
				return
			}
			reconnects++
			if waitForAccountPool(incoming.Context(), writer, policy, "openai", requestID, err, reconnects) {
				continue
			}
			if _, temporary := storage.AccountPoolRetryAfter(err); temporary && !writer.committed {
				writeRecoverableTurnStop(writer, "openai", requestID, lastModelVersion, "当前账户仍在处理其他请求，本轮未生成内容。请稍后再发送一次；新请求会重新探测该账户。", reconnects)
				return
			}
			if writer.committed {
				writeRecoveredStreamStop(writer, requestID, lastModelVersion, lastResponseID, reconnects, false)
			} else {
				http.Error(w, accountPoolError("OpenAI", err), http.StatusServiceUnavailable)
			}
			return
		}
		if pinnedAccountID == "" && lease != nil {
			pinnedAccountID = lease.ID
		}
		attemptConfig := upstream.ConfigFromModel(*attemptModel)
		if upstream.IsOpenAICodexOAuth(attemptConfig) {
			// Account metadata can change after forwardOpenAI made its routing
			// decision. This second guard is intentionally immediately before
			// URL/credential construction: a Codex OAuth token must never reach
			// a Chat Completions endpoint.
			pinnedModel := *attemptModel
			if lease != nil && strings.TrimSpace(lease.ID) != "" {
				pinnedModel.AccountIDs = []string{lease.ID}
			}
			releaseAttemptSuccess(lease)
			forwardOpenAIResponses(w, incoming, &pinnedModel, geminiReq, requestID, false, nil)
			return
		}
		apiURL, err := upstream.ResolveChatCompletionsURLForConfig(attemptConfig)
		if err != nil {
			releaseAttemptSuccess(lease)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		body, _ := json.Marshal(requestBody)
		trace("openai-upstream-request", map[string]any{
			"requestId": requestID, "attempt": attempt,
			"promptCache": cacheEnabled, "promptCacheExplicit": cacheEnabled && cache.explicit,
			"promptCacheKeyHash": strings.TrimPrefix(cache.key, "antigravity:"),
			"accountId": func() string {
				if lease == nil {
					return ""
				}
				return lease.ID
			}(),
		})
		req, err := http.NewRequestWithContext(incoming.Context(), "POST", apiURL, bytes.NewReader(body))
		if err != nil {
			releaseAttemptSuccess(lease)
			http.Error(w, err.Error(), 502)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		if err := upstream.ApplyCredentials(req, attemptConfig); err != nil {
			releaseAttemptSuccess(lease)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		req, writeTrace := traceUpstreamRequestWrite(req)
		resp, err := client.Do(req)
		if err != nil {
			trace("openai-upstream-error", map[string]any{"requestId": requestID, "attempt": attempt, "message": err.Error()})
			if incoming.Context().Err() != nil {
				releaseAttemptFailure(lease, 0, "", err.Error())
				return
			}
			reconnects++
			if canRetryUnsentTransportFailure(writer, policy, reconnects, writeTrace) {
				if lease != nil {
					lease.Release()
				}
				if waitForRejectedRequestRetry(incoming.Context(), policy, "openai", requestID, "connection-before-write", "", reconnects) {
					continue
				}
			}
			releaseAttemptFailure(lease, 0, "", err.Error())
			if writer.committed {
				writeUncertainUpstreamFailure(writer, "openai", requestID, lastModelVersion, lastResponseID, reconnects, false, "无法确认上游是否已接收请求："+err.Error())
			} else {
				writeRecoverableTurnStop(writer, "openai", requestID, lastModelVersion, "上游网络连接在请求送出后中断，本轮结果无法确认；这不是生图频率或张数限制，可以立即手动重新发送。XIASS Tools 未自动重放可能已送达的请求，以避免重复扣费或重复执行工具。", reconnects)
			}
			return
		}
		observeAttemptQuota(lease, "openai", resp)

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			errBody, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			retryAfter := resp.Header.Get("Retry-After")
			trace("openai-upstream-error-response", map[string]any{
				"requestId":  requestID,
				"statusCode": resp.StatusCode,
				"body":       string(errBody[:min(len(errBody), 500)]),
			})
			if cacheEnabled && !cacheFallbackUsed && !writer.committed && isUnsupportedCacheResponse(resp.StatusCode, string(errBody)) {
				releaseAttemptSuccess(lease)
				rememberUnsupportedPromptCache("openai", m)
				stripOpenAIPromptCaching(baseRequest)
				cacheEnabled = false
				cacheFallbackUsed = true
				trace("prompt-cache-fallback", map[string]any{
					"requestId": requestID, "provider": "openai", "statusCode": resp.StatusCode,
				})
				requestBody = baseRequest
				continue
			}
			if shouldFailOverAccount(lease, resp.StatusCode, string(errBody)) {
				retrySameAccount := shouldRetrySameAccount(lease, resp.StatusCode, string(errBody))
				lastRejectedStatus, lastRejectedBody = resp.StatusCode, string(errBody)
				reconnects++
				mayRetry := canRetryRejectedRequest(writer, policy, reconnects)
				if mayRetry && retrySameAccount {
					if lease != nil {
						lease.Release()
					}
				} else {
					releaseAttemptFailure(lease, resp.StatusCode, retryAfter, string(errBody))
					if !retrySameAccount {
						excludeFailedAttempt(excludedAccounts, lease)
					}
				}
				if mayRetry && waitForRejectedRequestRetry(incoming.Context(), policy, "openai", requestID, fmt.Sprintf("http-%d", resp.StatusCode), rejectedRetryAfter(resp.StatusCode, string(errBody), retryAfter, reconnects), reconnects) {
					requestBody = baseRequest
					continue
				}
				if incoming.Context().Err() != nil {
					return
				}
				if isRetryableStatus(resp.StatusCode) || isTransientProviderRejection(resp.StatusCode, string(errBody)) || isTransientModelPoolRejection(resp.StatusCode, string(errBody)) {
					writeRecoverableTurnStop(writer, "openai", requestID, lastModelVersion, "当前账户的第三方上游暂时没有可用线路，本轮未生成内容。请稍后再发送一次；新请求会重新探测该账户。", reconnects)
					return
				}
				returnRejectedUpstreamError(w, resp.StatusCode, errBody)
				return
			}
			releaseAttemptFailure(lease, resp.StatusCode, retryAfter, string(errBody))
			if incoming.Context().Err() != nil {
				return
			}
			if writer.committed {
				writeRecoveredStreamStop(writer, requestID, lastModelVersion, lastResponseID, reconnects, false)
				return
			}
			if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden || (resp.StatusCode == http.StatusNotFound && isModelRouteRejection(string(errBody))) {
				writeRejectedTurnStop(writer, "openai", requestID, lastModelVersion, resp.StatusCode, string(errBody))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(resp.StatusCode)
			_, _ = w.Write(errBody)
			return
		}

		outcome := streamOpenAIAttempt(writer, resp, requestID, attempt, accountUsageTraceForAttempt(lease, attemptModel, "openai", attempt))
		resp.Body.Close()
		if outcome.responseID != "" {
			lastResponseID = outcome.responseID
		}
		if outcome.modelVersion != "" {
			lastModelVersion = outcome.modelVersion
		}
		if outcome.finished {
			releaseAttemptSuccess(lease)
			return
		}
		reason := "incomplete-stream"
		if outcome.err != nil {
			reason = outcome.err.Error()
		}
		releaseAttemptFailure(lease, 0, "", reason)
		excludeFailedAttempt(excludedAccounts, lease)
		if incoming.Context().Err() != nil {
			return
		}
		writeUncertainUpstreamFailure(writer, "openai", requestID, lastModelVersion, lastResponseID, reconnects, outcome.unsafeOutput, "上游流未正常完成："+reason)
		return
	}
}

// forwardOpenAIResponses returns true only when the caller may retry the same
// request through Chat Completions because the upstream does not expose
// /responses. It never falls back after a semantic 4xx, which would hide a
// model capability/configuration error from the user.
func forwardOpenAIResponses(w http.ResponseWriter, incoming *http.Request, m *storage.CustomModel, geminiReq map[string]any, requestID string, allowFallback bool, fallbackAccountID *string) bool {
	conversionModel := openAICodexResponsesConversionModel(m)
	baseRequest, err := toOpenAIResponsesRequest(geminiReq, m.ExternalModelName, conversionModel)
	if err != nil {
		trace("responses-input-error", map[string]any{"requestId": requestID, "message": err.Error()})
		http.Error(w, err.Error(), http.StatusBadRequest)
		return false
	}
	applyOpenAIResponsesReasoning(baseRequest, m)
	policy := currentStreamRecoveryPolicy()
	writer := newDownstreamSSEWriter(w)
	client := &http.Client{Timeout: upstreamStreamTimeout}
	lastModelVersion, lastResponseID := "", ""
	reconnects := 0
	excludedAccounts := map[string]struct{}{}
	pinnedAccountID := ""
	lastRejectedStatus := 0
	lastRejectedBody := ""

	for attempt := 1; ; attempt++ {
		attemptModel, lease, err := acquireAttemptModel(m, excludedAccounts, pinnedAccountID)
		if err != nil {
			trace("responses-account-pool-error", map[string]any{"requestId": requestID, "attempt": attempt, "message": err.Error()})
			if lastRejectedStatus != 0 && !writer.committed {
				if isRetryableStatus(lastRejectedStatus) || isTransientProviderRejection(lastRejectedStatus, lastRejectedBody) || isTransientModelPoolRejection(lastRejectedStatus, lastRejectedBody) {
					writeRecoverableTurnStop(writer, "responses", requestID, lastModelVersion, "当前账户的第三方上游暂时不可用，本轮未生成内容。请稍后再发送一次；新请求会重新探测该账户。", reconnects)
				} else {
					writeRejectedTurnStop(writer, "responses", requestID, lastModelVersion, lastRejectedStatus, lastRejectedBody)
				}
				return false
			}
			reconnects++
			if waitForAccountPool(incoming.Context(), writer, policy, "responses", requestID, err, reconnects) {
				continue
			}
			if _, temporary := storage.AccountPoolRetryAfter(err); temporary && !writer.committed {
				writeRecoverableTurnStop(writer, "responses", requestID, lastModelVersion, "当前账户仍在处理其他请求，本轮未生成内容。请稍后再发送一次；新请求会重新探测该账户。", reconnects)
				return false
			}
			if writer.committed {
				writeRecoveredStreamStop(writer, requestID, lastModelVersion, lastResponseID, reconnects, false)
			} else {
				http.Error(w, accountPoolError("OpenAI", err), http.StatusServiceUnavailable)
			}
			return false
		}
		if pinnedAccountID == "" && lease != nil {
			pinnedAccountID = lease.ID
		}
		attemptConfig := upstream.ConfigFromModel(*attemptModel)
		codexOAuth := upstream.IsOpenAICodexOAuth(attemptConfig)
		if codexOAuth {
			// Never send a Codex access token to Chat Completions, even if this
			// model entered through generic automatic routing.
			allowFallback = false
		}
		apiURL, err := upstream.ResolveResponsesURLForConfig(attemptConfig)
		if err != nil {
			releaseAttemptSuccess(lease)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return false
		}
		// Compatibility is scoped to the selected account/endpoint. A pooled
		// account which rejects hosted tools must not disable them for another
		// account that may support them.
		requestBody, suppressedBuiltinTools := responseRequestForModel(baseRequest, attemptModel)
		if codexOAuth {
			// Preserve every image and tool requested by Antigravity. Codex has
			// its own Responses contract, rather than the gateway compatibility
			// cache used for generic OpenAI-compatible endpoints.
			requestBody = normalizeOpenAICodexResponsesRequest(baseRequest)
			suppressedBuiltinTools = nil
		}
		if len(suppressedBuiltinTools) > 0 {
			trace("responses-builtin-tools-suppressed", map[string]any{
				"requestId": requestID, "tools": suppressedBuiltinTools,
			})
		}
		body, _ := json.Marshal(requestBody)
		trace("responses-upstream-request", map[string]any{"requestId": requestID, "attempt": attempt, "accountId": func() string {
			if lease == nil {
				return ""
			}
			return lease.ID
		}()})
		req, err := http.NewRequestWithContext(incoming.Context(), http.MethodPost, apiURL, bytes.NewReader(body))
		if err != nil {
			releaseAttemptSuccess(lease)
			http.Error(w, err.Error(), http.StatusBadGateway)
			return false
		}
		req.Header.Set("Content-Type", "application/json")
		if err := upstream.ApplyCredentials(req, attemptConfig); err != nil {
			releaseAttemptSuccess(lease)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return false
		}
		req, writeTrace := traceUpstreamRequestWrite(req)
		resp, err := client.Do(req)
		if err != nil {
			trace("responses-upstream-error", map[string]any{"requestId": requestID, "attempt": attempt, "message": err.Error()})
			if incoming.Context().Err() != nil {
				releaseAttemptFailure(lease, 0, "", err.Error())
				return false
			}
			reconnects++
			if canRetryUnsentTransportFailure(writer, policy, reconnects, writeTrace) {
				if lease != nil {
					lease.Release()
				}
				if waitForRejectedRequestRetry(incoming.Context(), policy, "responses", requestID, "connection-before-write", "", reconnects) {
					continue
				}
			}
			releaseAttemptFailure(lease, 0, "", err.Error())
			if writer.committed {
				writeUncertainUpstreamFailure(writer, "responses", requestID, lastModelVersion, lastResponseID, reconnects, false, "无法确认上游是否已接收请求："+err.Error())
			} else {
				writeRecoverableTurnStop(writer, "responses", requestID, lastModelVersion, "上游网络连接在请求送出后中断，本轮结果无法确认；这不是生图频率或张数限制，可以立即手动重新发送。XIASS Tools 未自动重放可能已送达的请求，以避免重复扣费或重复执行工具。", reconnects)
			}
			return false
		}
		observeAttemptQuota(lease, "responses", resp)
		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			errBody, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			retryAfter := resp.Header.Get("Retry-After")
			trace("responses-upstream-error-response", map[string]any{"requestId": requestID, "statusCode": resp.StatusCode, "body": string(errBody[:min(len(errBody), 500)])})
			if !codexOAuth {
				if rejectedTools := rejectedResponsesBuiltinTools(resp.StatusCode, string(errBody), requestBody); len(rejectedTools) > 0 && !writer.committed {
					// A concrete 4xx validation response proves this request did not
					// begin a generation. It is therefore safe to retry once with
					// only the rejected optional hosted tools removed.
					releaseAttemptSuccess(lease)
					rememberUnsupportedResponsesBuiltinTools(attemptModel, rejectedTools)
					trace("responses-builtin-tools-fallback", map[string]any{
						"requestId": requestID, "statusCode": resp.StatusCode, "tools": responseBuiltinToolNames(rejectedTools),
					})
					continue
				}
			}
			if allowFallback && !writer.committed && upstream.CanFallbackToChatResponse(resp.StatusCode, string(errBody)) {
				releaseAttemptSuccess(lease)
				if fallbackAccountID != nil {
					*fallbackAccountID = pinnedAccountID
				}
				trace("responses-chat-fallback", map[string]any{"requestId": requestID, "statusCode": resp.StatusCode})
				return true
			}
			if shouldFailOverAccount(lease, resp.StatusCode, string(errBody)) {
				retrySameAccount := shouldRetrySameAccount(lease, resp.StatusCode, string(errBody))
				lastRejectedStatus, lastRejectedBody = resp.StatusCode, string(errBody)
				reconnects++
				mayRetry := canRetryRejectedRequest(writer, policy, reconnects)
				if mayRetry && retrySameAccount {
					if lease != nil {
						lease.Release()
					}
				} else {
					releaseAttemptFailure(lease, resp.StatusCode, retryAfter, string(errBody))
					if !retrySameAccount {
						excludeFailedAttempt(excludedAccounts, lease)
					}
				}
				if mayRetry && waitForRejectedRequestRetry(incoming.Context(), policy, "responses", requestID, fmt.Sprintf("http-%d", resp.StatusCode), rejectedRetryAfter(resp.StatusCode, string(errBody), retryAfter, reconnects), reconnects) {
					continue
				}
				if incoming.Context().Err() != nil {
					return false
				}
				if isRetryableStatus(resp.StatusCode) || isTransientProviderRejection(resp.StatusCode, string(errBody)) || isTransientModelPoolRejection(resp.StatusCode, string(errBody)) {
					writeRecoverableTurnStop(writer, "responses", requestID, lastModelVersion, "当前账户的第三方上游暂时没有可用线路，本轮未生成内容。请稍后再发送一次；新请求会重新探测该账户。", reconnects)
					return false
				}
				returnRejectedUpstreamError(w, resp.StatusCode, errBody)
				return false
			}
			releaseAttemptFailure(lease, resp.StatusCode, retryAfter, string(errBody))
			if incoming.Context().Err() != nil {
				return false
			}
			if writer.committed {
				writeRecoveredStreamStop(writer, requestID, lastModelVersion, lastResponseID, reconnects, false)
				return false
			}
			if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden || (resp.StatusCode == http.StatusNotFound && isModelRouteRejection(string(errBody))) {
				writeRejectedTurnStop(writer, "responses", requestID, lastModelVersion, resp.StatusCode, string(errBody))
				return false
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(resp.StatusCode)
			_, _ = w.Write(errBody)
			return false
		}

		outcome := streamOpenAIResponsesAttempt(writer, resp, requestID, attempt, accountUsageTraceForAttempt(lease, attemptModel, "responses", attempt))
		resp.Body.Close()
		if outcome.responseID != "" {
			lastResponseID = outcome.responseID
		}
		if outcome.modelVersion != "" {
			lastModelVersion = outcome.modelVersion
		}
		if outcome.finished {
			releaseAttemptSuccess(lease)
			return false
		}
		reason := "incomplete-stream"
		if outcome.err != nil {
			reason = outcome.err.Error()
		}
		releaseAttemptFailure(lease, 0, "", reason)
		excludeFailedAttempt(excludedAccounts, lease)
		if incoming.Context().Err() != nil {
			return false
		}
		writeUncertainUpstreamFailure(writer, "responses", requestID, lastModelVersion, lastResponseID, reconnects, outcome.unsafeOutput, "上游流未正常完成："+reason)
		return false
	}
}

func responseReasoningEffort(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "minimal", "low", "medium", "high":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func resolveOpenAIChatCompletionsURL(rawURL string) string {
	trimmed := strings.TrimSpace(rawURL)
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		base := strings.TrimRight(trimmed, "/")
		if strings.HasSuffix(base, "/chat/completions") {
			return base
		}
		if strings.HasSuffix(base, "/v1") {
			return base + "/chat/completions"
		}
		return base + "/v1/chat/completions"
	}

	path := strings.TrimRight(parsed.Path, "/")
	if strings.HasSuffix(path, "/chat/completions") {
		parsed.Path = path
		return parsed.String()
	}
	if path == "" {
		parsed.Path = "/v1/chat/completions"
	} else {
		parsed.Path = path + "/chat/completions"
	}
	return parsed.String()
}

// forwardAnthropic translates and forwards to Anthropic Messages API.
func forwardAnthropicLegacy(w http.ResponseWriter, incoming *http.Request, m *storage.CustomModel, geminiReq map[string]any, requestID string) {
	anthReq, err := toAnthropicRequestWithMedia(geminiReq, m.ExternalModelName)
	if err != nil {
		trace("anthropic-input-error", map[string]any{"requestId": requestID, "message": err.Error()})
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	applyAnthropicReasoning(anthReq, m)
	breakpointCount := applyAnthropicPromptCachingForModel(anthReq, m)
	cacheEnabled := breakpointCount > 0

	apiURL, err := upstream.ResolveAnthropicMessagesURLForConfig(upstream.ConfigFromModel(*m))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	client := &http.Client{Timeout: 5 * time.Minute}
	var lastErr error
	extraAttempts := 0
	for attempt := 1; attempt <= maxRetries+extraAttempts; attempt++ {
		body, _ := json.Marshal(anthReq)
		trace("anthropic-upstream-request", map[string]any{
			"requestId": requestID, "attempt": attempt,
			"promptCache": cacheEnabled, "promptCacheBreakpoints": func() int {
				if cacheEnabled {
					return breakpointCount
				}
				return 0
			}(),
		})
		req, err := http.NewRequestWithContext(incoming.Context(), "POST", apiURL, bytes.NewReader(body))
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		if err := upstream.ApplyCredentials(req, upstream.ConfigFromModel(*m)); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			trace("anthropic-upstream-error", map[string]any{"requestId": requestID, "attempt": attempt, "message": err.Error()})
			if attempt < maxRetries+extraAttempts && incoming.Context().Err() == nil {
				delay := retryDelay(attempt, "")
				trace("anthropic-upstream-retry", map[string]any{"requestId": requestID, "attempt": attempt, "delayMs": delay.Milliseconds()})
				time.Sleep(delay)
				continue
			}
			break
		}
		lastErr = nil

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			errBody, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			trace("anthropic-upstream-error-response", map[string]any{
				"requestId": requestID, "statusCode": resp.StatusCode,
				"body": string(errBody[:min(len(errBody), 500)]),
			})
			if cacheEnabled && isUnsupportedCacheResponse(resp.StatusCode, string(errBody)) {
				rememberUnsupportedPromptCache("anthropic", m)
				stripAnthropicPromptCaching(anthReq)
				cacheEnabled = false
				extraAttempts = 1
				trace("prompt-cache-fallback", map[string]any{
					"requestId": requestID, "provider": "anthropic", "statusCode": resp.StatusCode,
				})
				continue
			}
			if isRetryableStatus(resp.StatusCode) && attempt < maxRetries+extraAttempts {
				delay := retryDelay(attempt, resp.Header.Get("Retry-After"))
				trace("anthropic-upstream-retry", map[string]any{"requestId": requestID, "attempt": attempt, "statusCode": resp.StatusCode, "delayMs": delay.Milliseconds()})
				time.Sleep(delay)
				continue
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(resp.StatusCode)
			w.Write(errBody)
			return
		}

		defer resp.Body.Close()
		streamAnthropicResponse(w, resp, requestID, attempt)
		return
	}
	if lastErr != nil {
		http.Error(w, lastErr.Error(), 502)
	}
}

func forwardAnthropic(w http.ResponseWriter, incoming *http.Request, m *storage.CustomModel, geminiReq map[string]any, requestID string) {
	baseRequest, err := toAnthropicRequestWithMedia(geminiReq, m.ExternalModelName)
	if err != nil {
		trace("anthropic-input-error", map[string]any{"requestId": requestID, "message": err.Error()})
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	applyAnthropicReasoning(baseRequest, m)
	breakpointCount := applyAnthropicPromptCachingForModel(baseRequest, m)
	cacheEnabled := breakpointCount > 0

	policy := currentStreamRecoveryPolicy()
	writer := newDownstreamSSEWriter(w)
	client := &http.Client{Timeout: upstreamStreamTimeout}
	requestBody := baseRequest
	lastModelVersion, lastResponseID := "", ""
	reconnects := 0
	cacheFallbackUsed := false
	excludedAccounts := map[string]struct{}{}
	pinnedAccountID := ""
	lastRejectedStatus := 0
	lastRejectedBody := ""

attemptLoop:
	for attempt := 1; ; attempt++ {
		attemptModel, lease, err := acquireAttemptModel(m, excludedAccounts, pinnedAccountID)
		if err != nil {
			trace("anthropic-account-pool-error", map[string]any{"requestId": requestID, "attempt": attempt, "message": err.Error()})
			if lastRejectedStatus != 0 && !writer.committed {
				if isRetryableStatus(lastRejectedStatus) || isTransientProviderRejection(lastRejectedStatus, lastRejectedBody) || isTransientModelPoolRejection(lastRejectedStatus, lastRejectedBody) {
					writeRecoverableTurnStop(writer, "anthropic", requestID, lastModelVersion, "当前账户的第三方上游暂时不可用，本轮未生成内容。请稍后再发送一次；新请求会重新探测该账户。", reconnects)
				} else {
					writeRejectedTurnStop(writer, "anthropic", requestID, lastModelVersion, lastRejectedStatus, lastRejectedBody)
				}
				return
			}
			reconnects++
			if waitForAccountPool(incoming.Context(), writer, policy, "anthropic", requestID, err, reconnects) {
				continue
			}
			if _, temporary := storage.AccountPoolRetryAfter(err); temporary && !writer.committed {
				writeRecoverableTurnStop(writer, "anthropic", requestID, lastModelVersion, "当前账户仍在处理其他请求，本轮未生成内容。请稍后再发送一次；新请求会重新探测该账户。", reconnects)
				return
			}
			if writer.committed {
				writeRecoveredStreamStop(writer, requestID, lastModelVersion, lastResponseID, reconnects, false)
			} else {
				http.Error(w, accountPoolError("Claude", err), http.StatusServiceUnavailable)
			}
			return
		}
		if pinnedAccountID == "" && lease != nil {
			pinnedAccountID = lease.ID
		}
		attemptConfig := upstream.ConfigFromModel(*attemptModel)
		apiURLs, err := upstream.ResolveAnthropicMessageCandidates(attemptConfig)
		if err != nil {
			releaseAttemptSuccess(lease)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		body, _ := json.Marshal(requestBody)
		for endpointIndex, apiURL := range apiURLs {
			trace("anthropic-upstream-request", map[string]any{
				"requestId": requestID, "attempt": attempt, "endpointCandidate": endpointIndex + 1,
				"accountId": func() string {
					if lease == nil {
						return ""
					}
					return lease.ID
				}(),
				"promptCache": cacheEnabled, "promptCacheBreakpoints": func() int {
					if cacheEnabled {
						return breakpointCount
					}
					return 0
				}(),
			})
			req, err := http.NewRequestWithContext(incoming.Context(), http.MethodPost, apiURL, bytes.NewReader(body))
			if err != nil {
				releaseAttemptSuccess(lease)
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			req.Header.Set("Content-Type", "application/json")
			if err := upstream.ApplyCredentials(req, attemptConfig); err != nil {
				releaseAttemptSuccess(lease)
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}

			req, writeTrace := traceUpstreamRequestWrite(req)
			resp, err := client.Do(req)
			if err != nil {
				trace("anthropic-upstream-error", map[string]any{"requestId": requestID, "attempt": attempt, "message": err.Error()})
				if incoming.Context().Err() != nil {
					releaseAttemptFailure(lease, 0, "", err.Error())
					return
				}
				reconnects++
				if canRetryUnsentTransportFailure(writer, policy, reconnects, writeTrace) {
					if lease != nil {
						lease.Release()
					}
					if waitForRejectedRequestRetry(incoming.Context(), policy, "anthropic", requestID, "connection-before-write", "", reconnects) {
						continue attemptLoop
					}
				}
				releaseAttemptFailure(lease, 0, "", err.Error())
				if writer.committed {
					writeUncertainUpstreamFailure(writer, "anthropic", requestID, lastModelVersion, lastResponseID, reconnects, false, "无法确认上游是否已接收请求："+err.Error())
				} else {
					writeRecoverableTurnStop(writer, "anthropic", requestID, lastModelVersion, "上游网络连接在请求送出后中断，本轮结果无法确认；这不是生图频率或张数限制，可以立即手动重新发送。XIASS Tools 未自动重放可能已送达的请求，以避免重复扣费或重复执行工具。", reconnects)
				}
				return
			}
			observeAttemptQuota(lease, "anthropic", resp)

			if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
				errBody, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				retryAfter := resp.Header.Get("Retry-After")
				trace("anthropic-upstream-error-response", map[string]any{
					"requestId": requestID, "statusCode": resp.StatusCode,
					"body": string(errBody[:min(len(errBody), 500)]),
				})
				if endpointIndex+1 < len(apiURLs) && upstream.CanFallbackToChat(resp.StatusCode) {
					trace("anthropic-compatible-endpoint-fallback", map[string]any{"requestId": requestID, "statusCode": resp.StatusCode})
					continue
				}
				if cacheEnabled && !cacheFallbackUsed && !writer.committed && isUnsupportedCacheResponse(resp.StatusCode, string(errBody)) {
					releaseAttemptSuccess(lease)
					rememberUnsupportedPromptCache("anthropic", m)
					stripAnthropicPromptCaching(baseRequest)
					cacheEnabled = false
					cacheFallbackUsed = true
					trace("prompt-cache-fallback", map[string]any{"requestId": requestID, "provider": "anthropic", "statusCode": resp.StatusCode})
					requestBody = baseRequest
					continue attemptLoop
				}
				if shouldFailOverAccount(lease, resp.StatusCode, string(errBody)) {
					retrySameAccount := shouldRetrySameAccount(lease, resp.StatusCode, string(errBody))
					lastRejectedStatus, lastRejectedBody = resp.StatusCode, string(errBody)
					reconnects++
					mayRetry := canRetryRejectedRequest(writer, policy, reconnects)
					if mayRetry && retrySameAccount {
						if lease != nil {
							lease.Release()
						}
					} else {
						releaseAttemptFailure(lease, resp.StatusCode, retryAfter, string(errBody))
						if !retrySameAccount {
							excludeFailedAttempt(excludedAccounts, lease)
						}
					}
					if mayRetry && waitForRejectedRequestRetry(incoming.Context(), policy, "anthropic", requestID, fmt.Sprintf("http-%d", resp.StatusCode), rejectedRetryAfter(resp.StatusCode, string(errBody), retryAfter, reconnects), reconnects) {
						requestBody = baseRequest
						continue attemptLoop
					}
					if incoming.Context().Err() != nil {
						return
					}
					if isRetryableStatus(resp.StatusCode) || isTransientProviderRejection(resp.StatusCode, string(errBody)) || isTransientModelPoolRejection(resp.StatusCode, string(errBody)) {
						writeRecoverableTurnStop(writer, "anthropic", requestID, lastModelVersion, "当前账户的第三方上游暂时没有可用线路，本轮未生成内容。请稍后再发送一次；新请求会重新探测该账户。", reconnects)
						return
					}
					returnRejectedUpstreamError(w, resp.StatusCode, errBody)
					return
				}
				releaseAttemptFailure(lease, resp.StatusCode, retryAfter, string(errBody))
				if incoming.Context().Err() != nil {
					return
				}
				if writer.committed {
					writeRecoveredStreamStop(writer, requestID, lastModelVersion, lastResponseID, reconnects, false)
					return
				}
				if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden || (resp.StatusCode == http.StatusNotFound && isModelRouteRejection(string(errBody))) {
					writeRejectedTurnStop(writer, "anthropic", requestID, lastModelVersion, resp.StatusCode, string(errBody))
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(resp.StatusCode)
				_, _ = w.Write(errBody)
				return
			}

			outcome := streamAnthropicAttempt(writer, resp, requestID, attempt, accountUsageTraceForAttempt(lease, attemptModel, "anthropic", attempt))
			resp.Body.Close()
			if outcome.responseID != "" {
				lastResponseID = outcome.responseID
			}
			if outcome.modelVersion != "" {
				lastModelVersion = outcome.modelVersion
			}
			if outcome.finished {
				releaseAttemptSuccess(lease)
				return
			}
			reason := "incomplete-stream"
			if outcome.err != nil {
				reason = outcome.err.Error()
			}
			releaseAttemptFailure(lease, 0, "", reason)
			excludeFailedAttempt(excludedAccounts, lease)
			if incoming.Context().Err() != nil {
				return
			}
			writeUncertainUpstreamFailure(writer, "anthropic", requestID, lastModelVersion, lastResponseID, reconnects, outcome.unsafeOutput, "上游流未正常完成："+reason)
			return
		}
	}
}

func resolveAnthropicMessagesURL(rawURL string) string {
	trimmed := strings.TrimSpace(rawURL)
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		base := strings.TrimRight(trimmed, "/")
		if strings.HasSuffix(base, "/messages") {
			return base
		}
		if strings.HasSuffix(base, "/chat/completions") {
			return strings.TrimSuffix(base, "/chat/completions") + "/messages"
		}
		if strings.HasSuffix(base, "/v1") {
			return base + "/messages"
		}
		return base + "/v1/messages"
	}

	path := strings.TrimRight(parsed.Path, "/")
	switch {
	case strings.HasSuffix(path, "/messages"):
		parsed.Path = path
	case strings.HasSuffix(path, "/chat/completions"):
		parsed.Path = strings.TrimSuffix(path, "/chat/completions") + "/messages"
	case path == "":
		parsed.Path = "/v1/messages"
	case strings.HasSuffix(path, "/v1"):
		parsed.Path = path + "/messages"
	default:
		parsed.Path = path + "/v1/messages"
	}
	return parsed.String()
}

func numberAsInt(value any) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case float64:
		return int(v), true
	default:
		return 0, false
	}
}

// streamOpenAIResponse streams an OpenAI SSE response converting to Gemini format.
func streamOpenAIResponse(w http.ResponseWriter, resp *http.Response, requestID string, attempt int) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Content-Disposition", "attachment")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, canFlush := w.(http.Flusher)

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	streamState := openAIStreamState{traceID: requestID}
	startedAt := time.Now()
	var firstByteAt time.Time
	wroteEvent := false

	for scanner.Scan() {
		line := scanner.Text()
		if firstByteAt.IsZero() && len(line) > 0 {
			firstByteAt = time.Now()
		}
		geminiLine := convertOpenAILineToGemini(line, &streamState)
		if geminiLine != "" {
			if !wroteEvent {
				w.WriteHeader(http.StatusOK)
				wroteEvent = true
			}
			w.Write([]byte(geminiLine))
			if canFlush {
				flusher.Flush()
			}
		}
	}
	if err := scanner.Err(); err != nil {
		if !wroteEvent {
			writeEmptyUpstreamStreamError(w, "openai", requestID, attempt, resp.Header.Get("Content-Type"), err.Error())
			return
		}
		trace("openai-stream-error", map[string]any{"requestId": requestID, "attempt": attempt, "message": err.Error(), "downstreamCommitted": true})
	}
	if !wroteEvent {
		writeEmptyUpstreamStreamError(w, "openai", requestID, attempt, resp.Header.Get("Content-Type"), "上游响应中没有可识别的 OpenAI SSE 事件")
		return
	}
	if !streamState.finished {
		trace("openai-stream-missing-stop", map[string]any{"requestId": requestID, "attempt": attempt})
		w.Write([]byte(encodeAntigravityStreamEvent(finalStopResponse(streamState.modelVersion, streamState.responseID), requestID)))
		if canFlush {
			flusher.Flush()
		}
	}

	if streamState.usage != nil {
		promptTokens, completionTokens, cacheReadTokens, cacheWriteTokens := openAIUsage(streamState.usage)
		trace("usage", map[string]any{
			"requestId":        requestID,
			"promptTokens":     promptTokens,
			"completionTokens": completionTokens,
			"cacheReadTokens":  cacheReadTokens,
			"cacheWriteTokens": cacheWriteTokens,
			"firstByteMs":      firstByteAt.Sub(startedAt).Milliseconds(),
			"totalMs":          time.Since(startedAt).Milliseconds(),
		})
	}
}

// streamOpenAIResponsesResponse converts Responses API SSE events into the
// internal Antigravity/Gemini envelope. It intentionally shares the same
// downstream headers and empty-stream diagnostics as Chat Completions.
func streamOpenAIResponsesResponse(w http.ResponseWriter, resp *http.Response, requestID string, attempt int) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Content-Disposition", "attachment")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, canFlush := w.(http.Flusher)
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	state := openAIResponsesStreamState{traceID: requestID}
	startedAt := time.Now()
	var firstByteAt time.Time
	wroteEvent := false
	for scanner.Scan() {
		line := scanner.Text()
		if firstByteAt.IsZero() && line != "" {
			firstByteAt = time.Now()
		}
		geminiLine := convertOpenAIResponsesLineToGemini(line, &state)
		if geminiLine == "" {
			continue
		}
		if !wroteEvent {
			w.WriteHeader(http.StatusOK)
			wroteEvent = true
		}
		_, _ = w.Write([]byte(geminiLine))
		if canFlush {
			flusher.Flush()
		}
	}
	if err := scanner.Err(); err != nil {
		if !wroteEvent {
			writeEmptyUpstreamStreamError(w, "responses", requestID, attempt, resp.Header.Get("Content-Type"), err.Error())
			return
		}
		trace("responses-stream-error", map[string]any{"requestId": requestID, "attempt": attempt, "message": err.Error(), "downstreamCommitted": true})
	}
	if !wroteEvent {
		writeEmptyUpstreamStreamError(w, "responses", requestID, attempt, resp.Header.Get("Content-Type"), "上游响应中没有可识别的 Responses SSE 事件")
		return
	}
	if !state.finished {
		trace("responses-stream-missing-stop", map[string]any{"requestId": requestID, "attempt": attempt})
		_, _ = w.Write([]byte(responsesFinishEvent("STOP", &state)))
		if canFlush {
			flusher.Flush()
		}
	}
	if state.usage != nil {
		prompt, _ := numberAsInt(state.usage["input_tokens"])
		completion, _ := numberAsInt(state.usage["output_tokens"])
		trace("usage", map[string]any{
			"requestId": requestID, "promptTokens": prompt, "completionTokens": completion,
			"firstByteMs": firstByteAt.Sub(startedAt).Milliseconds(), "totalMs": time.Since(startedAt).Milliseconds(),
		})
	}
}

func openAIUsage(usage map[string]any) (prompt, completion, cacheRead, cacheWrite int) {
	prompt, _ = numberAsInt(usage["prompt_tokens"])
	completion, _ = numberAsInt(usage["completion_tokens"])
	if details, ok := usage["prompt_tokens_details"].(map[string]any); ok {
		cacheRead, _ = numberAsInt(details["cached_tokens"])
		if value, ok := numberAsInt(details["cache_write_tokens"]); ok {
			cacheWrite = value
		}
	}
	if value, ok := numberAsInt(usage["cache_read_input_tokens"]); ok {
		cacheRead = value
	}
	if value, ok := numberAsInt(usage["cache_creation_input_tokens"]); ok {
		cacheWrite = value
	}
	return
}

// streamAnthropicResponse streams an Anthropic SSE response converting to Gemini format.
func streamAnthropicResponse(w http.ResponseWriter, resp *http.Response, requestID string, attempt int) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Content-Disposition", "attachment")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, canFlush := w.(http.Flusher)
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	startedAt := time.Now()
	var firstByteAt time.Time
	totals := anthropicUsageTotals{}
	state := anthropicStreamState{traceID: requestID}
	wroteEvent := false

	for scanner.Scan() {
		line := scanner.Text()
		if firstByteAt.IsZero() && len(line) > 0 {
			firstByteAt = time.Now()
		}
		collectAnthropicUsage(line, &totals)
		geminiLine := convertAnthropicLineToGemini(line, &state)
		if geminiLine != "" {
			if !wroteEvent {
				w.WriteHeader(http.StatusOK)
				wroteEvent = true
			}
			w.Write([]byte(geminiLine))
			if canFlush {
				flusher.Flush()
			}
		}
	}
	if err := scanner.Err(); err != nil {
		if !wroteEvent {
			writeEmptyUpstreamStreamError(w, "anthropic", requestID, attempt, resp.Header.Get("Content-Type"), err.Error())
			return
		}
		trace("anthropic-stream-error", map[string]any{"requestId": requestID, "attempt": attempt, "message": err.Error(), "downstreamCommitted": true})
	}
	if !wroteEvent {
		writeEmptyUpstreamStreamError(w, "anthropic", requestID, attempt, resp.Header.Get("Content-Type"), "上游响应中没有可识别的 Anthropic SSE 事件")
		return
	}
	if !state.finished {
		trace("anthropic-stream-missing-stop", map[string]any{"requestId": requestID, "attempt": attempt})
		w.Write([]byte(encodeAntigravityStreamEvent(finalStopResponse(state.modelVersion, state.responseID), requestID)))
		if canFlush {
			flusher.Flush()
		}
	}

	if totals.seen {
		trace("usage", map[string]any{
			"requestId":        requestID,
			"promptTokens":     totals.input + totals.cacheRead + totals.cacheWrite,
			"completionTokens": totals.output,
			"cacheReadTokens":  totals.cacheRead,
			"cacheWriteTokens": totals.cacheWrite,
			"firstByteMs":      firstByteAt.Sub(startedAt).Milliseconds(),
			"totalMs":          time.Since(startedAt).Milliseconds(),
		})
	}
}

func finalStopResponse(modelVersion, responseID string) map[string]any {
	response := map[string]any{
		"candidates": []any{map[string]any{
			"content":      map[string]any{"role": "model", "parts": []any{}},
			"finishReason": "STOP",
		}},
	}
	if modelVersion != "" {
		response["modelVersion"] = modelVersion
	}
	if responseID != "" {
		response["responseId"] = responseID
	}
	return response
}

func writeEmptyUpstreamStreamError(w http.ResponseWriter, provider, requestID string, attempt int, contentType, message string) {
	trace(provider+"-empty-stream", map[string]any{
		"requestId":   requestID,
		"attempt":     attempt,
		"contentType": contentType,
		"message":     message,
	})
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Del("Content-Disposition")
	w.Header().Del("Connection")
	w.WriteHeader(http.StatusBadGateway)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    "invalid_upstream_stream",
		},
	})
}

func isRetryableStatus(code int) bool {
	return code == 429 || code == 502 || code == 503 || code == 504 || code == 524
}

func retryDelay(attempt int, retryAfter string) time.Duration {
	if seconds, err := strconv.ParseFloat(retryAfter, 64); err == nil && seconds >= 0 {
		return minDuration(time.Duration(seconds*float64(time.Second)), 10*time.Second)
	}
	if date, err := http.ParseTime(retryAfter); err == nil {
		return minDuration(maxDuration(time.Until(date), 0), 10*time.Second)
	}
	delay := 250 * time.Millisecond * time.Duration(1<<(attempt-1))
	return minDuration(delay, 2*time.Second)
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// streamOpenAIAttempt converts one upstream stream but deliberately does not
// synthesize a stop event when the upstream connection vanishes. The caller
// can then keep the same downstream SSE response alive and retry safely.
func streamOpenAIAttempt(writer *downstreamSSEWriter, resp *http.Response, requestID string, attempt int, usageTrace *accountUsageTraceContext) streamAttemptOutcome {
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	state := openAIStreamState{traceID: requestID}
	startedAt := time.Now()
	var firstByteAt time.Time
	outcome := streamAttemptOutcome{}

	for scanner.Scan() {
		line := scanner.Text()
		if firstByteAt.IsZero() && line != "" {
			firstByteAt = time.Now()
		}
		if event := convertOpenAILineToGemini(line, &state); event != "" {
			writer.write(event)
			outcome.wroteEvent = true
		}
	}
	if err := scanner.Err(); err != nil {
		outcome.err = err
		trace("openai-stream-error", map[string]any{"requestId": requestID, "attempt": attempt, "message": err.Error(), "downstreamCommitted": writer.committed})
	}
	if state.done && !state.finished {
		writer.write(encodeAntigravityStreamEvent(finalStopResponse(state.modelVersion, state.responseID), requestID))
		outcome.wroteEvent = true
	}
	outcome.finished = state.finished || state.done
	outcome.emittedText = state.emittedText.String()
	outcome.unsafeOutput = state.unsafeOutput
	outcome.upstreamStarted = state.upstreamStarted
	outcome.responseID = state.responseID
	outcome.modelVersion = state.modelVersion
	if state.usage != nil {
		promptTokens, completionTokens, cacheReadTokens, cacheWriteTokens := openAIUsage(state.usage)
		traceAccountUsage(usageTrace, map[string]any{
			"requestId": requestID, "promptTokens": promptTokens, "completionTokens": completionTokens,
			"cacheReadTokens": cacheReadTokens, "cacheWriteTokens": cacheWriteTokens,
			"firstByteMs": firstByteAt.Sub(startedAt).Milliseconds(), "totalMs": time.Since(startedAt).Milliseconds(),
		})
	}
	return outcome
}

func streamOpenAIResponsesAttempt(writer *downstreamSSEWriter, resp *http.Response, requestID string, attempt int, usageTrace *accountUsageTraceContext) streamAttemptOutcome {
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	state := openAIResponsesStreamState{traceID: requestID}
	startedAt := time.Now()
	var firstByteAt time.Time
	outcome := streamAttemptOutcome{}
	for scanner.Scan() {
		line := scanner.Text()
		if firstByteAt.IsZero() && line != "" {
			firstByteAt = time.Now()
		}
		if event := convertOpenAIResponsesLineToGemini(line, &state); event != "" {
			writer.write(event)
			outcome.wroteEvent = true
		}
	}
	if err := scanner.Err(); err != nil {
		outcome.err = err
		trace("responses-stream-error", map[string]any{"requestId": requestID, "attempt": attempt, "message": err.Error(), "downstreamCommitted": writer.committed})
	}
	if state.done && !state.finished {
		// A number of OpenAI-compatible Responses gateways terminate with the
		// generic [DONE] marker instead of response.completed. Convert it into
		// Antigravity's terminal event so the client never waits forever.
		writer.write(responsesFinishEvent("STOP", &state))
		outcome.wroteEvent = true
	}
	outcome.finished = state.finished || state.done
	outcome.emittedText = state.emittedText.String()
	outcome.unsafeOutput = state.unsafeOutput
	outcome.upstreamStarted = state.upstreamStarted
	outcome.responseID = state.responseID
	outcome.modelVersion = state.modelVersion
	if state.usage != nil {
		prompt, _ := numberAsInt(state.usage["input_tokens"])
		completion, _ := numberAsInt(state.usage["output_tokens"])
		traceAccountUsage(usageTrace, map[string]any{
			"requestId": requestID, "promptTokens": prompt, "completionTokens": completion,
			"firstByteMs": firstByteAt.Sub(startedAt).Milliseconds(), "totalMs": time.Since(startedAt).Milliseconds(),
		})
	}
	return outcome
}

func streamAnthropicAttempt(writer *downstreamSSEWriter, resp *http.Response, requestID string, attempt int, usageTrace *accountUsageTraceContext) streamAttemptOutcome {
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	startedAt := time.Now()
	var firstByteAt time.Time
	totals := anthropicUsageTotals{}
	state := anthropicStreamState{traceID: requestID}
	outcome := streamAttemptOutcome{}

	for scanner.Scan() {
		line := scanner.Text()
		if firstByteAt.IsZero() && line != "" {
			firstByteAt = time.Now()
		}
		collectAnthropicUsage(line, &totals)
		if event := convertAnthropicLineToGemini(line, &state); event != "" {
			writer.write(event)
			outcome.wroteEvent = true
		}
	}
	if err := scanner.Err(); err != nil {
		outcome.err = err
		trace("anthropic-stream-error", map[string]any{"requestId": requestID, "attempt": attempt, "message": err.Error(), "downstreamCommitted": writer.committed})
	}
	outcome.finished = state.finished
	outcome.emittedText = state.emittedText.String()
	outcome.unsafeOutput = state.unsafeOutput
	outcome.upstreamStarted = state.upstreamStarted
	outcome.responseID = state.responseID
	outcome.modelVersion = state.modelVersion
	if totals.seen {
		traceAccountUsage(usageTrace, map[string]any{
			"requestId": requestID, "promptTokens": totals.input + totals.cacheRead + totals.cacheWrite,
			"completionTokens": totals.output, "cacheReadTokens": totals.cacheRead, "cacheWriteTokens": totals.cacheWrite,
			"firstByteMs": firstByteAt.Sub(startedAt).Milliseconds(), "totalMs": time.Since(startedAt).Milliseconds(),
		})
	}
	return outcome
}
