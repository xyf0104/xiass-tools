package proxy

import "strings"

// toolCallAssociation keeps the real IDs emitted by prior assistant tool calls
// while one Gemini request is converted to an upstream request. Antigravity can
// omit the ID on a following functionResponse even though OpenAI Responses,
// Chat Completions and Anthropic Messages require that original ID.
//
// This is deliberately request-local. It never writes a cross-request cache or
// guesses an ID that did not appear in the current conversation payload. When
// a same-name match is ambiguous, the most recent unconsumed assistant call is
// the only defensible association; otherwise callers retain their established
// protocol-specific fallback value.
type toolCallAssociation struct {
	pendingByName map[string][]string
}

func newToolCallAssociation() *toolCallAssociation {
	return &toolCallAssociation{pendingByName: make(map[string][]string)}
}

func (association *toolCallAssociation) rememberAssistantCall(name, callID string) {
	if association == nil {
		return
	}
	name = strings.TrimSpace(name)
	callID = strings.TrimSpace(callID)
	if name == "" || callID == "" {
		return
	}
	association.pendingByName[name] = append(association.pendingByName[name], callID)
}

// resolveResponseID preserves an explicit response ID. With no explicit ID it
// consumes the nearest preceding assistant call with the same exact function
// name. A call ID is consumed at most once, which prevents two missing-ID tool
// responses from being silently attached to the same upstream tool invocation.
func (association *toolCallAssociation) resolveResponseID(name, explicitID, fallbackID string) string {
	explicitID = strings.TrimSpace(explicitID)
	if explicitID != "" {
		association.consumeID(explicitID)
		return explicitID
	}
	if association != nil {
		name = strings.TrimSpace(name)
		pending := association.pendingByName[name]
		if count := len(pending); count > 0 {
			resolved := pending[count-1]
			if count == 1 {
				delete(association.pendingByName, name)
			} else {
				association.pendingByName[name] = pending[:count-1]
			}
			return resolved
		}
	}
	return fallbackID
}

func (association *toolCallAssociation) consumeID(callID string) {
	if association == nil || callID == "" {
		return
	}
	for name, pending := range association.pendingByName {
		for index := len(pending) - 1; index >= 0; index-- {
			if pending[index] != callID {
				continue
			}
			pending = append(pending[:index], pending[index+1:]...)
			if len(pending) == 0 {
				delete(association.pendingByName, name)
			} else {
				association.pendingByName[name] = pending
			}
			return
		}
	}
}
