package main

import (
	"errors"
	"strings"
	"time"

	"antigravity-wf-assistant/internal/codexconfig"
	"antigravity-wf-assistant/internal/codexselection"
)

// errCodexXIASSLifecycleNotApplied stays entirely on the native side. It
// lets the caller keep a one-time selection available after a transactional
// lifecycle failure without serializing the selected credential or a raw
// implementation error to the renderer.
var errCodexXIASSLifecycleNotApplied = errors.New("selected XIASS credential was not applied")

// CodexKeySelectionStatus is the renderer-safe state of the XIASS website
// Key-selection flow. It never contains a Key, callback URL, state token, or
// encoded payload.
type CodexKeySelectionStatus struct {
	OK        bool                 `json:"ok"`
	Message   string               `json:"message"`
	Selection codexselection.State `json:"selection"`
}

func (a *App) codexKeySelectionService() *codexselection.Service {
	if a == nil {
		return nil
	}
	a.codexSelectionMu.Lock()
	defer a.codexSelectionMu.Unlock()
	if a.codexKeySelection == nil {
		a.codexKeySelection = codexselection.New()
	}
	return a.codexKeySelection
}

func (a *App) closeCodexKeySelections() {
	if a == nil {
		return
	}
	a.codexSelectionMu.Lock()
	service := a.codexKeySelection
	a.codexSelectionMu.Unlock()
	if service != nil {
		service.Close()
	}
}

// StartCodexXIASSKeySelection opens the user's configured XIASS API website
// on a short-lived loopback handoff. The sensitive connect URL stays native:
// only its redacted session state is returned to Vue.
func (a *App) StartCodexXIASSKeySelection(siteURL string) CodexKeySelectionStatus {
	if a == nil || a.ctx == nil || a.exitRequested.Load() {
		return CodexKeySelectionStatus{OK: false, Message: "XIASS Tools 尚未完成启动，无法打开 Key 选择页。"}
	}
	started, err := a.codexKeySelectionService().Begin(siteURL)
	if err != nil {
		return CodexKeySelectionStatus{OK: false, Message: "无法启动 XIASS API Key 选择。请检查网站地址后重试。"}
	}
	if err := a.openExternalURL(started.ConnectURL); err != nil {
		a.codexKeySelectionService().Cancel(started.State.SessionID)
		return CodexKeySelectionStatus{OK: false, Message: "无法打开 XIASS API Key 选择页，请稍后重试。"}
	}
	return CodexKeySelectionStatus{OK: true, Message: started.State.Message, Selection: started.State}
}

func (a *App) GetCodexXIASSKeySelectionStatus(sessionID string) CodexKeySelectionStatus {
	service := a.codexKeySelectionService()
	if service == nil {
		return CodexKeySelectionStatus{OK: false, Message: "XIASS API Key 选择服务不可用。"}
	}
	state := service.Status(sessionID)
	return CodexKeySelectionStatus{OK: state.Status == "pending" || state.Status == "ready", Message: state.Message, Selection: state}
}

// CompleteCodexXIASSKeySelectionManual supports the safe fallback where the
// user pastes the browser's complete callback URL. The URL is never persisted,
// logged, emitted, or placed in global renderer state.
func (a *App) CompleteCodexXIASSKeySelectionManual(sessionID, callbackURL string) CodexKeySelectionStatus {
	service := a.codexKeySelectionService()
	if service == nil {
		return CodexKeySelectionStatus{OK: false, Message: "XIASS API Key 选择服务不可用。"}
	}
	state := service.CompleteManualCallback(sessionID, callbackURL)
	return CodexKeySelectionStatus{OK: state.Status == "ready", Message: state.Message, Selection: state}
}

func (a *App) CancelCodexXIASSKeySelection(sessionID string) CodexKeySelectionStatus {
	service := a.codexKeySelectionService()
	if service == nil {
		return CodexKeySelectionStatus{OK: false, Message: "XIASS API Key 选择服务不可用。"}
	}
	state := service.Cancel(sessionID)
	return CodexKeySelectionStatus{OK: true, Message: state.Message, Selection: state}
}

// DiscoverCodexXIASSSelectionModels requests /v1/models with the key held in
// the native selection session. The API key cannot cross the Wails boundary.
func (a *App) DiscoverCodexXIASSSelectionModels(sessionID string) CodexModelDiscoveryResult {
	service := a.codexKeySelectionService()
	if service == nil {
		return CodexModelDiscoveryResult{OK: false, Message: "XIASS API Key 选择服务不可用。"}
	}
	ctx, cancel := a.upstreamContext(10 * time.Second)
	defer cancel()
	var models []string
	_, err := service.WithCredential(sessionID, false, func(credential codexselection.Credential) error {
		var discoveryErr error
		models, discoveryErr = codexconfig.DiscoverModels(ctx, credential.BaseURL, string(credential.APIKey), codexconfig.ModelDiscoveryOptions{})
		return discoveryErr
	})
	if err != nil {
		return CodexModelDiscoveryResult{OK: false, Message: "获取 XIASS API 上游模型失败。请检查 Key 选择状态、网络和模型服务后重试。"}
	}
	return CodexModelDiscoveryResult{OK: true, Message: codexModelCatalogMessage(len(models)), Models: models}
}

