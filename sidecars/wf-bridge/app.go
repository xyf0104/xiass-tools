package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"antigravity-wf-assistant/internal/agent"
	"antigravity-wf-assistant/internal/agentdiscovery"
	"antigravity-wf-assistant/internal/codexdesktop"
	"antigravity-wf-assistant/internal/codexselection"
	"antigravity-wf-assistant/internal/diagnostics"
	"antigravity-wf-assistant/internal/launcher"
	"antigravity-wf-assistant/internal/oauthflow"
	"antigravity-wf-assistant/internal/patcher"
	"antigravity-wf-assistant/internal/permissions"
	"antigravity-wf-assistant/internal/proxy"
	"antigravity-wf-assistant/internal/stats"
	"antigravity-wf-assistant/internal/storage"
	"antigravity-wf-assistant/internal/totp"
	"antigravity-wf-assistant/internal/updater"
	"antigravity-wf-assistant/internal/upstream"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	embeddedUpdatesDisabledMessage = "嵌入 XIASS Tools 时，更新由主应用统一管理。"
	embeddedHostManagedVersion     = "由宿主 XIASS Tools 管理"
)

// reportedVersion is intentionally presentation-only. The bridge is shipped
// inside XIASS Tools, so its embedded mode must never advertise a separately
// updatable helper version.
func (a *App) reportedVersion() string {
	if a != nil && a.embeddedMode {
		return embeddedHostManagedVersion
	}
	return updater.CurrentVersion
}

// App holds all Wails-exposed methods.
type App struct {
	ctx                            context.Context
	embeddedMode                   bool
	storageDir                     string
	permissions                    *permissions.Manager
	totpVault                      *totp.Vault
	agentRegistry                  *agent.Registry
	antigravityAgent               *antigravityAgentAdapter
	agentRefreshMu                 sync.Mutex
	agentDesktopSelectionMu        sync.Mutex
	agentDesktopSelections         map[agent.ID]agentdiscovery.DesktopSelection
	agentDesktopSelectionOperation sync.Mutex
	historyMu                      sync.RWMutex
	historyRunMu                   sync.Mutex
	historyStatus                  HistorySyncStatus
	launchMu                       sync.Mutex
	patchMu                        sync.Mutex
	installStateMu                 sync.Mutex
	updateMu                       sync.Mutex
	updateCheckMu                  sync.Mutex
	updateCheckCancel              context.CancelFunc
	updateCheckGeneration          uint64
	accountTestMu                  sync.Mutex
	accountTestCancels             map[string]*activeAccountTest
	oauthMu                        sync.Mutex
	oauthSessions                  map[string]*pendingOAuthSession
	oauthResults                   map[string]oauthAuthorizationRecord
	oauthLoopbacks                 map[string]*oauthLoopbackListener
	cursorProjectMCPMu             sync.Mutex
	cursorProjectMCP               map[string]cursorProjectMCPSelection
	codexSelectionMu               sync.Mutex
	codexKeySelection              *codexselection.Service
	codexDesktopMu                 sync.Mutex
	codexDesktopControl            codexDesktopControlService
	codexDesktopOperation          sync.Mutex
	eventSink                      func(string, any)
	nativeActions                  nativeActionExecutor
	exitRequested                  atomic.Bool
}

// activeAccountTest is deliberately keyed by an opaque renderer-generated
// request ID rather than an account ID. A user may test two different models
// from the same account, while a cancellation must only stop the one modal
// request the user just closed.
type activeAccountTest struct {
	cancel context.CancelFunc
}

// pendingOAuthSession binds a short-lived PKCE session to the account draft
// entered in the renderer. Secrets never leave this Go-side structure.
type pendingOAuthSession struct {
	flow         *oauthflow.Flow
	account      storage.UpstreamAccount
	state        string
	expires      time.Time
	completionMu sync.Mutex
}

type OAuthAuthorizationResult struct {
	OK                       bool   `json:"ok"`
	Message                  string `json:"message"`
	SessionID                string `json:"sessionId,omitempty"`
	AuthorizationURL         string `json:"authorizationUrl,omitempty"`
	RedirectURI              string `json:"redirectUri,omitempty"`
	ProfileID                string `json:"profileId,omitempty"`
	AutomaticCallback        bool   `json:"automaticCallback"`
	ManualCompletionRequired bool   `json:"manualCompletionRequired"`
	ExpiresAt                string `json:"expiresAt,omitempty"`
}

type OAuthCompletionResult struct {
	OK        bool                    `json:"ok"`
	Message   string                  `json:"message"`
	AccountID string                  `json:"accountId,omitempty"`
	Identity  storage.AccountIdentity `json:"identity,omitempty"`
}

// UpstreamAccountView is the redacted account data sent to the renderer.
// LocalUsage comes solely from WF's proxy trace; it is not a provider billing
// estimate and is deliberately kept out of upstream_accounts.json.
type UpstreamAccountView struct {
	storage.UpstreamAccount
	LocalUsage        stats.AccountUsage `json:"localUsage"`
	HasPrivateHeaders bool               `json:"hasPrivateHeaders"`
}

func newApp() *App {
	home, _ := os.UserHomeDir()
	dir := resolveXIASSStorageDir(home)
	_ = os.MkdirAll(dir, 0o700)
	_ = os.Chmod(dir, 0o700)
	storage.Init(dir)
	if err := diagnostics.Init(dir); err != nil {
		log.Printf("[xiass-tools] 无法初始化本地诊断日志: %v", err)
	}
	application := &App{
		storageDir:             dir,
		permissions:            permissions.New(home, dir),
		accountTestCancels:     make(map[string]*activeAccountTest),
		oauthSessions:          make(map[string]*pendingOAuthSession),
		oauthResults:           make(map[string]oauthAuthorizationRecord),
		oauthLoopbacks:         make(map[string]*oauthLoopbackListener),
		cursorProjectMCP:       make(map[string]cursorProjectMCPSelection),
		agentDesktopSelections: make(map[agent.ID]agentdiscovery.DesktopSelection),
		codexKeySelection:      codexselection.New(),
		codexDesktopControl:    codexdesktop.NewController(),
		historyStatus: HistorySyncStatus{
			State:   "pending",
			Message: "等待启动时同步历史会话",
		},
	}
	if vault, vaultErr := totp.New(dir); vaultErr != nil {
		log.Printf("[xiass-tools] 无法初始化系统凭据库：%v", vaultErr)
	} else {
		application.totpVault = vault
	}
	application.initializeAgentRegistry()
	return application
}

// initializeAgentRegistry binds only adapters with real, local implementations.
// A binding failure is logged and leaves the corresponding public profile
// conservatively unbound rather than making startup fail or advertising an
// integration that cannot be used.
func (a *App) initializeAgentRegistry() {
	registry := agent.NewDefaultRegistry()
	antigravityAdapter := newAntigravityAgentAdapter()
	if err := registry.Bind(antigravityAdapter); err != nil {
		log.Printf("[xiass-tools] 无法绑定 Antigravity 集成: %v", err)
	} else {
		a.antigravityAgent = antigravityAdapter
	}
	if err := registry.Bind(newCodexAgentAdapter()); err != nil {
		log.Printf("[xiass-tools] 无法绑定 Codex 集成: %v", err)
	}
	if err := registry.Bind(newClaudeCodeAgentAdapter()); err != nil {
		log.Printf("[xiass-tools] 无法绑定 Claude Code 集成: %v", err)
	}
	if err := registry.Bind(newCursorMCPAgentAdapter()); err != nil {
		log.Printf("[xiass-tools] 无法绑定 Cursor MCP 集成: %v", err)
	}
	if err := registry.Bind(newWindsurfMCPAgentAdapter()); err != nil {
		log.Printf("[xiass-tools] 无法绑定 Windsurf MCP 集成: %v", err)
	}
	for _, adapter := range agentdiscovery.NewAdapters() {
		// Each discovery adapter below has a concrete, narrowly-scoped binding
		// above. Avoid overwriting its verified capability status with a generic
		// read-only duplicate detector.
		switch adapter.Metadata().ID {
		case agent.ClaudeCodeID, agent.CursorID, agent.WindsurfID:
			continue
		}
		if err := registry.Bind(adapter); err != nil {
			log.Printf("[xiass-tools] 无法绑定本机 Agent 检测器: %v", err)
		}
	}
	a.agentRegistry = registry
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.startTray()
	if err := proxy.Start(a.storageDir); err != nil {
		log.Printf("[xiass-tools] 代理启动失败: %v", err)
	}
	go a.syncHistory()
}

// emitRuntimeEvent keeps the existing Wails event bridge for the standalone
// application while allowing the XIASS Tools headless bridge to forward the
// same events over its authenticated loopback transport.
func (a *App) emitRuntimeEvent(name string, payload any) {
	if a.eventSink != nil {
		a.eventSink(name, payload)
		return
	}
	if a.embeddedMode {
		return
	}
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, name, payload)
	}
}

func (a *App) shutdown(ctx context.Context) {
	a.releaseExitResources()
	a.stopTray()
}

// beforeClose handles application-level quit requests such as Cmd+Q and the
// Dock's Quit command. Window close itself is handled by HideWindowOnClose,
// which sends the assistant to the menu bar instead. Routing a system quit
// through requestQuit makes it just as reliable as the status-item action.
func (a *App) beforeClose(ctx context.Context) (prevent bool) {
	if a.exitRequested.Load() {
		return false
	}
	a.requestQuit()
	return true
}

// QuitApp explicitly exits the assistant. OnShutdown stops the local proxy so
// 127.0.0.1:50999 is released for the next launch.
func (a *App) QuitApp() Result {
	if a.ctx == nil {
		return Result{OK: false, Message: "助手尚未完成启动，请稍后再试。"}
	}
	a.requestQuit()
	return Result{OK: true, Message: "正在退出助手并释放本地代理端口。"}
}

