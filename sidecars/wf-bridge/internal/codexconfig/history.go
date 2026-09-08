package codexconfig

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
	_ "modernc.org/sqlite"
)

const (
	// legacyCodexProviderID is retained only to avoid migrating a historical
	// provider marker back over a newer configured provider.
	legacyCodexProviderID        = "xiass"
	historyBackupVersion         = 2
	historyMetadataMax           = 2 << 20
	historyMetadataLines         = 64
	historyBackupDirName         = "history-backups"
	historyOperationLock         = "history-operation.lock"
	historyManagedBy             = "XIASS Tools history repair"
	historyStatusPrepared        = "prepared"
	historyStatusApplying        = "applying"
	historyStatusCommitted       = "committed"
	historyStatusRolledBack      = "rolled_back"
	historyStatusRollbackFailed  = "rollback_failed"
	historySessionActionProvider = "provider"
	historySessionActionDropItem = "drop_incompatible_response_item"
	historySessionActionStripID  = "strip_invalid_message_id"
)

var historySanitizedRecord = []byte(`{}`)

var allowImmutableHistoryReadForTests bool

type HistoryRepairer struct {
	CodexHome     string
	BackupRoot    string
	LockPath      string
	ConfigManager *Manager
}

type HistoryRepairResult struct {
	TargetProvider      string                      `json:"target_provider"`
	SourceProviders     []string                    `json:"source_providers,omitempty"`
	Skipped             bool                        `json:"skipped,omitempty"`
	SkipReason          string                      `json:"skip_reason,omitempty"`
	ScannedSessionFiles int                         `json:"scanned_session_files"`
	UpdatedSessionFiles int                         `json:"updated_session_files"`
	SanitizedRecords    int                         `json:"sanitized_records"`
	ScannedDatabases    int                         `json:"scanned_databases"`
	UpdatedDatabaseRows int64                       `json:"updated_database_rows"`
	ThreadCount         int64                       `json:"thread_count"`
	BackupID            string                      `json:"backup_id,omitempty"`
	WorkspaceState      *WorkspaceStateRepairResult `json:"workspace_state,omitempty"`
}

type HistoryBackupInfo struct {
	ID               string    `json:"id"`
	CreatedAt        time.Time `json:"created_at"`
	TargetProvider   string    `json:"target_provider"`
	SanitizedRecords int       `json:"sanitized_records"`
}

type HistoryRepairApplyError struct {
	Cause       error
	RollbackErr error
}

func (e *HistoryRepairApplyError) Error() string {
	if e.RollbackErr != nil {
		return fmt.Sprintf("history repair failed: %v; automatic rollback also failed: %v", e.Cause, e.RollbackErr)
	}
	return fmt.Sprintf("history repair failed and was rolled back safely: %v", e.Cause)
}

func (e *HistoryRepairApplyError) Unwrap() error {
	return e.Cause
}

type historyRepairPlan struct {
	TargetProvider     string
	SourceProviders    []string
	Sessions           []historySessionPlan
	Databases          []historyDatabasePlan
	ScannedFiles       int
	ThreadCount        int64
	SanitizedRecords   int
	RolloutFilesSHA256 string
}

type historySessionPlan struct {
	Path         string    `json:"-"`
	RelativePath string    `json:"path"`
	LineIndex    int       `json:"line_index"`
	OriginalLine []byte    `json:"original_line"`
	UpdatedLine  []byte    `json:"updated_line"`
	Mode         uint32    `json:"mode"`
	ModifiedAt   time.Time `json:"modified_at"`
	Action       string    `json:"action"`
}

type historyDatabasePlan struct {
	Path                string `json:"-"`
	RelativePath        string `json:"path"`
	ThreadCount         int64  `json:"thread_count"`
	MismatchedRows      int64  `json:"mismatched_rows"`
	ThreadIDsSHA256     string `json:"thread_ids_sha256"`
	ThreadContentSHA256 string `json:"thread_content_sha256"`
}

type historyBackupFile struct {
	SourcePath string `json:"source_path"`
	BackupPath string `json:"backup_path"`
	Existed    bool   `json:"existed"`
	Mode       uint32 `json:"mode,omitempty"`
	SHA256     string `json:"sha256,omitempty"`
}

type historyBackupManifest struct {
	Version            int                   `json:"version"`
	ID                 string                `json:"id"`
	CreatedAt          time.Time             `json:"created_at"`
	CodexHome          string                `json:"codex_home"`
	TargetProvider     string                `json:"target_provider"`
	SourceProviders    []string              `json:"source_providers,omitempty"`
	ScannedFiles       int                   `json:"scanned_files"`
	RolloutFilesSHA256 string                `json:"rollout_files_sha256"`
	ManagedBy          string                `json:"managed_by"`
	Status             string                `json:"status"`
	StatusMessage      string                `json:"status_message,omitempty"`
	SessionChanges     []historySessionPlan  `json:"session_changes"`
	DatabaseFiles      []historyBackupFile   `json:"database_files"`
	DatabasePlans      []historyDatabasePlan `json:"database_plans"`
	ImportSource       string                `json:"import_source,omitempty"`
	ImportSourceID     string                `json:"import_source_id,omitempty"`
	ImportedAt         *time.Time            `json:"imported_at,omitempty"`
}

func NewHistoryRepairer(codexHome string) *HistoryRepairer {
	return NewHistoryRepairerWithManager(NewManager(codexHome))
}

// NewHistoryRepairerWithManager keeps history and workspace backups under the
// same first-party data directory as the selected configuration manager.
func NewHistoryRepairerWithManager(manager *Manager) *HistoryRepairer {
	if manager == nil {
		manager = NewManager("")
	}
	root := filepath.Dir(manager.BackupRoot)
	return &HistoryRepairer{
		CodexHome:     manager.CodexHome,
		BackupRoot:    filepath.Join(root, historyBackupDirName),
		LockPath:      filepath.Join(root, historyOperationLock),
		ConfigManager: manager,
	}
}

func (r *HistoryRepairer) RepairCurrentProvider() (HistoryRepairResult, error) {
	target, err := readCurrentProvider(filepath.Join(r.CodexHome, "config.toml"))
	if err != nil {
		return HistoryRepairResult{}, err
	}
	return r.Repair(target)
}

func (r *HistoryRepairer) RepairCurrentProviderCompatibility() (HistoryRepairResult, error) {
	target, err := readCurrentProvider(filepath.Join(r.CodexHome, "config.toml"))
	if err != nil {
		return HistoryRepairResult{}, err
	}
	return r.RepairWithCompatibility(target)
}

func (r *HistoryRepairer) Repair(targetProvider string) (HistoryRepairResult, error) {
	return r.repair(targetProvider, false)
}

func (r *HistoryRepairer) RepairWithCompatibility(targetProvider string) (HistoryRepairResult, error) {
	return r.repair(targetProvider, true)
}

