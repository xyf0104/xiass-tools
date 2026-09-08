package proxy

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"antigravity-wf-assistant/internal/storage"
	"github.com/andybalholm/brotli"
)

var modelContainerKeys = []string{"models", "availableModels", "available_models"}
var modelWrapperKeys = []string{"response", "result", "data"}
var modelSortKeys = []string{"agentModelSorts", "battleModeModelSorts"}
var modelIDIndexKeys = map[string]bool{
	"tieredModelIds":      true,
	"availableModelIds":   true,
	"allowedModelIds":     true,
	"allowlistedModelIds": true,
	// Present in newer FetchAvailableModels responses. This is not a generic
	// picker index: only models that are direct image backends belong here.
	// Older Antigravity versions omit this field and continue unchanged.
	"imageGenerationModelIds": true,
}

type modelResponseRoot struct {
	path  string
	value map[string]any
}

type modelInjectionSummary struct {
	officialCount    int
	customCount      int
	customNames      []string
	customSlugs      []string
	containers       []string
	indexPaths       []string
	unsupportedShape bool
	officialSample   any
	customSample     any
	assignments      modelRouteAssignments
	assignmentErr    error
}

func allocateModelSlugs(models []storage.CustomModel, usedModelIDs map[string]struct{}) map[string]string {
	assignments, _ := allocateModelSlugsWithExisting(models, usedModelIDs, modelRouteAssignments{})
	return assignments
}

func allocateModelSlugsWithExisting(models []storage.CustomModel, usedModelIDs map[string]struct{}, existing modelRouteAssignments) (map[string]string, error) {
	used := make(map[string]struct{}, len(usedModelIDs)+len(models))
	for id := range usedModelIDs {
		id = strings.TrimPrefix(id, "models/")
		if id != "" {
			used[id] = struct{}{}
		}
	}
	ordered := append([]storage.CustomModel(nil), models...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return modelPlaceholderKey(ordered[i]) < modelPlaceholderKey(ordered[j])
	})
	assignments := make(map[string]string, len(ordered))
	for _, model := range ordered {
		key := modelPlaceholderKey(model)
		slug := existing.slugs[key]
		if slug == "" {
			continue
		}
		if _, collision := used[slug]; collision {
			return nil, fmt.Errorf("原生模型响应与已激活模型 %s 的模型标识冲突", key)
		}
		assignments[key] = slug
		used[slug] = struct{}{}
	}
	for _, model := range ordered {
		key := modelPlaceholderKey(model)
		if assignments[key] != "" {
			continue
		}
		base := baseModelSlug(model)
		candidate := base
		for suffix := 2; ; suffix++ {
			if _, exists := used[candidate]; !exists {
				break
			}
			candidate = fmt.Sprintf("%s-%d", base, suffix)
		}
		assignments[key] = candidate
		used[candidate] = struct{}{}
	}
	return assignments, nil
}

func modelDisplayName(m storage.CustomModel) string {
	return storage.VisibleModelDisplayName(m)
}

func collectModelResponseRoots(parsed map[string]any) []modelResponseRoot {
	roots := make([]modelResponseRoot, 0, 4)
	var visit func(map[string]any, string, int)
	visit = func(value map[string]any, path string, depth int) {
		roots = append(roots, modelResponseRoot{path: path, value: value})
		if depth >= 6 {
			return
		}
		for _, key := range modelWrapperKeys {
			child, ok := value[key].(map[string]any)
			if !ok {
				continue
			}
			childPath := key
			if path != "" {
				childPath = path + "." + key
			}
			visit(child, childPath, depth+1)
		}
	}
	visit(parsed, "", 0)
	return roots
}

func modelPath(rootPath, key string) string {
	if rootPath == "" {
		return key
	}
	return rootPath + "." + key
}

func collectOfficialModelEntries(roots []modelResponseRoot) map[string]any {
	official := make(map[string]any)
	for _, root := range roots {
		for _, key := range modelContainerKeys {
			switch container := root.value[key].(type) {
			case map[string]any:
				for id, entry := range container {
					official[modelPath(root.path, key)+":"+id] = entry
				}
			case []any:
				for index, entry := range container {
					official[fmt.Sprintf("%s:%d", modelPath(root.path, key), index)] = entry
				}
			}
		}
	}
	return official
}

