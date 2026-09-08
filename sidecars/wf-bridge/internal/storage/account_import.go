package storage

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"
)

// Imported credential files are not a general-purpose object graph. The
// bounded traversal below only follows the common credential containers and
// stops after three nested containers. That covers exports such as
// credentials -> auth -> tokens without treating unrelated JSON as secrets.
const importedCredentialContainerDepth = 3

var importedCredentialContainerKeys = []string{
	"credentials", "credential", "auth", "authentication", "tokens", "token",
	"oauthTokens", "oauth_tokens", "tokenSet", "token_set",
}

var importedAPIKeyKeys = []string{
	"api_key", "apiKey", "api-key", "OPENAI_API_KEY", "openai_api_key",
}

var importedAccessTokenKeys = []string{
	"access_token", "accessToken", "access-token",
}

var importedRefreshTokenKeys = []string{
	"refresh_token", "refreshToken", "refresh-token", "mobile_rt", "mobileRT",
}

var importedIDTokenKeys = []string{
	"id_token", "idToken", "id-token",
}

func importedCredentialMaps(root map[string]any) []map[string]any {
	if len(root) == 0 {
		return nil
	}
	type node struct {
		value map[string]any
		depth int
	}
	queue := []node{{value: root}}
	result := make([]map[string]any, 0, 8)
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		result = append(result, current.value)
		if current.depth >= importedCredentialContainerDepth {
			continue
		}
		for _, key := range importedCredentialContainerKeys {
			child, ok := current.value[key].(map[string]any)
			if !ok || len(child) == 0 {
				continue
			}
			queue = append(queue, node{value: child, depth: current.depth + 1})
		}
	}
	return result
}

func importedStringFromMaps(values []map[string]any, keys ...string) string {
	for _, key := range keys {
		for _, value := range values {
			if candidate, ok := value[key].(string); ok && strings.TrimSpace(candidate) != "" {
				return strings.TrimSpace(candidate)
			}
		}
	}
	return ""
}

func importedValueFromMaps(values []map[string]any, keys ...string) (any, bool) {
	for _, key := range keys {
		for _, value := range values {
			candidate, ok := value[key]
			if !ok || candidate == nil {
				continue
			}
			if text, ok := candidate.(string); ok && strings.TrimSpace(text) == "" {
				continue
			}
			return candidate, true
		}
	}
	return nil, false
}

func normalizeImportedCredentials(credentials map[string]any) map[string]any {
	if len(credentials) == 0 {
		return nil
	}
	return canonicalImportedCredentials(importedCredentialMaps(credentials))
}

func credentialsFromImportObject(value map[string]any) map[string]any {
	if len(value) == 0 {
		return nil
	}
	return canonicalImportedCredentials(importedCredentialMaps(value))
}

// canonicalImportedCredentials deliberately rebuilds the persisted map from a
// small, explicit allow-list. An import dialog must not become a generic
// storage path for an external application's browser cookies, desktop session,
// client_secret, device identifiers, or arbitrary opaque state. The local
// upstream pool only needs transport credentials, refresh metadata, the
// public OAuth client identifier, and minimal display metadata.
//
// This function is also used while normalising already-saved accounts. That
// means a future user-initiated save safely removes fields which an older build
// may have accepted, while loading alone never mutates an existing file.
func canonicalImportedCredentials(sources []map[string]any) map[string]any {
	if len(sources) == 0 {
		return nil
	}
	result := make(map[string]any)
	canonicalizeImportedCredentials(result, sources)
	if len(result) == 0 {
		return nil
	}
	return result
}