func (r *HistoryRepairer) repair(targetProvider string, repairCompatibility bool) (HistoryRepairResult, error) {
	targetProvider = strings.TrimSpace(targetProvider)
	if !validHistoryProviderID(targetProvider) {
		return HistoryRepairResult{}, errors.New("invalid model provider for history repair")
	}

	var result HistoryRepairResult
	err := r.withLock(func() error {
		// A prior operation can leave a rollback manifest behind. Do not even
		// recover it while Codex might still be writing the same local files.
		if err := r.requireCodexHistoryWriteSafety(); err != nil {
			return err
		}
		if err := r.recoverInterruptedOperations(); err != nil {
			return &HistoryRepairApplyError{Cause: errors.New("an interrupted history repair requires recovery"), RollbackErr: err}
		}
		workspaceManager := r.ConfigManager
		if workspaceManager == nil || filepath.Clean(workspaceManager.CodexHome) != filepath.Clean(r.CodexHome) {
			workspaceManager = NewManager(r.CodexHome)
		}
		workspaceState, err := workspaceManager.RepairWorkspaceState()
		if err != nil {
			return fmt.Errorf("repair Codex workspace state: %w", err)
		}
		var sourceProviders []string
		if targetProvider != legacyCodexProviderID {
			var err error
			sourceProviders, err = r.discoverSourceProviders(targetProvider)
			if err != nil {
				return err
			}
		}
		plan, err := r.buildPlanWithCompatibility(targetProvider, sourceProviders, repairCompatibility)
		if err != nil {
			return err
		}
		result = HistoryRepairResult{
			TargetProvider:      targetProvider,
			SourceProviders:     append([]string(nil), sourceProviders...),
			ScannedSessionFiles: plan.ScannedFiles,
			ScannedDatabases:    len(plan.Databases),
			ThreadCount:         plan.ThreadCount,
			SanitizedRecords:    plan.SanitizedRecords,
			WorkspaceState:      &workspaceState,
		}
		needsDatabaseUpdate := false
		for _, database := range plan.Databases {
			if database.MismatchedRows > 0 {
				needsDatabaseUpdate = true
				break
			}
		}
		if len(plan.Sessions) == 0 && !needsDatabaseUpdate {
			return nil
		}
		// Planning is read-only. Check again after it completes so a Codex
		// launch during a long scan cannot advance to backup or mutation.
		if err := r.requireCodexHistoryWriteSafety(); err != nil {
			return err
		}

		manifest, err := r.createBackup(plan)
		if err != nil {
			return fmt.Errorf("create history repair backup: %w", err)
		}
		result.BackupID = manifest.ID
		manifest.Status = historyStatusApplying
		if err := r.writeBackupManifest(manifest); err != nil {
			return fmt.Errorf("record history repair start: %w", err)
		}

		appliedSessions := make([]historySessionPlan, 0, len(plan.Sessions))
		var updatedRows int64
		applyErr := func() error {
			for _, sessions := range groupHistorySessionPlans(plan.Sessions) {
				// Re-check immediately before touching each JSONL file. A user may
				// have opened Codex while the read-only plan or backup was running.
				if err := r.requireCodexHistoryWriteSafety(); err != nil {
					return err
				}
				if err := replaceHistorySessionLines(sessions, false); err != nil {
					return err
				}
				appliedSessions = append(appliedSessions, sessions...)
			}
			for _, database := range plan.Databases {
				// SQLite has independent WAL semantics, so it gets a separate
				// safety check immediately before the database write.
				if err := r.requireCodexHistoryWriteSafety(); err != nil {
					return err
				}
				rows, err := updateDatabaseProvider(database, targetProvider, plan.SourceProviders)
				if err != nil {
					return err
				}
				updatedRows += rows
			}
			if err := r.verifyPlan(plan); err != nil {
				return err
			}
			manifest.Status = historyStatusCommitted
			manifest.StatusMessage = "history repair verified"
			return r.writeBackupManifest(manifest)
		}()
		if applyErr != nil {
			rollbackErr := r.rollback(manifest, appliedSessions)
			if rollbackErr == nil {
				manifest.Status = historyStatusRolledBack
				manifest.StatusMessage = applyErr.Error()
			} else {
				manifest.Status = historyStatusRollbackFailed
				manifest.StatusMessage = rollbackErr.Error()
			}
			if statusErr := r.writeBackupManifest(manifest); statusErr != nil {
				if rollbackErr == nil {
					rollbackErr = statusErr
				} else {
					rollbackErr = fmt.Errorf("%v; record rollback status: %w", rollbackErr, statusErr)
				}
			}
			return &HistoryRepairApplyError{Cause: applyErr, RollbackErr: rollbackErr}
		}

		result.UpdatedSessionFiles = uniqueSessionFileCount(appliedSessions)
		result.UpdatedDatabaseRows = updatedRows
		return nil
	})
	return result, err
}

func (r *HistoryRepairer) buildPlan(targetProvider string, sourceProviders []string) (historyRepairPlan, error) {
	return r.buildPlanWithCompatibility(targetProvider, sourceProviders, false)
}

func (r *HistoryRepairer) buildPlanWithCompatibility(targetProvider string, sourceProviders []string, repairCompatibility bool) (historyRepairPlan, error) {
	plan := historyRepairPlan{TargetProvider: targetProvider, SourceProviders: append([]string(nil), sourceProviders...)}
	databasePaths, err := discoverHistoryDatabases(r.CodexHome)
	if err != nil {
		return plan, err
	}
	for _, path := range databasePaths {
		database, ok, err := inspectHistoryDatabase(r.CodexHome, path, sourceProviders)
		if err != nil {
			return plan, err
		}
		if ok {
			plan.Databases = append(plan.Databases, database)
			plan.ThreadCount += database.ThreadCount
		}
	}
	fullSessionScan := len(sourceProviders) > 0 || (repairCompatibility && shouldSanitizeSessionReplay(targetProvider))

	rollouts, err := discoverRolloutFiles(r.CodexHome)
	if err != nil {
		return plan, err
	}
	plan.ScannedFiles = len(rollouts)
	plan.RolloutFilesSHA256 = historyPathSetSHA256(r.CodexHome, rollouts)
	for _, path := range rollouts {
		_, needsUpdate, err := inspectSessionMetadata(r.CodexHome, path, targetProvider, sourceProviders)
		if err != nil {
			return plan, err
		}
		if fullSessionScan || needsUpdate {
			sessions, err := inspectAllSessionMetadata(r.CodexHome, path, targetProvider, sourceProviders, repairCompatibility)
			if err != nil {
				return plan, err
			}
			plan.Sessions = append(plan.Sessions, sessions...)
			for _, session := range sessions {
				if session.Action == historySessionActionDropItem || session.Action == historySessionActionStripID {
					plan.SanitizedRecords++
				}
			}
		}
	}
	return plan, nil
}

func (r *HistoryRepairer) createBackup(plan historyRepairPlan) (historyBackupManifest, error) {
	id, err := newBackupID()
	if err != nil {
		return historyBackupManifest{}, err
	}
	dir := filepath.Join(r.BackupRoot, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return historyBackupManifest{}, err
	}

	manifest := historyBackupManifest{
		Version:            historyBackupVersion,
		ID:                 id,
		CreatedAt:          time.Now().UTC(),
		CodexHome:          r.CodexHome,
		TargetProvider:     plan.TargetProvider,
		SourceProviders:    append([]string(nil), plan.SourceProviders...),
		ScannedFiles:       plan.ScannedFiles,
		RolloutFilesSHA256: plan.RolloutFilesSHA256,
		ManagedBy:          historyManagedBy,
		Status:             historyStatusPrepared,
		SessionChanges:     plan.Sessions,
		DatabasePlans:      plan.Databases,
	}
	for _, database := range plan.Databases {
		item, err := backupHistoryDatabase(r.CodexHome, dir, database)
		if err != nil {
			return historyBackupManifest{}, err
		}
		manifest.DatabaseFiles = append(manifest.DatabaseFiles, item)
	}
	for _, name := range []string{"config.toml", "session_index.jsonl", ".codex-global-state.json", ".codex-global-state.json.bak"} {
		source := filepath.Join(r.CodexHome, name)
		if _, err := os.Stat(source); err == nil {
			target := filepath.Join(dir, "snapshot", name)
			if err := copyHistoryRegularFile(source, target); err != nil {
				return historyBackupManifest{}, err
			}
		}
	}

	if err := r.writeBackupManifest(manifest); err != nil {
		return historyBackupManifest{}, err
	}
	return manifest, nil
}

