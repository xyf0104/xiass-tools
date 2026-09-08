//go:build wfbridge

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"antigravity-wf-assistant/internal/storage"
	"antigravity-wf-assistant/internal/updater"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

func TestWFBridgeParentPipeSignalsExitOnEOF(t *testing.T) {
	reader, writer := io.Pipe()
	exited, err := wfBridgeParentExitSignal("123", reader)
	if err != nil {
		t.Fatalf("create parent exit signal: %v", err)
	}
	select {
	case <-exited:
		t.Fatal("parent exit signal fired before the pipe closed")
	default:
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close parent pipe: %v", err)
	}
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("parent exit signal did not fire after EOF")
	}
}

func TestWFBridgeParentPipeRejectsInvalidPIDAndAllowsStandaloneMode(t *testing.T) {
	if exited, err := wfBridgeParentExitSignal("", strings.NewReader("")); err != nil || exited != nil {
		t.Fatalf("standalone mode should disable parent monitoring: signal=%v err=%v", exited, err)
	}
	if _, err := wfBridgeParentExitSignal("not-a-pid", strings.NewReader("")); err == nil {
		t.Fatal("invalid parent PID must be rejected")
	}
}

func TestWFBridgeEventHubReturnsOnlyNewEvents(t *testing.T) {
	hub := &wfBridgeEventHub{}
	hub.emit("wf:first", map[string]any{"value": 1})
	sequence, events := hub.after(0)
	if sequence != 1 || len(events) != 1 || events[0].Name != "wf:first" {
		t.Fatalf("unexpected initial events: sequence=%d events=%+v", sequence, events)
	}
	hub.emit("wf:second", nil)
	sequence, events = hub.after(1)
	if sequence != 2 || len(events) != 1 || events[0].Name != "wf:second" {
		t.Fatalf("unexpected incremental events: sequence=%d events=%+v", sequence, events)
	}
}

func TestWFBridgeRPCRequiresToken(t *testing.T) {
	bridge := &wfBridgeServer{application: &App{}, token: strings.Repeat("a", 32), events: &wfBridgeEventHub{}}
	request := httptest.NewRequest(http.MethodPost, "/rpc", strings.NewReader(`{"method":"IsProxyListening","args":[]}`))
	response := httptest.NewRecorder()
	bridge.handleRPC(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized, got %d", response.Code)
	}
}

func TestWFBridgeTransferAndDiagnosticsRequireToken(t *testing.T) {
	bridge := &wfBridgeServer{application: &App{storageDir: t.TempDir()}, token: strings.Repeat("b", 32)}
	for _, endpoint := range []struct {
		path    string
		handler http.HandlerFunc
	}{
		{"/transfer/export", bridge.handleTransferExport},
		{"/diagnostics", bridge.handleDiagnostics},
	} {
		request := httptest.NewRequest(http.MethodGet, endpoint.path, nil)
		response := httptest.NewRecorder()
		endpoint.handler(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("%s did not require authentication", endpoint.path)
		}
	}
}