func canonicalizeImportedCredentials(target map[string]any, sources []map[string]any) {
	for _, field := range []struct {
		canonical string
		keys      []string
	}{
		{"api_key", importedAPIKeyKeys},
		{"access_token", importedAccessTokenKeys},
		{"refresh_token", importedRefreshTokenKeys},
		{"id_token", importedIDTokenKeys},
		{"bearer_token", []string{"bearer_token", "bearerToken", "bearer-token"}},
		{"authorization", []string{"authorization", "Authorization"}},
		{"token", []string{"token", "api_token", "apiToken"}},
		{"setup_token", []string{"setup_token", "setupToken", "setup-token"}},
		{"pat", []string{"pat", "personal_access_token", "personalAccessToken"}},
		{"token_type", []string{"token_type", "tokenType"}},
		{"scope", []string{"scope", "scopes"}},
		// XIASS records the public OAuth client in credentials so a later local
		// refresh uses the same registered client. It is public metadata, never
		// a client secret.
		{"client_id", []string{"client_id", "clientId", "oauth_client_id", "oauthClientId"}},
		{"account_id", []string{"account_id", "accountId", "chatgpt_account_id"}},
		{"chatgpt_account_id", []string{"chatgpt_account_id", "chatgptAccountId"}},
		{"chatgpt_user_id", []string{"chatgpt_user_id", "chatgptUserId"}},
		{"organization_id", []string{"organization_id", "organizationId", "org_id", "orgId", "organization"}},
		{"plan_type", []string{"plan_type", "planType", "chatgpt_plan_type", "chatgptPlanType", "plan", "tier"}},
		{"subscription_expires_at", []string{"subscription_expires_at", "subscriptionExpiresAt", "plan_expires_at", "planExpiresAt"}},
		{"email", []string{"email", "user_email", "userEmail"}},
		{"subject", []string{"sub", "subject", "user_id", "userId"}},
		{"privacy_mode", []string{"privacy_mode", "privacyMode"}},
		{"identity_source", []string{"identity_source", "identitySource"}},
	} {
		if text := importedStringFromMaps(sources, field.keys...); text != "" {
			target[field.canonical] = text
		}
	}
	for _, field := range []struct {
		canonical string
		keys      []string
	}{
		{"auth_expires_at", []string{"auth_expires_at", "authExpiresAt"}},
		{"expires_at", []string{"expires_at", "expiresAt", "expiration", "expires"}},
		{"expires_in", []string{"expires_in", "expiresIn"}},
	} {
		if raw, ok := importedValueFromMaps(sources, field.keys...); ok {
			target[field.canonical] = raw
		}
	}
}

func effectiveAPIKeyFromCredentials(credentials map[string]any) string {
	sources := importedCredentialMaps(credentials)
	for _, keys := range [][]string{
		importedAPIKeyKeys,
		importedAccessTokenKeys,
		{"bearer_token", "bearerToken", "bearer-token"},
		{"token", "api_token", "apiToken"},
		{"setup_token", "setupToken", "setup-token"},
		{"pat", "personal_access_token", "personalAccessToken"},
	} {
		if value := importedStringFromMaps(sources, keys...); value != "" {
			return value
		}
	}
	if authorization := importedStringFromMaps(sources, "authorization", "Authorization"); authorization != "" {
		if len(authorization) >= 7 && strings.EqualFold(authorization[:7], "bearer ") {
			return strings.TrimSpace(authorization[7:])
		}
		return authorization
	}
	return ""
}

