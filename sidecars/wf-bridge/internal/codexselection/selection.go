// Package codexselection implements the XIASS website API-key handoff used by
// the Codex integration. It is intentionally distinct from OAuth: the website
// returns a user-selected API key in a browser fragment, not an OAuth code.
package codexselection

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	DefaultSiteURL          = "https://api.xiass.com"
	defaultSessionLifetime  = 10 * time.Minute
	maxPayloadBytes         = 16 << 10
	maxRequestBytes         = 32 << 10
	maxActiveSessions       = 8
	loopbackShutdownTimeout = 5 * time.Second
)

// State is a credential-safe lifecycle projection. It intentionally excludes
// state values, callback URLs, payloads, and API keys.
type State struct {
	SessionID string `json:"sessionId,omitempty"`
	Status    string `json:"status"`
	Message   string `json:"message"`
	ExpiresAt string `json:"expiresAt,omitempty"`
	BaseURL   string `json:"baseUrl,omitempty"`
	KeyName   string `json:"keyName,omitempty"`
}

// StartResult keeps the browser URL native-only. Callers may open it through
// the platform browser API but must not send it to a renderer or diagnostics.
type StartResult struct {
	State      State
	ConnectURL string
}

// Credential is only delivered to a native caller through WithCredential. It
// is never included in a State, event, status, or JSON response.
type Credential struct {
	BaseURL string
	KeyName string
	APIKey  []byte
}

// Options exists primarily for deterministic tests. Nil dependencies use the
// standard loopback listener and system clock.
type Options struct {
	Now      func() time.Time
	Lifetime time.Duration
	Listen   func(network, address string) (net.Listener, error)
}

type Service struct {
	mu       sync.Mutex
	now      func() time.Time
	lifetime time.Duration
	listen   func(network, address string) (net.Listener, error)
	sessions map[string]*session
}

type session struct {
	id           string
	state        []byte
	expires      time.Time
	status       string
	message      string
	siteHost     string
	callbackHost string
	callbackPath string
	baseURL      string
	keyName      string
	apiKey       []byte
	listener     *loopbackListener
	expiryTimer  *time.Timer
}

type loopbackListener struct {
	listener net.Listener
	server   *http.Server
	once     sync.Once
}

type callbackInput struct {
	Payload string `json:"payload"`
}

type callbackPayload struct {
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key"`
	KeyName string `json:"key_name"`
}

func New(options ...Options) *Service {
	var option Options
	if len(options) > 0 {
		option = options[0]
	}
	clock := option.Now
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	lifetime := option.Lifetime
	if lifetime <= 0 {
		lifetime = defaultSessionLifetime
	}
	listener := option.Listen
	if listener == nil {
		listener = net.Listen
	}
	return &Service{now: clock, lifetime: lifetime, listen: listener, sessions: make(map[string]*session)}
}

