package upstream

// This file is the small, desktop-safe subset of the local XIASS OpenAI
// OAuth transport. It keeps account tokens local, uses the dedicated Codex
// routes rather than treating OAuth access tokens as XIASS API keys, and
// exposes only account metadata/limit windows to the renderer.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"antigravity-wf-assistant/internal/storage"
)

const (
	openAICodexUserAgent      = "codex_cli_rs/0.144.1 (Ubuntu 22.4.0; x86_64) xterm-256color"
	openAICodexVersion        = "0.144.1"
	openAICodexModelsPath     = "/backend-api/codex/models"
	openAICodexQuotaPath      = "/backend-api/wham/usage"
	openAICodexRequestTimeout = 30 * time.Second
)

// applyOpenAICodexOAuthHeaders mirrors the non-secret identity/header portion
// of XIASS's direct OpenAI OAuth forwarding path. Credentials stay in Config
// and are never logged or returned in a result.
func applyOpenAICodexOAuthHeaders(req *http.Request, config Config) {
	if req == nil {
		return
	}
	if accountID := strings.TrimSpace(config.ChatGPTAccountID); accountID != "" {
		req.Header.Set("chatgpt-account-id", accountID)
	}
	req.Header.Set("OpenAI-Beta", "responses=experimental")
	req.Header.Set("Originator", "codex_cli_rs")
	req.Header.Set("User-Agent", openAICodexUserAgent)
	req.Header.Set("Version", openAICodexVersion)
}

// ResolveOpenAICodexModelsURL uses the same host as the account's direct
// Responses route. This keeps tests and a user-owned local relay possible
// while production OpenAI OAuth naturally resolves to chatgpt.com.
func ResolveOpenAICodexModelsURL(config Config) (string, error) {
	endpoint, err := resolveOpenAICodexSiblingURL(config.APIURL, openAICodexModelsPath)
	if err != nil {
		return "", err
	}
	// The ChatGPT Codex manifest rejects a request without this query value.
	// Keep it tied to the public Version header so an update cannot leave the
	// two protocol identifiers out of sync.
	parsed, err := validateBaseURL(endpoint)
	if err != nil {
		return "", err
	}
	query := parsed.Query()
	query.Set("client_version", openAICodexVersion)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

// ResolveOpenAICodexQuotaURL returns the XIASS-compatible /wham/usage route
// associated with an account's direct Codex Responses endpoint.
func ResolveOpenAICodexQuotaURL(config Config) (string, error) {
	return resolveOpenAICodexSiblingURL(config.APIURL, openAICodexQuotaPath)
}

func resolveOpenAICodexSiblingURL(rawURL, siblingPath string) (string, error) {
	parsed, err := validateBaseURL(rawURL)
	if err != nil {
		return "", err
	}
	path := strings.TrimRight(parsed.Path, "/")
	if index := strings.Index(path, "/backend-api/"); index >= 0 {
		prefix := path[:index]
		parsed.Path = prefix + siblingPath
	} else {
		parsed.Path = siblingPath
	}
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

type openAICodexQuotaWindow struct {
	UsedPercent        float64 `json:"used_percent"`
	LimitWindowSeconds int64   `json:"limit_window_seconds"`
	ResetAfterSeconds  int64   `json:"reset_after_seconds"`
	ResetAt            int64   `json:"reset_at"`
}

type openAICodexRateLimit struct {
	Allowed         bool                    `json:"allowed"`
	LimitReached    bool                    `json:"limit_reached"`
	PrimaryWindow   *openAICodexQuotaWindow `json:"primary_window"`
	SecondaryWindow *openAICodexQuotaWindow `json:"secondary_window"`
}

type openAICodexQuotaUsage struct {
	UserID    string                `json:"user_id"`
	AccountID string                `json:"account_id"`
	Email     string                `json:"email"`
	PlanType  string                `json:"plan_type"`
	RateLimit *openAICodexRateLimit `json:"rate_limit"`
}

// FetchOpenAICodexQuota performs only an explicit user-initiated quota
// request. It deliberately has no automatic background polling behaviour so
// opening the accounts page never consumes requests or account capacity.
func FetchOpenAICodexQuota(ctx context.Context, config Config) QuotaResult {
	if !IsOpenAICodexOAuth(config) {
		return QuotaResult{Message: "该账户不是 OpenAI / Codex OAuth 账户"}
	}
	if err := ValidateConfig(config); err != nil {
		return QuotaResult{Message: err.Error()}
	}
	endpoint, err := ResolveOpenAICodexQuotaURL(config)
	if err != nil {
		return QuotaResult{Message: err.Error()}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return QuotaResult{Message: "无法创建额度查询请求", Endpoint: endpoint}
	}
	req.Header.Set("Accept", "application/json")
	if err := ApplyCredentials(req, config); err != nil {
		return QuotaResult{Message: err.Error(), Endpoint: endpoint}
	}
	// /wham/usage uses the same OAuth credential but a slightly different
	// Codex identity set in XIASS. Override only public, non-secret headers.
	req.Header.Set("OpenAI-Beta", "codex-1")
	req.Header.Set("Originator", "Codex Desktop")
	req.Header.Set("OAI-Language", "zh-CN")
	req.Header.Set("Sec-Fetch-Site", "none")
	req.Header.Set("Sec-Fetch-Mode", "no-cors")
	req.Header.Set("Sec-Fetch-Dest", "empty")

	client := &http.Client{Timeout: openAICodexRequestTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return QuotaResult{Message: "无法连接上游额度接口：" + safeNetworkError(err), Endpoint: endpoint}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512<<10))
	result := QuotaResult{Endpoint: endpoint, StatusCode: resp.StatusCode}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		// No raw response body is exposed here: gateways sometimes echo request
		// diagnostics and an account token must never be surfaced by a test UI.
		result.Message = fmt.Sprintf("上游返回 HTTP %d", resp.StatusCode)
		return result
	}
	var usage openAICodexQuotaUsage
	if err := json.Unmarshal(body, &usage); err != nil {
		result.Message = "上游额度响应无法识别"
		return result
	}
	snapshot := openAICodexQuotaSnapshot(usage, resp.StatusCode, time.Now())
	if !snapshot.Available {
		result.Message = "上游未返回可显示的额度窗口"
		result.Snapshot = snapshot
		return result
	}
	result.OK = true
	result.Message = fmt.Sprintf("已更新 %d 个额度窗口", len(snapshot.Windows))
	result.Snapshot = snapshot
	return result
}

