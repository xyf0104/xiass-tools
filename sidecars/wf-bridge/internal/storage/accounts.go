package storage

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// OpenAICodexOAuthUpstream marks an OAuth account whose access token is used
// directly with the ChatGPT Codex backend. It is deliberately public routing
// metadata, not a credential. Keeping it on the account prevents a Codex
// access token from being accidentally sent to a generic API-key gateway.
const OpenAICodexOAuthUpstream = "openai_codex"

// OpenAICodexResponsesURL is the direct Responses endpoint used by the local
// XIASS OpenAI OAuth implementation. It is kept here so account migration and
// the scheduler share one stable, non-secret route.
const OpenAICodexResponsesURL = "https://chatgpt.com/backend-api/codex/responses"

// UpstreamAccount is a reusable credential and scheduling unit. Models can
// bind one or more account IDs; the local proxy then selects an eligible
// account without exposing account rotation to Antigravity.
//
// Credentials intentionally accepts imported JSON from common XIASS-style
// exports. APIKey remains the canonical value for a manually-created account;
// EffectiveAPIKey reads both forms so imports do not need a lossy conversion.
type UpstreamAccount struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	Notes           string            `json:"notes,omitempty"`
	Provider        string            `json:"provider"`
	Type            string            `json:"type"`
	APIURL          string            `json:"apiUrl"`
	EndpointMode    string            `json:"endpointMode,omitempty"`
	APIStyle        string            `json:"apiStyle,omitempty"`
	MessagePathMode string            `json:"messagePathMode,omitempty"`
	AuthMode        string            `json:"authMode,omitempty"`
	AuthHeader      string            `json:"authHeader,omitempty"`
	Headers         map[string]string `json:"headers,omitempty"`
	// QuotaURL is an optional, provider-documented full endpoint queried only
	// when the user explicitly requests an upstream quota refresh.
	QuotaURL string `json:"quotaUrl,omitempty"`
	// OAuth contains only public OAuth client configuration. Access and refresh
	// tokens remain inside Credentials and are never returned to the renderer.
	OAuth       OAuthConfiguration `json:"oauth,omitempty"`
	APIKey      string             `json:"apiKey,omitempty"`
	Credentials map[string]any     `json:"credentials,omitempty"`
	// Identity is display-only account metadata. Its Source makes clear whether
	// it came from an OAuth response, an imported JSON file, or has not yet
	// been verified by an upstream provider.
	Identity AccountIdentity `json:"identity,omitempty"`
	// AuthExpiresAt describes the access-token lifetime. It is deliberately
	// separate from ExpiresAt, which means the local scheduler must stop using
	// an account at a user-configured time.
	AuthExpiresAt  string        `json:"authExpiresAt,omitempty"`
	Quota          QuotaSnapshot `json:"quota,omitempty"`
	Enabled        bool          `json:"enabled"`
	Priority       int           `json:"priority"`
	MaxConcurrency int           `json:"maxConcurrency"`
	ExpiresAt      string        `json:"expiresAt,omitempty"`
	LastUsedAt     string        `json:"lastUsedAt,omitempty"`
	LastSuccessAt  string        `json:"lastSuccessAt,omitempty"`
	LastError      string        `json:"lastError,omitempty"`
	FailureCount   int           `json:"failureCount,omitempty"`
	CooldownUntil  string        `json:"cooldownUntil,omitempty"`
	CreatedAt      string        `json:"createdAt,omitempty"`
	UpdatedAt      string        `json:"updatedAt,omitempty"`
	ActiveRequests int           `json:"activeRequests,omitempty"`
}

// OAuthConfiguration is a public-client Authorization Code + PKCE setup.
// WF intentionally does not store a client secret: a desktop app is not a
// confidential OAuth client. The provider must have registered the redirect
// URI and client ID entered by the account owner.
type OAuthConfiguration struct {
	// Upstream identifies a built-in OAuth transport. Empty means generic OAuth
	// and preserves the user's custom API endpoint. The current built-in value
	// is openai_codex, which uses ChatGPT's Codex Responses transport.
	Upstream         string `json:"upstream,omitempty"`
	AuthorizationURL string `json:"authorizationUrl,omitempty"`
	TokenURL         string `json:"tokenUrl,omitempty"`
	ClientID         string `json:"clientId,omitempty"`
	RedirectURI      string `json:"redirectUri,omitempty"`
	Scopes           string `json:"scopes,omitempty"`
	// RefreshScopes is an optional provider-specific scope set for refresh
	// grants. It is public OAuth-client metadata (not a token) and lets a
	// profile use the narrower scope required by its token endpoint while
	// keeping the original authorization scopes intact.
	RefreshScopes string `json:"refreshScopes,omitempty"`
}

// AccountIdentity is metadata shown beside a credential. It is never used to
// authorize requests or infer billing entitlement.
type AccountIdentity struct {
	Email   string `json:"email,omitempty"`
	Subject string `json:"subject,omitempty"`
	// ChatGPTAccountID and ChatGPTUserID are display-only identifiers returned
	// by OpenAI/Codex OAuth claims or an account export. They are kept separate
	// from OrganizationID because a ChatGPT account can belong to more than one
	// organization/workspace.
	ChatGPTAccountID      string `json:"chatgptAccountId,omitempty"`
	ChatGPTUserID         string `json:"chatgptUserId,omitempty"`
	Plan                  string `json:"plan,omitempty"`
	OrganizationID        string `json:"organizationId,omitempty"`
	SubscriptionExpiresAt string `json:"subscriptionExpiresAt,omitempty"`
	PrivacyMode           string `json:"privacyMode,omitempty"`
	Source                string `json:"source,omitempty"`
	UpdatedAt             string `json:"updatedAt,omitempty"`
}

// QuotaSnapshot only contains values directly observed in upstream response
// headers or a user-requested quota endpoint. Empty fields mean the upstream
// did not expose that value; callers must not turn absence into a zero quota.
type QuotaSnapshot struct {
	Available         bool   `json:"available,omitempty"`
	Source            string `json:"source,omitempty"`
	UpdatedAt         string `json:"updatedAt,omitempty"`
	StatusCode        int    `json:"statusCode,omitempty"`
	RequestsRemaining string `json:"requestsRemaining,omitempty"`
	TokensRemaining   string `json:"tokensRemaining,omitempty"`
	RequestsReset     string `json:"requestsReset,omitempty"`
	TokensReset       string `json:"tokensReset,omitempty"`
	RetryAfter        string `json:"retryAfter,omitempty"`
	Message           string `json:"message,omitempty"`
	// Windows preserves the actual quota windows returned by a provider (for
	// example the OpenAI/Codex 5h and 7d windows). Empty windows mean that the
	// upstream did not expose detailed account quota information.
	Windows   []QuotaWindow `json:"windows,omitempty"`
	Plan      string        `json:"plan,omitempty"`
	Email     string        `json:"email,omitempty"`
	AccountID string        `json:"accountId,omitempty"`
}

