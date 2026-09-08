// Package codexconfig manages the local Codex configuration without starting,
// stopping, or otherwise controlling the Codex application.
//
// It intentionally has no UI or process-management dependencies so the same
// implementation can be reused by Windows and macOS front ends.
package codexconfig

import (
	"io/fs"
	"time"
)

const (
	// DefaultProviderID is the stable provider entry written by XIASS Tools.
	// It is deliberately separate from provider IDs used by earlier helpers so
	// applying a new configuration never overwrites an unrelated provider.
	DefaultProviderID   = "xiass_tools"
	DefaultProviderName = "XIASS Tools"
	DefaultModel        = "gpt-5.6-sol"
	DefaultWireAPI      = "responses"

	DefaultContextWindow         int64 = 372000
	DefaultAutoCompactTokenLimit int64 = DefaultContextWindow * 9 / 10
	MinimumContextWindow         int64 = 64000
	MaximumContextWindow         int64 = 1050000
	MinimumAutoCompactTokenLimit int64 = 16000
)

// ApplyConfig describes the Codex provider entry to write. BaseURL accepts a
// bare hostname, an API root, or a normal OpenAI-compatible /v1 endpoint.
// APIKey is never written to a manifest or returned by inspection APIs.
type ApplyConfig struct {
	BaseURL                    string `json:"base_url"`
	APIKey                     string `json:"api_key"`
	KeyName                    string `json:"key_name,omitempty"`
	ProviderID                 string `json:"provider_id,omitempty"`
	ProviderName               string `json:"provider_name,omitempty"`
	Model                      string `json:"model,omitempty"`
	ReviewModel                string `json:"review_model,omitempty"`
	WireAPI                    string `json:"wire_api,omitempty"`
	WebSearch                  string `json:"web_search,omitempty"`
	ModelContextWindow         int64  `json:"model_context_window,omitempty"`
	ModelAutoCompactTokenLimit int64  `json:"model_auto_compact_token_limit,omitempty"`
}

// ContextSettings are the two Codex context settings that XIASS Tools owns.
type ContextSettings struct {
	ModelContextWindow         int64 `json:"model_context_window"`
	ModelAutoCompactTokenLimit int64 `json:"model_auto_compact_token_limit"`
}

// ConfigLocation identifies a discovered Codex config file.
type ConfigLocation struct {
	CodexHome  string `json:"codex_home"`
	ConfigPath string `json:"config_path"`
	Exists     bool   `json:"exists"`
}

// Provider is the non-secret portion of a configured Codex provider.
type Provider struct {
	ID                    string   `json:"id"`
	Name                  string   `json:"name,omitempty"`
	BaseURL               string   `json:"base_url,omitempty"`
	WireAPI               string   `json:"wire_api,omitempty"`
	RequiresOpenAIAuth    bool     `json:"requires_openai_auth,omitempty"`
	SupportsWebSockets    bool     `json:"supports_websockets,omitempty"`
	HasExperimentalBearer bool     `json:"has_experimental_bearer_token"`
	HeaderNames           []string `json:"header_names,omitempty"`
}

// ManagedProviderIssue is a stable, deliberately non-sensitive explanation
// for why the active XIASS Tools provider could not be structurally verified.
// It is an enum rather than a wrapped parser/validation error so it can never
// become a channel for a local URL, header value, API key, filesystem path, or
// arbitrary config.toml content.
type ManagedProviderIssue string

const (
	ManagedProviderIssueNone              ManagedProviderIssue = ""
	ManagedProviderIssueInactive          ManagedProviderIssue = "inactive"
	ManagedProviderIssueProviderMissing   ManagedProviderIssue = "provider_missing"
	ManagedProviderIssueProviderMalformed ManagedProviderIssue = "provider_malformed"
	ManagedProviderIssueBaseURL           ManagedProviderIssue = "base_url_invalid"
	ManagedProviderIssueWireAPI           ManagedProviderIssue = "wire_api_invalid"
	ManagedProviderIssueBearer            ManagedProviderIssue = "bearer_token_missing"
	ManagedProviderIssueFlags             ManagedProviderIssue = "provider_flags_invalid"
	ManagedProviderIssueHeaders           ManagedProviderIssue = "headers_invalid"
	ManagedProviderIssueModel             ManagedProviderIssue = "model_invalid"
	ManagedProviderIssueReviewModel       ManagedProviderIssue = "review_model_invalid"
	ManagedProviderIssueContext           ManagedProviderIssue = "context_invalid"
	ManagedProviderIssueWebSearch         ManagedProviderIssue = "web_search_invalid"
)

