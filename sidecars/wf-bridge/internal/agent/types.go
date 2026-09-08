// Package agent contains the platform-neutral contract used to integrate
// external coding agents with XIASS Tools. It intentionally contains no
// product-specific filesystem, OAuth, quota, or patching implementation.
package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ID is a stable, machine-readable identifier for an agent integration.
// IDs are lower-case ASCII words separated by hyphens.
type ID string

const (
	AntigravityID ID = "antigravity"
	CodexID       ID = "codex"
	ClaudeCodeID  ID = "claude-code"
	CursorID      ID = "cursor"
	WindsurfID    ID = "windsurf"
)

// Category describes how an agent is normally presented to the user. It is
// metadata only and does not imply that XIASS Tools can configure the product.
type Category string

const (
	CategoryDesktopIDE Category = "desktop-ide"
	CategoryCodeEditor Category = "code-editor"
	CategoryTerminal   Category = "terminal-agent"
)

// Capability identifies an integration feature. A capability declaration says
// whether an adapter may provide the feature; runtime Status is the authority
// for whether it is available on a particular machine.
type Capability string

const (
	CapabilityDiscovery       Capability = "installation-discovery"
	CapabilityConfiguration   Capability = "configuration"
	CapabilityLocalProxy      Capability = "local-proxy"
	CapabilityPatchInjection  Capability = "patch-injection"
	CapabilityModelCatalog    Capability = "model-catalog"
	CapabilitySessionRecovery Capability = "session-recovery"
	CapabilityImageIO         Capability = "image-input-output"
	CapabilityDiagnostics     Capability = "diagnostics"
	CapabilityBackup          Capability = "backup"
	CapabilityOAuth           Capability = "oauth"
	CapabilityUsage           Capability = "usage"
	CapabilityTwoFactorAuth   Capability = "two-factor-authentication"
)

// CapabilityAvailability differentiates a product's public integration plan
// from a feature that has been bound and verified at runtime. In particular,
// RequiresBinding and NotImplemented must never be rendered as usable.
type CapabilityAvailability string

const (
	CapabilityAvailable       CapabilityAvailability = "available"
	CapabilityRequiresBinding CapabilityAvailability = "requires-binding"
	CapabilityNotImplemented  CapabilityAvailability = "not-implemented"
	CapabilityNotApplicable   CapabilityAvailability = "not-applicable"
)

// CapabilityDeclaration is the static capability matrix for an integration.
// It is deliberately conservative: adapters promote a capability to Available
// only through a live Status result after their implementation is bound.
type CapabilityDeclaration struct {
	Capability   Capability             `json:"capability"`
	Availability CapabilityAvailability `json:"availability"`
	Summary      string                 `json:"summary,omitempty"`
}

// Metadata is safe, public information used to describe an integration. It
// does not contain paths, credentials, tokens, account details, or logos.
type Metadata struct {
	ID           ID                      `json:"id"`
	DisplayName  string                  `json:"displayName"`
	Vendor       string                  `json:"vendor,omitempty"`
	Category     Category                `json:"category"`
	Description  string                  `json:"description,omitempty"`
	Capabilities []CapabilityDeclaration `json:"capabilities"`
}

// Installation captures only the discovered location and product version. An
// adapter should omit sensitive fields rather than putting them in Details.
type Installation struct {
	Root           string `json:"root,omitempty"`
	ExecutablePath string `json:"executablePath,omitempty"`
	Version        string `json:"version,omitempty"`
	Platform       string `json:"platform,omitempty"`
}

// State is the lifecycle state reported by an adapter. Unbound is a valid,
// non-error state for built-in metadata that has not yet received an adapter.
type State string

const (
	StateUnbound      State = "unbound"
	StateUnknown      State = "unknown"
	StateNotInstalled State = "not-installed"
	StateDetected     State = "detected"
	StateReady        State = "ready"
	StateDegraded     State = "degraded"
	StateError        State = "error"
)

// CapabilityStatus is the per-machine status of a declared capability.
type CapabilityStatus struct {
	Capability   Capability             `json:"capability"`
	Availability CapabilityAvailability `json:"availability"`
	Available    bool                   `json:"available"`
	Reason       string                 `json:"reason,omitempty"`
}

// Status is returned by an adapter's bounded detection pass. It must describe
// observed state; it must not treat configuration intent as successful setup.
type Status struct {
	AgentID      ID                 `json:"agentId"`
	DisplayName  string             `json:"displayName,omitempty"`
	State        State              `json:"state"`
	Message      string             `json:"message,omitempty"`
	Installation Installation       `json:"installation,omitempty"`
	Capabilities []CapabilityStatus `json:"capabilities"`
	Details      map[string]string  `json:"details,omitempty"`
	UpdatedAt    time.Time          `json:"updatedAt"`
}

// DiagnosticSeverity controls how a diagnostic is presented. Diagnostics are
// intentionally structured so a future desktop UI can group and export them.
type DiagnosticSeverity string

const (
	SeverityInfo    DiagnosticSeverity = "info"
	SeverityWarning DiagnosticSeverity = "warning"
	SeverityError   DiagnosticSeverity = "error"
)

// Diagnostic describes an actionable observation without exposing secrets.
type Diagnostic struct {
	AgentID     ID                 `json:"agentId"`
	Code        string             `json:"code"`
	Severity    DiagnosticSeverity `json:"severity"`
	Summary     string             `json:"summary"`
	Detail      string             `json:"detail,omitempty"`
	Remediation string             `json:"remediation,omitempty"`
	Details     map[string]string  `json:"details,omitempty"`
	CreatedAt   time.Time          `json:"createdAt"`
}