// Begin creates one short-lived, one-time browser handoff. The listener is
// bound only to 127.0.0.1 with an OS-selected random port.
func (service *Service) Begin(siteURL string) (StartResult, error) {
	if service == nil {
		return StartResult{}, errors.New("XIASS key selection service is unavailable")
	}
	site, err := normalizeSiteURL(siteURL)
	if err != nil {
		return StartResult{}, err
	}
	state, err := randomToken(32)
	if err != nil {
		return StartResult{}, errors.New("could not create key selection state")
	}
	sessionIDBytes, err := randomToken(18)
	if err != nil {
		zeroBytes(state)
		return StartResult{}, errors.New("could not create key selection session")
	}
	sessionID := string(sessionIDBytes)
	zeroBytes(sessionIDBytes)
	listener, err := service.listen("tcp4", "127.0.0.1:0")
	if err != nil {
		zeroBytes(state)
		return StartResult{}, errors.New("could not reserve a local callback port")
	}
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok || address == nil || !address.IP.IsLoopback() || address.Port < 1024 || address.Port > 65535 {
		_ = listener.Close()
		zeroBytes(state)
		return StartResult{}, errors.New("local callback listener is invalid")
	}
	callbackHost := net.JoinHostPort("127.0.0.1", itoa(address.Port))
	callbackURL := (&url.URL{Scheme: "http", Host: callbackHost, Path: "/callback"}).String()
	connectURL := makeConnectURL(site, callbackURL, string(state))
	now := service.now().UTC()
	entry := &session{
		id:           sessionID,
		state:        state,
		expires:      now.Add(service.lifetime),
		status:       "pending",
		message:      "正在等待你在 XIASS API 网站选择 API Key。",
		siteHost:     normalizedHost(site),
		callbackHost: callbackHost,
		callbackPath: "/callback",
	}
	callback := &loopbackListener{listener: listener}
	entry.listener = callback
	callback.server = &http.Server{
		Handler:           service.handler(sessionID),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		IdleTimeout:       10 * time.Second,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          log.New(io.Discard, "", 0),
	}

	service.mu.Lock()
	expired := service.expireLocked(service.now().UTC())
	if len(service.sessions) >= maxActiveSessions {
		service.mu.Unlock()
		closeLoopbacks(expired)
		callback.Close()
		zeroBytes(state)
		return StartResult{}, errors.New("too many pending XIASS key selections")
	}
	service.sessions[sessionID] = entry
	entry.expiryTimer = time.AfterFunc(service.lifetime, func() { service.expire(sessionID) })
	service.mu.Unlock()
	closeLoopbacks(expired)

	go func() { _ = callback.server.Serve(listener) }()
	return StartResult{
		State: State{
			SessionID: sessionID,
			Status:    "pending",
			Message:   "已在浏览器打开 XIASS API 的 Key 选择页；完成后会自动返回 XIASS Tools。",
			ExpiresAt: entry.expires.Format(time.RFC3339),
		},
		ConnectURL: connectURL,
	}, nil
}

// Status returns only a redacted session projection.
func (service *Service) Status(sessionID string) State {
	if service == nil || strings.TrimSpace(sessionID) == "" {
		return State{Status: "unknown", Message: "未找到 XIASS Key 选择会话。"}
	}
	service.mu.Lock()
	resources := service.expireLocked(service.now().UTC())
	entry := service.sessions[strings.TrimSpace(sessionID)]
	var state State
	if entry == nil {
		state = State{SessionID: strings.TrimSpace(sessionID), Status: "expired", Message: "Key 选择会话已结束或已过期。"}
	} else {
		state = entry.publicState()
	}
	service.mu.Unlock()
	closeLoopbacks(resources)
	return state
}

// CompleteManualCallback is a fallback for browsers that could not run the
// local callback page. The complete URL is parsed locally and cleared by the
// caller immediately after this method returns.
func (service *Service) CompleteManualCallback(sessionID, rawCallbackURL string) State {
	sessionID = strings.TrimSpace(sessionID)
	parsed, err := url.Parse(strings.TrimSpace(rawCallbackURL))
	if err != nil || parsed == nil {
		return State{SessionID: sessionID, Status: "pending", Message: "回调地址无效，请粘贴浏览器中的完整地址。"}
	}
	service.mu.Lock()
	resources := service.expireLocked(service.now().UTC())
	entry := service.sessions[sessionID]
	if entry == nil {
		service.mu.Unlock()
		closeLoopbacks(resources)
		return State{SessionID: sessionID, Status: "expired", Message: "Key 选择会话已结束或已过期。"}
	}
	validCallback := manualCallbackMatches(parsed, entry)
	service.mu.Unlock()
	closeLoopbacks(resources)
	if !validCallback {
		return State{SessionID: sessionID, Status: "pending", Message: "回调地址不属于当前 Key 选择会话。"}
	}
	values, err := url.ParseQuery(parsed.Fragment)
	if err != nil || len(values) != 2 || len(values["state"]) != 1 || len(values["payload"]) != 1 {
		return State{SessionID: sessionID, Status: "pending", Message: "回调地址缺少有效的选择信息。"}
	}
	return service.acceptPayload(sessionID, values.Get("state"), values.Get("payload"))
}

