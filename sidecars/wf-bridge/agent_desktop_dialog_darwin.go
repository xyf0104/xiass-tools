//go:build darwin

package main

import (
	"context"
	"errors"
	"strings"

	"antigravity-wf-assistant/internal/agent"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// selectAgentDesktopNativeTarget is deliberately native-only. It never accepts
// a Vue path string, and the generic file pattern is still followed by strict
// Go-side bundle identifier/executable verification.
func selectAgentDesktopNativeTarget(app *App, ctx context.Context, identifier agent.ID) (string, bool, error) {
	title, pattern, ok := agentDesktopDialogOptions(identifier)
	if !ok {
		return "", false, errors.New("unsupported desktop application")
	}
	selected, err := app.openFileDialog(runtime.OpenDialogOptions{
		Title:                      title,
		DefaultDirectory:           "/Applications",
		ResolvesAliases:            true,
		TreatPackagesAsDirectories: false,
		Filters: []runtime.FileFilter{{
			DisplayName: "受支持的应用 (*.app)",
			Pattern:     pattern,
		}},
	})
	if err != nil {
		return "", false, err
	}
	if strings.TrimSpace(selected) == "" {
		return "", true, nil
	}
	return selected, false, nil
}

func agentDesktopDialogOptions(identifier agent.ID) (title, pattern string, ok bool) {
	switch identifier {
	case agent.CursorID:
		return "选择 Cursor 应用", "Cursor.app;Cursor Nightly.app", true
	case agent.WindsurfID:
		return "选择 Windsurf 应用", "Windsurf.app", true
	default:
		return "", "", false
	}
}