// QuotaWindow is a typed, credential-free representation of one upstream
// rate-limit window. ResetAt is RFC3339 when the upstream supplied an absolute
// timestamp; ResetAfterSeconds is retained when it only supplied a duration.
type QuotaWindow struct {
	Label              string  `json:"label"`
	UsedPercent        float64 `json:"usedPercent"`
	LimitWindowSeconds int64   `json:"limitWindowSeconds,omitempty"`
	ResetAfterSeconds  int64   `json:"resetAfterSeconds,omitempty"`
	ResetAt            string  `json:"resetAt,omitempty"`
	Allowed            bool    `json:"allowed"`
	LimitReached       bool    `json:"limitReached,omitempty"`
}

type accountsStore struct {
	Accounts []UpstreamAccount `json:"accounts"`
}

type AccountImportResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
	Added   int    `json:"added"`
}

// AccountLease represents one in-flight request reservation. Finish must be
// called once the upstream response is complete or failed so concurrency and
// cooldown state remain accurate.
type AccountLease struct {
	ID              string
	HasAlternatives bool
	once            sync.Once
}

// AccountPoolUnavailableError distinguishes a temporarily unavailable pool
// from a permanently invalid model/account binding. The proxy can wait for a
// short cooldown without turning a recoverable scheduler state into an
// Antigravity "Agent terminated" loop.
type AccountPoolUnavailableError struct {
	RetryAfter time.Duration
	Reason     string
	Retryable  bool
}

func (err *AccountPoolUnavailableError) Error() string {
	if err == nil || strings.TrimSpace(err.Reason) == "" {
		return "绑定账户当前均不可调度：请检查额度、冷却时间或并发限制"
	}
	return err.Reason
}

// AccountPoolRetryAfter returns the minimum time until a temporarily blocked
// account pool should be probed again. It deliberately exposes no account ID
// or credential metadata.
func AccountPoolRetryAfter(err error) (time.Duration, bool) {
	var unavailable *AccountPoolUnavailableError
	if !errors.As(err, &unavailable) || unavailable == nil {
		return 0, false
	}
	return unavailable.RetryAfter, unavailable.Retryable
}

var (
	accountsMu   sync.RWMutex
	accountsFile string
	accountRun   = struct {
		sync.Mutex
		active map[string]int
	}{active: make(map[string]int)}
	quotaRun = struct {
		sync.RWMutex
		values        map[string]QuotaSnapshot
		lastPersisted map[string]time.Time
	}{values: make(map[string]QuotaSnapshot), lastPersisted: make(map[string]time.Time)}
)

func initAccountsFile(dir string) {
	accountsFile = filepath.Join(dir, "upstream_accounts.json")
	_ = os.Chmod(accountsFile, 0o600)
}

func normalizeAccount(account UpstreamAccount) UpstreamAccount {
	wasNew := strings.TrimSpace(account.ID) == ""
	if wasNew {
		account.ID = newAccountID()
	}
	account.Name = strings.TrimSpace(account.Name)
	account.Notes = strings.TrimSpace(account.Notes)
	account.Provider = normalizeAccountProvider(account.Provider)
	account.Type = normalizeAccountType(account.Type)
	// Do not rewrite a manually-entered full endpoint. In particular, a query
	// string or trailing slash may be meaningful to a gateway owned by the user.
	account.APIURL = strings.TrimSpace(account.APIURL)
	if account.APIURL == "" {
		account.APIURL = "https://api.xiass.com"
	}
	account.APIStyle = strings.ToLower(strings.TrimSpace(account.APIStyle))
	account.EndpointMode = normalizeEndpointMode(account.EndpointMode)
	account.MessagePathMode = normalizeMessagePathMode(account.MessagePathMode)
	account.AuthMode = strings.ToLower(strings.TrimSpace(account.AuthMode))
	account.AuthHeader = strings.TrimSpace(account.AuthHeader)
	account.QuotaURL = strings.TrimSpace(account.QuotaURL)
	account.OAuth = normalizeOAuthConfiguration(account.OAuth)
	account.Identity = normalizeAccountIdentity(account.Identity)
	account.AuthExpiresAt = strings.TrimSpace(account.AuthExpiresAt)
	account.Quota = normalizeQuotaSnapshot(account.Quota)
	if account.AuthMode == "" {
		switch account.Type {
		case "x_api_key":
			account.AuthMode = "x_api_key"
		case "custom_header":
			account.AuthMode = "custom_header"
		case "api_key":
			if account.Provider == "anthropic" {
				account.AuthMode = "x_api_key"
			} else {
				account.AuthMode = "bearer"
			}
		default:
			account.AuthMode = "bearer"
		}
	}
	if account.MaxConcurrency <= 0 {
		account.MaxConcurrency = 2
	}
	if account.MaxConcurrency > 32 {
		account.MaxConcurrency = 32
	}
	if account.Priority < 0 {
		account.Priority = 0
	}
	if account.Priority > 1000 {
		account.Priority = 1000
	}
	if wasNew {
		account.Enabled = true
	}
	if account.Name == "" {
		account.Name = accountProviderLabel(account.Provider) + " 账户"
	}
	if account.Headers != nil {
		account.Headers = cloneStringMap(account.Headers)
	}
	if account.Credentials != nil {
		account.Credentials = normalizeImportedCredentials(account.Credentials)
	}
	populateIdentityFromCredentials(&account, "导入的 OAuth 声明")
	normalizeOpenAICodexOAuthAccount(&account)
	now := time.Now().UTC().Format(time.RFC3339)
	if account.CreatedAt == "" {
		account.CreatedAt = now
	}
	account.UpdatedAt = now
	return account
}

func normalizeAccountProvider(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "anthropic", "claude":
		return "anthropic"
	case "grok", "xai", "x.ai":
		return "grok"
	case "gemini", "google", "antigravity":
		// Gemini and Antigravity credentials are normally consumed through an
		// OpenAI-compatible XIASS endpoint, so retain their identity for UI and
		// scheduling but use the compatible request bridge in the proxy.
		return "openai"
	case "custom", "compatible", "openai":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "openai"
	}
}

func accountProviderLabel(provider string) string {
	switch normalizeAccountProvider(provider) {
	case "anthropic":
		return "Claude"
	case "grok":
		return "Grok"
	case "custom":
		return "兼容接口"
	default:
		return "OpenAI"
	}
}

func normalizeAccountType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "api_key", "apikey", "api-key":
		return "api_key"
	case "x_api_key", "x-api-key":
		return "x_api_key"
	case "oauth", "oauth_token", "access_token", "bearer", "bearer_token":
		return "oauth"
	case "auth_json", "auth.json", "auth-json", "oauth_json", "oauth-json", "credential_json", "credential-json", "credentials_json", "credentials-json":
		return "auth_json"
	case "refresh_token", "refresh-token", "refreshtoken", "mobile_rt", "mobile-rt", "mobilert":
		return "refresh_token"
	case "codex_pat", "pat", "personal_access_token", "personal-access-token":
		return "codex_pat"
	case "setup_token", "setup-token":
		return "setup_token"
	case "custom_header", "custom-header":
		return "custom_header"
	case "json", "json_import", "json-import", "raw_json", "raw-json", "account_json", "account-json", "import":
		return "json"
	default:
		return "api_key"
	}
}

