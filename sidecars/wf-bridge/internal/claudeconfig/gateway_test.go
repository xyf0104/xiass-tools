package claudeconfig

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestApplyCredentialModesAreMutuallyExclusive(t *testing.T) {
	manager := newTestManager(t)
	writeSettings(t, manager, []byte(`{
  "model": "old-model",
  "apiKeyHelper": "~/bin/old-helper",
  "env": {
    "KEEP_ME": "preserved",
    "ANTHROPIC_AUTH_TOKEN": "old-bearer",
    "ANTHROPIC_API_KEY": "old-api-key"
  }
}
`))

	if _, err := manager.Apply(ApplyConfig{
		BaseURL:                     "https://gateway.example.test",
		CredentialMode:              CredentialModeAPIKey,
		Credential:                  newTestToken,
		EnableGatewayModelDiscovery: true,
		Model:                       "claude-opus-4-6",
	}); err != nil {
		t.Fatalf("API key Apply() error = %v", err)
	}
	written, err := os.ReadFile(manager.SettingsPath)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(written, &root); err != nil {
		t.Fatal(err)
	}
	var env map[string]string
	env = nil
	if err := json.Unmarshal(root["env"], &env); err != nil {
		t.Fatal(err)
	}
	if env["KEEP_ME"] != "preserved" || env["ANTHROPIC_API_KEY"] != newTestToken || env["ANTHROPIC_AUTH_TOKEN"] != "" || env["CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY"] != "1" {
		t.Fatalf("API key configuration did not replace conflicting credentials: %#v", env)
	}
	if _, exists := root["apiKeyHelper"]; exists {
		t.Fatalf("stale apiKeyHelper was retained: %s", written)
	}
	snapshot, err := manager.Inspect()
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Managed || !snapshot.CredentialConfigured || snapshot.CredentialMode != CredentialModeAPIKey || snapshot.AuthTokenConfigured || snapshot.APIKeyHelperConfigured || !snapshot.GatewayModelDiscoveryEnabled {
		t.Fatalf("API key snapshot = %#v", snapshot)
	}

	if _, err := manager.Apply(ApplyConfig{
		BaseURL:        "https://gateway.example.test",
		CredentialMode: CredentialModeAPIKeyHelper,
		APIKeyHelper:   "~/bin/get-xiass-claude-key",
		Model:          "claude-sonnet-4-5",
	}); err != nil {
		t.Fatalf("apiKeyHelper Apply() error = %v", err)
	}
	written, err = os.ReadFile(manager.SettingsPath)
	if err != nil {
		t.Fatal(err)
	}
	root = nil
	if err := json.Unmarshal(written, &root); err != nil {
		t.Fatal(err)
	}
	env = nil
	if err := json.Unmarshal(root["env"], &env); err != nil {
		t.Fatal(err)
	}
	if env["ANTHROPIC_API_KEY"] != "" || env["ANTHROPIC_AUTH_TOKEN"] != "" || env["CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY"] != "" {
		t.Fatalf("helper configuration retained conflicting env values: %#v", env)
	}
	var helper string
	if err := json.Unmarshal(root["apiKeyHelper"], &helper); err != nil || helper != "~/bin/get-xiass-claude-key" {
		t.Fatalf("apiKeyHelper = %q / %v", helper, err)
	}
	snapshot, err = manager.Inspect()
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Managed || snapshot.CredentialMode != CredentialModeAPIKeyHelper || snapshot.AuthTokenConfigured || !snapshot.APIKeyHelperConfigured || snapshot.GatewayModelDiscoveryEnabled {
		t.Fatalf("helper snapshot = %#v", snapshot)
	}
}

