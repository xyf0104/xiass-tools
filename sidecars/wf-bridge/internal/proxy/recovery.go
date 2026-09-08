package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"antigravity-wf-assistant/internal/storage"
)

const upstreamStreamTimeout = 15 * time.Minute

// streamRecoveryPolicy is kept in memory so a model request never needs to
// read the settings file on its hot path. The application updates it whenever
// the user saves the Settings page.
type streamRecoveryPolicy struct {
	enabled         bool
	maxAttempts     int
	maxDelaySeconds int
}

var streamRecoveryConfig = struct {
	sync.RWMutex
	policy streamRecoveryPolicy
}{
	policy: streamRecoveryPolicy{enabled: true, maxAttempts: 2, maxDelaySeconds: 20},
}

func ConfigureStreamRecovery(settings storage.StreamRecoverySettings) {
	defaults := storage.DefaultAppSettings().StreamRecovery
	if settings.MaxAttempts <= 0 {
		settings.MaxAttempts = defaults.MaxAttempts
	}
	if settings.MaxAttempts > 10 {
		settings.MaxAttempts = 10
	}
	if settings.MaxDelaySeconds <= 0 {
		settings.MaxDelaySeconds = defaults.MaxDelaySeconds
	}
	if settings.MaxDelaySeconds > 120 {
		settings.MaxDelaySeconds = 120
	}
	streamRecoveryConfig.Lock()
	streamRecoveryConfig.policy = streamRecoveryPolicy{
		enabled: settings.Enabled, maxAttempts: settings.MaxAttempts, maxDelaySeconds: settings.MaxDelaySeconds,
	}
	streamRecoveryConfig.Unlock()
}

func currentStreamRecoveryPolicy() streamRecoveryPolicy {
	streamRecoveryConfig.RLock()
	defer streamRecoveryConfig.RUnlock()
	return streamRecoveryConfig.policy
}

// downstreamSSEWriter tracks whether the HTTP response was committed. This is
// essential for recovery: once Antigravity has received part of an answer, an
// upstream HTTP error can no longer be forwarded as a second HTTP response.
type downstreamSSEWriter struct {
	w         http.ResponseWriter
	flusher   http.Flusher
	committed bool
}

// upstreamRequestWriteTrace distinguishes a connection failure that happened
// before net/http attempted to write the request from an ambiguous disconnect
// after bytes may have reached the provider. Only the former can be replayed
// without risking duplicate tokens, tools or image charges.
type upstreamRequestWriteTrace struct {
	writeAttempted atomic.Bool
}

func traceUpstreamRequestWrite(request *http.Request) (*http.Request, *upstreamRequestWriteTrace) {
	state := &upstreamRequestWriteTrace{}
	clientTrace := &httptrace.ClientTrace{
		WroteRequest: func(httptrace.WroteRequestInfo) {
			// Even a callback carrying a write error can follow a partial write.
			// Treat it as possibly delivered and therefore unsafe to replay.
			state.writeAttempted.Store(true)
		},
	}
	return request.WithContext(httptrace.WithClientTrace(request.Context(), clientTrace)), state
}

func canRetryUnsentTransportFailure(writer *downstreamSSEWriter, policy streamRecoveryPolicy, reconnect int, state *upstreamRequestWriteTrace) bool {
	return writer != nil && !writer.committed && canRetryUnsentTransportFailureBeforeResponse(policy, reconnect, state)
}

func canRetryUnsentTransportFailureBeforeResponse(policy streamRecoveryPolicy, reconnect int, state *upstreamRequestWriteTrace) bool {
	return state != nil && !state.writeAttempted.Load() && policy.enabled && reconnect > 0 && reconnect <= policy.maxAttempts
}

func newDownstreamSSEWriter(w http.ResponseWriter) *downstreamSSEWriter {
	flusher, _ := w.(http.Flusher)
	return &downstreamSSEWriter{w: w, flusher: flusher}
}

func (writer *downstreamSSEWriter) prepare() {
	writer.w.Header().Set("Content-Type", "text/event-stream")
	writer.w.Header().Set("Content-Disposition", "attachment")
	writer.w.Header().Set("Cache-Control", "no-cache")
	writer.w.Header().Set("Connection", "keep-alive")
}