// ValidateAdditionalHeaders accepts only ordinary, non-authentication HTTP
// metadata. Authentication belongs in the explicit account credential fields;
// accepting Authorization, Cookie, token, or secret-like headers here would
// make it too easy to persist a browser or desktop session by mistake. A nil
// map intentionally means "not supplied" so a redacted account edit can retain
// existing private metadata without sending it through the renderer.
func ValidateAdditionalHeaders(headers map[string]string) error {
	if headers == nil {
		return nil
	}
	if len(headers) > 32 {
		return fmt.Errorf("附加请求头最多可保存 32 项")
	}
	for rawName, rawValue := range headers {
		name := strings.TrimSpace(rawName)
		value := strings.TrimSpace(rawValue)
		if name == "" || value == "" {
			return fmt.Errorf("附加请求头的名称和值不能为空")
		}
		if len(name) > 256 || len(value) > 8192 {
			return fmt.Errorf("附加请求头过长")
		}
		if strings.ContainsAny(name, "\r\n") || strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("附加请求头不能包含换行符")
		}
		if !validAdditionalHeaderName(name) {
			return fmt.Errorf("附加请求头名称无效：%s", name)
		}
		if importedHeaderIsSensitive(name) {
			return fmt.Errorf("附加请求头 %s 可能包含认证信息；请使用 API Key、Bearer、x-api-key 或自定义认证头", name)
		}
	}
	return nil
}

func validAdditionalHeaderName(value string) bool {
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || strings.ContainsRune("!#$%&'*+-.^_`|~", character) {
			continue
		}
		return false
	}
	return true
}

// compactAccountType deliberately treats punctuation and case variants as the
// same account type. The UI is not the only caller of this package, so the
// storage boundary must not rely on a renderer having normalized the value.
func compactAccountType(value string) string {
	var compact strings.Builder
	for _, character := range strings.ToLower(strings.TrimSpace(value)) {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') {
			compact.WriteRune(character)
		}
	}
	return compact.String()
}

// isRawJSONAccountType identifies an instruction to import credentials, not a
// usable authentication scheme. Raw JSON must be parsed by
// ImportUpstreamAccounts so that only the intended credential fields are kept.
func isRawJSONAccountType(value string) bool {
	switch compactAccountType(value) {
	case "authjson", "oauthjson", "credentialjson", "credentialsjson", "json", "jsonimport", "rawjson", "accountjson", "import":
		return true
	default:
		return false
	}
}

// isRefreshTokenAccountType prevents a refresh token (including Mobile RT)
// from being stored as a bearer/API key. It has to be exchanged first, which
// keeps the account's token lifetime and refresh metadata consistent.
func isRefreshTokenAccountType(value string) bool {
	switch compactAccountType(value) {
	case "refreshtoken", "mobilert", "mobilerefreshtoken":
		return true
	default:
		return false
	}
}

func normalizeMessagePathMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "standard", "compat", "manual":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "auto"
	}
}

func normalizeEndpointMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "manual", "exact", "full", "full_url", "full-url":
		return "manual"
	default:
		return "auto"
	}
}

func newAccountID() string {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("acct-%d", time.Now().UnixNano())
	}
	return "acct-" + hex.EncodeToString(buf)
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

func cloneAnyMap(values map[string]any) map[string]any {
	if len(values) == 0 {
		return nil
	}
	data, err := json.Marshal(values)
	if err != nil {
		return nil
	}
	var cloned map[string]any
	if json.Unmarshal(data, &cloned) != nil {
		return nil
	}
	return cloned
}

// EffectiveAPIKey extracts the usable secret without leaking it to logs.
func (account UpstreamAccount) EffectiveAPIKey() string {
	if value := strings.TrimSpace(account.APIKey); value != "" {
		return value
	}
	return effectiveAPIKeyFromCredentials(account.Credentials)
}

// IsOpenAICodexOAuth reports whether this is the XIASS-compatible direct
// OpenAI OAuth transport. Generic OAuth accounts intentionally remain false:
// their manually configured endpoint and headers must never be rewritten.
func (account UpstreamAccount) IsOpenAICodexOAuth() bool {
	return normalizeAccountProvider(account.Provider) == "openai" &&
		normalizeAccountType(account.Type) == "oauth" &&
		strings.EqualFold(strings.TrimSpace(account.OAuth.Upstream), OpenAICodexOAuthUpstream)
}

func (account UpstreamAccount) ToModel(model CustomModel) CustomModel {
	model.Provider = account.Provider
	model.APIURL = account.APIURL
	model.APIKey = account.EffectiveAPIKey()
	if account.APIStyle != "" {
		model.APIStyle = account.APIStyle
	}
	if account.EndpointMode != "" {
		model.EndpointMode = account.EndpointMode
	}
	if account.MessagePathMode != "" {
		model.MessagePathMode = account.MessagePathMode
	}
	model.AuthMode = account.AuthMode
	model.AuthHeader = account.AuthHeader
	model.Headers = cloneStringMap(account.Headers)
	model.RuntimeOAuthUpstream = account.OAuth.Upstream
	model.RuntimeChatGPTAccountID = account.Identity.ChatGPTAccountID
	if account.IsOpenAICodexOAuth() {
		model.APIURL = OpenAICodexResponsesURL
		model.EndpointMode = "manual"
		model.APIStyle = "responses"
		model.AuthMode = "bearer"
	}
	return model
}

// accountMatchesModelRequestProtocol is a final safety guard for account
// pools created by earlier releases. New discovery imports only merge matching
// route contracts, but an old custom_models.json can still contain account IDs
// whose API surface no longer matches the model that Antigravity selected.
//
// The request body is converted before AcquireAccountForModel runs, so changing
// provider or API style here would send that already-serialized body to an
// incompatible upstream endpoint. API URLs, credentials, and endpoint hosts
// intentionally do not participate in this comparison: different accounts
// may safely rotate across different gateways when they implement the same
// request protocol.
func accountMatchesModelRequestProtocol(account UpstreamAccount, model CustomModel) bool {
	base := normalizeModelDisplayName(model)
	// The OpenAI Codex OAuth route is the one intentional cross-surface
	// exception. forwardOpenAIChat detects it immediately after acquisition and
	// re-enters the Responses converter before it can send a Chat Completions
	// body, so treating it as eligible preserves the guarded OAuth migration
	// path without admitting generic cross-protocol accounts.
	if account.IsOpenAICodexOAuth() && normalizeAccountProvider(base.Provider) == "openai" {
		return true
	}
	attempt := account.ToModel(base)
	return normalizeAccountProvider(base.Provider) == normalizeAccountProvider(attempt.Provider) &&
		effectiveModelRequestAPIStyle(base) == effectiveModelRequestAPIStyle(attempt)
}

// effectiveModelRequestAPIStyle mirrors the route selection relevant to the
// body converter without importing internal/upstream (which depends on this
// storage package). Anthropic always uses the Messages body shape; old or
// unknown OpenAI-compatible settings retain the legacy Chat Completions
// fallback through normalizedDiscoveredModelAPIStyle.
func effectiveModelRequestAPIStyle(model CustomModel) string {
	if normalizeAccountProvider(model.Provider) == "anthropic" {
		return "messages"
	}
	return normalizedDiscoveredModelAPIStyle(model.APIStyle)
}

