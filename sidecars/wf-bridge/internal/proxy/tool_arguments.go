package proxy

import (
	"encoding/json"
	"strings"
)

// decodeFunctionCallArgs accepts only a JSON object. Tool arguments are part
// of the IDE's signed function-call contract: turning malformed input into an
// empty object makes required fields disappear and causes Antigravity to
// replay the whole planner turn. An explicit {} remains valid for tools that
// do not take parameters.
func decodeFunctionCallArgs(raw string) (map[string]any, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, false
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(raw), &args); err != nil || args == nil {
		return nil, false
	}
	return args, true
}

// marshalFunctionCallArgs validates an initial provider-side input object
// before retaining it. The provider may send input directly on
// content_block_start instead of following it with input_json_delta events.
func marshalFunctionCallArgs(value any) (string, bool) {
	if value == nil {
		return "", false
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return "", false
	}
	if _, ok := decodeFunctionCallArgs(string(raw)); !ok {
		return "", false
	}
	return string(raw), true
}

// traceDroppedFunctionCall records only non-sensitive metadata. Never include
// malformed arguments themselves because they can contain user content or
// credentials supplied to a tool.
func traceDroppedFunctionCall(provider, requestID, toolID, name, raw string) {
	trace("invalid-tool-call-dropped", map[string]any{
		"provider":    provider,
		"requestId":   requestID,
		"toolId":      toolID,
		"name":        name,
		"argumentLen": len(strings.TrimSpace(raw)),
	})
}
