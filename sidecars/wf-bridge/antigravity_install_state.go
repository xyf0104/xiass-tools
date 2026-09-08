package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"antigravity-wf-assistant/internal/patcher"
)

const antigravityInstallStateFile = "antigravity-install-state.json"

// Increment this only when an already connected Antigravity installation
// must be run through Apply again to receive a newer on-disk compatibility
// rule. Storing it per target lets IDE-only and Agent-only operations migrate
// independently on computers that have both products installed.
const antigravityPatchRevision = "macos-image-ui-v6"

type antigravityInstallRecord struct {
	Kind              string `json:"kind"`
	AppPath           string `json:"appPath"`
	Version           string `json:"version"`
	PatchRevision     string `json:"patchRevision,omitempty"`
	ExecutableSize    int64  `json:"executableSize,omitempty"`
	ExecutableModTime int64  `json:"executableModTime,omitempty"`
}

type antigravityInstallState struct {
	Schema    int                        `json:"schema"`
	UpdatedAt string                     `json:"updatedAt"`
	Targets   []antigravityInstallRecord `json:"targets"`
}

func (a *App) antigravityInstallStatePath() string {
	return filepath.Join(a.storageDir, antigravityInstallStateFile)
}

func antigravityInstallRecordFromTarget(target patcher.TargetStatus) antigravityInstallRecord {
	record := antigravityInstallRecord{
		Kind: strings.TrimSpace(target.Kind), AppPath: filepath.Clean(target.AppPath), Version: strings.TrimSpace(target.Version),
	}
	if info, err := os.Stat(target.ExecutablePath); err == nil && info.Mode().IsRegular() {
		record.ExecutableSize = info.Size()
		record.ExecutableModTime = info.ModTime().UnixNano()
	}
	return record
}

func antigravityInstallRecordKey(kind, appPath string) string {
	return strings.ToLower(strings.TrimSpace(kind) + "|" + filepath.Clean(appPath))
}

func (a *App) loadAntigravityInstallState() antigravityInstallState {
	data, err := os.ReadFile(a.antigravityInstallStatePath())
	if err != nil {
		return antigravityInstallState{Schema: 1}
	}
	var state antigravityInstallState
	if json.Unmarshal(data, &state) != nil || state.Schema != 1 {
		return antigravityInstallState{Schema: 1}
	}
	return state
}

func (a *App) saveAntigravityInstallState(state antigravityInstallState) error {
	state.Schema = 1
	state.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	sort.Slice(state.Targets, func(i, j int) bool {
		return antigravityInstallRecordKey(state.Targets[i].Kind, state.Targets[i].AppPath) < antigravityInstallRecordKey(state.Targets[j].Kind, state.Targets[j].AppPath)
	})
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	path := a.antigravityInstallStatePath()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func (a *App) recordConnectedAntigravityTargets(action string) error {
	a.installStateMu.Lock()
	defer a.installStateMu.Unlock()
	status := patcher.GetQuickStatus()
	state := a.loadAntigravityInstallState()
	selectedKinds := map[string]bool{}
	switch action {
	case "apply-ide":
		selectedKinds["ide"] = true
	case "apply-agent":
		selectedKinds["agent"] = true
	default:
		selectedKinds["ide"], selectedKinds["agent"] = true, true
	}
	kept := make([]antigravityInstallRecord, 0, len(state.Targets)+len(status.Targets))
	for _, record := range state.Targets {
		if !selectedKinds[record.Kind] {
			kept = append(kept, record)
		}
	}
	for _, target := range status.Targets {
		if selectedKinds[target.Kind] {
			record := antigravityInstallRecordFromTarget(target)
			record.PatchRevision = antigravityPatchRevision
			kept = append(kept, record)
		}
	}
	state.Targets = kept
	return a.saveAntigravityInstallState(state)
}

func (a *App) antigravityProductRepatchState(status patcher.Status) (bool, string) {
	a.installStateMu.Lock()
	defer a.installStateMu.Unlock()
	saved := a.loadAntigravityInstallState()
	if len(saved.Targets) == 0 {
		return false, ""
	}
	current := make(map[string]antigravityInstallRecord, len(status.Targets))
	currentKinds := map[string]bool{}
	for _, target := range status.Targets {
		record := antigravityInstallRecordFromTarget(target)
		current[antigravityInstallRecordKey(record.Kind, record.AppPath)] = record
		currentKinds[record.Kind] = true
	}
	for _, previous := range saved.Targets {
		if !currentKinds[previous.Kind] {
			continue
		}
		if previous.PatchRevision != antigravityPatchRevision {
			return true, fmt.Sprintf("检测到 %s 使用的是旧版连接规则，需要重新连接以升级图片显示规则。", antigravityProductKindLabel(previous.Kind))
		}
		now, ok := current[antigravityInstallRecordKey(previous.Kind, previous.AppPath)]
		if !ok {
			return true, fmt.Sprintf("检测到 %s 的安装路径已变化，需要重新连接。", antigravityProductKindLabel(previous.Kind))
		}
		if now.Version != "" && previous.Version != "" && now.Version != previous.Version {
			return true, fmt.Sprintf("检测到 %s 已从 v%s 更新为 v%s，需要重新连接补丁。", antigravityProductKindLabel(now.Kind), previous.Version, now.Version)
		}
		if previous.ExecutableSize > 0 && (now.ExecutableSize != previous.ExecutableSize || now.ExecutableModTime != previous.ExecutableModTime) {
			return true, fmt.Sprintf("检测到 %s 已重新安装或程序文件发生变化，需要重新连接补丁。", antigravityProductKindLabel(now.Kind))
		}
		delete(current, antigravityInstallRecordKey(previous.Kind, previous.AppPath))
	}
	for _, now := range current {
		for _, previous := range saved.Targets {
			if previous.Kind == now.Kind {
				return true, fmt.Sprintf("检测到新的 %s 安装 v%s，需要重新连接补丁。", antigravityProductKindLabel(now.Kind), now.Version)
			}
		}
	}
	return false, ""
}

func (a *App) clearConnectedAntigravityTargets() error {
	a.installStateMu.Lock()
	defer a.installStateMu.Unlock()
	return a.saveAntigravityInstallState(antigravityInstallState{Schema: 1})
}

func antigravityProductKindLabel(kind string) string {
	if kind == "agent" {
		return "Antigravity 2.0"
	}
	return "Antigravity IDE"
}