// requestQuit is shared by the menu-bar/tray menu and frontend. It stops the
// loopback proxy before ending the native event loop, so the port is released
// before the process disappears. The platform exit call deliberately bypasses
// the normal close-to-background handler.
func (a *App) requestQuit() {
	if a.ctx == nil || a.exitRequested.Swap(true) {
		return
	}
	a.releaseExitResources()
	a.stopTray()
	a.quitNativeApplication()
	go func() {
		timer := time.NewTimer(3 * time.Second)
		defer timer.Stop()
		<-timer.C
		if a.exitRequested.Load() {
			log.Printf("[xiass-tools] 正常退出超时，已在释放代理端口后结束进程")
			os.Exit(0)
		}
	}()
}

// releaseExitResources closes every locally owned listener before a native
// quit is requested. The watchdog in requestQuit deliberately has an
// os.Exit fallback for a stuck platform event loop, which bypasses Wails'
// OnShutdown hook; keeping this cleanup here therefore makes explicit quits
// safe on that fallback path as well.
func (a *App) releaseExitResources() {
	if a.nativeActions != nil {
		a.nativeActions.Close()
	}
	a.stopOAuthLoopbacks()
	a.clearCursorProjectMCPSelections()
	a.closeCodexKeySelections()
	_ = proxy.Stop()
}

// ─── Result types ─────────────────────────────────────────────────────────────

type Result struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// ExportDiagnosticLogs opens the native save dialog and creates a bounded,
// credential-safe ZIP for support. Account/model/OAuth stores are never added;
// diagnostics.Export also applies a second redaction pass to every text file.
func (a *App) ExportDiagnosticLogs() Result {
	if a.ctx == nil {
		return Result{OK: false, Message: "助手尚未完成启动，请稍后再试。"}
	}
	filename := "XIASS-Tools-Diagnostics-" + time.Now().Format("20060102-150405") + ".zip"
	destination, err := a.saveFileDialog(runtime.SaveDialogOptions{
		Title:           "导出诊断日志",
		DefaultFilename: filename,
		Filters: []runtime.FileFilter{{
			DisplayName: "ZIP 诊断包 (*.zip)",
			Pattern:     "*.zip",
		}},
	})
	if err != nil {
		return Result{OK: false, Message: "无法打开日志保存窗口: " + err.Error()}
	}
	if strings.TrimSpace(destination) == "" {
		return Result{OK: true, Message: "已取消导出诊断日志。"}
	}
	if !strings.EqualFold(filepath.Ext(destination), ".zip") {
		destination += ".zip"
	}
	home, _ := os.UserHomeDir()
	snapshot := map[string]any{
		"patchStatus": a.GetPatchStatus(),
		"historySync": a.GetHistorySyncStatus(),
	}
	if err := diagnostics.Export(destination, a.storageDir, diagnostics.Options{
		Version: a.reportedVersion(), Snapshot: snapshot, HomeDir: home,
	}); err != nil {
		return Result{OK: false, Message: "导出诊断日志失败: " + err.Error()}
	}
	log.Printf("[xiass-tools] 用户已导出脱敏诊断日志")
	return Result{OK: true, Message: "诊断日志已导出：" + destination}
}

type PatchStatus struct {
	AgentPatched             bool                `json:"agentPatched"`
	IDEPatched               bool                `json:"idePatched"`
	ProxyListening           bool                `json:"proxyListening"`
	ProxyManaged             bool                `json:"proxyManaged"`
	ProxyOwned               bool                `json:"proxyOwned"`
	ProxyRepatchRequired     bool                `json:"proxyRepatchRequired"`
	ProductRepatchRequired   bool                `json:"productRepatchRequired"`
	ProductRepatchMessage    string              `json:"productRepatchMessage"`
	LastRequestAt            string              `json:"lastRequestAt"`
	LastRequestPath          string              `json:"lastRequestPath"`
	LastModelFetchAt         string              `json:"lastModelFetchAt"`
	LastModelInjectionAt     string              `json:"lastModelInjectionAt"`
	LastInjectedModelCount   int                 `json:"lastInjectedModelCount"`
	LastInjectedModelNames   []string            `json:"lastInjectedModelNames"`
	LastInjectedModelSlugs   []string            `json:"lastInjectedModelSlugs"`
	LastModelShape           string              `json:"lastModelShape"`
	LastModelIndexes         string              `json:"lastModelIndexes"`
	LastModelStatusCode      int                 `json:"lastModelStatusCode"`
	LastModelEncoding        string              `json:"lastModelEncoding"`
	LastModelRequestCanceled bool                `json:"lastModelRequestCanceled"`
	LastError                string              `json:"lastError"`
	AsarPath                 string              `json:"asarPath"`
	LSPath                   string              `json:"lsPath"`
	IDEExtension             string              `json:"ideExtension"`
	IDELS                    string              `json:"ideLS"`
	Log                      string              `json:"log"`
	Targets                  []PatchTargetStatus `json:"targets"`
}

type PatchTargetStatus struct {
	Name               string `json:"name"`
	Kind               string `json:"kind"`
	Version            string `json:"version"`
	AppPath            string `json:"appPath"`
	ExecutablePath     string `json:"executablePath"`
	MainPath           string `json:"mainPath"`
	ASARPath           string `json:"asarPath"`
	ExtensionPath      string `json:"extensionPath"`
	LanguageServerPath string `json:"languageServerPath"`
	Supported          bool   `json:"supported"`
	ConnectionMode     string `json:"connectionMode"`
	Reason             string `json:"reason"`
	Patched            bool   `json:"patched"`
	Running            bool   `json:"running"`
	Launchable         bool   `json:"launchable"`
}

type StatsResult struct {
	TotalRequests    int     `json:"totalRequests"`
	CustomRequests   int     `json:"customRequests"`
	TotalTokens      int     `json:"totalTokens"`
	PromptTokens     int     `json:"promptTokens"`
	CompletionTokens int     `json:"completionTokens"`
	CacheReadTokens  int     `json:"cacheReadTokens"`
	CacheWriteTokens int     `json:"cacheWriteTokens"`
	CacheHitRate     float64 `json:"cacheHitRate"`
}

type AutoApprovalSettings struct {
	Enabled     bool     `json:"enabled"`
	Mode        string   `json:"mode"`
	CustomRules []string `json:"customRules"`
}

type HistorySyncStatus struct {
	State     string `json:"state"`
	Message   string `json:"message"`
	LastRunAt string `json:"lastRunAt"`
}

type BatchModelResult struct {
	OK        bool   `json:"ok"`
	Message   string `json:"message"`
	Added     int    `json:"added"`
	Bound     int    `json:"bound"`
	Unchanged int    `json:"unchanged"`
}

type UpdateCheckResult struct {
	OK      bool         `json:"ok"`
	Message string       `json:"message"`
	Info    updater.Info `json:"info"`
}

type UpdateProgress struct {
	Phase      string `json:"phase"`
	Downloaded int64  `json:"downloaded"`
	Total      int64  `json:"total"`
	Percent    int    `json:"percent"`
	Message    string `json:"message"`
}

type PatchProgress struct {
	Phase     string `json:"phase"`
	Operation string `json:"operation"`
	Percent   int    `json:"percent"`
	Message   string `json:"message"`
}

// ─── Proxy ────────────────────────────────────────────────────────────────────

func (a *App) StartProxy() Result {
	if err := proxy.Start(a.storageDir); err != nil {
		return Result{OK: false, Message: err.Error()}
	}
	return Result{OK: true, Message: "本地代理已启动"}
}

func (a *App) StopProxy() Result {
	if err := proxy.Stop(); err != nil {
		return Result{OK: false, Message: err.Error()}
	}
	return Result{OK: true, Message: "代理已停止"}
}

func (a *App) IsProxyListening() bool {
	return proxy.IsListening()
}

// GetAppSettings exposes only local, non-secret assistant preferences.
func (a *App) GetAppSettings() storage.AppSettings {
	settings, err := storage.LoadAppSettings()
	if err != nil {
		return storage.DefaultAppSettings()
	}
	return settings
}

// SaveAppSettings updates stream recovery immediately so a user never needs to
// restart the assistant just to change retry behaviour.
func (a *App) SaveAppSettings(settings storage.AppSettings) Result {
	settings = storage.NormalizeAppSettings(settings)
	if err := storage.SaveAppSettings(settings); err != nil {
		return Result{OK: false, Message: err.Error()}
	}
	proxy.ConfigureStreamRecovery(settings.StreamRecovery)
	return Result{OK: true, Message: "设置已保存并立即生效"}
}

func (a *App) CheckForUpdates() UpdateCheckResult {
	if a.embeddedMode {
		return UpdateCheckResult{
			Message: embeddedUpdatesDisabledMessage,
			Info:    updater.Info{CurrentVersion: embeddedHostManagedVersion},
		}
	}
	settings, err := storage.LoadAppSettings()
	if err != nil {
		settings = storage.DefaultAppSettings()
	}
	ctx, cancel, generation := a.beginUpdateCheck()
	defer a.finishUpdateCheck(generation, cancel)
	info, err := updater.CheckWithCache(ctx, settings.Updates.SkippedVersion, filepath.Join(a.storageDir, "update-release-cache.json"))
	if err != nil {
		return UpdateCheckResult{Message: updateCheckErrorMessage(err), Info: info}
	}
	if info.Cached {
		return UpdateCheckResult{OK: true, Message: cachedUpdateCheckMessage(info), Info: info}
	}
	if !info.Available {
		return UpdateCheckResult{OK: true, Message: "当前已是最新版本", Info: info}
	}
	if info.Skipped {
		return UpdateCheckResult{OK: true, Message: fmt.Sprintf("已跳过 v%s；可随时在设置中重新安装", info.LatestVersion), Info: info}
	}
	return UpdateCheckResult{OK: true, Message: fmt.Sprintf("发现新版本 v%s", info.LatestVersion), Info: info}
}

// CancelUpdateCheck interrupts the short-lived request immediately. The
// renderer also clears its local spinner without waiting for a stale Wails
// promise, so closing Settings or losing the network never leaves a forever
// loading state behind.
func (a *App) CancelUpdateCheck() Result {
	if a.embeddedMode {
		return Result{OK: false, Message: embeddedUpdatesDisabledMessage}
	}
	a.updateCheckMu.Lock()
	cancel := a.updateCheckCancel
	a.updateCheckMu.Unlock()
	if cancel == nil {
		return Result{OK: true, Message: "当前没有正在进行的更新检查"}
	}
	cancel()
	return Result{OK: true, Message: "已取消检查更新"}
}

