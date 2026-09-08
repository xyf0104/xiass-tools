package codexconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

const workspaceStateBackupDirectory = "workspace-state-backups"

// WorkspaceStateRepairResult describes a conservative repair of Codex's local
// workspace-state cache. It is mainly relevant after a state file created on
// one operating system is reused on another one.
type WorkspaceStateRepairResult struct {
	Scanned      bool   `json:"scanned"`
	Updated      bool   `json:"updated"`
	ProjectCount int    `json:"project_count"`
	BackupID     string `json:"backup_id,omitempty"`
}

// RepairWorkspaceState performs a platform-appropriate workspace-state repair
// under the same operation lock as config.toml. It never starts or stops Codex.
func (m *Manager) RepairWorkspaceState() (WorkspaceStateRepairResult, error) {
	return m.RepairWorkspaceStateForOS(runtime.GOOS)
}

// RepairWorkspaceStateForOS makes the platform choice injectable for tests and
// for a controlled migration. Production callers should use RepairWorkspaceState.
func (m *Manager) RepairWorkspaceStateForOS(goos string) (WorkspaceStateRepairResult, error) {
	if err := m.validatePaths(); err != nil {
		return WorkspaceStateRepairResult{}, err
	}
	if err := m.requireCodexHistoryWriteSafety(); err != nil {
		return WorkspaceStateRepairResult{}, err
	}
	var result WorkspaceStateRepairResult
	err := m.withLock(func() error {
		if err := m.requireCodexHistoryWriteSafety(); err != nil {
			return err
		}
		var err error
		result, err = m.repairWorkspaceStateForOS(goos)
		return err
	})
	return result, err
}

func (m *Manager) repairWorkspaceStateForOS(goos string) (WorkspaceStateRepairResult, error) {
	result := WorkspaceStateRepairResult{}
	statePath := filepath.Join(m.CodexHome, ".codex-global-state.json")
	data, exists, mode, err := readRegularFile(statePath)
	if err != nil {
		return result, fmt.Errorf("read Codex workspace state: %w", err)
	}
	if !exists {
		return result, nil
	}
	var state map[string]any
	if err := json.Unmarshal(data, &state); err != nil {
		return result, fmt.Errorf("parse Codex workspace state: %w", err)
	}
	result.Scanned = true
	changed, projectCount := normalizeWorkspaceState(state, goos)
	result.ProjectCount = projectCount
	if !changed {
		return result, nil
	}

	backupID, err := newBackupID()
	if err != nil {
		return result, err
	}
	backupDirectory := filepath.Join(filepath.Dir(m.BackupRoot), workspaceStateBackupDirectory, backupID)
	if err := os.MkdirAll(backupDirectory, 0o700); err != nil {
		return result, fmt.Errorf("create workspace state backup: %w", err)
	}
	if err := copyRegularFile(statePath, filepath.Join(backupDirectory, filepath.Base(statePath))); err != nil {
		return result, fmt.Errorf("backup workspace state: %w", err)
	}
	fallbackPath := statePath + ".bak"
	fallbackData, fallbackExisted, fallbackMode, err := readRegularFile(fallbackPath)
	if err != nil {
		return result, fmt.Errorf("inspect workspace state fallback: %w", err)
	}
	if fallbackExisted {
		if err := writeFileAtomic(filepath.Join(backupDirectory, filepath.Base(fallbackPath)), fallbackData, fallbackMode); err != nil {
			return result, fmt.Errorf("backup workspace state fallback: %w", err)
		}
	}

	updated, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return result, fmt.Errorf("encode repaired workspace state: %w", err)
	}
	updated = append(updated, '\n')
	if err := ensureFileUnchanged(statePath, data, true); err != nil {
		return result, err
	}
	if err := m.requireCodexHistoryWriteSafety(); err != nil {
		return result, err
	}
	if err := writeFileAtomic(statePath, updated, mode); err != nil {
		return result, fmt.Errorf("write repaired workspace state: %w", err)
	}
	if err := m.requireCodexHistoryWriteSafety(); err != nil {
		return result, err
	}
	if err := writeFileAtomic(fallbackPath, updated, mode); err != nil {
		rollbackErr := restoreWorkspaceStateBackup(statePath, fallbackPath, backupDirectory, fallbackExisted)
		if rollbackErr != nil {
			return result, fmt.Errorf("write workspace state fallback: %v; rollback failed: %w", err, rollbackErr)
		}
		return result, fmt.Errorf("write workspace state fallback; repaired state was rolled back: %w", err)
	}
	if err := verifyWorkspaceState(statePath, goos); err != nil {
		rollbackErr := restoreWorkspaceStateBackup(statePath, fallbackPath, backupDirectory, fallbackExisted)
		if rollbackErr != nil {
			return result, fmt.Errorf("verify repaired workspace state: %v; rollback failed: %w", err, rollbackErr)
		}
		return result, fmt.Errorf("verify repaired workspace state; changes were rolled back: %w", err)
	}
	if err := verifyWorkspaceState(fallbackPath, goos); err != nil {
		rollbackErr := restoreWorkspaceStateBackup(statePath, fallbackPath, backupDirectory, fallbackExisted)
		if rollbackErr != nil {
			return result, fmt.Errorf("verify repaired workspace state fallback: %v; rollback failed: %w", err, rollbackErr)
		}
		return result, fmt.Errorf("verify repaired workspace state fallback; changes were rolled back: %w", err)
	}
	result.Updated = true
	result.BackupID = backupID
	return result, nil
}

