// Package oauthflow implements a provider-neutral OAuth 2.0 Authorization
// Code flow with PKCE for the desktop application. It intentionally has no
// knowledge of particular providers or UI frameworks.
package oauthflow

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultSessionTTL       = 10 * time.Minute
	maxSessionTTL           = 30 * time.Minute
	defaultHTTPTimeout      = 30 * time.Second
	maxTokenResponseBytes   = 1 << 20
	randomValueBytes        = 32
	openAIPKCEVerifierBytes = 64
	maxExpirationInSeconds  = int64((1<<63 - 1) / int64(time.Second))
)

var (
	// ErrInvalidConfig means a caller did not supply a complete, safe public
	// OAuth client configuration.
	ErrInvalidConfig = errors.New("invalid OAuth configuration")
	// ErrInvalidCallback means callback text did not contain a usable code.
	ErrInvalidCallback = errors.New("invalid OAuth callback")
	// ErrAuthorizationDenied means the authorization server returned an OAuth
	// error instead of an authorization code. Details are intentionally omitted.
	ErrAuthorizationDenied = errors.New("OAuth authorization was denied")
	// ErrSessionNotFound covers expired, consumed, and unknown sessions so an
	// untrusted caller cannot distinguish their existence.
	ErrSessionNotFound = errors.New("OAuth session is unavailable")
	// ErrSessionInProgress prevents concurrent token exchanges for one session.
	ErrSessionInProgress = errors.New("OAuth session exchange is already in progress")
	// ErrStateMismatch protects the authorization-code callback against CSRF.
	ErrStateMismatch = errors.New("OAuth callback state did not match")
	// ErrTokenExchange is returned without provider response text, which may
	// contain sensitive values or implementation details.
	ErrTokenExchange = errors.New("OAuth token exchange failed")
)

// Config contains only public OAuth client settings. A desktop application is
// a public client and must never embed a confidential client secret.
type Config struct {
	AuthorizationURL string
	TokenURL         string
	PublicClientID   string
	RedirectURI      string
	Scopes           []string
	// RefreshScopes optionally narrows the scope value sent on refresh-token
	// requests. When empty, Scopes is retained for backwards compatibility.
	RefreshScopes []string
	// PKCEVerifierFormat selects the representation of the one-time verifier.
	// RFC 7636 base64url is the safe default for custom OAuth. The OpenAI Codex
	// public client is an interoperability exception: its official desktop
	// clients generate a 64-byte lowercase-hex verifier, so the built-in profile
	// explicitly opts into that public, non-secret format.
	PKCEVerifierFormat PKCEVerifierFormat
	// AuthorizationParameters contains provider-documented, non-secret
	// parameters that are appended to the authorization request. Core OAuth and
	// PKCE parameters are always owned by Flow and cannot be overridden here.
	AuthorizationParameters map[string]string
	SessionTTL              time.Duration
}

// PKCEVerifierFormat controls the encoding of a freshly generated verifier.
// It is deliberately a small closed set so callers cannot inject a verifier.
type PKCEVerifierFormat string

const (
	PKCEVerifierFormatBase64URL PKCEVerifierFormat = "base64url"
	PKCEVerifierFormatOpenAIHex PKCEVerifierFormat = "openai_hex"
)

