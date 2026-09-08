package proxy

import (
	"fmt"
	"net/http"
	"strings"

	"antigravity-wf-assistant/internal/storage"
)

// acquireAttemptModel selects an account for one upstream attempt. After the
// first lease, pinnedAccountID excludes every other binding so transient
// retries stay on the same credential and upstream. Exclusions remain scoped
// to this request and never become a cross-request blacklist.
func acquireAttemptModel(base *storage.CustomModel, excluded map[string]struct{}, pinnedAccountID string) (*storage.CustomModel, *storage.AccountLease, error) {
	effectiveExcluded := excluded
	if strings.TrimSpace(pinnedAccountID) != "" {
		effectiveExcluded = make(map[string]struct{}, len(excluded)+len(base.AccountIDs))
		for id := range excluded {
			effectiveExcluded[id] = struct{}{}
		}
		for _, id := range base.AccountIDs {
			id = strings.TrimSpace(id)
			if id != "" && id != pinnedAccountID {
				effectiveExcluded[id] = struct{}{}
			}
		}
	}
	selected, lease, err := storage.AcquireAccountForModel(*base, effectiveExcluded)
	if err != nil {
		return nil, nil, err
	}
	return &selected, lease, nil
}

func excludeFailedAttempt(excluded map[string]struct{}, lease *storage.AccountLease) {
	if lease != nil && lease.ID != "" {
		excluded[lease.ID] = struct{}{}
	}
}

// observeAttemptQuota passively records rate-limit headers for the account
// selected for this upstream attempt. It deliberately runs before any retry,
// fallback, or response-body handling so both accepted and rejected upstream
// responses remain visible in the account view.
func observeAttemptQuota(lease *storage.AccountLease, provider string, response *http.Response) {
	if lease == nil || lease.ID == "" || response == nil {
		return
	}
	storage.ObserveUpstreamQuota(lease.ID, provider, response.StatusCode, response.Header)
}

func releaseAttemptSuccess(lease *storage.AccountLease) {
	if lease != nil {
		lease.Finish(http.StatusOK, "", "")
	}
}

func releaseAttemptFailure(lease *storage.AccountLease, statusCode int, retryAfter, message string) {
	if lease == nil {
		return
	}
	if shouldRecordAccountFailure(statusCode, message) {
		lease.Finish(statusCode, retryAfter, message)
		return
	}
	// A regular 4xx is usually a model/request validation issue. The account
	// itself remains usable and should not be penalised by the scheduler.
	lease.Finish(http.StatusOK, "", "")
}

func shouldRecordAccountFailure(statusCode int, message string) bool {
	return statusCode == 0 || statusCode == http.StatusUnauthorized ||
		(statusCode == http.StatusForbidden && isAccountLevelForbidden(message)) ||
		statusCode == http.StatusTooManyRequests || statusCode == http.StatusBadGateway ||
		statusCode == http.StatusServiceUnavailable || statusCode == http.StatusGatewayTimeout || statusCode == 524
}

// shouldFailOverAccount retains its historical name but now identifies only
// temporary rejections that are safe to retry on the pinned account.
func shouldFailOverAccount(_ *storage.AccountLease, statusCode int, body string) bool {
	return isRetryableStatus(statusCode) || isTransientProviderRejection(statusCode, body) || isTransientModelPoolRejection(statusCode, body)
}

// shouldRetrySameAccount mirrors the temporary classification above. Keeping
// it separate makes the no-account-switch invariant explicit at call sites.
func shouldRetrySameAccount(_ *storage.AccountLease, statusCode int, body string) bool {
	return isRetryableStatus(statusCode) || isTransientProviderRejection(statusCode, body) || isTransientModelPoolRejection(statusCode, body)
}

func isAccountLevelForbidden(body string) bool {
	value := strings.ToLower(body)
	for _, marker := range []string{
		"insufficient balance", "insufficient_balance", "billing_error", "payment required",
		"quota exhausted", "quota_exhausted", "credit balance", "invalid api key",
		"invalid_api_key", "authentication", "access token", "api key expired",
	} {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}

func isTransientProviderRejection(statusCode int, body string) bool {
	if statusCode != http.StatusForbidden || isAccountLevelForbidden(body) {
		return false
	}
	value := strings.ToLower(body)
	for _, marker := range []string{
		"provider terms of service", "provider terms", "provider unavailable",
		"provider_name", "permission_denied", "permission_error",
	} {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}

func isModelRouteRejection(body string) bool {
	value := strings.ToLower(body)
	for _, marker := range []string{
		"model_not_found", "model not found", "model is not supported",
		"not supported by any configured account", "no available channel for model",
		"no provider available for model",
	} {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}

// isTransientModelPoolRejection distinguishes a relay whose account group is
// temporarily empty from a genuinely unknown model ID. Some gateways report
// pool depletion as HTTP 404 while they asynchronously replenish accounts.
// A concrete rejection is safe to retry before any response has been emitted.
func isTransientModelPoolRejection(statusCode int, body string) bool {
	if statusCode != http.StatusNotFound {
		return false
	}
	value := strings.ToLower(body)
	for _, marker := range []string{
		"not supported by any configured account", "no available channel for model",
		"no provider available for model", "no available account for model",
	} {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}

// rejectedRetryAfter extends the retry window for gateway account replenishing.
// Many third-party relays refill their provider pool within a few seconds; the
// old 250/500ms backoff exhausted both retries before that refill could finish.
// An explicit Retry-After header always remains authoritative.
func rejectedRetryAfter(statusCode int, body, upstreamValue string, reconnect int) string {
	if value := strings.TrimSpace(upstreamValue); value != "" {
		return value
	}
	base := 0.0
	switch {
	case isTransientProviderRejection(statusCode, body):
		base = 1.5
	case isTransientModelPoolRejection(statusCode, body):
		base = 1.5
	case statusCode == http.StatusTooManyRequests:
		base = 2
	case statusCode == http.StatusBadGateway || statusCode == http.StatusServiceUnavailable || statusCode == http.StatusGatewayTimeout || statusCode == 524:
		base = 1
	}
	if base == 0 {
		return ""
	}
	multiplier := 1 << min(max(reconnect-1, 0), 2)
	return fmt.Sprintf("%.3f", base*float64(multiplier))
}

func accountPoolError(provider string, err error) string {
	if err == nil {
		return ""
	}
	message := strings.TrimSpace(err.Error())
	if message == "" {
		message = "账户池没有可用账户"
	}
	return fmt.Sprintf("%s 账户池不可用：%s", provider, message)
}