func (r *HistoryRepairer) writeBackupManifest(manifest historyBackupManifest) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	path := filepath.Join(r.BackupRoot, manifest.ID, "manifest.json")
	return writeFileAtomic(path, data, 0o600)
}

func (r *HistoryRepairer) recoverInterruptedOperations() error {
	entries, err := os.ReadDir(r.BackupRoot)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !validBackupID(entry.Name()) {
			continue
		}
		manifestPath := filepath.Join(r.BackupRoot, entry.Name(), "manifest.json")
		data, exists, _, err := readRegularFile(manifestPath)
		if err != nil || !exists {
			continue
		}
		var manifest historyBackupManifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			return fmt.Errorf("read interrupted history manifest %s: %w", entry.Name(), err)
		}
		if manifest.Version != historyBackupVersion || manifest.ManagedBy != historyManagedBy || manifest.ID != entry.Name() {
			continue
		}
		if err := r.validateBackupManifest(manifest); err != nil {
			return fmt.Errorf("validate interrupted history manifest %s: %w", entry.Name(), err)
		}
		switch manifest.Status {
		case historyStatusPrepared:
			manifest.Status = historyStatusRolledBack
			manifest.StatusMessage = "prepared operation ended before any writes"
			if err := r.writeBackupManifest(manifest); err != nil {
				return err
			}
		case historyStatusApplying, historyStatusRollbackFailed:
			sessions := append([]historySessionPlan(nil), manifest.SessionChanges...)
			for index := range sessions {
				sessions[index].Path = filepath.Join(r.CodexHome, filepath.FromSlash(sessions[index].RelativePath))
			}
			if err := r.validateInterruptedRollbackBaseline(manifest); err != nil {
				manifest.Status = historyStatusRollbackFailed
				manifest.StatusMessage = err.Error()
				_ = r.writeBackupManifest(manifest)
				return fmt.Errorf("interrupted history repair has newer local data and was not overwritten: %w", err)
			}
			if err := r.rollback(manifest, sessions); err != nil {
				manifest.Status = historyStatusRollbackFailed
				manifest.StatusMessage = err.Error()
				_ = r.writeBackupManifest(manifest)
				return fmt.Errorf("recover interrupted history repair %s: %w", manifest.ID, err)
			}
			manifest.Status = historyStatusRolledBack
			manifest.StatusMessage = "recovered automatically after an interrupted operation"
			if err := r.writeBackupManifest(manifest); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *HistoryRepairer) validateInterruptedRollbackBaseline(manifest historyBackupManifest) error {
	rollouts, err := discoverRolloutFiles(r.CodexHome)
	if err != nil {
		return err
	}
	if len(rollouts) != manifest.ScannedFiles || historyPathSetSHA256(r.CodexHome, rollouts) != manifest.RolloutFilesSHA256 {
		return errors.New("rollout file set changed after the interrupted repair")
	}
	for _, expected := range manifest.DatabasePlans {
		path := filepath.Join(r.CodexHome, filepath.FromSlash(expected.RelativePath))
		actual, ok, err := inspectHistoryDatabase(r.CodexHome, path, manifest.SourceProviders)
		if err != nil {
			return err
		}
		if !ok || actual.ThreadCount != expected.ThreadCount || actual.ThreadIDsSHA256 != expected.ThreadIDsSHA256 || actual.ThreadContentSHA256 != expected.ThreadContentSHA256 {
			return fmt.Errorf("thread identity set changed after the interrupted repair: %s", expected.RelativePath)
		}
	}
	return nil
}

func (r *HistoryRepairer) RestoreBackup(backupID string) error {
	if backupID == "" {
		return nil
	}
	if !validBackupID(backupID) {
		return errors.New("invalid history backup ID")
	}
	return r.withLock(func() error {
		if err := r.requireCodexHistoryWriteSafety(); err != nil {
			return err
		}
		data, exists, _, err := readRegularFile(filepath.Join(r.BackupRoot, backupID, "manifest.json"))
		if err != nil || !exists {
			if err == nil {
				err = errors.New("history backup manifest does not exist")
			}
			return err
		}
		var manifest historyBackupManifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			return err
		}
		if manifest.Version != historyBackupVersion || manifest.ManagedBy != historyManagedBy || manifest.ID != backupID {
			return errors.New("unsupported or mismatched history backup")
		}
		if manifest.Status != historyStatusCommitted {
			return errors.New("only completed history backups can be restored")
		}
		if err := r.validateBackupManifest(manifest); err != nil {
			return err
		}
		sessions := append([]historySessionPlan(nil), manifest.SessionChanges...)
		for index := range sessions {
			sessions[index].Path = filepath.Join(r.CodexHome, filepath.FromSlash(sessions[index].RelativePath))
		}
		if err := r.validateInterruptedRollbackBaseline(manifest); err != nil {
			manifest.Status = historyStatusRollbackFailed
			manifest.StatusMessage = err.Error()
			_ = r.writeBackupManifest(manifest)
			return fmt.Errorf("history changed after the repair and was not overwritten: %w", err)
		}
		if err := r.rollback(manifest, sessions); err != nil {
			manifest.Status = historyStatusRollbackFailed
			manifest.StatusMessage = err.Error()
			_ = r.writeBackupManifest(manifest)
			return err
		}
		manifest.Status = historyStatusRolledBack
		manifest.StatusMessage = "restored after Codex failed to start"
		return r.writeBackupManifest(manifest)
	})
}

func (r *HistoryRepairer) ListBackups() ([]HistoryBackupInfo, error) {
	entries, err := os.ReadDir(r.BackupRoot)
	if errors.Is(err, fs.ErrNotExist) {
		return []HistoryBackupInfo{}, nil
	}
	if err != nil {
		return nil, err
	}
	items := make([]HistoryBackupInfo, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !validBackupID(entry.Name()) {
			continue
		}
		data, exists, _, err := readRegularFile(filepath.Join(r.BackupRoot, entry.Name(), "manifest.json"))
		if err != nil || !exists {
			continue
		}
		var manifest historyBackupManifest
		if err := json.Unmarshal(data, &manifest); err != nil || manifest.Version != historyBackupVersion ||
			manifest.ManagedBy != historyManagedBy || manifest.ID != entry.Name() || manifest.Status != historyStatusCommitted {
			continue
		}
		sanitized := 0
		for _, session := range manifest.SessionChanges {
			if session.Action == historySessionActionDropItem || session.Action == historySessionActionStripID {
				sanitized++
			}
		}
		items = append(items, HistoryBackupInfo{
			ID:               manifest.ID,
			CreatedAt:        manifest.CreatedAt,
			TargetProvider:   manifest.TargetProvider,
			SanitizedRecords: sanitized,
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt) })
	return items, nil
}

func (r *HistoryRepairer) DeleteBackup(backupID string) error {
	if !validBackupID(backupID) {
		return errors.New("invalid history backup ID")
	}
	return r.withLock(func() error {
		data, exists, _, err := readRegularFile(filepath.Join(r.BackupRoot, backupID, "manifest.json"))
		if err != nil || !exists {
			if err == nil {
				err = errors.New("history backup manifest does not exist")
			}
			return err
		}
		var manifest historyBackupManifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			return err
		}
		if manifest.Version != historyBackupVersion || manifest.ManagedBy != historyManagedBy || manifest.ID != backupID {
			return errors.New("unsupported or mismatched history backup")
		}
		if manifest.Status != historyStatusCommitted {
			return errors.New("only completed history backups can be deleted")
		}
		if err := r.validateBackupManifest(manifest); err != nil {
			return err
		}
		return removeBackupDirectory(r.BackupRoot, backupID)
	})
}

