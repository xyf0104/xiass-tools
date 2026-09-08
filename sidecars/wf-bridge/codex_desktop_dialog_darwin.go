//go:build darwin

package main

import (
	"context"
	"strings"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// selectCodexDesktopNativeTarget keeps a chooser path on the Go side. The
// native panel selects an application package rather than allowing the Vue
// renderer to enumerate or retain local filesystem locations.
func selectCodexDesktopNativeTarget(app *App, ctx context.Context) (string, bool, error) {
	selected, err := app.openFileDialog(runtime.OpenDialogOptions{
		Title:                      "选择 Codex 或 ChatGPT Desktop 应用",
		DefaultDirectory:           "/Applications",
		ResolvesAliases:            true,
		TreatPackagesAsDirectories: false,
		Filters: []runtime.FileFilter{{
			DisplayName: "Codex / ChatGPT 应用 (*.app)",
			Pattern:     "*.app",
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
