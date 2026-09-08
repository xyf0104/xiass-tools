package proxy

import (
	"strings"
	"sync"
	"time"

	"antigravity-wf-assistant/internal/storage"
)

// Antigravity executes a native image tool in two requests. The agent turn
// uses the selected custom model, then a separate generateContent request is
// issued to an internal Gemini image model with an image_gen request ID. Keep
// only the selected compatible model for that trajectory so the latter request
// can use the same upstream image endpoint instead of silently reaching
// Gemini.
const imageGenerationSourceTTL = 30 * time.Minute

type imageGenerationSource struct {
	model    storage.CustomModel
	recorded time.Time
}

var imageGenerationSources = struct {
	sync.Mutex
	byTrajectory map[string]imageGenerationSource
}{
	byTrajectory: make(map[string]imageGenerationSource),
}

func rememberImageGenerationSource(requestID string, model *storage.CustomModel) {
	if model == nil || !isOpenAICompatibleImageProvider(model.Provider) {
		return
	}
	trajectoryID := antigravityImageGenerationTrajectory(requestID)
	if trajectoryID == "" {
		return
	}
	now := time.Now()
	imageGenerationSources.Lock()
	pruneImageGenerationSourcesLocked(now)
	imageGenerationSources.byTrajectory[trajectoryID] = imageGenerationSource{
		model:    *model,
		recorded: now,
	}
	imageGenerationSources.Unlock()
	trace("image-generation-source-recorded", map[string]any{
		"requestId":    requestID,
		"trajectoryId": trajectoryID,
		"model":        model.Name,
	})
}

func imageGenerationSourceForRequest(requestID string) *storage.CustomModel {
	if !isNativeImageGenerationRequestID(requestID) {
		return nil
	}
	trajectoryID := antigravityImageGenerationTrajectory(requestID)
	if trajectoryID == "" {
		return nil
	}
	now := time.Now()
	imageGenerationSources.Lock()
	pruneImageGenerationSourcesLocked(now)
	source, ok := imageGenerationSources.byTrajectory[trajectoryID]
	imageGenerationSources.Unlock()
	if !ok {
		return nil
	}
	model := source.model
	return &model
}

// forgetImageGenerationSource removes only the in-memory source associated
// with a trajectory. A native agent request means the user switched away from
// a custom image-capable model, so a later native image_gen request must not
// inherit the old upstream route.
func forgetImageGenerationSource(requestID string) {
	trajectoryID := antigravityImageGenerationTrajectory(requestID)
	if trajectoryID == "" {
		return
	}
	imageGenerationSources.Lock()
	pruneImageGenerationSourcesLocked(time.Now())
	delete(imageGenerationSources.byTrajectory, trajectoryID)
	imageGenerationSources.Unlock()
}

func isNativeImageGenerationRequestID(requestID string) bool {
	parts := strings.Split(strings.Trim(strings.TrimSpace(requestID), "/"), "/")
	if len(parts) < 4 {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(parts[0])) {
	case "image_gen", "imagegen", "image-generation":
		return true
	default:
		return false
	}
}

func antigravityImageGenerationTrajectory(requestID string) string {
	parts := strings.Split(strings.Trim(strings.TrimSpace(requestID), "/"), "/")
	if len(parts) < 4 {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(parts[0])) {
	case "agent", "image_gen", "imagegen", "image-generation":
		trajectoryID := strings.TrimSpace(parts[len(parts)-2])
		if trajectoryID != "" {
			return trajectoryID
		}
	}
	return ""
}

func pruneImageGenerationSourcesLocked(now time.Time) {
	for trajectoryID, source := range imageGenerationSources.byTrajectory {
		if now.Sub(source.recorded) > imageGenerationSourceTTL {
			delete(imageGenerationSources.byTrajectory, trajectoryID)
		}
	}
}

func resetImageGenerationSourcesForTest() {
	imageGenerationSources.Lock()
	imageGenerationSources.byTrajectory = make(map[string]imageGenerationSource)
	imageGenerationSources.Unlock()
}