func (a *App) beginUpdateCheck() (context.Context, context.CancelFunc, uint64) {
	parent := a.ctx
	if parent == nil {
		parent = context.Background()
	}
	a.updateCheckMu.Lock()
	if a.updateCheckCancel != nil {
		a.updateCheckCancel()
	}
	ctx, cancel := context.WithTimeout(parent, updater.CheckTimeout)
	a.updateCheckGeneration++
	generation := a.updateCheckGeneration
	a.updateCheckCancel = cancel
	a.updateCheckMu.Unlock()
	return ctx, cancel, generation
}

func (a *App) finishUpdateCheck(generation uint64, cancel context.CancelFunc) {
	cancel()
	a.updateCheckMu.Lock()
	if generation == a.updateCheckGeneration && a.updateCheckCancel != nil {
		a.updateCheckCancel = nil
	}
	a.updateCheckMu.Unlock()
}

func updateCheckErrorMessage(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "已取消检查更新"
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Sprintf("检查更新超时（最多 %d 秒）。请检查网络后重试。", int(updater.CheckTimeout/time.Second))
	default:
		return "检查更新失败：" + err.Error()
	}
}

func cachedUpdateCheckMessage(info updater.Info) string {
	checkedAt := strings.TrimSpace(info.CheckedAt)
	if checkedAt == "" {
		checkedAt = "上次成功检查"
	} else {
		checkedAt = "上次检查 " + checkedAt
	}
	if info.CacheReason == "fresh" {
		if info.Available {
			return fmt.Sprintf("使用最近成功检查结果（%s）：发现 v%s。", checkedAt, info.LatestVersion)
		}
		return fmt.Sprintf("使用最近成功检查结果（%s）。", checkedAt)
	}
	prefix := "GitHub 暂时无法确认新版本"
	if info.CacheReason == "timeout" {
		prefix = fmt.Sprintf("检查更新在 %d 秒后超时", int(updater.CheckTimeout/time.Second))
	}
	if info.Available {
		return fmt.Sprintf("%s，显示缓存结果（%s）：发现 v%s；安装前仍会重新验证。", prefix, checkedAt, info.LatestVersion)
	}
	return fmt.Sprintf("%s，显示缓存结果（%s）；这不代表一定是最新版本。", prefix, checkedAt)
}

func (a *App) SkipUpdateVersion(version string) Result {
	if a.embeddedMode {
		return Result{OK: false, Message: embeddedUpdatesDisabledMessage}
	}
	settings, err := storage.LoadAppSettings()
	if err != nil {
		settings = storage.DefaultAppSettings()
	}
	settings.Updates.SkippedVersion = strings.TrimSpace(version)
	if err := storage.SaveAppSettings(settings); err != nil {
		return Result{OK: false, Message: err.Error()}
	}
	return Result{OK: true, Message: "该版本已跳过"}
}

// InstallLatestUpdate downloads only the release installer selected by the
// updater package, validates its SHA256 manifest, then opens the native
// installer. Elevated installation remains a visible OS-owned action.
func (a *App) InstallLatestUpdate() Result {
	if a.embeddedMode {
		return Result{OK: false, Message: embeddedUpdatesDisabledMessage}
	}
	if a.ctx == nil {
		return Result{OK: false, Message: "助手尚未完成启动，请稍后再试。"}
	}
	a.updateMu.Lock()
	defer a.updateMu.Unlock()
	ctx, cancel := context.WithTimeout(a.ctx, 15*time.Minute)
	defer cancel()
	a.emitUpdateProgress(UpdateProgress{Phase: "checking", Message: "正在验证更新信息"})
	lastPercent := -1
	path, info, err := updater.DownloadLatestInstaller(ctx, func(downloaded, total int64) {
		percent := 0
		if total > 0 {
			percent = int(downloaded * 100 / total)
		}
		if percent == lastPercent && total > 0 {
			return
		}
		lastPercent = percent
		a.emitUpdateProgress(UpdateProgress{Phase: "downloading", Downloaded: downloaded, Total: total, Percent: percent, Message: "正在下载并校验安装包"})
	})
	if err != nil {
		a.emitUpdateProgress(UpdateProgress{Phase: "error", Message: err.Error()})
		return Result{OK: false, Message: err.Error()}
	}
	a.emitUpdateProgress(UpdateProgress{Phase: "verified", Downloaded: info.AssetSize, Total: info.AssetSize, Percent: 100, Message: "校验完成，正在启动安装程序"})
	if err := updater.LaunchInstaller(path); err != nil {
		a.emitUpdateProgress(UpdateProgress{Phase: "error", Message: err.Error()})
		return Result{OK: false, Message: "无法启动更新安装程序：" + err.Error()}
	}
	a.emitUpdateProgress(UpdateProgress{Phase: "launching", Percent: 100, Message: "安装程序已启动，助手将退出并释放端口"})
	// LaunchInstaller has successfully handed the verified PKG to the system.
	// Quit immediately once that handoff succeeds; a fixed delay can leave the
	// currently installed bundle running when Installer begins its replacement.
	go a.requestQuit()
	return Result{OK: true, Message: "安装程序已启动；完成系统安装后请重新打开 XIASS Tools。"}
}

func (a *App) emitUpdateProgress(progress UpdateProgress) {
	if a.ctx != nil {
		a.emitRuntimeEvent("wf:update-progress", progress)
	}
}

func (a *App) GetAutoApprovalStatus() permissions.Status {
	status, _ := a.permissions.Status()
	return status
}

func (a *App) SetAutoApproval(settings AutoApprovalSettings) Result {
	status, err := a.permissions.Apply(permissions.Settings{
		Enabled: settings.Enabled, Mode: settings.Mode, CustomRules: settings.CustomRules,
	})
	if err != nil {
		return Result{OK: false, Message: err.Error()}
	}
	if status.Enabled {
		return Result{OK: true, Message: fmt.Sprintf("命令自动批准已启用，共管理 %d 条授权规则。重新打开 Agent Window 后生效。", len(status.ManagedGrants))}
	}
	return Result{OK: true, Message: "命令自动批准已关闭，XIASS Tools 添加的授权已移除。重新打开 Agent Window 后生效。"}
}

func (a *App) GetHistorySyncStatus() HistorySyncStatus {
	a.historyMu.RLock()
	defer a.historyMu.RUnlock()
	return a.historyStatus
}

func (a *App) SyncHistoryNow() Result {
	return a.syncHistory()
}

func (a *App) syncHistory() Result {
	a.historyRunMu.Lock()
	defer a.historyRunMu.Unlock()
	a.setHistoryStatus("running", "正在恢复全部历史会话")
	message, err := patcher.Run("sync-history")
	if err != nil {
		message = fmt.Sprintf("历史会话自动恢复失败：%s", err.Error())
		log.Printf("[xiass-tools] %s", message)
		a.setHistoryStatus("error", message)
		return Result{OK: false, Message: message}
	}
	log.Printf("[xiass-tools] %s", message)
	a.setHistoryStatus("success", message)
	return Result{OK: true, Message: message}
}

func (a *App) setHistoryStatus(state, message string) {
	a.historyMu.Lock()
	defer a.historyMu.Unlock()
	a.historyStatus = HistorySyncStatus{
		State:     state,
		Message:   message,
		LastRunAt: time.Now().Format(time.RFC3339),
	}
}

// ─── Models ───────────────────────────────────────────────────────────────────

func (a *App) GetModels() []storage.CustomModel {
	models, _ := storage.LoadModels()
	return models
}

func (a *App) SaveModel(m storage.CustomModel) Result {
	config, err := a.modelValidationConfig(m)
	if err != nil {
		return Result{OK: false, Message: err.Error()}
	}
	if err := upstream.ValidateConfig(config); err != nil {
		return Result{OK: false, Message: err.Error()}
	}
	if err := storage.AddOrUpdateModel(m); err != nil {
		return Result{OK: false, Message: err.Error()}
	}
	return Result{OK: true, Message: "模型已保存"}
}

// DefaultUpstreamConfig provides a useful first-run configuration without
// embedding any credentials. The user still supplies their own API key.
func (a *App) DefaultUpstreamConfig() upstream.Config {
	return upstream.Config{
		Provider: "openai", APIURL: upstream.DefaultXIASSBaseURL, EndpointMode: "auto", APIStyle: "chat_completions", MessagePathMode: "auto", AuthMode: "bearer",
	}
}

// DiscoverUpstreamModels is a user-initiated, credential-safe GET /models.
// It never writes configuration or logs an API key.
func (a *App) DiscoverUpstreamModels(config upstream.Config) upstream.DiscoveryResult {
	resolved, err := a.resolveUpstreamConfig(config)
	if err != nil {
		return upstream.DiscoveryResult{Message: err.Error()}
	}
	ctx, cancel := a.upstreamContext(30 * time.Second)
	defer cancel()
	return upstream.DiscoverModels(ctx, resolved)
}

// TestUpstreamModel runs only after the user presses the test control. It is
// intentionally a tiny request and exposes no upstream response body.
func (a *App) TestUpstreamModel(config upstream.Config, model string) upstream.TestResult {
	resolved, err := a.resolveUpstreamConfig(config)
	if err != nil {
		return upstream.TestResult{Message: err.Error()}
	}
	ctx, cancel := a.upstreamContext(45 * time.Second)
	defer cancel()
	return upstream.TestModel(ctx, resolved, model)
}

// TestUpstreamModelDetailed is the replayable XIASS-style account test used
// by the account-card modal. The returned log is already credential-safe: the
// renderer never receives authorization values, headers, raw request bodies,
// or complete upstream response bodies.
func (a *App) TestUpstreamModelDetailed(config upstream.Config, request upstream.AccountTestRequest) upstream.AccountTestResult {
	request.RequestID = strings.TrimSpace(request.RequestID)
	resolved, err := a.resolveUpstreamConfig(config)
	if err != nil {
		return upstream.AccountTestResult{
			AccountID: request.AccountID,
			RequestID: request.RequestID,
			Message:   err.Error(),
			Steps:     []upstream.AccountTestStep{{Type: "error", Tone: "error", Text: err.Error()}},
		}
	}
	ctx, release, err := a.beginAccountTest(request.RequestID, 60*time.Second)
	if err != nil {
		return failedAccountTestRequest(request, err)
	}
	defer release()
	return upstream.RunAccountTest(ctx, resolved, request)
}

