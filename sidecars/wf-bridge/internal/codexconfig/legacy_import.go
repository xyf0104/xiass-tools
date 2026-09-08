package codexconfig

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	legacyHelperDataDirectory       = "xiass-helper"
	legacyConfigBackupDirectory     = "backups"
	legacyHistoryBackupDirectory    = "history-backups"
	legacyImportSource              = "xiass-codex-helper"
	legacyHistoryManagedBy          = "XIASS Codex Helper history repair"
	legacyConfigManifestMaxBytes    = int64(1 << 20)
	legacyConfigFileMaxBytes        = int64(8 << 20)
	legacyHistoryManifestMaxBytes   = int64(32 << 20)
	legacySnapshotFileMaxBytes      = int64(64 << 20)
	legacyHistoryDatabaseMaxBytes   = int64(8 << 30)
	legacyHistoryAggregateMaxBytes  = int64(16 << 30)
	legacyMaxHistoryProviders       = 128
	legacyMaxHistorySessionChanges  = 100000
	legacyMaxHistoryDatabaseFiles   = 64
	legacyMaxHistoryLineIndex       = 10000000
	legacyMaxPortableRelativeLength = 1024
)

var legacyBackupIDPattern = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}\.[0-9]{9}Z-[a-f0-9]{8}$`)

// legacyConfigBackupManifest intentionally models only the first-party
// XIASS Codex Helper schema. It must not be widened with current-only fields:
// strict JSON decoding is part of the format boundary.
type legacyConfigBackupManifest struct {
	Version         int       `json:"version"`
	ID              string    `json:"id"`
	Reason          string    `json:"reason"`
	CreatedAt       time.Time `json:"created_at"`
	ConfigPath      string    `json:"config_path"`
	OriginalExisted bool      `json:"original_existed"`
	OriginalMode    uint32    `json:"original_mode,omitempty"`
	OriginalSHA256  string    `json:"original_sha256,omitempty"`
	AppliedSHA256   string    `json:"applied_sha256,omitempty"`
}

// legacyHistoryBackupManifest is separate from the current manifest so an old
// archive cannot smuggle current-only provenance or status fields through a
// permissive decoder.
type legacyHistoryBackupManifest struct {
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
}

type legacyConfigArtifact struct {
	manifest     legacyConfigBackupManifest
	manifestHash string
	configData   []byte
	configMode   fs.FileMode
}

func (a *legacyConfigArtifact) clear() {
	zeroLegacyBytes(a.configData)
	a.configData = nil
}

type legacyHistoryArtifact struct {
	manifest     legacyHistoryBackupManifest
	manifestHash string
}

// ListLegacyBackups discovers only valid, first-party legacy archives in the
// fixed $CODEX_HOME/xiass-helper location. It is read-only and intentionally
// hides malformed entries rather than exposing filenames, paths, or contents.
func (m *Manager) ListLegacyBackups() ([]LegacyBackupInfo, error) {
	if err := m.validatePaths(); err != nil {
		return nil, err
	}
	config, err := m.listLegacyConfigBackups()
	if err != nil {
		return nil, err
	}
	history, err := m.listLegacyHistoryBackups()
	if err != nil {
		return nil, err
	}
	items := append(config, history...)
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt) })
	return items, nil
}

// ImportLegacyConfigBackup copies one verified configuration backup into the
// XIASS Tools store. It never restores or changes the active config.toml.
func (m *Manager) ImportLegacyConfigBackup(sourceID string) (LegacyImportResult, error) {
	if err := m.validatePaths(); err != nil {
		return LegacyImportResult{}, err
	}
	if !validLegacyBackupID(sourceID) {
		return LegacyImportResult{}, legacyConfigImportError()
	}

	var result LegacyImportResult
	err := m.withLock(func() error {
		artifact, err := m.readLegacyConfigBackup(sourceID)
		if err != nil {
			return err
		}
		defer artifact.clear()

		stage, err := createLegacyImportStage(m.BackupRoot)
		if err != nil {
			return legacyConfigImportError()
		}
		defer func() { _ = os.RemoveAll(stage) }()

		destinationID, err := nextLegacyImportID(m.BackupRoot)
		if err != nil {
			return legacyConfigImportError()
		}
		importedAt := time.Now().UTC()
		manifest := BackupManifest{
			Version:         backupManifestVersion,
			Tool:            DefaultProviderName,
			ID:              destinationID,
			Reason:          "imported_legacy_config",
			CreatedAt:       artifact.manifest.CreatedAt,
			ConfigPath:      m.ConfigPath,
			OriginalExisted: artifact.manifest.OriginalExisted,
			OriginalMode:    artifact.manifest.OriginalMode,
			OriginalSHA256:  artifact.manifest.OriginalSHA256,
			ImportSource:    legacyImportSource,
			ImportSourceID:  sourceID,
			ImportedAt:      &importedAt,
		}

		if artifact.manifest.OriginalExisted {
			if err := writeFileAtomic(filepath.Join(stage, "config.toml"), artifact.configData, 0o600); err != nil {
				return legacyConfigImportError()
			}
			copied, exists, _, err := readRegularFile(filepath.Join(stage, "config.toml"))
			if err != nil || !exists || sha256Hex(copied) != artifact.manifest.OriginalSHA256 {
				zeroLegacyBytes(copied)
				return legacyConfigImportError()
			}
			zeroLegacyBytes(copied)
		}
		if err := writeLegacyImportManifest(filepath.Join(stage, "manifest.json"), manifest); err != nil {
			return legacyConfigImportError()
		}

		if err := m.verifyLegacyConfigUnchanged(sourceID, artifact); err != nil {
			return err
		}
		if err := validateImportedConfigStage(m, stage, manifest); err != nil {
			return legacyConfigImportError()
		}
		if err := publishLegacyImportStage(m.BackupRoot, stage, destinationID); err != nil {
			return legacyConfigImportError()
		}
		stage = ""
		result = LegacyImportResult{
			Kind:             LegacyBackupConfig,
			SourceID:         sourceID,
			ImportedBackupID: destinationID,
			CreatedAt:        manifest.CreatedAt,
			Message:          "Legacy configuration backup imported",
		}
		return nil
	})
	return result, err
}

// ImportLegacyHistoryBackup copies one verified, completed history backup into
// XIASS Tools. It never restores or writes active session, SQLite, config, or
// workspace data. Later restoration remains protected by the Codex-not-running
// write guard in HistoryRepairer.RestoreBackup.
func (m *Manager) ImportLegacyHistoryBackup(sourceID string) (LegacyImportResult, error) {
	if err := m.validatePaths(); err != nil {
		return LegacyImportResult{}, err
	}
	if !validLegacyBackupID(sourceID) {
		return LegacyImportResult{}, legacyHistoryImportError()
	}

	repairer := NewHistoryRepairerWithManager(m)
	var result LegacyImportResult
	err := repairer.withLock(func() error {
		artifact, err := m.readLegacyHistoryBackup(sourceID)
		if err != nil {
			return err
		}

		stage, err := createLegacyImportStage(repairer.BackupRoot)
		if err != nil {
			return legacyHistoryImportError()
		}
		defer func() { _ = os.RemoveAll(stage) }()
		destinationID, err := nextLegacyImportID(repairer.BackupRoot)
		if err != nil {
			return legacyHistoryImportError()
		}

		importedAt := time.Now().UTC()
		manifest := historyBackupManifest{
			Version:            historyBackupVersion,
			ID:                 destinationID,
			CreatedAt:          artifact.manifest.CreatedAt,
			CodexHome:          m.CodexHome,
			TargetProvider:     artifact.manifest.TargetProvider,
			SourceProviders:    append([]string(nil), artifact.manifest.SourceProviders...),
			ScannedFiles:       artifact.manifest.ScannedFiles,
			RolloutFilesSHA256: artifact.manifest.RolloutFilesSHA256,
			ManagedBy:          historyManagedBy,
			Status:             historyStatusCommitted,
			StatusMessage:      "imported legacy history backup",
			SessionChanges:     append([]historySessionPlan(nil), artifact.manifest.SessionChanges...),
			DatabaseFiles:      append([]historyBackupFile(nil), artifact.manifest.DatabaseFiles...),
			DatabasePlans:      append([]historyDatabasePlan(nil), artifact.manifest.DatabasePlans...),
			ImportSource:       legacyImportSource,
			ImportSourceID:     sourceID,
			ImportedAt:         &importedAt,
		}
		for _, database := range artifact.manifest.DatabaseFiles {
			source := legacyHistoryBackupFilePath(m, sourceID, database.BackupPath)
			destination := legacyStageChildPath(stage, database.BackupPath)
			if source == "" || destination == "" || copyLegacyImportFile(source, destination, database.SHA256, legacyHistoryDatabaseMaxBytes, secureMode(fs.FileMode(database.Mode))) != nil {
				return legacyHistoryImportError()
			}
		}
		if err := writeLegacyImportManifest(filepath.Join(stage, "manifest.json"), manifest); err != nil {
			return legacyHistoryImportError()
		}

		if err := m.verifyLegacyHistoryUnchanged(sourceID, artifact); err != nil {
			return err
		}
		if err := validateImportedHistoryStage(stage, manifest); err != nil {
			return legacyHistoryImportError()
		}
		if err := publishLegacyImportStage(repairer.BackupRoot, stage, destinationID); err != nil {
			return legacyHistoryImportError()
		}
		stage = ""
		result = LegacyImportResult{
			Kind:             LegacyBackupHistory,
			SourceID:         sourceID,
			ImportedBackupID: destinationID,
			CreatedAt:        manifest.CreatedAt,
			Message:          "Legacy history backup imported",
		}
		return nil
	})
	return result, err
}

func (m *Manager) listLegacyConfigBackups() ([]LegacyBackupInfo, error) {
	ids, err := legacyBackupIDs(m.legacyConfigBackupRoot())
	if err != nil {
		return nil, errors.New("legacy XIASS Codex Helper backup storage is unavailable")
	}
	items := make([]LegacyBackupInfo, 0, len(ids))
	for _, id := range ids {
		artifact, err := m.readLegacyConfigBackup(id)
		if err != nil {
			continue
		}
		items = append(items, LegacyBackupInfo{
			Kind:       LegacyBackupConfig,
			SourceID:   id,
			CreatedAt:  artifact.manifest.CreatedAt,
			Reason:     artifact.manifest.Reason,
			Valid:      true,
			Importable: true,
			Message:    "Ready to import",
		})
		artifact.clear()
	}
	return items, nil
}

func (m *Manager) listLegacyHistoryBackups() ([]LegacyBackupInfo, error) {
	ids, err := legacyBackupIDs(m.legacyHistoryBackupRoot())
	if err != nil {
		return nil, errors.New("legacy XIASS Codex Helper backup storage is unavailable")
	}
	items := make([]LegacyBackupInfo, 0, len(ids))
	for _, id := range ids {
		artifact, err := m.readLegacyHistoryBackup(id)
		if err != nil {
			continue
		}
		items = append(items, LegacyBackupInfo{
			Kind:           LegacyBackupHistory,
			SourceID:       id,
			CreatedAt:      artifact.manifest.CreatedAt,
			TargetProvider: artifact.manifest.TargetProvider,
			Valid:          true,
			Importable:     true,
			Message:        "Ready to import",
		})
	}
	return items, nil
}

func (m *Manager) readLegacyConfigBackup(sourceID string) (legacyConfigArtifact, error) {
	var artifact legacyConfigArtifact
	directory, err := legacyBackupDirectory(m.legacyConfigBackupRoot(), sourceID)
	if err != nil {
		return artifact, legacyConfigImportError()
	}
	if err := validateLegacyConfigDirectory(directory); err != nil {
		return artifact, legacyConfigImportError()
	}
	manifestData, _, err := readLegacyRegularFile(filepath.Join(directory, "manifest.json"), legacyConfigManifestMaxBytes)
	if err != nil {
		return artifact, legacyConfigImportError()
	}
	defer zeroLegacyBytes(manifestData)
	if err := decodeLegacyJSON(manifestData, &artifact.manifest); err != nil {
		return legacyConfigArtifact{}, legacyConfigImportError()
	}
	artifact.manifestHash = sha256Hex(manifestData)
	if err := validateLegacyConfigManifest(m, sourceID, artifact.manifest); err != nil {
		return legacyConfigArtifact{}, legacyConfigImportError()
	}
	if !artifact.manifest.OriginalExisted {
		if _, err := os.Lstat(filepath.Join(directory, "config.toml")); !errors.Is(err, fs.ErrNotExist) {
			return legacyConfigArtifact{}, legacyConfigImportError()
		}
		return artifact, nil
	}
	data, mode, err := readLegacyRegularFile(filepath.Join(directory, "config.toml"), legacyConfigFileMaxBytes)
	if err != nil {
		return legacyConfigArtifact{}, legacyConfigImportError()
	}
	if sha256Hex(data) != artifact.manifest.OriginalSHA256 || validateTOML(data) != nil {
		zeroLegacyBytes(data)
		return legacyConfigArtifact{}, legacyConfigImportError()
	}
	artifact.configData = data
	artifact.configMode = mode
	return artifact, nil
}

func (m *Manager) verifyLegacyConfigUnchanged(sourceID string, before legacyConfigArtifact) error {
	after, err := m.readLegacyConfigBackup(sourceID)
	if err != nil {
		return legacyConfigImportError()
	}
	defer after.clear()
	if after.manifestHash != before.manifestHash || !bytes.Equal(after.configData, before.configData) {
		return legacyConfigImportError()
	}
	return nil
}

func (m *Manager) readLegacyHistoryBackup(sourceID string) (legacyHistoryArtifact, error) {
	var artifact legacyHistoryArtifact
	directory, err := legacyBackupDirectory(m.legacyHistoryBackupRoot(), sourceID)
	if err != nil {
		return artifact, legacyHistoryImportError()
	}
	if err := validateLegacyHistoryDirectory(directory, false, true); err != nil {
		return artifact, legacyHistoryImportError()
	}
	manifestData, _, err := readLegacyRegularFile(filepath.Join(directory, "manifest.json"), legacyHistoryManifestMaxBytes)
	if err != nil {
		return artifact, legacyHistoryImportError()
	}
	defer zeroLegacyBytes(manifestData)
	if err := decodeLegacyJSON(manifestData, &artifact.manifest); err != nil {
		return legacyHistoryArtifact{}, legacyHistoryImportError()
	}
	artifact.manifestHash = sha256Hex(manifestData)
	if err := validateLegacyHistoryManifest(m, sourceID, artifact.manifest); err != nil {
		return legacyHistoryArtifact{}, legacyHistoryImportError()
	}
	if err := validateLegacyHistoryDirectory(directory, len(artifact.manifest.DatabaseFiles) > 0, false); err != nil {
		return legacyHistoryArtifact{}, legacyHistoryImportError()
	}
	if err := validateHistoryDatabaseTree(directory, artifact.manifest.DatabaseFiles, artifact.manifest.DatabasePlans, artifact.manifest.SourceProviders); err != nil {
		return legacyHistoryArtifact{}, legacyHistoryImportError()
	}
	return artifact, nil
}

func (m *Manager) verifyLegacyHistoryUnchanged(sourceID string, before legacyHistoryArtifact) error {
	after, err := m.readLegacyHistoryBackup(sourceID)
	if err != nil || after.manifestHash != before.manifestHash {
		return legacyHistoryImportError()
	}
	return nil
}

func validateLegacyConfigManifest(m *Manager, sourceID string, manifest legacyConfigBackupManifest) error {
	if manifest.Version != backupManifestVersion || manifest.ID != sourceID || !validLegacyCreatedAt(manifest.CreatedAt) ||
		!sameCleanPath(manifest.ConfigPath, m.ConfigPath) || !validLegacyConfigReason(manifest.Reason) ||
		!validLegacyMode(manifest.OriginalMode) {
		return legacyConfigImportError()
	}
	if manifest.OriginalExisted {
		if !validCanonicalSHA256(manifest.OriginalSHA256) {
			return legacyConfigImportError()
		}
	} else if manifest.OriginalSHA256 != "" {
		return legacyConfigImportError()
	}
	if manifest.AppliedSHA256 != "" && !validCanonicalSHA256(manifest.AppliedSHA256) {
		return legacyConfigImportError()
	}
	return nil
}

func validateLegacyHistoryManifest(m *Manager, sourceID string, manifest legacyHistoryBackupManifest) error {
	if manifest.Version != historyBackupVersion || manifest.ID != sourceID || manifest.ManagedBy != legacyHistoryManagedBy ||
		manifest.Status != historyStatusCommitted || !validLegacyCreatedAt(manifest.CreatedAt) ||
		!sameCleanPath(manifest.CodexHome, m.CodexHome) || !validHistoryProviderID(manifest.TargetProvider) ||
		manifest.ScannedFiles < 0 || !validCanonicalSHA256(manifest.RolloutFilesSHA256) ||
		len(manifest.SourceProviders) > legacyMaxHistoryProviders ||
		len(manifest.SessionChanges) > legacyMaxHistorySessionChanges ||
		len(manifest.DatabaseFiles) > legacyMaxHistoryDatabaseFiles ||
		len(manifest.DatabaseFiles) != len(manifest.DatabasePlans) {
		return legacyHistoryImportError()
	}

	providers := make(map[string]struct{}, len(manifest.SourceProviders))
	for _, provider := range manifest.SourceProviders {
		if !validHistoryProviderID(provider) || provider == manifest.TargetProvider {
			return legacyHistoryImportError()
		}
		if _, exists := providers[provider]; exists {
			return legacyHistoryImportError()
		}
		providers[provider] = struct{}{}
	}

	sessions := make(map[string]struct{}, len(manifest.SessionChanges))
	sessionFiles := make(map[string]struct{}, len(manifest.SessionChanges))
	for _, session := range manifest.SessionChanges {
		if err := validateLegacyHistorySession(m, session, manifest.TargetProvider, providers); err != nil {
			return legacyHistoryImportError()
		}
		key := session.RelativePath + ":" + fmt.Sprint(session.LineIndex)
		if _, exists := sessions[key]; exists {
			return legacyHistoryImportError()
		}
		sessions[key] = struct{}{}
		sessionFiles[session.RelativePath] = struct{}{}
	}
	if manifest.ScannedFiles < len(sessionFiles) {
		return legacyHistoryImportError()
	}

	plans := make(map[string]historyDatabasePlan, len(manifest.DatabasePlans))
	for _, plan := range manifest.DatabasePlans {
		if err := validateLegacyHistoryDatabasePlan(m, plan); err != nil {
			return legacyHistoryImportError()
		}
		if _, exists := plans[plan.RelativePath]; exists {
			return legacyHistoryImportError()
		}
		plans[plan.RelativePath] = plan
	}
	files := make(map[string]struct{}, len(manifest.DatabaseFiles))
	for _, file := range manifest.DatabaseFiles {
		plan, exists := plans[legacyBackupRelativePath(file.BackupPath)]
		if !exists || !validLegacyHistoryBackupFile(m, file, plan) {
			return legacyHistoryImportError()
		}
		if _, duplicate := files[file.BackupPath]; duplicate {
			return legacyHistoryImportError()
		}
		files[file.BackupPath] = struct{}{}
	}
	return nil
}

func validateLegacyHistorySession(m *Manager, session historySessionPlan, target string, sourceProviders map[string]struct{}) error {
	if !validLegacySessionRelativePath(session.RelativePath) || !legacyRelativePathWithin(m.CodexHome, session.RelativePath) ||
		session.LineIndex < 0 || session.LineIndex > legacyMaxHistoryLineIndex || !validLegacyMode(session.Mode) ||
		!validLegacyCreatedAt(session.ModifiedAt) || len(session.OriginalLine) == 0 ||
		len(session.OriginalLine) > historyMetadataMax || len(session.UpdatedLine) == 0 || len(session.UpdatedLine) > historyMetadataMax {
		return legacyHistoryImportError()
	}
	return validateLegacySessionAction(session, target, sourceProviders)
}

func validateLegacySessionAction(session historySessionPlan, target string, sourceProviders map[string]struct{}) error {
	original, err := decodeLegacyJSONRecord(session.OriginalLine)
	if err != nil {
		return legacyHistoryImportError()
	}
	switch session.Action {
	case historySessionActionProvider:
		if original["type"] != "session_meta" {
			return legacyHistoryImportError()
		}
		payload, ok := original["payload"].(map[string]any)
		if !ok {
			return legacyHistoryImportError()
		}
		provider, ok := payload["model_provider"].(string)
		if !ok || provider == target {
			return legacyHistoryImportError()
		}
		if _, known := sourceProviders[provider]; !known {
			return legacyHistoryImportError()
		}
		payload["model_provider"] = target
		rebuilt, err := json.Marshal(original)
		if err != nil || !bytes.Equal(rebuilt, session.UpdatedLine) {
			return legacyHistoryImportError()
		}
		return nil
	case historySessionActionDropItem, historySessionActionStripID:
		if !shouldSanitizeSessionReplay(target) {
			return legacyHistoryImportError()
		}
		rebuilt, action, changed, err := sanitizeSessionReplayRecord(original)
		if err != nil || !changed || action != session.Action || !bytes.Equal(rebuilt, session.UpdatedLine) {
			return legacyHistoryImportError()
		}
		return nil
	default:
		return legacyHistoryImportError()
	}
}

func validateLegacyHistoryDatabasePlan(m *Manager, plan historyDatabasePlan) error {
	if !validLegacyDatabaseRelativePath(plan.RelativePath) || !legacyRelativePathWithin(m.CodexHome, plan.RelativePath) ||
		plan.ThreadCount < 0 || plan.MismatchedRows < 0 || !validCanonicalSHA256(plan.ThreadIDsSHA256) ||
		!validCanonicalSHA256(plan.ThreadContentSHA256) {
		return legacyHistoryImportError()
	}
	return nil
}

func validLegacyHistoryBackupFile(m *Manager, file historyBackupFile, plan historyDatabasePlan) bool {
	if !file.Existed || !validLegacyMode(file.Mode) || !validCanonicalSHA256(file.SHA256) {
		return false
	}
	if !sameCleanPath(file.SourcePath, filepath.Join(m.CodexHome, filepath.FromSlash(plan.RelativePath))) {
		return false
	}
	return file.BackupPath == filepath.ToSlash(filepath.Join("database", filepath.FromSlash(plan.RelativePath)))
}

func validateHistoryDatabaseTree(directory string, files []historyBackupFile, plans []historyDatabasePlan, sourceProviders []string) error {
	if len(files) == 0 {
		return nil
	}
	expected := make(map[string]historyBackupFile, len(files))
	planByRelative := make(map[string]historyDatabasePlan, len(plans))
	for _, plan := range plans {
		planByRelative[plan.RelativePath] = plan
	}
	for _, file := range files {
		relative := legacyBackupRelativePath(file.BackupPath)
		if relative == "" {
			return legacyHistoryImportError()
		}
		expected[relative] = file
	}
	root := filepath.Join(directory, "database")
	var aggregateBytes int64
	return walkLegacyExpectedFiles(root, expected, func(relative string, file historyBackupFile, source string) error {
		plan, exists := planByRelative[relative]
		if !exists {
			return legacyHistoryImportError()
		}
		info, err := os.Lstat(source)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > legacyHistoryDatabaseMaxBytes || aggregateBytes > legacyHistoryAggregateMaxBytes-info.Size() {
			return legacyHistoryImportError()
		}
		aggregateBytes += info.Size()
		return validateLegacyDatabaseSnapshot(source, file, plan, sourceProviders)
	})
}

func validateLegacyDatabaseSnapshot(source string, file historyBackupFile, plan historyDatabasePlan, sourceProviders []string) error {
	actual, _, err := legacyFileSHA256(source, legacyHistoryDatabaseMaxBytes)
	if err != nil || actual != file.SHA256 {
		return legacyHistoryImportError()
	}
	database, err := openHistoryDatabaseReadOnly(source)
	if err != nil {
		return legacyHistoryImportError()
	}
	defer database.Close()
	if err := checkHistoryDatabase(database); err != nil {
		return legacyHistoryImportError()
	}
	count, ids, contents, err := databaseThreadIdentity(database)
	if err != nil || count != plan.ThreadCount || ids != plan.ThreadIDsSHA256 || contents != plan.ThreadContentSHA256 {
		return legacyHistoryImportError()
	}
	if len(sourceProviders) == 0 {
		if plan.MismatchedRows != 0 {
			return legacyHistoryImportError()
		}
		return nil
	}
	where, arguments := historyProviderWhereClause("model_provider", sourceProviders)
	var mismatched int64
	if err := database.QueryRow("SELECT COUNT(*) FROM threads WHERE "+where, arguments...).Scan(&mismatched); err != nil || mismatched != plan.MismatchedRows {
		return legacyHistoryImportError()
	}
	return nil
}

func validateLegacyConfigDirectory(directory string) error {
	entries, err := strictLegacyDirectoryEntries(directory)
	if err != nil {
		return legacyConfigImportError()
	}
	allowed := map[string]bool{"manifest.json": true, "config.toml": true}
	seen := map[string]bool{}
	for _, entry := range entries {
		if !allowed[entry.Name()] || !entry.Mode().IsRegular() {
			return legacyConfigImportError()
		}
		seen[entry.Name()] = true
	}
	if !seen["manifest.json"] {
		return legacyConfigImportError()
	}
	return nil
}

// validateLegacyHistoryDirectory validates the archival tree without reading a
// snapshot's contents. Snapshot files are never copied; inspecting metadata
// only prevents a symlink or special file from being silently accepted.
func validateLegacyHistoryDirectory(directory string, databaseRequired, allowOptionalDatabase bool) error {
	entries, err := strictLegacyDirectoryEntries(directory)
	if err != nil {
		return legacyHistoryImportError()
	}
	seen := map[string]fs.FileMode{}
	for _, entry := range entries {
		switch entry.Name() {
		case "manifest.json":
			if !entry.Mode().IsRegular() {
				return legacyHistoryImportError()
			}
		case "database", "snapshot":
			if !entry.IsDir() {
				return legacyHistoryImportError()
			}
		default:
			return legacyHistoryImportError()
		}
		seen[entry.Name()] = entry.Mode()
	}
	if _, ok := seen["manifest.json"]; !ok {
		return legacyHistoryImportError()
	}
	if databaseRequired {
		if _, ok := seen["database"]; !ok {
			return legacyHistoryImportError()
		}
	} else if !allowOptionalDatabase {
		if _, ok := seen["database"]; ok {
			return legacyHistoryImportError()
		}
	}
	if _, ok := seen["snapshot"]; ok {
		if err := validateLegacySnapshotDirectory(filepath.Join(directory, "snapshot")); err != nil {
			return legacyHistoryImportError()
		}
	}
	return nil
}

func validateLegacySnapshotDirectory(directory string) error {
	entries, err := strictLegacyDirectoryEntries(directory)
	if err != nil {
		return legacyHistoryImportError()
	}
	allowed := map[string]bool{
		"config.toml":                  true,
		"session_index.jsonl":          true,
		".codex-global-state.json":     true,
		".codex-global-state.json.bak": true,
	}
	for _, entry := range entries {
		if !allowed[entry.Name()] || !entry.Mode().IsRegular() || entry.Size() < 0 || entry.Size() > legacySnapshotFileMaxBytes {
			return legacyHistoryImportError()
		}
	}
	return nil
}

func validateImportedConfigStage(m *Manager, stage string, manifest BackupManifest) error {
	if manifest.Version != backupManifestVersion || manifest.Tool != DefaultProviderName || !validBackupID(manifest.ID) ||
		!validLegacyCreatedAt(manifest.CreatedAt) || manifest.Reason != "imported_legacy_config" ||
		manifest.ImportSource != legacyImportSource || !validLegacyBackupID(manifest.ImportSourceID) ||
		(manifest.ImportedAt == nil || !validLegacyCreatedAt(*manifest.ImportedAt)) || !validLegacyMode(manifest.OriginalMode) || !sameCleanPath(manifest.ConfigPath, m.ConfigPath) {
		return legacyConfigImportError()
	}
	if manifest.OriginalExisted {
		data, _, err := readLegacyRegularFile(filepath.Join(stage, "config.toml"), legacyConfigFileMaxBytes)
		if err != nil {
			return legacyConfigImportError()
		}
		defer zeroLegacyBytes(data)
		if !validCanonicalSHA256(manifest.OriginalSHA256) || sha256Hex(data) != manifest.OriginalSHA256 || validateTOML(data) != nil {
			return legacyConfigImportError()
		}
	} else if manifest.OriginalSHA256 != "" {
		return legacyConfigImportError()
	}
	return validateLegacyConfigDirectory(stage)
}

func validateImportedHistoryStage(stage string, manifest historyBackupManifest) error {
	legacy := legacyHistoryBackupManifest{
		Version:            manifest.Version,
		ID:                 manifest.ID,
		CreatedAt:          manifest.CreatedAt,
		CodexHome:          manifest.CodexHome,
		TargetProvider:     manifest.TargetProvider,
		SourceProviders:    manifest.SourceProviders,
		ScannedFiles:       manifest.ScannedFiles,
		RolloutFilesSHA256: manifest.RolloutFilesSHA256,
		ManagedBy:          legacyHistoryManagedBy,
		Status:             historyStatusCommitted,
		SessionChanges:     manifest.SessionChanges,
		DatabaseFiles:      manifest.DatabaseFiles,
		DatabasePlans:      manifest.DatabasePlans,
	}
	manager := NewManager(manifest.CodexHome)
	if err := validateLegacyHistoryManifest(manager, manifest.ID, legacy); err != nil ||
		manifest.ManagedBy != historyManagedBy || manifest.Status != historyStatusCommitted ||
		manifest.ImportSource != legacyImportSource || !validLegacyBackupID(manifest.ImportSourceID) ||
		(manifest.ImportedAt == nil || !validLegacyCreatedAt(*manifest.ImportedAt)) {
		return legacyHistoryImportError()
	}
	if err := validateLegacyHistoryDirectory(stage, len(manifest.DatabaseFiles) > 0, false); err != nil {
		return legacyHistoryImportError()
	}
	if err := validateHistoryDatabaseTree(stage, manifest.DatabaseFiles, manifest.DatabasePlans, manifest.SourceProviders); err != nil {
		return legacyHistoryImportError()
	}
	return nil
}

func legacyBackupIDs(root string) ([]string, error) {
	info, err := os.Lstat(root)
	if errors.Is(err, fs.ErrNotExist) {
		return []string{}, nil
	}
	parentInfo, parentErr := os.Lstat(filepath.Dir(root))
	if err != nil || parentErr != nil || parentInfo.Mode()&os.ModeSymlink != 0 || !parentInfo.IsDir() || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, errors.New("unsafe legacy backup root")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !validLegacyBackupID(entry.Name()) {
			continue
		}
		candidate := filepath.Join(root, entry.Name())
		if !legacyPathInside(root, candidate) {
			continue
		}
		entryInfo, err := os.Lstat(candidate)
		if err != nil || entryInfo.Mode()&os.ModeSymlink != 0 || !entryInfo.IsDir() {
			continue
		}
		ids = append(ids, entry.Name())
	}
	sort.Strings(ids)
	return ids, nil
}

func legacyBackupDirectory(root, sourceID string) (string, error) {
	if !validLegacyBackupID(sourceID) {
		return "", errors.New("invalid legacy backup ID")
	}
	rootInfo, err := os.Lstat(root)
	parentInfo, parentErr := os.Lstat(filepath.Dir(root))
	if err != nil || parentErr != nil || parentInfo.Mode()&os.ModeSymlink != 0 || !parentInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return "", errors.New("unsafe legacy backup root")
	}
	directory := filepath.Join(root, sourceID)
	if !legacyPathInside(root, directory) {
		return "", errors.New("invalid legacy backup path")
	}
	info, err := os.Lstat(directory)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", errors.New("unsafe legacy backup directory")
	}
	return directory, nil
}

func strictLegacyDirectoryEntries(directory string) ([]fs.FileInfo, error) {
	info, err := os.Lstat(directory)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, errors.New("unsafe legacy directory")
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	result := make([]fs.FileInfo, 0, len(entries))
	for _, entry := range entries {
		candidate := filepath.Join(directory, entry.Name())
		if !legacyPathInside(directory, candidate) {
			return nil, errors.New("unsafe legacy directory entry")
		}
		entryInfo, err := os.Lstat(candidate)
		if err != nil || entryInfo.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("unsafe legacy directory entry")
		}
		result = append(result, fileInfoWithName{FileInfo: entryInfo, name: entry.Name()})
	}
	return result, nil
}

type fileInfoWithName struct {
	fs.FileInfo
	name string
}

func (f fileInfoWithName) Name() string { return f.name }

func readLegacyRegularFile(path string, maximum int64) ([]byte, fs.FileMode, error) {
	if maximum <= 0 {
		return nil, 0, errors.New("invalid legacy size limit")
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maximum {
		return nil, 0, errors.New("unsafe legacy file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maximum+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || int64(len(data)) > maximum {
		zeroLegacyBytes(data)
		return nil, 0, errors.New("read legacy file")
	}
	after, err := os.Lstat(path)
	if err != nil || after.Mode()&os.ModeSymlink != 0 || !after.Mode().IsRegular() || !os.SameFile(info, after) || after.Size() != info.Size() {
		zeroLegacyBytes(data)
		return nil, 0, errors.New("legacy file changed while reading")
	}
	return data, info.Mode().Perm(), nil
}

func decodeLegacyJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func decodeLegacyJSONRecord(data []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var record map[string]any
	if err := decoder.Decode(&record); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("invalid JSON record")
	}
	return record, nil
}

func writeLegacyImportManifest(destination string, manifest any) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writeFileAtomic(destination, data, 0o600)
}

func createLegacyImportStage(parent string) (string, error) {
	if info, err := os.Lstat(parent); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", errors.New("unsafe import destination")
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", errors.New("unsafe import destination")
	}
	dataRoot := filepath.Dir(parent)
	if info, err := os.Lstat(dataRoot); err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.IsDir()) {
		return "", errors.New("unsafe import destination")
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", errors.New("unsafe import destination")
	}
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", err
	}
	info, err := os.Lstat(parent)
	dataRootInfo, dataRootErr := os.Lstat(dataRoot)
	if err != nil || dataRootErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || dataRootInfo.Mode()&os.ModeSymlink != 0 || !dataRootInfo.IsDir() {
		return "", errors.New("unsafe import destination")
	}
	if err := protectPrivateDirectory(parent); err != nil {
		return "", errors.New("could not secure Codex configuration backup storage")
	}
	stage, err := os.MkdirTemp(parent, ".legacy-import-")
	if err != nil {
		return "", err
	}
	if err := protectPrivateDirectory(stage); err != nil {
		_ = os.Remove(stage)
		return "", errors.New("could not secure Codex configuration import stage")
	}
	return stage, nil
}

func nextLegacyImportID(parent string) (string, error) {
	for range 16 {
		id, err := newBackupID()
		if err != nil {
			return "", err
		}
		candidate := filepath.Join(parent, id)
		if !legacyPathInside(parent, candidate) {
			return "", errors.New("invalid import destination")
		}
		if _, err := os.Lstat(candidate); errors.Is(err, fs.ErrNotExist) {
			return id, nil
		} else if err != nil {
			return "", err
		}
	}
	return "", errors.New("could not allocate a new backup ID")
}

func publishLegacyImportStage(parent, stage, destinationID string) error {
	if !validBackupID(destinationID) || !legacyPathInside(parent, stage) {
		return errors.New("invalid import stage")
	}
	destination := filepath.Join(parent, destinationID)
	if !legacyPathInside(parent, destination) {
		return errors.New("invalid import destination")
	}
	// Reserve the final directory with Mkdir rather than relying on Rename's
	// platform-dependent overwrite semantics. The manifest moves last, so a
	// partial destination cannot be listed as a usable backup.
	if err := os.Mkdir(destination, 0o700); err != nil {
		return err
	}
	published := false
	defer func() {
		if !published {
			// This directory was created exclusively by this import attempt and
			// has no manifest yet. Never touch the legacy source tree.
			_ = os.RemoveAll(destination)
		}
	}()
	if err := protectPrivateDirectory(destination); err != nil {
		return errors.New("could not secure Codex configuration backup directory")
	}
	entries, err := os.ReadDir(stage)
	if err != nil {
		return err
	}
	manifestPath := filepath.Join(stage, "manifest.json")
	manifestInfo, err := os.Lstat(manifestPath)
	if err != nil || manifestInfo.Mode()&os.ModeSymlink != 0 || !manifestInfo.Mode().IsRegular() {
		return errors.New("invalid import manifest")
	}
	for _, entry := range entries {
		if entry.Name() == "manifest.json" {
			continue
		}
		source := filepath.Join(stage, entry.Name())
		if !legacyPathInside(stage, source) {
			return errors.New("invalid import stage entry")
		}
		info, err := os.Lstat(source)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || (!info.Mode().IsRegular() && !info.IsDir()) {
			return errors.New("unsafe import stage entry")
		}
		destinationPath := filepath.Join(destination, entry.Name())
		if err := os.Rename(source, destinationPath); err != nil {
			return err
		}
		if info.IsDir() {
			if err := protectPrivateDirectory(destinationPath); err != nil {
				return errors.New("could not secure Codex configuration backup directory")
			}
		} else if err := finalizePrivateFile(destinationPath, secureMode(info.Mode())); err != nil {
			return err
		}
	}
	publishedManifest := filepath.Join(destination, "manifest.json")
	if err := os.Rename(manifestPath, publishedManifest); err != nil {
		return err
	}
	if err := finalizePrivateFile(publishedManifest, 0o600); err != nil {
		return err
	}
	published = true
	// The stage is now empty. Failure to remove it is harmless and must not turn
	// a fully published backup into an apparent import failure.
	_ = os.Remove(stage)
	directory, err := os.Open(parent)
	if err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return nil
}

func copyLegacyImportFile(source, destination, expectedHash string, maximum int64, mode fs.FileMode) error {
	if !validCanonicalSHA256(expectedHash) || maximum <= 0 || destination == "" {
		return legacyHistoryImportError()
	}
	info, err := os.Lstat(source)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maximum {
		return legacyHistoryImportError()
	}
	input, err := os.Open(source)
	if err != nil {
		return legacyHistoryImportError()
	}
	defer input.Close()
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return legacyHistoryImportError()
	}
	if err := protectPrivateDirectory(filepath.Dir(destination)); err != nil {
		return legacyHistoryImportError()
	}
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".legacy-copy-")
	if err != nil {
		return legacyHistoryImportError()
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}()
	if err := preparePrivateFile(temporaryPath, secureMode(mode)); err != nil {
		return legacyHistoryImportError()
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(temporary, hash), io.LimitReader(input, maximum+1))
	if copyErr != nil || written > maximum || hex.EncodeToString(hash.Sum(nil)) != expectedHash {
		return legacyHistoryImportError()
	}
	if err := temporary.Sync(); err != nil {
		return legacyHistoryImportError()
	}
	if err := temporary.Close(); err != nil {
		return legacyHistoryImportError()
	}
	if err := replaceFileAtomic(temporaryPath, destination); err != nil {
		return legacyHistoryImportError()
	}
	if err := finalizePrivateFile(destination, secureMode(mode)); err != nil {
		return legacyHistoryImportError()
	}
	actual, _, err := legacyFileSHA256(destination, maximum)
	if err != nil || actual != expectedHash {
		return legacyHistoryImportError()
	}
	after, err := os.Lstat(source)
	if err != nil || after.Mode()&os.ModeSymlink != 0 || !after.Mode().IsRegular() || !os.SameFile(info, after) || after.Size() != info.Size() {
		return legacyHistoryImportError()
	}
	return nil
}

func legacyFileSHA256(path string, maximum int64) (string, int64, error) {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maximum {
		return "", 0, legacyHistoryImportError()
	}
	file, err := os.Open(path)
	if err != nil {
		return "", 0, legacyHistoryImportError()
	}
	hash := sha256.New()
	written, copyErr := io.Copy(hash, io.LimitReader(file, maximum+1))
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil || written > maximum {
		return "", 0, legacyHistoryImportError()
	}
	after, err := os.Lstat(path)
	if err != nil || after.Mode()&os.ModeSymlink != 0 || !after.Mode().IsRegular() || !os.SameFile(info, after) || after.Size() != info.Size() {
		return "", 0, legacyHistoryImportError()
	}
	return hex.EncodeToString(hash.Sum(nil)), written, nil
}

func walkLegacyExpectedFiles(root string, expected map[string]historyBackupFile, validate func(relative string, file historyBackupFile, source string) error) error {
	rootInfo, err := os.Lstat(root)
	if err != nil || rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return legacyHistoryImportError()
	}
	allowedDirectories := map[string]struct{}{".": {}}
	for relative := range expected {
		for parent := path.Dir(relative); parent != "."; parent = path.Dir(parent) {
			allowedDirectories[parent] = struct{}{}
		}
	}
	seen := make(map[string]struct{}, len(expected))
	err = filepath.WalkDir(root, func(candidate string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || (filepath.Clean(candidate) != filepath.Clean(root) && !legacyPathInside(root, candidate)) {
			return legacyHistoryImportError()
		}
		info, err := os.Lstat(candidate)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return legacyHistoryImportError()
		}
		relativePath, err := filepath.Rel(root, candidate)
		if err != nil {
			return legacyHistoryImportError()
		}
		relative := filepath.ToSlash(relativePath)
		if relative == "." {
			return nil
		}
		if strings.Count(relative, "/") > 3 {
			return legacyHistoryImportError()
		}
		if info.IsDir() {
			if _, ok := allowedDirectories[relative]; !ok {
				return legacyHistoryImportError()
			}
			return nil
		}
		if !info.Mode().IsRegular() {
			return legacyHistoryImportError()
		}
		file, ok := expected[relative]
		if !ok {
			return legacyHistoryImportError()
		}
		if _, duplicate := seen[relative]; duplicate {
			return legacyHistoryImportError()
		}
		seen[relative] = struct{}{}
		return validate(relative, file, candidate)
	})
	if err != nil || len(seen) != len(expected) {
		return legacyHistoryImportError()
	}
	return nil
}

func (m *Manager) legacyConfigBackupRoot() string {
	return filepath.Join(m.CodexHome, legacyHelperDataDirectory, legacyConfigBackupDirectory)
}

func (m *Manager) legacyHistoryBackupRoot() string {
	return filepath.Join(m.CodexHome, legacyHelperDataDirectory, legacyHistoryBackupDirectory)
}

func legacyHistoryBackupFilePath(m *Manager, sourceID, backupPath string) string {
	root := filepath.Join(m.legacyHistoryBackupRoot(), sourceID)
	return legacyStageChildPath(root, backupPath)
}

func legacyStageChildPath(root, portable string) string {
	if !validPortableRelativePath(portable) {
		return ""
	}
	candidate := filepath.Join(root, filepath.FromSlash(portable))
	if !legacyPathInside(root, candidate) {
		return ""
	}
	return candidate
}

func legacyBackupRelativePath(backupPath string) string {
	if !strings.HasPrefix(backupPath, "database/") {
		return ""
	}
	relative := strings.TrimPrefix(backupPath, "database/")
	if !validLegacyDatabaseRelativePath(relative) {
		return ""
	}
	return relative
}

func validLegacyBackupID(value string) bool {
	return legacyBackupIDPattern.MatchString(value)
}

func validCanonicalSHA256(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validLegacyMode(mode uint32) bool {
	return mode&^uint32(0o777) == 0
}

func validLegacyCreatedAt(value time.Time) bool {
	return !value.IsZero() && value.Year() >= 2000 && value.Year() <= 2200
}

func validLegacyConfigReason(value string) bool {
	return value == "apply" || value == "pre_restore"
}

func validLegacySessionRelativePath(value string) bool {
	if !validPortableRelativePath(value) || filepath.Ext(value) != ".jsonl" {
		return false
	}
	return strings.HasPrefix(value, "sessions/") || strings.HasPrefix(value, "archived_sessions/")
}

func validLegacyDatabaseRelativePath(value string) bool {
	if !validPortableRelativePath(value) {
		return false
	}
	extension := strings.ToLower(path.Ext(value))
	if extension != ".db" && extension != ".sqlite" && extension != ".sqlite3" {
		return false
	}
	directory := path.Dir(value)
	base := strings.ToLower(path.Base(value))
	if directory == "." {
		return strings.HasPrefix(base, "state_")
	}
	return directory == "sqlite"
}

func validPortableRelativePath(value string) bool {
	if value == "" || len(value) > legacyMaxPortableRelativeLength || strings.ContainsAny(value, "\\\\\x00") || strings.HasPrefix(value, "/") {
		return false
	}
	cleaned := path.Clean(value)
	return cleaned == value && cleaned != "." && !strings.HasPrefix(cleaned, "../")
}

func legacyRelativePathWithin(root, portable string) bool {
	if !validPortableRelativePath(portable) {
		return false
	}
	return legacyPathInside(root, filepath.Join(root, filepath.FromSlash(portable)))
}

func legacyPathInside(root, candidate string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator))
}

func sameCleanPath(left, right string) bool {
	return strings.TrimSpace(left) != "" && strings.TrimSpace(right) != "" && filepath.Clean(left) == filepath.Clean(right)
}

func zeroLegacyBytes(data []byte) {
	for index := range data {
		data[index] = 0
	}
}

func legacyConfigImportError() error {
	return errors.New("legacy configuration backup could not be imported safely")
}

func legacyHistoryImportError() error {
	return errors.New("legacy history backup could not be imported safely")
}
