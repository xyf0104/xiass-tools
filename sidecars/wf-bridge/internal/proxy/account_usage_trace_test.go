package proxy

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAccountBoundStreamUsageTraceIncludesContext(t *testing.T) {
	dir := t.TempDir()
	InitTrace(dir)

	streamOpenAIAttempt(newDownstreamSSEWriter(httptest.NewRecorder()), &http.Response{
		Body: io.NopCloser(strings.NewReader("data: {\"id\":\"chat-1\",\"model\":\"gpt-upstream\",\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2}}\n\n")),
	}, "openai-request", 2, &accountUsageTraceContext{accountID: "acct-openai", model: "gpt-test", provider: "openai", attempt: 2})

	streamOpenAIResponsesAttempt(newDownstreamSSEWriter(httptest.NewRecorder()), &http.Response{
		Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"model\":\"gpt-upstream\",\"usage\":{\"input_tokens\":4,\"output_tokens\":3}}}\n\n")),
	}, "responses-request", 3, &accountUsageTraceContext{accountID: "acct-responses", model: "gpt-image", provider: "responses", attempt: 3})

	streamAnthropicAttempt(newDownstreamSSEWriter(httptest.NewRecorder()), &http.Response{
		Body: io.NopCloser(strings.NewReader("data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg-1\",\"model\":\"claude-upstream\",\"usage\":{\"input_tokens\":5}}}\n\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":4}}\n\ndata: {\"type\":\"message_stop\"}\n\n")),
	}, "anthropic-request", 4, &accountUsageTraceContext{accountID: "acct-anthropic", model: "claude-test", provider: "anthropic", attempt: 4})

	usageByRequest := accountUsageEventsByRequest(t, dir)
	assertAccountUsageTrace(t, usageByRequest["openai-request"], "acct-openai", "gpt-test", "openai", 2)
	assertAccountUsageTrace(t, usageByRequest["responses-request"], "acct-responses", "gpt-image", "responses", 3)
	assertAccountUsageTrace(t, usageByRequest["anthropic-request"], "acct-anthropic", "claude-test", "anthropic", 4)
}

func TestLegacyStreamUsageTraceDoesNotGainAccountFields(t *testing.T) {
	dir := t.TempDir()
	InitTrace(dir)

	streamOpenAIAttempt(newDownstreamSSEWriter(httptest.NewRecorder()), &http.Response{
		Body: io.NopCloser(strings.NewReader("data: {\"id\":\"chat-1\",\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2}}\n\n")),
	}, "legacy-request", 1, nil)

	event := accountUsageEventsByRequest(t, dir)["legacy-request"]
	if event == nil {
		t.Fatal("legacy stream did not emit a usage event")
	}
	for _, field := range []string{"accountId", "model", "provider"} {
		if _, exists := event[field]; exists {
			t.Fatalf("legacy usage unexpectedly included %s: %#v", field, event)
		}
	}
}

func accountUsageEventsByRequest(t *testing.T, dir string) map[string]map[string]any {
	t.Helper()
	file, err := os.Open(filepath.Join(dir, "proxy-trace.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	result := make(map[string]map[string]any)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var event map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatal(err)
		}
		if event["event"] != "usage" {
			continue
		}
		requestID, _ := event["requestId"].(string)
		result[requestID] = event
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func assertAccountUsageTrace(t *testing.T, event map[string]any, accountID, model, provider string, attempt int) {
	t.Helper()
	if event == nil {
		t.Fatal("account-bound stream did not emit a usage event")
	}
	if event["accountId"] != accountID || event["model"] != model || event["provider"] != provider {
		t.Fatalf("usage trace context = %#v", event)
	}
	gotAttempt, ok := event["attempt"].(float64)
	if !ok || int(gotAttempt) != attempt {
		t.Fatalf("usage trace attempt = %#v, want %d", event["attempt"], attempt)
	}
}
