package stats

import (
	"os"
	"path/filepath"
	"testing"
)

func TestComputeAggregatesCacheUsage(t *testing.T) {
	dir := t.TempDir()
	trace := `{ "event": "generation-request", "customMatched": true }
{ "event": "usage", "promptTokens": 1000, "completionTokens": 200, "cacheReadTokens": 700, "cacheWriteTokens": 100 }
`
	if err := os.WriteFile(filepath.Join(dir, "proxy-trace.jsonl"), []byte(trace), 0o644); err != nil {
		t.Fatal(err)
	}

	got := Compute(dir)
	if got.CacheReadTokens != 700 {
		t.Errorf("cache read = %d, want 700", got.CacheReadTokens)
	}
	if got.CacheWriteTokens != 100 {
		t.Errorf("cache write = %d, want 100", got.CacheWriteTokens)
	}
	if got.PromptTokens != 1000 || got.TotalTokens != 1200 {
		t.Errorf("token totals = prompt %d, total %d", got.PromptTokens, got.TotalTokens)
	}
	const wantHitRate = 700.0 / 900.0
	if got.CacheHitRate != wantHitRate {
		t.Errorf("cache hit rate = %f, want %f", got.CacheHitRate, wantHitRate)
	}
}

func TestComputeAccountUsageGroupsBoundAccountTraces(t *testing.T) {
	dir := t.TempDir()
	trace := `{ "event": "usage", "accountId": "acct-one", "requestId": "req-one", "attempt": 1, "time": "2026-08-03T01:00:00Z", "promptTokens": 100, "completionTokens": 20, "cacheReadTokens": 60, "cacheWriteTokens": 10 }
{ "event": "usage", "accountId": "acct-one", "requestId": "req-one", "attempt": 1, "time": "2026-08-03T01:01:00Z", "promptTokens": 999, "completionTokens": 999 }
{ "event": "usage", "accountId": "acct-one", "requestId": "req-two", "attempt": 2, "time": "2026-08-03T02:00:00Z", "promptTokens": 40, "completionTokens": 4, "cacheWriteTokens": 2 }
{ "event": "usage", "accountId": "acct-two", "requestId": "req-one", "attempt": 1, "time": "2026-08-03T03:00:00Z", "promptTokens": 3, "completionTokens": 4 }
{ "event": "usage", "requestId": "legacy", "promptTokens": 500, "completionTokens": 500 }
`
	if err := os.WriteFile(filepath.Join(dir, "proxy-trace.jsonl"), []byte(trace), 0o644); err != nil {
		t.Fatal(err)
	}

	got := ComputeAccountUsage(dir)
	if len(got) != 2 {
		t.Fatalf("account usages = %#v, want two bound accounts", got)
	}
	first := got["acct-one"]
	if first.AccountID != "acct-one" || first.RequestCount != 2 {
		t.Fatalf("first account identity/count = %+v", first)
	}
	if first.PromptTokens != 140 || first.CompletionTokens != 24 || first.TotalTokens != 164 {
		t.Fatalf("first account token totals = %+v", first)
	}
	if first.CacheReadTokens != 60 || first.CacheWriteTokens != 12 {
		t.Fatalf("first account cache totals = %+v", first)
	}
	if first.LastUsedAt != "2026-08-03T02:00:00Z" {
		t.Fatalf("first account last used = %q", first.LastUsedAt)
	}

	second := got["acct-two"]
	if second.RequestCount != 1 || second.PromptTokens != 3 || second.CompletionTokens != 4 || second.TotalTokens != 7 {
		t.Fatalf("second account totals = %+v", second)
	}
}