// ConfigSnapshot is a verified, redacted view of config.toml.
type ConfigSnapshot struct {
	Location         ConfigLocation  `json:"location"`
	SHA256           string          `json:"sha256,omitempty"`
	Mode             fs.FileMode     `json:"mode,omitempty"`
	Valid            bool            `json:"valid"`
	ModelProvider    string          `json:"model_provider,omitempty"`
	Model            string          `json:"model,omitempty"`
	ReviewModel      string          `json:"review_model,omitempty"`
	WebSearch        string          `json:"web_search,omitempty"`
	Context          ContextSettings `json:"context"`
	Providers        []Provider      `json:"providers,omitempty"`
	ConfiguredModels []string        `json:"configured_models,omitempty"`
	// ManagedProviderVerified only becomes true when the active
	// xiass_tools entry matches every non-secret Codex semantics XIASS Tools
	// relies on. Valid TOML alone is not evidence that the managed provider is
	// usable. ManagedProviderIssue contains only a fixed redacted enum.
	ManagedProviderVerified bool                          `json:"managed_provider_verified"`
	ManagedProviderIssue    ManagedProviderIssue          `json:"managed_provider_issue,omitempty"`
	LegacyProviderMigration LegacyProviderMigrationStatus `json:"legacy_provider_migration"`
}

// LegacyProviderMigrationStatus is the deliberately small, redacted result of
// checking whether an exact first-party predecessor Provider can be migrated.
// It contains neither a Provider's contents nor any credential, endpoint,
// header, local path, account, session, or history detail. Available becomes
// true only after the native layer has proven that one (and only one) of the
// fixed predecessor IDs can be rewritten without touching other Providers.
//
// The migration source is intentionally fixed to xiass and
// codex_local_access. An application that happens to use another
// legacy-looking ID is not XIASS Tools' migration target.
type LegacyProviderMigrationStatus struct {
	Available  bool   `json:"available"`
	ProviderID string `json:"provider_id,omitempty"`
	WasActive  bool   `json:"was_active"`
}

// LegacyProviderMigrationResult reports one explicit Provider-ID migration.
// Like other config results it never returns a key, endpoint, header, path,
// raw configuration, or legacy Provider data.
type LegacyProviderMigrationResult struct {
	BackupID   string `json:"backup_id,omitempty"`
	ConfigSHA  string `json:"config_sha256,omitempty"`
	Migrated   bool   `json:"migrated"`
	ProviderID string `json:"provider_id,omitempty"`
	WasActive  bool   `json:"was_active"`
}

// BackupManifest describes one exact, checksum-protected config backup.
// It deliberately contains no secret data; secrets only live in the protected
// config.toml backup file.
type BackupManifest struct {
	Version         int       `json:"version"`
	Tool            string    `json:"tool"`
	ID              string    `json:"id"`
	Reason          string    `json:"reason"`
	CreatedAt       time.Time `json:"created_at"`
	ConfigPath      string    `json:"config_path"`
	OriginalExisted bool      `json:"original_existed"`
	OriginalMode    uint32    `json:"original_mode,omitempty"`
	OriginalSHA256  string    `json:"original_sha256,omitempty"`
	AppliedSHA256   string    `json:"applied_sha256,omitempty"`
	// ImportSource fields record safe provenance for a backup copied from a
	// first-party predecessor. They never contain a path or backup contents.
	ImportSource   string     `json:"import_source,omitempty"`
	ImportSourceID string     `json:"import_source_id,omitempty"`
	ImportedAt     *time.Time `json:"imported_at,omitempty"`
}

// BackupInfo is the safe list view of a stored backup.
type BackupInfo struct {
	ID              string    `json:"id"`
	Reason          string    `json:"reason"`
	CreatedAt       time.Time `json:"created_at"`
	OriginalExisted bool      `json:"original_existed"`
}

