package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"antigravity-wf-assistant/internal/mcpconfig"
)

func TestTargetScopedMCPBridgeLifecycleUsesOnlyVerifiedRecoveryPoints(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		t.Skip("desktop MCP discovery fixture is only defined for macOS and Windows")
	}

	for _, target := range []mcpconfig.Target{mcpconfig.TargetCursor, mcpconfig.TargetWindsurf} {
		t.Run(string(target), func(t *testing.T) {
			home := setupMCPBridgeHome(t)
			installMCPBridgeClient(t, home, target)
			manager, err := mcpconfig.NewDefaultManager(target)
			if err != nil {
				t.Fatal(err)
			}

			// Create one known recovery point through the audited manager. The
			// bridge must not create, restore, or delete any recovery point until
			// its corresponding explicit method is called.
			if _, err := manager.ApplyRemote(mcpconfig.ApplyInput{RemoteURL: "https://initial-bridge-secret.invalid/mcp"}); err != nil {
				t.Fatal(err)
			}
			initialList := callMCPBridgeList(t, &App{}, target)
			if !initialList.OK || len(initialList.Backups) != 1 {
				t.Fatalf("initial backup list = %#v", initialList)
			}
			initialBackupID := initialList.Backups[0].ID

			app := &App{}
			before := callMCPBridgeGet(t, app, target)
			if !before.OK || !before.ClientDetected || before.Snapshot.Target != target || !before.Snapshot.ManagedServerConfigured {
				t.Fatalf("target-scoped status = %#v", before)
			}
			// Legacy callers remain compatible, but are restricted to the same
			// two documented targets. Neither read method may create a backup.
			legacy := app.GetMCPConfiguration(string(target))
			if !legacy.OK || legacy.Snapshot.Target != target {
				t.Fatalf("legacy target status = %#v", legacy)
			}
			if afterRead := callMCPBridgeList(t, app, target); len(afterRead.Backups) != 1 {
				t.Fatalf("Get unexpectedly changed recovery points: %#v", afterRead)
			}

			applied := callMCPBridgeApply(t, app, target, MCPRemoteInput{RemoteURL: "https://apply-bridge-secret.invalid/mcp"})
			if !applied.OK || applied.Snapshot.Target != target || !applied.Snapshot.ManagedServerConfigured {
				t.Fatalf("target-scoped apply = %#v", applied)
			}
			if afterApply := callMCPBridgeList(t, app, target); len(afterApply.Backups) != 2 {
				t.Fatalf("Apply did not create exactly one recovery point: %#v", afterApply)
			}

			restored := callMCPBridgeRestore(t, app, target, initialBackupID)
			if !restored.OK || restored.Result.Snapshot.Target != target || restored.Result.Snapshot.Exists {
				t.Fatalf("explicit restore = %#v", restored)
			}
			if afterRestore := callMCPBridgeList(t, app, target); len(afterRestore.Backups) != 3 {
				t.Fatalf("Restore did not create its explicit safety recovery point: %#v", afterRestore)
			}

			deleted := callMCPBridgeDelete(t, app, target, initialBackupID)
			if !deleted.OK {
				t.Fatalf("explicit deletion = %#v", deleted)
			}
			if afterDelete := callMCPBridgeList(t, app, target); len(afterDelete.Backups) != 2 || mcpBridgeContainsBackup(afterDelete.Backups, initialBackupID) {
				t.Fatalf("Delete did not remove only the selected recovery point: %#v", afterDelete)
			}

			for _, response := range []any{before, legacy, initialList, applied, restored, deleted} {
				assertMCPBridgeResponseRedacted(t, response, home, "initial-bridge-secret.invalid", "apply-bridge-secret.invalid")
			}
		})
	}
}