func (r *HistoryRepairer) validateBackupManifest(manifest historyBackupManifest) error {
	if filepath.Clean(manifest.CodexHome) != filepath.Clean(r.CodexHome) {
		return errors.New("history backup belongs to a different Codex home")
	}
	backupDir := filepath.Join(r.BackupRoot, manifest.ID)
	for _, session := range manifest.SessionChanges {
		if filepath.IsAbs(session.RelativePath) || !pathWithin(r.CodexHome, filepath.Join(r.CodexHome, filepath.FromSlash(session.RelativePath))) {
			return errors.New("history backup contains an invalid session path")
		}
	}
	for _, database := range manifest.DatabaseFiles {
		if !pathWithin(r.CodexHome, database.SourcePath) || filepath.IsAbs(database.BackupPath) || !pathWithin(backupDir, filepath.Join(backupDir, filepath.FromSlash(database.BackupPath))) {
			return errors.New("history backup contains an invalid database path")
		}
		if database.Existed && len(database.SHA256) != sha256.Size*2 {
			return errors.New("history backup contains an invalid database checksum")
		}
	}
	return nil
}

func pathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator))
}

func (r *HistoryRepairer) verifyPlan(plan historyRepairPlan) error {
	rollouts, err := discoverRolloutFiles(r.CodexHome)
	if err != nil {
		return err
	}
	if len(rollouts) != plan.ScannedFiles || historyPathSetSHA256(r.CodexHome, rollouts) != plan.RolloutFilesSHA256 {
		return errors.New("rollout file set changed during history repair")
	}
	for _, sessions := range groupHistorySessionPlans(plan.Sessions) {
		if err := verifyHistorySessionPlans(sessions, plan.TargetProvider); err != nil {
			return err
		}
	}
	for _, expected := range plan.Databases {
		actual, ok, err := inspectHistoryDatabase(r.CodexHome, expected.Path, plan.SourceProviders)
		if err != nil {
			return err
		}
		if !ok || actual.ThreadCount != expected.ThreadCount || actual.ThreadIDsSHA256 != expected.ThreadIDsSHA256 || actual.ThreadContentSHA256 != expected.ThreadContentSHA256 {
			return fmt.Errorf("thread identity set changed during repair: %s", expected.RelativePath)
		}
		if actual.MismatchedRows != 0 {
			return fmt.Errorf("database provider verification failed: %s", expected.RelativePath)
		}
	}
	if plan.TargetProvider != legacyCodexProviderID {
		remaining, err := r.discoverSourceProviders(plan.TargetProvider)
		if err != nil {
			return err
		}
		if len(remaining) > 0 {
			return fmt.Errorf("history provider verification found %d unsynchronized provider markers", len(remaining))
		}
	}
	return nil
}

func verifySessionRepairPlan(plan historySessionPlan, targetProvider string) error {
	raw, err := sessionRecordAtLine(plan.Path, plan.LineIndex)
	if err != nil {
		return err
	}
	return verifySessionRepairRecord(plan, raw, targetProvider)
}

func verifyHistorySessionPlans(plans []historySessionPlan, targetProvider string) error {
	if len(plans) == 0 {
		return nil
	}
	path := plans[0].Path
	byLine := make(map[int]historySessionPlan, len(plans))
	for _, plan := range plans {
		if plan.Path != path {
			return errors.New("session repair plans contain multiple files")
		}
		if _, duplicate := byLine[plan.LineIndex]; duplicate {
			return fmt.Errorf("session repair plans contain duplicate line %d: %s", plan.LineIndex, plan.RelativePath)
		}
		byLine[plan.LineIndex] = plan
	}
	verified := 0
	err := scanHistoryLines(path, func(lineIndex int, raw []byte) error {
		plan, ok := byLine[lineIndex]
		if !ok {
			return nil
		}
		if err := verifySessionRepairRecord(plan, raw, targetProvider); err != nil {
			return err
		}
		verified++
		return nil
	})
	if err != nil {
		return err
	}
	if verified != len(plans) {
		return fmt.Errorf("session repair verification lost %d records: %s", len(plans)-verified, plans[0].RelativePath)
	}
	return nil
}

func verifySessionRepairRecord(plan historySessionPlan, raw []byte, targetProvider string) error {
	switch plan.Action {
	case historySessionActionProvider:
		var record map[string]any
		if err := json.Unmarshal(raw, &record); err != nil {
			return fmt.Errorf("session provider verification read invalid record: %s", plan.RelativePath)
		}
		payload, _ := record["payload"].(map[string]any)
		provider, _ := payload["model_provider"].(string)
		if provider != targetProvider {
			return fmt.Errorf("session provider verification failed: %s", plan.RelativePath)
		}
	case historySessionActionDropItem:
		if !bytes.Equal(bytes.TrimSpace(raw), historySanitizedRecord) {
			return fmt.Errorf("session compatibility record was not removed: %s", plan.RelativePath)
		}
	case historySessionActionStripID:
		var record map[string]any
		if err := json.Unmarshal(raw, &record); err != nil {
			return fmt.Errorf("session message ID verification read invalid record: %s", plan.RelativePath)
		}
		payload, _ := record["payload"].(map[string]any)
		if payload["type"] != "message" {
			return fmt.Errorf("session message ID verification found wrong record: %s", plan.RelativePath)
		}
		if id, hasID := payload["id"]; hasID {
			idString, ok := id.(string)
			if !ok || !strings.HasPrefix(strings.TrimSpace(idString), "msg_") {
				return fmt.Errorf("session message ID verification failed: %s", plan.RelativePath)
			}
		}
	default:
		return fmt.Errorf("unsupported session repair action: %s", plan.Action)
	}
	return nil
}

func (r *HistoryRepairer) rollback(manifest historyBackupManifest, sessions []historySessionPlan) error {
	if err := r.verifyDatabaseBackups(manifest); err != nil {
		return err
	}
	var failures []string
	groups := groupHistorySessionPlans(sessions)
	for index := len(groups) - 1; index >= 0; index-- {
		if err := r.requireCodexHistoryWriteSafety(); err != nil {
			return err
		}
		if err := replaceHistorySessionLines(groups[index], true); err != nil {
			failures = append(failures, err.Error())
		}
	}
	backupDir := filepath.Join(r.BackupRoot, manifest.ID)
	for _, item := range manifest.DatabaseFiles {
		if err := r.requireCodexHistoryWriteSafety(); err != nil {
			return err
		}
		if item.Existed {
			for _, sidecar := range []string{item.SourcePath + "-wal", item.SourcePath + "-shm"} {
				if err := os.Remove(sidecar); err != nil && !errors.Is(err, fs.ErrNotExist) {
					failures = append(failures, err.Error())
				}
			}
			if err := copyHistoryRegularFile(filepath.Join(backupDir, item.BackupPath), item.SourcePath); err != nil {
				failures = append(failures, err.Error())
			} else if err := verifyFileSHA256(item.SourcePath, item.SHA256); err != nil {
				failures = append(failures, err.Error())
			}
			continue
		}
		if err := os.Remove(item.SourcePath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			failures = append(failures, err.Error())
		}
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	return nil
}

func (r *HistoryRepairer) verifyDatabaseBackups(manifest historyBackupManifest) error {
	backupDir := filepath.Join(r.BackupRoot, manifest.ID)
	for _, item := range manifest.DatabaseFiles {
		if !item.Existed {
			continue
		}
		if err := verifyFileSHA256(filepath.Join(backupDir, filepath.FromSlash(item.BackupPath)), item.SHA256); err != nil {
			return fmt.Errorf("history database backup verification failed: %w", err)
		}
	}
	return nil
}

func (r *HistoryRepairer) withLock(fn func() error) error {
	release, err := AcquireOperationLock(r.LockPath)
	if err != nil {
		return err
	}
	defer release()
	return fn()
}

func readCurrentProvider(configPath string) (string, error) {
	data, exists, _, err := readRegularFile(configPath)
	if err != nil {
		return "", fmt.Errorf("read Codex config: %w", err)
	}
	if !exists {
		return "openai", nil
	}
	var root map[string]any
	if err := toml.Unmarshal(data, &root); err != nil {
		return "", fmt.Errorf("read model provider from Codex config: %w", err)
	}
	provider, _ := root["model_provider"].(string)
	provider = strings.TrimSpace(provider)
	if provider == "" {
		provider = "openai"
	}
	if !validHistoryProviderID(provider) {
		return "", errors.New("Codex config contains an invalid model provider")
	}
	return provider, nil
}

func validHistoryProviderID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if !(char >= 'a' && char <= 'z') && !(char >= 'A' && char <= 'Z') && !(char >= '0' && char <= '9') && char != '_' && char != '-' && char != '.' {
			return false
		}
	}
	return true
}

