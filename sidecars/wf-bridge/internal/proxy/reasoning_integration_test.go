package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"antigravity-wf-assistant/internal/storage"
)

// These tests exercise the real Antigravity routing path rather than only the
// request helper.  A local upstream records the JSON that would leave the
// proxy, then returns a minimal valid streaming response so the route must
// complete normally as well.
func TestAntigravityReasoningProfilesReachMatchingUpstreamContract(t *testing.T) {
	t.Run("GPT 5.6 Chat Completions", func(t *testing.T) {
		var received map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if request.URL.Path != "/v1/chat/completions" {
				t.Fatalf("path = %s", request.URL.Path)
			}
			if err := json.NewDecoder(request.Body).Decode(&received); err != nil {
				t.Fatal(err)
			}
			writeOpenAIChatStream(writer, "gpt-ok")
		}))
		defer server.Close()

		model := storage.CustomModel{Name: "models/reasoning-gpt", Provider: "openai", APIURL: server.URL, APIKey: "key", APIStyle: "chat_completions", ExternalModelName: "gpt-5.6-sol", ReasoningEffort: "max"}
		setupAntigravityIntegrationModel(t, model)
		recorder := httptest.NewRecorder()
		handleRequest(recorder, antigravityRequest(model.Name, "reasoning-gpt", textTurn("hello")))
		if recorder.Code != http.StatusOK || received["reasoning_effort"] != "max" {
			t.Fatalf("status=%d upstream=%#v", recorder.Code, received)
		}
	})

	t.Run("GPT 5.6 Responses", func(t *testing.T) {
		var received map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if request.URL.Path != "/v1/responses" {
				t.Fatalf("path = %s", request.URL.Path)
			}
			if err := json.NewDecoder(request.Body).Decode(&received); err != nil {
				t.Fatal(err)
			}
			writer.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(writer, `data: {"type":"response.completed","response":{"id":"reasoning-response","model":"gpt-5.6-sol","output":[{"type":"message","content":[{"type":"output_text","text":"responses-ok"}]}]}}`+"\n\n")
		}))
		defer server.Close()

		model := storage.CustomModel{Name: "models/reasoning-responses", Provider: "openai", APIURL: server.URL, APIKey: "key", APIStyle: "responses", ExternalModelName: "gpt-5.6-sol", ReasoningEffort: "xhigh"}
		setupAntigravityIntegrationModel(t, model)
		recorder := httptest.NewRecorder()
		handleRequest(recorder, antigravityRequest(model.Name, "reasoning-responses", textTurn("hello")))
		reasoning, _ := received["reasoning"].(map[string]any)
		if recorder.Code != http.StatusOK || reasoning["effort"] != "xhigh" {
			t.Fatalf("status=%d upstream=%#v", recorder.Code, received)
		}
	})

	t.Run("DeepSeek V4 OpenAI compatibility", func(t *testing.T) {
		var received map[string]any
		enabled := true
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if err := json.NewDecoder(request.Body).Decode(&received); err != nil {
				t.Fatal(err)
			}
			writeOpenAIChatStream(writer, "deepseek-ok")
		}))
		defer server.Close()

		model := storage.CustomModel{Name: "models/reasoning-deepseek", Provider: "openai", APIURL: server.URL, APIKey: "key", APIStyle: "chat_completions", ExternalModelName: "deepseek-v4-pro", ReasoningEffort: "max", ThinkingEnabled: &enabled}
		setupAntigravityIntegrationModel(t, model)
		recorder := httptest.NewRecorder()
		handleRequest(recorder, antigravityRequest(model.Name, "reasoning-deepseek", textTurn("hello")))
		thinking, _ := received["thinking"].(map[string]any)
		if recorder.Code != http.StatusOK || received["reasoning_effort"] != "max" || thinking["type"] != "enabled" {
			t.Fatalf("status=%d upstream=%#v", recorder.Code, received)
		}
	})

	t.Run("Claude modern effort", func(t *testing.T) {
		var received map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if request.URL.Path != "/v1/messages" {
				t.Fatalf("path = %s", request.URL.Path)
			}
			if err := json.NewDecoder(request.Body).Decode(&received); err != nil {
				t.Fatal(err)
			}
			writer.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(writer, `data: {"type":"message_start","message":{"id":"reasoning-claude","model":"claude-opus-4.7"}}`+"\n\n")
			_, _ = io.WriteString(writer, `data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"claude-ok"}}`+"\n\n")
			_, _ = io.WriteString(writer, `data: {"type":"message_stop"}`+"\n\n")
		}))
		defer server.Close()

		model := storage.CustomModel{Name: "models/reasoning-claude", Provider: "anthropic", APIURL: server.URL, APIKey: "key", APIStyle: "messages", AuthMode: "x_api_key", ExternalModelName: "claude-opus-4.7", ReasoningEffort: "xhigh"}
		setupAntigravityIntegrationModel(t, model)
		recorder := httptest.NewRecorder()
		handleRequest(recorder, antigravityRequest(model.Name, "reasoning-claude", textTurn("hello")))
		outputConfig, _ := received["output_config"].(map[string]any)
		if recorder.Code != http.StatusOK || outputConfig["effort"] != "xhigh" {
			t.Fatalf("status=%d upstream=%#v", recorder.Code, received)
		}
		if _, hasLegacyThinking := received["thinking"]; hasLegacyThinking {
			t.Fatalf("modern Claude received legacy thinking payload: %#v", received["thinking"])
		}
	})
}