// AddDiscoveredModels saves a selected batch from a previously discovered
// model list. The default UI selects all discovered models; callers may pass a
// narrowed list to respect manual selection.
func (a *App) AddDiscoveredModels(config upstream.Config, modelIDs []string) BatchModelResult {
	upstreamName := strings.TrimSpace(config.UpstreamName)
	boundAccountIDs := configuredAccountIDs(config)
	if config.AccountID == "" && len(boundAccountIDs) > 0 {
		config.AccountID = boundAccountIDs[0]
	}
	resolved, err := a.resolveUpstreamConfig(config)
	if err != nil {
		return BatchModelResult{Message: err.Error()}
	}
	resolved.UpstreamName = upstreamName
	if err := upstream.ValidateConfig(resolved); err != nil {
		return BatchModelResult{Message: err.Error()}
	}
	return a.saveDiscoveredModels(resolved, boundAccountIDs, modelIDs, nil)
}

// saveDiscoveredModels writes a discovery result without ever copying an
// account credential into a model configuration. Account-backed imports use a
// storage-layer merge so one account can be added to an existing pool without
// replacing its peers or creating a duplicate Antigravity model.
func (a *App) saveDiscoveredModels(resolved upstream.Config, boundAccountIDs, modelIDs []string, accountSnapshot *storage.AccountSyncSnapshot) BatchModelResult {
	provider := upstream.NormalizedProvider(resolved.Provider)
	seen := make(map[string]struct{}, len(modelIDs))
	candidates := make([]storage.CustomModel, 0, len(modelIDs))
	for _, rawID := range modelIDs {
		modelID := strings.TrimSpace(strings.TrimPrefix(rawID, "models/"))
		if modelID == "" {
			continue
		}
		if _, exists := seen[modelID]; exists {
			continue
		}
		seen[modelID] = struct{}{}
		model := storage.NewDiscoveredModel(provider, resolved.APIURL, resolved.APIKey, modelID)
		model.UpstreamName = resolved.UpstreamName
		model.APIStyle = upstream.EffectiveAPIStyle(resolved)
		model.EndpointMode = upstream.NormalizedEndpointMode(resolved.EndpointMode)
		model.MessagePathMode = resolved.MessagePathMode
		model.AuthMode = strings.ToLower(strings.TrimSpace(resolved.AuthMode))
		model.AuthHeader = strings.TrimSpace(resolved.AuthHeader)
		model.Headers = cloneHeaders(resolved.Headers)
		if len(boundAccountIDs) > 0 {
			model.AccountIDs = boundAccountIDs
			// The credential belongs to the account pool. Do not duplicate it in
			// every discovered model on disk.
			model.APIKey = ""
		}
		model.Capabilities = storage.DefaultCapabilitiesForAPIStyle(provider, modelID, model.APIStyle)
		model.Capabilities.Configured = true
		candidates = append(candidates, model)
	}
	if len(candidates) == 0 {
		return BatchModelResult{Message: "请至少选择一个上游模型"}
	}

	if len(boundAccountIDs) > 0 {
		var (
			merged storage.DiscoveredAccountModelMergeResult
			err    error
		)
		if accountSnapshot != nil {
			merged, err = storage.MergeDiscoveredAccountModelsForCurrentAccount(*accountSnapshot, candidates)
		} else {
			merged, err = storage.MergeDiscoveredAccountModels(candidates)
		}
		if err != nil {
			if errors.Is(err, storage.ErrAccountSyncChanged) {
				return BatchModelResult{Message: err.Error()}
			}
			return BatchModelResult{Message: "保存模型失败：" + err.Error()}
		}
		return BatchModelResult{
			OK: true, Added: merged.Added, Bound: merged.Bound, Unchanged: merged.Unchanged,
			Message: accountModelSyncMessage(merged.Added, merged.Bound, merged.Unchanged),
		}
	}

	added := 0
	for _, model := range candidates {
		if err := storage.AddOrUpdateModel(model); err != nil {
			return BatchModelResult{Message: fmt.Sprintf("保存 %s 失败：%s", model.ExternalModelName, err.Error()), Added: added}
		}
		added++
	}
	return BatchModelResult{OK: true, Added: added, Message: fmt.Sprintf("已添加或更新 %d 个模型；重启 Antigravity 后生效。", added)}
}

func accountModelSyncMessage(added, bound, unchanged int) string {
	parts := make([]string, 0, 3)
	if added > 0 {
		parts = append(parts, fmt.Sprintf("新增 %d 个模型", added))
	}
	if bound > 0 {
		parts = append(parts, fmt.Sprintf("已将 %d 个已有模型加入账户池", bound))
	}
	if unchanged > 0 {
		parts = append(parts, fmt.Sprintf("%d 个模型已绑定", unchanged))
	}
	if len(parts) == 0 {
		return "没有可同步的模型"
	}
	return "已同步到 Antigravity：" + strings.Join(parts, "；") + "。"
}

// ─── Upstream account pool ───────────────────────────────────────────────────

func (a *App) GetUpstreamAccounts() []UpstreamAccountView {
	accounts, _ := storage.LoadUpstreamAccounts()
	localUsage := stats.ComputeAccountUsage(a.storageDir)
	// The renderer only needs account metadata and health. Keep reusable
	// credentials inside the private Go storage layer; an edit with an empty
	// credential field is merged with the existing secret below.
	views := make([]UpstreamAccountView, 0, len(accounts))
	for _, account := range accounts {
		hasPrivateHeaders := len(account.Headers) != 0
		account.APIKey = ""
		account.Credentials = nil
		account.Headers = nil
		views = append(views, UpstreamAccountView{
			UpstreamAccount:   account,
			LocalUsage:        localUsage[account.ID],
			HasPrivateHeaders: hasPrivateHeaders,
		})
	}
	return views
}

func (a *App) DefaultUpstreamAccount() storage.UpstreamAccount {
	return storage.UpstreamAccount{
		Name: "", Provider: "openai", Type: "api_key", APIURL: upstream.DefaultXIASSBaseURL,
		EndpointMode: "auto", APIStyle: "chat_completions", MessagePathMode: "auto", AuthMode: "bearer", Enabled: true,
		Priority: 50, MaxConcurrency: 2,
		OAuth: storage.OAuthConfiguration{RedirectURI: "http://localhost:1455/auth/callback", Scopes: "openid profile email offline_access"},
	}
}

func (a *App) SaveUpstreamAccount(account storage.UpstreamAccount) Result {
	if strings.EqualFold(strings.TrimSpace(account.Type), "refresh_token") {
		return Result{OK: false, Message: "Refresh Token / Mobile RT 必须通过“兑换并保存 OAuth 账户”导入，不能作为 API Key 直接保存。"}
	}
	if err := storage.ValidateAdditionalHeaders(account.Headers); err != nil {
		return Result{OK: false, Message: err.Error()}
	}
	if strings.TrimSpace(account.ID) != "" {
		existing, err := storage.GetUpstreamAccount(account.ID)
		if err == nil {
			if strings.TrimSpace(account.EffectiveAPIKey()) == "" {
				// Leaving the secret blank in an edit means "keep existing". This lets
				// the frontend avoid ever receiving stored API keys or OAuth JSON.
				account.APIKey = existing.APIKey
				account.Credentials = existing.Credentials
			}
			// RefreshScopes is provider profile metadata that older renderer forms do
			// not expose as a field. Preserve it on a normal credential-safe edit so
			// the next automatic token refresh retains the provider-required scope.
			if strings.TrimSpace(account.OAuth.RefreshScopes) == "" {
				account.OAuth.RefreshScopes = existing.OAuth.RefreshScopes
			}
		} else if strings.TrimSpace(account.EffectiveAPIKey()) == "" {
			return Result{OK: false, Message: err.Error()}
		}
	}
	if err := upstream.ValidateConfig(upstream.ConfigFromAccount(account)); err != nil {
		return Result{OK: false, Message: err.Error()}
	}
	if err := storage.SaveUpstreamAccount(account); err != nil {
		return Result{OK: false, Message: err.Error()}
	}
	return Result{OK: true, Message: "账户已保存到本地账户池"}
}

func (a *App) DeleteUpstreamAccount(id string) Result {
	if err := storage.DeleteUpstreamAccount(id); err != nil {
		return Result{OK: false, Message: err.Error()}
	}
	return Result{OK: true, Message: "账户已删除"}
}

func (a *App) SetUpstreamAccountEnabled(id string, enabled bool) Result {
	if err := storage.SetUpstreamAccountEnabled(id, enabled); err != nil {
		return Result{OK: false, Message: err.Error()}
	}
	if enabled {
		return Result{OK: true, Message: "账户已恢复调度"}
	}
	return Result{OK: true, Message: "账户已暂停，不会再被新请求选中"}
}

func (a *App) ImportUpstreamAccounts(raw string) storage.AccountImportResult {
	return storage.ImportUpstreamAccounts(raw)
}

func (a *App) DiscoverAccountModels(accountID string) upstream.DiscoveryResult {
	account, err := storage.GetUpstreamAccount(accountID)
	if err != nil {
		return upstream.DiscoveryResult{Message: err.Error()}
	}
	// This is an explicit account-card action, not a scheduler acquisition. A
	// paused or cooling-down account remains discoverable/testable so it can be
	// repaired before being re-enabled in the pool.
	ctx, cancel := a.upstreamContext(30 * time.Second)
	defer cancel()
	account, err = refreshExplicitUpstreamAccount(ctx, account)
	if err != nil {
		return upstream.DiscoveryResult{Message: err.Error()}
	}
	return upstream.DiscoverModels(ctx, upstream.ConfigFromAccount(account))
}

