package proxy

import "strings"

var jsonSchemaTypes = map[string]string{
	"ARRAY":   "array",
	"BOOLEAN": "boolean",
	"INTEGER": "integer",
	"NULL":    "null",
	"NUMBER":  "number",
	"OBJECT":  "object",
	"STRING":  "string",
}

// normalizeJSONSchema converts the protobuf-style upper-case type names used
// by Antigravity into standard JSON Schema names accepted by OpenAI and
// Anthropic. It clones the schema so the original Gemini request is untouched.
func normalizeJSONSchema(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, child := range typed {
			if key == "type" {
				out[key] = normalizeJSONSchemaType(child)
			} else {
				out[key] = normalizeJSONSchema(child)
			}
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, child := range typed {
			out[i] = normalizeJSONSchema(child)
		}
		return out
	default:
		return value
	}
}

func normalizeJSONSchemaType(value any) any {
	switch typed := value.(type) {
	case string:
		if normalized, ok := jsonSchemaTypes[strings.ToUpper(typed)]; ok {
			return normalized
		}
		return typed
	case []any:
		out := make([]any, len(typed))
		for i, child := range typed {
			out[i] = normalizeJSONSchemaType(child)
		}
		return out
	default:
		return value
	}
}