// WithCredential invokes a native-only function with the selected key. The
// key is copied into a short-lived byte slice, zeroed afterward, and never
// included in any serializable result. Successful consumers can consume the
// session so the key cannot be reused by a duplicate callback.
func (service *Service) WithCredential(sessionID string, consume bool, use func(Credential) error) (State, error) {
	if service == nil || use == nil {
		return State{Status: "unknown", Message: "XIASS Key 选择会话不可用。"}, errors.New("XIASS key selection is unavailable")
	}
	service.mu.Lock()
	resources := service.expireLocked(service.now().UTC())
	entry := service.sessions[strings.TrimSpace(sessionID)]
	if entry == nil || entry.status != "ready" || len(entry.apiKey) == 0 {
		service.mu.Unlock()
		closeLoopbacks(resources)
		return State{SessionID: strings.TrimSpace(sessionID), Status: "expired", Message: "请重新从 XIASS API 选择一个 Key。"}, errors.New("XIASS key selection is not ready")
	}
	credential := Credential{
		BaseURL: entry.baseURL,
		KeyName: entry.keyName,
		APIKey:  append([]byte(nil), entry.apiKey...),
	}
	before := entry.publicState()
	service.mu.Unlock()
	closeLoopbacks(resources)

	err := use(credential)
	zeroBytes(credential.APIKey)
	if err != nil {
		return before, err
	}
	if !consume {
		return service.Status(sessionID), nil
	}
	service.mu.Lock()
	entry = service.sessions[strings.TrimSpace(sessionID)]
	var discarded *loopbackListener
	if entry != nil {
		discarded = service.disposeLocked(entry)
		delete(service.sessions, entry.id)
	}
	service.mu.Unlock()
	if discarded != nil {
		discarded.Close()
	}
	return State{SessionID: strings.TrimSpace(sessionID), Status: "applied", Message: "已使用所选 Key 安全写入 Codex 配置。"}, nil
}

func (service *Service) Cancel(sessionID string) State {
	if service == nil {
		return State{Status: "cancelled", Message: "Key 选择会话已结束。"}
	}
	service.mu.Lock()
	entry := service.sessions[strings.TrimSpace(sessionID)]
	var resource *loopbackListener
	if entry != nil {
		resource = service.disposeLocked(entry)
		delete(service.sessions, entry.id)
	}
	service.mu.Unlock()
	if resource != nil {
		resource.Close()
	}
	return State{SessionID: strings.TrimSpace(sessionID), Status: "cancelled", Message: "已取消 XIASS Key 选择会话。"}
}

// Close releases every local listener and clears all in-memory credentials.
func (service *Service) Close() {
	if service == nil {
		return
	}
	service.mu.Lock()
	resources := make([]*loopbackListener, 0, len(service.sessions))
	for id, entry := range service.sessions {
		if resource := service.disposeLocked(entry); resource != nil {
			resources = append(resources, resource)
		}
		delete(service.sessions, id)
	}
	service.mu.Unlock()
	closeLoopbacks(resources)
}

func (service *Service) handler(sessionID string) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !service.trustedLoopbackRequest(sessionID, request) {
			http.Error(writer, "local callback only", http.StatusForbidden)
			return
		}
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/callback":
			writeCallbackPage(writer)
		case request.Method == http.MethodPost && request.URL.Path == "/complete":
			service.handleBrowserCompletion(sessionID, writer, request)
		default:
			http.NotFound(writer, request)
		}
	})
}

