package storage

import (
	"testing"

	"antigravity-wf-assistant/internal/proxyendpoint"
)

func TestProxyRuntimeStagesFallbackUntilExplicitCommit(t *testing.T) {
	Init(t.TempDir())
	primary, fallback := 55001, 55002
	if err := SaveProxyRuntimePort(primary); err != nil {
		t.Fatal(err)
	}
	if err := StageProxyRuntimePort(fallback); err != nil {
		t.Fatal(err)
	}
	committed, err := LoadCommittedProxyRuntimePort()
	if err != nil || committed != primary {
		t.Fatalf("committed port = %d, err=%v; want %d", committed, err, primary)
	}
	effective, err := LoadProxyRuntimePort()
	if err != nil || effective != fallback {
		t.Fatalf("transaction endpoint = %d, err=%v; want %d", effective, err, fallback)
	}
	// A failed or interrupted patch leaves Q journaled. This prevents a later
	// restart from silently choosing P after some target files may have reached
	// Q; the explicit Apply flow must finish or repair the transaction.
	if committed, err = LoadCommittedProxyRuntimePort(); err != nil || committed != primary {
		t.Fatalf("staged fallback changed committed endpoint: %d, err=%v", committed, err)
	}
	if effective, err = LoadProxyRuntimePort(); err != nil || effective != fallback {
		t.Fatalf("interrupted patch lost staged endpoint: %d, err=%v", effective, err)
	}
	if committed, err = CommitStagedProxyRuntimePort(); err != nil || committed != fallback {
		t.Fatalf("successful patch did not commit fallback: %d, err=%v", committed, err)
	}
}

func TestProxyRuntimeRejectsBinaryUnsafePorts(t *testing.T) {
	Init(t.TempDir())
	for _, port := range []int{0, proxyendpoint.MinPort - 1, proxyendpoint.MaxPort + 1} {
		if err := SaveProxyRuntimePort(port); err == nil {
			t.Fatalf("SaveProxyRuntimePort accepted unsafe port %d", port)
		}
		if err := StageProxyRuntimePort(port); err == nil {
			t.Fatalf("StageProxyRuntimePort accepted unsafe port %d", port)
		}
	}
}