func openAICodexQuotaSnapshot(usage openAICodexQuotaUsage, statusCode int, now time.Time) storage.QuotaSnapshot {
	snapshot := storage.QuotaSnapshot{
		Source:     "OpenAI / Codex OAuth 用量接口",
		UpdatedAt:  now.UTC().Format(time.RFC3339),
		StatusCode: statusCode,
		Plan:       strings.TrimSpace(usage.PlanType),
		Email:      strings.TrimSpace(usage.Email),
		AccountID:  strings.TrimSpace(usage.AccountID),
	}
	if snapshot.AccountID == "" {
		snapshot.AccountID = strings.TrimSpace(usage.UserID)
	}
	if usage.RateLimit == nil {
		return snapshot
	}
	for _, item := range []struct {
		window *openAICodexQuotaWindow
		order  int
	}{
		{usage.RateLimit.PrimaryWindow, 0},
		{usage.RateLimit.SecondaryWindow, 1},
	} {
		if item.window == nil {
			continue
		}
		window := storage.QuotaWindow{
			Label:              openAICodexWindowLabel(item.window.LimitWindowSeconds, item.order),
			UsedPercent:        item.window.UsedPercent,
			LimitWindowSeconds: item.window.LimitWindowSeconds,
			ResetAfterSeconds:  item.window.ResetAfterSeconds,
			Allowed:            usage.RateLimit.Allowed,
			LimitReached:       usage.RateLimit.LimitReached,
		}
		if item.window.ResetAt > 0 {
			window.ResetAt = time.Unix(item.window.ResetAt, 0).UTC().Format(time.RFC3339)
		} else if item.window.ResetAfterSeconds > 0 {
			window.ResetAt = now.Add(time.Duration(item.window.ResetAfterSeconds) * time.Second).UTC().Format(time.RFC3339)
		}
		snapshot.Windows = append(snapshot.Windows, window)
	}
	sort.SliceStable(snapshot.Windows, func(i, j int) bool {
		left, right := snapshot.Windows[i].LimitWindowSeconds, snapshot.Windows[j].LimitWindowSeconds
		if left == 0 || right == 0 {
			return snapshot.Windows[i].Label < snapshot.Windows[j].Label
		}
		return left < right
	})
	snapshot.Available = len(snapshot.Windows) > 0
	return snapshot
}

func openAICodexWindowLabel(seconds int64, fallbackIndex int) string {
	switch {
	case seconds >= 4*60*60 && seconds <= 6*60*60:
		return "5h"
	case seconds >= 6*24*60*60 && seconds <= 8*24*60*60:
		return "7d"
	case seconds > 0 && seconds%86400 == 0:
		return fmt.Sprintf("%dd", seconds/86400)
	case seconds > 0 && seconds%3600 == 0:
		return fmt.Sprintf("%dh", seconds/3600)
	case seconds > 0:
		return fmt.Sprintf("%dm", seconds/60)
	default:
		return fmt.Sprintf("窗口 %d", fallbackIndex+1)
	}
}