func TestMCPBridgeTargetScopedInputAndSensitiveStatusRemainRendererSafe(t *testing.T) {
	inputType := reflect.TypeOf(MCPRemoteInput{})
	if inputType.NumField() != 1 || inputType.Field(0).Name != "RemoteURL" {
		t.Fatalf("target-scoped input unexpectedly accepts renderer-controlled routing: %#v", inputType)
	}
	if _, exists := inputType.FieldByName("Target"); exists {
		t.Fatal("target-scoped input must not allow renderer-controlled MCP target selection")
	}

	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		t.Skip("desktop MCP discovery fixture is only defined for macOS and Windows")
	}
	home := setupMCPBridgeHome(t)
	installMCPBridgeClient(t, home, mcpconfig.TargetCursor)
	configPath := filepath.Join(home, ".cursor", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	secret := "renderer-must-not-see-this-secret"
	if err := os.WriteFile(configPath, []byte(`{"mcpServers":{"foreign":{"headers":{"Authorization":"Bearer `+secret+`"}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	status := (&App{}).GetCursorMCPConfiguration()
	if !status.OK || status.CanApply || !status.Snapshot.Valid || !status.Snapshot.HasSensitiveConfiguration {
		t.Fatalf("sensitive global MCP status = %#v", status)
	}
	if !strings.Contains(status.Message, "只读保护处理") || strings.Contains(status.Message, "已将该文件设为只读") {
		t.Fatalf("sensitive status claimed an unimplemented filesystem mutation: %q", status.Message)
	}
	assertMCPBridgeResponseRedacted(t, status, home, secret, "Authorization", "headers")

	backups := (&App{}).ListCursorMCPBackups()
	if !backups.OK || backups.Backups == nil || len(backups.Backups) != 0 {
		t.Fatalf("empty recovery list = %#v", backups)
	}
	encoded, err := json.Marshal(backups)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"backups":[]`) {
		t.Fatalf("empty recovery list was not serialized as an array: %s", encoded)
	}
}

func TestMCPBridgeBackupListingAndDeletionDoNotRequireClientDiscovery(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		t.Skip("desktop MCP configuration fixture is only defined for macOS and Windows")
	}
	home := setupMCPBridgeHome(t)
	manager, err := mcpconfig.NewDefaultManager(mcpconfig.TargetCursor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ApplyRemote(mcpconfig.ApplyInput{RemoteURL: "https://uninstalled-client-secret.invalid/mcp"}); err != nil {
		t.Fatal(err)
	}

	app := &App{}
	listed := app.ListCursorMCPBackups()
	if !listed.OK || len(listed.Backups) != 1 {
		t.Fatalf("backup listing unexpectedly required client discovery: %#v", listed)
	}
	if deleted := app.DeleteCursorMCPBackup(listed.Backups[0].ID); !deleted.OK {
		t.Fatalf("verified recovery-point deletion unexpectedly required client discovery: %#v", deleted)
	}
	afterDelete := app.ListCursorMCPBackups()
	if !afterDelete.OK || afterDelete.Backups == nil || len(afterDelete.Backups) != 0 {
		t.Fatalf("recovery point remained after explicit deletion: %#v", afterDelete)
	}
	assertMCPBridgeResponseRedacted(t, listed, home, "uninstalled-client-secret.invalid")
	assertMCPBridgeResponseRedacted(t, afterDelete, home, "uninstalled-client-secret.invalid")
}

func TestTargetScopedMCPBridgeRemovalOnlyRemovesReservedEntry(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		t.Skip("desktop MCP discovery fixture is only defined for macOS and Windows")
	}

	for _, target := range []mcpconfig.Target{mcpconfig.TargetCursor, mcpconfig.TargetWindsurf} {
		t.Run(string(target), func(t *testing.T) {
			home := setupMCPBridgeHome(t)
			installMCPBridgeClient(t, home, target)
			if _, err := mcpconfig.NewDefaultManager(target); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(mcpBridgeConfigPath(home, target)), 0o700); err != nil {
				t.Fatal(err)
			}
			original := []byte(`{"futureMetadata":{"keep":true},"mcpServers":{"foreign":{"url":"https://foreign-bridge-secret.invalid/mcp"},"xiass-tools-copy":{"url":"https://similarly-named-bridge-secret.invalid/mcp"},"xiass-tools":{"url":"https://xiass-bridge-secret.invalid/mcp"}}}`)
			if err := os.WriteFile(mcpBridgeConfigPath(home, target), original, 0o600); err != nil {
				t.Fatal(err)
			}

			removed := callMCPBridgeRemove(t, &App{}, target)
			if !removed.OK || !removed.Result.Removed || !removed.Result.BackupCreated || removed.Result.Snapshot.ManagedServerConfigured || removed.Result.Snapshot.ServerCount != 2 {
				t.Fatalf("target-scoped remove status = %#v", removed)
			}
			updated, err := os.ReadFile(mcpBridgeConfigPath(home, target))
			if err != nil {
				t.Fatal(err)
			}
			var root map[string]json.RawMessage
			if err := json.Unmarshal(updated, &root); err != nil {
				t.Fatal(err)
			}
			var servers map[string]json.RawMessage
			if err := json.Unmarshal(root["mcpServers"], &servers); err != nil {
				t.Fatal(err)
			}
			if _, exists := servers[mcpconfig.ManagedServerID]; exists {
				t.Fatal("bridge removal retained the managed server")
			}
			if _, exists := servers["foreign"]; !exists {
				t.Fatal("bridge removal deleted a foreign server")
			}
			if _, exists := servers["xiass-tools-copy"]; !exists {
				t.Fatal("bridge removal deleted a similarly named foreign server")
			}
			if _, exists := root["futureMetadata"]; !exists {
				t.Fatal("bridge removal deleted unknown top-level metadata")
			}
			backups := callMCPBridgeList(t, &App{}, target)
			if !backups.OK || len(backups.Backups) != 1 || backups.Backups[0].Reason != "remove" {
				t.Fatalf("bridge removal recovery points = %#v", backups)
			}
			assertMCPBridgeResponseRedacted(t, removed, home, "foreign-bridge-secret.invalid", "similarly-named-bridge-secret.invalid", "xiass-bridge-secret.invalid")
		})
	}
}

