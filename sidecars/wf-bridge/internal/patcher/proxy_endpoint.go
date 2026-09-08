package patcher

import (
	"fmt"
	"sync"

	"antigravity-wf-assistant/internal/proxyendpoint"
	"antigravity-wf-assistant/internal/storage"
)

// Patch execution resolves this once from the non-secret runtime state before
// it touches an Antigravity file.  Keeping the endpoint state here prevents a
// fallback listener and a newly written patch from drifting apart.
var patchProxyEndpointState = struct {
	sync.RWMutex
	endpoint proxyendpoint.Endpoint
}{endpoint: proxyendpoint.MustForPort(proxyendpoint.DefaultPort)}

func refreshPatchProxyEndpoint() error {
	port, err := storage.LoadProxyRuntimePort()
	if err != nil {
		return fmt.Errorf("读取本地代理运行状态失败: %w", err)
	}
	endpoint, err := proxyendpoint.ForPort(port)
	if err != nil {
		return err
	}
	patchProxyEndpointState.Lock()
	patchProxyEndpointState.endpoint = endpoint
	patchProxyEndpointState.Unlock()
	return nil
}

func currentPatchProxyEndpoint() proxyendpoint.Endpoint {
	patchProxyEndpointState.RLock()
	defer patchProxyEndpointState.RUnlock()
	return patchProxyEndpointState.endpoint
}

// setPatchProxyPortForTest makes endpoint replacement tests deterministic and
// restores the previous process-local value afterward. It never reads or
// writes user configuration.
func setPatchProxyPortForTest(port int) func() {
	endpoint := proxyendpoint.MustForPort(port)
	patchProxyEndpointState.Lock()
	previous := patchProxyEndpointState.endpoint
	patchProxyEndpointState.endpoint = endpoint
	patchProxyEndpointState.Unlock()
	return func() {
		patchProxyEndpointState.Lock()
		patchProxyEndpointState.endpoint = previous
		patchProxyEndpointState.Unlock()
	}
}