// SyncUpstreamAccountModels is the account-card one-click path: discover every
// model available to this saved account, then atomically make those models
// available to Antigravity. Explicit account actions bypass scheduler status,
// so a paused account can be repaired and synced before it is enabled again.
func (a *App) SyncUpstreamAccountModels(accountID string) BatchModelResult {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return BatchModelResult{Message: "请选择需要同步的账户"}
	}
	account, err := storage.GetUpstreamAccount(accountID)
	if err != nil {
		return BatchModelResult{Message: err.Error()}
	}
	ctx, cancel := a.upstreamContext(30 * time.Second)
	defer cancel()
	account, err = refreshExplicitUpstreamAccount(ctx, account)
	if err != nil {
		return BatchModelResult{Message: err.Error()}
	}
	// Capture only connection-routing metadata after a possible OAuth refresh.
	// The storage merge revalidates this snapshot under its account lock, so a
	// deleted or reconfigured account can never receive stale models.
	snapshot := storage.NewAccountSyncSnapshot(account)
	config := upstream.ConfigFromAccount(account)
	if err := upstream.ValidateConfig(config); err != nil {
		return BatchModelResult{Message: err.Error()}
	}
	discovery := upstream.DiscoverModels(ctx, config)
	if !discovery.OK {
		return BatchModelResult{Message: "无法获取该账户的模型列表：" + strings.TrimSpace(discovery.Message)}
	}
	modelIDs := make([]string, 0, len(discovery.Models))
	for _, model := range discovery.Models {
		if modelID := strings.TrimSpace(model.ID); modelID != "" {
			modelIDs = append(modelIDs, modelID)
		}
	}
	result := a.saveDiscoveredModels(config, []string{account.ID}, modelIDs, &snapshot)
	if result.OK && result.Message == "" {
		result.Message = accountModelSyncMessage(result.Added, result.Bound, result.Unchanged)
	}
	return result
}

func (a *App) TestUpstreamAccount(accountID, model string) upstream.TestResult {
	account, err := storage.GetUpstreamAccount(accountID)
	if err != nil {
		return upstream.TestResult{Message: err.Error()}
	}
	ctx, cancel := a.upstreamContext(45 * time.Second)
	defer cancel()
	account, err = refreshExplicitUpstreamAccount(ctx, account)
	if err != nil {
		return upstream.TestResult{Message: err.Error()}
	}
	resolved, err := a.resolveUpstreamConfig(upstream.ConfigFromAccount(account))
	if err != nil {
		return upstream.TestResult{Message: err.Error()}
	}
	return upstream.TestModel(ctx, resolved, model)
}

// TestUpstreamAccountDetailed resolves the reusable account on the Go side
// before testing it. Keeping accountId, model, prompt, and mode in one
// request matches the XIASS account-card interaction while preserving the
// legacy two-argument TestUpstreamAccount API for existing views.
func (a *App) TestUpstreamAccountDetailed(request upstream.AccountTestRequest) upstream.AccountTestResult {
	request.RequestID = strings.TrimSpace(request.RequestID)
	accountID := strings.TrimSpace(request.AccountID)
	if accountID == "" {
		return upstream.AccountTestResult{
			RequestID: request.RequestID,
			Message:   "请选择需要测试的账户",
			Steps:     []upstream.AccountTestStep{{Type: "error", Tone: "error", Text: "请选择需要测试的账户"}},
		}
	}
	account, err := storage.GetUpstreamAccount(accountID)
	if err != nil {
		return upstream.AccountTestResult{
			AccountID: accountID,
			RequestID: request.RequestID,
			Message:   err.Error(),
			Steps:     []upstream.AccountTestStep{{Type: "error", Tone: "error", Text: err.Error()}},
		}
	}
	request.AccountID = account.ID
	// Manual account tests intentionally bypass pool eligibility. An account
	// that is paused for scheduling, cooling down, or being repaired still
	// needs an explicit XIASS-style health probe from its own card.
	ctx, release, err := a.beginAccountTest(request.RequestID, 60*time.Second)
	if err != nil {
		return failedAccountTestRequest(request, err)
	}
	defer release()
	account, err = refreshExplicitUpstreamAccount(ctx, account)
	if err != nil {
		return failedAccountTestRequest(request, err)
	}
	return upstream.RunAccountTest(ctx, upstream.ConfigFromAccount(account), request)
}

// CancelUpstreamAccountTest stops one explicit account-card probe. It never
// touches the proxy, an account's saved credentials, or another test for the
// same account. The renderer must create a fresh opaque requestId for every
// test attempt; duplicate active IDs are rejected by beginAccountTest.
func (a *App) CancelUpstreamAccountTest(requestID string) Result {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return Result{Message: "缺少测试请求标识，无法取消测试。"}
	}
	a.accountTestMu.Lock()
	active := a.accountTestCancels[requestID]
	a.accountTestMu.Unlock()
	if active == nil {
		return Result{Message: "没有正在进行的账户测试。"}
	}
	active.cancel()
	return Result{OK: true, Message: "已取消正在进行的账户测试。"}
}

// StartOAuthAuthorization creates a short-lived, provider-neutral PKCE
// session and opens its one-time authorization URL in the system browser. The
// account owner supplies a registered public OAuth client configuration; WF
// deliberately does not ship a third-party client ID or secret.
func (a *App) StartOAuthAuthorization(draft storage.UpstreamAccount) OAuthAuthorizationResult {
	if strings.TrimSpace(draft.ID) != "" {
		existing, err := storage.GetUpstreamAccount(draft.ID)
		if err != nil {
			return OAuthAuthorizationResult{Message: err.Error()}
		}
		// The account view is deliberately redacted. Retain existing secrets for
		// the eventual token rotation while accepting updated public settings.
		if strings.TrimSpace(draft.APIKey) == "" {
			draft.APIKey = existing.APIKey
		}
		if draft.Credentials == nil {
			draft.Credentials = existing.Credentials
		}
	}
	if strings.TrimSpace(draft.ID) == "" {
		accountID, err := newOAuthImportedAccountID()
		if err != nil {
			return OAuthAuthorizationResult{Message: "无法创建本地账户标识：" + err.Error()}
		}
		draft.ID = accountID
	}

	config := oauthflow.Config{
		AuthorizationURL: draft.OAuth.AuthorizationURL,
		TokenURL:         draft.OAuth.TokenURL,
		PublicClientID:   draft.OAuth.ClientID,
		RedirectURI:      draft.OAuth.RedirectURI,
		Scopes:           strings.Fields(draft.OAuth.Scopes),
		RefreshScopes:    strings.Fields(draft.OAuth.RefreshScopes),
	}
	flow, err := oauthflow.New(config)
	if err != nil {
		return OAuthAuthorizationResult{Message: oauthErrorMessage(err)}
	}
	// Validate the target API URL and custom-header shape without demanding an
	// access token before the authorization-code exchange has occurred.
	upstreamConfig := upstream.ConfigFromAccount(draft)
	upstreamConfig.APIKey = "oauth-pending"
	if err := upstream.ValidateConfig(upstreamConfig); err != nil {
		return OAuthAuthorizationResult{Message: err.Error()}
	}
	authorization, err := flow.Begin()
	if err != nil {
		return OAuthAuthorizationResult{Message: oauthErrorMessage(err)}
	}

	a.oauthMu.Lock()
	a.ensureOAuthMapsLocked()
	a.discardExpiredOAuthSessionsLocked(time.Now())
	a.oauthSessions[authorization.SessionID] = &pendingOAuthSession{
		flow: flow, account: draft, state: authorization.State, expires: authorization.ExpiresAt,
	}
	a.oauthMu.Unlock()
	openError := a.openExternalURL(authorization.URL)
	message := "已在浏览器打开授权页；授权后请粘贴完整回调 URL 或授权码。"
	if openError != nil {
		message = "无法自动打开授权页，请复制授权链接到浏览器后继续。"
	}
	return OAuthAuthorizationResult{
		OK:                       true,
		Message:                  message,
		SessionID:                authorization.SessionID,
		AuthorizationURL:         authorization.URL,
		RedirectURI:              draft.OAuth.RedirectURI,
		ManualCompletionRequired: true,
		ExpiresAt:                authorization.ExpiresAt.UTC().Format(time.RFC3339),
	}
}

// CompleteOAuthAuthorization exchanges a copied callback URL or code. A raw
// code is bound to the retained state from StartOAuthAuthorization, while a
// complete callback has its state verified by oauthflow before any token is
// accepted.
func (a *App) CompleteOAuthAuthorization(sessionID, callback string) OAuthCompletionResult {
	sessionID = strings.TrimSpace(sessionID)
	a.oauthMu.Lock()
	a.ensureOAuthMapsLocked()
	a.discardExpiredOAuthSessionsLocked(time.Now())
	if record, found := a.oauthResults[sessionID]; found && record.result.OK {
		a.oauthMu.Unlock()
		return record.result
	}
	session := a.oauthSessions[sessionID]
	a.oauthMu.Unlock()
	if session == nil {
		return OAuthCompletionResult{Message: "授权会话不存在或已过期，请重新生成登录链接。"}
	}
	// A manual fallback and a loopback redirect can arrive at nearly the same
	// time. Serialising them at the session (not HTTP listener) level lets both
	// paths safely converge on one persisted account and one completion result.
	session.completionMu.Lock()
	defer session.completionMu.Unlock()

	a.oauthMu.Lock()
	a.discardExpiredOAuthSessionsLocked(time.Now())
	if record, found := a.oauthResults[sessionID]; found && record.result.OK {
		a.oauthMu.Unlock()
		return record.result
	}
	if a.oauthSessions[sessionID] != session {
		a.oauthMu.Unlock()
		return OAuthCompletionResult{Message: "授权会话不存在或已过期，请重新生成登录链接。"}
	}
	a.oauthMu.Unlock()

	parsed, err := oauthflow.ExtractCallback(callback)
	if err != nil {
		return OAuthCompletionResult{Message: oauthErrorMessage(err)}
	}
	if strings.TrimSpace(parsed.State) == "" {
		parsed.State = session.state
	}
	ctx, cancel := a.upstreamContext(45 * time.Second)
	defer cancel()
	token, err := session.flow.ExchangeCode(ctx, sessionID, parsed.Code, parsed.State)
	if err != nil {
		return OAuthCompletionResult{Message: oauthErrorMessage(err)}
	}

	account := applyOAuthToken(session.account, token, "OAuth 授权响应")
	result := a.SaveUpstreamAccount(account)
	if !result.OK {
		return OAuthCompletionResult{Message: result.Message}
	}
	// Fetching the persisted account returns identity claims normalized by the
	// storage layer. The local ID was assigned before the OAuth session began.
	stored, err := storage.GetUpstreamAccount(account.ID)
	completion := OAuthCompletionResult{
		OK: true, Message: "OAuth 授权已完成，账户已加入本地账户池。", AccountID: account.ID,
	}
	if err != nil {
		completion.Message = result.Message
	} else {
		completion.Identity = stored.Identity
	}
	a.recordOAuthAuthorizationResult(sessionID, completion)
	a.oauthMu.Lock()
	delete(a.oauthSessions, sessionID)
	a.oauthMu.Unlock()
	return completion
}

