package oauthflow

import (
	"context"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestBeginCreatesDistinctPKCESessions(t *testing.T) {
	flow, err := New(Config{
		AuthorizationURL: "https://accounts.example.test/authorize?tenant=wf",
		TokenURL:         "https://accounts.example.test/token",
		PublicClientID:   "desktop-public-client",
		RedirectURI:      "http://127.0.0.1:38199/callback",
		Scopes:           []string{"openid", "profile", "openid"},
	})
	if err != nil {
		t.Fatal(err)
	}

	first, err := flow.Begin()
	if err != nil {
		t.Fatal(err)
	}
	second, err := flow.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if first.SessionID == second.SessionID || first.State == second.State {
		t.Fatalf("sessions must be unique: first=%+v second=%+v", first, second)
	}

	parsed, err := url.Parse(first.URL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	if query.Get("response_type") != "code" || query.Get("client_id") != "desktop-public-client" || query.Get("redirect_uri") != "http://127.0.0.1:38199/callback" {
		t.Fatalf("authorization query is incomplete: %s", parsed.RawQuery)
	}
	if query.Get("scope") != "openid profile" || query.Get("state") != first.State || query.Get("code_challenge_method") != "S256" {
		t.Fatalf("authorization query has unexpected OAuth parameters: %s", parsed.RawQuery)
	}
	if query.Get("tenant") != "wf" || strings.Contains(first.URL, "code_verifier") {
		t.Fatalf("authorization URL did not preserve safe existing query or leaked verifier: %s", first.URL)
	}

	flow.mu.Lock()
	stored := *flow.sessions[first.SessionID]
	flow.mu.Unlock()
	if len(stored.verifier) < 43 || query.Get("code_challenge") != pkceChallenge(stored.verifier) {
		t.Fatal("invalid S256 PKCE values")
	}
}

func TestExchangeCodePostsPKCEFormAndReturnsToken(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/token" {
			http.NotFound(writer, request)
			return
		}
		if request.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", request.Method)
		}
		if err := request.ParseForm(); err != nil {
			t.Error(err)
			return
		}
		if got, want := request.Form.Get("grant_type"), "authorization_code"; got != want {
			t.Errorf("grant_type = %q, want %q", got, want)
		}
		if got, want := request.Form.Get("client_id"), "desktop-public-client"; got != want {
			t.Errorf("client_id = %q, want %q", got, want)
		}
		if got, want := request.Form.Get("redirect_uri"), server.URL+"/callback"; got != want {
			t.Errorf("redirect_uri = %q, want %q", got, want)
		}
		if request.Form.Get("code") != "code-from-provider" || request.Form.Get("code_verifier") == "" {
			t.Error("missing code or PKCE verifier")
		}
		if request.Form.Get("state") != "" {
			t.Error("state must not be sent to the token endpoint")
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"access_token":"access-token","refresh_token":"refresh-token","id_token":"id-token","token_type":"Bearer","scope":"openid profile","expires_in":3600}`))
	}))
	defer server.Close()

	now := time.Date(2026, time.August, 3, 10, 0, 0, 0, time.UTC)
	flow, err := New(testConfig(server.URL), WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	authorization, err := flow.Begin()
	if err != nil {
		t.Fatal(err)
	}
	token, err := flow.ExchangeCode(context.Background(), authorization.SessionID, "code-from-provider", authorization.State)
	if err != nil {
		t.Fatal(err)
	}
	if token.AccessToken != "access-token" || token.RefreshToken != "refresh-token" || token.IDToken != "id-token" || token.TokenType != "Bearer" || token.Scope != "openid profile" || token.ExpiresIn != 3600 {
		t.Fatalf("unexpected token: %+v", token)
	}
	if want := now.Add(time.Hour); !token.ExpiresAt.Equal(want) {
		t.Fatalf("expires at = %s, want %s", token.ExpiresAt, want)
	}
}

func TestOpenAICodexProfileUsesOfficialHexPKCEVerifier(t *testing.T) {
	var receivedVerifier string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := request.ParseForm(); err != nil {
			t.Error(err)
			return
		}
		receivedVerifier = request.Form.Get("code_verifier")
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"access_token":"access-token"}`))
	}))
	defer server.Close()

	flow, err := New(Config{
		AuthorizationURL:   server.URL + "/authorize",
		TokenURL:           server.URL + "/token",
		PublicClientID:     "openai-public-client",
		RedirectURI:        server.URL + "/callback",
		PKCEVerifierFormat: PKCEVerifierFormatOpenAIHex,
	})
	if err != nil {
		t.Fatal(err)
	}
	authorization, err := flow.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := flow.ExchangeCode(context.Background(), authorization.SessionID, "code", authorization.State); err != nil {
		t.Fatal(err)
	}
	if len(receivedVerifier) != openAIPKCEVerifierBytes*2 {
		t.Fatalf("OpenAI verifier length = %d, want %d", len(receivedVerifier), openAIPKCEVerifierBytes*2)
	}
	decoded, err := hex.DecodeString(receivedVerifier)
	if err != nil || len(decoded) != openAIPKCEVerifierBytes {
		t.Fatalf("OpenAI verifier must be 64 random bytes encoded as lowercase hex: %q (%v)", receivedVerifier, err)
	}
	if receivedVerifier != strings.ToLower(receivedVerifier) {
		t.Fatalf("OpenAI verifier must be lowercase hex: %q", receivedVerifier)
	}
	parsed, err := url.Parse(authorization.URL)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := parsed.Query().Get("code_challenge"), pkceChallenge(receivedVerifier); got != want {
		t.Fatalf("OpenAI PKCE challenge = %q, want S256 verifier challenge %q", got, want)
	}
}