func (service *Service) trustedLoopbackRequest(sessionID string, request *http.Request) bool {
	remoteHost, _, err := net.SplitHostPort(strings.TrimSpace(request.RemoteAddr))
	if err != nil || !net.ParseIP(remoteHost).IsLoopback() {
		return false
	}
	service.mu.Lock()
	entry := service.sessions[sessionID]
	expectedHost := ""
	if entry != nil {
		expectedHost = entry.callbackHost
	}
	service.mu.Unlock()
	return expectedHost != "" && strings.EqualFold(strings.TrimSpace(request.Host), expectedHost)
}

func (service *Service) handleBrowserCompletion(sessionID string, writer http.ResponseWriter, request *http.Request) {
	defer request.Body.Close()
	reader := http.MaxBytesReader(writer, request.Body, maxRequestBytes)
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	var input callbackInput
	if err := decoder.Decode(&input); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		http.Error(writer, "invalid callback", http.StatusBadRequest)
		return
	}
	state := strings.TrimSpace(request.Header.Get("X-XIASS-Selection-State"))
	result := service.acceptPayload(sessionID, state, input.Payload)
	if result.Status != "ready" {
		http.Error(writer, "callback could not be completed", http.StatusBadRequest)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = io.WriteString(writer, `{"ok":true}`)
}

func (service *Service) acceptPayload(sessionID, receivedState, encodedPayload string) State {
	service.mu.Lock()
	resources := service.expireLocked(service.now().UTC())
	entry := service.sessions[strings.TrimSpace(sessionID)]
	if entry == nil {
		service.mu.Unlock()
		closeLoopbacks(resources)
		return State{SessionID: strings.TrimSpace(sessionID), Status: "expired", Message: "Key 选择会话已结束或已过期。"}
	}
	if entry.status == "ready" {
		result := entry.publicState()
		service.mu.Unlock()
		closeLoopbacks(resources)
		return result
	}
	if entry.status != "pending" || !constantTimeEqual(entry.state, []byte(strings.TrimSpace(receivedState))) {
		result := State{SessionID: entry.id, Status: "pending", Message: "网站返回的选择信息未通过验证。"}
		service.mu.Unlock()
		closeLoopbacks(resources)
		return result
	}
	payload, err := decodePayload(encodedPayload, entry.siteHost)
	if err != nil {
		result := State{SessionID: entry.id, Status: "pending", Message: "网站返回的 Key 信息无效，请重新选择。"}
		service.mu.Unlock()
		closeLoopbacks(resources)
		return result
	}
	entry.baseURL = payload.BaseURL
	entry.keyName = payload.KeyName
	zeroBytes(entry.apiKey)
	entry.apiKey = []byte(payload.APIKey)
	entry.status = "ready"
	entry.message = "已选择 API Key，可获取模型或安全写入 Codex 配置。"
	callback := entry.listener
	entry.listener = nil
	result := entry.publicState()
	service.mu.Unlock()
	closeLoopbacks(resources)
	if callback != nil {
		callback.Shutdown()
	}
	return result
}

func (service *Service) expire(sessionID string) {
	service.mu.Lock()
	entry := service.sessions[sessionID]
	var resource *loopbackListener
	if entry != nil && !service.now().UTC().Before(entry.expires) {
		resource = service.disposeLocked(entry)
		delete(service.sessions, sessionID)
	}
	service.mu.Unlock()
	if resource != nil {
		resource.Close()
	}
}

func (service *Service) expireLocked(now time.Time) []*loopbackListener {
	resources := make([]*loopbackListener, 0)
	for id, entry := range service.sessions {
		if now.Before(entry.expires) {
			continue
		}
		if resource := service.disposeLocked(entry); resource != nil {
			resources = append(resources, resource)
		}
		delete(service.sessions, id)
	}
	return resources
}

func (service *Service) disposeLocked(entry *session) *loopbackListener {
	if entry == nil {
		return nil
	}
	if entry.expiryTimer != nil {
		entry.expiryTimer.Stop()
		entry.expiryTimer = nil
	}
	zeroBytes(entry.state)
	zeroBytes(entry.apiKey)
	entry.apiKey = nil
	resource := entry.listener
	entry.listener = nil
	return resource
}

