package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"antigravity-wf-assistant/internal/proxyendpoint"
	"antigravity-wf-assistant/internal/storage"
)

const (
	// ProxyPort is kept as a source-compatible name for integrations built
	// against older releases. Runtime traffic must use CurrentPort instead.
	ProxyPort = proxyendpoint.DefaultPort

	// Fallback ports must remain five digits because Language Server endpoint
	// strings are patched in-place without changing binary length.
	fallbackProxyPortStart = 51000

	// A local proxy can have an active upstream/SSE request while the user
	// exits the desktop app. Shutdown must never leave the tray/menu action
	// waiting forever for that request to return.
	proxyShutdownGracePeriod = 2 * time.Second
)

var (
	serverMu    sync.Mutex
	srv         *http.Server
	srvListener net.Listener
	activePort  int
	stopping    bool
)

// Start binds the persisted local endpoint. New installations prefer 50999;
// if another process owns it, a free five-digit loopback port is selected and
// persisted only after its listener is acquired. That guarantees future patch
// operations and restarts use the same endpoint.
func Start(storageDir string) error {
	serverMu.Lock()
	defer serverMu.Unlock()

	if srv != nil {
		return nil // already running
	}
	if stopping {
		return fmt.Errorf("本地代理正在停止，请稍后重试")
	}
	// Do not let a stale in-memory value from a stopped server override the
	// persisted endpoint selected for this launch.
	activePort = 0
	configuredPort, err := storage.LoadCommittedProxyRuntimePort()
	if err != nil {
		return fmt.Errorf("读取本地代理运行状态失败；为避免已补丁的 Antigravity 指向错误端口，未启动代理: %w", err)
	}
	pending, err := storage.HasStagedProxyRuntimePort()
	if err != nil {
		return fmt.Errorf("读取未完成的本地代理端口切换失败；为避免 Antigravity 指向错误端口，未启动代理: %w", err)
	}

	InitTrace(storageDir)
	if settings, err := storage.LoadAppSettings(); err != nil {
		log.Printf("[xiass-tools] 读取代理恢复设置失败，使用安全默认值: %v", err)
	} else {
		ConfigureStreamRecovery(settings.StreamRecovery)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", handleRequest)
	if pending {
		// A previous ApplyPatch may have written the staged Q immediately
		// before a crash or an atomic state-commit error. Never silently
		// return to P here: files can already target Q. Keep Q live and let
		// the explicit Apply operation finish/verify the transaction.
		stagedPort, stagedErr := storage.LoadProxyRuntimePort()
		if stagedErr != nil {
			return fmt.Errorf("读取暂存的本地代理端口失败: %w", stagedErr)
		}
		ln, listenErr := net.Listen("tcp", loopbackAddress(stagedPort))
		if listenErr != nil {
			if isManagedListenerAt(stagedPort) {
				activePort = stagedPort
				log.Printf("[xiass-tools] 已复用未完成端口切换所需的本地代理")
				return nil
			}
			return fmt.Errorf("检测到未完成的本地代理端口切换；为避免已补丁的 Antigravity 断连，未改用旧连接。请保持助手运行并重新应用补丁")
		}
		startServerLocked(mux, ln, stagedPort)
		return nil
	}

	for index, port := range proxyPortCandidates(configuredPort) {
		ln, listenErr := net.Listen("tcp", loopbackAddress(port))
		if listenErr != nil {
			// Only the committed endpoint may be reused. A managed listener on
			// an arbitrary fallback port can belong to another configuration, so
			// selecting it would silently route this user's models elsewhere.
			if index == 0 && isManagedListenerAt(port) {
				activePort = port
				log.Printf("[xiass-tools] 已复用另一个当前 XIASS Tools 实例的本地代理")
				return nil
			}
			continue
		}
		if port != configuredPort {
			// Do not commit Q yet. Existing Antigravity resources may still
			// point to P, and committing Q here would make them fail after the
			// next restart. ApplyPatch promotes Q only after it has rewritten
			// every selected target successfully.
			if stageErr := storage.StageProxyRuntimePort(port); stageErr != nil {
				_ = ln.Close()
				return fmt.Errorf("已找到可用本地代理端口，但暂存端口切换失败: %w", stageErr)
			}
		}
		startServerLocked(mux, ln, port)
		return nil
	}
	return fmt.Errorf("未找到可用的五位本地代理端口；请关闭占用本地端口的程序后重试")
}

func startServerLocked(mux *http.ServeMux, ln net.Listener, port int) {
	server := &http.Server{Handler: mux}
	srv = server
	srvListener = ln
	activePort = port
	go func() {
		log.Printf("[xiass-tools] 本地代理已启动")
		if err := server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			log.Printf("[xiass-tools] 本地代理服务器错误: %v", err)
		}
		serverMu.Lock()
		if srv == server {
			srv = nil
			srvListener = nil
			activePort = 0
		}
		serverMu.Unlock()
	}()
	trace("proxy-started", map[string]any{"port": port})
}