func (r *HistoryRepairer) discoverSourceProviders(target string) ([]string, error) {
	providers := map[string]struct{}{}
	add := func(value string) error {
		if value == target {
			return nil
		}
		if len(value) > 1024 {
			return errors.New("conversation history contains an unexpectedly long model provider")
		}
		providers[value] = struct{}{}
		if len(providers) > 900 {
			return errors.New("conversation history contains too many distinct model providers")
		}
		return nil
	}

	rollouts, err := discoverRolloutFiles(r.CodexHome)
	if err != nil {
		return nil, err
	}
	for _, path := range rollouts {
		if err := scanHistoryLines(path, func(_ int, raw []byte) error {
			var record map[string]any
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.UseNumber()
			if decoder.Decode(&record) != nil || record["type"] != "session_meta" {
				return nil
			}
			payload, ok := record["payload"].(map[string]any)
			if !ok {
				return fmt.Errorf("session metadata payload is invalid: %s", path)
			}
			provider, _ := payload["model_provider"].(string)
			return add(provider)
		}); err != nil {
			return nil, err
		}
	}

	databases, err := discoverHistoryDatabases(r.CodexHome)
	if err != nil {
		return nil, err
	}
	for _, path := range databases {
		database, err := openHistoryDatabaseReadOnly(path)
		if err != nil {
			return nil, fmt.Errorf("open Codex database %s: %w", path, err)
		}
		hasThreads, tableErr := databaseHasTable(database, "threads")
		if tableErr != nil || !hasThreads {
			_ = database.Close()
			if tableErr != nil {
				return nil, tableErr
			}
			continue
		}
		columns, columnsErr := databaseColumns(database, "threads")
		if columnsErr != nil {
			_ = database.Close()
			return nil, columnsErr
		}
		if _, ok := columns["model_provider"]; !ok {
			_ = database.Close()
			continue
		}
		rows, queryErr := database.Query("SELECT DISTINCT COALESCE(model_provider, '') FROM threads")
		if queryErr != nil {
			_ = database.Close()
			return nil, queryErr
		}
		for rows.Next() {
			var provider string
			if err := rows.Scan(&provider); err != nil {
				_ = rows.Close()
				_ = database.Close()
				return nil, err
			}
			if err := add(provider); err != nil {
				_ = rows.Close()
				_ = database.Close()
				return nil, err
			}
		}
		rowsErr := rows.Err()
		_ = rows.Close()
		closeErr := database.Close()
		if rowsErr != nil {
			return nil, rowsErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
	}

	result := make([]string, 0, len(providers))
	for provider := range providers {
		result = append(result, provider)
	}
	sort.Strings(result)
	return result, nil
}

func discoverRolloutFiles(codexHome string) ([]string, error) {
	var files []string
	for _, directory := range []string{"sessions", "archived_sessions"} {
		root := filepath.Join(codexHome, directory)
		if _, err := os.Stat(root); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.Type()&os.ModeSymlink != 0 {
				if !entry.IsDir() && strings.HasPrefix(entry.Name(), "rollout-") && strings.HasSuffix(entry.Name(), ".jsonl") {
					return fmt.Errorf("refusing to repair symbolic-link rollout file: %s", path)
				}
				return nil
			}
			if entry.IsDir() || !strings.HasPrefix(entry.Name(), "rollout-") || !strings.HasSuffix(entry.Name(), ".jsonl") {
				return nil
			}
			files = append(files, path)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(files)
	return files, nil
}

func inspectSessionMetadata(codexHome, path, targetProvider string, sourceProviders []string) (historySessionPlan, bool, error) {
	var plan historySessionPlan
	info, err := os.Lstat(path)
	if err != nil {
		return plan, false, err
	}
	if !info.Mode().IsRegular() {
		return plan, false, fmt.Errorf("rollout is not a regular file: %s", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return plan, false, err
	}
	defer file.Close()

	reader := bufio.NewReaderSize(file, 64*1024)
	readBytes := 0
	for lineIndex := 0; lineIndex < historyMetadataLines && readBytes <= historyMetadataMax; lineIndex++ {
		line, readErr := reader.ReadBytes('\n')
		readBytes += len(line)
		if len(line) > 0 {
			raw, _ := splitLineEnding(line)
			if len(bytes.TrimSpace(raw)) > 0 {
				var record map[string]any
				decoder := json.NewDecoder(bytes.NewReader(raw))
				decoder.UseNumber()
				if decoder.Decode(&record) == nil && record["type"] == "session_meta" {
					payload, ok := record["payload"].(map[string]any)
					if !ok {
						return plan, false, fmt.Errorf("session metadata payload is invalid: %s", path)
					}
					current, _ := payload["model_provider"].(string)
					if current == targetProvider || !containsHistoryProvider(sourceProviders, current) {
						return plan, false, nil
					}
					payload["model_provider"] = targetProvider
					updated, err := json.Marshal(record)
					if err != nil {
						return plan, false, err
					}
					relative, err := filepath.Rel(codexHome, path)
					if err != nil || strings.HasPrefix(relative, "..") {
						return plan, false, fmt.Errorf("rollout is outside Codex home: %s", path)
					}
					return historySessionPlan{
						Path:         path,
						RelativePath: filepath.ToSlash(relative),
						LineIndex:    lineIndex,
						OriginalLine: append([]byte(nil), raw...),
						UpdatedLine:  updated,
						Mode:         uint32(info.Mode().Perm()),
						ModifiedAt:   info.ModTime(),
					}, true, nil
				}
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return plan, false, readErr
		}
	}
	return plan, false, nil
}

var errStopHistoryLineScan = errors.New("stop history line scan")

func inspectAllSessionMetadata(codexHome, path, targetProvider string, sourceProviders []string, repairCompatibility bool) ([]historySessionPlan, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("rollout is not a regular file: %s", path)
	}
	relative, err := filepath.Rel(codexHome, path)
	if err != nil || strings.HasPrefix(relative, "..") {
		return nil, fmt.Errorf("rollout is outside Codex home: %s", path)
	}
	var plans []historySessionPlan
	err = scanHistoryLines(path, func(lineIndex int, raw []byte) error {
		var record map[string]any
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if decoder.Decode(&record) != nil {
			return nil
		}

		if record["type"] == "session_meta" {
			payload, ok := record["payload"].(map[string]any)
			if !ok {
				return fmt.Errorf("session metadata payload is invalid: %s", path)
			}
			current, _ := payload["model_provider"].(string)
			if current != targetProvider && containsHistoryProvider(sourceProviders, current) {
				payload["model_provider"] = targetProvider
				updated, err := json.Marshal(record)
				if err != nil {
					return err
				}
				plans = append(plans, newHistorySessionPlan(path, relative, lineIndex, raw, updated, info, historySessionActionProvider))
			}
			return nil
		}

		if !repairCompatibility || !shouldSanitizeSessionReplay(targetProvider) {
			return nil
		}
		updated, action, changed, err := sanitizeSessionReplayRecord(record)
		if err != nil {
			return fmt.Errorf("sanitize session replay record: %w", err)
		}
		if changed {
			plans = append(plans, newHistorySessionPlan(path, relative, lineIndex, raw, updated, info, action))
		}
		return nil
	})
	return plans, err
}

func shouldSanitizeSessionReplay(targetProvider string) bool {
	// OpenAI's first-party provider can verify its own continuation payloads.
	// Any configured external provider (including a manually entered compatible
	// API) must instead replay portable message/tool history without those
	// provider-bound encrypted records.
	return !strings.EqualFold(strings.TrimSpace(targetProvider), "openai")
}

func newHistorySessionPlan(path, relative string, lineIndex int, original, updated []byte, info fs.FileInfo, action string) historySessionPlan {
	return historySessionPlan{
		Path:         path,
		RelativePath: filepath.ToSlash(relative),
		LineIndex:    lineIndex,
		OriginalLine: append([]byte(nil), original...),
		UpdatedLine:  append([]byte(nil), updated...),
		Mode:         uint32(info.Mode().Perm()),
		ModifiedAt:   info.ModTime(),
		Action:       action,
	}
}

// sanitizeSessionReplayRecord removes only protocol-internal continuation
// records that a third-party Responses endpoint cannot verify. The visible
// user/assistant messages and all tool output records remain intact.
func sanitizeSessionReplayRecord(record map[string]any) (updated []byte, action string, changed bool, err error) {
	if record["type"] != "response_item" {
		return nil, "", false, nil
	}
	payload, ok := record["payload"].(map[string]any)
	if !ok {
		return nil, "", false, nil
	}

	itemType, _ := payload["type"].(string)
	switch strings.TrimSpace(itemType) {
	case "reasoning", "compaction", "compaction_summary":
		if _, hasEncryptedContent := payload["encrypted_content"]; hasEncryptedContent {
			return append([]byte(nil), historySanitizedRecord...), historySessionActionDropItem, true, nil
		}
	case "message":
		if rawID, hasID := payload["id"]; hasID {
			id, validStringID := rawID.(string)
			if !validStringID || !strings.HasPrefix(strings.TrimSpace(id), "msg_") {
				delete(payload, "id")
				updated, err = json.Marshal(record)
				if err != nil {
					return nil, "", false, err
				}
				return updated, historySessionActionStripID, true, nil
			}
		}
	}
	return nil, "", false, nil
}

func sessionProviderAtLine(path string, targetLine int) (string, error) {
	provider := ""
	found := false
	err := scanHistoryLines(path, func(lineIndex int, raw []byte) error {
		if lineIndex != targetLine {
			return nil
		}
		var record map[string]any
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&record); err != nil || record["type"] != "session_meta" {
			return fmt.Errorf("session metadata line is invalid: %s", path)
		}
		payload, ok := record["payload"].(map[string]any)
		if !ok {
			return fmt.Errorf("session metadata payload is invalid: %s", path)
		}
		provider, _ = payload["model_provider"].(string)
		found = true
		return errStopHistoryLineScan
	})
	if err != nil {
		return "", err
	}
	if !found {
		return "", fmt.Errorf("session metadata line disappeared: %s", path)
	}
	return provider, nil
}

func sessionRecordAtLine(path string, targetLine int) ([]byte, error) {
	var record []byte
	found := false
	err := scanHistoryLines(path, func(lineIndex int, raw []byte) error {
		if lineIndex != targetLine {
			return nil
		}
		record = append([]byte(nil), raw...)
		found = true
		return errStopHistoryLineScan
	})
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("session record disappeared: %s", path)
	}
	return record, nil
}

func scanHistoryLines(path string, visit func(lineIndex int, raw []byte) error) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, 64*1024)
	lineIndex := 0
	line := make([]byte, 0, 64*1024)
	oversized := false
	for {
		fragment, readErr := reader.ReadSlice('\n')
		if !oversized {
			if len(line)+len(fragment) <= historyMetadataMax {
				line = append(line, fragment...)
			} else {
				line = line[:0]
				oversized = true
			}
		}
		if errors.Is(readErr, bufio.ErrBufferFull) {
			continue
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return readErr
		}
		if !oversized && len(line) > 0 {
			raw, _ := splitLineEnding(line)
			if err := visit(lineIndex, raw); err != nil {
				if errors.Is(err, errStopHistoryLineScan) {
					return nil
				}
				return err
			}
		}
		lineIndex++
		line = line[:0]
		oversized = false
		if errors.Is(readErr, io.EOF) {
			return nil
		}
	}
}

