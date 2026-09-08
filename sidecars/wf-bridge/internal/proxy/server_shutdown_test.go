package proxy

import (
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"antigravity-wf-assistant/internal/storage"
)

// TestStopBoundsAnActiveHandler exercises the shutdown condition that used to
// make tray/menu exit hang: an active request calls CurrentPort while Stop is
// waiting for it. Stop must release serverMu before Shutdown, use a bounded
// grace period, force-close the socket, and leave no owned server state.
func TestStopBoundsAnActiveHandler(t *testing.T) {
	stateDir := prepareProxyRuntimeTest(t)
	port := findFreeFiveDigitPort(t)
	if err := storage.SaveProxyRuntimePort(port); err != nil {
		t.Fatal(err)
	}

	listener, err := net.Listen("tcp", loopbackAddress(port))
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	queryCurrentPort := make(chan struct{})
	queriedCurrentPort := make(chan struct{})
	releaseHandler := make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(entered)
		<-queryCurrentPort
		_ = CurrentPort()
		close(queriedCurrentPort)
		<-releaseHandler
	})}

	serverMu.Lock()
	srv = server
	activePort = port
	stopping = false
	serverMu.Unlock()
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		close(releaseHandler)
		_ = server.Close()
		_ = Stop()
	})

	requestDone := make(chan struct{})
	go func() {
		_, _ = http.Get("http://" + loopbackAddress(port) + "/active")
		close(requestDone)
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("test request did not reach active handler")
	}

	stopDone := make(chan error, 1)
	startedAt := time.Now()
	go func() { stopDone <- Stop() }()
	waitForProxyStopping(t)
	if err := Start(stateDir); err == nil || !strings.Contains(err.Error(), "正在停止") {
		t.Fatalf("Start must not bind a new endpoint while Stop is draining the old server: %v", err)
	}
	close(queryCurrentPort)
	select {
	case <-queriedCurrentPort:
		// This proves Stop did not hold serverMu while Shutdown waited for the
		// active handler.
	case <-time.After(time.Second):
		t.Fatal("active handler could not read proxy state while shutdown waited")
	}

	select {
	case err := <-stopDone:
		if err != nil {
			t.Fatalf("forced shutdown must still release the endpoint: %v", err)
		}
	case <-time.After(proxyShutdownGracePeriod + time.Second):
		t.Fatal("Stop exceeded the bounded shutdown period")
	}
	if elapsed := time.Since(startedAt); elapsed > proxyShutdownGracePeriod+time.Second {
		t.Fatalf("Stop took %s, exceeding its bounded grace period", elapsed)
	}
	if OwnsListener() {
		t.Fatal("Stop retained an owned server after force-closing the active request")
	}
	serverMu.Lock()
	stillStopping := stopping
	serverMu.Unlock()
	if stillStopping {
		t.Fatal("Stop retained the stopping state after force-close")
	}

	select {
	case <-requestDone:
	case <-time.After(time.Second):
		t.Fatal("force-closed request did not return")
	}
}

// TestStopClosesListenerBeforeServeRegisters covers the startup/shutdown race
// that is most visible on Windows under -race. Start publishes its listener
// before the Serve goroutine gets a chance to register it with http.Server;
// Shutdown alone cannot close that not-yet-registered listener. Stop must
// release the raw listener so a staged proxy endpoint can be rebound
// immediately after a completed stop.
func TestStopClosesListenerBeforeServeRegisters(t *testing.T) {
	prepareProxyRuntimeTest(t)
	port := findFreeFiveDigitPort(t)
	listener, err := net.Listen("tcp", loopbackAddress(port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	serverMu.Lock()
	srv = &http.Server{Handler: http.NewServeMux()}
	srvListener = listener
	activePort = port
	stopping = false
	serverMu.Unlock()

	if err := Stop(); err != nil {
		t.Fatalf("stop unregistered listener: %v", err)
	}

	// Intentionally do not wait: the production caller may restart directly
	// after Stop returns, and this bind must not race the old raw listener.
	rebound, err := net.Listen("tcp", loopbackAddress(port))
	if err != nil {
		t.Fatalf("stopped listener still owns %d: %v", port, err)
	}
	if err := rebound.Close(); err != nil {
		t.Fatal(err)
	}
}

func waitForProxyStopping(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		serverMu.Lock()
		isStopping := stopping
		serverMu.Unlock()
		if isStopping {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("Stop did not enter its shutdown state")
}