func (entry *session) publicState() State {
	if entry == nil {
		return State{Status: "unknown", Message: "未找到 XIASS Key 选择会话。"}
	}
	return State{
		SessionID: entry.id,
		Status:    entry.status,
		Message:   entry.message,
		ExpiresAt: entry.expires.UTC().Format(time.RFC3339),
		BaseURL:   entry.baseURL,
		KeyName:   entry.keyName,
	}
}

func (listener *loopbackListener) Close() {
	if listener == nil {
		return
	}
	listener.once.Do(func() {
		if listener.server != nil {
			_ = listener.server.Close()
		}
		if listener.listener != nil {
			_ = listener.listener.Close()
		}
	})
}

// Shutdown stops accepting new loopback callbacks without aborting the
// successful /complete request that is currently writing its response. Close
// is deliberately retained for cancellation, expiry, and app shutdown where
// an immediate connection abort is appropriate.
func (listener *loopbackListener) Shutdown() {
	if listener == nil {
		return
	}
	listener.once.Do(func() {
		server := listener.server
		rawListener := listener.listener
		go func() {
			if server == nil {
				if rawListener != nil {
					_ = rawListener.Close()
				}
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), loopbackShutdownTimeout)
			defer cancel()
			_ = server.Shutdown(ctx)
		}()
	})
}

func closeLoopbacks(resources []*loopbackListener) {
	for _, resource := range resources {
		resource.Close()
	}
}

func normalizeSiteURL(value string) (*url.URL, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		value = DefaultSiteURL
	}
	if !strings.Contains(value, "://") {
		value = "https://" + value
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed == nil || !strings.EqualFold(parsed.Scheme, "https") || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("XIASS site must be an HTTPS URL without credentials, query, or fragment")
	}
	parsed.Scheme = "https"
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.RawPath = ""
	return parsed, nil
}

func makeConnectURL(site *url.URL, callbackURL, state string) string {
	endpoint := *site
	basePath := strings.TrimRight(strings.TrimSpace(endpoint.Path), "/")
	endpoint.Path = basePath + "/codex-helper/connect"
	endpoint.RawPath = ""
	endpoint.RawQuery = url.Values{"callback": []string{callbackURL}, "state": []string{state}}.Encode()
	endpoint.ForceQuery = false
	endpoint.Fragment = ""
	return endpoint.String()
}

func normalizedHost(parsed *url.URL) string {
	if parsed == nil {
		return ""
	}
	return strings.ToLower(parsed.Host)
}

func manualCallbackMatches(parsed *url.URL, entry *session) bool {
	if parsed == nil || entry == nil || !strings.EqualFold(parsed.Scheme, "http") || parsed.User != nil || parsed.RawQuery != "" || parsed.Path != entry.callbackPath || !strings.EqualFold(parsed.Host, entry.callbackHost) {
		return false
	}
	return parsed.Fragment != ""
}

func decodePayload(encoded, expectedHost string) (callbackPayload, error) {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" || len(encoded) > maxPayloadBytes*2 {
		return callbackPayload{}, errors.New("payload length is invalid")
	}
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(data) == 0 || len(data) > maxPayloadBytes {
		return callbackPayload{}, errors.New("payload encoding is invalid")
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var payload callbackPayload
	if err := decoder.Decode(&payload); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return callbackPayload{}, errors.New("payload format is invalid")
	}
	payload.BaseURL = strings.TrimSpace(payload.BaseURL)
	payload.APIKey = strings.TrimSpace(payload.APIKey)
	payload.KeyName = strings.TrimSpace(payload.KeyName)
	if payload.BaseURL == "" || payload.APIKey == "" || len(payload.APIKey) > 8192 || containsControl(payload.APIKey) {
		return callbackPayload{}, errors.New("payload credential is invalid")
	}
	parsed, err := url.Parse(payload.BaseURL)
	if err != nil || parsed == nil || !strings.EqualFold(parsed.Scheme, "https") || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || !strings.EqualFold(normalizedHost(parsed), expectedHost) {
		return callbackPayload{}, errors.New("payload API endpoint is invalid")
	}
	if payload.KeyName == "" {
		payload.KeyName = "XIASS API Key"
	}
	if len(payload.KeyName) > 200 || containsControl(payload.KeyName) {
		return callbackPayload{}, errors.New("payload key name is invalid")
	}
	return payload, nil
}