type ApplyResult struct {
	BackupID   string `json:"backup_id"`
	ConfigSHA  string `json:"config_sha256"`
	ProviderID string `json:"provider_id"`
}

// RemoveResult reports the result of explicitly removing the one provider
// entry owned by XIASS Tools. It never includes configuration contents,
// credentials, filesystem paths, or any state belonging to another provider.
//
// Removed is false for a verified no-op. A no-op intentionally does not create
// a backup or rewrite config.toml.
type RemoveResult struct {
	BackupID  string `json:"backup_id,omitempty"`
	ConfigSHA string `json:"config_sha256,omitempty"`
	Removed   bool   `json:"removed"`
	WasActive bool   `json:"was_active"`
}

type RestoreResult struct {
	RestoredBackupID string `json:"restored_backup_id"`
	SafetyBackupID   string `json:"safety_backup_id"`
}

// LegacyBackupKind identifies a first-party XIASS Codex Helper backup format
// that can be copied into the current XIASS Tools backup store. It is only a
// staging/import operation; it never restores active Codex data.
type LegacyBackupKind string

const (
	LegacyBackupConfig  LegacyBackupKind = "config"
	LegacyBackupHistory LegacyBackupKind = "history"
)

// LegacyBackupInfo is a redacted discovery result. No source paths, config
// contents, session lines, database content, API keys, or headers are exposed.
type LegacyBackupInfo struct {
	Kind           LegacyBackupKind `json:"kind"`
	SourceID       string           `json:"source_id"`
	CreatedAt      time.Time        `json:"created_at"`
	Reason         string           `json:"reason,omitempty"`
	TargetProvider string           `json:"target_provider,omitempty"`
	Valid          bool             `json:"valid"`
	Importable     bool             `json:"importable"`
	Message        string           `json:"message,omitempty"`
}

// LegacyImportResult reports the new current-format backup ID after a
// validated, copy-only import. It deliberately omits all secret data.
type LegacyImportResult struct {
	Kind             LegacyBackupKind `json:"kind"`
	SourceID         string           `json:"source_id"`
	ImportedBackupID string           `json:"imported_backup_id"`
	CreatedAt        time.Time        `json:"created_at"`
	Message          string           `json:"message"`
}

// MutationError reports an operation that failed after the target could have
// changed. Callers can use errors.As to surface the rollback state accurately.
type MutationError struct {
	Cause       error
	RollbackErr error
}

func (e *MutationError) Error() string {
	if e.RollbackErr != nil {
		return "Codex configuration mutation failed: " + e.Cause.Error() + "; rollback also failed: " + e.RollbackErr.Error()
	}
	return "Codex configuration mutation failed and was rolled back safely: " + e.Cause.Error()
}

func (e *MutationError) Unwrap() error { return e.Cause }

// ManagerOptions allow a platform UI to use its own application data root and
// deterministic write-safety guard. Historical Provider migration is not an
// option: it is fixed to the two reviewed first-party IDs in
// legacy_provider_migration.go.
type ManagerOptions struct {
	DataDirectoryName string
	ProviderID        string
	// HistoryWriteGuard is intentionally only a narrow, read-only safety
	// predicate. Production uses the platform detector; tests may inject a
	// deterministic guard without starting, stopping, or inspecting secrets.
	HistoryWriteGuard historyWriteGuard
	// legacyProviderMigrationWrite is an internal test seam. Production always
	// uses the same atomic writer as every other config mutation; callers
	// outside this package cannot supply it.
	legacyProviderMigrationWrite func(string, []byte, fs.FileMode) error
}

// Manager owns a single Codex config.toml lifecycle. Its paths are exported so
// the UI can show only the config location, while callers should avoid exposing
// backup internals or secret file contents.
type Manager struct {
	CodexHome                    string
	ConfigPath                   string
	BackupRoot                   string
	LockPath                     string
	ProviderID                   string
	historyWriteGuard            historyWriteGuard
	legacyProviderMigrationWrite func(string, []byte, fs.FileMode) error
}
