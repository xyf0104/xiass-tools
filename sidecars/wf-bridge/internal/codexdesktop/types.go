// Package codexdesktop discovers a locally installed Codex Desktop app without
// reading user data, credentials, conversations, or application settings.
//
// Its exported status intentionally contains only redacted observations. In
// particular it never exposes paths, process IDs, process arguments, registry
// values, or underlying operating-system errors.
package codexdesktop

import (
	"context"
	"io/fs"
	"time"
)

const defaultProcessTimeout = 2 * time.Second

// State describes the overall confidence of a discovery result.
type State string

const (
	StateNotInstalled State = "not_installed"
	StateInstalled    State = "installed"
	StateRunning      State = "running"
	StateDegraded     State = "degraded"
)

// Source identifies a bounded, public installation location without exposing
// its actual filesystem path.
type Source string

const (
	SourceSystemApplications Source = "system_applications"
	SourceUserApplications   Source = "user_applications"
	SourceLocalAppData       Source = "local_app_data"
	SourceProgramFiles       Source = "program_files"
	SourceWindowsStore       Source = "windows_store"
	// SourcePublicDiscovery means the application was found through a bounded
	// public OS index or registration rather than a fixed conventional path.
	// It deliberately does not reveal which concrete path or registry value
	// produced the result. macOS uses it for Spotlight and Windows uses it for
	// App Paths registration.
	SourcePublicDiscovery Source = "public_discovery"
	// SourceManualSelection records that the user explicitly selected a
	// structure-validated application through the native picker. The selected
	// path itself is deliberately never included in any public DTO.
	SourceManualSelection Source = "manual_selection"
)

// Warning is a stable, non-sensitive reason that discovery could not make a
// complete observation. Warning values never contain an OS error or a path.
type Warning string

const (
	WarningEnvironmentUnavailable Warning = "environment_unavailable"
	WarningInspectionUnavailable  Warning = "inspection_unavailable"
	WarningInvalidInstallation    Warning = "invalid_public_installation"
	WarningProcessListUnavailable Warning = "process_list_unavailable"
)

// Installation is the redacted result of validating a public application
// location. Version is sourced only from public package metadata and is
// sanitized before being returned.
type Installation struct {
	Present            bool   `json:"present"`
	Source             Source `json:"source,omitempty"`
	Version            string `json:"version,omitempty"`
	ExecutableVerified bool   `json:"executableVerified"`
}

// Status is a read-only snapshot of local Codex Desktop state.
type Status struct {
	State        State        `json:"state"`
	Installation Installation `json:"installation"`
	Running      bool         `json:"running"`
	CheckedAt    time.Time    `json:"checkedAt"`
	Warnings     []Warning    `json:"warnings,omitempty"`
}

// FileSystem is the minimal read-only filesystem surface used by discovery.
// It is injectable to make detection deterministic in tests.
type FileSystem interface {
	Stat(string) (fs.FileInfo, error)
	ReadFile(string) ([]byte, error)
	UserHomeDir() (string, error)
	Getenv(string) string
}

// Process contains only the executable path supplied by a process-listing
// implementation. The detector never returns this path to its caller.
type Process struct {
	Executable string
}

// ProcessLister obtains a read-only process snapshot. Implementations must
// honor ctx so the detector's timeout remains effective.
type ProcessLister interface {
	List(ctx context.Context) ([]Process, error)
}

// BundleFinder is a narrowly scoped public-metadata lookup used only by the
// macOS detector. Production uses a bounded Spotlight query for an exact,
// built-in bundle identifier; the result is never returned without the normal
// Info.plist and executable validation. It never receives a user-controlled
// query or reads application data.
//
// It remains in the shared Options type so callers can inject deterministic
// test doubles while each platform keeps its own discovery implementation.
type BundleFinder interface {
	FindBundles(ctx context.Context, bundleIdentifier string, limit int) ([]string, error)
}

// Registry is a deliberately narrow read-only registry surface. It is used by
// the Windows implementation for public Store package registration and is
// ignored on macOS.
type Registry interface {
	Subkeys(path string, limit int) ([]string, error)
}

// Options supplies optional test doubles. Nil dependencies use the platform's
// built-in read-only implementations.
type Options struct {
	FileSystem     FileSystem
	Processes      ProcessLister
	Registry       Registry
	BundleFinder   BundleFinder
	Now            func() time.Time
	ProcessTimeout time.Duration
}

// Detector performs bounded, read-only local discovery.
type Detector struct {
	options Options
}

// New creates a Codex Desktop detector. Production callers normally pass no
// options; dependencies can be injected for tests.
func New(options ...Options) *Detector {
	var option Options
	if len(options) > 0 {
		option = options[0]
	}
	return &Detector{options: option}
}

// NewDetector is an explicit alias for New for integrations that prefer a
// descriptive constructor name.
func NewDetector(options ...Options) *Detector {
	return New(options...)
}

// Discover is a convenience wrapper around New(options...).Discover(ctx).
func Discover(ctx context.Context, options ...Options) Status {
	return New(options...).Discover(ctx)
}

func (detector *Detector) fileSystem() FileSystem {
	if detector != nil && detector.options.FileSystem != nil {
		return detector.options.FileSystem
	}
	return systemFileSystem{}
}

func (detector *Detector) processLister() ProcessLister {
	if detector != nil && detector.options.Processes != nil {
		return detector.options.Processes
	}
	return systemProcessLister{}
}

func (detector *Detector) processTimeout() time.Duration {
	if detector != nil && detector.options.ProcessTimeout > 0 {
		return detector.options.ProcessTimeout
	}
	return defaultProcessTimeout
}

func (detector *Detector) now() time.Time {
	if detector != nil && detector.options.Now != nil {
		return detector.options.Now().UTC()
	}
	return time.Now().UTC()
}

func addWarning(status *Status, warning Warning) {
	for _, current := range status.Warnings {
		if current == warning {
			return
		}
	}
	status.Warnings = append(status.Warnings, warning)
}