func LoadUpstreamAccounts() ([]UpstreamAccount, error) {
	accountsMu.RLock()
	defer accountsMu.RUnlock()
	accounts, err := loadAccountsLocked()
	if err != nil {
		return nil, err
	}
	return withActiveRequestCounts(accounts), nil
}

func loadAccountsLocked() ([]UpstreamAccount, error) {
	if accountsFile == "" {
		return nil, nil
	}
	data, err := os.ReadFile(accountsFile)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var store accountsStore
	if err := json.Unmarshal(data, &store); err != nil {
		return nil, err
	}
	for i := range store.Accounts {
		store.Accounts[i] = normalizeLoadedAccount(store.Accounts[i])
	}
	return store.Accounts, nil
}

func normalizeLoadedAccount(account UpstreamAccount) UpstreamAccount {
	account.Name = strings.TrimSpace(account.Name)
	account.Provider = normalizeAccountProvider(account.Provider)
	account.Type = normalizeAccountType(account.Type)
	account.APIURL = strings.TrimSpace(account.APIURL)
	if account.APIURL == "" {
		account.APIURL = "https://api.xiass.com"
	}
	account.MessagePathMode = normalizeMessagePathMode(account.MessagePathMode)
	account.EndpointMode = normalizeEndpointMode(account.EndpointMode)
	account.QuotaURL = strings.TrimSpace(account.QuotaURL)
	account.OAuth = normalizeOAuthConfiguration(account.OAuth)
	account.Identity = normalizeAccountIdentity(account.Identity)
	account.AuthExpiresAt = strings.TrimSpace(account.AuthExpiresAt)
	account.Quota = normalizeQuotaSnapshot(account.Quota)
	account.Credentials = normalizeImportedCredentials(account.Credentials)
	populateIdentityFromCredentials(&account, "导入的 OAuth 声明")
	normalizeOpenAICodexOAuthAccount(&account)
	if account.MaxConcurrency <= 0 {
		account.MaxConcurrency = 2
	}
	if account.MaxConcurrency > 32 {
		account.MaxConcurrency = 32
	}
	return account
}

func normalizeOAuthConfiguration(config OAuthConfiguration) OAuthConfiguration {
	config.Upstream = strings.ToLower(strings.TrimSpace(config.Upstream))
	config.AuthorizationURL = strings.TrimSpace(config.AuthorizationURL)
	config.TokenURL = strings.TrimSpace(config.TokenURL)
	config.ClientID = strings.TrimSpace(config.ClientID)
	config.RedirectURI = strings.TrimSpace(config.RedirectURI)
	config.Scopes = strings.Join(strings.Fields(config.Scopes), " ")
	config.RefreshScopes = strings.Join(strings.Fields(config.RefreshScopes), " ")
	return config
}

func normalizeAccountIdentity(identity AccountIdentity) AccountIdentity {
	identity.Email = strings.TrimSpace(identity.Email)
	identity.Subject = strings.TrimSpace(identity.Subject)
	identity.ChatGPTAccountID = strings.TrimSpace(identity.ChatGPTAccountID)
	identity.ChatGPTUserID = strings.TrimSpace(identity.ChatGPTUserID)
	identity.Plan = strings.TrimSpace(identity.Plan)
	identity.OrganizationID = strings.TrimSpace(identity.OrganizationID)
	identity.SubscriptionExpiresAt = strings.TrimSpace(identity.SubscriptionExpiresAt)
	identity.PrivacyMode = strings.TrimSpace(identity.PrivacyMode)
	identity.Source = strings.TrimSpace(identity.Source)
	identity.UpdatedAt = strings.TrimSpace(identity.UpdatedAt)
	return identity
}

func normalizeQuotaSnapshot(snapshot QuotaSnapshot) QuotaSnapshot {
	snapshot.Source = strings.TrimSpace(snapshot.Source)
	snapshot.UpdatedAt = strings.TrimSpace(snapshot.UpdatedAt)
	snapshot.RequestsRemaining = strings.TrimSpace(snapshot.RequestsRemaining)
	snapshot.TokensRemaining = strings.TrimSpace(snapshot.TokensRemaining)
	snapshot.RequestsReset = strings.TrimSpace(snapshot.RequestsReset)
	snapshot.TokensReset = strings.TrimSpace(snapshot.TokensReset)
	snapshot.RetryAfter = strings.TrimSpace(snapshot.RetryAfter)
	snapshot.Message = strings.TrimSpace(snapshot.Message)
	snapshot.Plan = strings.TrimSpace(snapshot.Plan)
	snapshot.Email = strings.TrimSpace(snapshot.Email)
	snapshot.AccountID = strings.TrimSpace(snapshot.AccountID)
	if len(snapshot.Windows) > 0 {
		windows := make([]QuotaWindow, 0, len(snapshot.Windows))
		for _, window := range snapshot.Windows {
			window.Label = strings.TrimSpace(window.Label)
			window.ResetAt = strings.TrimSpace(window.ResetAt)
			if window.Label == "" {
				continue
			}
			windows = append(windows, window)
		}
		snapshot.Windows = windows
	}
	return snapshot
}

// normalizeOpenAICodexOAuthAccount migrates the old WF profile (which pointed
// an OAuth token at api.xiass.com as if it were an API key) to XIASS's real
// OpenAI OAuth route. Detection is deliberately narrow: a user-created custom
// OAuth account is never changed unless it already carries Codex identity
// metadata, the known public Codex profile client ID, or the Codex endpoint.
func normalizeOpenAICodexOAuthAccount(account *UpstreamAccount) {
	if account == nil || normalizeAccountProvider(account.Provider) != "openai" || normalizeAccountType(account.Type) != "oauth" {
		return
	}
	endpoint := strings.ToLower(strings.TrimSpace(account.APIURL))
	knownProfile := strings.TrimSpace(account.OAuth.ClientID) == "app_EMoamEEZ73f0CkXaXp7hrann"
	hasCodexIdentity := strings.TrimSpace(account.Identity.ChatGPTAccountID) != "" ||
		strings.TrimSpace(account.Identity.ChatGPTUserID) != ""
	isCodexEndpoint := strings.Contains(endpoint, "chatgpt.com/backend-api/codex")
	isLegacyXIASSProfile := knownProfile && (endpoint == "" || strings.Contains(endpoint, "api.xiass.com"))
	if !strings.EqualFold(account.OAuth.Upstream, OpenAICodexOAuthUpstream) && !hasCodexIdentity && !isCodexEndpoint && !isLegacyXIASSProfile {
		return
	}
	account.OAuth.Upstream = OpenAICodexOAuthUpstream
	// A generic, previous-version xiass URL is unsafe for an OAuth access token;
	// use the direct XIASS Codex route. A deliberately entered direct endpoint
	// remains untouched so local tests/proxying can still be used.
	if endpoint == "" || strings.Contains(endpoint, "api.xiass.com") || isCodexEndpoint {
		account.APIURL = OpenAICodexResponsesURL
	}
	account.EndpointMode = "manual"
	account.APIStyle = "responses"
	account.AuthMode = "bearer"
}