func (writer *downstreamSSEWriter) write(event string) {
	if event == "" {
		return
	}
	writer.prepare()
	if !writer.committed {
		writer.w.WriteHeader(http.StatusOK)
		writer.committed = true
	}
	_, _ = writer.w.Write([]byte(event))
	if writer.flusher != nil {
		writer.flusher.Flush()
	}
}

// writeComment is a valid SSE keepalive that Antigravity ignores as content.
// It prevents a local client-side idle timeout while an upstream account pool
// is rotating or a temporary network error is being retried.
func (writer *downstreamSSEWriter) writeComment(comment string) {
	comment = strings.ReplaceAll(strings.ReplaceAll(comment, "\r", " "), "\n", " ")
	writer.write(": " + comment + "\n\n")
}

func recoveryDelay(policy streamRecoveryPolicy, attempt int, retryAfter string) time.Duration {
	delay := retryDelay(attempt, retryAfter)
	maximum := time.Duration(policy.maxDelaySeconds) * time.Second
	if maximum > 0 && delay > maximum {
		return maximum
	}
	return delay
}

// canRetryRejectedRequest deliberately permits retries only before the proxy
// has committed an SSE response. It must be used exclusively for a concrete
// non-2xx upstream response: a transport error does not prove that the
// upstream did not receive the request.
func canRetryRejectedRequest(writer *downstreamSSEWriter, policy streamRecoveryPolicy, reconnect int) bool {
	return writer != nil && !writer.committed && policy.enabled && reconnect > 0 && reconnect <= policy.maxAttempts
}