// BackupRequest lets a caller scope a future adapter backup. The registry does
// not create filesystem backups itself; only a bound BackupProvider may do so.
type BackupRequest struct {
	Destination      string `json:"destination,omitempty"`
	Reason           string `json:"reason,omitempty"`
	IncludeConfig    bool   `json:"includeConfig"`
	IncludeSessions  bool   `json:"includeSessions"`
	IncludeWorkspace bool   `json:"includeWorkspace"`
}

// BackupFile is an inventory entry in a backup result. SHA256 is optional and
// should be populated only when the provider actually computed it.
type BackupFile struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
	SHA256      string `json:"sha256,omitempty"`
	Size        int64  `json:"size,omitempty"`
}

// Backup is a provider-created backup manifest. Providers are responsible for
// using transaction-safe filesystem operations and for excluding credentials
// from diagnostic metadata.
type Backup struct {
	ID        string            `json:"id"`
	AgentID   ID                `json:"agentId"`
	CreatedAt time.Time         `json:"createdAt"`
	Root      string            `json:"root,omitempty"`
	Files     []BackupFile      `json:"files,omitempty"`
	Details   map[string]string `json:"details,omitempty"`
}

// Operation is a future-facing, explicitly requested adapter operation. The
// registry exposes it only when an adapter implements OperationProvider.
type Operation string

const (
	OperationConfigure Operation = "configure"
	OperationApply     Operation = "apply"
	OperationRemove    Operation = "remove"
	OperationRefresh   Operation = "refresh"
)

// OperationRequest contains opaque, non-secret options for a provider. Secret
// material belongs in the platform's secure storage, never in this structure.
type OperationRequest struct {
	Operation Operation         `json:"operation"`
	Options   map[string]string `json:"options,omitempty"`
}

// OperationResult records the observable outcome of a provider operation.
type OperationResult struct {
	AgentID   ID                `json:"agentId"`
	Operation Operation         `json:"operation"`
	Message   string            `json:"message,omitempty"`
	Details   map[string]string `json:"details,omitempty"`
}

// Adapter is the minimal runtime binding. Its Metadata is registered with the
// platform-neutral registry, and Detect reports observed local state.
type Adapter interface {
	Metadata() Metadata
	Detect(context.Context) (Status, error)
}

// DiagnosticProvider is an optional adapter extension.
type DiagnosticProvider interface {
	Diagnose(context.Context) ([]Diagnostic, error)
}

// BackupProvider is an optional adapter extension.
type BackupProvider interface {
	Backup(context.Context, BackupRequest) (Backup, error)
}

// OperationProvider is an optional adapter extension used for future concrete
// configuration, apply, remove, and refresh bindings.
type OperationProvider interface {
	Perform(context.Context, OperationRequest) (OperationResult, error)
}

// Clone returns a detached copy suitable for callers. Metadata is exposed from
// a shared registry, so callers must never receive its backing slices.
func (m Metadata) Clone() Metadata {
	clone := m
	clone.Capabilities = append([]CapabilityDeclaration(nil), m.Capabilities...)
	return clone
}

// Validate checks public metadata before it can enter the registry.
func (m Metadata) Validate() error {
	if err := m.ID.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(m.DisplayName) == "" {
		return fmt.Errorf("agent %q: display name is required", m.ID)
	}
	switch m.Category {
	case CategoryDesktopIDE, CategoryCodeEditor, CategoryTerminal:
	default:
		return fmt.Errorf("agent %q: unsupported category %q", m.ID, m.Category)
	}
	seen := make(map[Capability]struct{}, len(m.Capabilities))
	for _, declaration := range m.Capabilities {
		if declaration.Capability == "" {
			return fmt.Errorf("agent %q: capability is required", m.ID)
		}
		if _, exists := seen[declaration.Capability]; exists {
			return fmt.Errorf("agent %q: duplicate capability %q", m.ID, declaration.Capability)
		}
		seen[declaration.Capability] = struct{}{}
		switch declaration.Availability {
		case CapabilityAvailable, CapabilityRequiresBinding, CapabilityNotImplemented, CapabilityNotApplicable:
		default:
			return fmt.Errorf("agent %q: unsupported availability %q", m.ID, declaration.Availability)
		}
	}
	return nil
}

// Validate checks that an ID is stable and portable across platforms.
func (id ID) Validate() error {
	value := string(id)
	if value == "" {
		return errors.New("agent id is required")
	}
	if value != strings.ToLower(value) {
		return fmt.Errorf("agent id %q must be lower-case", id)
	}
	if strings.HasPrefix(value, "-") || strings.HasSuffix(value, "-") {
		return fmt.Errorf("agent id %q cannot begin or end with a hyphen", id)
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '-' {
			continue
		}
		return fmt.Errorf("agent id %q contains unsupported character %q", id, character)
	}
	return nil
}

func cloneDetails(details map[string]string) map[string]string {
	if len(details) == 0 {
		return nil
	}
	clone := make(map[string]string, len(details))
	for key, value := range details {
		clone[key] = value
	}
	return clone
}

func cloneStatus(status Status) Status {
	clone := status
	clone.Capabilities = append([]CapabilityStatus(nil), status.Capabilities...)
	clone.Details = cloneDetails(status.Details)
	return clone
}

func cloneDiagnostic(diagnostic Diagnostic) Diagnostic {
	clone := diagnostic
	clone.Details = cloneDetails(diagnostic.Details)
	return clone
}

func cloneBackup(backup Backup) Backup {
	clone := backup
	clone.Files = append([]BackupFile(nil), backup.Files...)
	clone.Details = cloneDetails(backup.Details)
	return clone
}
