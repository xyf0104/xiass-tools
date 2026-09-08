// Package agentdiscovery provides conservative, read-only local discovery
// adapters for supported coding agents. It intentionally does not read
// credentials, inspect conversations, mutate configuration, or implement
// provider/OAuth/account operations.
package agentdiscovery

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"antigravity-wf-assistant/internal/agent"
)

const defaultVersionTimeout = 2 * time.Second

var versionPattern = regexp.MustCompile(`(?i)(?:^|[^0-9])v?(\d+(?:\.\d+){1,3})(?:[^0-9]|$)`)

// FileSystem is the minimal read-only filesystem surface needed by discovery.
// Keeping it injectable makes detection deterministic in tests and prevents an
// adapter from growing accidental write capabilities.
type FileSystem interface {
	Stat(string) (fs.FileInfo, error)
	ReadFile(string) ([]byte, error)
	UserHomeDir() (string, error)
	Getenv(string) string
}

// CommandRunner is the bounded command surface used only to find Claude Code
// and ask it for its public version. Implementations must respect ctx.
type CommandRunner interface {
	LookPath(string) (string, error)
	Output(context.Context, string, ...string) ([]byte, error)
}

// VersionReader reads a desktop application's public product version. An
// empty version is valid because version metadata is optional for discovery.
type VersionReader func(FileSystem, string) (string, error)

// Options supplies test doubles for an adapter. Nil members are replaced with
// the operating system's read-only implementations.
type Options struct {
	FileSystem     FileSystem
	Commands       CommandRunner
	ReadVersion    VersionReader
	Now            func() time.Time
	VersionTimeout time.Duration
}

// Adapter is a concrete agent.Adapter that only reports observed local state.
// It deliberately does not implement the registry's optional write, backup,
// OAuth, quota, or account-provider interfaces.
type Adapter struct {
	id      agent.ID
	options Options
}

var _ agent.Adapter = (*Adapter)(nil)

// NewClaudeCodeAdapter returns a read-only Claude Code detector. The optional
// options parameter exists for deterministic tests; production callers should
// call it with no arguments.
func NewClaudeCodeAdapter(options ...Options) *Adapter {
	return newAdapter(agent.ClaudeCodeID, firstOptions(options))
}

// DiscoverClaudeCodeCLI resolves the installed Claude Code command using the
// same platform-aware, inspection-only path rules as the read-only adapter.
// PATH is checked first. Known fallback files are only stat'ed and are never
// executed, which lets the production Claude configuration adapter detect a
// newly installed npm/user-local CLI before the desktop process inherits an
// updated shell PATH.
func DiscoverClaudeCodeCLI() (string, error) {
	path, _, err := resolveClaudeCodeCLI(systemFileSystem{}, systemCommandRunner{})
	return path, err
}

// NewCursorAdapter returns a read-only Cursor detector.
func NewCursorAdapter(options ...Options) *Adapter {
	return newAdapter(agent.CursorID, firstOptions(options))
}

// NewWindsurfAdapter returns a read-only Windsurf detector.
func NewWindsurfAdapter(options ...Options) *Adapter {
	return newAdapter(agent.WindsurfID, firstOptions(options))
}

// NewAdapters returns the three non-Antigravity adapters in stable UI order.
// It does not register them; the application controls binding explicitly.
func NewAdapters(options ...Options) []agent.Adapter {
	option := firstOptions(options)
	return []agent.Adapter{
		newAdapter(agent.ClaudeCodeID, option),
		newAdapter(agent.CursorID, option),
		newAdapter(agent.WindsurfID, option),
	}
}

func newAdapter(id agent.ID, options Options) *Adapter {
	return &Adapter{id: id, options: options}
}

func firstOptions(options []Options) Options {
	if len(options) == 0 {
		return Options{}
	}
	return options[0]
}

// Metadata returns the existing public XIASS Tools profile. It does not
// broaden the capability matrix or claim that an account-related feature is
// implemented.
func (adapter *Adapter) Metadata() agent.Metadata {
	for _, metadata := range agent.BuiltinMetadata() {
		if metadata.ID == adapter.id {
			return metadata
		}
	}
	return agent.Metadata{
		ID:          adapter.id,
		DisplayName: string(adapter.id),
		Category:    agent.CategoryTerminal,
	}
}