// waitForRejectedRequestRetry waits before retrying a request that the
// upstream explicitly rejected. It intentionally does not send an SSE
// keepalive: doing so would commit a successful downstream response before
// there is a successful upstream stream.
func waitForRejectedRequestRetry(ctx context.Context, policy streamRecoveryPolicy, provider, requestID, reason, retryAfter string, reconnect int) bool {
	if !policy.enabled || reconnect <= 0 || reconnect > policy.maxAttempts || ctx.Err() != nil {
		return false
	}
	delay := recoveryDelay(policy, reconnect, retryAfter)
	trace(provider+"-stream-reconnect", map[string]any{
		"requestId": requestID, "reconnect": reconnect, "maxReconnects": policy.maxAttempts,
		"delayMs": delay.Milliseconds(), "reason": reason,
	})
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// waitForAccountPool retries a temporary scheduler state only before any
// downstream output. The wait is bounded by the user's recovery settings and
// does not consume an upstream request or token while the pool is cooling.
func waitForAccountPool(ctx context.Context, writer *downstreamSSEWriter, policy streamRecoveryPolicy, provider, requestID string, err error, reconnect int) bool {
	retryAfter, temporary := storage.AccountPoolRetryAfter(err)
	if !temporary || writer == nil || writer.committed || !policy.enabled || reconnect <= 0 || reconnect > policy.maxAttempts || ctx.Err() != nil {
		return false
	}
	maximum := time.Duration(policy.maxDelaySeconds) * time.Second
	if maximum > 0 && retryAfter > maximum {
		// A long quota/auth cooldown is not a useful in-request wait. Finish the
		// turn immediately so the user can switch upstream without staring at a
		// spinner for multiple capped intervals.
		return false
	}
	if retryAfter < 100*time.Millisecond {
		retryAfter = 100 * time.Millisecond
	}
	trace(provider+"-account-pool-wait", map[string]any{
		"requestId": requestID, "reconnect": reconnect, "maxReconnects": policy.maxAttempts,
		"delayMs": retryAfter.Milliseconds(), "reason": err.Error(),
	})
	timer := time.NewTimer(retryAfter)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// writeRecoverableTurnStop converts exhausted temporary upstream failures into
// one ordinary completed Antigravity turn. This avoids the IDE's terminal
// Retry loop while still telling the user that no model answer was produced.
// It is never used for permanent credential, model or request errors.
func writeRecoverableTurnStop(writer *downstreamSSEWriter, provider, requestID, modelVersion, message string, reconnects int) {
	message = strings.TrimSpace(message)
	if message == "" {
		message = "上游暂时不可用，本轮未生成内容。请稍后再发送一次；新请求会重新探测当前账户。"
	}
	trace(provider+"-recoverable-turn-stop", map[string]any{
		"requestId": requestID, "reconnects": reconnects, "message": message,
	})
	response := map[string]any{
		"candidates": []any{map[string]any{
			"content":      map[string]any{"role": "model", "parts": []any{map[string]any{"text": message}}},
			"finishReason": "STOP",
		}},
	}
	if modelVersion != "" {
		response["modelVersion"] = modelVersion
	}
	writer.write(encodeAntigravityStreamEvent(response, requestID))
}

func writeRejectedTurnStop(writer *downstreamSSEWriter, provider, requestID, modelVersion string, statusCode int, body string) {
	message := fmt.Sprintf("第三方上游拒绝了当前请求（HTTP %d）。请检查所选模型和接口配置后重新发送。", statusCode)
	switch {
	case statusCode == http.StatusUnauthorized:
		message = "第三方上游鉴权失败（HTTP 401）。请检查 API Key 或登录凭据后重新发送。"
	case statusCode == http.StatusForbidden && isAccountLevelForbidden(body):
		message = "第三方上游账户余额、额度或权限不足（HTTP 403）。请补充额度或修复当前账户权限后重新发送。"
	case statusCode == http.StatusForbidden:
		message = "第三方上游拒绝了当前线路（HTTP 403）。请检查当前账户权限后重新发送。"
	case statusCode == http.StatusNotFound && isModelRouteRejection(body):
		message = "第三方上游没有为当前账户配置所选模型（HTTP 404）。请重新获取模型列表，或切换支持该模型的上游。"
	case statusCode == http.StatusTooManyRequests:
		message = "第三方上游当前请求过多或额度窗口已满（HTTP 429）。XIASS Tools 已按设置重试当前账户，请稍后重新发送。"
	}
	writeRecoverableTurnStop(writer, provider, requestID, modelVersion, message, 0)
}

func writeRecoveredStreamStop(writer *downstreamSSEWriter, requestID, modelVersion, responseID string, reconnects int, unsafe bool) {
	// Do not append a synthetic assistant message here. It would be persisted in
	// Antigravity's conversation and then sent back to the upstream on the next
	// user turn, which is both confusing and a source of repeated replies.
	// A valid SSE comment records the state without becoming chat content.
	reason := "partial-output"
	if unsafe {
		reason = "tool-or-attachment"
	}
	writer.writeComment(fmt.Sprintf("wf-stream-stopped reconnects=%d reason=%s", reconnects, reason))
	writer.write(encodeAntigravityStreamEvent(finalStopResponse(modelVersion, responseID), requestID))
}

// writeUncertainUpstreamFailure never replays a request whose outcome is
// uncertain. If no assistant event has reached Antigravity, return a normal
// HTTP error; otherwise finish the already-visible partial stream without
// adding any assistant text to the conversation.
func writeUncertainUpstreamFailure(writer *downstreamSSEWriter, provider, requestID, modelVersion, responseID string, reconnects int, unsafe bool, message string) {
	message = strings.TrimSpace(message)
	if message == "" {
		message = "上游流式响应在完成前中断；为避免重复执行，本次请求未自动重放"
	}
	trace(provider+"-stream-not-replayed", map[string]any{
		"requestId": requestID, "message": message, "downstreamCommitted": writer != nil && writer.committed,
	})
	if writer != nil && writer.committed {
		writeRecoveredStreamStop(writer, requestID, modelVersion, responseID, reconnects, unsafe)
		return
	}
	if writer == nil {
		return
	}
	writer.w.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.w.WriteHeader(http.StatusBadGateway)
	_ = json.NewEncoder(writer.w).Encode(map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    "uncertain_upstream_result",
		},
	})
}

// returnRejectedUpstreamError forwards an explicit upstream rejection only
// before the downstream response is committed. Keeping this in one place
// avoids accidentally turning a failed retry path into a synthetic success.
func returnRejectedUpstreamError(w http.ResponseWriter, statusCode int, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_, _ = w.Write(body)
}

type streamAttemptOutcome struct {
	wroteEvent      bool
	finished        bool
	emittedText     string
	unsafeOutput    bool
	upstreamStarted bool
	responseID      string
	modelVersion    string
	err             error
}
