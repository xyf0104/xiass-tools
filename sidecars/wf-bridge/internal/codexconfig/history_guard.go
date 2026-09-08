package codexconfig

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"antigravity-wf-assistant/internal/codexdesktop"
)

// ErrCodexHistoryWriteUnsafe means local Codex history must remain untouched.
// The caller must never respond by closing or killing another application; it
// should ask the user to exit Codex themselves and retry deliberately.
var ErrCodexHistoryWriteUnsafe = errors.New("Codex is still running; quit it yourself before changing local Codex history")

type historyWriteGuard func() error

func defaultHistoryWriteGuard() error {
	// Unit tests only operate in isolated temporary homes. Avoid making their
	// outcome depend on whether a developer happens to have Codex open.
	if runningUnderGoTest() {
		return nil
	}
	return ensureCodexHistoryWriteSafe()
}

// ensureCodexHistoryWriteSafe makes a bounded, read-only observation before
// changing any local rollout, SQLite, or workspace data. A partial observation
// is intentionally treated as unsafe: no repair is better than risking a
// concurrent write with the user's still-running Codex Desktop app.
func ensureCodexHistoryWriteSafe() error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	status := codexdesktop.Discover(ctx)
	switch status.State {
	case codexdesktop.StateRunning:
		return ErrCodexHistoryWriteUnsafe
	case codexdesktop.StateInstalled, codexdesktop.StateNotInstalled:
		return nil
	case codexdesktop.StateDegraded:
		return errors.New("could not safely verify whether Codex is running; quit Codex and retry")
	default:
		return errors.New("could not determine whether Codex is running; quit Codex and retry")
	}
}

func runningUnderGoTest() bool {
	for _, argument := range os.Args {
		if strings.HasPrefix(argument, "-test.") {
			return true
		}
	}
	return strings.HasSuffix(os.Args[0], ".test")
}

func (m *Manager) requireCodexHistoryWriteSafety() error {
	if m == nil {
		return errors.New("Codex history safety guard is unavailable")
	}
	guard := m.historyWriteGuard
	if guard == nil {
		guard = defaultHistoryWriteGuard
	}
	if err := guard(); err != nil {
		return fmt.Errorf("refusing to modify local Codex history: %w", err)
	}
	return nil
}

func (r *HistoryRepairer) requireCodexHistoryWriteSafety() error {
	if r == nil {
		return errors.New("Codex history repairer is unavailable")
	}
	manager := r.ConfigManager
	if manager == nil || filepathClean(manager.CodexHome) != filepathClean(r.CodexHome) {
		manager = NewManager(r.CodexHome)
	}
	return manager.requireCodexHistoryWriteSafety()
}

func filepathClean(path string) string {
	return strings.TrimSpace(path)
}