func TestRejectsUnknownPKCEVerifierFormat(t *testing.T) {
	config := testConfig("https://oauth.example.test")
	config.PKCEVerifierFormat = PKCEVerifierFormat("unknown")
	if _, err := New(config); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("unknown verifier format error = %v, want invalid configuration", err)
	}
}

func TestExtractCallbackForms(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  Callback
	}{
		{name: "full URL", input: "https://127.0.0.1:39000/callback?code=abc%2B123&state=state-value", want: Callback{Code: "abc+123", State: "state-value"}},
		{name: "query", input: "code=abc%2B123&state=state-value", want: Callback{Code: "abc+123", State: "state-value"}},
		{name: "raw code", input: "opaque-code+/=", want: Callback{Code: "opaque-code+/="}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ExtractCallback(test.input)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("ExtractCallback(%q) = %+v, want %+v", test.input, got, test.want)
			}
		})
	}
}

func TestStateMismatchDoesNotCallTokenEndpoint(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	flow, err := New(testConfig(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	authorization, err := flow.Begin()
	if err != nil {
		t.Fatal(err)
	}
	_, err = flow.ExchangeCode(context.Background(), authorization.SessionID, "code", "wrong-state")
	if !errors.Is(err, ErrStateMismatch) {
		t.Fatalf("error = %v, want state mismatch", err)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("token endpoint received %d request(s) after state mismatch", got)
	}
}

func TestExpiredSessionIsRejected(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	now := time.Date(2026, time.August, 3, 11, 0, 0, 0, time.UTC)
	flow, err := New(testConfig(server.URL), WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	flow.config.SessionTTL = time.Second
	authorization, err := flow.Begin()
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	_, err = flow.ExchangeCode(context.Background(), authorization.SessionID, "code", authorization.State)
	if !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("error = %v, want expired session rejection", err)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("token endpoint received %d request(s) for expired session", got)
	}
}

func TestSuccessfulExchangeConsumesSession(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"access_token":"access-token"}`))
	}))
	defer server.Close()

	flow, err := New(testConfig(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	authorization, err := flow.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = flow.ExchangeCode(context.Background(), authorization.SessionID, "code", authorization.State); err != nil {
		t.Fatal(err)
	}
	_, err = flow.ExchangeCode(context.Background(), authorization.SessionID, "code", authorization.State)
	if !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("replay error = %v, want consumed session rejection", err)
	}
}

func TestRefreshPostsConfiguredScopesAndRetainsUnrotatedRefreshToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := request.ParseForm(); err != nil {
			t.Error(err)
			return
		}
		if got, want := request.Form.Get("grant_type"), "refresh_token"; got != want {
			t.Errorf("grant_type = %q, want %q", got, want)
		}
		if got, want := request.Form.Get("client_id"), "desktop-public-client"; got != want {
			t.Errorf("client_id = %q, want %q", got, want)
		}
		if got, want := request.Form.Get("refresh_token"), "existing-refresh-token"; got != want {
			t.Errorf("refresh_token = %q, want %q", got, want)
		}
		if got, want := request.Form.Get("scope"), "openid profile"; got != want {
			t.Errorf("scope = %q, want %q", got, want)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"access_token":"new-access-token","expires_in":"120"}`))
	}))
	defer server.Close()

	flow, err := New(testConfig(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	token, err := flow.Refresh(context.Background(), "existing-refresh-token")
	if err != nil {
		t.Fatal(err)
	}
	if token.AccessToken != "new-access-token" || token.RefreshToken != "existing-refresh-token" || token.ExpiresIn != 120 {
		t.Fatalf("unexpected refresh result: %+v", token)
	}
}

func TestOAuthURLsRequireHTTPSExceptLoopback(t *testing.T) {
	valid := Config{
		AuthorizationURL: "https://accounts.example.test/authorize",
		TokenURL:         "https://accounts.example.test/token",
		PublicClientID:   "desktop-public-client",
		RedirectURI:      "http://localhost:38199/callback",
	}
	if _, err := New(valid); err != nil {
		t.Fatalf("localhost HTTP redirect should be allowed: %v", err)
	}

	for name, mutate := range map[string]func(*Config){
		"authorization endpoint": func(config *Config) { config.AuthorizationURL = "http://example.com/authorize" },
		"token endpoint":         func(config *Config) { config.TokenURL = "http://example.com/token" },
		"redirect endpoint":      func(config *Config) { config.RedirectURI = "http://example.com/callback" },
	} {
		t.Run(name, func(t *testing.T) {
			config := valid
			mutate(&config)
			if _, err := New(config); !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("New(%+v) error = %v, want HTTP rejection", config, err)
			}
		})
	}

	loopback := httptest.NewServer(http.NotFoundHandler())
	defer loopback.Close()
	if _, err := New(Config{
		AuthorizationURL: loopback.URL + "/authorize",
		TokenURL:         loopback.URL + "/token",
		PublicClientID:   "desktop-public-client",
		RedirectURI:      loopback.URL + "/callback",
	}); err != nil {
		t.Fatalf("127.0.0.1 HTTP endpoints should be allowed for local callback/test server: %v", err)
	}
}

func TestTokenErrorDoesNotExposeProviderBody(t *testing.T) {
	const sensitiveBody = "provider-internal-secret-value"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = writer.Write([]byte(`{"error_description":"` + sensitiveBody + `"}`))
	}))
	defer server.Close()

	flow, err := New(testConfig(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	authorization, err := flow.Begin()
	if err != nil {
		t.Fatal(err)
	}
	_, err = flow.ExchangeCode(context.Background(), authorization.SessionID, "code", authorization.State)
	if !errors.Is(err, ErrTokenExchange) {
		t.Fatalf("error = %v, want token exchange error", err)
	}
	if strings.Contains(err.Error(), sensitiveBody) {
		t.Fatalf("token error leaked provider body: %v", err)
	}
}

func testConfig(serverURL string) Config {
	return Config{
		AuthorizationURL: serverURL + "/authorize",
		TokenURL:         serverURL + "/token",
		PublicClientID:   "desktop-public-client",
		RedirectURI:      serverURL + "/callback",
		Scopes:           []string{"openid", "profile"},
	}
}