func uniqueSessionFileCount(plans []historySessionPlan) int {
	paths := map[string]struct{}{}
	for _, plan := range plans {
		paths[plan.Path] = struct{}{}
	}
	return len(paths)
}

func containsHistoryProvider(values []string, candidate string) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func splitLineEnding(line []byte) ([]byte, []byte) {
	if bytes.HasSuffix(line, []byte("\r\n")) {
		return line[:len(line)-2], line[len(line)-2:]
	}
	if bytes.HasSuffix(line, []byte("\n")) {
		return line[:len(line)-1], line[len(line)-1:]
	}
	return line, nil
}

func replaceSessionMetadataLine(plan historySessionPlan, expected, replacement []byte) (err error) {
	plan.OriginalLine = append([]byte(nil), expected...)
	plan.UpdatedLine = append([]byte(nil), replacement...)
	return replaceHistorySessionLines([]historySessionPlan{plan}, false)
}

func groupHistorySessionPlans(plans []historySessionPlan) [][]historySessionPlan {
	if len(plans) == 0 {
		return nil
	}
	sorted := append([]historySessionPlan(nil), plans...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Path == sorted[j].Path {
			return sorted[i].LineIndex < sorted[j].LineIndex
		}
		return sorted[i].Path < sorted[j].Path
	})
	groups := make([][]historySessionPlan, 0, len(sorted))
	for _, plan := range sorted {
		if len(groups) == 0 || groups[len(groups)-1][0].Path != plan.Path {
			groups = append(groups, []historySessionPlan{plan})
			continue
		}
		groups[len(groups)-1] = append(groups[len(groups)-1], plan)
	}
	return groups
}