// Detect performs bounded, read-only observation. Expected local failures are
// represented as StateDegraded instead of returned errors so registry-wide
// discovery can continue with the remaining agents.
func (adapter *Adapter) Detect(ctx context.Context) (agent.Status, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	switch adapter.id {
	case agent.ClaudeCodeID:
		return adapter.detectClaudeCode(ctx), nil
	case agent.CursorID:
		return adapter.detectDesktop(cursorSpec), nil
	case agent.WindsurfID:
		return adapter.detectDesktop(windsurfSpec), nil
	default:
		return adapter.newStatus(agent.StateDegraded, "This adapter has no local discovery implementation."), nil
	}
}

func (adapter *Adapter) fileSystem() FileSystem {
	if adapter != nil && adapter.options.FileSystem != nil {
		return adapter.options.FileSystem
	}
	return systemFileSystem{}
}

func (adapter *Adapter) commandRunner() CommandRunner {
	if adapter != nil && adapter.options.Commands != nil {
		return adapter.options.Commands
	}
	return systemCommandRunner{}
}

func (adapter *Adapter) versionReader() VersionReader {
	if adapter != nil && adapter.options.ReadVersion != nil {
		return adapter.options.ReadVersion
	}
	return readDesktopProductVersion
}

func (adapter *Adapter) timeout() time.Duration {
	if adapter != nil && adapter.options.VersionTimeout > 0 {
		return adapter.options.VersionTimeout
	}
	return defaultVersionTimeout
}

func (adapter *Adapter) now() time.Time {
	if adapter != nil && adapter.options.Now != nil {
		return adapter.options.Now().UTC()
	}
	return time.Now().UTC()
}

func (adapter *Adapter) newStatus(state agent.State, message string) agent.Status {
	metadata := adapter.Metadata()
	status := agent.Status{
		AgentID:     metadata.ID,
		DisplayName: metadata.DisplayName,
		State:       state,
		Message:     message,
		UpdatedAt:   adapter.now(),
	}
	status.Capabilities = discoveryCapabilities(metadata)
	return status
}

func discoveryCapabilities(metadata agent.Metadata) []agent.CapabilityStatus {
	statuses := make([]agent.CapabilityStatus, 0, len(metadata.Capabilities))
	for _, declaration := range metadata.Capabilities {
		status := agent.CapabilityStatus{
			Capability:   declaration.Capability,
			Availability: declaration.Availability,
			Available:    false,
			Reason:       "This read-only adapter does not implement this capability.",
		}
		if declaration.Capability == agent.CapabilityDiscovery {
			status.Availability = agent.CapabilityAvailable
			status.Available = true
			status.Reason = "Local read-only discovery completed."
		}
		statuses = append(statuses, status)
	}
	return statuses
}

