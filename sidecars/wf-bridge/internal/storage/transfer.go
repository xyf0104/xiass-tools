package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	HelperTransferSchema      = "xiass-tools.wf-helper-transfer"
	HelperTransferVersion     = 1
	maxHelperTransferEntries  = 10000
	maxHelperTransferJSONSize = 32 << 20
)

// HelperTransferBundle is the complete user-owned WF configuration boundary.
// It intentionally contains credentials and is returned only through the
// authenticated local bridge for an explicit account-backup action.
type HelperTransferBundle struct {
	Schema   string            `json:"schema"`
	Version  int               `json:"version"`
	Accounts []UpstreamAccount `json:"accounts"`
	Models   []CustomModel     `json:"models"`
	Settings AppSettings       `json:"settings"`
	Summary  struct {
		AccountCount int `json:"accountCount"`
		ModelCount   int `json:"modelCount"`
	} `json:"summary"`
}

type HelperTransferRestoreResult struct {
	OK           bool `json:"ok"`
	AccountCount int  `json:"accountCount"`
	ModelCount   int  `json:"modelCount"`
	RolledBack   bool `json:"rolledBack"`
}

type helperTransferFileState struct {
	path   string
	exists bool
	data   []byte
}

var helperTransferWriteFile = writeHelperTransferFile

func ExportHelperTransferBundle() (HelperTransferBundle, error) {
	if err := ensureHelperTransferSourcesSafe(); err != nil {
		return HelperTransferBundle{}, err
	}
	accountsMu.RLock()
	accounts, accountsErr := loadAccountsLocked()
	accountsMu.RUnlock()
	if accountsErr != nil {
		return HelperTransferBundle{}, fmt.Errorf("无法读取 WF 账户配置")
	}
	mu.RLock()
	models, modelsErr := loadModelsLocked()
	mu.RUnlock()
	if modelsErr != nil {
		return HelperTransferBundle{}, fmt.Errorf("无法读取 WF 模型配置")
	}
	settingsMu.RLock()
	settings, settingsErr := loadAppSettingsLocked()
	settingsMu.RUnlock()
	if settingsErr != nil {
		return HelperTransferBundle{}, fmt.Errorf("无法读取 WF 应用设置")
	}

	bundle := HelperTransferBundle{
		Schema: HelperTransferSchema, Version: HelperTransferVersion,
		Accounts: accounts, Models: models, Settings: settings,
	}
	bundle.Summary.AccountCount = len(accounts)
	bundle.Summary.ModelCount = len(models)
	if encoded, err := json.Marshal(bundle); err != nil || len(encoded) > maxHelperTransferJSONSize {
		return HelperTransferBundle{}, fmt.Errorf("WF 备份数据超过安全大小限制")
	}
	return bundle, nil
}

func normalizeHelperTransferBundle(bundle HelperTransferBundle) (HelperTransferBundle, error) {
	if bundle.Schema != HelperTransferSchema || bundle.Version != HelperTransferVersion {
		return HelperTransferBundle{}, fmt.Errorf("WF 备份格式不受支持")
	}
	if len(bundle.Accounts) > maxHelperTransferEntries || len(bundle.Models) > maxHelperTransferEntries {
		return HelperTransferBundle{}, fmt.Errorf("WF 备份条目超过安全数量限制")
	}
	accountIDs := make(map[string]struct{}, len(bundle.Accounts))
	for index := range bundle.Accounts {
		account := bundle.Accounts[index]
		if strings.TrimSpace(account.ID) == "" {
			return HelperTransferBundle{}, fmt.Errorf("WF 备份包含无 ID 账户")
		}
		if err := ValidateAdditionalHeaders(account.Headers); err != nil {
			return HelperTransferBundle{}, fmt.Errorf("WF 备份包含无效账户请求头")
		}
		account = normalizeLoadedAccount(account)
		if account.EffectiveAPIKey() == "" {
			return HelperTransferBundle{}, fmt.Errorf("WF 备份包含无凭据账户")
		}
		if _, duplicate := accountIDs[account.ID]; duplicate {
			return HelperTransferBundle{}, fmt.Errorf("WF 备份包含重复账户 ID")
		}
		accountIDs[account.ID] = struct{}{}
		bundle.Accounts[index] = account
	}
	modelNames := make(map[string]struct{}, len(bundle.Models))
	for index := range bundle.Models {
		model := normalizeModelDisplayName(bundle.Models[index])
		name := strings.TrimSpace(model.Name)
		if name == "" || strings.TrimSpace(model.ExternalModelName) == "" {
			return HelperTransferBundle{}, fmt.Errorf("WF 备份包含无效模型")
		}
		if _, duplicate := modelNames[name]; duplicate {
			return HelperTransferBundle{}, fmt.Errorf("WF 备份包含重复模型")
		}
		modelNames[name] = struct{}{}
		bundle.Models[index] = model
	}
	bundle.Settings = NormalizeAppSettings(bundle.Settings)
	bundle.Summary.AccountCount = len(bundle.Accounts)
	bundle.Summary.ModelCount = len(bundle.Models)
	if encoded, err := json.Marshal(bundle); err != nil || len(encoded) > maxHelperTransferJSONSize {
		return HelperTransferBundle{}, fmt.Errorf("WF 备份数据超过安全大小限制")
	}
	return bundle, nil
}