// replaceHistorySessionLines applies all planned edits for one rollout in a
// single atomic rewrite. Compatibility repairs often touch many reasoning
// records in a long conversation; rewriting the entire file per record makes
// a correct repair look stalled on customer machines.
func replaceHistorySessionLines(plans []historySessionPlan, reverse bool) (err error) {
	if len(plans) == 0 {
		return nil
	}
	path := plans[0].Path
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("rollout changed into a non-regular file: %s", path)
	}

	byLine := make(map[int]historySessionPlan, len(plans))
	for _, plan := range plans {
		if plan.Path != path {
			return errors.New("session repair plans contain multiple files")
		}
		if _, duplicate := byLine[plan.LineIndex]; duplicate {
			return fmt.Errorf("session repair plans contain duplicate line %d: %s", plan.LineIndex, plan.RelativePath)
		}
		byLine[plan.LineIndex] = plan
	}

	input, err := os.Open(path)
	if err != nil {
		return err
	}
	defer input.Close()
	tmp, err := os.CreateTemp(filepath.Dir(path), ".xiass-history-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}()
	if err := tmp.Chmod(fs.FileMode(plans[0].Mode)); err != nil {
		return err
	}

	reader := bufio.NewReaderSize(input, 64*1024)
	replaced := 0
	lineIndex := 0
	for {
		plan, needsReplacement := byLine[lineIndex]
		if !needsReplacement {
			var readErr error
			for {
				fragment, nextErr := reader.ReadSlice('\n')
				if len(fragment) > 0 {
					if _, err := tmp.Write(fragment); err != nil {
						return err
					}
				}
				if errors.Is(nextErr, bufio.ErrBufferFull) {
					continue
				}
				readErr = nextErr
				break
			}
			if readErr != nil && !errors.Is(readErr, io.EOF) {
				return readErr
			}
			lineIndex++
			if errors.Is(readErr, io.EOF) {
				break
			}
			continue
		}

		line := make([]byte, 0, 64*1024)
		var readErr error
		for {
			fragment, nextErr := reader.ReadSlice('\n')
			if len(line)+len(fragment) > historyMetadataMax {
				return fmt.Errorf("session record line is unexpectedly large: %s", plans[0].RelativePath)
			}
			line = append(line, fragment...)
			if errors.Is(nextErr, bufio.ErrBufferFull) {
				continue
			}
			readErr = nextErr
			break
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return readErr
		}
		if len(line) == 0 && errors.Is(readErr, io.EOF) {
			break
		}
		raw, ending := splitLineEnding(line)
		{
			expected, replacement := plan.OriginalLine, plan.UpdatedLine
			if reverse {
				expected, replacement = replacement, expected
			}
			if !bytes.Equal(raw, replacement) {
				if !bytes.Equal(raw, expected) {
					return fmt.Errorf("session changed while repair was running: %s", plan.RelativePath)
				}
				if _, err := tmp.Write(replacement); err != nil {
					return err
				}
			} else if _, err := tmp.Write(raw); err != nil {
				return err
			}
			replaced++
			if _, err := tmp.Write(ending); err != nil {
				return err
			}
		}
		lineIndex++
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	if replaced != len(plans) {
		return fmt.Errorf("session repair records disappeared: %s", plans[0].RelativePath)
	}
	if err := input.Close(); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := replaceFileAtomic(tmpPath, path); err != nil {
		return fmt.Errorf("replace session atomically: %w", err)
	}
	_ = os.Chtimes(path, plans[0].ModifiedAt, plans[0].ModifiedAt)
	return nil
}

func discoverHistoryDatabases(codexHome string) ([]string, error) {
	candidates := []string{filepath.Join(codexHome, "state_5.sqlite")}
	sqliteDir := filepath.Join(codexHome, "sqlite")
	entries, err := os.ReadDir(sqliteDir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		extension := strings.ToLower(filepath.Ext(entry.Name()))
		if extension == ".db" || extension == ".sqlite" || extension == ".sqlite3" {
			candidates = append(candidates, filepath.Join(sqliteDir, entry.Name()))
		}
	}
	sort.Strings(candidates)
	seen := map[string]struct{}{}
	result := make([]string, 0, len(candidates))
	for _, path := range candidates {
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, err
		}
		if !isRequiredHistoryDatabase(path) {
			relevant, err := optionalHistoryDatabaseHasProviderThreads(path)
			if err != nil || !relevant {
				continue
			}
		}
		result = append(result, path)
	}
	return result, nil
}

func isRequiredHistoryDatabase(path string) bool {
	return strings.HasPrefix(strings.ToLower(filepath.Base(path)), "state_")
}

func optionalHistoryDatabaseHasProviderThreads(path string) (bool, error) {
	database, err := openHistoryDatabaseReadOnly(path)
	if err != nil {
		return false, err
	}
	defer database.Close()
	hasThreads, err := databaseHasTable(database, "threads")
	if err != nil || !hasThreads {
		return false, err
	}
	columns, err := databaseColumns(database, "threads")
	if err != nil {
		return false, err
	}
	_, relevant := columns["model_provider"]
	return relevant, nil
}

func inspectHistoryDatabase(codexHome, path string, sourceProviders []string) (historyDatabasePlan, bool, error) {
	var plan historyDatabasePlan
	info, err := os.Lstat(path)
	if err != nil {
		return plan, false, err
	}
	if !info.Mode().IsRegular() {
		return plan, false, fmt.Errorf("Codex database is not a regular file: %s", path)
	}
	database, err := openHistoryDatabaseReadOnly(path)
	if err != nil {
		return plan, false, fmt.Errorf("open Codex database %s: %w", path, err)
	}
	defer database.Close()

	hasThreads, err := databaseHasTable(database, "threads")
	if err != nil || !hasThreads {
		return plan, false, err
	}
	columns, err := databaseColumns(database, "threads")
	if err != nil {
		return plan, false, err
	}
	if _, ok := columns["model_provider"]; !ok {
		return plan, false, nil
	}
	if err := checkHistoryDatabase(database); err != nil {
		return plan, false, fmt.Errorf("Codex database integrity check failed for %s: %w", path, err)
	}
	plan.ThreadCount, plan.ThreadIDsSHA256, plan.ThreadContentSHA256, err = databaseThreadIdentity(database)
	if err != nil {
		return plan, false, err
	}
	if len(sourceProviders) > 0 {
		where, arguments := historyProviderWhereClause("model_provider", sourceProviders)
		if err := database.QueryRow("SELECT COUNT(*) FROM threads WHERE "+where, arguments...).Scan(&plan.MismatchedRows); err != nil {
			return plan, false, err
		}
	}
	relative, err := filepath.Rel(codexHome, path)
	if err != nil || strings.HasPrefix(relative, "..") {
		return plan, false, fmt.Errorf("Codex database is outside Codex home: %s", path)
	}
	plan.Path = path
	plan.RelativePath = filepath.ToSlash(relative)
	return plan, true, nil
}

func updateDatabaseProvider(plan historyDatabasePlan, targetProvider string, sourceProviders []string) (int64, error) {
	if plan.MismatchedRows == 0 {
		return 0, nil
	}
	database, err := openHistoryDatabase(plan.Path)
	if err != nil {
		return 0, err
	}
	defer database.Close()
	transaction, err := database.Begin()
	if err != nil {
		return 0, err
	}
	where, sourceArguments := historyProviderWhereClause("model_provider", sourceProviders)
	arguments := make([]any, 0, len(sourceArguments)+1)
	arguments = append(arguments, targetProvider)
	arguments = append(arguments, sourceArguments...)
	result, err := transaction.Exec("UPDATE threads SET model_provider = ? WHERE "+where, arguments...)
	if err != nil {
		_ = transaction.Rollback()
		return 0, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		_ = transaction.Rollback()
		return 0, err
	}
	if err := transaction.Commit(); err != nil {
		return 0, err
	}
	if rows != plan.MismatchedRows {
		return rows, fmt.Errorf("database changed concurrently: updated %d rows, expected %d", rows, plan.MismatchedRows)
	}
	return rows, nil
}

func historyProviderWhereClause(column string, providers []string) (string, []any) {
	placeholders := make([]string, len(providers))
	arguments := make([]any, len(providers))
	for index, provider := range providers {
		placeholders[index] = "?"
		arguments[index] = provider
	}
	return "COALESCE(" + column + ", '') IN (" + strings.Join(placeholders, ",") + ")", arguments
}

func databaseThreadIdentity(database *sql.DB) (int64, string, string, error) {
	columns, err := orderedDatabaseColumns(database, "threads", "model_provider")
	if err != nil {
		return 0, "", "", err
	}
	selected := make([]string, len(columns))
	idIndex := -1
	for index, column := range columns {
		selected[index] = quoteSQLiteIdentifier(column)
		if column == "id" {
			idIndex = index
		}
	}
	if idIndex < 0 {
		return 0, "", "", errors.New("threads table has no id column")
	}
	rows, err := database.Query("SELECT " + strings.Join(selected, ",") + " FROM threads ORDER BY id")
	if err != nil {
		return 0, "", "", err
	}
	defer rows.Close()
	idHash := sha256.New()
	contentHash := sha256.New()
	_, _ = io.WriteString(contentHash, strings.Join(columns, "\x00"))
	_, _ = io.WriteString(contentHash, "\n")
	var count int64
	for rows.Next() {
		values := make([]any, len(columns))
		destinations := make([]any, len(columns))
		for index := range values {
			destinations[index] = &values[index]
		}
		if err := rows.Scan(destinations...); err != nil {
			return 0, "", "", err
		}
		_, _ = io.WriteString(idHash, fmt.Sprint(values[idIndex]))
		_, _ = io.WriteString(idHash, "\n")
		encoded, err := json.Marshal(values)
		if err != nil {
			return 0, "", "", err
		}
		_, _ = contentHash.Write(encoded)
		_, _ = io.WriteString(contentHash, "\n")
		count++
	}
	if err := rows.Err(); err != nil {
		return 0, "", "", err
	}
	return count, hex.EncodeToString(idHash.Sum(nil)), hex.EncodeToString(contentHash.Sum(nil)), nil
}

