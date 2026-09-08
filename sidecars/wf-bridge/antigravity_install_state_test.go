package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"antigravity-wf-assistant/internal/patcher"
)

func writeDarwinInstallStateExecutable(t *testing.T, root, name, contents string) string {
	t.Helper()
	path := filepath.Join(root, name+".app", "Contents", "MacOS", name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDarwinAntigravityProductRepatchStateTracksVersionAndExecutable(t *testing.T) {
	dir := t.TempDir()
	executable := writeDarwinInstallStateExecutable(t, dir, "Antigravity", "first")
	appPath := filepath.Dir(filepath.Dir(filepath.Dir(executable)))
	app := &App{storageDir: dir}
	target := patcher.TargetStatus{Kind: "ide", AppPath: appPath, ExecutablePath: executable, Version: "2.5.5"}
	record := antigravityInstallRecordFromTarget(target)
	record.PatchRevision = antigravityPatchRevision
	if err := app.saveAntigravityInstallState(antigravityInstallState{Schema: 1, Targets: []antigravityInstallRecord{record}}); err != nil {
		t.Fatal(err)
	}
	if required, message := app.antigravityProductRepatchState(patcher.Status{Targets: []patcher.TargetStatus{target}}); required || message != "" {
		t.Fatalf("unchanged macOS target requested reconnect: required=%t message=%q", required, message)
	}

	target.Version = "2.6.0"
	if required, message := app.antigravityProductRepatchState(patcher.Status{Targets: []patcher.TargetStatus{target}}); !required || !strings.Contains(message, "2.6.0") {
		t.Fatalf("macOS version change not detected: required=%t message=%q", required, message)
	}

	target.Version = "2.5.5"
	if err := os.WriteFile(executable, []byte("second-build"), 0o755); err != nil {
		t.Fatal(err)
	}
	if required, message := app.antigravityProductRepatchState(patcher.Status{Targets: []patcher.TargetStatus{target}}); !required || !strings.Contains(message, "程序文件") {
		t.Fatalf("macOS executable change not detected: required=%t message=%q", required, message)
	}
}

func TestDarwinAntigravityProductRepatchStateRequiresMacOSV6Revision(t *testing.T) {
	dir := t.TempDir()
	executable := writeDarwinInstallStateExecutable(t, dir, "Antigravity", "same-build")
	appPath := filepath.Dir(filepath.Dir(filepath.Dir(executable)))
	app := &App{storageDir: dir}
	target := patcher.TargetStatus{Kind: "ide", AppPath: appPath, ExecutablePath: executable, Version: "2.5.5"}
	legacy := antigravityInstallState{Schema: 1, Targets: []antigravityInstallRecord{antigravityInstallRecordFromTarget(target)}}
	if err := app.saveAntigravityInstallState(legacy); err != nil {
		t.Fatal(err)
	}
	if required, message := app.antigravityProductRepatchState(patcher.Status{Targets: []patcher.TargetStatus{target}}); !required || !strings.Contains(message, "旧版连接规则") {
		t.Fatalf("legacy macOS image rule did not request reconnect: required=%t message=%q", required, message)
	}

	record := antigravityInstallRecordFromTarget(target)
	record.PatchRevision = antigravityPatchRevision
	if err := app.saveAntigravityInstallState(antigravityInstallState{Schema: 1, Targets: []antigravityInstallRecord{record}}); err != nil {
		t.Fatal(err)
	}
	if required, message := app.antigravityProductRepatchState(patcher.Status{Targets: []patcher.TargetStatus{target}}); required || message != "" {
		t.Fatalf("current macOS image rule requested reconnect: required=%t message=%q", required, message)
	}
}

func TestDarwinAntigravityProductRepatchStateKeepsIDEAndAgentIndependent(t *testing.T) {
	dir := t.TempDir()
	ideExecutable := writeDarwinInstallStateExecutable(t, dir, "Antigravity IDE", "ide")
	agentExecutable := writeDarwinInstallStateExecutable(t, dir, "Antigravity 2", "agent")
	ide := patcher.TargetStatus{Kind: "ide", AppPath: filepath.Dir(filepath.Dir(filepath.Dir(ideExecutable))), ExecutablePath: ideExecutable, Version: "2.1.1"}
	agent := patcher.TargetStatus{Kind: "agent", AppPath: filepath.Dir(filepath.Dir(filepath.Dir(agentExecutable))), ExecutablePath: agentExecutable, Version: "2.6.0"}
	ideRecord := antigravityInstallRecordFromTarget(ide)
	ideRecord.PatchRevision = antigravityPatchRevision
	agentRecord := antigravityInstallRecordFromTarget(agent)
	// Simulate a machine where only IDE has received the v6/v4 upgrade.
	app := &App{storageDir: dir}
	if err := app.saveAntigravityInstallState(antigravityInstallState{Schema: 1, Targets: []antigravityInstallRecord{ideRecord, agentRecord}}); err != nil {
		t.Fatal(err)
	}
	required, message := app.antigravityProductRepatchState(patcher.Status{Targets: []patcher.TargetStatus{ide, agent}})
	if !required || !strings.Contains(message, "Antigravity 2.0") {
		t.Fatalf("Agent-only pending revision was not reported independently: required=%t message=%q", required, message)
	}
}
