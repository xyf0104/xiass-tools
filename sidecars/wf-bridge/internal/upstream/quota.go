package upstream

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"antigravity-wf-assistant/internal/storage"
)

// QuotaResult is deliberately conservative. It only exposes recognised
// fields returned by the configured upstream; an empty snapshot never means
// an account has no quota.
type QuotaResult struct {
	OK         bool                  `json:"ok"`
	Message    string                `json:"message"`
	Endpoint   string                `json:"endpoint"`
	StatusCode int                   `json:"statusCode"`
	Snapshot   storage.QuotaSnapshot `json:"snapshot"`
}

// FetchQuota performs an explicit, user-triggered quota request. There is no
// portable quota endpoint for OpenAI-compatible services, so callers must
// supply a provider-supported full URL rather than guessing one from APIURL.
func FetchQuota(ctx context.Context, config Config, quotaURL string) QuotaResult {
	// The built-in OpenAI/Codex OAuth account has a real, authenticated quota
	// route. It intentionally does not require a user-entered quota URL, and
	// must be handled before the generic URL validation below.
	if IsOpenAICodexOAuth(config) {
		return FetchOpenAICodexQuota(ctx, config)
	}
	quotaURL = strings.TrimSpace(quotaURL)
	if quotaURL == "" {
		return QuotaResult{Message: "未配置上游额度接口"}
	}
	parsed, err := url.Parse(quotaURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return QuotaResult{Message: "额度接口地址无效", Endpoint: quotaURL}
	}
	if parsed.Scheme != "https" && !isLoopbackHTTPURL(parsed) {
		return QuotaResult{Message: "额度接口必须使用 HTTPS（本机回环地址除外）", Endpoint: quotaURL}
	}
	if err := ValidateConfig(config); err != nil {
		return QuotaResult{Message: err.Error(), Endpoint: quotaURL}
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return QuotaResult{Message: "无法创建额度请求", Endpoint: quotaURL}
	}
	request.Header.Set("Accept", "application/json")
	if err := ApplyCredentials(request, config); err != nil {
		return QuotaResult{Message: err.Error(), Endpoint: quotaURL}
	}
	response, err := (&http.Client{Timeout: 30 * time.Second}).Do(request)
	if err != nil {
		return QuotaResult{Message: "无法连接上游额度接口：" + safeNetworkError(err), Endpoint: quotaURL}
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 256<<10))
	snapshot := quotaSnapshotFromResponse(response, body)
	result := QuotaResult{
		Endpoint: quotaURL, StatusCode: response.StatusCode, Snapshot: snapshot,
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		result.Message = statusMessage(response.StatusCode, body, config)
		return result
	}
	result.OK = true
	if snapshot.Available {
		result.Message = fmt.Sprintf("已更新上游额度快照（HTTP %d）", response.StatusCode)
	} else {
		result.Message = "额度接口已响应，但未返回可识别的额度字段"
	}
	return result
}

func isLoopbackHTTPURL(parsed *url.URL) bool {
	if parsed == nil || parsed.Scheme != "http" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func quotaSnapshotFromResponse(response *http.Response, body []byte) storage.QuotaSnapshot {
	snapshot := storage.QuotaSnapshot{
		Source:     "用户配置的额度接口",
		UpdatedAt:  time.Now().UTC().Format(time.RFC3339),
		StatusCode: response.StatusCode,
	}
	if response != nil {
		snapshot.RequestsRemaining = quotaHeader(response.Header,
			"X-RateLimit-Remaining-Requests", "Anthropic-Ratelimit-Requests-Remaining")
		snapshot.TokensRemaining = quotaHeader(response.Header,
			"X-RateLimit-Remaining-Tokens", "Anthropic-Ratelimit-Tokens-Remaining")
		snapshot.RequestsReset = quotaHeader(response.Header,
			"X-RateLimit-Reset-Requests", "Anthropic-Ratelimit-Requests-Reset")
		snapshot.TokensReset = quotaHeader(response.Header,
			"X-RateLimit-Reset-Tokens", "Anthropic-Ratelimit-Tokens-Reset")
		snapshot.RetryAfter = quotaHeader(response.Header, "Retry-After")
	}
	var decoded any
	if json.Unmarshal(body, &decoded) == nil {
		collectQuotaJSON(decoded, &snapshot, 0)
	}
	snapshot.Available = snapshot.RequestsRemaining != "" || snapshot.TokensRemaining != "" ||
		snapshot.RequestsReset != "" || snapshot.TokensReset != "" || snapshot.RetryAfter != ""
	return snapshot
}

func quotaHeader(headers http.Header, names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(headers.Get(name)); value != "" {
			return value
		}
	}
	return ""
}

// collectQuotaJSON supports intentionally small, common field names. It does
// not treat arbitrary numbers as a balance, which avoids showing misleading
// quota data from a provider-specific payload.
func collectQuotaJSON(value any, snapshot *storage.QuotaSnapshot, depth int) {
	if snapshot == nil || depth > 4 {
		return
	}
	switch current := value.(type) {
	case map[string]any:
		if snapshot.RequestsRemaining == "" {
			snapshot.RequestsRemaining = quotaJSONValue(current, "requests_remaining", "remaining_requests", "request_remaining")
		}
		if snapshot.TokensRemaining == "" {
			snapshot.TokensRemaining = quotaJSONValue(current, "tokens_remaining", "remaining_tokens", "token_remaining")
		}
		if snapshot.RequestsReset == "" {
			snapshot.RequestsReset = quotaJSONValue(current, "requests_reset", "reset_requests", "request_reset")
		}
		if snapshot.TokensReset == "" {
			snapshot.TokensReset = quotaJSONValue(current, "tokens_reset", "reset_tokens", "token_reset")
		}
		if snapshot.RetryAfter == "" {
			snapshot.RetryAfter = quotaJSONValue(current, "retry_after")
		}
		for _, child := range current {
			collectQuotaJSON(child, snapshot, depth+1)
		}
	case []any:
		for _, child := range current {
			collectQuotaJSON(child, snapshot, depth+1)
		}
	}
}

func quotaJSONValue(values map[string]any, names ...string) string {
	for _, name := range names {
		value, ok := values[name]
		if !ok {
			continue
		}
		switch current := value.(type) {
		case string:
			if text := strings.TrimSpace(current); text != "" {
				return text
			}
		case float64:
			return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.6f", current), "0"), ".")
		case json.Number:
			return current.String()
		}
	}
	return ""
}