func orderedDatabaseColumns(database *sql.DB, table string, excluded ...string) ([]string, error) {
	excludedSet := map[string]struct{}{}
	for _, column := range excluded {
		excludedSet[column] = struct{}{}
	}
	rows, err := database.Query("PRAGMA table_info(" + quoteSQLiteIdentifier(table) + ")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var cid int
		var name, kind string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, err
		}
		if _, skip := excludedSet[name]; !skip {
			columns = append(columns, name)
		}
	}
	return columns, rows.Err()
}

func historyPathSetSHA256(codexHome string, paths []string) string {
	hash := sha256.New()
	for _, path := range paths {
		relative, err := filepath.Rel(codexHome, path)
		if err != nil {
			relative = path
		}
		_, _ = io.WriteString(hash, filepath.ToSlash(relative))
		_, _ = io.WriteString(hash, "\n")
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func openHistoryDatabase(path string) (*sql.DB, error) {
	return openHistoryDatabaseDSN(path)
}

func openHistoryDatabaseReadOnly(path string) (*sql.DB, error) {
	location := sqliteFileURL(path)
	query := location.Query()
	query.Set("mode", "ro")
	location.RawQuery = query.Encode()
	database, err := openHistoryDatabaseDSN(location.String())
	if err == nil {
		return database, nil
	}
	if !allowImmutableHistoryReadForTests {
		return nil, err
	}
	query.Set("immutable", "1")
	location.RawQuery = query.Encode()
	fallback, fallbackErr := openHistoryDatabaseDSN(location.String())
	if fallbackErr != nil {
		return nil, fmt.Errorf("read-only open failed: %v; immutable fallback failed: %w", err, fallbackErr)
	}
	return fallback, nil
}

func sqliteFileURL(path string) url.URL {
	if len(path) >= 3 && ((path[0] >= 'A' && path[0] <= 'Z') || (path[0] >= 'a' && path[0] <= 'z')) && path[1] == ':' && (path[2] == '\\' || path[2] == '/') {
		normalized := strings.ReplaceAll(path, `\`, "/")
		return url.URL{Scheme: "file", Path: "/" + normalized}
	}
	if strings.HasPrefix(path, `\\`) {
		normalized := strings.TrimPrefix(strings.ReplaceAll(path, `\`, "/"), "//")
		parts := strings.SplitN(normalized, "/", 2)
		if len(parts) == 2 {
			return url.URL{Scheme: "file", Host: parts[0], Path: "/" + parts[1]}
		}
	}
	return url.URL{Scheme: "file", Path: path}
}

func openHistoryDatabaseDSN(dsn string) (*sql.DB, error) {
	database, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	database.SetMaxOpenConns(1)
	if err := database.Ping(); err != nil {
		_ = database.Close()
		return nil, err
	}
	if _, err := database.Exec("PRAGMA busy_timeout = 5000"); err != nil {
		_ = database.Close()
		return nil, err
	}
	return database, nil
}

func databaseHasTable(database *sql.DB, table string) (bool, error) {
	var exists int
	err := database.QueryRow("SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = ? LIMIT 1", table).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func databaseColumns(database *sql.DB, table string) (map[string]struct{}, error) {
	rows, err := database.Query("PRAGMA table_info(" + quoteSQLiteIdentifier(table) + ")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := map[string]struct{}{}
	for rows.Next() {
		var cid int
		var name, kind string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, err
		}
		columns[name] = struct{}{}
	}
	return columns, rows.Err()
}

func quoteSQLiteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func checkHistoryDatabase(database *sql.DB) error {
	rows, err := database.Query("PRAGMA quick_check")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		if err := rows.Scan(&status); err != nil {
			return err
		}
		if status != "ok" {
			return errors.New(status)
		}
	}
	return rows.Err()
}

func backupHistoryDatabase(codexHome, backupDir string, plan historyDatabasePlan) (historyBackupFile, error) {
	relative, err := filepath.Rel(codexHome, plan.Path)
	if err != nil || strings.HasPrefix(relative, "..") {
		return historyBackupFile{}, fmt.Errorf("database file is outside Codex home: %s", plan.Path)
	}
	item := historyBackupFile{
		SourcePath: plan.Path,
		BackupPath: filepath.ToSlash(filepath.Join("database", relative)),
		Existed:    true,
	}
	info, err := os.Lstat(plan.Path)
	if err != nil {
		return item, err
	}
	if !info.Mode().IsRegular() {
		return item, fmt.Errorf("database backup source is not a regular file: %s", plan.Path)
	}
	item.Mode = uint32(info.Mode().Perm())
	target := filepath.Join(backupDir, filepath.FromSlash(item.BackupPath))
	if err := createSQLiteSnapshot(plan.Path, target, info.Mode().Perm()); err != nil {
		return item, err
	}
	backupDatabase, err := openHistoryDatabaseReadOnly(target)
	if err != nil {
		return item, err
	}
	defer backupDatabase.Close()
	if err := checkHistoryDatabase(backupDatabase); err != nil {
		return item, fmt.Errorf("history database snapshot integrity check failed: %w", err)
	}
	count, identity, contentIdentity, err := databaseThreadIdentity(backupDatabase)
	if err != nil {
		return item, err
	}
	if count != plan.ThreadCount || identity != plan.ThreadIDsSHA256 || contentIdentity != plan.ThreadContentSHA256 {
		return item, errors.New("history database snapshot does not match the source thread set")
	}
	item.SHA256, err = fileSHA256(target)
	if err != nil {
		return item, err
	}
	return item, nil
}

func createSQLiteSnapshot(source, destination string, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	if err := os.Remove(destination); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	database, err := openHistoryDatabaseReadOnly(source)
	if err != nil {
		return err
	}
	defer database.Close()
	if _, err := database.Exec("VACUUM INTO ?", destination); err != nil {
		return fmt.Errorf("create consistent SQLite snapshot: %w", err)
	}
	return os.Chmod(destination, mode)
}

func copyHistoryRegularFile(source, destination string) (err error) {
	inputInfo, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !inputInfo.Mode().IsRegular() {
		return fmt.Errorf("backup source is not a regular file: %s", source)
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(destination), ".xiass-copy-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}()
	if err := tmp.Chmod(inputInfo.Mode().Perm()); err != nil {
		return err
	}
	hash := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, hash), input); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := replaceFileAtomic(tmpPath, destination); err != nil {
		return err
	}
	written, err := os.Open(destination)
	if err != nil {
		return err
	}
	writtenHash := sha256.New()
	_, copyErr := io.Copy(writtenHash, written)
	closeErr := written.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if hex.EncodeToString(hash.Sum(nil)) != hex.EncodeToString(writtenHash.Sum(nil)) {
		return fmt.Errorf("backup checksum mismatch for %s", source)
	}
	return nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func verifyFileSHA256(path, expected string) error {
	actual, err := fileSHA256(path)
	if err != nil {
		return err
	}
	if actual != expected {
		return fmt.Errorf("checksum mismatch for %s", path)
	}
	return nil
}