func collectUsedModelIDs(roots []modelResponseRoot) map[string]struct{} {
	used := make(map[string]struct{})
	for _, root := range roots {
		for _, key := range modelContainerKeys {
			switch container := root.value[key].(type) {
			case map[string]any:
				for id := range container {
					used[id] = struct{}{}
				}
			case []any:
				for _, raw := range container {
					entry, ok := raw.(map[string]any)
					if !ok {
						continue
					}
					for _, field := range []string{"name", "id", "modelId", "model_id"} {
						if value, ok := entry[field].(string); ok && value != "" {
							used[value] = struct{}{}
						}
					}
				}
			}
		}
	}
	return used
}

func collectNativeImageGenerationModelIDs(roots []modelResponseRoot) []string {
	var collected []string
	seen := make(map[string]bool)
	var visit func(any)
	visit = func(value any) {
		switch current := value.(type) {
		case string:
			if current = strings.TrimSpace(current); current != "" && !seen[current] {
				seen[current] = true
				collected = append(collected, current)
			}
		case []any:
			for _, item := range current {
				visit(item)
			}
		case map[string]any:
			for _, item := range current {
				visit(item)
			}
		}
	}
	for _, root := range roots {
		if value, exists := root.value["imageGenerationModelIds"]; exists {
			visit(value)
		}
	}
	return collected
}

func buildArrayModelEntry(m storage.CustomModel, slug, placeholder string) map[string]any {
	entry := buildFakeModelEntry(m, placeholder)
	entry["name"] = "models/" + slug
	entry["version"] = "1.0"
	entry["inputTokenLimit"] = 1048576
	entry["outputTokenLimit"] = 65536
	entry["supportedGenerationMethods"] = []any{"generateContent", "streamGenerateContent", "countTokens"}
	return entry
}

func arrayHasInjectedModel(entries []any, slug, placeholder string) bool {
	for _, raw := range entries {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if entry["name"] == "models/"+slug || entry["name"] == slug || entry["model"] == placeholder {
			return true
		}
	}
	return false
}

