package proxy

import (
	"net"
	"net/http"
	"testing"
	"time"

	"antigravity-wf-assistant/internal/proxyendpoint"
	"antigravity-wf-assistant/internal/storage"
)

func TestProxyPortCandidatesPreferHistoricalDefaultAndStayFixedWidth(t *testing.T) {
	candidates := proxyPortCandidates(proxyendpoint.DefaultPort)
	if len(candidates) == 0 || candidates[0] != proxyendpoint.DefaultPort {
		t.Fatalf("default candidate order = %v", candidates[:minInt(len(candidates), 3)])
	}
	for _, port := range candidates[:minInt(len(candidates), 32)] {
		if !proxyendpoint.IsSupportedPort(port) {
			t.Fatalf("candidate %d is not a five-digit patch-safe port", port)
		}
	}
	for _, port := range []int{9999, 100000} {
		if proxyendpoint.IsSupportedPort(port) {
			t.Fatalf("invalid binary-unsafe port accepted: %d", port)
		}
	}
}

func TestFallbackDoesNotCommitOverExistingPatchedEndpointBeforeApply(t *testing.T) {
	stateDir := prepareProxyRuntimeTest(t)
	primary := findFreeFiveDigitPort(t)
	if err := storage.SaveProxyRuntimePort(primary); err != nil {
		t.Fatal(err)
	}
	occupied, err := net.Listen("tcp", loopbackAddress(primary))
	if err != nil {
		t.Fatal(err)
	}

	if err := Start(stateDir); err != nil {
		_ = occupied.Close()
		t.Fatal(err)
	}
	fallback := CurrentPort()
	if fallback == primary || !proxyendpoint.IsSupportedPort(fallback) {
		t.Fatalf("fallback port = %d, want a different five-digit port from %d", fallback, primary)
	}
	committed, err := storage.LoadCommittedProxyRuntimePort()
	if err != nil {
		t.Fatal(err)
	}
	if committed != primary {
		t.Fatalf("external collision silently changed committed endpoint from %d to %d", primary, committed)
	}
	effective, err := storage.LoadProxyRuntimePort()
	if err != nil {
		t.Fatal(err)
	}
	if effective != fallback {
		t.Fatalf("patch transaction endpoint = %d, want live fallback %d", effective, fallback)
	}
	pending, err := storage.HasStagedProxyRuntimePort()
	if err != nil || !pending {
		t.Fatalf("fallback must remain staged until explicit apply: pending=%t err=%v", pending, err)
	}
	if err := Stop(); err != nil {
		t.Fatal(err)
	}
	if err := occupied.Close(); err != nil {
		t.Fatal(err)
	}

	// A later startup preserves Q's pending journal instead of quietly returning
	// to P: a crash can occur after Antigravity was rewritten to Q but before
	// the final runtime-state commit. The UI blocks launch until Apply completes.
	if err := Start(stateDir); err != nil {
		t.Fatal(err)
	}
	if got := CurrentPort(); got != fallback {
		t.Fatalf("restart chose %d, want staged endpoint %d", got, fallback)
	}
	pending, err = storage.HasStagedProxyRuntimePort()
	if err != nil || !pending {
		t.Fatalf("staged fallback journal was silently cleared: pending=%t err=%v", pending, err)
	}
}

func TestSuccessfulFallbackCommitSurvivesRestart(t *testing.T) {
	stateDir := prepareProxyRuntimeTest(t)
	primary := findFreeFiveDigitPort(t)
	if err := storage.SaveProxyRuntimePort(primary); err != nil {
		t.Fatal(err)
	}
	occupied, err := net.Listen("tcp", loopbackAddress(primary))
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	if err := Start(stateDir); err != nil {
		t.Fatal(err)
	}
	fallback := CurrentPort()
	if err := CommitSelectedPort(); err != nil {
		t.Fatal(err)
	}
	committed, err := storage.LoadCommittedProxyRuntimePort()
	if err != nil || committed != fallback {
		t.Fatalf("committed fallback = %d, err=%v; want %d", committed, err, fallback)
	}
	pending, err := storage.HasStagedProxyRuntimePort()
	if err != nil || pending {
		t.Fatalf("successful apply must clear staged state: pending=%t err=%v", pending, err)
	}
	if err := Stop(); err != nil {
		t.Fatal(err)
	}
	if err := Start(stateDir); err != nil {
		t.Fatal(err)
	}
	if got := CurrentPort(); got != fallback {
		t.Fatalf("restart chose %d, want committed fallback %d", got, fallback)
	}
}

func TestCommittedManagedListenerIsReusedWithoutTakingOwnership(t *testing.T) {
	stateDir := prepareProxyRuntimeTest(t)
	port := findFreeFiveDigitPort(t)
	if err := storage.SaveProxyRuntimePort(port); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", loopbackAddress(port))
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_antigravity-wf/health" {
			w.Header().Set("X-Antigravity-WF", "go-proxy")
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	waitForManagedListener(t, port)

	if err := Start(stateDir); err != nil {
		t.Fatal(err)
	}
	if OwnsListener() {
		t.Fatal("a reused helper listener must not be claimed as this process's server")
	}
	if got := CurrentPort(); got != port || !IsManagedListener() {
		t.Fatalf("managed listener was not reused: port=%d managed=%t", got, IsManagedListener())
	}
	pending, err := storage.HasStagedProxyRuntimePort()
	if err != nil || pending {
		t.Fatalf("managed committed listener must not create staged fallback: pending=%t err=%v", pending, err)
	}
	if err := Stop(); err != nil {
		t.Fatalf("stopping a reused managed listener must be a no-op: %v", err)
	}
}

func prepareProxyRuntimeTest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	storage.Init(dir)
	if err := storage.SaveProxyRuntimePort(findFreeFiveDigitPort(t)); err != nil {
		t.Fatalf("isolate test proxy endpoint: %v", err)
	}
	if err := Stop(); err != nil {
		t.Fatalf("stop previous test proxy: %v", err)
	}
	t.Cleanup(func() { _ = Stop() })
	return dir
}

func findFreeFiveDigitPort(t *testing.T) int {
	t.Helper()
	for port := 55000; port <= proxyendpoint.MaxPort; port++ {
		listener, err := net.Listen("tcp", loopbackAddress(port))
		if err != nil {
			continue
		}
		_ = listener.Close()
		return port
	}
	t.Fatal("no free five-digit loopback test port")
	return 0
}

func waitForManagedListener(t *testing.T, port int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if isManagedListenerAt(port) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("managed listener %d did not become healthy", port)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
