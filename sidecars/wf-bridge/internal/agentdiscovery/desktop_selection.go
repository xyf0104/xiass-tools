package agentdiscovery

import (
	"errors"
	"strings"

	"antigravity-wf-assistant/internal/agent"
)

// ErrDesktopSelectionRejected intentionally carries no local pathname. A
// native picker selection may contain a private volume or user directory, so
// callers must reduce this error to stable public copy before crossing a UI
// boundary.
var ErrDesktopSelectionRejected = errors.New("desktop application selection was rejected")

// DesktopSelection is a short-lived, Go-side capability for a Cursor or
// Windsurf installation selected through a native dialog. All path-bearing
// fields are intentionally unexported: JSON/Wails cannot serialize them, and
// the renderer never sends one back for execution.
type DesktopSelection struct {
	agentID      agent.ID
	root         string
	executable   string
	launchTarget string
	version      string
}

func (selection DesktopSelection) AgentID() agent.ID { return selection.agentID }
func (selection DesktopSelection) Version() string   { return selection.version }

// LaunchTarget is for the native Go lifecycle layer only. It returns the
// verified .app bundle on macOS and the verified .exe on Windows; it is never
// placed in a Wails result.
func (selection DesktopSelection) LaunchTarget() string { return selection.launchTarget }

func (selection DesktopSelection) valid() bool {
	return (selection.agentID == agent.CursorID || selection.agentID == agent.WindsurfID) &&
		strings.TrimSpace(selection.root) != "" &&
		strings.TrimSpace(selection.executable) != "" &&
		strings.TrimSpace(selection.launchTarget) != ""
}

// ValidateDesktopSelection structurally verifies a Cursor or Windsurf target
// chosen by the native file dialog. It accepts no renderer-provided path in
// production: the App calls this only with the dialog's result.
func ValidateDesktopSelection(identifier agent.ID, selectedPath string) (DesktopSelection, error) {
	return validateDesktopSelectionWithFileSystem(systemFileSystem{}, identifier, selectedPath)
}

// RevalidateDesktopSelection repeats the exact structural checks immediately
// before launch. A user may replace an app bundle after choosing it, so a prior
// successful selection is never treated as permanent trust.
func RevalidateDesktopSelection(selection DesktopSelection) (DesktopSelection, error) {
	if !selection.valid() {
		return DesktopSelection{}, ErrDesktopSelectionRejected
	}
	return ValidateDesktopSelection(selection.agentID, selection.root)
}

func validateDesktopSelectionWithFileSystem(filesystem FileSystem, identifier agent.ID, selectedPath string) (DesktopSelection, error) {
	if filesystem == nil || strings.ContainsRune(selectedPath, '\x00') {
		return DesktopSelection{}, ErrDesktopSelectionRejected
	}
	spec, ok := desktopSpecForID(identifier)
	if !ok {
		return DesktopSelection{}, ErrDesktopSelectionRejected
	}
	selection, err := validatePlatformDesktopSelection(filesystem, spec, selectedPath)
	if err != nil || !selection.valid() || selection.agentID != identifier {
		return DesktopSelection{}, ErrDesktopSelectionRejected
	}
	return selection, nil
}

func desktopSpecForID(identifier agent.ID) (desktopSpec, bool) {
	switch identifier {
	case agent.CursorID:
		return cursorSpec, true
	case agent.WindsurfID:
		return windsurfSpec, true
	default:
		return desktopSpec{}, false
	}
}