func RestoreHelperTransferBundle(bundle HelperTransferBundle) (HelperTransferRestoreResult, error) {
	bundle, err := normalizeHelperTransferBundle(bundle)
	if err != nil {
		return HelperTransferRestoreResult{}, err
	}
	accountsData, _ := json.MarshalIndent(accountsStore{Accounts: bundle.Accounts}, "", "  ")
	modelsData, _ := json.MarshalIndent(modelsStore{Models: bundle.Models}, "", "  ")
	settingsData, _ := json.MarshalIndent(bundle.Settings, "", "  ")
	updates := []helperTransferFileState{
		{path: accountsFile, data: append(accountsData, '\n')},
		{path: modelsFile, data: append(modelsData, '\n')},
		{path: appSettingsPath(), data: append(settingsData, '\n')},
	}

	accountsMu.Lock()
	mu.Lock()
	settingsMu.Lock()
	defer accountsMu.Unlock()
	defer mu.Unlock()
	defer settingsMu.Unlock()

	oldStates := make([]helperTransferFileState, len(updates))
	for index := range updates {
		state, stateErr := snapshotHelperTransferFile(updates[index].path)
		if stateErr != nil {
			return HelperTransferRestoreResult{}, stateErr
		}
		oldStates[index] = state
	}

	committed := 0
	for index := range updates {
		if writeErr := helperTransferWriteFile(updates[index].path, updates[index].data); writeErr != nil {
			rolledBack := rollbackHelperTransferFiles(oldStates, committed)
			return HelperTransferRestoreResult{RolledBack: rolledBack}, fmt.Errorf("WF 备份恢复失败")
		}
		committed++
	}
	return HelperTransferRestoreResult{
		OK: true, AccountCount: len(bundle.Accounts), ModelCount: len(bundle.Models),
	}, nil
}

func ensureHelperTransferSourcesSafe() error {
	for _, path := range []string{accountsFile, modelsFile, appSettingsPath()} {
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("WF 配置文件不是安全的常规文件")
		}
		if info.Size() > maxHelperTransferJSONSize {
			return fmt.Errorf("WF 配置文件超过安全大小限制")
		}
	}
	return nil
}

func snapshotHelperTransferFile(path string) (helperTransferFileState, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return helperTransferFileState{path: path}, nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return helperTransferFileState{}, fmt.Errorf("WF 配置文件不是安全的常规文件")
	}
	if info.Size() > maxHelperTransferJSONSize {
		return helperTransferFileState{}, fmt.Errorf("WF 配置文件超过安全大小限制")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return helperTransferFileState{}, fmt.Errorf("无法读取 WF 配置文件")
	}
	return helperTransferFileState{path: path, exists: true, data: data}, nil
}

func writeHelperTransferFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".wf-transfer-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return replaceStorageFile(temporaryPath, path)
}

func rollbackHelperTransferFiles(updates []helperTransferFileState, committed int) bool {
	ok := true
	for index := committed - 1; index >= 0; index-- {
		if updates[index].exists {
			ok = writeHelperTransferFile(updates[index].path, updates[index].data) == nil && ok
			continue
		}
		info, err := os.Lstat(updates[index].path)
		if err == nil && info.Mode()&os.ModeSymlink == 0 && info.Mode().IsRegular() {
			ok = os.Remove(updates[index].path) == nil && ok
		} else if !os.IsNotExist(err) {
			ok = false
		}
	}
	return ok
}