func TestGatewayDiscoveryAndMessagesUseExpectedAuthenticatedContract(t *testing.T) {
	secret := "gateway-test-secret-must-not-leak"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Anthropic-Version") != anthropicAPIVersion {
			t.Errorf("Anthropic-Version = %q", request.Header.Get("Anthropic-Version"))
		}
		if request.Header.Get("Authorization") != "Bearer "+secret || request.Header.Get("X-Api-Key") != "" {
			t.Errorf("unexpected auth headers: authorization=%q x-api-key=%q", request.Header.Get("Authorization"), request.Header.Get("X-Api-Key"))
		}
		switch request.URL.Path {
		case "/v1/models":
			if request.Method != http.MethodGet {
				t.Errorf("model discovery method = %s", request.Method)
			}
			if request.URL.Query().Get("limit") != "1000" {
				t.Errorf("model discovery limit = %q", request.URL.Query().Get("limit"))
			}
			writer.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(writer, `{"data":[{"id":"claude-opus-4-6","display_name":"Gateway Opus"},{"id":"anthropic-custom","displayName":"Anthropic Custom"},{"id":"vertex_ai/claude-sonnet-4-6","display_name":"Vertex Sonnet"},{"id":"bedrock/anthropic.claude-sonnet-4-5","display_name":"Bedrock Sonnet"},{"id":"gpt-not-eligible","display_name":"Do not return"}]}`)
		case "/v1/messages":
			if request.Method != http.MethodPost {
				t.Errorf("messages method = %s", request.Method)
			}
			if request.URL.Query().Get("beta") != "true" {
				t.Errorf("messages beta query = %q", request.URL.Query().Get("beta"))
			}
			var payload struct {
				Model     string `json:"model"`
				MaxTokens int    `json:"max_tokens"`
				Messages  []struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				} `json:"messages"`
			}
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Errorf("decode test payload: %v", err)
			}
			if payload.Model != "claude-opus-4-6" || payload.MaxTokens != 1 || len(payload.Messages) != 1 || payload.Messages[0].Role != "user" || payload.Messages[0].Content != "Reply with OK." {
				t.Errorf("unexpected test payload: %#v", payload)
			}
			writer.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(writer, `{"id":"msg_gateway_check","type":"message","content":[]}`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	input := GatewayRequest{BaseURL: server.URL, CredentialMode: CredentialModeAuthToken, Credential: secret, Model: "claude-opus-4-6"}
	discovery, err := DiscoverGatewayModels(context.Background(), input)
	if err != nil {
		t.Fatalf("DiscoverGatewayModels() error = %v", err)
	}
	if discovery.HTTPStatus != http.StatusOK || len(discovery.Models) != 4 || discovery.Models[0].ID != "anthropic-custom" || discovery.Models[0].DisplayName != "Anthropic Custom" || discovery.Models[1].ID != "bedrock/anthropic.claude-sonnet-4-5" || discovery.Models[1].DisplayName != "Bedrock Sonnet" || discovery.Models[2].ID != "claude-opus-4-6" || discovery.Models[2].DisplayName != "Gateway Opus" || discovery.Models[3].ID != "vertex_ai/claude-sonnet-4-6" || discovery.Models[3].DisplayName != "Vertex Sonnet" {
		t.Fatalf("unexpected discovery result: %#v", discovery)
	}
	if strings.Contains(mustJSON(t, discovery), secret) {
		t.Fatalf("discovery result leaked credential: %#v", discovery)
	}
	testResult, err := TestGatewayMessages(context.Background(), input)
	if err != nil {
		t.Fatalf("TestGatewayMessages() error = %v", err)
	}
	if testResult.HTTPStatus != http.StatusOK || testResult.DurationMS < 0 || strings.Contains(mustJSON(t, testResult), secret) {
		t.Fatalf("unexpected messages result: %#v", testResult)
	}
}

