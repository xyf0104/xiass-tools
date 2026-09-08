package permissions

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const eagerPolicy = "CASCADE_COMMANDS_AUTO_EXECUTION_EAGER"

var developmentGrants = []string{
	"command(git)",
	"command(go)",
	"command(npm)",
	"command(node)",
	"command(npx)",
	"command(pnpm)",
	"command(pytest)",
	"command(python)",
	"command(wails)",
	"command(yarn)",
}

type Settings struct {
	Enabled     bool     `json:"enabled"`
	Mode        string   `json:"mode"`
	CustomRules []string `json:"customRules"`
}

type Status struct {
	Enabled       bool     `json:"enabled"`
	Mode          string   `json:"mode"`
	CustomRules   []string `json:"customRules"`
	ManagedGrants []string `json:"managedGrants"`
	ConfigPath    string   `json:"configPath"`
	BackupPath    string   `json:"backupPath"`
}

type managedState struct {
	Enabled        bool     `json:"enabled"`
	Mode           string   `json:"mode"`
	CustomRules    []string `json:"customRules"`
	ManagedGrants  []string `json:"managedGrants"`
	PreviousPolicy *string  `json:"previousPolicy,omitempty"`
}

type Manager struct {
	configPath string
	statePath  string
	backupPath string
}

func New(home, storageDir string) *Manager {
	configPath := filepath.Join(home, ".gemini", "config", "config.json")
	return &Manager{
		configPath: configPath,
		statePath:  filepath.Join(storageDir, "auto-approval-state.json"),
		backupPath: configPath + ".antigravity-wf-backup",
	}
}

func NewWithPaths(configPath, statePath, backupPath string) *Manager {
	return &Manager{configPath: configPath, statePath: statePath, backupPath: backupPath}
}

func (m *Manager) Status() (Status, error) {
	state, err := m.loadState()
	if err != nil {
		return Status{}, err
	}
	return Status{
		Enabled:       state.Enabled,
		Mode:          state.Mode,
		CustomRules:   append([]string(nil), state.CustomRules...),
		ManagedGrants: append([]string(nil), state.ManagedGrants...),
		ConfigPath:    m.configPath,
		BackupPath:    m.backupPath,
	}, nil
}

func (m *Manager) Apply(settings Settings) (Status, error) {
	root, raw, err := m.loadConfig()
	if err != nil {
		return Status{}, err
	}
	state, err := m.loadState()
	if err != nil {
		return Status{}, err
	}
	userSettings := ensureMap(root, "userSettings")
	grants := ensureMap(userSettings, "globalPermissionGrants")
	allow := stringSlice(grants["allow"])
	allow = removeValues(allow, state.ManagedGrants)

	if !settings.Enabled {
		if !state.Enabled {
			return m.Status()
		}
		grants["allow"] = allow
		if state.PreviousPolicy == nil {
			delete(userSettings, "autoExecutionPolicy")
		} else if current, _ := userSettings["autoExecutionPolicy"].(string); current == eagerPolicy {
			userSettings["autoExecutionPolicy"] = *state.PreviousPolicy
		}
		if err := m.writeConfig(root, raw); err != nil {
			return Status{}, err
		}
		state.Enabled = false
		state.ManagedGrants = nil
		state.CustomRules = nil
		if err := m.writeState(state); err != nil {
			_ = atomicWrite(m.configPath, raw, 0o600)
			return Status{}, err
		}
		return m.Status()
	}

	managed, err := grantsFor(settings)
	if err != nil {
		return Status{}, err
	}
	if !state.Enabled {
		if value, ok := userSettings["autoExecutionPolicy"].(string); ok {
			state.PreviousPolicy = &value
		} else {
			state.PreviousPolicy = nil
		}
	}
	if err := m.ensureBackup(raw); err != nil {
		return Status{}, err
	}
	userSettings["autoExecutionPolicy"] = eagerPolicy
	grants["allow"] = appendUnique(allow, managed...)
	if err := m.writeConfig(root, raw); err != nil {
		return Status{}, err
	}
	state.Enabled = true
	state.Mode = settings.Mode
	state.CustomRules = normalizeRules(settings.CustomRules)
	state.ManagedGrants = managed
	if err := m.writeState(state); err != nil {
		_ = atomicWrite(m.configPath, raw, 0o600)
		return Status{}, err
	}
	return m.Status()
}