func TestTargetScopedMCPBridgeRemovalFailsClosedForSensitiveAndNoopState(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		t.Skip("desktop MCP discovery fixture is only defined for macOS and Windows")
	}

	for _, target := range []mcpconfig.Target{mcpconfig.TargetCursor, mcpconfig.TargetWindsurf} {
		t.Run(string(target), func(t *testing.T) {
			home := setupMCPBridgeHome(t)
			installMCPBridgeClient(t, home, target)
			path := mcpBridgeConfigPath(home, target)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}

			foreignOnly := []byte(`{"mcpServers":{"foreign":{"url":"https://foreign-bridge-secret.invalid/mcp"}}}`)
			if err := os.WriteFile(path, foreignOnly, 0o600); err != nil {
				t.Fatal(err)
			}
			noop := callMCPBridgeRemove(t, &App{}, target)
			if !noop.OK || noop.Result.Removed || noop.Result.BackupCreated {
				t.Fatalf("bridge no-op remove = %#v", noop)
			}
			if after, err := os.ReadFile(path); err != nil || !reflect.DeepEqual(after, foreignOnly) {
				t.Fatalf("bridge no-op changed foreign-only configuration: %q / %v", after, err)
			}
			if listed := callMCPBridgeList(t, &App{}, target); !listed.OK || len(listed.Backups) != 0 {
				t.Fatalf("bridge no-op created recovery points: %#v", listed)
			}

			secret := "bridge-direct-secret"
			sensitive := []byte(`{"mcpServers":{"foreign":{"headers":{"Authorization":"Bearer ` + secret + `"}},"xiass-tools":{"url":"https://xiass-bridge-secret.invalid/mcp"}}}`)
			if err := os.WriteFile(path, sensitive, 0o600); err != nil {
				t.Fatal(err)
			}
			blocked := callMCPBridgeRemove(t, &App{}, target)
			if blocked.OK || blocked.Result.Removed || blocked.Result.BackupCreated {
				t.Fatalf("sensitive bridge removal reported success: %#v", blocked)
			}
			if after, err := os.ReadFile(path); err != nil || !reflect.DeepEqual(after, sensitive) {
				t.Fatalf("sensitive bridge removal changed configuration: %q / %v", after, err)
			}
			if listed := callMCPBridgeList(t, &App{}, target); !listed.OK || len(listed.Backups) != 0 {
				t.Fatalf("sensitive bridge removal created recovery points: %#v", listed)
			}
			assertMCPBridgeResponseRedacted(t, blocked, home, secret, "Authorization", "headers", "xiass-bridge-secret.invalid")
		})
	}
}

func setupMCPBridgeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
		t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
		t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
	} else if runtime.GOOS == "darwin" {
		// Make the complete user-config root exist before NewDefaultManager
		// canonicalizes it. macOS temp directories are commonly exposed below
		// /var (a compatibility symlink to /private/var), while the target
		// directory itself must remain a real, non-symlink hierarchy.
		if err := os.MkdirAll(filepath.Join(home, "Library", "Application Support"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

func installMCPBridgeClient(t *testing.T, home string, target mcpconfig.Target) {
	t.Helper()
	name := "Cursor"
	if target == mcpconfig.TargetWindsurf {
		name = "Windsurf"
	}
	var executable string
	switch runtime.GOOS {
	case "darwin":
		executable = filepath.Join(home, "Applications", name+".app", "Contents", "MacOS", name)
	case "windows":
		executable = filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", name, name+".exe")
	default:
		t.Fatalf("unsupported desktop test platform %q", runtime.GOOS)
	}
	if err := os.MkdirAll(filepath.Dir(executable), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
}

func callMCPBridgeGet(t *testing.T, app *App, target mcpconfig.Target) MCPConfigurationStatus {
	t.Helper()
	switch target {
	case mcpconfig.TargetCursor:
		return app.GetCursorMCPConfiguration()
	case mcpconfig.TargetWindsurf:
		return app.GetWindsurfMCPConfiguration()
	default:
		t.Fatalf("unsupported target %q", target)
		return MCPConfigurationStatus{}
	}
}

func callMCPBridgeApply(t *testing.T, app *App, target mcpconfig.Target, input MCPRemoteInput) MCPConfigurationStatus {
	t.Helper()
	switch target {
	case mcpconfig.TargetCursor:
		return app.ApplyCursorMCPConfiguration(input)
	case mcpconfig.TargetWindsurf:
		return app.ApplyWindsurfMCPConfiguration(input)
	default:
		t.Fatalf("unsupported target %q", target)
		return MCPConfigurationStatus{}
	}
}

func callMCPBridgeRemove(t *testing.T, app *App, target mcpconfig.Target) MCPRemoveStatus {
	t.Helper()
	switch target {
	case mcpconfig.TargetCursor:
		return app.RemoveCursorMCPConfiguration()
	case mcpconfig.TargetWindsurf:
		return app.RemoveWindsurfMCPConfiguration()
	default:
		t.Fatalf("unsupported target %q", target)
		return MCPRemoveStatus{}
	}
}

func callMCPBridgeList(t *testing.T, app *App, target mcpconfig.Target) MCPBackupListStatus {
	t.Helper()
	switch target {
	case mcpconfig.TargetCursor:
		return app.ListCursorMCPBackups()
	case mcpconfig.TargetWindsurf:
		return app.ListWindsurfMCPBackups()
	default:
		t.Fatalf("unsupported target %q", target)
		return MCPBackupListStatus{Backups: emptyMCPBackups()}
	}
}

func callMCPBridgeRestore(t *testing.T, app *App, target mcpconfig.Target, backupID string) MCPRestoreStatus {
	t.Helper()
	switch target {
	case mcpconfig.TargetCursor:
		return app.RestoreCursorMCPBackup(backupID)
	case mcpconfig.TargetWindsurf:
		return app.RestoreWindsurfMCPBackup(backupID)
	default:
		t.Fatalf("unsupported target %q", target)
		return MCPRestoreStatus{}
	}
}

func callMCPBridgeDelete(t *testing.T, app *App, target mcpconfig.Target, backupID string) MCPBackupDeleteStatus {
	t.Helper()
	switch target {
	case mcpconfig.TargetCursor:
		return app.DeleteCursorMCPBackup(backupID)
	case mcpconfig.TargetWindsurf:
		return app.DeleteWindsurfMCPBackup(backupID)
	default:
		t.Fatalf("unsupported target %q", target)
		return MCPBackupDeleteStatus{}
	}
}

func mcpBridgeContainsBackup(backups []mcpconfig.BackupInfo, wanted string) bool {
	for _, backup := range backups {
		if backup.ID == wanted {
			return true
		}
	}
	return false
}

func mcpBridgeConfigPath(home string, target mcpconfig.Target) string {
	switch target {
	case mcpconfig.TargetCursor:
		return filepath.Join(home, ".cursor", "mcp.json")
	case mcpconfig.TargetWindsurf:
		return filepath.Join(home, ".codeium", "windsurf", "mcp_config.json")
	default:
		return ""
	}
}

func assertMCPBridgeResponseRedacted(t *testing.T, response any, forbidden ...string) {
	t.Helper()
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range forbidden {
		if value != "" && strings.Contains(string(encoded), value) {
			t.Fatalf("renderer response leaked %q: %s", value, encoded)
		}
	}
	for _, value := range []string{"mcp.json", "mcp_config.json", "configuration.json", "headers", "env", "token", "oauth", "account", "authorization"} {
		if strings.Contains(strings.ToLower(string(encoded)), value) {
			t.Fatalf("renderer response contained forbidden configuration detail %q: %s", value, encoded)
		}
	}
}