// populateIdentityFromCredentials imports non-secret OAuth metadata when it
// is available. JWT content is intentionally labelled as a claim because
// desktop credential import cannot prove the original token signature.
func populateIdentityFromCredentials(account *UpstreamAccount, fallbackSource string) {
	if account == nil {
		return
	}
	account.Credentials = normalizeImportedCredentials(account.Credentials)
	if account.Credentials == nil {
		return
	}
	credentialMaps := importedCredentialMaps(account.Credentials)
	identity := account.Identity
	if identity.Email == "" {
		identity.Email = importedStringFromMaps(credentialMaps, "email", "user_email", "userEmail")
	}
	if identity.Subject == "" {
		identity.Subject = importedStringFromMaps(credentialMaps, "sub", "subject", "user_id", "userId", "chatgpt_user_id")
	}
	if identity.ChatGPTAccountID == "" {
		identity.ChatGPTAccountID = importedStringFromMaps(credentialMaps, "chatgpt_account_id", "chatgptAccountId")
	}
	if identity.ChatGPTUserID == "" {
		identity.ChatGPTUserID = importedStringFromMaps(credentialMaps, "chatgpt_user_id", "chatgptUserId")
	}
	if identity.Plan == "" {
		identity.Plan = importedStringFromMaps(credentialMaps, "plan", "plan_type", "planType", "chatgpt_plan_type", "tier")
	}
	if identity.OrganizationID == "" {
		identity.OrganizationID = importedStringFromMaps(credentialMaps, "organization_id", "organizationId", "org_id", "orgId", "account_id", "accountId", "chatgpt_account_id", "poid")
	}
	if identity.SubscriptionExpiresAt == "" {
		identity.SubscriptionExpiresAt = importedStringFromMaps(credentialMaps, "subscription_expires_at", "subscriptionExpiresAt", "plan_expires_at", "planExpiresAt")
	}
	if identity.PrivacyMode == "" {
		identity.PrivacyMode = importedStringFromMaps(credentialMaps, "privacy_mode", "privacyMode")
	}
	if idToken := importedStringFromMaps(credentialMaps, importedIDTokenKeys...); idToken != "" {
		mergeIdentityClaims(&identity, decodeJWTClaims(idToken))
	}
	if accessToken := importedStringFromMaps(credentialMaps, importedAccessTokenKeys...); accessToken != "" {
		mergeIdentityClaims(&identity, decodeJWTClaims(accessToken))
	}
	if account.AuthExpiresAt == "" {
		account.AuthExpiresAt = importedAuthExpiresAt(account.Credentials)
	}
	// Older exports only carry chatgpt_account_id. Retain the historical
	// organization fallback for those records while keeping a real organization
	// ID separate whenever the provider supplied one.
	if identity.OrganizationID == "" && identity.ChatGPTAccountID != "" {
		identity.OrganizationID = identity.ChatGPTAccountID
	}
	if identity.Email != "" || identity.Subject != "" || identity.ChatGPTAccountID != "" || identity.ChatGPTUserID != "" || identity.Plan != "" || identity.OrganizationID != "" || identity.SubscriptionExpiresAt != "" {
		if identity.Source == "" {
			identity.Source = fallbackSource
		}
		if identity.UpdatedAt == "" {
			identity.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		}
	}
	account.Identity = normalizeAccountIdentity(identity)
}

func decodeJWTClaims(token string) map[string]any {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) < 2 || parts[1] == "" {
		return nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		payload, err = base64.URLEncoding.DecodeString(parts[1])
		if err != nil {
			return nil
		}
	}
	var claims map[string]any
	if json.Unmarshal(payload, &claims) != nil {
		return nil
	}
	return claims
}

func mergeIdentityClaims(identity *AccountIdentity, claims map[string]any) {
	if identity == nil || claims == nil {
		return
	}
	if identity.Email == "" {
		identity.Email = stringValue(claims, "email")
	}
	if identity.Subject == "" {
		identity.Subject = stringValue(claims, "sub", "user_id", "chatgpt_user_id")
	}
	if identity.ChatGPTAccountID == "" {
		identity.ChatGPTAccountID = stringValue(claims, "chatgpt_account_id", "chatgptAccountId")
	}
	if identity.ChatGPTUserID == "" {
		identity.ChatGPTUserID = stringValue(claims, "chatgpt_user_id", "chatgptUserId")
	}
	if identity.Plan == "" {
		identity.Plan = stringValue(claims, "plan", "plan_type", "chatgpt_plan_type")
	}
	if identity.SubscriptionExpiresAt == "" {
		identity.SubscriptionExpiresAt = stringValue(claims, "subscription_expires_at", "subscriptionExpiresAt", "plan_expires_at", "planExpiresAt")
	}
	if identity.OrganizationID == "" {
		identity.OrganizationID = stringValue(claims, "organization_id", "organization", "org_id", "account_id", "poid")
	}
	if authClaims := mapValue(claims, "https://api.openai.com/auth", "auth"); authClaims != nil {
		if identity.Subject == "" {
			identity.Subject = stringValue(authClaims, "user_id", "chatgpt_user_id")
		}
		if identity.ChatGPTAccountID == "" {
			identity.ChatGPTAccountID = stringValue(authClaims, "chatgpt_account_id")
		}
		if identity.ChatGPTUserID == "" {
			identity.ChatGPTUserID = stringValue(authClaims, "chatgpt_user_id")
		}
		if identity.Plan == "" {
			identity.Plan = stringValue(authClaims, "chatgpt_plan_type", "plan_type", "plan")
		}
		if identity.SubscriptionExpiresAt == "" {
			identity.SubscriptionExpiresAt = stringValue(authClaims, "subscription_expires_at", "subscriptionExpiresAt", "plan_expires_at", "planExpiresAt")
		}
		if identity.OrganizationID == "" {
			identity.OrganizationID = stringValue(authClaims, "organization_id", "org_id", "poid")
			if identity.OrganizationID == "" {
				identity.OrganizationID = openAIOrganizationID(authClaims)
			}
		}
	}
	if identity.OrganizationID == "" && identity.ChatGPTAccountID != "" {
		identity.OrganizationID = identity.ChatGPTAccountID
	}
}

// openAIOrganizationID obtains a display-only default organization ID from
// the documented OpenAI ID-token claim. It intentionally makes no authorization
// decision from an unsigned JWT payload.
func openAIOrganizationID(claims map[string]any) string {
	items, ok := claims["organizations"].([]any)
	if !ok {
		return ""
	}
	first := ""
	for _, item := range items {
		organization, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id := stringValue(organization, "id", "organization_id", "organizationId")
		if id == "" {
			continue
		}
		if first == "" {
			first = id
		}
		if enabled, ok := organization["is_default"].(bool); ok && enabled {
			return id
		}
	}
	return first
}

func withActiveRequestCounts(accounts []UpstreamAccount) []UpstreamAccount {
	accountRun.Lock()
	defer accountRun.Unlock()
	quotaRun.RLock()
	defer quotaRun.RUnlock()
	result := make([]UpstreamAccount, len(accounts))
	for i, account := range accounts {
		result[i] = account
		result[i].ActiveRequests = accountRun.active[account.ID]
		if snapshot, ok := quotaRun.values[account.ID]; ok {
			result[i].Quota = snapshot
		}
	}
	return result
}

