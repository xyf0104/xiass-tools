package patcher

import (
	"testing"

	"antigravity-wf-assistant/internal/storage"
)

func TestPatchEndpointRefreshUsesStagedFallbackUntilCommit(t *testing.T) {
	original := currentPatchProxyEndpoint()
	t.Cleanup(func() {
		patchProxyEndpointState.Lock()
		patchProxyEndpointState.endpoint = original
		patchProxyEndpointState.Unlock()
	})
	storage.Init(t.TempDir())
	if err := storage.SaveProxyRuntimePort(55001); err != nil {
		t.Fatal(err)
	}
	if err := storage.StageProxyRuntimePort(55002); err != nil {
		t.Fatal(err)
	}
	if err := refreshPatchProxyEndpoint(); err != nil {
		t.Fatal(err)
	}
	if got := currentPatchProxyEndpoint().Port; got != 55002 {
		t.Fatalf("patcher endpoint = %d, want staged live port 55002", got)
	}
	if _, err := storage.CommitStagedProxyRuntimePort(); err != nil {
		t.Fatal(err)
	}
	if err := refreshPatchProxyEndpoint(); err != nil {
		t.Fatal(err)
	}
	if got := currentPatchProxyEndpoint().Port; got != 55002 {
		t.Fatalf("patcher endpoint = %d after commit, want 55002", got)
	}
}