func randomToken(size int) ([]byte, error) {
	bytes := make([]byte, size)
	if _, err := rand.Read(bytes); err != nil {
		return nil, err
	}
	return []byte(base64.RawURLEncoding.EncodeToString(bytes)), nil
}

func constantTimeEqual(left, right []byte) bool {
	leftHash := sha256.Sum256(left)
	rightHash := sha256.Sum256(right)
	return subtle.ConstantTimeCompare(leftHash[:], rightHash[:]) == 1
}

func containsControl(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}

func zeroBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

func itoa(value int) string {
	return strconv.Itoa(value)
}

func writeCallbackPage(writer http.ResponseWriter) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Pragma", "no-cache")
	writer.Header().Set("Referrer-Policy", "no-referrer")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.Header().Set("X-Frame-Options", "DENY")
	writer.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; connect-src 'self'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(writer, callbackPage)
}

const callbackPage = `<!doctype html><meta charset="utf-8"><meta name="referrer" content="no-referrer"><title>XIASS Tools</title><style>body{font:14px -apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;margin:44px;color:#26323d;background:#fff}p{line-height:1.6}.fallback{display:flex;gap:8px;margin-top:16px}button{font:inherit;padding:7px 11px;cursor:pointer}[hidden]{display:none!important}</style><p id="message">正在安全返回 XIASS Tools…</p><div id="fallback" class="fallback" hidden><button id="retry-callback" type="button">重新提交</button><button id="copy-callback" type="button">复制完整回调地址</button></div><script>(()=>{const rawCallbackURL=location.href,output=document.getElementById("message"),fallback=document.getElementById("fallback"),retry=document.getElementById("retry-callback"),copy=document.getElementById("copy-callback"),fragment=new URLSearchParams(location.hash.slice(1)),state=fragment.get("state")||"",payload=fragment.get("payload")||"";let submitting=false;history.replaceState(null,"",location.pathname);function showFallback(message){output.textContent=message;fallback.hidden=false}async function submit(){if(submitting)return;submitting=true;fallback.hidden=true;output.textContent="正在安全返回 XIASS Tools…";try{const response=await fetch("/complete",{method:"POST",headers:{"Content-Type":"application/json","X-XIASS-Selection-State":state},body:JSON.stringify({payload}),credentials:"omit",cache:"no-store"});if(!response.ok)throw new Error();output.textContent="已返回 XIASS Tools，可以关闭此页面。"}catch{showFallback("自动返回失败。可重新提交，或复制完整回调地址后回到 XIASS Tools 手动完成。")}finally{submitting=false}}function fallbackCopy(){const area=document.createElement("textarea");area.value=rawCallbackURL;area.setAttribute("aria-hidden","true");area.style.cssText="position:fixed;left:-9999px;top:0;opacity:0";document.body.appendChild(area);area.select();try{return document.execCommand("copy")}finally{area.remove()}}async function copyCallback(){let copied=false;if(navigator.clipboard&&typeof navigator.clipboard.writeText==="function"){try{await navigator.clipboard.writeText(rawCallbackURL);copied=true}catch{}}if(!copied)copied=fallbackCopy();output.textContent=copied?"完整回调地址已复制，可回到 XIASS Tools 手动完成。":"无法复制完整回调地址，请回到 XIASS Tools 重新开始。"}retry.addEventListener("click",submit);copy.addEventListener("click",copyCallback);if(!state||!payload||payload.length>32768){output.textContent="返回信息无效，请回到 XIASS Tools 重试。";return}submit()})()</script>`