// ApplyCodexXIASSSelection writes the selected native-only Key to the
// dedicated xiass_tools provider. The caller's BaseURL/APIKey fields are
// deliberately ignored: the verified website selection owns both values.
func (a *App) ApplyCodexXIASSSelection(sessionID string, input codexconfig.ApplyConfig) CodexConfigurationStatus {
	manager, err := a.codexManager()
	if err != nil {
		return codexConfigurationUnavailableStatus()
	}
	service := a.codexKeySelectionService()
	if service == nil {
		return CodexConfigurationStatus{OK: false, Message: "XIASS API Key 选择服务不可用。"}
	}
	var result codexconfig.ApplyResult
	_, err = service.WithCredential(sessionID, true, func(credential codexselection.Credential) error {
		input.APIKey = string(credential.APIKey)
		input.BaseURL = credential.BaseURL
		input.KeyName = credential.KeyName
		if strings.TrimSpace(input.ProviderName) == "" {
			input.ProviderName = credential.KeyName
		}
		var applyErr error
		result, applyErr = manager.Apply(input)
		input.APIKey = ""
		return applyErr
	})
	if err != nil {
		return codexConfigurationAfterError(manager, "未保存通过 XIASS API 选择的 Codex 配置。请重新选择 Key 并检查模型设置后重试。")
	}
	status := a.GetCodexConfiguration()
	if !status.OK {
		status.Message = "配置已安全写入并通过校验，但刷新状态失败：" + status.Message
		return status
	}
	status.Message = "已使用 XIASS API 选择的 Key 安全保存 Codex 配置，并创建可恢复备份 " + result.BackupID + "。请自行退出并重新打开 Codex 后使新配置生效。"
	return status
}

// ApplyCodexXIASSSelectionWithLifecycle applies a browser-selected XIASS API
// key through the same explicit Codex Desktop lifecycle transaction as a
// manually entered key. BaseURL, APIKey, and KeyName in input are ignored;
// only the short-lived native selection credential is used. The API key never
// crosses the Wails response boundary, is never copied into App state, and is
// cleared from this stack frame before the method returns.
//
// If the transaction does not complete, the selection remains available for a
// user-initiated retry until it expires or is cancelled. A successful
// transaction consumes and zeroes the one-time native selection session.
func (a *App) ApplyCodexXIASSSelectionWithLifecycle(sessionID string, input CodexConfigurationLifecycleInput) CodexConfigurationLifecycleStatus {
	if a == nil || a.ctx == nil || a.exitRequested.Load() {
		return CodexConfigurationLifecycleStatus{OK: false, Message: "XIASS Tools 尚未完成启动，无法应用 Codex 配置。"}
	}
	service := a.codexKeySelectionService()
	if service == nil {
		return CodexConfigurationLifecycleStatus{OK: false, Message: "XIASS API Key 选择服务不可用。"}
	}

	// The configuration input is renderer-controlled. The selection owns the
	// connection credential, so discard any manually supplied credential fields
	// before we ask the native service for its short-lived key.
	input.Config.APIKey = ""
	input.Config.BaseURL = ""
	input.Config.KeyName = ""

	a.codexDesktopOperation.Lock()
	defer a.codexDesktopOperation.Unlock()

	manager, err := a.codexManager()
	if err != nil {
		return CodexConfigurationLifecycleStatus{OK: false, Message: "无法识别本机 Codex 配置目录；未执行任何操作。"}
	}

	var lifecycleStatus CodexConfigurationLifecycleStatus
	_, err = service.WithCredential(sessionID, true, func(credential codexselection.Credential) error {
		// codexconfig accepts a string API key, so this conversion is strictly
		// limited to the synchronous native apply call. The selection service
		// separately zeroes its copied byte slice once this callback returns.
		input.Config.APIKey = string(credential.APIKey)
		input.Config.BaseURL = credential.BaseURL
		input.Config.KeyName = credential.KeyName
		if strings.TrimSpace(input.Config.ProviderName) == "" {
			input.Config.ProviderName = credential.KeyName
		}
		defer func() {
			input.Config.APIKey = ""
			input.Config.BaseURL = ""
			input.Config.KeyName = ""
		}()

		lifecycleStatus = a.applyCodexConfigurationWithLifecycleLocked(input, manager, manager.Apply)
		if !lifecycleStatus.OK {
			return errCodexXIASSLifecycleNotApplied
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, errCodexXIASSLifecycleNotApplied) {
			return lifecycleStatus
		}
		return CodexConfigurationLifecycleStatus{OK: false, Message: "未保存通过 XIASS API 选择的 Codex 配置。请重新选择 Key 并检查模型设置后重试。"}
	}
	return lifecycleStatus
}