func TestGatewayDiscoveryUsesClaudeCodeTimeoutAndLimit(t *testing.T) {
	input := GatewayRequest{BaseURL: "https://gateway.example.test", CredentialMode: CredentialModeAuthToken, Credential: "discovery-contract-secret"}
	client := &http.Client{Transport: gatewayRoundTripper(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/v1/models" || request.URL.Query().Get("limit") != "1000" {
			t.Fatalf("discovery request = %s", request.URL.String())
		}
		deadline, ok := request.Context().Deadline()
		if !ok {
			t.Fatal("discovery request has no deadline")
		}
		remaining := time.Until(deadline)
		if remaining <= 0 || remaining > gatewayDiscoveryRequestTimeout || remaining < gatewayDiscoveryRequestTimeout-time.Second {
			t.Fatalf("discovery deadline remaining = %s, want approximately %s", remaining, gatewayDiscoveryRequestTimeout)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"data":[{"id":"claude-discovery-contract"}]}`)),
		}, nil
	})}
	result, err := discoverGatewayModels(context.Background(), input, client)
	if err != nil || len(result.Models) != 1 || result.Models[0].ID != "claude-discovery-contract" {
		t.Fatalf("discovery contract result = %#v / %v", result, err)
	}
	if got := newGatewayHTTPClient(gatewayDiscoveryRequestTimeout).Timeout; got != gatewayDiscoveryRequestTimeout {
		t.Fatalf("discovery HTTP timeout = %s, want %s", got, gatewayDiscoveryRequestTimeout)
	}
}

func TestGatewayMessagesRejectsMalformedSuccessfulResponses(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{name: "empty", body: ""},
		{name: "html", body: "<html>sign in</html>"},
		{name: "empty object", body: `{}`},
		{name: "error envelope", body: `{"error":{"message":"unavailable"}}`},
		{name: "missing message ID", body: `{"type":"message","content":[]}`},
		{name: "invalid content", body: `{"id":"msg_invalid","type":"message","content":null}`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path != "/v1/messages" {
					t.Fatalf("test path = %s", request.URL.Path)
				}
				writer.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(writer, testCase.body)
			}))
			defer server.Close()
			result, err := TestGatewayMessages(context.Background(), GatewayRequest{BaseURL: server.URL, CredentialMode: CredentialModeAuthToken, Credential: "malformed-response-secret", Model: "claude-test"})
			if !errors.Is(err, errGatewayInvalidResponse) || result.HTTPStatus != http.StatusOK {
				t.Fatalf("malformed success result = %#v / %v", result, err)
			}
		})
	}
}

func TestGatewayDiscoveryConflictIsVisibleWithoutExposingProviderSettings(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		conflictKey string
	}{
		{name: "provider routing", conflictKey: "CLAUDE_CODE_USE_VERTEX"},
		{name: "nonessential traffic", conflictKey: "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			manager := newTestManager(t)
			secret := "user-managed-claude-route-secret"
			writeSettings(t, manager, []byte(`{
  "env": {
    "`+testCase.conflictKey+`": "1",
    "KEEP_ME": "`+secret+`"
  }
}
`))
			if _, err := manager.Apply(ApplyConfig{
				BaseURL:                     "https://gateway.example.test",
				CredentialMode:              CredentialModeAuthToken,
				Credential:                  newTestToken,
				EnableGatewayModelDiscovery: true,
				Model:                       "claude-gateway-conflict-test",
			}); err != nil {
				t.Fatalf("Apply() error = %v", err)
			}
			snapshot, err := manager.Inspect()
			if err != nil {
				t.Fatal(err)
			}
			if !snapshot.GatewayModelDiscoveryEnabled || !snapshot.GatewayModelDiscoveryBlocked {
				t.Fatalf("gateway discovery conflict snapshot = %#v", snapshot)
			}
			encodedSnapshot := mustJSON(t, snapshot)
			if strings.Contains(encodedSnapshot, secret) || strings.Contains(encodedSnapshot, testCase.conflictKey) {
				t.Fatalf("snapshot exposed user-managed setting: %s", encodedSnapshot)
			}
			written, err := os.ReadFile(manager.SettingsPath)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(written), testCase.conflictKey) || !strings.Contains(string(written), secret) {
				t.Fatalf("Apply() changed preserved provider configuration: %s", written)
			}
		})
	}
}

func TestGatewayAPIKeyDoesNotFollowRedirectAndHelperIsNeverRun(t *testing.T) {
	secret := "api-key-test-secret-must-not-leak"
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		t.Fatalf("redirect target was called with headers: %#v", request.Header)
	}))
	defer redirectTarget.Close()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-Api-Key") != secret || request.Header.Get("Authorization") != "" {
			t.Errorf("unexpected API key headers: authorization=%q x-api-key=%q", request.Header.Get("Authorization"), request.Header.Get("X-Api-Key"))
		}
		http.Redirect(writer, request, redirectTarget.URL+"/v1/models", http.StatusFound)
	}))
	defer server.Close()

	result, err := DiscoverGatewayModels(context.Background(), GatewayRequest{BaseURL: server.URL, CredentialMode: CredentialModeAPIKey, Credential: secret})
	var httpError *GatewayHTTPError
	if !errors.As(err, &httpError) || httpError.StatusCode != http.StatusFound || result.HTTPStatus != http.StatusFound {
		t.Fatalf("redirect result = %#v / %v", result, err)
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(mustJSON(t, result), secret) {
		t.Fatalf("redirect error leaked credential: %v / %#v", err, result)
	}
	_, err = DiscoverGatewayModels(context.Background(), GatewayRequest{BaseURL: server.URL, CredentialMode: CredentialModeAPIKeyHelper, Credential: secret})
	if !errors.Is(err, ErrGatewayHelperCheckUnsupported) {
		t.Fatalf("apiKeyHelper discovery error = %v", err)
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

type gatewayRoundTripper func(*http.Request) (*http.Response, error)

func (roundTrip gatewayRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}
