package claudeconfig

import (
	"io/fs"
	"time"
)

const (
	settingsFilename      = "settings.json"
	backupManifestVersion = 1
	backupToolName        = "XIASS Tools Claude Code user settings"
	maxSettingsBytes      = 1 << 20
	maxManifestBytes      = 64 << 10
	maxBaseURLBytes       = 2048
	maxAuthTokenBytes     = 8192
	maxModelBytes         = 256
	maxAPIKeyHelperBytes  = 2048
)

// CredentialMode identifies the one authentication mechanism XIASS Tools
// explicitly configures for Claude Code. Claude Code has a documented
// precedence order between these mechanisms, so Apply always writes exactly
// one mode and clears XIASS-managed conflicting values.
type CredentialMode string

const (
	CredentialModeAuthToken    CredentialMode = "auth_token"
	CredentialModeAPIKey       CredentialMode = "api_key"
	CredentialModeAPIKeyHelper CredentialMode = "api_key_helper"
)

// ApplyConfig is the narrow portion of Claude Code user settings managed by
// this package. Credential and AuthToken deliberately have no JSON
// representation so they cannot be accidentally returned through a UI binding
// or event payload. AuthToken is retained as a compatibility input for older
// callers and is treated as a bearer credential when CredentialMode is empty.
type ApplyConfig struct {
	BaseURL                     string         `json:"base_url"`
	CredentialMode              CredentialMode `json:"credential_mode"`
	Credential                  string         `json:"-"`
	AuthToken                   string         `json:"-"`
	APIKeyHelper                string         `json:"-"`
	EnableGatewayModelDiscovery bool           `json:"enable_gateway_model_discovery"`
	Model                       string         `json:"model"`
}

// ConfigLocation identifies the only Claude Code file this manager touches.
type ConfigLocation struct {
	ConfigDir    string `json:"config_dir"`
	SettingsPath string `json:"settings_path"`
	Exists       bool   `json:"exists"`
}

// Snapshot is a redacted, read-only view of settings.json. It never contains
// ANTHROPIC_AUTH_TOKEN or any other environment value.
type Snapshot struct {
	Location                     ConfigLocation `json:"location"`
	SHA256                       string         `json:"sha256,omitempty"`
	Mode                         fs.FileMode    `json:"mode,omitempty"`
	Valid                        bool           `json:"valid"`
	Model                        string         `json:"model,omitempty"`
	BaseURL                      string         `json:"base_url,omitempty"`
	CredentialMode               CredentialMode `json:"credential_mode,omitempty"`
	CredentialConfigured         bool           `json:"credential_configured"`
	AuthTokenConfigured          bool           `json:"auth_token_configured"`
	APIKeyHelperConfigured       bool           `json:"api_key_helper_configured"`
	GatewayModelDiscoveryEnabled bool           `json:"gateway_model_discovery_enabled"`
	// GatewayModelDiscoveryBlocked is a redacted compatibility fact: a
	// user-managed provider routing flag or a nonessential-traffic restriction
	// prevents Claude Code from performing standard ANTHROPIC_BASE_URL model
	// discovery even when the discovery preference itself is saved as enabled.
	GatewayModelDiscoveryBlocked bool `json:"gateway_model_discovery_blocked"`
	Managed                      bool `json:"managed"`
}

// BackupManifest authenticates one private backup without containing the
// settings data, API endpoint, or secret token. TargetSHA256 binds a backup to
// one canonical settings.json path without persisting that path itself.
type BackupManifest struct {
	Version         int       `json:"version"`
	Tool            string    `json:"tool"`
	ID              string    `json:"id"`
	Reason          string    `json:"reason"`
	CreatedAt       time.Time `json:"created_at"`
	TargetSHA256    string    `json:"target_sha256"`
	OriginalExisted bool      `json:"original_existed"`
	OriginalMode    uint32    `json:"original_mode,omitempty"`
	OriginalSHA256  string    `json:"original_sha256,omitempty"`
	AppliedSHA256   string    `json:"applied_sha256,omitempty"`
}

// BackupInfo is safe to return to callers. It deliberately excludes paths and
// all saved settings content.
type BackupInfo struct {
	ID              string    `json:"id"`
	Reason          string    `json:"reason"`
	CreatedAt       time.Time `json:"created_at"`
	OriginalExisted bool      `json:"original_existed"`
}

// LegacyBackupInfo identifies a verified backup from a pre-XIASS Tools
// directory. Source is a stable label rather than a filesystem path. Legacy
// data is read-only until a caller explicitly requests a copy migration.
type LegacyBackupInfo struct {
	Source          string    `json:"source"`
	ID              string    `json:"id"`
	Reason          string    `json:"reason"`
	CreatedAt       time.Time `json:"created_at"`
	OriginalExisted bool      `json:"original_existed"`
}

// ApplyResult contains verification metadata only; it never contains a token.
type ApplyResult struct {
	BackupID       string `json:"backup_id"`
	SettingsSHA256 string `json:"settings_sha256"`
}

// RestoreResult identifies the validated source backup and the safety backup
// created from the setting state that was active before restoration.
type RestoreResult struct {
	RestoredBackupID string `json:"restored_backup_id"`
	SafetyBackupID   string `json:"safety_backup_id"`
}

// ManagerOptions lets an application choose an app-owned backup directory.
// It has no option for credentials, accounts, sessions, projects, or managed
// settings because those paths are intentionally out of scope.
type ManagerOptions struct {
	BackupRoot        string
	LegacyBackupRoots []string
}

// Manager controls a single settings.json lifecycle. The fields are exported
// only to make the selected, user-visible target inspectable; callers must not
// surface the backup directory or use it as a generic file manager.
type Manager struct {
	ConfigDir         string
	SettingsPath      string
	BackupRoot        string
	LockPath          string
	legacyBackupRoots []legacyBackupRoot

	// afterAtomicWriteForTest deterministically exercises rollback without
	// adding a production failure mode or exposing secret-bearing state.
	afterAtomicWriteForTest func() error
}

type legacyBackupRoot struct {
	source string
	path   string
}

// MutationError reports that an already-started mutation was rolled back. Its
// public error text remains generic so validation or filesystem internals can
// never accidentally echo a token.
type MutationError struct {
	Cause       error
	RollbackErr error
}

func (e *MutationError) Error() string {
	if e == nil {
		return "Claude user settings mutation failed"
	}
	if e.RollbackErr != nil {
		return "Claude user settings mutation failed; automatic rollback also failed"
	}
	return "Claude user settings mutation failed and was rolled back safely"
}

func (e *MutationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}