func grantsFor(settings Settings) ([]string, error) {
	switch settings.Mode {
	case "development":
		return append([]string(nil), developmentGrants...), nil
	case "all":
		return []string{"command(*)"}, nil
	case "custom":
		rules := normalizeRules(settings.CustomRules)
		if len(rules) == 0 {
			return nil, errors.New("自定义模式至少需要一条 command(...) 规则")
		}
		for _, rule := range rules {
			if !strings.HasPrefix(rule, "command(") || !strings.HasSuffix(rule, ")") {
				return nil, fmt.Errorf("无效命令授权规则：%s", rule)
			}
			if rule == "command(*)" {
				return nil, errors.New("command(*) 只能通过“所有命令”模式启用")
			}
		}
		return rules, nil
	default:
		return nil, fmt.Errorf("未知自动批准模式：%s", settings.Mode)
	}
}

func normalizeRules(rules []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(rules))
	for _, rule := range rules {
		rule = strings.TrimSpace(rule)
		if rule != "" && !seen[rule] {
			seen[rule] = true
			result = append(result, rule)
		}
	}
	sort.Strings(result)
	return result
}

func (m *Manager) loadConfig() (map[string]any, []byte, error) {
	raw, err := os.ReadFile(m.configPath)
	// A freshly installed Antigravity instance may not create its settings file
	// until it has been opened once. Applying the optional auto-approval patch
	// must therefore create a minimal config rather than failing on a new Mac.
	// This mirrors the Windows implementation and keeps the feature independent
	// of first-launch ordering.
	if errors.Is(err, os.ErrNotExist) {
		return map[string]any{}, []byte("{}\n"), nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("读取 Antigravity 配置失败：%w", err)
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, nil, fmt.Errorf("Antigravity 配置不是有效 JSON：%w", err)
	}
	return root, raw, nil
}

func (m *Manager) loadState() (managedState, error) {
	raw, err := os.ReadFile(m.statePath)
	if errors.Is(err, os.ErrNotExist) {
		return managedState{Mode: "development"}, nil
	}
	if err != nil {
		return managedState{}, err
	}
	var state managedState
	if err := json.Unmarshal(raw, &state); err != nil {
		return managedState{}, fmt.Errorf("自动批准状态文件损坏：%w", err)
	}
	if state.Mode == "" {
		state.Mode = "development"
	}
	return state, nil
}

func (m *Manager) ensureBackup(raw []byte) error {
	if _, err := os.Stat(m.backupPath); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := atomicWrite(m.backupPath, raw, 0o600); err != nil {
		return fmt.Errorf("创建 Antigravity 配置备份失败：%w", err)
	}
	return nil
}

func (m *Manager) writeConfig(root map[string]any, original []byte) error {
	encoded, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	if err := atomicWrite(m.configPath, append(encoded, '\n'), 0o600); err != nil {
		_ = os.WriteFile(m.configPath, original, 0o600)
		return fmt.Errorf("写入 Antigravity 配置失败：%w", err)
	}
	return nil
}

func (m *Manager) writeState(state managedState) error {
	encoded, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(m.statePath, append(encoded, '\n'), 0o600)
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".antigravity-wf-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func ensureMap(parent map[string]any, key string) map[string]any {
	if value, ok := parent[key].(map[string]any); ok {
		return value
	}
	value := map[string]any{}
	parent[key] = value
	return value
}

func stringSlice(value any) []string {
	items, _ := value.([]any)
	result := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			result = append(result, text)
		}
	}
	if strings, ok := value.([]string); ok {
		return append([]string(nil), strings...)
	}
	return result
}

func removeValues(values, remove []string) []string {
	blocked := map[string]bool{}
	for _, value := range remove {
		blocked[value] = true
	}
	result := values[:0]
	for _, value := range values {
		if !blocked[value] {
			result = append(result, value)
		}
	}
	return result
}

func appendUnique(values []string, additions ...string) []string {
	seen := map[string]bool{}
	for _, value := range values {
		seen[value] = true
	}
	for _, value := range additions {
		if !seen[value] {
			seen[value] = true
			values = append(values, value)
		}
	}
	return values
}
