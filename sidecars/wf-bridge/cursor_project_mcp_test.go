package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCursorProjectMCPSelectionIsOpaqueAndDoesNotWriteOnSelection(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "cursor-workspace")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}

	app := &App{}
	status := app.selectCursorProjectMCPConfiguration(project)
	if !status.OK || !status.CanApply || status.SelectionID == "" || status.ProjectName != "cursor-workspace" || status.Snapshot.Target != "cursor" {
		t.Fatalf("project selection = %#v", status)
	}
	if !validCursorProjectMCPSelectionID(status.SelectionID) {
		t.Fatalf("selection ID is not opaque: %q", status.SelectionID)
	}
	assertCursorProjectMCPRedacted(t, status, project, root, ".cursor", "mcp.json")

	refreshed := app.GetCursorProjectMCPConfiguration(status.SelectionID)
	if !refreshed.OK || refreshed.SelectionID != status.SelectionID || refreshed.ProjectName != "cursor-workspace" || !refreshed.CanApply {
		t.Fatalf("selected project status = %#v", refreshed)
	}
	assertCursorProjectMCPRedacted(t, refreshed, project, root, ".cursor", "mcp.json")
}

func TestCursorProjectMCPSelectionExpiresAndInputsCannotRouteToAPath(t *testing.T) {
	remoteInput := reflect.TypeOf(CursorProjectMCPRemoteInput{})
	if remoteInput.NumField() != 2 || remoteInput.Field(0).Name != "SelectionID" || remoteInput.Field(1).Name != "RemoteURL" {
		t.Fatalf("project remote input unexpectedly changed: %#v", remoteInput)
	}
	for _, field := range []string{"ProjectRoot", "Path", "Command", "Headers", "Env", "Token"} {
		if _, found := remoteInput.FieldByName(field); found {
			t.Fatalf("project remote input must not expose renderer-controlled %s", field)
		}
	}
	selectionInput := reflect.TypeOf(CursorProjectMCPSelectionInput{})
	if selectionInput.NumField() != 1 || selectionInput.Field(0).Name != "SelectionID" {
		t.Fatalf("project selection input unexpectedly changed: %#v", selectionInput)
	}

	root := t.TempDir()
	project := filepath.Join(root, "expiring-workspace")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	app := &App{}
	selected := app.selectCursorProjectMCPConfiguration(project)
	if !selected.OK || selected.SelectionID == "" {
		t.Fatalf("project selection = %#v", selected)
	}
	app.cursorProjectMCPMu.Lock()
	entry := app.cursorProjectMCP[selected.SelectionID]
	entry.expiresAt = time.Now().Add(-time.Second)
	app.cursorProjectMCP[selected.SelectionID] = entry
	app.cursorProjectMCPMu.Unlock()

	expired := app.GetCursorProjectMCPConfiguration(selected.SelectionID)
	if expired.OK || expired.SelectionID != "" || !strings.Contains(expired.Message, "过期") {
		t.Fatalf("expired project status = %#v", expired)
	}
	assertCursorProjectMCPRedacted(t, expired, project, root, "expiring-workspace", ".cursor", "mcp.json")
}

func TestCursorProjectMCPApplyFailureDoesNotEchoRemoteOrProject(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "safe-workspace")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	app := &App{}
	selected := app.selectCursorProjectMCPConfiguration(project)
	if !selected.OK || selected.SelectionID == "" {
		t.Fatalf("project selection = %#v", selected)
	}
	endpoint := "https://sensitive-project-endpoint.invalid/mcp?token=must-not-escape"
	result := app.ApplyCursorProjectMCPConfiguration(CursorProjectMCPRemoteInput{
		SelectionID: selected.SelectionID,
		RemoteURL:   endpoint,
	})
	if result.OK || !strings.Contains(result.Message, "远程地址") {
		t.Fatalf("invalid remote result = %#v", result)
	}
	assertCursorProjectMCPRedacted(t, result, project, root, endpoint, "must-not-escape", ".cursor", "mcp.json")
}

func assertCursorProjectMCPRedacted(t *testing.T, value any, forbidden ...string) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range forbidden {
		if token != "" && strings.Contains(string(encoded), token) {
			t.Fatalf("project MCP response leaked %q: %s", token, encoded)
		}
	}
}
