package mcpconfig

import (
	"errors"
	"io/fs"
	"time"
)

const (
	// ManagedServerID is reserved for the one remote MCP entry XIASS Tools may
	// create or update. It never owns, removes, or rewrites other entries.
	ManagedServerID = "xiass-tools"

	maxConfigurationBytes = int64(1 << 20)
	maxManifestBytes      = int64(64 << 10)
	backupDirectoryName   = "mcp-backups"
	lockFilename          = "mcp-config.operation.lock"
	backupManifestVersion = 1
)

// configurationScope distinguishes the documented global MCP file from a
// user-selected Cursor project file. It stays internal: paths and project
// identities must never be serialized to the renderer.
type configurationScope string

const (
	configurationScopeGlobal  configurationScope = "global"
	configurationScopeProject configurationScope = "project"
)

// Target identifies one documented client MCP configuration file.
type Target string

const (
	TargetCursor   Target = "cursor"
	TargetWindsurf Target = "windsurf"
)

var (
	ErrUnsupportedTarget    = errors.New("MCP configuration target is unsupported")
	ErrUnavailable          = errors.New("MCP configuration is unavailable")
	ErrInvalidConfiguration = errors.New("MCP configuration is invalid")
	ErrUnsafeConfiguration  = errors.New("MCP configuration cannot be safely modified")
	ErrInvalidRemote        = errors.New("MCP remote endpoint is unsupported")
	ErrOperationBusy        = errors.New("another MCP configuration operation is already running")
	ErrOperation            = errors.New("MCP configuration operation failed")
)

// Snapshot is intentionally redacted. It reports only structural state and
// never contains a configuration path, URL, command, argument, header, env
// value, account, key, OAuth data, or other client-private state.
type Snapshot struct {
	Target                    Target `json:"target"`
	Exists                    bool   `json:"exists"`
	Valid                     bool   `json:"valid"`
	ServerCount               int    `json:"serverCount"`
	ManagedServerConfigured   bool   `json:"managedServerConfigured"`
	HasSensitiveConfiguration bool   `json:"hasSensitiveConfiguration"`
}

// ApplyInput contains the user-supplied remote endpoint for the reserved
// server entry. It is consumed only by ApplyRemote and is never echoed by a
// result, status, error, backup manifest, or diagnostic.
type ApplyInput struct {
	RemoteURL string
}

// ApplyResult reports only safe completion state. Backup identifiers and
// storage paths are intentionally not exposed to callers.
type ApplyResult struct {
	Snapshot      Snapshot `json:"snapshot"`
	BackupCreated bool     `json:"backupCreated"`
}

// RemoveResult reports the outcome of an explicit removal of the one MCP key
// reserved for XIASS Tools. It never identifies, inspects, or claims ownership
// of any other entry. When Removed is false, no configuration file or recovery
// point was created by this operation.
type RemoveResult struct {
	Snapshot      Snapshot `json:"snapshot"`
	BackupCreated bool     `json:"backupCreated"`
	Removed       bool     `json:"removed"`
}

// BackupInfo is the only backup metadata exposed to callers. It contains no
// file paths, URLs, headers, environment data, configuration contents, or
// checksums, so it is safe for a future renderer to list recovery points.
type BackupInfo struct {
	ID string `json:"id"`
	// CreatedAt is deliberately serialized as RFC3339Nano text. The manifest
	// retains its native time.Time for integrity checks, while this renderer DTO
	// avoids a Wails binding-generator fallback to an untyped time namespace.
	CreatedAt       string `json:"createdAt"`
	Reason          string `json:"reason"`
	OriginalExisted bool   `json:"originalExisted"`
}

// RestoreResult confirms a completed restore without exposing either the
// restored endpoint or the private on-disk safety-backup location.
type RestoreResult struct {
	Snapshot      Snapshot `json:"snapshot"`
	BackupCreated bool     `json:"backupCreated"`
}

type backupManifest struct {
	Version         int       `json:"version"`
	ID              string    `json:"id"`
	Target          Target    `json:"target"`
	CreatedAt       time.Time `json:"createdAt"`
	Reason          string    `json:"reason"`
	OriginalExisted bool      `json:"originalExisted"`
	OriginalMode    uint32    `json:"originalMode,omitempty"`
	OriginalSHA256  string    `json:"originalSHA256,omitempty"`
	AppliedSHA256   string    `json:"appliedSHA256,omitempty"`
	// Scope and ProjectID make a copied recovery point unusable outside the
	// exact configuration scope that created it. ProjectID is a one-way digest
	// rather than a local filesystem path.
	Scope     configurationScope `json:"scope,omitempty"`
	ProjectID string             `json:"projectId,omitempty"`
}

type verifiedBackup struct {
	manifest backupManifest
	data     []byte
}

type manager struct {
	target      Target
	scope       configurationScope
	userHome    string
	appConfig   string
	projectRoot string
	projectID   string
	configPath  string
	backupRoot  string
	lockPath    string

	// Tests may force a post-write failure to assert that rollback restores the
	// original configuration. Production managers never set this hook.
	afterAtomicWriteForTest func() error
	// Tests may simulate an external configuration change after a recovery
	// point is created but before the guarded write begins. Production managers
	// never set this hook; it proves that removal fails closed rather than
	// overwriting a concurrent client-side edit.
	beforeRemoveWriteForTest func() error
}

func defaultMode(mode fs.FileMode) fs.FileMode {
	if mode == 0 {
		return 0o600
	}
	mode = mode.Perm() & 0o700
	if mode&0o600 != 0o600 {
		mode |= 0o600
	}
	return mode
}
