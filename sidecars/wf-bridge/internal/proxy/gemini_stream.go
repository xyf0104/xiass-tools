package proxy

import "encoding/json"

func antigravityResponseEnvelope(response map[string]any, traceID string) map[string]any {
	if traceID == "" {
		traceID = "antigravity-wf"
	}
	return map[string]any{
		"response": response,
		"traceId":  traceID,
		"metadata": map[string]any{},
	}
}

// encodeAntigravityStreamEvent wraps a Gemini generate-content response in
// the internal Cloud Code envelope consumed by Antigravity. The public Gemini
// API streams candidates at the top level, but Antigravity's
// v1internal:streamGenerateContent endpoint always nests them under response.
func encodeAntigravityStreamEvent(response map[string]any, traceID string) string {
	envelope := antigravityResponseEnvelope(response, traceID)
	out, _ := json.Marshal(envelope)
	return "data: " + string(out) + "\n\n"
}
