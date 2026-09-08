package main

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"antigravity-wf-assistant/internal/proxy"
	"antigravity-wf-assistant/internal/storage"
	"antigravity-wf-assistant/internal/updater"
)

func TestApplicationQuitIsOnlyInterceptedUntilNativeExitIsRequested(t *testing.T) {
	app := &App{}
	if !app.beforeClose(context.Background()) {
		t.Fatal("a system quit must be routed through the explicit native exit path")
	}
	app.exitRequested.Store(true)
	if app.beforeClose(context.Background()) {
		t.Fatal("an explicit exit must be allowed to close the application")
	}
}

func TestQuitAppBeforeStartupIsRejected(t *testing.T) {
	result := (&App{}).QuitApp()
	if result.OK {
		t.Fatal("QuitApp must not report success before Wails supplies its lifecycle context")
	}
}

func TestExitResourceCleanupClosesOAuthLoopbacks(t *testing.T) {
	callback, _, err := newOAuthLoopbackListener("http://127.0.0.1:0/callback")
	if err != nil {
		t.Fatalf("create OAuth loopback listener: %v", err)
	}
	address := callback.listener.Addr().String()
	app := &App{oauthLoopbacks: map[string]*oauthLoopbackListener{"pending": callback}}

	app.releaseExitResources()

	app.oauthMu.Lock()
	_, retained := app.oauthLoopbacks["pending"]
	app.oauthMu.Unlock()
	if retained {
		t.Fatal("exit cleanup retained a pending OAuth loopback listener")
	}
	connection, err := net.DialTimeout("tcp", address, 200*time.Millisecond)
	if err == nil {
		_ = connection.Close()
		t.Fatalf("OAuth loopback %s still accepts connections after exit cleanup", address)
	}
}

func TestExitResourceCleanupStopsOwnedProxyListener(t *testing.T) {
	_ = proxy.Stop()
	t.Cleanup(func() { _ = proxy.Stop() })

	stateDir := t.TempDir()
	storage.Init(stateDir)
	reserved, port := reserveFiveDigitLoopback(t)
	if err := reserved.Close(); err != nil {
		t.Fatal(err)
	}
	if err := storage.SaveProxyRuntimePort(port); err != nil {
		t.Fatal(err)
	}
	if err := proxy.Start(stateDir); err != nil {
		t.Fatalf("start owned proxy: %v", err)
	}
	if !proxy.OwnsListener() || !proxy.IsManagedListener() {
		t.Fatal("test proxy did not start as an owned managed listener")
	}

	(&App{}).releaseExitResources()

	if proxy.OwnsListener() || proxy.IsListening() {
		t.Fatal("explicit exit cleanup retained the owned proxy listener")
	}
}

func TestFreshUpdateCacheMessageDoesNotClaimGitHubFailed(t *testing.T) {
	message := cachedUpdateCheckMessage(updater.Info{
		Cached: true, CacheReason: "fresh", CheckedAt: "2026-08-04T12:00:00Z",
		Available: true, LatestVersion: "1.4.6",
	})
	if !strings.Contains(message, "使用最近成功检查结果") || !strings.Contains(message, "v1.4.6") {
		t.Fatalf("fresh cache message = %q", message)
	}
	if strings.Contains(message, "GitHub 暂时无法确认") {
		t.Fatalf("fresh cache message incorrectly reports a network failure: %q", message)
	}
}

func TestPatchStatusRepatchFlagTracksOnlyStagedPortTransitions(t *testing.T) {
	storage.Init(t.TempDir())
	if err := storage.SaveProxyRuntimePort(55001); err != nil {
		t.Fatal(err)
	}
	if currentProxyRepatchRequired() {
		t.Fatal("committed endpoint must not request a re-patch")
	}
	if err := storage.StageProxyRuntimePort(55002); err != nil {
		t.Fatal(err)
	}
	if !currentProxyRepatchRequired() {
		t.Fatal("staged endpoint must request an explicit re-patch")
	}
	if _, err := storage.CommitStagedProxyRuntimePort(); err != nil {
		t.Fatal(err)
	}
	if currentProxyRepatchRequired() {
		t.Fatal("committed successful endpoint must clear the re-patch flag")
	}
}

func TestOnlyIDEPatchIsBlockedDuringStagedPortTransition(t *testing.T) {
	storage.Init(t.TempDir())
	if err := storage.SaveProxyRuntimePort(55001); err != nil {
		t.Fatal(err)
	}
	if err := storage.StageProxyRuntimePort(55002); err != nil {
		t.Fatal(err)
	}
	result := (&App{}).ApplyIDEPatch()
	if result.OK || !strings.Contains(result.Message, "应用全部补丁") {
		t.Fatalf("only-IDE patch must be blocked during global rebind: %+v", result)
	}
}