// Authorization is returned by Begin. State is safe to retain beside the
// session ID for manual callback entry; the PKCE verifier is never exposed.
type Authorization struct {
	SessionID string    `json:"sessionId"`
	URL       string    `json:"url"`
	State     string    `json:"state"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// Callback is the normalized data copied from an OAuth redirect. A raw code
// has an empty State; callers then supply their retained Authorization.State to
// ExchangeCode.
type Callback struct {
	Code  string `json:"code"`
	State string `json:"state"`
}

// Token is deliberately limited to OAuth token fields and non-secret metadata.
// It does not retain arbitrary upstream response fields or response bodies.
type Token struct {
	AccessToken  string    `json:"accessToken"`
	RefreshToken string    `json:"refreshToken"`
	IDToken      string    `json:"idToken"`
	TokenType    string    `json:"tokenType"`
	Scope        string    `json:"scope"`
	ExpiresIn    int64     `json:"expiresIn"`
	ExpiresAt    time.Time `json:"expiresAt"`
}

type session struct {
	state       string
	verifier    string
	redirectURI string
	expiresAt   time.Time
	exchanging  bool
}

// Flow owns short-lived in-memory authorization sessions. It is safe for
// concurrent use by the UI and callback handlers.
type Flow struct {
	config Config
	client *http.Client
	now    func() time.Time

	mu       sync.Mutex
	sessions map[string]*session
}

// Option changes local Flow behavior without altering the public OAuth
// configuration. Options exist primarily for app wiring and deterministic tests.
type Option func(*Flow)

// WithHTTPClient sets the HTTP client used for token requests. New clones it
// and always disables redirects before sending forms that contain PKCE data.
func WithHTTPClient(client *http.Client) Option {
	return func(flow *Flow) {
		if client != nil {
			flow.client = cloneHTTPClient(client)
		}
	}
}

// WithClock supplies the clock used for session expiry and ExpiresAt. It is
// useful for deterministic tests and otherwise should not be needed.
func WithClock(now func() time.Time) Option {
	return func(flow *Flow) {
		if now != nil {
			flow.now = now
		}
	}
}

// New validates a caller-supplied public OAuth client configuration. No
// provider URLs, client IDs, or redirect URIs are assumed by this package.
func New(config Config, options ...Option) (*Flow, error) {
	validated, err := normalizeConfig(config)
	if err != nil {
		return nil, err
	}

	flow := &Flow{
		config:   validated,
		client:   cloneHTTPClient(nil),
		now:      time.Now,
		sessions: make(map[string]*session),
	}
	for _, option := range options {
		if option != nil {
			option(flow)
		}
	}
	return flow, nil
}

// Begin creates a new stateful Authorization Code + PKCE session and returns
// the URL the caller should open in a browser.
func (flow *Flow) Begin() (Authorization, error) {
	if flow == nil {
		return Authorization{}, fmt.Errorf("%w: flow is nil", ErrInvalidConfig)
	}

	state, err := randomURLValue()
	if err != nil {
		return Authorization{}, fmt.Errorf("%w: could not generate state", ErrTokenExchange)
	}
	verifier, err := randomPKCEVerifier(flow.config.PKCEVerifierFormat)
	if err != nil {
		return Authorization{}, fmt.Errorf("%w: could not generate verifier", ErrTokenExchange)
	}
	challenge := pkceChallenge(verifier)

	endpoint, err := url.Parse(flow.config.AuthorizationURL)
	if err != nil {
		// New already validated this, but retaining the guard keeps Begin safe if
		// a future internal caller changes Flow construction.
		return Authorization{}, fmt.Errorf("%w: authorization URL", ErrInvalidConfig)
	}
	query := endpoint.Query()
	query.Set("response_type", "code")
	query.Set("client_id", flow.config.PublicClientID)
	query.Set("redirect_uri", flow.config.RedirectURI)
	if len(flow.config.Scopes) > 0 {
		query.Set("scope", strings.Join(flow.config.Scopes, " "))
	}
	query.Set("state", state)
	query.Set("code_challenge", challenge)
	query.Set("code_challenge_method", "S256")
	for key, value := range flow.config.AuthorizationParameters {
		query.Set(key, value)
	}
	endpoint.RawQuery = query.Encode()

	now := flow.now().UTC()
	expiresAt := now.Add(flow.config.SessionTTL)
	for attempts := 0; attempts < 3; attempts++ {
		sessionID, err := randomURLValue()
		if err != nil {
			return Authorization{}, fmt.Errorf("%w: could not generate session ID", ErrTokenExchange)
		}

		flow.mu.Lock()
		flow.discardExpiredLocked(now)
		if _, exists := flow.sessions[sessionID]; !exists {
			flow.sessions[sessionID] = &session{
				state:       state,
				verifier:    verifier,
				redirectURI: flow.config.RedirectURI,
				expiresAt:   expiresAt,
			}
			flow.mu.Unlock()
			return Authorization{SessionID: sessionID, URL: endpoint.String(), State: state, ExpiresAt: expiresAt}, nil
		}
		flow.mu.Unlock()
	}

	return Authorization{}, fmt.Errorf("%w: could not create session", ErrTokenExchange)
}

// ExchangeCallback parses full callback URLs, query strings, or raw codes and
// exchanges a callback carrying its state. For raw-code entry, use
// ExchangeCode with the State returned from Begin.
func (flow *Flow) ExchangeCallback(ctx context.Context, sessionID, input string) (Token, error) {
	callback, err := ExtractCallback(input)
	if err != nil {
		return Token{}, err
	}
	return flow.ExchangeCode(ctx, sessionID, callback.Code, callback.State)
}

// ExchangeCode validates and consumes one short-lived authorization session.
// It never accepts a redirect URI from the caller: the URI used is the one
// retained when Begin created the session.
func (flow *Flow) ExchangeCode(ctx context.Context, sessionID, code, state string) (Token, error) {
	if flow == nil {
		return Token{}, fmt.Errorf("%w: flow is nil", ErrInvalidConfig)
	}
	code = strings.TrimSpace(code)
	if code == "" {
		return Token{}, fmt.Errorf("%w: authorization code is missing", ErrInvalidCallback)
	}

	active, err := flow.startExchange(sessionID, state)
	if err != nil {
		return Token{}, err
	}

	result, err := flow.requestToken(ctx, url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {flow.config.PublicClientID},
		"redirect_uri":  {active.redirectURI},
		"code":          {code},
		"code_verifier": {active.verifier},
	})
	flow.finishExchange(sessionID, err == nil)
	return result, err
}

// Refresh exchanges a refresh token using the configured public client. When
// an upstream does not rotate the refresh token, the input value is retained in
// the result so the caller can safely persist the working credential.
func (flow *Flow) Refresh(ctx context.Context, refreshToken string) (Token, error) {
	if flow == nil {
		return Token{}, fmt.Errorf("%w: flow is nil", ErrInvalidConfig)
	}
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return Token{}, fmt.Errorf("%w: refresh token is missing", ErrInvalidCallback)
	}

	form := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {flow.config.PublicClientID},
		"refresh_token": {refreshToken},
	}
	refreshScopes := flow.config.RefreshScopes
	if len(refreshScopes) == 0 {
		refreshScopes = flow.config.Scopes
	}
	if len(refreshScopes) > 0 {
		form.Set("scope", strings.Join(refreshScopes, " "))
	}

	result, err := flow.requestToken(ctx, form)
	if err != nil {
		return Token{}, err
	}
	if result.RefreshToken == "" {
		result.RefreshToken = refreshToken
	}
	return result, nil
}

// ExtractCallback accepts the three forms a desktop app commonly receives:
// a complete callback URL, a URL query string, or a raw authorization code.
func ExtractCallback(input string) (Callback, error) {
	raw := strings.TrimSpace(input)
	if raw == "" {
		return Callback{}, fmt.Errorf("%w: callback is empty", ErrInvalidCallback)
	}

	if strings.Contains(raw, "://") || strings.HasPrefix(strings.ToLower(raw), "http:") || strings.HasPrefix(strings.ToLower(raw), "https:") {
		parsed, err := url.Parse(raw)
		if err != nil {
			return Callback{}, fmt.Errorf("%w: callback URL", ErrInvalidCallback)
		}
		return callbackFromValues(parsed.Query())
	}

	queryText := strings.TrimPrefix(raw, "?")
	if values, err := url.ParseQuery(queryText); err == nil && hasCallbackFields(values) {
		return callbackFromValues(values)
	} else if err != nil && strings.HasPrefix(raw, "?") {
		return Callback{}, fmt.Errorf("%w: callback query", ErrInvalidCallback)
	}

	return Callback{Code: raw}, nil
}

func callbackFromValues(values url.Values) (Callback, error) {
	if strings.TrimSpace(values.Get("error")) != "" {
		return Callback{}, ErrAuthorizationDenied
	}
	code := strings.TrimSpace(values.Get("code"))
	if code == "" {
		return Callback{}, fmt.Errorf("%w: authorization code is missing", ErrInvalidCallback)
	}
	return Callback{Code: code, State: strings.TrimSpace(values.Get("state"))}, nil
}

func hasCallbackFields(values url.Values) bool {
	_, hasCode := values["code"]
	_, hasState := values["state"]
	_, hasError := values["error"]
	return hasCode || hasState || hasError
}

func (flow *Flow) startExchange(sessionID, state string) (session, error) {
	now := flow.now().UTC()
	flow.mu.Lock()
	defer flow.mu.Unlock()
	flow.discardExpiredLocked(now)

	stored, found := flow.sessions[strings.TrimSpace(sessionID)]
	if !found {
		return session{}, ErrSessionNotFound
	}
	if !constantTimeEqual(stored.state, strings.TrimSpace(state)) {
		return session{}, ErrStateMismatch
	}
	if stored.exchanging {
		return session{}, ErrSessionInProgress
	}
	stored.exchanging = true
	return *stored, nil
}

func (flow *Flow) finishExchange(sessionID string, success bool) {
	flow.mu.Lock()
	defer flow.mu.Unlock()
	stored, found := flow.sessions[sessionID]
	if !found {
		return
	}
	if success {
		delete(flow.sessions, sessionID)
		return
	}
	stored.exchanging = false
}

func (flow *Flow) requestToken(ctx context.Context, form url.Values) (Token, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, flow.config.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return Token{}, fmt.Errorf("%w: could not create request", ErrTokenExchange)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	response, err := flow.client.Do(req)
	if err != nil {
		return Token{}, fmt.Errorf("%w: request failed", ErrTokenExchange)
	}
	defer response.Body.Close()

	body, err := readTokenResponse(response.Body)
	if err != nil {
		return Token{}, err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return Token{}, fmt.Errorf("%w: token endpoint returned HTTP %d", ErrTokenExchange, response.StatusCode)
	}

	token, err := decodeToken(body, flow.now().UTC())
	if err != nil {
		return Token{}, err
	}
	return token, nil
}

func readTokenResponse(body io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, maxTokenResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: could not read token response", ErrTokenExchange)
	}
	if len(data) > maxTokenResponseBytes {
		return nil, fmt.Errorf("%w: token response is too large", ErrTokenExchange)
	}
	return data, nil
}

func decodeToken(data []byte, now time.Time) (Token, error) {
	var response tokenResponse
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&response); err != nil {
		return Token{}, fmt.Errorf("%w: token response is not valid JSON", ErrTokenExchange)
	}
	if strings.TrimSpace(response.AccessToken) == "" {
		return Token{}, fmt.Errorf("%w: token response has no access token", ErrTokenExchange)
	}

	result := Token{
		AccessToken:  strings.TrimSpace(response.AccessToken),
		RefreshToken: strings.TrimSpace(response.RefreshToken),
		IDToken:      strings.TrimSpace(response.IDToken),
		TokenType:    strings.TrimSpace(response.TokenType),
		Scope:        strings.TrimSpace(response.Scope),
	}
	if response.ExpiresIn.set {
		result.ExpiresIn = response.ExpiresIn.value
		if result.ExpiresIn > 0 {
			result.ExpiresAt = now.Add(time.Duration(result.ExpiresIn) * time.Second)
		}
	}
	return result, nil
}

type tokenResponse struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	IDToken      string    `json:"id_token"`
	TokenType    string    `json:"token_type"`
	Scope        string    `json:"scope"`
	ExpiresIn    expiresIn `json:"expires_in"`
}

type expiresIn struct {
	set   bool
	value int64
}

func (value *expiresIn) UnmarshalJSON(data []byte) error {
	raw := strings.TrimSpace(string(data))
	if raw == "" || raw == "null" {
		return nil
	}
	if strings.HasPrefix(raw, "\"") {
		var quoted string
		if err := json.Unmarshal(data, &quoted); err != nil {
			return err
		}
		raw = strings.TrimSpace(quoted)
	}
	seconds, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || seconds < 0 || seconds > maxExpirationInSeconds {
		return errors.New("invalid expires_in")
	}
	value.set = true
	value.value = seconds
	return nil
}

func normalizeConfig(config Config) (Config, error) {
	config.AuthorizationURL = strings.TrimSpace(config.AuthorizationURL)
	config.TokenURL = strings.TrimSpace(config.TokenURL)
	config.PublicClientID = strings.TrimSpace(config.PublicClientID)
	config.RedirectURI = strings.TrimSpace(config.RedirectURI)
	if config.AuthorizationURL == "" || config.TokenURL == "" || config.PublicClientID == "" || config.RedirectURI == "" {
		return Config{}, fmt.Errorf("%w: authorization URL, token URL, public client ID, and redirect URI are required", ErrInvalidConfig)
	}
	for label, value := range map[string]string{
		"authorization URL": config.AuthorizationURL,
		"token URL":         config.TokenURL,
		"redirect URI":      config.RedirectURI,
	} {
		validated, err := validateOAuthURL(value)
		if err != nil {
			return Config{}, fmt.Errorf("%w: %s", ErrInvalidConfig, label)
		}
		switch label {
		case "authorization URL":
			config.AuthorizationURL = validated
		case "token URL":
			config.TokenURL = validated
		case "redirect URI":
			config.RedirectURI = validated
		}
	}
	config.Scopes = normalizeScopes(config.Scopes)
	config.RefreshScopes = normalizeScopes(config.RefreshScopes)
	config.PKCEVerifierFormat = PKCEVerifierFormat(strings.ToLower(strings.TrimSpace(string(config.PKCEVerifierFormat))))
	switch config.PKCEVerifierFormat {
	case "", PKCEVerifierFormatBase64URL:
		config.PKCEVerifierFormat = PKCEVerifierFormatBase64URL
	case PKCEVerifierFormatOpenAIHex:
		// OpenAI Codex compatibility is intentionally opt-in through the
		// reviewed built-in profile. It still uses crypto/rand and S256.
	default:
		return Config{}, fmt.Errorf("%w: unsupported PKCE verifier format", ErrInvalidConfig)
	}
	parameters, err := normalizeAuthorizationParameters(config.AuthorizationParameters)
	if err != nil {
		return Config{}, err
	}
	config.AuthorizationParameters = parameters
	if config.SessionTTL == 0 {
		config.SessionTTL = defaultSessionTTL
	}
	if config.SessionTTL < 0 {
		return Config{}, fmt.Errorf("%w: session TTL must not be negative", ErrInvalidConfig)
	}
	if config.SessionTTL > maxSessionTTL {
		config.SessionTTL = maxSessionTTL
	}
	return config, nil
}

var reservedAuthorizationParameters = map[string]struct{}{
	"response_type":         {},
	"client_id":             {},
	"redirect_uri":          {},
	"scope":                 {},
	"state":                 {},
	"code_challenge":        {},
	"code_challenge_method": {},
	"code_verifier":         {},
	"grant_type":            {},
	"refresh_token":         {},
	"authorization":         {},
}

func normalizeAuthorizationParameters(values map[string]string) (map[string]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	result := make(map[string]string, len(values))
	for rawKey, rawValue := range values {
		key := strings.TrimSpace(rawKey)
		value := strings.TrimSpace(rawValue)
		if key == "" || value == "" || len(key) > 128 || len(value) > 4096 {
			return nil, fmt.Errorf("%w: invalid authorization parameter", ErrInvalidConfig)
		}
		for _, character := range key {
			if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '_' || character == '-' || character == '.') {
				return nil, fmt.Errorf("%w: invalid authorization parameter", ErrInvalidConfig)
			}
		}
		if _, reserved := reservedAuthorizationParameters[strings.ToLower(key)]; reserved {
			return nil, fmt.Errorf("%w: authorization parameter %q is reserved", ErrInvalidConfig, key)
		}
		result[key] = value
	}
	return result, nil
}

func validateOAuthURL(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return "", errors.New("invalid URL")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "https" && !(scheme == "http" && isLoopbackHost(parsed.Hostname())) {
		return "", errors.New("OAuth endpoints must use HTTPS")
	}
	parsed.Scheme = scheme
	return parsed.String(), nil
}

func isLoopbackHost(host string) bool {
	host = strings.Trim(strings.ToLower(host), "[]")
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func normalizeScopes(scopes []string) []string {
	seen := make(map[string]struct{}, len(scopes))
	result := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		scope = strings.TrimSpace(scope)
		if scope == "" {
			continue
		}
		if _, exists := seen[scope]; exists {
			continue
		}
		seen[scope] = struct{}{}
		result = append(result, scope)
	}
	return result
}

func cloneHTTPClient(client *http.Client) *http.Client {
	if client == nil {
		client = &http.Client{Timeout: defaultHTTPTimeout}
	}
	copy := *client
	if copy.Timeout <= 0 {
		copy.Timeout = defaultHTTPTimeout
	}
	// Token forms contain the authorization code and verifier. Returning the
	// redirect response rather than following it prevents forwarding them to a
	// potentially unrelated host.
	copy.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &copy
}

func randomURLValue() (string, error) {
	bytes := make([]byte, randomValueBytes)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

func randomPKCEVerifier(format PKCEVerifierFormat) (string, error) {
	switch format {
	case PKCEVerifierFormatOpenAIHex:
		bytes := make([]byte, openAIPKCEVerifierBytes)
		if _, err := rand.Read(bytes); err != nil {
			return "", err
		}
		return hex.EncodeToString(bytes), nil
	case PKCEVerifierFormatBase64URL:
		return randomURLValue()
	default:
		// Config normalization makes this unreachable for normal callers, but
		// retain a defensive error in case a future internal caller mutates it.
		return "", fmt.Errorf("unsupported PKCE verifier format")
	}
}

func pkceChallenge(verifier string) string {
	digest := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func constantTimeEqual(left, right string) bool {
	leftHash := sha256.Sum256([]byte(left))
	rightHash := sha256.Sum256([]byte(right))
	return subtle.ConstantTimeCompare(leftHash[:], rightHash[:]) == 1
}

func (flow *Flow) discardExpiredLocked(now time.Time) {
	for sessionID, stored := range flow.sessions {
		if !now.Before(stored.expiresAt) {
			delete(flow.sessions, sessionID)
		}
	}
}
