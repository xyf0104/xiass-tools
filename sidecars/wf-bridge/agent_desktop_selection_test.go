package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"antigravity-wf-assistant/internal/agent"
)

func TestAgentDesktopSelectionRejectsUnknownIDsAndNeverEchoesPickerPath(t *testing.T) {
	if _, ok := selectableAgentDesktopID("codex"); ok {
		t.Fatal("Codex must not be accepted by the Cursor/Windsurf native picker")
	}
	if id, ok := selectableAgentDesktopID(" cursor "); !ok || id != agent.CursorID {
		t.Fatalf("Cursor selection id = %q, %v", id, ok)
	}

	app := &App{ctx: context.Background()}
	secretPath := "/Volumes/Private Work/Not-Cursor.app"
	status := app.selectAgentDesktopInstallationWithPicker(agent.CursorID, func(context.Context, agent.ID) (string, bool, error) {
		return secretPath, false, nil
	})
	if status.OK || status.Selected || status.CanLaunch {
		t.Fatalf("invalid picker status = %#v", status)
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{secretPath, "/Volumes/Private Work", "executable", "root"} {
		if strings.Contains(strings.ToLower(string(encoded)), strings.ToLower(forbidden)) {
			t.Fatalf("selection status leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestAgentDesktopSelectionCancellationIsExplicitAndDoesNotRequirePath(t *testing.T) {
	app := &App{ctx: context.Background()}
	status := app.selectAgentDesktopInstallationWithPicker(agent.WindsurfID, func(context.Context, agent.ID) (string, bool, error) {
		return "", true, nil
	})
	if !status.OK || status.Selected || status.CanLaunch || !strings.Contains(status.Message, "取消") {
		t.Fatalf("cancelled picker status = %#v", status)
	}
}