func normalizeWorkspaceState(state map[string]any, goos string) (bool, int) {
	changed := false
	for _, key := range []string{"active-workspace-roots", "electron-saved-workspace-roots"} {
		normalized, updated := normalizeWorkspacePathList(state[key], goos)
		if updated {
			state[key] = normalized
			changed = true
		}
	}
	if order, ok := state["project-order"].([]any); ok {
		normalized, updated := normalizeWorkspacePathList(order, goos)
		if updated {
			state["project-order"] = normalized
			changed = true
		}
	}
	if selected, ok := state["selected-project"].(string); ok {
		if normalized, updated := normalizeWorkspacePath(selected, goos); updated {
			state["selected-project"] = normalized
			changed = true
		}
	}

	projectCount := 0
	if projects, ok := mapValue(state["local-projects"]); ok {
		for _, raw := range projects {
			project, ok := mapValue(raw)
			if !ok {
				continue
			}
			projectCount++
			roots, rootsChanged := normalizeWorkspacePathList(project["rootPaths"], goos)
			if len(roots) == 0 {
				if name, ok := project["name"].(string); ok {
					if inferred, pathLike := normalizeWorkspaceProjectPath(name, goos); pathLike {
						roots = []string{inferred}
						rootsChanged = true
					}
				}
			}
			if rootsChanged {
				project["rootPaths"] = roots
				changed = true
			}
			if len(roots) > 0 {
				if name, ok := project["name"].(string); ok && workspaceProjectNameIsPath(name, goos) {
					base := path.Base(roots[0])
					if base != "" && base != "." && name != base {
						project["name"] = base
						changed = true
					}
				}
			}
		}
	}

	if hints, ok := mapValue(state["thread-workspace-root-hints"]); ok {
		for threadID, raw := range hints {
			value, ok := raw.(string)
			if !ok {
				continue
			}
			if normalized, updated := normalizeWorkspacePath(value, goos); updated {
				hints[threadID] = normalized
				changed = true
			}
		}
	}
	if writable, ok := mapValue(state["thread-writable-roots"]); ok {
		for threadID, raw := range writable {
			normalized, updated := normalizeWorkspacePathList(raw, goos)
			if updated {
				writable[threadID] = normalized
				changed = true
			}
		}
	}
	return changed, projectCount
}

func normalizeWorkspacePathList(raw any, goos string) ([]string, bool) {
	var values []string
	switch items := raw.(type) {
	case []any:
		values = make([]string, 0, len(items))
		for _, item := range items {
			if value, ok := item.(string); ok {
				values = append(values, value)
			}
		}
	case []string:
		values = append([]string(nil), items...)
	case nil:
		return nil, false
	default:
		return nil, false
	}
	changed := false
	for index, value := range values {
		if normalized, updated := normalizeWorkspacePath(value, goos); updated {
			values[index] = normalized
			changed = true
		}
	}
	return values, changed
}

func normalizeWorkspacePath(value, goos string) (string, bool) {
	if goos != "darwin" {
		return value, false
	}
	trimmed := strings.TrimSpace(value)
	normalized := strings.ReplaceAll(trimmed, `\`, "/")
	if strings.HasPrefix(normalized, "/") {
		// The requested target is macOS, so use slash-path semantics even
		// when this repair is being verified on another host platform.
		normalized = path.Clean(normalized)
	}
	return normalized, normalized != value
}

func normalizeWorkspaceProjectPath(value, goos string) (string, bool) {
	normalized, _ := normalizeWorkspacePath(value, goos)
	if goos == "darwin" && path.IsAbs(normalized) {
		return normalized, true
	}
	return "", false
}

func workspaceProjectNameIsPath(value, goos string) bool {
	_, ok := normalizeWorkspaceProjectPath(value, goos)
	return ok
}

func verifyWorkspaceState(path, goos string) error {
	data, exists, _, err := readRegularFile(path)
	if err != nil {
		return err
	}
	if !exists {
		return errors.New("workspace state was not written")
	}
	var state map[string]any
	if err := json.Unmarshal(data, &state); err != nil {
		return err
	}
	changed, _ := normalizeWorkspaceState(state, goos)
	if changed {
		return errors.New("workspace state still contains invalid project paths")
	}
	return nil
}

func restoreWorkspaceStateBackup(statePath, fallbackPath, backupDirectory string, fallbackExisted bool) error {
	if err := copyRegularFile(filepath.Join(backupDirectory, filepath.Base(statePath)), statePath); err != nil {
		return err
	}
	if fallbackExisted {
		return copyRegularFile(filepath.Join(backupDirectory, filepath.Base(fallbackPath)), fallbackPath)
	}
	if err := os.Remove(fallbackPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func copyRegularFile(source, destination string) error {
	data, exists, mode, err := readRegularFile(source)
	if err != nil {
		return err
	}
	if !exists {
		return errors.New("source file does not exist")
	}
	return writeFileAtomic(destination, data, mode)
}