// ImportOAuthRefreshToken exchanges a user-supplied refresh token with the
// configured public OAuth client, then stores only the exchanged access token
// and refresh-token credential as an OAuth account. The refresh token is never
// treated as an upstream API key or returned to the renderer.
func (a *App) ImportOAuthRefreshToken(draft storage.UpstreamAccount, refreshToken string) OAuthCompletionResult {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return OAuthCompletionResult{Message: "请填写 Refresh Token 或 Mobile RT。"}
	}
	if strings.TrimSpace(draft.ID) == "" {
		accountID, err := newOAuthImportedAccountID()
		if err != nil {
			return OAuthCompletionResult{Message: "无法创建本地账户标识：" + err.Error()}
		}
		draft.ID = accountID
	} else {
		existing, err := storage.GetUpstreamAccount(draft.ID)
		if err != nil {
			return OAuthCompletionResult{Message: err.Error()}
		}
		// Account views intentionally omit credentials. Retain unrelated existing
		// OAuth metadata while applyOAuthToken replaces the access/refresh pair.
		if draft.Credentials == nil {
			draft.Credentials = existing.Credentials
		}
		draft.Identity = existing.Identity
	}

	// Validate the target inference configuration without ever accepting the
	// refresh token as that configuration's API key.
	upstreamConfig := upstream.ConfigFromAccount(draft)
	upstreamConfig.APIKey = "oauth-pending"
	if err := upstream.ValidateConfig(upstreamConfig); err != nil {
		return OAuthCompletionResult{Message: err.Error()}
	}
	flow, err := oauthflow.New(oauthflow.Config{
		AuthorizationURL: draft.OAuth.AuthorizationURL,
		TokenURL:         draft.OAuth.TokenURL,
		PublicClientID:   draft.OAuth.ClientID,
		RedirectURI:      draft.OAuth.RedirectURI,
		Scopes:           strings.Fields(draft.OAuth.Scopes),
		RefreshScopes:    strings.Fields(draft.OAuth.RefreshScopes),
	})
	if err != nil {
		return OAuthCompletionResult{Message: oauthErrorMessage(err)}
	}
	ctx, cancel := a.upstreamContext(45 * time.Second)
	defer cancel()
	token, err := flow.Refresh(ctx, refreshToken)
	if err != nil {
		return OAuthCompletionResult{Message: oauthErrorMessage(err)}
	}
	account := applyOAuthToken(draft, token, "Refresh Token / Mobile RT 导入")
	if err := storage.SaveUpstreamAccount(account); err != nil {
		return OAuthCompletionResult{Message: err.Error()}
	}
	stored, err := storage.GetUpstreamAccount(account.ID)
	if err != nil {
		return OAuthCompletionResult{OK: true, Message: "刷新令牌已兑换并保存为 OAuth 账户。", AccountID: account.ID}
	}
	return OAuthCompletionResult{
		OK: true, Message: "刷新令牌已兑换并保存为 OAuth 账户。",
		AccountID: stored.ID, Identity: stored.Identity,
	}
}

func newOAuthImportedAccountID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return "acct-" + hex.EncodeToString(bytes), nil
}

// RefreshUpstreamOAuthAccount rotates an access token only after the user
// explicitly requests it. Automatic scheduling refreshes are handled in the
// storage layer when a bound account is near its access-token expiry.
func (a *App) RefreshUpstreamOAuthAccount(accountID string) Result {
	account, err := storage.GetUpstreamAccount(accountID)
	if err != nil {
		return Result{Message: err.Error()}
	}
	refreshToken := credentialText(account.Credentials, "refresh_token", "refreshToken", "mobile_rt", "mobileRT")
	if refreshToken == "" {
		return Result{Message: "该账户没有可用的刷新令牌，请重新授权或导入完整 OAuth JSON。"}
	}
	flow, err := oauthflow.New(oauthflow.Config{
		AuthorizationURL: account.OAuth.AuthorizationURL,
		TokenURL:         account.OAuth.TokenURL,
		PublicClientID:   account.OAuth.ClientID,
		RedirectURI:      account.OAuth.RedirectURI,
		Scopes:           strings.Fields(account.OAuth.Scopes),
		RefreshScopes:    strings.Fields(account.OAuth.RefreshScopes),
	})
	if err != nil {
		return Result{Message: oauthErrorMessage(err)}
	}
	ctx, cancel := a.upstreamContext(45 * time.Second)
	defer cancel()
	token, err := flow.Refresh(ctx, refreshToken)
	if err != nil {
		return Result{Message: oauthErrorMessage(err)}
	}
	account = applyOAuthToken(account, token, "OAuth 刷新响应")
	if err := storage.SaveUpstreamAccount(account); err != nil {
		return Result{Message: err.Error()}
	}
	return Result{OK: true, Message: "访问令牌已刷新"}
}

// RefreshUpstreamAccountQuota calls an optional, provider-documented quota
// endpoint configured by the account owner. It never runs as part of loading
// the account page, model discovery, or a normal inference request.
func (a *App) RefreshUpstreamAccountQuota(accountID string) upstream.QuotaResult {
	account, err := storage.GetUpstreamAccount(accountID)
	if err != nil {
		return upstream.QuotaResult{Message: err.Error()}
	}
	ctx, cancel := a.upstreamContext(30 * time.Second)
	defer cancel()
	account, err = refreshExplicitUpstreamAccount(ctx, account)
	if err != nil {
		return upstream.QuotaResult{Message: err.Error()}
	}
	result := upstream.FetchQuota(ctx, upstream.ConfigFromAccount(account), account.QuotaURL)
	if result.OK {
		if err := storage.SaveQuotaSnapshot(account.ID, result.Snapshot); err != nil {
			result.OK = false
			result.Message = err.Error()
		}
	}
	return result
}

// refreshExplicitUpstreamAccount keeps user-triggered account actions on the
// same OAuth lifecycle as proxy scheduling. The caller owns ctx and its
// timeout; after a possible token rotation the account is loaded again so no
// upstream request can retain the stale access token captured before refresh.
func refreshExplicitUpstreamAccount(ctx context.Context, account storage.UpstreamAccount) (storage.UpstreamAccount, error) {
	if err := storage.EnsureAccountAccessToken(ctx, account.ID); err != nil {
		return storage.UpstreamAccount{}, fmt.Errorf("OAuth 访问令牌刷新失败：%w", err)
	}
	return storage.GetUpstreamAccount(account.ID)
}

func (a *App) discardExpiredOAuthSessionsLocked(now time.Time) {
	for sessionID, session := range a.oauthSessions {
		if session == nil || !now.Before(session.expires) {
			delete(a.oauthSessions, sessionID)
			if callback := a.oauthLoopbacks[sessionID]; callback != nil {
				callback.Close()
				delete(a.oauthLoopbacks, sessionID)
			}
		}
	}
	for sessionID, record := range a.oauthResults {
		if !now.Before(record.expires) {
			delete(a.oauthResults, sessionID)
		}
	}
}

func applyOAuthToken(account storage.UpstreamAccount, token oauthflow.Token, source string) storage.UpstreamAccount {
	credentials := make(map[string]any, len(account.Credentials)+7)
	for key, value := range account.Credentials {
		credentials[key] = value
	}
	credentials["access_token"] = token.AccessToken
	if token.RefreshToken != "" {
		credentials["refresh_token"] = token.RefreshToken
	}
	if token.IDToken != "" {
		credentials["id_token"] = token.IDToken
	}
	if token.TokenType != "" {
		credentials["token_type"] = token.TokenType
	}
	if token.Scope != "" {
		credentials["scope"] = token.Scope
	}
	// Keep the public OAuth client beside the rotated tokens. This mirrors
	// XIASS exports and lets a later JSON re-import refresh with the exact
	// registered client rather than guessing one. It is not a client secret.
	if clientID := strings.TrimSpace(account.OAuth.ClientID); clientID != "" {
		credentials["client_id"] = clientID
	}
	account.Type = "oauth"
	account.APIKey = token.AccessToken
	account.Credentials = credentials
	if token.ExpiresAt.IsZero() {
		account.AuthExpiresAt = ""
	} else {
		account.AuthExpiresAt = token.ExpiresAt.UTC().Format(time.RFC3339)
	}
	if account.AuthMode == "" {
		account.AuthMode = "bearer"
	}
	account.Identity.Source = source
	account.Identity.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return account
}

func credentialText(credentials map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := credentials[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func oauthErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, oauthflow.ErrInvalidConfig):
		return "OAuth 配置无效：请检查授权地址、令牌地址、公开客户端 ID 和已注册的回调地址。"
	case errors.Is(err, oauthflow.ErrAuthorizationDenied):
		return "授权被取消或拒绝。"
	case errors.Is(err, oauthflow.ErrInvalidCallback):
		return "回调地址或授权码无效。"
	case errors.Is(err, oauthflow.ErrStateMismatch):
		return "回调状态不匹配，请重新生成登录链接。"
	case errors.Is(err, oauthflow.ErrSessionNotFound):
		return "授权会话不存在或已过期，请重新生成登录链接。"
	case errors.Is(err, oauthflow.ErrTokenExchange):
		return "令牌兑换失败，请检查 OAuth 配置、授权码和网络后重试。"
	default:
		return "OAuth 操作失败：" + err.Error()
	}
}

