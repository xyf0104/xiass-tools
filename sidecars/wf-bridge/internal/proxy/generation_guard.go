package proxy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
	"time"
)

// Antigravity occasionally issues an overlapping retry while the original
// stream is still being processed. Starting both would charge two independent
// upstream generations and can create duplicated assistant turns. The guard
// only suppresses concurrent semantic duplicates; a deliberate later retry is
// never blocked.
const activeGenerationTTL = 20 * time.Minute

var activeGenerations = struct {
	sync.Mutex
	entries map[string]time.Time
}{entries: make(map[string]time.Time)}

func reserveGeneration(modelID, requestID string, request map[string]any) (func(), bool) {
	key := generationFingerprint(modelID, requestID, request)
	now := time.Now()
	activeGenerations.Lock()
	for candidate, started := range activeGenerations.entries {
		if now.Sub(started) > activeGenerationTTL {
			delete(activeGenerations.entries, candidate)
		}
	}
	if _, exists := activeGenerations.entries[key]; exists {
		activeGenerations.Unlock()
		return nil, false
	}
	activeGenerations.entries[key] = now
	activeGenerations.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			activeGenerations.Lock()
			delete(activeGenerations.entries, key)
			activeGenerations.Unlock()
		})
	}, true
}

func generationFingerprint(modelID, requestID string, request map[string]any) string {
	// requestId identifies an HTTP attempt, not a chat turn. A client-side
	// retry can therefore have a new requestId while carrying the exact same
	// generation payload. Deliberately exclude it from the in-flight key so
	// those overlapping attempts cannot start a second billable generation.
	//
	// Keep the parameter for compatibility with older callers and tests.
	_ = requestID
	canonicalRequest := canonicalGenerationRequest(request)
	payload, err := json.Marshal(map[string]any{
		"model": modelID, "request": canonicalRequest,
	})
	if err != nil {
		payload = []byte(modelID)
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// canonicalGenerationRequest removes only transport-level attempt metadata.
// Conversation contents, tool arguments and session identifiers remain part
// of the key, so unrelated turns cannot suppress one another.
func canonicalGenerationRequest(request map[string]any) map[string]any {
	if len(request) == 0 {
		return request
	}
	canonical := make(map[string]any, len(request))
	for key, value := range request {
		normalized := strings.ToLower(strings.TrimSpace(key))
		switch normalized {
		case "requestid", "request_id", "attemptid", "attempt_id", "traceid", "trace_id":
			continue
		default:
			canonical[key] = value
		}
	}
	return canonical
}