// injectCustomModels supports root and wrapped model responses, map and array
// containers, and every currently known picker index.
func injectCustomModels(parsed map[string]any, models []storage.CustomModel) modelInjectionSummary {
	summary := modelInjectionSummary{}
	roots := collectModelResponseRoots(parsed)
	if len(models) == 0 {
		return summary
	}
	// Validate the response shape before touching the process-wide slug and
	// placeholder assignments used by request routing. An unknown JSON payload
	// must be a complete no-op: a failed compatibility probe must not change
	// how an already-open Antigravity picker routes its selected model.
	hasSupportedContainer := false
	for _, root := range roots {
		for _, key := range modelContainerKeys {
			switch root.value[key].(type) {
			case map[string]any, []any:
				hasSupportedContainer = true
			}
		}
	}
	if !hasSupportedContainer {
		summary.unsupportedShape = true
		return summary
	}
	official := collectOfficialModelEntries(roots)
	existingAssignments := snapshotModelRouteAssignments()
	slugAssignments, slugErr := allocateModelSlugsWithExisting(models, collectUsedModelIDs(roots), existingAssignments)
	if slugErr != nil {
		summary.assignmentErr = slugErr
		return summary
	}
	assignments, placeholderErr := allocateModelPlaceholdersWithExisting(models, official, existingAssignments)
	if placeholderErr != nil {
		summary.assignmentErr = placeholderErr
		return summary
	}
	summary.assignments = modelRouteAssignments{
		placeholders:        assignments,
		slugs:               slugAssignments,
		nativeImageModelIDs: collectNativeImageGenerationModelIDs(roots),
	}
	slugs := make([]string, 0, len(models))
	imageGenerationSlugs := make([]string, 0, len(models))
	for _, model := range models {
		key := modelPlaceholderKey(model)
		if assignments[key] == "" || slugAssignments[key] == "" {
			continue
		}
		slug := slugAssignments[key]
		slugs = append(slugs, slug)
		// A custom chat model can advertise image generation because the
		// trajectory router selects a configured direct image model for its
		// turn. imageGenerationModelIds is different: Antigravity treats it as
		// an execution-model directory, so putting a chat model there can make
		// the IDE invoke the chat endpoint as an image backend.
		if isOpenAICompatibleImageProvider(model.Provider) && isDirectImageModelName(model.ExternalModelName) {
			imageGenerationSlugs = append(imageGenerationSlugs, slug)
		}
		summary.customNames = append(summary.customNames, modelDisplayName(model))
		summary.customSlugs = append(summary.customSlugs, slug)
	}
	summary.customCount = len(slugs)

	var indexedRoots []modelResponseRoot
	for _, root := range roots {
		rootInjected := false
		for _, key := range modelContainerKeys {
			raw, exists := root.value[key]
			if !exists {
				continue
			}
			switch container := raw.(type) {
			case map[string]any:
				if len(container) > summary.officialCount {
					summary.officialCount = len(container)
				}
				for _, entry := range container {
					if summary.officialSample == nil {
						summary.officialSample = entry
					}
					break
				}
				for _, model := range models {
					key := modelPlaceholderKey(model)
					placeholder, slug := assignments[key], slugAssignments[key]
					if placeholder == "" || slug == "" {
						continue
					}
					entry := buildFakeModelEntry(model, placeholder)
					container[slug] = entry
					if summary.customSample == nil {
						summary.customSample = entry
					}
				}
				summary.containers = append(summary.containers, modelPath(root.path, key)+":map")
				rootInjected = true
			case []any:
				if len(container) > summary.officialCount {
					summary.officialCount = len(container)
				}
				if len(container) > 0 && summary.officialSample == nil {
					summary.officialSample = container[0]
				}
				injected := make([]any, 0, len(models))
				for _, model := range models {
					key := modelPlaceholderKey(model)
					placeholder, slug := assignments[key], slugAssignments[key]
					if placeholder == "" || slug == "" || arrayHasInjectedModel(container, slug, placeholder) {
						continue
					}
					entry := buildArrayModelEntry(model, slug, placeholder)
					injected = append(injected, entry)
					if summary.customSample == nil {
						summary.customSample = entry
					}
				}
				root.value[key] = append(injected, container...)
				summary.containers = append(summary.containers, modelPath(root.path, key)+":array")
				rootInjected = true
			}
		}
		if rootInjected {
			indexedRoots = append(indexedRoots, root)
		}
	}
	if len(summary.containers) == 0 {
		// Do not invent a models/agentModelSorts schema for an unknown successful
		// response. A future Antigravity release may use a different payload that
		// happens to be JSON; adding guessed fields can make its Language Server
		// reject or silently reinterpret the response. The caller records the
		// explicit compatibility diagnostic and forwards the original shape.
		summary.unsupportedShape = true
		return summary
	}
	for _, root := range indexedRoots {
		summary.indexPaths = append(summary.indexPaths, addModelIndexes(root.value, root.path, slugs, imageGenerationSlugs)...)
	}
	summary.indexPaths = uniqueStrings(summary.indexPaths)
	return summary
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

func addModelIndexes(parsed map[string]any, rootPath string, modelIDs, imageGenerationModelIDs []string) []string {
	if len(modelIDs) == 0 {
		return nil
	}
	var paths []string
	for _, key := range modelSortKeys {
		raw, exists := parsed[key]
		if !exists {
			continue
		}
		updated, changed := addModelSortIDs(raw, modelIDs)
		if !changed {
			// A field with a familiar name is not enough evidence that this
			// Language Server uses the legacy sorter schema. Leave an unknown
			// value untouched; validation will fail the detached candidate rather
			// than returning a payload with a guessed replacement type.
			continue
		}
		parsed[key] = updated
		paths = append(paths, modelPath(rootPath, key)+".groups[].modelIds")
	}
	// Do not invent agentModelSorts for a response that merely happens to have a
	// models-shaped container. The picker index is consumed by the Language
	// Server/renderer rather than by this HTTP endpoint alone, and an unknown
	// version may use a different proto field or visibility gate. Validation
	// below will reject the candidate when no known, existing index receives the
	// model IDs, so the caller forwards the exact native response unchanged.
	for key := range modelIDIndexKeys {
		value, exists := parsed[key]
		if !exists {
			continue
		}
		ids := modelIDs
		if key == "imageGenerationModelIds" {
			ids = imageGenerationModelIDs
		}
		if len(ids) == 0 {
			continue
		}
		var updated any
		var changed bool
		if key == "imageGenerationModelIds" {
			updated, changed = prependImageGenerationModelIDs(value, ids)
		} else {
			updated, changed = prependModelIDs(value, ids)
		}
		if changed {
			parsed[key] = updated
			paths = append(paths, modelPath(rootPath, key))
		}
	}
	return paths
}

// prependImageGenerationModelIDs accepts only the known flat string-list
// structure. Unlike ordinary picker indexes, this field is an execution
// directory; recursively modifying a map or mixed proto value risks routing a
// native Gemini image request through a custom chat model. Unknown variants
// are intentionally left byte-for-byte intact.
func prependImageGenerationModelIDs(value any, modelIDs []string) (any, bool) {
	ids, ok := value.([]any)
	if !ok {
		return value, false
	}
	for _, item := range ids {
		if _, ok := item.(string); !ok {
			return value, false
		}
	}
	return prependUniqueModelIDs(ids, modelIDs), true
}

func addModelSortIDs(raw any, modelIDs []string) (any, bool) {
	sorts, ok := raw.([]any)
	if !ok {
		return raw, false
	}
	inserted := false
	for sortIndex, rawSort := range sorts {
		sortEntry, ok := rawSort.(map[string]any)
		if !ok {
			continue
		}
		groups, ok := sortEntry["groups"].([]any)
		if !ok {
			continue
		}
		groupsChanged := false
		for groupIndex, rawGroup := range groups {
			group, ok := rawGroup.(map[string]any)
			if !ok {
				continue
			}
			ids, ok := group["modelIds"].([]any)
			if !ok {
				continue
			}
			group["modelIds"] = prependUniqueModelIDs(ids, modelIDs)
			groups[groupIndex] = group
			groupsChanged = true
			inserted = true
		}
		if groupsChanged {
			sortEntry["groups"] = groups
			sorts[sortIndex] = sortEntry
		}
	}
	return sorts, inserted
}

func prependUniqueModelIDs(existing []any, modelIDs []string) []any {
	custom := make([]any, 0, len(modelIDs))
	for _, modelID := range modelIDs {
		found := false
		for _, value := range existing {
			if value == modelID || value == "models/"+modelID {
				found = true
				break
			}
		}
		if !found {
			custom = append(custom, modelID)
		}
	}
	return append(custom, existing...)
}

func prependModelIDs(value any, modelIDs []string) (any, bool) {
	switch current := value.(type) {
	case []any:
		isStringList := len(current) == 0
		for _, item := range current {
			if _, ok := item.(string); ok {
				isStringList = true
				continue
			}
			isStringList = false
			break
		}
		if isStringList {
			return prependUniqueModelIDs(current, modelIDs), true
		}
		changed := false
		for index, item := range current {
			if updated, itemChanged := prependModelIDs(item, modelIDs); itemChanged {
				current[index] = updated
				changed = true
			}
		}
		return current, changed
	case map[string]any:
		changed := false
		for key, item := range current {
			if updated, itemChanged := prependModelIDs(item, modelIDs); itemChanged {
				current[key] = updated
				changed = true
			}
		}
		return current, changed
	default:
		return value, false
	}
}

func validateModelInjection(parsed map[string]any, models []storage.CustomModel, summary modelInjectionSummary) error {
	if len(models) == 0 {
		return nil
	}
	if summary.unsupportedShape {
		return fmt.Errorf("上游模型响应不包含已支持的模型容器（models、availableModels 或 available_models）；为避免破坏未知 Antigravity 协议，未注入自定义模型")
	}
	if summary.assignmentErr != nil {
		return fmt.Errorf("为保持已打开模型选择器的路由一致性，未注入自定义模型: %w", summary.assignmentErr)
	}
	if summary.customCount != len(models) {
		return fmt.Errorf("只为 %d/%d 个自定义模型分配了有效标识", summary.customCount, len(models))
	}
	roots := collectModelResponseRoots(parsed)
	for _, model := range models {
		key := modelPlaceholderKey(model)
		slug := summary.assignments.slugs[key]
		placeholder := summary.assignments.placeholders[key]
		if slug == "" || placeholder == "" {
			return fmt.Errorf("注入后缺少 %s 的本次模型路由标识", key)
		}
		if !responseContainsModel(roots, slug, placeholder) {
			return fmt.Errorf("注入后未在模型容器中找到 %s", slug)
		}
		if !responseIndexesModel(roots, slug, placeholder) {
			return fmt.Errorf("注入后没有模型选择索引引用 %s", slug)
		}
	}
	return nil
}

func responseContainsModel(roots []modelResponseRoot, slug, placeholder string) bool {
	for _, root := range roots {
		for _, key := range modelContainerKeys {
			switch container := root.value[key].(type) {
			case map[string]any:
				if _, ok := container[slug]; ok {
					return true
				}
			case []any:
				if arrayHasInjectedModel(container, slug, placeholder) {
					return true
				}
			}
		}
	}
	return false
}

func responseIndexesModel(roots []modelResponseRoot, slug, placeholder string) bool {
	wanted := map[string]bool{slug: true, "models/" + slug: true, placeholder: true, "models/" + placeholder: true}
	for _, root := range roots {
		for key, value := range root.value {
			lower := strings.ToLower(key)
			// imageGenerationModelIds is an execution-model directory, not a
			// visibility index for the agent picker. It must never make an
			// otherwise unknown models response look compatible enough to inject.
			if lower == "imagegenerationmodelids" {
				continue
			}
			if strings.HasSuffix(lower, "modelids") || strings.HasSuffix(lower, "model_ids") || key == "agentModelSorts" || key == "battleModeModelSorts" {
				if containsModelReference(value, wanted) {
					return true
				}
			}
		}
	}
	return false
}

func containsModelReference(value any, wanted map[string]bool) bool {
	switch current := value.(type) {
	case string:
		return wanted[current]
	case []any:
		for _, item := range current {
			if containsModelReference(item, wanted) {
				return true
			}
		}
	case map[string]any:
		for _, item := range current {
			if containsModelReference(item, wanted) {
				return true
			}
		}
	}
	return false
}

// handleFetchAvailableModels forwards Google's model response and injects the
// configured models into current and legacy Antigravity response shapes.
func handleFetchAvailableModels(w http.ResponseWriter, r *http.Request) {
	handleFetchAvailableModelsWithClient(w, r, &http.Client{Timeout: 30 * time.Second})
}

// handleFetchAvailableModelsWithClient isolates the upstream transport so the
// model-response conversion can be verified locally without contacting Google.
// Production always calls the wrapper above with a bounded HTTP client.
func handleFetchAvailableModelsWithClient(w http.ResponseWriter, r *http.Request, client *http.Client) {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, googleBaseURL+"/v1internal:fetchAvailableModels", bytes.NewReader(body))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	for key, values := range r.Header {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	req.Header.Set("Host", googleHost)
	req.Header.Set("Accept-Encoding", "identity")

	resp, err := client.Do(req)
	if err != nil {
		trace("model-response-error", map[string]any{"message": err.Error()})
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	body, err = io.ReadAll(resp.Body)
	if err != nil {
		trace("model-response-error", map[string]any{"statusCode": resp.StatusCode, "encoding": resp.Header.Get("Content-Encoding"), "message": err.Error()})
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	encoding := resp.Header.Get("Content-Encoding")
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		trace("model-response-error", map[string]any{"statusCode": resp.StatusCode, "encoding": encoding, "message": fmt.Sprintf("模型上游返回 HTTP %d", resp.StatusCode)})
		forwardRawModelResponse(w, resp, body)
		return
	}
	decoded, err := decodeModelResponse(body, encoding)
	if err != nil {
		trace("model-response-error", map[string]any{"statusCode": resp.StatusCode, "encoding": encoding, "message": err.Error()})
		forwardRawModelResponse(w, resp, body)
		return
	}
	var raw any
	if err := json.Unmarshal(decoded, &raw); err != nil {
		trace("model-response-error", map[string]any{"statusCode": resp.StatusCode, "encoding": encoding, "message": fmt.Sprintf("模型响应 JSON 解析失败: %v", err)})
		forwardRawModelResponse(w, resp, body)
		return
	}
	parsed, ok := raw.(map[string]any)
	if !ok {
		trace("model-response-error", map[string]any{"statusCode": resp.StatusCode, "encoding": encoding, "message": "模型响应 JSON 根节点不是对象"})
		forwardRawModelResponse(w, resp, body)
		return
	}
	models, loadErr := storage.LoadEnabledModels()
	if loadErr != nil {
		trace("model-injection-error", map[string]any{"message": fmt.Sprintf("读取自定义模型失败: %v", loadErr)})
		models = nil
	}

	// Serialize snapshot -> allocation -> validation -> commit. Two concurrent
	// fetches must not calculate candidate mappings from the same old snapshot
	// and then publish them in a different order from the picker responses they
	// return to Antigravity.
	modelInjectionTransactionMu.Lock()
	// Build and validate the candidate on a detached JSON tree. The original
	// decoded response remains the exact fail-closed fallback, while the global
	// routing assignments remain untouched until the candidate is proven safe.
	candidate, cloneErr := cloneModelResponse(parsed)
	var summary modelInjectionSummary
	var validationErr error
	var out []byte
	var marshalErr error
	if cloneErr == nil {
		summary = injectCustomModels(candidate, models)
		validationErr = validateModelInjection(candidate, models, summary)
		if validationErr == nil {
			out, marshalErr = json.Marshal(candidate)
			if marshalErr == nil && len(models) > 0 {
				commitModelRouteAssignments(summary.assignments)
			}
		}
	}
	modelInjectionTransactionMu.Unlock()

	if cloneErr != nil {
		trace("model-injection-error", map[string]any{
			"message": fmt.Sprintf("无法创建模型注入候选副本: %v", cloneErr),
		})
		copyDecodedModelHeaders(w.Header(), resp.Header)
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(decoded)
		return
	}
	if validationErr != nil {
		trace("model-injection-error", map[string]any{"configuredCount": len(models), "customCount": summary.customCount, "containers": summary.containers, "indexPaths": summary.indexPaths, "message": validationErr.Error()})
	}
	if err := saveModelStructureSnapshot(candidate, summary, resp.StatusCode, encoding, validationErr); err != nil {
		trace("model-snapshot-error", map[string]any{"message": err.Error()})
	}
	if validationErr != nil {
		// A failed post-injection validation means the proxy cannot prove that
		// this Language Server will accept the altered picker schema. Never
		// hand it a partially injected response: that can leave the IDE with a
		// model map and indexes that disagree, which is worse than safely
		// showing only the native models for this compatibility probe.
		copyDecodedModelHeaders(w.Header(), resp.Header)
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(decoded)
		return
	}
	if marshalErr != nil {
		trace("model-response-error", map[string]any{"statusCode": resp.StatusCode, "encoding": encoding, "message": marshalErr.Error()})
		http.Error(w, marshalErr.Error(), http.StatusBadGateway)
		return
	}
	copyDecodedModelHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	if _, err := w.Write(out); err != nil {
		trace("model-response-error", map[string]any{"statusCode": resp.StatusCode, "message": err.Error()})
		return
	}
	if validationErr == nil && loadErr == nil {
		trace("models-injected", map[string]any{"officialCount": summary.officialCount, "customCount": summary.customCount, "customNames": summary.customNames, "customSlugs": summary.customSlugs, "containers": summary.containers, "indexPaths": summary.indexPaths})
	}
}

func cloneModelResponse(parsed map[string]any) (map[string]any, error) {
	encoded, err := json.Marshal(parsed)
	if err != nil {
		return nil, err
	}
	var clone map[string]any
	if err := json.Unmarshal(encoded, &clone); err != nil {
		return nil, err
	}
	return clone, nil
}

func decodeModelResponse(body []byte, encoding string) ([]byte, error) {
	encoding = strings.ToLower(strings.TrimSpace(strings.Split(encoding, ",")[0]))
	switch encoding {
	case "", "identity":
		return body, nil
	case "gzip":
		reader, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("gzip 模型响应解码失败: %w", err)
		}
		defer reader.Close()
		return io.ReadAll(reader)
	case "deflate":
		reader, err := zlib.NewReader(bytes.NewReader(body))
		if err == nil {
			defer reader.Close()
			return io.ReadAll(reader)
		}
		raw := flate.NewReader(bytes.NewReader(body))
		defer raw.Close()
		decoded, rawErr := io.ReadAll(raw)
		if rawErr != nil {
			return nil, fmt.Errorf("deflate 模型响应解码失败: zlib=%v, raw=%w", err, rawErr)
		}
		return decoded, nil
	case "br":
		decoded, err := io.ReadAll(brotli.NewReader(bytes.NewReader(body)))
		if err != nil {
			return nil, fmt.Errorf("br 模型响应解码失败: %w", err)
		}
		return decoded, nil
	default:
		return nil, fmt.Errorf("不支持的模型响应 Content-Encoding: %s", encoding)
	}
}

func copyDecodedModelHeaders(target, source http.Header) {
	for key, values := range source {
		lower := strings.ToLower(key)
		if lower == "content-encoding" || lower == "content-length" || lower == "transfer-encoding" {
			continue
		}
		for _, value := range values {
			target.Add(key, value)
		}
	}
	target.Set("Content-Type", "application/json; charset=utf-8")
}

func forwardRawModelResponse(w http.ResponseWriter, resp *http.Response, body []byte) {
	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}

func saveModelStructureSnapshot(parsed map[string]any, summary modelInjectionSummary, statusCode int, encoding string, validationErr error) error {
	rootKeys := make([]string, 0, len(parsed))
	for key := range parsed {
		rootKeys = append(rootKeys, key)
	}
	sort.Strings(rootKeys)
	validation := "ok"
	if validationErr != nil {
		validation = "failed"
	}
	snapshot := map[string]any{
		"updatedAt":       time.Now().UTC().Format(time.RFC3339Nano),
		"statusCode":      statusCode,
		"contentEncoding": encoding,
		"rootFields":      rootKeys,
		"containers":      summary.containers,
		"indexes":         summary.indexPaths,
		"officialSample":  jsonShape(summary.officialSample, 0),
		"customSample":    jsonShape(summary.customSample, 0),
		"validation":      validation,
	}
	encoded, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	dir := storage.StorageDir()
	if dir == "" {
		return nil
	}
	path := filepath.Join(dir, "model-response-structure.json")
	temp, err := os.CreateTemp(dir, ".model-response-structure-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(append(encoded, '\n')); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	_ = os.Remove(path)
	return os.Rename(tempPath, path)
}

func jsonShape(value any, depth int) any {
	if depth >= 6 {
		return "object"
	}
	switch current := value.(type) {
	case nil:
		return "none"
	case map[string]any:
		shape := make(map[string]any, len(current))
		for key, item := range current {
			shape[key] = jsonShape(item, depth+1)
		}
		return shape
	case []any:
		shape := map[string]any{"type": "array", "length": len(current)}
		if len(current) > 0 {
			shape["item"] = jsonShape(current[0], depth+1)
		}
		return shape
	case string:
		return "string"
	case bool:
		return "boolean"
	case float64, float32, int, int64, json.Number:
		return "number"
	default:
		return fmt.Sprintf("%T", value)
	}
}