func (adapter *Adapter) detectClaudeCode(ctx context.Context) agent.Status {
	filesystem := adapter.fileSystem()
	runner := adapter.commandRunner()
	cliPath, cliFromPATH, cliErr := resolveClaudeCodeCLI(filesystem, runner)

	home, homeErr := filesystem.UserHomeDir()
	configPath := ""
	var configErr error
	if homeErr == nil && strings.TrimSpace(home) != "" {
		candidate := joinClaudeConfigPath(home)
		present, err := existingDirectory(filesystem, candidate)
		configErr = err
		if present {
			configPath = candidate
		}
	}

	issues := make([]string, 0, 3)
	if cliErr != nil {
		issues = append(issues, "the Claude command could not be resolved")
	}
	if homeErr != nil || strings.TrimSpace(home) == "" {
		issues = append(issues, "the home directory could not be resolved")
	}
	if configErr != nil {
		issues = append(issues, "the Claude configuration directory could not be read")
	}

	version := ""
	if cliPath != "" && cliFromPATH && cliErr == nil {
		// A PATH-resolved `claude` is the only candidate we invoke during
		// discovery. Known install-location fallbacks are deliberately
		// inspection-only: a malicious or stale binary in one of those paths
		// must never be executed merely because XIASS Tools opened.
		commandContext, cancel := context.WithTimeout(ctx, adapter.timeout())
		output, err := runner.Output(commandContext, cliPath, "--version")
		cancel()
		if err != nil {
			issues = append(issues, "the Claude version command did not complete")
		} else {
			version = parseVersion(string(output))
		}
	}

	configFound := configPath != ""
	cliFound := cliPath != "" && cliErr == nil
	if !cliFound && !configFound {
		if len(issues) > 0 {
			return adapter.newStatus(agent.StateDegraded, "Claude Code discovery is incomplete: "+strings.Join(issues, "; ")+".")
		}
		return adapter.newStatus(agent.StateNotInstalled, "Claude Code was not found on PATH and no ~/.claude directory was found.")
	}

	state := agent.StateDetected
	message := "Claude Code was partially detected."
	if cliFound && configFound {
		state = agent.StateReady
		message = "Claude Code CLI and local configuration directory were found."
	}
	if len(issues) > 0 {
		state = agent.StateDegraded
		message = "Claude Code was found, but discovery is incomplete: " + strings.Join(issues, "; ") + "."
	}
	status := adapter.newStatus(state, message)
	status.Installation.ExecutablePath = cliPath
	status.Installation.Version = version
	status.Installation.Platform = "macos"
	if configFound {
		status.Installation.Root = configPath
	} else if cliFound {
		status.Installation.Root = pathDirectory(cliPath)
	}
	if configFound {
		status.Details = map[string]string{"configurationDirectory": configPath}
	}
	return status
}

// resolveClaudeCodeCLI checks PATH first, then a small platform-owned list of
// conventional Claude Code locations. The fallback list is a bounded set of
// exact filenames; it does not search directories, enumerate a shell PATH, or
// run anything it finds. That makes a normal discovery pass useful after a
// shell profile changed while still keeping startup read-only.
func resolveClaudeCodeCLI(filesystem FileSystem, runner CommandRunner) (path string, fromPATH bool, err error) {
	path, err = runner.LookPath("claude")
	path = strings.TrimSpace(path)
	if err == nil && path != "" {
		return path, true, nil
	}
	if errors.Is(err, exec.ErrNotFound) {
		err = nil
	}
	lookupErr := err
	var candidateErr error
	for _, candidate := range knownClaudeCodeCLICandidates(filesystem) {
		present, inspectErr := existingRegularFile(filesystem, candidate)
		if inspectErr != nil {
			if candidateErr == nil {
				candidateErr = inspectErr
			}
			continue
		}
		if present {
			return candidate, false, nil
		}
	}
	if lookupErr != nil {
		return "", false, lookupErr
	}
	return "", false, candidateErr
}

func parseVersion(value string) string {
	match := versionPattern.FindStringSubmatch(value)
	if len(match) != 2 {
		return ""
	}
	return match[1]
}

func existingDirectory(filesystem FileSystem, path string) (bool, error) {
	if strings.TrimSpace(path) == "" {
		return false, nil
	}
	info, err := filesystem.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if !info.IsDir() {
		return false, errors.New("expected a directory")
	}
	return true, nil
}

func existingRegularFile(filesystem FileSystem, path string) (bool, error) {
	if strings.TrimSpace(path) == "" {
		return false, nil
	}
	info, err := filesystem.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, errors.New("expected a regular file")
	}
	return true, nil
}

type systemFileSystem struct{}

func (systemFileSystem) Stat(path string) (fs.FileInfo, error) { return os.Stat(path) }
func (systemFileSystem) ReadFile(path string) ([]byte, error)  { return os.ReadFile(path) }
func (systemFileSystem) UserHomeDir() (string, error)          { return os.UserHomeDir() }
func (systemFileSystem) Getenv(key string) string              { return os.Getenv(key) }

type systemCommandRunner struct{}

func (systemCommandRunner) LookPath(name string) (string, error) { return exec.LookPath(name) }
func (systemCommandRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}