func proxyPortCandidates(preferred int) []int {
	if !proxyendpoint.IsSupportedPort(preferred) {
		preferred = proxyendpoint.DefaultPort
	}
	candidates := make([]int, 0, proxyendpoint.MaxPort-fallbackProxyPortStart+2)
	candidates = append(candidates, preferred)
	for port := fallbackProxyPortStart; port <= proxyendpoint.MaxPort; port++ {
		if port != preferred {
			candidates = append(candidates, port)
		}
	}
	return candidates
}

func loopbackAddress(port int) string {
	return fmt.Sprintf("127.0.0.1:%d", port)
}

// Stop shuts the proxy down without holding serverMu while http.Server waits
// for requests. A handler can legitimately call CurrentPort/other helpers;
// holding the mutex here would deadlock shutdown. Long-lived SSE/upstream
// streams get a small grace period, then Close releases their connections so
// quitting the desktop app always releases the local endpoint.
func Stop() error {
	serverMu.Lock()
	if stopping {
		serverMu.Unlock()
		return nil
	}
	if srv == nil {
		port := currentPortLocked()
		serverMu.Unlock()
		if isListeningAt(port) && !isManagedListenerAt(port) {
			return fmt.Errorf("本地代理监听器不属于当前 XIASS Tools，无法从这里停止")
		}
		return nil
	}
	server := srv
	listener := srvListener
	srv = nil
	srvListener = nil
	// Do not keep an old in-memory endpoint after the listener has begun to
	// close. CurrentPort will safely resolve the persisted endpoint instead.
	activePort = 0
	stopping = true
	serverMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), proxyShutdownGracePeriod)
	err := server.Shutdown(ctx)
	cancel()
	// http.Server only begins tracking a listener from inside Serve. Start
	// launches Serve asynchronously, so a fast Stop can otherwise call
	// Shutdown before that registration occurs. Close the raw listener we own
	// after Shutdown has marked the server as stopping: this closes the
	// pre-registration window without making a normal Shutdown report
	// net.ErrClosed for a listener it was already tracking.
	if listener != nil {
		if closeErr := listener.Close(); closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
			log.Printf("[xiass-tools] 关闭本地代理监听器时出现异常: %v", closeErr)
		}
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		// Shutdown timed out or failed while a handler was still running. Close
		// forcefully releases active sockets; it intentionally does not wait for
		// an upstream request that may never finish.
		closeErr := server.Close()
		serverMu.Lock()
		stopping = false
		serverMu.Unlock()
		if closeErr != nil && !errors.Is(closeErr, http.ErrServerClosed) {
			return fmt.Errorf("本地代理未能在限定时间内停止，且强制关闭失败: %w", closeErr)
		}
		// From the desktop user's perspective the endpoint is now released;
		// retain the timeout only in the local diagnostic log instead of
		// reporting a false failed-exit result.
		log.Printf("[xiass-tools] 本地代理有活动请求，已在等待后强制关闭: %v", err)
		return nil
	}

	serverMu.Lock()
	stopping = false
	serverMu.Unlock()
	return nil
}

// CurrentPort returns the active or persisted endpoint port. It is intentionally
// an internal runtime detail and is not shown in the normal desktop UI.
func CurrentPort() int {
	serverMu.Lock()
	defer serverMu.Unlock()
	return currentPortLocked()
}

func currentPortLocked() int {
	if proxyendpoint.IsSupportedPort(activePort) {
		return activePort
	}
	port, err := storage.LoadProxyRuntimePort()
	if err == nil && proxyendpoint.IsSupportedPort(port) {
		return port
	}
	return proxyendpoint.DefaultPort
}