func TestWFBridgeDiagnosticsContainSanitizedHelperLogsWithoutPaths(t *testing.T) {
	dir := t.TempDir()
	storage.Init(dir)
	if err := os.WriteFile(filepath.Join(dir, "xiass-tools.log"), []byte("Authorization: Bearer helper-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	bridge := &wfBridgeServer{application: &App{storageDir: dir}, token: strings.Repeat("c", 32)}
	request := httptest.NewRequest(http.MethodGet, "/diagnostics", nil)
	request.Header.Set("Authorization", "Bearer "+bridge.token)
	response := httptest.NewRecorder()
	bridge.handleDiagnostics(response, request)
	body := response.Body.String()
	if response.Code != http.StatusOK || strings.Contains(body, "helper-secret") || strings.Contains(body, dir) {
		t.Fatalf("unsafe helper diagnostic response (%d): %s", response.Code, body)
	}
	for _, required := range []string{"embedded_wf_helper", "[REDACTED]", "external_codex_auth"} {
		if !strings.Contains(body, required) {
			t.Fatalf("helper diagnostic response missing %q: %s", required, body)
		}
	}
}

func TestWFBridgeRPCInvokesSafeExportedMethod(t *testing.T) {
	request := wfBridgeRPCRequest{Method: "IsProxyListening", Args: []json.RawMessage{}}
	if _, err := invokeWFBridgeMethod(&App{}, request); err != nil {
		t.Fatalf("invoke exported method: %v", err)
	}
	if _, err := invokeWFBridgeMethod(&App{}, wfBridgeRPCRequest{Method: "QuitApp"}); err == nil {
		t.Fatal("QuitApp must remain owned by the Tauri parent")
	}
	if _, err := invokeWFBridgeMethod(&App{}, wfBridgeRPCRequest{Method: "MissingMethod"}); err == nil {
		t.Fatal("unknown method must be rejected")
	}
}

func wfBridgeJSONArgs(t *testing.T, values ...any) []json.RawMessage {
	t.Helper()
	args := make([]json.RawMessage, 0, len(values))
	for _, value := range values {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("encode bridge argument: %v", err)
		}
		args = append(args, encoded)
	}
	return args
}

func TestWFBridgeResolvesCodexHelperRPCSurfaceWithoutExecuting(t *testing.T) {
	// This validates the production bridge's reflection and JSON binding for
	// the complete Codex helper surface without starting/stopping Codex or
	// touching its on-disk configuration during a test run.
	app := &App{}
	cases := []struct {
		method string
		args   []any
	}{
		{"GetCodexConfiguration", nil},
		{"ApplyCodexConfiguration", []any{map[string]any{}}},
		{"ApplyCodexConfigurationWithLifecycle", []any{map[string]any{"config": map[string]any{}}}},
		{"RemoveCodexXIASSProvider", nil},
		{"MigrateCodexLegacyProvider", nil},
		{"MigrateCodexLegacyProviderWithLifecycle", []any{"XIASS_STOP_CODEX"}},
		{"DiscoverCodexModels", []any{"https://api.xiass.com", "test-key"}},
		{"GetCodexAccountCandidates", nil},
		{"DiscoverCodexAccountModels", []any{"account-id"}},
		{"ApplyCodexConfigurationFromAccount", []any{map[string]any{}}},
		{"ApplyCodexConfigurationFromAccountWithLifecycle", []any{map[string]any{}}},
		{"RestoreCodexConfiguration", []any{"backup-id"}},
		{"DeleteCodexConfigurationBackup", []any{"backup-id"}},
		{"ImportCodexLegacyConfigBackup", []any{"legacy-id"}},
		{"ImportCodexLegacyHistoryBackup", []any{"legacy-id"}},
		{"RepairCodexHistory", []any{true}},
		{"RestoreCodexHistoryBackup", []any{"backup-id"}},
		{"DeleteCodexHistoryBackup", []any{"backup-id"}},
		{"StartCodexXIASSKeySelection", []any{"https://api.xiass.com"}},
		{"GetCodexXIASSKeySelectionStatus", []any{"selection-id"}},
		{"CompleteCodexXIASSKeySelectionManual", []any{"selection-id", "http://127.0.0.1:1/callback#state=s&payload=p"}},
		{"CancelCodexXIASSKeySelection", []any{"selection-id"}},
		{"DiscoverCodexXIASSSelectionModels", []any{"selection-id"}},
		{"ApplyCodexXIASSSelection", []any{"selection-id", map[string]any{}}},
		{"ApplyCodexXIASSSelectionWithLifecycle", []any{"selection-id", map[string]any{"config": map[string]any{}}}},
		{"GetCodexDesktopControlStatus", nil},
		{"SelectCodexDesktopInstallation", nil},
		{"SelectCodexDesktopInstallationPath", []any{"/tmp/Codex.app"}},
		{"LaunchCodexDesktop", nil},
		{"StopCodexDesktop", []any{"XIASS_STOP_CODEX"}},
		{"RestartCodexDesktop", []any{"XIASS_STOP_CODEX"}},
	}

	for _, item := range cases {
		t.Run(item.method, func(t *testing.T) {
			method, args, err := prepareWFBridgeMethod(app, wfBridgeRPCRequest{
				Method: item.method,
				Args:   wfBridgeJSONArgs(t, item.args...),
			})
			if err != nil {
				t.Fatalf("resolve %s: %v", item.method, err)
			}
			if !method.IsValid() || method.Type().NumIn() != len(args) {
				t.Fatalf("invalid bridge binding for %s", item.method)
			}
		})
	}

	if _, _, err := prepareWFBridgeMethod(nil, wfBridgeRPCRequest{Method: "GetCodexConfiguration"}); err == nil {
		t.Fatal("nil bridge application must be rejected")
	}
}

func TestWFBridgeResolvesClaudeCodeAndMCPSurfaceWithoutExecuting(t *testing.T) {
	// Resolve the complete renderer-facing Claude Code and MCP surface without
	// invoking any operation that could read, write, launch, or select a local
	// client. The concrete values also prove the bridge retains JSON DTO fields
	// rather than merely accepting the matching argument count.
	claudeApply := ClaudeCodeApplyInput{
		BaseURL:                     "https://api.xiass.com",
		CredentialMode:              "auth_token",
		Credential:                  "test-credential",
		AuthToken:                   "legacy-test-token",
		APIKeyHelper:                "helper-command",
		EnableGatewayModelDiscovery: true,
		Model:                       "claude-test-model",
	}
	claudeAccountApply := ClaudeCodeApplyAccountInput{
		AccountID:                   "account-id",
		Model:                       "claude-account-model",
		EnableGatewayModelDiscovery: true,
	}
	claudeGateway := ClaudeCodeGatewayRequestInput{
		BaseURL:        "https://api.xiass.com",
		CredentialMode: "auth_token",
		Credential:     "test-credential",
		AuthToken:      "legacy-test-token",
		Model:          "claude-test-model",
	}
	mcpInput := MCPConfigurationInput{Target: "cursor", RemoteURL: "https://mcp.xiass.com"}
	mcpRemote := MCPRemoteInput{RemoteURL: "https://mcp.xiass.com"}
	cursorProjectRemote := CursorProjectMCPRemoteInput{SelectionID: "project-selection-id", RemoteURL: "https://mcp.xiass.com"}
	cursorProjectSelection := CursorProjectMCPSelectionInput{SelectionID: "project-selection-id"}
	cursorProjectBackup := CursorProjectMCPBackupInput{SelectionID: "project-selection-id", BackupID: "backup-id"}

	cases := []struct {
		method   string
		args     []any
		wantArgs []any
	}{
		{"GetClaudeCodeConfiguration", nil, nil},
		{"ApplyClaudeCodeConfiguration", []any{claudeApply}, []any{claudeApply}},
		{"GetClaudeCodeAccountCandidates", nil, nil},
		{"ApplyClaudeCodeConfigurationFromAccount", []any{claudeAccountApply}, []any{claudeAccountApply}},
		{"DiscoverClaudeCodeGatewayModels", []any{claudeGateway}, []any{claudeGateway}},
		{"TestClaudeCodeGateway", []any{claudeGateway}, []any{claudeGateway}},
		{"RestoreClaudeCodeConfiguration", []any{"backup-id"}, []any{"backup-id"}},
		{"DeleteClaudeCodeConfigurationBackup", []any{"backup-id"}, []any{"backup-id"}},
		{"MigrateClaudeCodeLegacyBackup", []any{"legacy-source", "backup-id"}, []any{"legacy-source", "backup-id"}},
		{"GetMCPConfiguration", []any{"cursor"}, []any{"cursor"}},
		{"ApplyMCPConfiguration", []any{mcpInput}, []any{mcpInput}},
		{"GetCursorMCPConfiguration", nil, nil},
		{"ApplyCursorMCPConfiguration", []any{mcpRemote}, []any{mcpRemote}},
		{"RemoveCursorMCPConfiguration", nil, nil},
		{"ListCursorMCPBackups", nil, nil},
		{"RestoreCursorMCPBackup", []any{"backup-id"}, []any{"backup-id"}},
		{"DeleteCursorMCPBackup", []any{"backup-id"}, []any{"backup-id"}},
		{"GetWindsurfMCPConfiguration", nil, nil},
		{"ApplyWindsurfMCPConfiguration", []any{mcpRemote}, []any{mcpRemote}},
		{"RemoveWindsurfMCPConfiguration", nil, nil},
		{"ListWindsurfMCPBackups", nil, nil},
		{"RestoreWindsurfMCPBackup", []any{"backup-id"}, []any{"backup-id"}},
		{"DeleteWindsurfMCPBackup", []any{"backup-id"}, []any{"backup-id"}},
		{"ChooseCursorProjectMCPConfiguration", nil, nil},
		{"GetCursorProjectMCPConfiguration", []any{"project-selection-id"}, []any{"project-selection-id"}},
		{"ApplyCursorProjectMCPConfiguration", []any{cursorProjectRemote}, []any{cursorProjectRemote}},
		{"RemoveCursorProjectMCPConfiguration", []any{cursorProjectSelection}, []any{cursorProjectSelection}},
		{"ListCursorProjectMCPBackups", []any{cursorProjectSelection}, []any{cursorProjectSelection}},
		{"RestoreCursorProjectMCPBackup", []any{cursorProjectBackup}, []any{cursorProjectBackup}},
		{"DeleteCursorProjectMCPBackup", []any{cursorProjectBackup}, []any{cursorProjectBackup}},
	}

	if len(cases) != 30 {
		t.Fatalf("Claude/MCP embedded bridge surface has %d methods, want 30", len(cases))
	}
	for _, item := range cases {
		t.Run(item.method, func(t *testing.T) {
			method, arguments, err := prepareWFBridgeMethod(&App{}, wfBridgeRPCRequest{
				Method: item.method,
				Args:   wfBridgeJSONArgs(t, item.args...),
			})
			if err != nil {
				t.Fatalf("resolve %s: %v", item.method, err)
			}
			if !method.IsValid() || method.Type().NumIn() != len(arguments) {
				t.Fatalf("invalid bridge binding for %s", item.method)
			}
			if len(arguments) != len(item.wantArgs) {
				t.Fatalf("bound argument count for %s = %d, want %d", item.method, len(arguments), len(item.wantArgs))
			}
			for index, want := range item.wantArgs {
				if got := arguments[index].Interface(); !reflect.DeepEqual(got, want) {
					t.Fatalf("bound argument %d for %s = %#v, want %#v", index+1, item.method, got, want)
				}
			}
		})
	}

	for _, rejected := range []struct {
		name    string
		request wfBridgeRPCRequest
	}{
		{
			name: "cursor project picker rejects a renderer supplied path",
			request: wfBridgeRPCRequest{
				Method: "ChooseCursorProjectMCPConfiguration",
				Args:   wfBridgeJSONArgs(t, "/tmp/renderer-controlled-project"),
			},
		},
		{
			name: "cursor global MCP rejects a JSON array instead of its DTO",
			request: wfBridgeRPCRequest{
				Method: "ApplyCursorMCPConfiguration",
				Args:   []json.RawMessage{json.RawMessage(`[]`)},
			},
		},
		{
			name: "Windsurf project MCP is intentionally unavailable",
			request: wfBridgeRPCRequest{
				Method: "GetWindsurfProjectMCPConfiguration",
			},
		},
	} {
		t.Run(rejected.name, func(t *testing.T) {
			if _, _, err := prepareWFBridgeMethod(&App{}, rejected.request); err == nil {
				t.Fatal("unsupported embedded bridge request must be rejected")
			}
		})
	}
}

func TestWFBridgeDisablesHelperOwnedUpdates(t *testing.T) {
	app := &App{embeddedMode: true, ctx: context.Background()}
	if result := app.CheckForUpdates(); result.OK || result.Message != embeddedUpdatesDisabledMessage {
		t.Fatalf("embedded update check was not blocked: %+v", result)
	}
	if result := app.CancelUpdateCheck(); result.OK || result.Message != embeddedUpdatesDisabledMessage {
		t.Fatalf("embedded update cancellation was not blocked: %+v", result)
	}
	if result := app.SkipUpdateVersion("99.0.0"); result.OK || result.Message != embeddedUpdatesDisabledMessage {
		t.Fatalf("embedded update skip was not blocked: %+v", result)
	}
	if result := app.InstallLatestUpdate(); result.OK || result.Message != embeddedUpdatesDisabledMessage {
		t.Fatalf("embedded update installation was not blocked: %+v", result)
	}
	if result := (&App{}).CancelUpdateCheck(); !result.OK {
		t.Fatalf("standalone helper update controls should remain enabled: %+v", result)
	}
}

func TestWFBridgeReportsHostManagedVersion(t *testing.T) {
	embedded := &App{embeddedMode: true, ctx: context.Background()}
	if got := embedded.reportedVersion(); got != embeddedHostManagedVersion {
		t.Fatalf("embedded bridge version = %q, want host-managed label %q", got, embeddedHostManagedVersion)
	}
	if got := embedded.CheckForUpdates().Info.CurrentVersion; got != embeddedHostManagedVersion {
		t.Fatalf("embedded update status version = %q, want host-managed label %q", got, embeddedHostManagedVersion)
	}
	if got := (&App{}).reportedVersion(); got != updater.CurrentVersion {
		t.Fatalf("standalone helper version = %q, want %q", got, updater.CurrentVersion)
	}
}

func TestWFBridgeRuntimeDoesNotEmbedCredential(t *testing.T) {
	if strings.Contains(wfBridgeRuntimeJavaScript, "XIASS_WF_RPC_TOKEN") {
		t.Fatal("runtime script must receive the per-process credential at runtime")
	}
	if !strings.Contains(wfBridgeRuntimeJavaScript, "xiass-wf-auth") {
		t.Fatal("runtime script is missing the postMessage authentication handshake")
	}
	if !strings.Contains(wfBridgeRuntimeJavaScript, "xiass-wf-host-action") {
		t.Fatal("runtime script is missing the native host action forwarding bridge")
	}
}

func waitForWFBridgeHostAction(t *testing.T, hub *wfBridgeEventHub) nativeActionRequest {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		_, events := hub.after(0)
		for _, event := range events {
			if event.Name != wfBridgeHostActionEvent {
				continue
			}
			request, ok := event.Payload.(nativeActionRequest)
			if !ok {
				t.Fatalf("unexpected host action payload: %#v", event.Payload)
			}
			return request
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("host action was not emitted")
	return nativeActionRequest{}
}

func TestWFBridgeNativeSaveCompletesThroughEventHub(t *testing.T) {
	hub := &wfBridgeEventHub{}
	actions := newWFBridgeNativeActions(hub)
	app := &App{
		embeddedMode:  true,
		ctx:           context.Background(),
		nativeActions: actions,
	}
	type dialogResult struct {
		path string
		err  error
	}
	completed := make(chan dialogResult, 1)
	go func() {
		path, err := app.saveFileDialog(runtime.SaveDialogOptions{
			Title:            "Export diagnostics",
			DefaultDirectory: "/tmp",
			DefaultFilename:  "diagnostics.zip",
			Filters:          []runtime.FileFilter{{DisplayName: "ZIP archive", Pattern: "*.zip"}},
		})
		completed <- dialogResult{path: path, err: err}
	}()

	request := waitForWFBridgeHostAction(t, hub)
	if request.Kind != nativeActionSaveFile || request.RequestID == "" {
		t.Fatalf("unexpected native action request: %+v", request)
	}
	if request.DefaultFilename != "diagnostics.zip" || len(request.Filters) != 1 {
		t.Fatalf("save dialog metadata was not forwarded: %+v", request)
	}
	selectedPath := "/tmp/diagnostics.zip"
	if err := actions.Complete(nativeActionResult{
		RequestID: request.RequestID,
		OK:        true,
		Value:     selectedPath,
	}); err != nil {
		t.Fatalf("complete native action: %v", err)
	}

	select {
	case result := <-completed:
		if result.err != nil || result.path != selectedPath {
			t.Fatalf("unexpected save result: path=%q err=%v", result.path, result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("save dialog did not complete")
	}
	if _, events := hub.after(0); len(events) != 0 {
		t.Fatalf("completed host action should not be replayed: %+v", events)
	}
}

func TestWFBridgeNativeActionStopsWithApplicationContext(t *testing.T) {
	hub := &wfBridgeEventHub{}
	actions := newWFBridgeNativeActions(hub)
	ctx, cancel := context.WithCancel(context.Background())
	app := &App{embeddedMode: true, ctx: ctx, nativeActions: actions}
	completed := make(chan error, 1)
	go func() {
		_, err := app.openFileDialog(runtime.OpenDialogOptions{Title: "Import"})
		completed <- err
	}()
	waitForWFBridgeHostAction(t, hub)
	cancel()

	select {
	case err := <-completed:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context cancellation, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("native action remained blocked after application cancellation")
	}
}

func TestWFBridgeNativeActionStopsWhenApplicationReleasesResources(t *testing.T) {
	hub := &wfBridgeEventHub{}
	actions := newWFBridgeNativeActions(hub)
	app := &App{embeddedMode: true, ctx: context.Background(), nativeActions: actions}
	completed := make(chan error, 1)
	go func() {
		_, err := app.openDirectoryDialog(runtime.OpenDialogOptions{Title: "Choose directory"})
		completed <- err
	}()
	waitForWFBridgeHostAction(t, hub)
	app.releaseExitResources()

	select {
	case err := <-completed:
		if err == nil || !strings.Contains(err.Error(), "通道已关闭") {
			t.Fatalf("expected closed host error, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("native action remained blocked after application cleanup")
	}
}

func TestWFBridgeHostActionResultRequiresTokenAndCompletesWaiter(t *testing.T) {
	hub := &wfBridgeEventHub{}
	actions := newWFBridgeNativeActions(hub)
	token := strings.Repeat("b", 32)
	bridge := &wfBridgeServer{token: token, events: hub, actions: actions}
	completed := make(chan error, 1)
	go func() {
		_, err := actions.Execute(context.Background(), nativeActionRequest{Kind: nativeActionOpenURL})
		completed <- err
	}()
	request := waitForWFBridgeHostAction(t, hub)
	payload, err := json.Marshal(nativeActionResult{RequestID: request.RequestID, OK: true})
	if err != nil {
		t.Fatalf("encode result: %v", err)
	}

	unauthorized := httptest.NewRequest(http.MethodPost, "/host-action-result", strings.NewReader(string(payload)))
	unauthorizedResponse := httptest.NewRecorder()
	bridge.handleHostActionResult(unauthorizedResponse, unauthorized)
	if unauthorizedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized result rejection, got %d", unauthorizedResponse.Code)
	}

	authorized := httptest.NewRequest(http.MethodPost, "/host-action-result", strings.NewReader(string(payload)))
	authorized.Header.Set("Authorization", "Bearer "+token)
	authorizedResponse := httptest.NewRecorder()
	bridge.handleHostActionResult(authorizedResponse, authorized)
	if authorizedResponse.Code != http.StatusOK {
		t.Fatalf("expected accepted host result, got %d", authorizedResponse.Code)
	}
	select {
	case err := <-completed:
		if err != nil {
			t.Fatalf("host action did not complete successfully: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("host result did not unblock action waiter")
	}
}