// ObserveUpstreamQuota records only recognised rate-limit response headers.
// It is intentionally passive: opening the account list never creates an
// extra upstream request. Persisting is throttled because many APIs return
// rate headers on every streaming response.
func ObserveUpstreamQuota(accountID, provider string, statusCode int, headers http.Header) {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" || headers == nil {
		return
	}
	snapshot := QuotaSnapshot{
		Source:     strings.TrimSpace(provider) + " 上游响应头",
		UpdatedAt:  time.Now().UTC().Format(time.RFC3339),
		StatusCode: statusCode,
	}
	if strings.TrimSpace(provider) == "" {
		snapshot.Source = "上游响应头"
	}
	snapshot.RequestsRemaining = firstHeader(headers,
		"X-RateLimit-Remaining-Requests", "Anthropic-Ratelimit-Requests-Remaining")
	snapshot.TokensRemaining = firstHeader(headers,
		"X-RateLimit-Remaining-Tokens", "Anthropic-Ratelimit-Tokens-Remaining")
	snapshot.RequestsReset = firstHeader(headers,
		"X-RateLimit-Reset-Requests", "Anthropic-Ratelimit-Requests-Reset")
	snapshot.TokensReset = firstHeader(headers,
		"X-RateLimit-Reset-Tokens", "Anthropic-Ratelimit-Tokens-Reset")
	snapshot.RetryAfter = firstHeader(headers, "Retry-After")
	snapshot.Available = snapshot.RequestsRemaining != "" || snapshot.TokensRemaining != "" ||
		snapshot.RequestsReset != "" || snapshot.TokensReset != "" || snapshot.RetryAfter != ""
	if !snapshot.Available {
		return
	}

	now := time.Now()
	quotaRun.Lock()
	quotaRun.values[accountID] = snapshot
	lastPersisted := quotaRun.lastPersisted[accountID]
	shouldPersist := lastPersisted.IsZero() || now.Sub(lastPersisted) >= time.Minute
	if shouldPersist {
		quotaRun.lastPersisted[accountID] = now
	}
	quotaRun.Unlock()
	if shouldPersist {
		_ = updateUpstreamAccount(accountID, func(account *UpstreamAccount) {
			account.Quota = snapshot
		})
	}
}

// SaveQuotaSnapshot stores a result from an explicit user-requested quota
// refresh. No proxy request path calls this helper automatically.
func SaveQuotaSnapshot(accountID string, snapshot QuotaSnapshot) error {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return fmt.Errorf("未找到该账户")
	}
	snapshot = normalizeQuotaSnapshot(snapshot)
	if snapshot.UpdatedAt == "" {
		snapshot.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	quotaRun.Lock()
	quotaRun.values[accountID] = snapshot
	quotaRun.lastPersisted[accountID] = time.Now()
	quotaRun.Unlock()
	return updateUpstreamAccount(accountID, func(account *UpstreamAccount) {
		account.Quota = snapshot
		mergeOpenAICodexQuotaIdentity(account, snapshot)
	})
}

// mergeOpenAICodexQuotaIdentity fills display-only fields returned by the
// authenticated Codex quota endpoint. It never replaces an existing OAuth or
// imported identity value: a quota response is useful enrichment, not an
// authority for silently changing which account the user believes is active.
func mergeOpenAICodexQuotaIdentity(account *UpstreamAccount, snapshot QuotaSnapshot) {
	if account == nil || !account.IsOpenAICodexOAuth() {
		return
	}
	changed := false
	if account.Identity.Email == "" && snapshot.Email != "" {
		account.Identity.Email = snapshot.Email
		changed = true
	}
	if account.Identity.Plan == "" && snapshot.Plan != "" {
		account.Identity.Plan = snapshot.Plan
		changed = true
	}
	if account.Identity.ChatGPTAccountID == "" && snapshot.AccountID != "" {
		account.Identity.ChatGPTAccountID = snapshot.AccountID
		changed = true
	}
	if !changed {
		return
	}
	if account.Identity.Source == "" {
		account.Identity.Source = "OpenAI / Codex OAuth 用量接口"
	}
	if snapshot.UpdatedAt != "" {
		account.Identity.UpdatedAt = snapshot.UpdatedAt
	} else {
		account.Identity.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	}
}

func firstHeader(headers http.Header, names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(headers.Get(name)); value != "" {
			return value
		}
	}
	return ""
}

func SaveUpstreamAccount(account UpstreamAccount) error {
	if isRawJSONAccountType(account.Type) {
		return fmt.Errorf("原始 JSON 凭据只能通过“导入账户 JSON”功能导入，不能作为 API Key 直接保存")
	}
	if isRefreshTokenAccountType(account.Type) {
		return fmt.Errorf("Refresh Token / Mobile RT 必须先兑换为 OAuth 访问令牌，不能作为 API Key 直接保存")
	}
	headersProvided := account.Headers != nil
	if err := ValidateAdditionalHeaders(account.Headers); err != nil {
		return err
	}
	account = normalizeAccount(account)
	// normalizeAccount clones empty maps as nil for compact persistence. Here a
	// non-nil empty map has a distinct update meaning: the caller explicitly
	// cleared the private headers, so preserve that intent until the replacement
	// record has been selected below.
	if headersProvided && account.Headers == nil {
		account.Headers = map[string]string{}
	}
	if account.EffectiveAPIKey() == "" {
		return fmt.Errorf("请填写 API Key、访问令牌或导入含凭据的 JSON")
	}
	accountsMu.Lock()
	defer accountsMu.Unlock()
	accounts, err := loadAccountsLocked()
	if err != nil {
		return err
	}
	for i, existing := range accounts {
		if existing.ID == account.ID {
			account.CreatedAt = existing.CreatedAt
			// Account views never include request headers. A nil header map from
			// an edit therefore means "preserve"; an explicit empty map clears
			// the saved additional headers.
			if account.Headers == nil {
				account.Headers = cloneStringMap(existing.Headers)
			}
			accounts[i] = account
			return saveAccountsLocked(accounts)
		}
	}
	accounts = append(accounts, account)
	return saveAccountsLocked(accounts)
}

func GetUpstreamAccount(id string) (UpstreamAccount, error) {
	accountsMu.RLock()
	defer accountsMu.RUnlock()
	accounts, err := loadAccountsLocked()
	if err != nil {
		return UpstreamAccount{}, err
	}
	for _, account := range accounts {
		if account.ID == strings.TrimSpace(id) {
			return account, nil
		}
	}
	return UpstreamAccount{}, fmt.Errorf("未找到该账户")
}

func DeleteUpstreamAccount(id string) error {
	id = strings.TrimSpace(id)
	accountsMu.Lock()
	defer accountsMu.Unlock()
	accounts, err := loadAccountsLocked()
	if err != nil {
		return err
	}
	result := accounts[:0]
	found := false
	for _, account := range accounts {
		if account.ID == id {
			found = true
			continue
		}
		result = append(result, account)
	}
	if !found {
		return fmt.Errorf("未找到该账户")
	}
	if err := saveAccountsLocked(result); err != nil {
		return err
	}
	accountRun.Lock()
	delete(accountRun.active, id)
	accountRun.Unlock()
	return nil
}