// CommitSelectedPort promotes a staged fallback only after the caller has
// finished an explicit successful ApplyPatch transaction. It never rewrites
// Antigravity files itself and never commits a port different from the one the
// running proxy is actually serving.
func CommitSelectedPort() error {
	serverMu.Lock()
	port := currentPortLocked()
	serverMu.Unlock()
	effectivePort, err := storage.LoadProxyRuntimePort()
	if err != nil {
		return fmt.Errorf("读取本地代理运行状态失败: %w", err)
	}
	if effectivePort != port {
		return fmt.Errorf("本地代理端口状态已变化；为避免写入不一致配置，未提交端口切换")
	}
	_, err = storage.CommitStagedProxyRuntimePort()
	return err
}

// IsManagedListener verifies that the listener implements the current helper
// health contract instead of trusting any process that happens to own a port.
func IsManagedListener() bool {
	return isManagedListenerAt(CurrentPort())
}

func isManagedListenerAt(port int) bool {
	client := &http.Client{Timeout: 500 * time.Millisecond}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/_antigravity-wf/health", port))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK && resp.Header.Get("X-Antigravity-WF") == "go-proxy"
}

func OwnsListener() bool {
	serverMu.Lock()
	defer serverMu.Unlock()
	return srv != nil
}

// IsListening returns true if the selected proxy endpoint is currently
// accepting connections.
func IsListening() bool {
	return isListeningAt(CurrentPort())
}

func isListeningAt(port int) bool {
	conn, err := net.DialTimeout("tcp", loopbackAddress(port), 400*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// readBody reads and returns the request body.
func readBody(r *http.Request) ([]byte, error) {
	defer r.Body.Close()
	return io.ReadAll(r.Body)
}

// handleRequest is the top-level HTTP handler.
func handleRequest(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if path == "/_antigravity-wf/health" {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("X-Antigravity-WF", "go-proxy")
		_, _ = io.WriteString(w, `{"ok":true,"proxy":"antigravity-wf"}`)
		return
	}

	cleanPath := cleanPatchedPath(path)

	trace("request", map[string]any{
		"method":    r.Method,
		"path":      path,
		"cleanPath": cleanPath,
	})

	if strings.Contains(cleanPath, ":fetchAvailableModels") {
		handleFetchAvailableModels(w, r)
		return
	}

	if strings.Contains(cleanPath, ":streamGenerateContent") ||
		strings.Contains(cleanPath, ":generateContent") {
		handleGenerate(w, r, cleanPath)
		return
	}

	// Everything else: passthrough
	body, err := readBody(r)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	passthroughRequest(w, r, body, cleanPath)
}

// cleanPatchedPath strips the path prefix inserted by the text and fixed-size
// binary patch variants before a request is routed or passed through.
func cleanPatchedPath(path string) string {
	prefixes := []string{
		"/v1internal/antigravity-wf/",
		"/v1internal/wfproxy/",
		"/v1internal/wfproxy-sandbox/",
	}
	// Upgrade compatibility: accept requests from applications patched by a
	// pre-WF release until the user reapplies the current patch.
	prefixes = append(prefixes, legacyPatchedPrefixes()...)
	for _, prefix := range prefixes {
		if strings.HasPrefix(path, prefix) {
			cleanPath := strings.TrimPrefix(path, prefix)
			if strings.HasPrefix(cleanPath, "v1internal:") || strings.HasPrefix(cleanPath, "v1internal/") {
				return "/" + cleanPath
			}
		}
	}
	// Newer Language Server binaries can embed a different Cloud Code hostname.
	// The patcher preserves Mach-O byte length by placing an opaque single
	// filler segment under /v1internal/. Strip that segment only when what
	// follows is a real Antigravity v1internal path; arbitrary local-proxy paths
	// remain untouched.
	const binaryPrefix = "/v1internal/"
	if strings.HasPrefix(path, binaryPrefix) {
		remainder := strings.TrimPrefix(path, binaryPrefix)
		if slash := strings.IndexByte(remainder, '/'); slash >= 0 {
			candidate := remainder[slash:]
			if strings.HasPrefix(candidate, "/v1internal:") || strings.HasPrefix(candidate, "/v1internal/") {
				return candidate
			}
		}
	}
	return path
}
