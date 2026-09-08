package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"antigravity-wf-assistant/internal/codexdesktop"
)

func TestCodexDesktopRendererDTOAndFailureMessageDoNotExposeLocalDetails(t *testing.T) {
	status := codexDesktopStatusForRenderer(codexdesktop.ControlStatus{
		Installation: codexdesktop.Installation{
			Present:            true,
			Source:             codexdesktop.SourceManualSelection,
			Version:            "26.825.32147",
			ExecutableVerified: true,
		},
		Discovered: true,
		Selected:   true,
		CanLaunch:  true,
	}, false, codexDesktopMessageForError(errors.New("/Users/alice/Applications/ChatGPT.app --pid 1234 token=secret"), "launch"))
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("marshal renderer DTO: %v", err)
	}
	for _, forbidden := range []string{"/Users/alice", "ChatGPT.app", "1234", "secret", "token="} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("renderer DTO leaked %q: %s", forbidden, encoded)
		}
	}
	if strings.Contains(string(encoded), `"executable":`) || strings.Contains(string(encoded), `"path":`) || strings.Contains(string(encoded), `"pid":`) {
		t.Fatalf("renderer DTO contains forbidden control field: %s", encoded)
	}
}

func TestPastedCodexDesktopPathUsesNativeValidationWithoutExposingPath(t *testing.T) {
	const pastedPath = "/private/test-user/Applications/Codex.app"
	fake := &recordingCodexDesktopControl{status: codexdesktop.ControlStatus{
		Installation: codexdesktop.Installation{
			Present:            true,
			Source:             codexdesktop.SourceManualSelection,
			Version:            "26.825.32147",
			ExecutableVerified: true,
		},
		Discovered: true,
		Selected:   true,
		CanLaunch:  true,
	}}
	app := &App{ctx: context.Background(), codexDesktopControl: fake}

	status := app.SelectCodexDesktopInstallationPath("  " + pastedPath + "  ")
	if !status.OK {
		t.Fatalf("SelectCodexDesktopInstallationPath() = %+v", status)
	}
	if fake.selectedPath != pastedPath {
		t.Fatalf("SelectPath() = %q, want trimmed pasted path", fake.selectedPath)
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("marshal pasted-path result: %v", err)
	}
	for _, forbidden := range []string{pastedPath, "/private/test-user", "Codex.app"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("pasted path leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestPastedCodexDesktopPathRejectsBlankWithoutCallingNativeValidation(t *testing.T) {
	fake := &recordingCodexDesktopControl{status: codexdesktop.ControlStatus{CanSelect: true}}
	app := &App{ctx: context.Background(), codexDesktopControl: fake}

	status := app.SelectCodexDesktopInstallationPath(" \t\n ")
	if status.OK {
		t.Fatalf("blank pasted path unexpectedly succeeded: %+v", status)
	}
	if fake.selectCalls != 0 {
		t.Fatalf("SelectPath() calls = %d, want 0", fake.selectCalls)
	}
}

type recordingCodexDesktopControl struct {
	status       codexdesktop.ControlStatus
	selectedPath string
	selectCalls  int
}

func (fake *recordingCodexDesktopControl) Status(context.Context) codexdesktop.ControlStatus {
	return fake.status
}

func (fake *recordingCodexDesktopControl) SelectPath(_ context.Context, value string) (codexdesktop.ControlStatus, error) {
	fake.selectCalls++
	fake.selectedPath = value
	return fake.status, nil
}

func (fake *recordingCodexDesktopControl) Launch(context.Context) (codexdesktop.ControlStatus, error) {
	return fake.status, nil
}

func (fake *recordingCodexDesktopControl) Stop(context.Context, string) (codexdesktop.ControlStatus, error) {
	return fake.status, nil
}

func (fake *recordingCodexDesktopControl) Restart(context.Context, string) (codexdesktop.ControlStatus, error) {
	return fake.status, nil
}