func (a *App) resolveUpstreamConfig(config upstream.Config) (upstream.Config, error) {
	accountIDs := configuredAccountIDs(config)
	if len(accountIDs) == 0 {
		return config, nil
	}
	configuredProvider := strings.TrimSpace(config.Provider)
	var primary storage.UpstreamAccount
	for index, accountID := range accountIDs {
		account, err := storage.GetUpstreamAccount(accountID)
		if err != nil {
			return upstream.Config{}, err
		}
		if !account.Enabled {
			return upstream.Config{}, fmt.Errorf("所选账户已暂停：%s", account.Name)
		}
		if strings.TrimSpace(account.EffectiveAPIKey()) == "" {
			return upstream.Config{}, fmt.Errorf("所选账户没有可用凭据：%s", account.Name)
		}
		if configuredProvider != "" && upstream.NormalizedProvider(account.Provider) != upstream.NormalizedProvider(configuredProvider) {
			return upstream.Config{}, fmt.Errorf("所选账户必须与模型使用同一种上游协议")
		}
		if index == 0 {
			primary = account
			continue
		}
		if upstream.NormalizedProvider(account.Provider) != upstream.NormalizedProvider(primary.Provider) {
			return upstream.Config{}, fmt.Errorf("所选账户必须使用同一种上游协议")
		}
	}
	return upstream.ConfigFromAccount(primary), nil
}

func (a *App) modelValidationConfig(model storage.CustomModel) (upstream.Config, error) {
	accountIDs := normalizedAccountIDs(model.AccountIDs)
	if len(accountIDs) == 0 {
		return upstream.ConfigFromModel(model), nil
	}
	var primary storage.UpstreamAccount
	for index, accountID := range accountIDs {
		account, err := storage.GetUpstreamAccount(accountID)
		if err != nil {
			return upstream.Config{}, fmt.Errorf("模型绑定的账户不可用：%w", err)
		}
		if index == 0 {
			primary = account
			continue
		}
		if upstream.NormalizedProvider(account.Provider) != upstream.NormalizedProvider(primary.Provider) {
			return upstream.Config{}, fmt.Errorf("模型绑定的账户必须使用同一种上游协议")
		}
	}
	if strings.TrimSpace(model.Provider) != "" && upstream.NormalizedProvider(model.Provider) != upstream.NormalizedProvider(primary.Provider) {
		return upstream.Config{}, fmt.Errorf("模型与绑定账户必须使用同一种上游协议")
	}
	return upstream.ConfigFromAccount(primary), nil
}

func configuredAccountIDs(config upstream.Config) []string {
	values := append([]string{}, config.AccountIDs...)
	if accountID := strings.TrimSpace(config.AccountID); accountID != "" {
		values = append(values, accountID)
	}
	return normalizedAccountIDs(values)
}

func normalizedAccountIDs(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

const accountTestRequestIDMaxBytes = 128

// beginAccountTest owns the cancellation lifetime for an explicit account
// test. A duplicate active request ID is rejected instead of replacing the
// existing cancel function: this keeps a delayed cancel from one modal action
// from ever being applied to a newer test action with the same ID. Cleanup is
// pointer-guarded as an additional race safety net.
func (a *App) beginAccountTest(requestID string, timeout time.Duration) (context.Context, func(), error) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		ctx, cancel := a.upstreamContext(timeout)
		return ctx, cancel, nil
	}
	if len(requestID) > accountTestRequestIDMaxBytes || strings.ContainsAny(requestID, "\x00\r\n\t") {
		return nil, nil, errors.New("测试请求标识无效，请重新开始测试。")
	}

	ctx, cancel := a.upstreamContext(timeout)
	active := &activeAccountTest{cancel: cancel}
	a.accountTestMu.Lock()
	if a.accountTestCancels == nil {
		a.accountTestCancels = make(map[string]*activeAccountTest)
	}
	if _, exists := a.accountTestCancels[requestID]; exists {
		a.accountTestMu.Unlock()
		cancel()
		return nil, nil, errors.New("同一测试请求仍在进行中，请等待其结束后重试。")
	}
	a.accountTestCancels[requestID] = active
	a.accountTestMu.Unlock()

	return ctx, func() {
		cancel()
		a.accountTestMu.Lock()
		if a.accountTestCancels[requestID] == active {
			delete(a.accountTestCancels, requestID)
		}
		a.accountTestMu.Unlock()
	}, nil
}

func failedAccountTestRequest(request upstream.AccountTestRequest, err error) upstream.AccountTestResult {
	message := "账户测试无法开始"
	if err != nil && strings.TrimSpace(err.Error()) != "" {
		message = err.Error()
	}
	return upstream.AccountTestResult{
		AccountID: request.AccountID,
		RequestID: request.RequestID,
		Message:   message,
		Steps:     []upstream.AccountTestStep{{Type: "error", Tone: "error", Text: message}},
	}
}

func (a *App) upstreamContext(timeout time.Duration) (context.Context, context.CancelFunc) {
	parent := a.ctx
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, timeout)
}

func cloneHeaders(headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	result := make(map[string]string, len(headers))
	for name, value := range headers {
		result[name] = value
	}
	return result
}

func (a *App) DeleteModel(name string) Result {
	if err := storage.DeleteModel(name); err != nil {
		return Result{OK: false, Message: err.Error()}
	}
	return Result{OK: true, Message: "模型已删除"}
}

// ─── Patch ────────────────────────────────────────────────────────────────────

func (a *App) GetPatchStatus() PatchStatus {
	return a.patchStatusFrom(patcher.GetStatus())
}

// GetQuickPatchStatus keeps the first dashboard paint responsive. It uses
// only standard paths, saved successful paths and a metadata-valid cache;
// RefreshPatchStatus performs the full compatibility verification.
func (a *App) GetQuickPatchStatus() PatchStatus {
	return a.patchStatusFrom(patcher.GetQuickStatus())
}

// RefreshPatchStatus explicitly re-runs bundle discovery and compatibility
// verification when the user refreshes the dashboard.
func (a *App) RefreshPatchStatus() PatchStatus {
	return a.patchStatusFrom(patcher.RefreshStatus())
}

func (a *App) patchStatusFrom(s patcher.Status) PatchStatus {
	diagnostics := proxy.GetDiagnostics()
	agentPatched := s.AgentPatched != nil && *s.AgentPatched
	idePatched := s.IDEPatched != nil && *s.IDEPatched
	proxyRepatchRequired := currentProxyRepatchRequired()
	productRepatchRequired, productRepatchMessage := a.antigravityProductRepatchState(s)
	targets := make([]PatchTargetStatus, 0, len(s.Targets))
	for _, target := range s.Targets {
		running := false
		if launcher.Supported() {
			running, _ = launcher.IsRunning(target.AppPath)
		}
		targets = append(targets, PatchTargetStatus{
			Name: target.Name, Kind: target.Kind, Version: target.Version,
			AppPath: target.AppPath, MainPath: target.MainPath, ASARPath: target.ASARPath,
			ExecutablePath: target.ExecutablePath,
			ExtensionPath:  target.ExtensionPath, LanguageServerPath: target.LanguageServerPath,
			Supported: target.Supported, ConnectionMode: target.ConnectionMode, Reason: target.Reason,
			Patched: target.Patched, Running: running, Launchable: launcher.Supported(),
		})
	}
	return PatchStatus{
		AgentPatched:             agentPatched,
		IDEPatched:               idePatched,
		ProxyListening:           proxy.IsListening(),
		ProxyManaged:             proxy.IsManagedListener(),
		ProxyOwned:               proxy.OwnsListener(),
		ProxyRepatchRequired:     proxyRepatchRequired,
		ProductRepatchRequired:   productRepatchRequired,
		ProductRepatchMessage:    productRepatchMessage,
		LastRequestAt:            diagnostics.LastRequestAt,
		LastRequestPath:          diagnostics.LastRequestPath,
		LastModelFetchAt:         diagnostics.LastModelFetchAt,
		LastModelInjectionAt:     diagnostics.LastModelInjectionAt,
		LastInjectedModelCount:   diagnostics.LastInjectedModelCount,
		LastInjectedModelNames:   diagnostics.LastInjectedModelNames,
		LastInjectedModelSlugs:   diagnostics.LastInjectedModelSlugs,
		LastModelShape:           diagnostics.LastModelShape,
		LastModelIndexes:         diagnostics.LastModelIndexes,
		LastModelStatusCode:      diagnostics.LastModelStatusCode,
		LastModelEncoding:        diagnostics.LastModelContentEncoding,
		LastModelRequestCanceled: diagnostics.LastModelRequestCanceled,
		LastError:                diagnostics.LastError,
		AsarPath:                 s.AsarPath,
		LSPath:                   s.LSPath,
		IDEExtension:             s.IDEExtensionPath,
		IDELS:                    s.IDELSPath,
		Targets:                  targets,
	}
}

func currentProxyRepatchRequired() bool {
	pending, err := storage.HasStagedProxyRuntimePort()
	return err == nil && pending
}

// ensureProxyReadyForAntigravityLaunch starts (or verifies) the local helper
// before an Antigravity process is launched. A patched installation must
// never be opened while its endpoint is unavailable, nor while a staged port
// migration still needs every installation to be rewritten by ApplyPatch.
//
// The second staged-state check is intentional: Start can discover that the
// committed port is occupied and safely select a fallback port. In that case,
// launching now would leave existing patched targets pointing at the old
// endpoint.
func (a *App) ensureProxyReadyForAntigravityLaunch() error {
	if pending, err := storage.HasStagedProxyRuntimePort(); err != nil {
		return fmt.Errorf("无法确认本地代理端口切换状态；为避免 Antigravity 连接到错误代理，本次启动已停止: %w", err)
	} else if pending {
		return errors.New("本地代理已安全选择新的连接方式，但 Antigravity 仍可能指向旧连接。请先点击“应用全部补丁”，完成后再启动")
	}

	if err := proxy.Start(a.storageDir); err != nil {
		return fmt.Errorf("本地代理启动失败，未启动 Antigravity: %w", err)
	}
	if pending, err := storage.HasStagedProxyRuntimePort(); err != nil {
		return fmt.Errorf("无法确认本地代理端口切换状态；为避免 Antigravity 连接到错误代理，本次启动已停止: %w", err)
	} else if pending {
		return errors.New("本地代理连接正在安全调整中。请先点击“应用全部补丁”，完成后再启动 Antigravity")
	}
	if !proxy.IsManagedListener() {
		return errors.New("本地代理未通过健康检查，未启动 Antigravity。请稍后重试或重新打开 XIASS Tools")
	}
	return nil
}