func SetUpstreamAccountEnabled(id string, enabled bool) error {
	return updateUpstreamAccount(id, func(account *UpstreamAccount) {
		account.Enabled = enabled
		if enabled {
			account.LastError = ""
			account.FailureCount = 0
			account.CooldownUntil = ""
		}
	})
}

func updateUpstreamAccount(id string, update func(*UpstreamAccount)) error {
	id = strings.TrimSpace(id)
	accountsMu.Lock()
	defer accountsMu.Unlock()
	accounts, err := loadAccountsLocked()
	if err != nil {
		return err
	}
	for i := range accounts {
		if accounts[i].ID != id {
			continue
		}
		update(&accounts[i])
		accounts[i].UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		return saveAccountsLocked(accounts)
	}
	return fmt.Errorf("未找到该账户")
}

func saveAccountsLocked(accounts []UpstreamAccount) error {
	if accountsFile == "" {
		return fmt.Errorf("账户存储尚未初始化")
	}
	data, err := json.MarshalIndent(accountsStore{Accounts: accounts}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(accountsFile), 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(accountsFile), ".upstream-accounts-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(append(data, '\n')); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := replaceStorageFile(tempPath, accountsFile); err != nil {
		return err
	}
	return os.Chmod(accountsFile, 0o600)
}

// AcquireAccountForModel reserves the best usable bound account. Linked
// accounts are sorted by priority, current concurrency and least-recent use;
// accounts in a temporary cooldown are skipped. A model without bindings
// deliberately retains legacy per-model credentials for backward compatibility.
// A model that predates account-pool sync can retain both: its direct credential
// is used only when every bound account is unavailable, so attaching a new pool
// never turns a previously working model into an account-only configuration.
func AcquireAccountForModel(model CustomModel, excluded map[string]struct{}) (CustomModel, *AccountLease, error) {
	ids := normalizedAccountIDs(model.AccountIDs)
	if len(ids) == 0 {
		return model, nil, nil
	}
	// Refresh near-expiry OAuth credentials before evaluating the pool. Failure
	// is recorded on that account and the remaining bound accounts stay eligible.
	for _, accountID := range ids {
		if _, skipped := excluded[accountID]; skipped {
			continue
		}
		_ = EnsureAccountAccessToken(context.Background(), accountID)
	}
	accountsMu.RLock()
	accounts, err := loadAccountsLocked()
	accountsMu.RUnlock()
	if err != nil {
		if hasDirectModelCredential(model) {
			return model, nil, nil
		}
		return model, nil, err
	}
	now := time.Now()
	allowed := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		allowed[id] = struct{}{}
	}
	type candidate struct {
		account UpstreamAccount
		active  int
	}
	var candidates []candidate
	incompatibleProtocol := false
	eligibleCount := 0
	var earliestCooldown time.Duration
	poolBusy := false
	accountRun.Lock()
	for _, account := range accounts {
		if _, ok := allowed[account.ID]; !ok {
			continue
		}
		if _, skip := excluded[account.ID]; skip {
			continue
		}
		if !accountMatchesModelRequestProtocol(account, model) {
			incompatibleProtocol = true
			continue
		}
		if !accountEnabledAndAuthorized(account, now) {
			continue
		}
		eligibleCount++
		if until, ok := parseAccountTime(account.CooldownUntil); ok && until.After(now) {
			remaining := time.Until(until)
			if earliestCooldown <= 0 || remaining < earliestCooldown {
				earliestCooldown = remaining
			}
			continue
		}
		active := accountRun.active[account.ID]
		if active >= account.MaxConcurrency {
			poolBusy = true
			continue
		}
		candidates = append(candidates, candidate{account: account, active: active})
	}
	if len(candidates) > 0 {
		sort.SliceStable(candidates, func(i, j int) bool {
			if candidates[i].account.Priority != candidates[j].account.Priority {
				return candidates[i].account.Priority < candidates[j].account.Priority
			}
			if candidates[i].active != candidates[j].active {
				return candidates[i].active < candidates[j].active
			}
			return candidates[i].account.LastUsedAt < candidates[j].account.LastUsedAt
		})
		accountRun.active[candidates[0].account.ID]++
	}
	accountRun.Unlock()
	if len(candidates) == 0 {
		if hasDirectModelCredential(model) {
			return model, nil, nil
		}
		if incompatibleProtocol {
			return model, nil, fmt.Errorf("绑定账户与当前模型的请求协议不兼容，请重新同步该账户的模型")
		}
		if earliestCooldown > 0 {
			return model, nil, &AccountPoolUnavailableError{
				RetryAfter: earliestCooldown,
				Reason:     "绑定账户正在短暂冷却，XIASS Tools 会在可用后安全重试",
				Retryable:  true,
			}
		}
		if poolBusy {
			return model, nil, &AccountPoolUnavailableError{
				RetryAfter: 350 * time.Millisecond,
				Reason:     "绑定账户当前并发已满，XIASS Tools 会在空闲后安全重试",
				Retryable:  true,
			}
		}
		return model, nil, fmt.Errorf("绑定账户当前均不可调度：请检查凭据、到期时间或账户状态")
	}
	selected := candidates[0].account
	_ = updateUpstreamAccount(selected.ID, func(account *UpstreamAccount) {
		account.LastUsedAt = now.UTC().Format(time.RFC3339)
	})
	return selected.ToModel(model), &AccountLease{ID: selected.ID, HasAlternatives: eligibleCount > 1}, nil
}

func hasDirectModelCredential(model CustomModel) bool {
	return strings.TrimSpace(model.APIKey) != ""
}

func normalizedAccountIDs(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func accountUsable(account UpstreamAccount, now time.Time) bool {
	if !accountEnabledAndAuthorized(account, now) {
		return false
	}
	if until, ok := parseAccountTime(account.CooldownUntil); ok && until.After(now) {
		return false
	}
	return true
}

func accountEnabledAndAuthorized(account UpstreamAccount, now time.Time) bool {
	if !account.Enabled || account.EffectiveAPIKey() == "" {
		return false
	}
	if until, ok := parseAccountTime(account.ExpiresAt); ok && !until.After(now) {
		return false
	}
	if expiresAt, ok := parseAccountTime(account.AuthExpiresAt); ok && !expiresAt.After(now) {
		return false
	}
	return true
}

func parseAccountTime(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339, value)
	return parsed, err == nil
}

