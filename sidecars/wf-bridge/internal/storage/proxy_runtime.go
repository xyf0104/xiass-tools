package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"antigravity-wf-assistant/internal/proxyendpoint"
)

// proxyRuntime is intentionally separate from settings.json and all account or
// model stores.  It contains no credential, provider, model, or chat data: it
// only records the loopback port that already-patched Antigravity resources
// must use after the assistant restarts.
type proxyRuntime struct {
	SchemaVersion int `json:"schemaVersion"`
	Port          int `json:"port"`
	// PendingPort is a bound fallback that may be used by the current explicit
	// Apply Patch operation. It is never treated as the next-start port until
	// that operation commits after its patch transaction succeeds.
	PendingPort int `json:"pendingPort,omitempty"`
}

const proxyRuntimeSchemaVersion = 1

var proxyRuntimeMu sync.RWMutex

func proxyRuntimePath() string {
	return filepath.Join(storageDir, "proxy_runtime.json")
}

// LoadProxyRuntimePort returns the endpoint currently selected for a patch
// transaction. A staged fallback takes precedence so the patcher and live
// proxy agree during ApplyPatch; proxy startup itself must instead call
// LoadCommittedProxyRuntimePort.
func LoadProxyRuntimePort() (int, error) {
	proxyRuntimeMu.RLock()
	defer proxyRuntimeMu.RUnlock()
	runtime, err := loadProxyRuntimeLocked()
	if err != nil {
		return proxyendpoint.DefaultPort, err
	}
	if runtime.PendingPort != 0 {
		return runtime.PendingPort, nil
	}
	return runtime.Port, nil
}

// LoadCommittedProxyRuntimePort returns the port an already-patched
// installation is guaranteed to target on the next assistant launch. Pending
// fallback state is intentionally ignored here to avoid silently disconnecting
// an installation that is still patched to the prior port.
func LoadCommittedProxyRuntimePort() (int, error) {
	proxyRuntimeMu.RLock()
	defer proxyRuntimeMu.RUnlock()
	runtime, err := loadProxyRuntimeLocked()
	if err != nil {
		return proxyendpoint.DefaultPort, err
	}
	return runtime.Port, nil
}

func loadProxyRuntimeLocked() (proxyRuntime, error) {
	if strings.TrimSpace(storageDir) == "" {
		return proxyRuntime{SchemaVersion: proxyRuntimeSchemaVersion, Port: proxyendpoint.DefaultPort}, nil
	}
	data, err := os.ReadFile(proxyRuntimePath())
	if os.IsNotExist(err) {
		return proxyRuntime{SchemaVersion: proxyRuntimeSchemaVersion, Port: proxyendpoint.DefaultPort}, nil
	}
	if err != nil {
		return proxyRuntime{}, err
	}
	var runtime proxyRuntime
	if err := json.Unmarshal(data, &runtime); err != nil {
		return proxyRuntime{}, fmt.Errorf("解析本地代理运行状态失败: %w", err)
	}
	if runtime.SchemaVersion != proxyRuntimeSchemaVersion {
		return proxyRuntime{}, fmt.Errorf("本地代理运行状态版本不受支持: %d", runtime.SchemaVersion)
	}
	if !proxyendpoint.IsSupportedPort(runtime.Port) {
		return proxyRuntime{}, fmt.Errorf("本地代理运行状态中的端口无效: %d", runtime.Port)
	}
	if runtime.PendingPort != 0 && !proxyendpoint.IsSupportedPort(runtime.PendingPort) {
		return proxyRuntime{}, fmt.Errorf("本地代理暂存端口无效: %d", runtime.PendingPort)
	}
	return runtime, nil
}

// SaveProxyRuntimePort atomically records a committed loopback port. It is
// retained for migrations and tests; normal fallback selection should use
// StageProxyRuntimePort followed by CommitStagedProxyRuntimePort.
func SaveProxyRuntimePort(port int) error {
	if !proxyendpoint.IsSupportedPort(port) {
		return fmt.Errorf("本地代理端口无效: %d", port)
	}
	proxyRuntimeMu.Lock()
	defer proxyRuntimeMu.Unlock()
	if strings.TrimSpace(storageDir) == "" {
		return fmt.Errorf("本地存储尚未初始化")
	}
	return writeProxyRuntimeLocked(proxyRuntime{SchemaVersion: proxyRuntimeSchemaVersion, Port: port})
}

// StageProxyRuntimePort records a listener that is already bound for the
// current process, without changing the next-start committed endpoint. This
// protects existing Antigravity installations if a prior port P is blocked and
// the current process temporarily falls back to Q before the user elects to
// re-apply its patch.
func StageProxyRuntimePort(port int) error {
	if !proxyendpoint.IsSupportedPort(port) {
		return fmt.Errorf("本地代理端口无效: %d", port)
	}
	proxyRuntimeMu.Lock()
	defer proxyRuntimeMu.Unlock()
	if strings.TrimSpace(storageDir) == "" {
		return fmt.Errorf("本地存储尚未初始化")
	}
	runtime, err := loadProxyRuntimeLocked()
	if err != nil {
		return err
	}
	runtime.PendingPort = port
	return writeProxyRuntimeLocked(runtime)
}

// CommitStagedProxyRuntimePort promotes a staged endpoint only after the
// patcher has completed successfully. It returns the committed port and is a
// no-op when no fallback rebind was staged.
func CommitStagedProxyRuntimePort() (int, error) {
	proxyRuntimeMu.Lock()
	defer proxyRuntimeMu.Unlock()
	if strings.TrimSpace(storageDir) == "" {
		return proxyendpoint.DefaultPort, fmt.Errorf("本地存储尚未初始化")
	}
	runtime, err := loadProxyRuntimeLocked()
	if err != nil {
		return proxyendpoint.DefaultPort, err
	}
	if runtime.PendingPort == 0 {
		return runtime.Port, nil
	}
	runtime.Port = runtime.PendingPort
	runtime.PendingPort = 0
	if err := writeProxyRuntimeLocked(runtime); err != nil {
		return proxyendpoint.DefaultPort, err
	}
	return runtime.Port, nil
}

// HasStagedProxyRuntimePort reports whether this run selected a fallback that
// still requires an explicit successful patch transaction before it can become
// the safe next-start endpoint.
func HasStagedProxyRuntimePort() (bool, error) {
	proxyRuntimeMu.RLock()
	defer proxyRuntimeMu.RUnlock()
	runtime, err := loadProxyRuntimeLocked()
	if err != nil {
		return false, err
	}
	return runtime.PendingPort != 0, nil
}

func writeProxyRuntimeLocked(runtime proxyRuntime) error {
	payload, err := json.MarshalIndent(runtime, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(storageDir, 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(storageDir, ".proxy-runtime-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(append(payload, '\n')); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return replaceStorageFile(tempPath, proxyRuntimePath())
}
