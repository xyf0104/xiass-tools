package proxy

import "testing"

func TestDiagnosticsTreatsCanceledModelFetchAsCancellation(t *testing.T) {
	InitTrace(t.TempDir())
	trace("model-response-error", map[string]any{
		"statusCode": 400,
		"encoding":   "gzip",
		"message":    "upstream rejected request",
	})

	trace("model-response-error", map[string]any{
		"message": "context canceled",
	})
	diagnostics := GetDiagnostics()
	if !diagnostics.LastModelRequestCanceled {
		t.Fatalf("canceled model request was not marked: %+v", diagnostics)
	}
	if diagnostics.LastError != "" || diagnostics.LastModelStatusCode != 0 || diagnostics.LastModelContentEncoding != "" {
		t.Fatalf("canceled model request retained stale error metadata: %+v", diagnostics)
	}

	// Chat-stream cancellation is unrelated to model-list injection and must
	// not overwrite the status shown on the dashboard.
	trace("openai-stream-error", map[string]any{"message": "context canceled"})
	diagnostics = GetDiagnostics()
	if !diagnostics.LastModelRequestCanceled || diagnostics.LastError != "" || diagnostics.LastModelStatusCode != 0 {
		t.Fatalf("non-model cancellation overwrote model diagnostics: %+v", diagnostics)
	}

	trace("models-injected", map[string]any{"customCount": 1})
	diagnostics = GetDiagnostics()
	if diagnostics.LastModelRequestCanceled {
		t.Fatalf("successful injection did not clear cancellation state: %+v", diagnostics)
	}
}