// Finish releases an in-flight reservation and records health. statusCode is
// 0 for a network interruption; a 2xx status is a successful request.
func (lease *AccountLease) Finish(statusCode int, _ string, failure string) {
	if lease == nil || lease.ID == "" {
		return
	}
	lease.once.Do(func() {
		accountRun.Lock()
		if accountRun.active[lease.ID] <= 1 {
			delete(accountRun.active, lease.ID)
		} else {
			accountRun.active[lease.ID]--
		}
		accountRun.Unlock()
		_ = updateUpstreamAccount(lease.ID, func(account *UpstreamAccount) {
			now := time.Now().UTC()
			if statusCode >= http.StatusOK && statusCode < http.StatusMultipleChoices && strings.TrimSpace(failure) == "" {
				account.LastSuccessAt = now.Format(time.RFC3339)
				account.LastError = ""
				account.FailureCount = 0
				account.CooldownUntil = ""
				return
			}
			account.FailureCount++
			account.LastError = shortAccountError(failure, statusCode)
			// A gateway/network failure is scoped to the current request only.
			// Keeping a persistent scheduler cooldown made the next independent
			// request skip an account after a one-frame relay outage. The proxy
			// already bounds and pins same-account retries in memory, so retain the
			// health record without placing the account in a cross-request blacklist.
			account.CooldownUntil = ""
		})
	})
}

// Release relinquishes only the in-flight concurrency reservation. It is used
// immediately before a safe same-account retry: recording a cooldown at that
// point would make the scheduler reject the very retry it just approved.
// The final attempt still records success or failure through Finish.
func (lease *AccountLease) Release() {
	if lease == nil || lease.ID == "" {
		return
	}
	lease.once.Do(func() {
		accountRun.Lock()
		if accountRun.active[lease.ID] <= 1 {
			delete(accountRun.active, lease.ID)
		} else {
			accountRun.active[lease.ID]--
		}
		accountRun.Unlock()
	})
}

func shortAccountError(failure string, statusCode int) string {
	failure = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(failure, "\r", " "), "\n", " "))
	if failure == "" {
		if statusCode > 0 {
			failure = fmt.Sprintf("上游返回 HTTP %d", statusCode)
		} else {
			failure = "上游连接中断"
		}
	}
	if len([]rune(failure)) > 240 {
		failure = string([]rune(failure)[:240]) + "…"
	}
	return failure
}

// ImportUpstreamAccounts accepts a single JSON object, an array, XIASS's
// {accounts:[...]} export, or a credentials object. It intentionally does not
// log raw JSON because it may contain refresh tokens or API secrets.
func ImportUpstreamAccounts(raw string) AccountImportResult {
	var decoded any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return AccountImportResult{Message: "账户 JSON 无法解析"}
	}
	objects := accountImportObjects(decoded)
	if len(objects) == 0 {
		return AccountImportResult{Message: "JSON 中没有找到可导入的账户"}
	}
	added := 0
	for _, object := range objects {
		account, ok := accountFromImportObject(object)
		if !ok || account.EffectiveAPIKey() == "" {
			continue
		}
		if err := SaveUpstreamAccount(account); err != nil {
			return AccountImportResult{Message: err.Error(), Added: added}
		}
		added++
	}
	if added == 0 {
		return AccountImportResult{Message: "未发现 API Key、访问令牌或 OAuth 凭据", Added: 0}
	}
	return AccountImportResult{OK: true, Added: added, Message: fmt.Sprintf("已导入 %d 个账户", added)}
}

func accountImportObjects(value any) []map[string]any {
	switch current := value.(type) {
	case []any:
		result := make([]map[string]any, 0, len(current))
		for _, item := range current {
			result = append(result, accountImportObjects(item)...)
		}
		return result
	case map[string]any:
		for _, key := range []string{"accounts", "data", "items"} {
			if children, ok := current[key]; ok {
				if result := accountImportObjects(children); len(result) > 0 {
					return result
				}
			}
		}
		return []map[string]any{current}
	default:
		return nil
	}
}

func accountFromImportObject(value map[string]any) (UpstreamAccount, bool) {
	credentials := credentialsFromImportObject(value)
	credentialMaps := importedCredentialMaps(value)
	identity := importedAccountIdentity(value)
	provider := importedStringFromMaps(credentialMaps, "provider", "platform", "vendor")
	apiURL := importedStringFromMaps(credentialMaps, "apiUrl", "api_url", "baseUrl", "base_url", "endpoint", "url")
	name := stringValue(value, "name", "displayName", "label", "email")
	if name == "" {
		name = identity.Email
	}
	account := UpstreamAccount{
		Name:            name,
		Notes:           stringValue(value, "notes", "remark", "description"),
		Provider:        provider,
		Type:            stringValue(value, "type", "authType", "auth_type"),
		APIURL:          apiURL,
		EndpointMode:    stringValue(value, "endpointMode", "endpoint_mode", "urlMode", "url_mode"),
		APIStyle:        stringValue(value, "apiStyle", "api_style", "mode"),
		MessagePathMode: stringValue(value, "messagePathMode", "message_path_mode"),
		AuthMode:        stringValue(value, "authMode", "auth_mode"),
		AuthHeader:      stringValue(value, "authHeader", "auth_header"),
		OAuth:           importedOAuthConfiguration(value),
		APIKey:          importedStringFromMaps([]map[string]any{value}, importedAPIKeyKeys...),
		Credentials:     credentials,
		Identity:        identity,
		AuthExpiresAt:   importedAuthExpiresAt(value),
		Enabled:         true,
		Priority:        intValue(value, "priority"),
		MaxConcurrency:  intValue(value, "maxConcurrency", "max_concurrency", "concurrency"),
		ExpiresAt:       importedAccountExpiresAt(value),
	}
	if headers := mapValue(value, "headers", "extraHeaders", "extra_headers"); headers != nil {
		account.Headers = sanitizeImportedHeaders(stringMap(headers))
	}
	if importedOAuthCredentialsPresent(credentials) && (account.Type == "" || isRawJSONAccountType(account.Type) || isRefreshTokenAccountType(account.Type)) {
		// A JSON export is a transport format, not an auth scheme. Once we have
		// extracted OAuth credentials it must become an OAuth account so expiry
		// handling and automatic refresh are enabled after import.
		account.Type = "oauth"
	} else if isRawJSONAccountType(account.Type) {
		// A JSON envelope that contains a normal API key is still a valid import.
		// Preserve the distinction at the direct-save boundary, but normalize it
		// here because the importer has already extracted its credential safely.
		account.Type = "api_key"
	} else if account.Type == "" {
		account.Type = "api_key"
	}
	account = normalizeAccount(account)
	return account, account.EffectiveAPIKey() != ""
}

func mapValue(value map[string]any, keys ...string) map[string]any {
	for _, key := range keys {
		if child, ok := value[key].(map[string]any); ok {
			return cloneAnyMap(child)
		}
	}
	return nil
}

func stringValue(value map[string]any, keys ...string) string {
	for _, key := range keys {
		if candidate, ok := value[key].(string); ok && strings.TrimSpace(candidate) != "" {
			return strings.TrimSpace(candidate)
		}
	}
	return ""
}

func intValue(value map[string]any, keys ...string) int {
	for _, key := range keys {
		switch candidate := value[key].(type) {
		case float64:
			return int(candidate)
		case int:
			return candidate
		case string:
			if parsed, err := strconv.Atoi(strings.TrimSpace(candidate)); err == nil {
				return parsed
			}
		}
	}
	return 0
}

func stringMap(value map[string]any) map[string]string {
	result := make(map[string]string, len(value))
	for key, item := range value {
		switch current := item.(type) {
		case string:
			result[key] = current
		case float64, bool:
			result[key] = fmt.Sprint(current)
		}
	}
	return result
}
