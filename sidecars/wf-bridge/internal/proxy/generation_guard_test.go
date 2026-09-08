package proxy

import "testing"

func TestGenerationGuardSuppressesOverlappingAttemptsWithNewRequestID(t *testing.T) {
	request := map[string]any{
		"contents": []any{map[string]any{
			"role":  "user",
			"parts": []any{map[string]any{"text": "只生成一次"}},
		}},
	}

	firstRelease, accepted := reserveGeneration("models/custom-gpt", "attempt-one", request)
	if !accepted {
		t.Fatal("first generation was unexpectedly rejected")
	}
	defer firstRelease()

	secondRelease, accepted := reserveGeneration("models/custom-gpt", "attempt-two", request)
	if accepted || secondRelease != nil {
		t.Fatal("an overlapping semantic duplicate with a new request ID was not suppressed")
	}

	// The guard only covers overlap. Once the original has finished, an
	// intentional user retry must be allowed through.
	firstRelease()
	thirdRelease, accepted := reserveGeneration("models/custom-gpt", "attempt-three", request)
	if !accepted || thirdRelease == nil {
		t.Fatal("a completed generation incorrectly blocked a later retry")
	}
	thirdRelease()
}

func TestGenerationFingerprintSeparatesDifferentTurns(t *testing.T) {
	first := generationFingerprint("models/custom-gpt", "one", map[string]any{"contents": []any{"first"}})
	second := generationFingerprint("models/custom-gpt", "two", map[string]any{"contents": []any{"second"}})
	if first == second {
		t.Fatal("different chat turns shared an in-flight generation key")
	}
}

func TestGenerationFingerprintIgnoresAttemptMetadata(t *testing.T) {
	first := generationFingerprint("models/custom-gpt", "one", map[string]any{
		"requestId": "one", "contents": []any{"same turn"},
	})
	second := generationFingerprint("models/custom-gpt", "two", map[string]any{
		"requestId": "two", "contents": []any{"same turn"},
	})
	if first != second {
		t.Fatal("transport attempt metadata changed the semantic generation key")
	}
}