// sanitizeImportedHeaders retains ordinary, non-secret protocol headers used
// by compatible API gateways (for example, product/version headers) while
// rejecting fields that would turn an account-credential import into browser
// or desktop-session migration. API authentication always travels through the
// explicit account credential and auth-header fields instead.
func sanitizeImportedHeaders(headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	result := make(map[string]string, len(headers))
	for name, value := range headers {
		name = strings.TrimSpace(name)
		value = strings.TrimSpace(value)
		if name == "" || value == "" || importedHeaderIsSensitive(name) {
			continue
		}
		result[name] = value
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func importedHeaderIsSensitive(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "authorization" || name == "proxy-authorization" ||
		name == "cookie" || name == "set-cookie" || name == "x-api-key" ||
		name == "api-key" || name == "x-goog-api-key" {
		return true
	}
	return strings.Contains(name, "cookie") || strings.Contains(name, "token") || strings.Contains(name, "secret")
}

func importedOAuthCredentialsPresent(credentials map[string]any) bool {
	sources := importedCredentialMaps(credentials)
	return importedStringFromMaps(sources, importedAccessTokenKeys...) != "" ||
		importedStringFromMaps(sources, importedRefreshTokenKeys...) != "" ||
		importedStringFromMaps(sources, importedIDTokenKeys...) != ""
}

func importedOAuthConfiguration(value map[string]any) OAuthConfiguration {
	sources := []map[string]any{value}
	for _, key := range []string{
		"oauth", "oauthConfig", "oauth_config", "oauthConfiguration", "oauth_configuration", "oauthClient", "oauth_client",
	} {
		if child, ok := value[key].(map[string]any); ok && len(child) > 0 {
			sources = append(sources, child)
		}
	}
	// XIASS-style exports put client_id beside tokens under credentials. Include
	// the bounded credential containers only for this public configuration
	// lookup; this never treats a token as an OAuth client setting.
	sources = append(sources, importedCredentialMaps(value)...)
	return normalizeOAuthConfiguration(OAuthConfiguration{
		AuthorizationURL: importedStringFromMaps(sources, "authorizationUrl", "authorization_url", "authorizeUrl", "authorize_url", "authUrl", "auth_url"),
		TokenURL:         importedStringFromMaps(sources, "tokenUrl", "token_url", "tokenEndpoint", "token_endpoint"),
		ClientID:         importedStringFromMaps(sources, "clientId", "client_id", "publicClientId", "public_client_id"),
		RedirectURI:      importedStringFromMaps(sources, "redirectUri", "redirect_uri", "callbackUrl", "callback_url"),
		Scopes:           importedScopesFromMaps(sources),
		RefreshScopes:    importedStringFromMaps(sources, "refreshScopes", "refresh_scopes"),
	})
}

func importedScopesFromMaps(values []map[string]any) string {
	if scope := importedStringFromMaps(values, "scopes", "scope"); scope != "" {
		return scope
	}
	for _, key := range []string{"scopes", "scope"} {
		for _, value := range values {
			items, ok := value[key].([]any)
			if !ok {
				continue
			}
			parts := make([]string, 0, len(items))
			for _, item := range items {
				if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
					parts = append(parts, strings.TrimSpace(text))
				}
			}
			if len(parts) > 0 {
				return strings.Join(parts, " ")
			}
		}
	}
	return ""
}

func importedAccountIdentity(value map[string]any) AccountIdentity {
	sources := []map[string]any{value}
	for _, key := range []string{"identity", "profile", "user", "account", "metadata", "extra"} {
		if child, ok := value[key].(map[string]any); ok && len(child) > 0 {
			sources = append(sources, child)
		}
	}
	sources = append(sources, importedCredentialMaps(value)...)
	return normalizeAccountIdentity(AccountIdentity{
		Email:                 importedStringFromMaps(sources, "email", "user_email", "userEmail"),
		Subject:               importedStringFromMaps(sources, "sub", "subject", "user_id", "userId", "chatgpt_user_id"),
		ChatGPTAccountID:      importedStringFromMaps(sources, "chatgpt_account_id", "chatgptAccountId"),
		ChatGPTUserID:         importedStringFromMaps(sources, "chatgpt_user_id", "chatgptUserId"),
		Plan:                  importedStringFromMaps(sources, "plan", "plan_type", "planType", "chatgpt_plan_type", "tier"),
		OrganizationID:        importedStringFromMaps(sources, "organization_id", "organizationId", "organization", "org_id", "orgId", "account_id", "accountId", "poid"),
		SubscriptionExpiresAt: importedStringFromMaps(sources, "subscription_expires_at", "subscriptionExpiresAt", "plan_expires_at", "planExpiresAt"),
		PrivacyMode:           importedStringFromMaps(sources, "privacy_mode", "privacyMode"),
		Source:                importedStringFromMaps(sources, "identity_source", "identitySource"),
	})
}

func importedAuthExpiresAt(value map[string]any) string {
	sources := importedCredentialMaps(value)
	if expiry := importedAbsoluteTimeFromMaps(sources, "auth_expires_at", "authExpiresAt"); expiry != "" {
		return expiry
	}
	// An account export can have a top-level expires_at for account scheduling.
	// Prefer a token container's expiry when one is present.
	if len(sources) > 1 {
		if expiry := importedAbsoluteTimeFromMaps(sources[1:], "expires_at", "expiresAt", "expiration", "expires"); expiry != "" {
			return expiry
		}
	}
	if expiry := importedAbsoluteTimeFromMaps(sources[:minImportedMapCount(len(sources), 1)], "expires_at", "expiresAt", "expiration", "expires"); expiry != "" {
		return expiry
	}
	if expiry := importedRelativeTimeFromMaps(sources, "expires_in", "expiresIn"); expiry != "" {
		return expiry
	}
	for _, keys := range [][]string{importedIDTokenKeys, importedAccessTokenKeys} {
		if token := importedStringFromMaps(sources, keys...); token != "" {
			if claims := decodeJWTClaims(token); claims != nil {
				if expiry := importedAbsoluteTimeFromMaps([]map[string]any{claims}, "exp"); expiry != "" {
					return expiry
				}
			}
		}
	}
	return ""
}

func importedAccountExpiresAt(value map[string]any) string {
	if expiry := importedAbsoluteTimeFromMaps([]map[string]any{value}, "account_expires_at", "accountExpiresAt", "scheduled_expires_at", "scheduledExpiresAt"); expiry != "" {
		return expiry
	}
	// Keep XIASS-style account expiry fields when the object is clearly an
	// account envelope. A raw Codex auth.json has no envelope fields, so its
	// expires_at remains an OAuth access-token expiry instead.
	if importedAccountEnvelope(value) {
		return importedAbsoluteTimeFromMaps([]map[string]any{value}, "expires_at", "expiresAt")
	}
	return ""
}

func importedAccountEnvelope(value map[string]any) bool {
	for _, key := range []string{"name", "provider", "platform", "vendor", "type", "apiUrl", "api_url", "baseUrl", "base_url", "status", "priority", "credentials", "credential"} {
		if _, ok := value[key]; ok {
			return true
		}
	}
	return false
}

func importedAbsoluteTimeFromMaps(values []map[string]any, keys ...string) string {
	for _, key := range keys {
		for _, value := range values {
			candidate, ok := value[key]
			if !ok {
				continue
			}
			if parsed, ok := parseImportedTime(candidate); ok {
				return parsed.UTC().Format(time.RFC3339)
			}
		}
	}
	return ""
}

func importedRelativeTimeFromMaps(values []map[string]any, keys ...string) string {
	for _, key := range keys {
		for _, value := range values {
			candidate, ok := value[key]
			if !ok {
				continue
			}
			seconds, ok := importedSeconds(candidate)
			if !ok || seconds < 0 || seconds > 10*365*24*60*60 {
				continue
			}
			return time.Now().UTC().Add(time.Duration(seconds) * time.Second).Format(time.RFC3339)
		}
	}
	return ""
}

func parseImportedTime(value any) (time.Time, bool) {
	switch current := value.(type) {
	case string:
		text := strings.TrimSpace(current)
		if text == "" {
			return time.Time{}, false
		}
		if parsed, err := time.Parse(time.RFC3339, text); err == nil {
			return parsed, true
		}
		seconds, ok := importedSeconds(text)
		if !ok {
			return time.Time{}, false
		}
		return importedUnixTime(seconds)
	case float64:
		seconds, ok := importedSeconds(current)
		if !ok {
			return time.Time{}, false
		}
		return importedUnixTime(seconds)
	case float32:
		seconds, ok := importedSeconds(current)
		if !ok {
			return time.Time{}, false
		}
		return importedUnixTime(seconds)
	case int:
		return importedUnixTime(int64(current))
	case int64:
		return importedUnixTime(current)
	case int32:
		return importedUnixTime(int64(current))
	case json.Number:
		return parseImportedTime(current.String())
	default:
		return time.Time{}, false
	}
}

func importedSeconds(value any) (int64, bool) {
	switch current := value.(type) {
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(current), 10, 64)
		return parsed, err == nil
	case float64:
		if math.IsNaN(current) || math.IsInf(current, 0) || current != math.Trunc(current) || current > float64(math.MaxInt64) || current < float64(math.MinInt64) {
			return 0, false
		}
		return int64(current), true
	case float32:
		return importedSeconds(float64(current))
	case int:
		return int64(current), true
	case int64:
		return current, true
	case int32:
		return int64(current), true
	case json.Number:
		return importedSeconds(current.String())
	default:
		return 0, false
	}
}

func importedUnixTime(seconds int64) (time.Time, bool) {
	if seconds <= 0 {
		return time.Time{}, false
	}
	// Millisecond timestamps are common in browser/local-storage exports.
	if seconds >= 100000000000 {
		seconds /= 1000
	}
	if seconds > 253402300799 { // 9999-12-31T23:59:59Z
		return time.Time{}, false
	}
	return time.Unix(seconds, 0), true
}

func minImportedMapCount(value, limit int) int {
	if value < limit {
		return value
	}
	return limit
}