// LaunchOrRestartAntigravity starts a detected installation, or performs a
// safe restart when it is already running. Restart never force-kills: it asks
// macOS to quit normally, waits for the process to exit, synchronises history,
// and only then launches the selected bundle again.
func (a *App) LaunchOrRestartAntigravity(appPath string) Result {
	a.launchMu.Lock()
	defer a.launchMu.Unlock()

	selected := ""
	selectedName := "Antigravity"
	for _, target := range patcher.GetQuickStatus().Targets {
		if filepath.Clean(target.AppPath) == filepath.Clean(appPath) {
			selected = target.AppPath
			selectedName = target.Name
			break
		}
	}
	if selected == "" {
		return Result{OK: false, Message: "只能启动当前自动检测到的 Antigravity 安装，请刷新状态后重试。"}
	}
	if !launcher.Supported() {
		return Result{OK: false, Message: "当前平台暂不支持安全启动与重启。"}
	}
	if err := a.ensureProxyReadyForAntigravityLaunch(); err != nil {
		return Result{OK: false, Message: err.Error()}
	}

	running, err := launcher.IsRunning(selected)
	if err != nil {
		return Result{OK: false, Message: err.Error()}
	}
	if history := a.syncHistory(); !history.OK {
		return Result{OK: false, Message: "为保护聊天历史，本次启动/重启已停止：" + history.Message}
	}

	verb := "启动"
	if running {
		verb = "重启"
		if err := launcher.QuitGracefully(selected, 30*time.Second); err != nil {
			return Result{OK: false, Message: err.Error()}
		}
		if history := a.syncHistory(); !history.OK {
			_ = launcher.Launch(selected)
			return Result{OK: false, Message: "应用已正常退出，但历史同步失败；已尝试重新打开应用：" + history.Message}
		}
	}

	if err := launcher.Launch(selected); err != nil {
		return Result{OK: false, Message: err.Error()}
	}
	if err := launcher.WaitUntilRunning(selected, 15*time.Second); err != nil {
		return Result{OK: false, Message: err.Error()}
	}
	return Result{OK: true, Message: fmt.Sprintf("%s已%s；聊天历史已在操作前后安全同步。", selectedName, verb)}
}

func (a *App) applyAllPatchesWithResolvedEndpoint() Result {
	// Resolve and bind the endpoint before writing any Antigravity file. The
	// patcher reads the same persisted non-secret runtime state, so a fallback
	// selected after a 50999 collision is never written as a dead endpoint.
	if err := proxy.Start(a.storageDir); err != nil {
		return Result{OK: false, Message: fmt.Sprintf("本地代理启动失败，未写入补丁：%s", err)}
	}
	out, err := patcher.Run("apply")
	if err != nil {
		return Result{OK: false, Message: fmt.Sprintf("%s\n%s", err.Error(), out)}
	}
	if err := proxy.CommitSelectedPort(); err != nil {
		return Result{OK: false, Message: fmt.Sprintf("补丁已写入，但本地代理端口切换尚未安全确认：%s。请保持助手运行并重新执行“应用全部补丁”。", err)}
	}
	return Result{OK: true, Message: out}
}

func (a *App) applyIDEPatchWithResolvedEndpoint() Result {
	if pending, err := storage.HasStagedProxyRuntimePort(); err != nil {
		return Result{OK: false, Message: "无法确认本地代理端口切换状态；为保护所有已补丁的 Antigravity，本次仅 IDE 补丁已停止：" + err.Error()}
	} else if pending {
		return Result{OK: false, Message: "本地代理连接正在安全调整中。请使用“应用全部补丁”，避免其他 Antigravity 安装仍指向旧连接。"}
	}
	if err := proxy.Start(a.storageDir); err != nil {
		return Result{OK: false, Message: fmt.Sprintf("本地代理启动失败，未写入 IDE 补丁：%s", err)}
	}
	if pending, err := storage.HasStagedProxyRuntimePort(); err != nil {
		return Result{OK: false, Message: "无法确认本地代理端口切换状态；为保护所有已补丁的 Antigravity，本次仅 IDE 补丁已停止：" + err.Error()}
	} else if pending {
		return Result{OK: false, Message: "本地代理连接正在安全调整中。请使用“应用全部补丁”，避免其他 Antigravity 安装仍指向旧连接。"}
	}
	out, err := patcher.Run("apply-ide")
	if err != nil {
		return Result{OK: false, Message: fmt.Sprintf("%s\n%s", err.Error(), out)}
	}
	if err := proxy.CommitSelectedPort(); err != nil {
		return Result{OK: false, Message: fmt.Sprintf("IDE 补丁已写入，但本地代理端口切换尚未安全确认：%s。请保持助手运行并重新执行“仅 IDE 补丁”。", err)}
	}
	return Result{OK: true, Message: out}
}

func (a *App) ApplyPatch() Result {
	return a.runPatchAction("apply", "全部连接", a.applyAllPatchesWithResolvedEndpoint)
}

func (a *App) ApplyIDEPatch() Result {
	return a.runPatchAction("apply-ide", "连接 Antigravity IDE", a.applyIDEPatchWithResolvedEndpoint)
}

func (a *App) ApplyAgentPatch() Result {
	return a.runPatchAction("apply-agent", "连接 Antigravity 2.0", func() Result {
		if pending, err := storage.HasStagedProxyRuntimePort(); err != nil {
			return Result{OK: false, Message: "无法确认本地代理端口切换状态；为保护所有已连接的 Antigravity，本次操作已停止：" + err.Error()}
		} else if pending {
			return Result{OK: false, Message: "本地代理连接正在安全调整中。请使用“全部连接”，避免其他 Antigravity 安装仍指向旧连接。"}
		}
		if err := proxy.Start(a.storageDir); err != nil {
			return Result{OK: false, Message: fmt.Sprintf("本地代理启动失败，未写入 Antigravity 2.0 补丁：%s", err)}
		}
		if pending, err := storage.HasStagedProxyRuntimePort(); err != nil {
			return Result{OK: false, Message: "无法确认本地代理端口切换状态；为保护所有已连接的 Antigravity，本次操作已停止：" + err.Error()}
		} else if pending {
			return Result{OK: false, Message: "本地代理连接正在安全调整中。请使用“全部连接”，避免其他 Antigravity 安装仍指向旧连接。"}
		}
		out, err := patcher.Run("apply-agent")
		if err != nil {
			return Result{OK: false, Message: fmt.Sprintf("%s\n%s", err.Error(), out)}
		}
		if err := proxy.CommitSelectedPort(); err != nil {
			return Result{OK: false, Message: fmt.Sprintf("Antigravity 2.0 补丁已写入，但本地代理端口切换尚未安全确认：%s。请保持助手运行并重新执行“仅连接 Antigravity 2.0”。", err)}
		}
		return Result{OK: true, Message: out}
	})
}

func (a *App) runPatchAction(actionName string, operation string, action func() Result) Result {
	if !a.patchMu.TryLock() {
		return Result{OK: false, Message: "已有连接任务正在进行，请等待当前任务完成。"}
	}
	defer a.patchMu.Unlock()

	a.emitPatchProgress(PatchProgress{Phase: "analyzing", Operation: operation, Percent: 5, Message: "正在识别已安装产品与兼容结构"})
	progressContext, cancelProgress := context.WithCancel(context.Background())
	progressDone := make(chan struct{})
	go func() {
		defer close(progressDone)
		ticker := time.NewTicker(650 * time.Millisecond)
		defer ticker.Stop()
		percent := 12
		for {
			select {
			case <-progressContext.Done():
				return
			case <-ticker.C:
				a.emitPatchProgress(PatchProgress{Phase: "patching", Operation: operation, Percent: percent, Message: "正在验证结构并安全写入连接配置"})
				if percent < 84 {
					percent += 2
				}
			}
		}
	}()

	result := action()
	cancelProgress()
	<-progressDone
	if !result.OK {
		a.emitPatchProgress(PatchProgress{Phase: "error", Operation: operation, Percent: 100, Message: result.Message})
		return result
	}
	if err := a.recordConnectedAntigravityTargets(actionName); err != nil {
		log.Printf("[xiass-tools] 无法保存已连接的 Antigravity 安装路径: %v", err)
	}
	a.emitPatchProgress(PatchProgress{Phase: "verifying", Operation: operation, Percent: 90, Message: "正在核验连接状态与安装完整性"})
	a.emitPatchProgress(PatchProgress{Phase: "complete", Operation: operation, Percent: 100, Message: "连接成功，可以打开 Antigravity"})
	return result
}

func (a *App) emitPatchProgress(progress PatchProgress) {
	if a.ctx != nil {
		a.emitRuntimeEvent("wf:patch-progress", progress)
	}
}

func (a *App) RestorePatch() Result {
	out, err := patcher.Run("restore")
	if err != nil {
		return Result{OK: false, Message: fmt.Sprintf("%s\n%s", err.Error(), out)}
	}
	if err := a.clearConnectedAntigravityTargets(); err != nil {
		log.Printf("[xiass-tools] 无法清除 Antigravity 连接基线: %v", err)
	}
	return Result{OK: true, Message: out}
}

// ─── Stats ────────────────────────────────────────────────────────────────────

func (a *App) GetStats() StatsResult {
	s := stats.Compute(a.storageDir)
	return StatsResult{
		TotalRequests:    s.TotalRequests,
		CustomRequests:   s.CustomRequests,
		TotalTokens:      s.TotalTokens,
		PromptTokens:     s.PromptTokens,
		CompletionTokens: s.CompletionTokens,
		CacheReadTokens:  s.CacheReadTokens,
		CacheWriteTokens: s.CacheWriteTokens,
		CacheHitRate:     s.CacheHitRate,
	}
}