func TestOnlyIDEPatchStopsIfStartingProxyCreatesFallbackStage(t *testing.T) {
	_ = proxy.Stop()
	t.Cleanup(func() { _ = proxy.Stop() })
	stateDir := t.TempDir()
	storage.Init(stateDir)
	occupied, primary := reserveFiveDigitLoopback(t)
	defer occupied.Close()
	if err := storage.SaveProxyRuntimePort(primary); err != nil {
		t.Fatal(err)
	}
	result := (&App{storageDir: stateDir}).ApplyIDEPatch()
	if result.OK || !strings.Contains(result.Message, "应用全部补丁") {
		t.Fatalf("only-IDE patch must stop after a newly staged fallback: %+v", result)
	}
	pending, err := storage.HasStagedProxyRuntimePort()
	if err != nil || !pending {
		t.Fatalf("new fallback was not preserved as a global rebind: pending=%t err=%v", pending, err)
	}
	if committed, err := storage.LoadCommittedProxyRuntimePort(); err != nil || committed != primary {
		t.Fatalf("only-IDE patch must not commit fallback: port=%d err=%v", committed, err)
	}
}

func TestLaunchProxyStartsAndPassesManagedHealthCheck(t *testing.T) {
	_ = proxy.Stop()
	t.Cleanup(func() { _ = proxy.Stop() })

	stateDir := t.TempDir()
	storage.Init(stateDir)
	reserved, port := reserveFiveDigitLoopback(t)
	if err := reserved.Close(); err != nil {
		t.Fatal(err)
	}
	if err := storage.SaveProxyRuntimePort(port); err != nil {
		t.Fatal(err)
	}

	if err := (&App{storageDir: stateDir}).ensureProxyReadyForAntigravityLaunch(); err != nil {
		t.Fatalf("launch preparation rejected a healthy helper: %v", err)
	}
	if !proxy.IsManagedListener() {
		t.Fatal("launch preparation must require a managed helper health response")
	}
}

func TestLaunchProxyBlocksWhenStartCreatesFallbackStage(t *testing.T) {
	_ = proxy.Stop()
	t.Cleanup(func() { _ = proxy.Stop() })

	stateDir := t.TempDir()
	storage.Init(stateDir)
	occupied, primary := reserveFiveDigitLoopback(t)
	defer occupied.Close()
	if err := storage.SaveProxyRuntimePort(primary); err != nil {
		t.Fatal(err)
	}

	err := (&App{storageDir: stateDir}).ensureProxyReadyForAntigravityLaunch()
	if err == nil || !strings.Contains(err.Error(), "应用全部补丁") {
		t.Fatalf("launch must stop while a fallback endpoint awaits a full re-patch: %v", err)
	}
	pending, stateErr := storage.HasStagedProxyRuntimePort()
	if stateErr != nil || !pending {
		t.Fatalf("fallback selected during launch must remain staged: pending=%t err=%v", pending, stateErr)
	}
	if committed, stateErr := storage.LoadCommittedProxyRuntimePort(); stateErr != nil || committed != primary {
		t.Fatalf("launch must not commit fallback before full patch: port=%d err=%v", committed, stateErr)
	}
}

func TestLaunchProxyBlocksExistingStagedTransition(t *testing.T) {
	_ = proxy.Stop()
	t.Cleanup(func() { _ = proxy.Stop() })

	storage.Init(t.TempDir())
	if err := storage.SaveProxyRuntimePort(55001); err != nil {
		t.Fatal(err)
	}
	if err := storage.StageProxyRuntimePort(55002); err != nil {
		t.Fatal(err)
	}

	err := (&App{}).ensureProxyReadyForAntigravityLaunch()
	if err == nil || !strings.Contains(err.Error(), "应用全部补丁") {
		t.Fatalf("launch must stop before starting an existing staged transition: %v", err)
	}
	if proxy.OwnsListener() {
		t.Fatal("launch preparation must not start an owned helper for an unresolved staged transition")
	}
}

func reserveFiveDigitLoopback(t *testing.T) (net.Listener, int) {
	t.Helper()
	for attempt := 0; attempt < 32; attempt++ {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := listener.Addr().(*net.TCPAddr).Port
		if port >= 10000 {
			return listener, port
		}
		_ = listener.Close()
	}
	t.Fatal("unable to reserve a five-digit test port")
	return nil, 0
}
