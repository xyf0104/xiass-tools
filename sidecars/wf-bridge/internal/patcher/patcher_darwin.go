//go:build darwin

package patcher

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"antigravity-wf-assistant/internal/storage"
)

const (
	productionEndpoint = "https://daily-cloudcode-pa.googleapis.com"
	sandboxEndpoint    = "https://daily-cloudcode-pa.sandbox.googleapis.com"
	publicEndpoint     = "https://cloudcode-pa.googleapis.com"
	autopushEndpoint   = "https://autopush-cloudcode-pa.sandbox.googleapis.com"

	baseProxyEndpoint          = "http://127.0.0.1:50999"
	textProxyEndpoint          = "http://127.0.0.1:50999/v1internal/antigravity-wf"
	binaryProxyEndpoint        = "http://127.0.0.1:50999/v1internal/wfproxy"
	binarySandboxProxyEndpoint = "http://127.0.0.1:50999/v1internal/wfproxy-sandbox"

	darwinExtensionMarker = "antigravity-wf:mac-extension-endpoint"
	darwinASARMarker      = "antigravity-wf:mac-asar-endpoint"
)

// Recognized only while upgrading an application patched by an older WF
// build. Newly written applications contain only the WF markers/endpoints.
var (
	legacyTextProxyEndpoint          = "http://127.0.0.1:50999/v1internal/antigravity-" + legacyPatcherProductToken()
	legacyBinaryProxyEndpoint        = "http://127.0.0.1:50999/v1internal/" + legacyPatcherProductToken() + "xxx"
	legacyBinarySandboxProxyEndpoint = legacyBinaryProxyEndpoint + "-sandbox"
	legacyDarwinExtensionMarker      = "antigravity-" + legacyPatcherProductToken() + ":mac-extension-endpoint"
	legacyDarwinASARMarker           = "antigravity-" + legacyPatcherProductToken() + ":mac-asar-endpoint"
)

var darwinCloudCodeCallPattern = regexp.MustCompile(`await [A-Za-z_$][\w$]*\.getCloudCodeUrl\(\)`)
var darwinExtensionDataPattern = regexp.MustCompile(`"--app_data_dir",[A-Za-z_$][\w$]*\.getInstance\(\)\.appDataDirectoryName`)
var darwinMainDataPattern = regexp.MustCompile(`"--app_data_dir",[A-Za-z_$][\w$]*\.ideName`)
var darwinCloudCodeEndpointFlagPattern = regexp.MustCompile(`["']--cloud_code_endpoint["']`)
var darwinAPIServerURLFlagPattern = regexp.MustCompile(`["']--api_server_url["']`)
var darwinCloudCodeEndpointLiteralPattern = regexp.MustCompile(`(["']--cloud_code_endpoint["']\s*,\s*["'])(https?://[^"']+)(["'])`)
var darwinAPIServerURLLiteralPattern = regexp.MustCompile(`(["']--api_server_url["']\s*,\s*["'])(https?://[^"']+)(["'])`)

// Language Server binaries in newer Antigravity releases may use a different
// Cloud Code hostname. Keep this bounded to Google's cloudcode-pa hosts; an
// arbitrary URL in a binary must never be rewritten as a local proxy.
var darwinCloudCodeURLPattern = regexp.MustCompile(`https://[A-Za-z0-9.-]*cloudcode-pa(?:\.sandbox)?\.googleapis\.com`)

// A Language Server binary contains this helper-only fixed-width path after a
// patch. It remains recognizable even when the selected fallback port changes.
var darwinManagedBinaryEndpointPattern = regexp.MustCompile(`http://127\.0\.0\.1:[1-9][0-9]{4}/v1internal/`)

// signDarwinLanguageServer is kept as a narrow seam for transaction tests.
// Production always calls signPatchedDarwinLanguageServer; tests can induce a
// post-ASAR-write failure without depending on the host machine's codesign
// implementation or certificate configuration.
var signDarwinLanguageServer = signPatchedDarwinLanguageServer

// darwinOperationMu serializes all mutations coordinated by runDarwin. Wails
// bindings can be invoked concurrently (for example, a double-click on Apply
// while startup history recovery is still running); without this guard two
// operations could prepare and replace the same app/ASAR/backup files from
// different snapshots. Windows already protects its equivalent entry point.
var darwinOperationMu sync.Mutex

const darwinSharedDataArgument = `"--app_data_dir","antigravity"`

type darwinTargets struct {
	app            string
	name           string
	kind           string
	version        string
	main           string
	asar           string
	extension      string
	extensionEntry string
	language       string
}

type byteReplacement struct {
	old []byte
	new []byte
}

type patchPlan struct {
	path     string
	original []byte
	updated  []byte
	mode     os.FileMode
	changed  bool
}

// bindDarwinPatchPlanDestination preserves metadata from the installed file.
// Backups are intentionally stored as 0600, so destination permissions must
// always come from the active installation; otherwise a Language Server could
// become non-executable and Electron would fail to spawn it with EACCES.
func bindDarwinPatchPlanDestination(plan *patchPlan, destination string, requireExecutable bool) error {
	if plan == nil {
		return fmt.Errorf("补丁计划为空: %s", destination)
	}
	info, err := os.Stat(destination)
	if err != nil {
		return fmt.Errorf("检查补丁目标 %s 失败: %w", destination, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("补丁目标不是常规文件: %s", destination)
	}
	destinationMode := info.Mode()
	if requireExecutable && destinationMode.Perm()&0o111 == 0 {
		// Official Language Servers are ordinary 0755 executables. Repair all
		// execute/read bits rather than only owner-execute so an application in
		// /Applications remains launchable by every local user.
		destinationMode = (destinationMode &^ os.ModePerm) | 0o755
	}
	if plan.mode.Perm() != destinationMode.Perm() {
		plan.changed = true
	}
	plan.path = destination
	plan.mode = destinationMode
	return nil
}

func verifyDarwinExecutable(path string) error {
	if path == "" {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("Language Server 不可执行: %s（权限 %04o）", path, info.Mode().Perm())
	}
	return nil
}

// darwinPatchSnapshot captures the bytes actually active immediately before a
// write. Persistent backups provide the user-visible pre-upgrade restore
// point; these snapshots protect a partially written multi-file transaction.
type darwinPatchSnapshot struct {
	path string
	data []byte
	mode os.FileMode
	// existed makes a settings.json creation rollback-safe. The older patch
	// transaction only touched installed files, but the current user-setting
	// connection path can validly create the first settings file.
	existed bool
}

func snapshotDarwinPatchTargets(plans []*patchPlan) ([]darwinPatchSnapshot, error) {
	snapshots := make([]darwinPatchSnapshot, 0, len(plans))
	seen := make(map[string]bool, len(plans))
	for _, plan := range plans {
		if plan == nil || !plan.changed || plan.path == "" || seen[plan.path] {
			continue
		}
		seen[plan.path] = true
		data, readErr := os.ReadFile(plan.path)
		if os.IsNotExist(readErr) {
			snapshots = append(snapshots, darwinPatchSnapshot{path: plan.path, existed: false})
			continue
		}
		if readErr != nil {
			return nil, fmt.Errorf("读取事务回滚文件 %s: %w", plan.path, readErr)
		}
		info, statErr := os.Stat(plan.path)
		if statErr != nil {
			return nil, fmt.Errorf("检查事务回滚文件 %s: %w", plan.path, statErr)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("事务回滚路径不是常规文件: %s", plan.path)
		}
		snapshots = append(snapshots, darwinPatchSnapshot{path: plan.path, data: data, mode: info.Mode(), existed: true})
	}
	return snapshots, nil
}

func restoreDarwinPatchSnapshots(snapshots []darwinPatchSnapshot) error {
	for index := len(snapshots) - 1; index >= 0; index-- {
		snapshot := snapshots[index]
		if !snapshot.existed {
			if err := os.Remove(snapshot.path); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("移除本次新建的 %s: %w", snapshot.path, err)
			}
			continue
		}
		if err := writeFileAtomic(snapshot.path, snapshot.data, snapshot.mode); err != nil {
			return fmt.Errorf("恢复 %s: %w", snapshot.path, err)
		}
	}
	return nil
}

func runDarwin(action string) (string, error) {
	darwinOperationMu.Lock()
	defer darwinOperationMu.Unlock()

	if action == "sync-history" {
		summary, err := syncDarwinHistory()
		if err != nil {
			return "", err
		}
		return summary.message(), nil
	}
	// Restore only consumes snapshots captured before the most recent upgrade;
	// it must remain available even when a hand-edited runtime port state is
	// corrupt.
	if action == "status" {
		_ = refreshPatchProxyEndpoint()
	} else if action != "restore" {
		if err := refreshPatchProxyEndpoint(); err != nil {
			return "", err
		}
	}

	if action == "status" {
		return darwinStatusAll(locateDarwinInstallations()), nil
	}
	// Mutating actions retain the complete discovery contract. A valid recent
	// deep cache may be reused, otherwise process and Spotlight discovery run
	// before any target is compatibility-checked or written.
	targets := locateDarwinInstallationsCached(true, false)

	if len(targets) == 0 {
		return "", fmt.Errorf("未找到可支持的 Antigravity 安装（已检查环境变量、/Applications、~/Applications、正在运行的应用和 Spotlight）")
	}

	var message string
	var err error
	switch action {
	case "apply":
		message, err = applyDarwinTargetsForKind(targets, "")
	case "apply-ide":
		message, err = applyDarwinTargetsForKind(targets, "ide")
	case "apply-agent":
		message, err = applyDarwinTargetsForKind(targets, "agent")
	case "restore":
		message, err = restoreDarwinTargets(targets)
	default:
		return "", fmt.Errorf("未知补丁操作: %s", action)
	}
	if err != nil {
		invalidateDarwinDiscoveryCache()
		return message, err
	}
	// The operation may have changed renderer/ASAR metadata. Refresh the
	// target fingerprints immediately so the post-success install-state write
	// can still resolve a newly discovered non-standard bundle before its path
	// has been persisted for the first time.
	refreshed := make([]darwinTargets, 0, len(targets))
	for _, target := range targets {
		if current, ok := inspectDarwinApp(target.app); ok {
			refreshed = append(refreshed, current)
		}
	}
	cacheDarwinInstallations(refreshed, false)
	return message, nil
}

func locateDarwinTargets() darwinTargets {
	targets := locateDarwinInstallations()
	if len(targets) == 0 {
		return darwinTargets{}
	}
	return targets[0]
}

func existingFile(path string) string {
	if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
		return path
	}
	return ""
}

func darwinStatus(targets darwinTargets) string {
	mainPatched, _, _, fullyPatched := darwinTargetPatchState(targets)

	return fmt.Sprintf(
		"agent_patched=%t\nide_patched=%t\nide_main_patched=%t\nproxy_listening=%t\nasar=%s\nlanguage_server=%s\nide_extension=%s\nide_language_server=%s\n",
		fullyPatched,
		fullyPatched,
		mainPatched,
		proxyPortListening(),
		firstNonEmpty(targets.asar, targets.main),
		targets.language,
		targets.extensionEntry,
		targets.language,
	)
}

func darwinStatusAll(targets []darwinTargets) string {
	status := buildDarwinStatus(targets)
	return fmt.Sprintf(
		"agent_patched=%s\nide_patched=%s\nide_main_patched=%s\nproxy_listening=%t\nasar=%s\nlanguage_server=%s\nide_extension=%s\nide_language_server=%s\n",
		boolPointerText(status.AgentPatched), boolPointerText(status.IDEPatched), boolPointerText(status.IdeMainPatched),
		status.ProxyListening, status.AsarPath, status.LSPath, status.IDEExtensionPath, status.IDELSPath,
	)
}

func getDarwinStatus() Status {
	// Status remains available even if a hand-edited runtime state is invalid;
	// apply will surface that error and refuse to write an inconsistent patch.
	_ = refreshPatchProxyEndpoint()
	return buildDarwinStatus(locateDarwinInstallations())
}

func getDarwinQuickStatus() Status {
	_ = refreshPatchProxyEndpoint()
	return buildDarwinQuickStatus(locateDarwinInstallationsQuick())
}

func refreshDarwinStatus() Status {
	_ = refreshPatchProxyEndpoint()
	return buildDarwinStatus(refreshDarwinInstallations())
}

func buildDarwinQuickStatus(targets []darwinTargets) Status {
	status := Status{ProxyListening: proxyPortListening()}
	for _, target := range targets {
		entry := TargetStatus{
			Name: target.name, Kind: target.kind, Version: target.version, AppPath: target.app,
			ExecutablePath: darwinAppExecutablePath(target.app),
			MainPath:       target.main, ASARPath: target.asar, ExtensionPath: target.extensionEntry,
			LanguageServerPath: target.language, Reason: "正在后台核验兼容结构",
		}
		status.Targets = append(status.Targets, entry)
		if status.AsarPath == "" {
			status.AsarPath = firstNonEmpty(target.asar, target.main)
			status.LSPath = target.language
		}
		if target.kind == "ide" && status.IDEExtensionPath == "" {
			status.IDEExtensionPath = target.extensionEntry
			status.IDELSPath = target.language
		}
	}
	return status
}

func buildDarwinStatus(targets []darwinTargets) Status {
	status := Status{ProxyListening: proxyPortListening()}
	agentInstalled, ideInstalled := false, false
	agentPatched, idePatched := true, true
	ideMainPatched := true
	for _, target := range targets {
		supported, mode, reason := darwinTargetConnectionSupport(target)
		mainPatched, _, _, patched := darwinTargetPatchState(target)
		// IDE connection now uses the vendor-supported user setting instead of
		// modifying the Electron main process, extension, or Language Server.
		// Keep the legacy inspection helper for migration/restore fixtures, but
		// make the live status reflect the production connection contract.
		if target.kind == "ide" {
			patched = supported &&
				darwinCloudCodeSettingIsConfigured(darwinSettingsPathForStatus(target), currentPatchProxyEndpoint().Base) &&
				!darwinImagePreviewNeedsPatch(target)
			mainPatched = patched
		}
		entry := TargetStatus{
			Name: target.name, Kind: target.kind, Version: target.version, AppPath: target.app,
			ExecutablePath: darwinAppExecutablePath(target.app),
			MainPath:       target.main, ASARPath: target.asar, ExtensionPath: target.extensionEntry,
			LanguageServerPath: target.language, Supported: supported, ConnectionMode: mode, Reason: reason, Patched: patched,
		}
		status.Targets = append(status.Targets, entry)
		if status.AsarPath == "" {
			status.AsarPath = firstNonEmpty(target.asar, target.main)
			status.LSPath = target.language
		}
		if target.kind == "agent" {
			agentInstalled = true
			agentPatched = agentPatched && patched
		} else {
			ideInstalled = true
			idePatched = idePatched && patched
			ideMainPatched = ideMainPatched && mainPatched
			if status.IDEExtensionPath == "" {
				status.IDEExtensionPath = target.extensionEntry
				status.IDELSPath = target.language
			}
		}
	}
	if agentInstalled {
		status.AgentPatched = boolPointer(agentPatched)
	}
	if ideInstalled {
		status.IDEPatched = boolPointer(idePatched)
		status.IdeMainPatched = boolPointer(ideMainPatched)
	}
	// Older frontends expect both aggregate flags even when macOS packages the
	// Agent Window and IDE into one bundle. Keep those fields compatible while
	// the per-target list remains the authoritative state.
	if !agentInstalled && ideInstalled {
		status.AgentPatched = boolPointer(idePatched)
	}
	if !ideInstalled && agentInstalled {
		status.IDEPatched = boolPointer(agentPatched)
		status.IdeMainPatched = boolPointer(agentPatched)
	}
	return status
}

func darwinAppExecutablePath(appPath string) string {
	name := strings.TrimSpace(darwinBundleValue(appPath, "CFBundleExecutable"))
	if name == "" {
		return ""
	}
	return existingFile(filepath.Join(appPath, "Contents", "MacOS", name))
}

func darwinTargetPatchState(target darwinTargets) (main, extension, language, fully bool) {
	// Some current Agent/IDE layouts receive the endpoint entirely from a
	// verified JavaScript launcher and do not ship a standalone binary. Treat
	// that absence as a valid, explicitly degraded path; a present binary still
	// has to pass the fixed-width endpoint verification below.
	if target.language == "" {
		switch target.kind {
		case "agent":
			language = darwinASARHasSupportedEntrypoints(target.asar)
		case "ide":
			language = target.extensionEntry != ""
		}
	} else {
		language, _ = darwinLanguagePatchState(target.language)
		language = language && verifyDarwinExecutable(target.language) == nil
	}
	imagePreviewPatched := !darwinImagePreviewNeedsPatch(target)
	if target.kind == "agent" {
		main = darwinASARPatched(target.asar)
		integrityValid := verifyDarwinAgentASARIntegrity(target) == nil
		return main, true, language, main && language && imagePreviewPatched && integrityValid
	}
	main = darwinMainPatched(target.main)
	extension = target.extensionEntry == "" || darwinExtensionPatched(target.extensionEntry)
	return main, extension, language, main && extension && language && imagePreviewPatched
}

func darwinImagePreviewNeedsPatch(target darwinTargets) bool {
	if target.kind == "agent" {
		return imagePreviewASARNeedsPatch(target.asar)
	}
	return imagePreviewRenderersNeedPatch(darwinImagePreviewRendererPaths(target))
}

func darwinASARUnpackedImagePreviewRendererPaths(target darwinTargets) []string {
	if target.kind != "agent" || target.asar == "" {
		return nil
	}
	return imagePreviewASARUnpackedRendererPathsForPath(target.asar)
}

func darwinMainPatched(path string) bool {
	endpoint := currentPatchProxyEndpoint()
	if !fileHasPatch(
		path,
		[][]byte{[]byte(productionEndpoint), []byte(authEligibilityOriginal)},
		[][]byte{[]byte(endpoint.Text), []byte(authEligibilityPatched)},
	) {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	source := string(data)
	return !darwinMainDataPattern.MatchString(source) &&
		(strings.Contains(source, darwinSharedDataArgument) || !strings.Contains(source, "--app_data_dir"))
}

func darwinExtensionPatched(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	source := string(data)
	endpoint := currentPatchProxyEndpoint()
	return strings.Contains(source, darwinExtensionMarker) &&
		strings.Contains(source, endpoint.Base) && !darwinCloudCodeCallPattern.MatchString(source) &&
		!darwinExtensionDataPattern.MatchString(source) &&
		(strings.Contains(source, darwinSharedDataArgument) || !strings.Contains(source, "--app_data_dir"))
}

func darwinASARPatched(path string) bool {
	if path == "" {
		return false
	}
	archive, err := readASAR(path)
	if err != nil {
		return false
	}
	launcher, err := archive.readFile("dist/languageServer.js")
	if err != nil {
		return false
	}
	main, err := archive.readFile("dist/main.js")
	return err == nil && bytes.Contains(launcher, []byte(currentPatchProxyEndpoint().Base)) &&
		!darwinCloudCodeURLPattern.Match(launcher) && bytes.Contains(main, []byte(darwinASARMarker))
}

func darwinASARContainsKnownPatch(path string) bool {
	if path == "" {
		return false
	}
	archive, err := readASAR(path)
	if err != nil {
		return false
	}
	for _, name := range []string{"dist/main.js", "dist/languageServer.js"} {
		data, readErr := archive.readFile(name)
		if readErr == nil && containsKnownDarwinPatch(data) {
			return true
		}
	}
	return false
}

func patchDarwinCloudCodeLauncher(source string) string {
	endpoint := currentPatchProxyEndpoint()
	source = normalizeDarwinManagedEndpoints(source)
	source = darwinCloudCodeURLPattern.ReplaceAllString(source, endpoint.Base)
	// Current Agent launchers carry both --api_server_url (the native Gemini
	// service) and --cloud_code_endpoint (the endpoint managed by this helper).
	// Prefer the latter and preserve the Gemini URL byte-for-byte. Older builds
	// that expose only --api_server_url continue to use that single verified
	// fallback.
	if literalPattern, ok := darwinLauncherManagedLiteralPattern(source); ok {
		source = literalPattern.ReplaceAllString(source, `${1}`+endpoint.Base+`${3}`)
	}
	return source
}

func darwinLauncherManagedFlagLocations(source string) ([][]int, bool) {
	if locations := darwinCloudCodeEndpointFlagPattern.FindAllStringIndex(source, -1); len(locations) > 0 {
		return locations, len(locations) == 1
	}
	locations := darwinAPIServerURLFlagPattern.FindAllStringIndex(source, -1)
	return locations, len(locations) == 1
}

func darwinLauncherManagedLiteralPattern(source string) (*regexp.Regexp, bool) {
	locations, ok := darwinLauncherManagedFlagLocations(source)
	if !ok || len(locations) != 1 {
		return nil, false
	}
	if darwinCloudCodeEndpointFlagPattern.MatchString(source) {
		return darwinCloudCodeEndpointLiteralPattern, true
	}
	return darwinAPIServerURLLiteralPattern, true
}

func normalizeDarwinManagedEndpoints(source string) string {
	endpoint := currentPatchProxyEndpoint()
	for _, replacement := range []struct{ old, new string }{
		{legacyBinarySandboxProxyEndpoint, endpoint.BinarySandbox},
		{legacyBinaryProxyEndpoint, endpoint.Binary},
		{legacyTextProxyEndpoint, endpoint.Text},
		{binarySandboxProxyEndpoint, endpoint.BinarySandbox},
		{binaryProxyEndpoint, endpoint.Binary},
		{textProxyEndpoint, endpoint.Text},
		{baseProxyEndpoint, endpoint.Base},
	} {
		source = strings.ReplaceAll(source, replacement.old, replacement.new)
	}
	return source
}

func darwinLauncherHasProxyEndpoint(source string) bool {
	locations, ok := darwinLauncherManagedFlagLocations(source)
	if !ok {
		return false
	}
	endpoint := currentPatchProxyEndpoint().Base
	location := locations[0]
	start := location[0] - 1024
	if start < 0 {
		start = 0
	}
	end := location[1] + 1024
	if end > len(source) {
		end = len(source)
	}
	return strings.Contains(source[start:end], endpoint)
}

func boolPointer(value bool) *bool { return &value }

func boolPointerText(value *bool) string {
	if value == nil {
		return "None"
	}
	if *value {
		return "true"
	}
	return "false"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func fileHasPatch(path string, originals, patched [][]byte) bool {
	if path == "" {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	hasPatched := false
	for _, marker := range patched {
		hasPatched = hasPatched || bytes.Contains(data, marker)
	}
	for _, marker := range originals {
		if bytes.Contains(data, marker) {
			return false
		}
	}
	return hasPatched
}

func proxyPortListening() bool {
	port, err := storage.LoadProxyRuntimePort()
	if err != nil {
		return false
	}
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 400*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// darwinPatchSource always selects the bytes active on this Mac. The caller
// persists those exact bytes before writing, whether they came from the vendor,
// an older WF release, or a third-party modification.
func darwinPatchSource(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	if _, err := os.ReadFile(path); err != nil {
		return "", err
	}
	return path, nil
}

func applyDarwinPatch(targets darwinTargets) (message string, err error) {
	if targets.app == "" {
		return "", fmt.Errorf("%s 安装不完整，缺少应用目录", targets.name)
	}
	if targets.kind == "agent" {
		return applyDarwinASARPatch(targets)
	}
	if targets.main == "" {
		return "", fmt.Errorf("%s 中未找到主进程脚本", targets.app)
	}
	if targets.language == "" && targets.extensionEntry == "" {
		return "", fmt.Errorf("%s 缺少可验证的扩展入口和 language_server；为避免写入未知版本，未应用补丁", targets.name)
	}
	mainSource, err := darwinPatchSource(targets.main)
	if err != nil {
		return "", err
	}
	mainPlan, err := prepareDarwinMainPatch(mainSource)
	if err != nil {
		return "", err
	}
	if err := bindDarwinPatchPlanDestination(mainPlan, targets.main, false); err != nil {
		return "", err
	}
	authRecognized := bytes.Contains(mainPlan.original, []byte(authEligibilityOriginal)) ||
		bytes.Contains(mainPlan.original, []byte(authEligibilityPatched))
	plans := []*patchPlan{mainPlan}
	var languagePlan *patchPlan
	if targets.language != "" {
		languageSource, languageSourceErr := darwinPatchSource(targets.language)
		if languageSourceErr != nil {
			return "", languageSourceErr
		}
		languagePlan, _, err = prepareDarwinLanguagePatch(languageSource)
		if err != nil {
			return "", err
		}
		if err := bindDarwinPatchPlanDestination(languagePlan, targets.language, true); err != nil {
			return "", err
		}
		plans = append(plans, languagePlan)
	}

	var extensionPlan *patchPlan
	if targets.extensionEntry != "" {
		extensionSource, sourceErr := darwinPatchSource(targets.extensionEntry)
		if sourceErr != nil {
			return "", sourceErr
		}
		extensionPlan, err = prepareDarwinExtensionPatch(extensionSource)
		if err != nil {
			return "", err
		}
		if err := bindDarwinPatchPlanDestination(extensionPlan, targets.extensionEntry, false); err != nil {
			return "", err
		}
		plans = append(plans, extensionPlan)
	}
	for _, rendererPath := range darwinImagePreviewRendererPaths(targets) {
		if rendererPath == targets.main {
			// The endpoint plan above already applied the image-preview fallback
			// to out/main.js when the known renderer shape was present.
			continue
		}
		rendererPlan, err := prepareDarwinImagePreviewPatch(rendererPath)
		if err != nil {
			return "", err
		}
		plans = append(plans, rendererPlan)
	}
	changed := false
	for _, plan := range plans {
		changed = changed || plan.changed
	}
	if !changed {
		return fmt.Sprintf("%s 补丁已处于激活状态，无需重复应用。", targets.name), nil
	}
	if darwinMainDataPattern.Match(mainPlan.original) ||
		(extensionPlan != nil && darwinExtensionDataPattern.Match(extensionPlan.original)) {
		if err := mergeDarwinHistory(); err != nil {
			return "", fmt.Errorf("合并历史会话失败: %w", err)
		}
	}

	if err := saveApplyBackups(plans, nil); err != nil {
		return "", fmt.Errorf("创建补丁备份失败: %w", err)
	}
	snapshots, snapshotErr := snapshotDarwinPatchTargets(plans)
	if snapshotErr != nil {
		return "", fmt.Errorf("创建补丁事务回滚副本失败: %w", snapshotErr)
	}
	defer func() {
		if err == nil {
			return
		}
		if rollbackErr := restoreDarwinPatchSnapshots(snapshots); rollbackErr != nil {
			err = fmt.Errorf("%w；补丁事务回滚失败: %v", err, rollbackErr)
		}
	}()
	if err = writePatchPlans(plans); err != nil {
		return "", err
	}
	if languagePlan != nil && languagePlan.changed {
		if err = signDarwinLanguageServer(targets.language); err != nil {
			return "", fmt.Errorf("补丁已自动回滚，macOS 语言服务器签名失败: %w", err)
		}
	}
	if err = verifyDarwinExecutable(targets.language); err != nil {
		return "", fmt.Errorf("补丁已自动回滚，macOS 语言服务器权限验证失败: %w", err)
	}

	warnings := make([]string, 0, 2)
	if !authRecognized {
		warnings = append(warnings, "提示：此版本未匹配旧版登录资格分支，已保留其原生本地凭据登录逻辑。")
	}
	if targets.language == "" {
		warnings = append(warnings, "提示：该版本没有独立 Language Server，已通过受验证入口脚本传递代理地址。")
	}
	warning := ""
	if len(warnings) > 0 {
		warning = "\n" + strings.Join(warnings, "\n")
	}
	return fmt.Sprintf(
		"%s 补丁应用成功。\n应用: %s\n主进程: %s\n扩展: %s\n语言服务器: %s%s",
		targets.name, targets.app, targets.main, targets.extensionEntry, darwinOptionalPath(targets.language), warning,
	), nil
}

func darwinOptionalPath(path string) string {
	if strings.TrimSpace(path) != "" {
		return path
	}
	return "未发现（入口脚本已验证）"
}

func prepareDarwinMainPatch(path string) (*patchPlan, error) {
	endpoint := currentPatchProxyEndpoint()
	plan, err := preparePatch(path, []byteReplacement{
		{old: []byte(productionEndpoint), new: []byte(endpoint.Text)},
		{old: []byte(authEligibilityOriginal), new: []byte(authEligibilityPatched)},
	})
	if err != nil {
		return nil, err
	}
	source := string(plan.updated)
	source = normalizeDarwinManagedEndpoints(source)
	if darwinMainDataPattern.MatchString(source) {
		source = darwinMainDataPattern.ReplaceAllString(source, darwinSharedDataArgument)
	}
	if updated, result := patchImagePreviewRenderer(source); result.Changed {
		source = updated
	}
	plan.updated = []byte(source)
	plan.changed = !bytes.Equal(plan.original, plan.updated)
	return plan, nil
}

func darwinImagePreviewRendererPaths(target darwinTargets) []string {
	if target.kind != "ide" || target.app == "" {
		return nil
	}
	return imagePreviewRendererPaths(filepath.Join(target.app, "Contents", "Resources", "app"))
}

func prepareDarwinImagePreviewPatch(path string) (*patchPlan, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取图片预览渲染器 %s 失败: %w", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	updated, result := patchImagePreviewRenderer(string(data))
	return &patchPlan{
		path: path, original: data, updated: []byte(updated), mode: info.Mode(), changed: result.Changed,
	}, nil
}

func applyDarwinPatches(targets []darwinTargets, onlyIDE bool) (string, error) {
	var messages []string
	selected := 0
	for _, target := range targets {
		if onlyIDE && target.kind != "ide" {
			continue
		}
		selected++
		message, err := applyDarwinPatch(target)
		if err != nil {
			return strings.Join(messages, "\n\n"), fmt.Errorf("%s 补丁失败: %w", target.name, err)
		}
		messages = append(messages, message)
	}
	if selected == 0 {
		return "", fmt.Errorf("未找到 unpacked IDE 类型的 Antigravity 安装")
	}
	return strings.Join(messages, "\n\n") + "\n\n请保持本工具运行，然后完全退出并重启对应的 Antigravity。", nil
}

func prepareDarwinExtensionPatch(path string) (*patchPlan, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取 %s 失败: %w", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	source := strings.ReplaceAll(string(data), legacyDarwinExtensionMarker, darwinExtensionMarker)
	source = normalizeDarwinManagedEndpoints(source)
	endpoint := currentPatchProxyEndpoint()
	if darwinExtensionPatched(path) {
		return &patchPlan{path: path, original: data, updated: data, mode: info.Mode()}, nil
	}
	count := len(darwinCloudCodeCallPattern.FindAllStringIndex(source, -1))
	endpointAlreadyPatched := count == 0 && strings.Contains(source, endpoint.Base)
	if count == 0 && !endpointAlreadyPatched {
		return nil, fmt.Errorf("%s 中未找到受支持的 getCloudCodeUrl 调用", path)
	}
	if count > 0 {
		source = darwinCloudCodeCallPattern.ReplaceAllString(source, `"`+endpoint.Base+`"`)
	}
	if darwinExtensionDataPattern.MatchString(source) {
		source = darwinExtensionDataPattern.ReplaceAllString(source, darwinSharedDataArgument)
	}
	marker := "// " + darwinExtensionMarker
	license := "/*! For license information please see extension.js.LICENSE.txt */"
	if !strings.Contains(source, darwinExtensionMarker) {
		if strings.HasPrefix(source, license) {
			source = strings.Replace(source, license, license+"\n"+marker, 1)
		} else {
			source = marker + "\n" + source
		}
	}
	updated := []byte(source)
	return &patchPlan{path: path, original: data, updated: updated, mode: info.Mode(), changed: !bytes.Equal(data, updated)}, nil
}

type darwinHistorySummary struct {
	target  string
	sources int
	copied  int
}

func (s darwinHistorySummary) message() string {
	if s.sources == 0 {
		return fmt.Sprintf("启动时历史会话检查完成：未发现独立的旧会话目录；共享目录：%s", s.target)
	}
	if s.copied == 0 {
		return fmt.Sprintf("启动时历史会话检查完成：已检查 %d 个旧目录，没有新增文件；共享目录：%s", s.sources, s.target)
	}
	return fmt.Sprintf(
		"启动时历史会话恢复完成：从 %d 个旧目录新增 %d 个文件；共享目录：%s；旧目录备份已保留",
		s.sources, s.copied, s.target,
	)
}

func mergeDarwinHistory() error {
	_, err := syncDarwinHistory()
	return err
}

func syncDarwinHistory() (darwinHistorySummary, error) {
	base := strings.TrimSpace(os.Getenv("ANTIGRAVITY_WF_GEMINI_DIR"))
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return darwinHistorySummary{}, err
		}
		base = filepath.Join(home, ".gemini")
	}
	target := filepath.Join(base, "antigravity")
	summary := darwinHistorySummary{target: target}
	sources, err := discoverDarwinHistorySources(base, target)
	if err != nil {
		return summary, err
	}
	summary.sources = len(sources)
	if len(sources) == 0 {
		return summary, nil
	}
	if info, err := os.Stat(target); os.IsNotExist(err) {
		if err := os.MkdirAll(target, 0o755); err != nil {
			return summary, fmt.Errorf("创建共享历史目录失败: %w", err)
		}
	} else if err != nil {
		return summary, err
	} else if !info.IsDir() {
		return summary, fmt.Errorf("共享历史路径不是目录: %s", target)
	}

	resources := []string{
		"annotations", "brain", "browser_recordings", "context_state", "conversations",
		"code_tracker", "html_artifacts", "implicit", "knowledge", "playground", "plugins", "prompting", "scratch",
	}
	for _, source := range sources {
		backup := source + ".antigravity-wf-backup"
		if _, err := copyDirectoryMissingCount(source, backup); err != nil {
			return summary, fmt.Errorf("备份旧历史目录失败（%s）: %w", source, err)
		}
		for _, resource := range resources {
			copied, err := copyDirectoryMissingCount(filepath.Join(source, resource), filepath.Join(target, resource))
			if err != nil {
				return summary, fmt.Errorf("恢复历史资源失败（%s）: %w", resource, err)
			}
			summary.copied += copied
		}
		for _, name := range []string{"mcp_config.json"} {
			sourceFile, targetFile := filepath.Join(source, name), filepath.Join(target, name)
			if _, err := os.Stat(targetFile); err == nil {
				continue
			} else if !os.IsNotExist(err) {
				return summary, err
			}
			if info, err := os.Stat(sourceFile); err == nil && info.Mode().IsRegular() {
				if err := copyFileAtomic(sourceFile, targetFile, info.Mode()); err != nil {
					return summary, err
				}
				summary.copied++
			}
		}
	}
	return summary, nil
}

func copyDirectoryMissing(source, target string) error {
	_, err := copyDirectoryMissingCount(source, target)
	return err
}

func discoverDarwinHistorySources(base, target string) ([]string, error) {
	entries, err := os.ReadDir(base)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var sources []string
	for _, entry := range entries {
		name := strings.ToLower(entry.Name())
		path := filepath.Join(base, entry.Name())
		if !entry.IsDir() || filepath.Clean(path) == filepath.Clean(target) || !strings.HasPrefix(name, "antigravity") {
			continue
		}
		if strings.Contains(name, "antigravity-wf-backup") || strings.Contains(name, "antigravity-"+legacyPatcherProductToken()+"-backup") || strings.Contains(name, ".previous-") {
			continue
		}
		if !darwinDirectoryContainsHistory(path) {
			continue
		}
		sources = append(sources, path)
	}
	sort.Strings(sources)
	return sources, nil
}

func darwinDirectoryContainsHistory(path string) bool {
	for _, resource := range []string{"conversations", "brain", "context_state", "prompting"} {
		if info, err := os.Stat(filepath.Join(path, resource)); err == nil && info.IsDir() {
			return true
		}
	}
	return false
}

func copyDirectoryMissingCount(source, target string) (int, error) {
	info, err := os.Stat(source)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if !info.IsDir() {
		return 0, nil
	}
	copied := 0
	err = filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		destination := filepath.Join(target, relative)
		if entry.IsDir() {
			return os.MkdirAll(destination, 0o755)
		}
		if entry.Type()&os.ModeSymlink != 0 || skipDarwinHistoryFile(entry.Name()) {
			return nil
		}
		if _, err := os.Stat(destination); err == nil {
			return nil
		} else if !os.IsNotExist(err) {
			return err
		}
		fileInfo, err := entry.Info()
		if err != nil {
			return err
		}
		if err := copyFileAtomic(path, destination, fileInfo.Mode()); err != nil {
			return err
		}
		copied++
		return nil
	})
	return copied, err
}

func skipDarwinHistoryFile(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".tmp") || strings.HasSuffix(lower, ".db-wal") ||
		strings.HasSuffix(lower, ".db-shm") || strings.HasSuffix(lower, ".lock")
}

func applyDarwinASARPatch(target darwinTargets) (message string, err error) {
	if target.asar == "" {
		return "", fmt.Errorf("%s 中未找到 app.asar", target.app)
	}
	var languagePlan *patchPlan
	if target.language != "" {
		languageSource, languageSourceErr := darwinPatchSource(target.language)
		if languageSourceErr != nil {
			return "", languageSourceErr
		}
		languagePlan, _, err = prepareDarwinLanguagePatch(languageSource)
		if err != nil {
			return "", err
		}
		if err := bindDarwinPatchPlanDestination(languagePlan, target.language, true); err != nil {
			return "", err
		}
	}
	asarWasPatched := darwinASARPatched(target.asar)
	// Rebuild app.asar only for endpoint or packed-renderer changes. Renderer
	// files declared as unpacked live beside the archive and must not be folded
	// into its manifest during an image-preview upgrade.
	asarChanged := !asarWasPatched || imagePreviewASARArchiveNeedsPatch(target.asar)
	previewPlans := make([]*patchPlan, 0)
	for _, rendererPath := range darwinASARUnpackedImagePreviewRendererPaths(target) {
		plan, planErr := prepareDarwinImagePreviewPatch(rendererPath)
		if planErr != nil {
			return "", planErr
		}
		previewPlans = append(previewPlans, plan)
	}
	if !asarChanged && (languagePlan == nil || !languagePlan.changed) && !patchPlansChanged(previewPlans) {
		return fmt.Sprintf("%s 补丁已处于激活状态，无需重复应用。", target.name), nil
	}
	// Capture the complete active Agent pair for every transaction, even when
	// only an unpacked renderer or Language Server changes. Restore treats the
	// Agent as one unit and must never combine a stale ASAR/plist snapshot with
	// freshly captured companion files.
	if err = writeFileBackup(target.asar); err != nil {
		return "", fmt.Errorf("创建 app.asar 升级前备份失败: %w", err)
	}
	plistPath, plistErr := darwinAgentInfoPlistPath(target)
	if plistErr != nil {
		return "", plistErr
	}
	if err = writeFileBackup(plistPath); err != nil {
		return "", fmt.Errorf("创建 Info.plist 升级前备份失败: %w", err)
	}
	var candidate string
	var integrityPlan *patchPlan
	var rollbackASAR string
	var rollbackASARMode os.FileMode
	if asarChanged {
		// Keep a per-operation snapshot of the active archive. A late failure
		// (for example code-signing the language server) must restore this exact
		// old-WF/third-party state.
		activeInfo, statErr := os.Stat(target.asar)
		if statErr != nil {
			return "", statErr
		}
		rollbackFile, createErr := os.CreateTemp(filepath.Dir(target.asar), ".antigravity-wf-asar-rollback-*")
		if createErr != nil {
			return "", createErr
		}
		rollbackASAR = rollbackFile.Name()
		if closeErr := rollbackFile.Close(); closeErr != nil {
			return "", closeErr
		}
		if removeErr := os.Remove(rollbackASAR); removeErr != nil && !os.IsNotExist(removeErr) {
			return "", removeErr
		}
		if copyErr := copyFileAtomic(target.asar, rollbackASAR, activeInfo.Mode()); copyErr != nil {
			return "", fmt.Errorf("创建 app.asar 事务回滚副本失败: %w", copyErr)
		}
		rollbackASARMode = activeInfo.Mode()
		defer os.Remove(rollbackASAR)

		candidate, err = prepareDarwinASARCandidate(target.asar, target.asar)
		if err != nil {
			return "", err
		}
		defer os.Remove(candidate)
		integrityPlan, err = prepareDarwinAgentASARIntegrityPatch(target, candidate)
		if err != nil {
			return "", fmt.Errorf("准备 app.asar 完整性元数据失败: %w", err)
		}
	}
	plans := append([]*patchPlan{}, previewPlans...)
	if languagePlan != nil {
		plans = append(plans, languagePlan)
	}
	if integrityPlan != nil {
		plans = append(plans, integrityPlan)
	}
	if err = saveApplyBackups(plans, nil); err != nil {
		return "", fmt.Errorf("创建补丁备份失败: %w", err)
	}
	planSnapshots, snapshotErr := snapshotDarwinPatchTargets(plans)
	if snapshotErr != nil {
		return "", fmt.Errorf("创建补丁事务回滚副本失败: %w", snapshotErr)
	}
	wroteASAR := false
	defer func() {
		if err == nil {
			return
		}
		if wroteASAR {
			if rollbackErr := copyFileAtomic(rollbackASAR, target.asar, rollbackASARMode); rollbackErr != nil {
				err = fmt.Errorf("%w；app.asar 事务回滚失败: %v", err, rollbackErr)
			}
		}
		if rollbackErr := restoreDarwinPatchSnapshots(planSnapshots); rollbackErr != nil {
			err = fmt.Errorf("%w；补丁事务回滚失败: %v", err, rollbackErr)
		}
	}()
	preASARPlans := make([]*patchPlan, 0, len(plans))
	for _, plan := range plans {
		if plan != integrityPlan {
			preASARPlans = append(preASARPlans, plan)
		}
	}
	if err = writePatchPlans(preASARPlans); err != nil {
		return "", err
	}
	if asarChanged {
		info, statErr := os.Stat(target.asar)
		if statErr != nil {
			return "", statErr
		}
		if err = os.Chmod(candidate, info.Mode().Perm()); err != nil {
			return "", err
		}
		if err = os.Rename(candidate, target.asar); err != nil {
			return "", fmt.Errorf("替换 app.asar 失败: %w", err)
		}
		wroteASAR = true
	}
	if integrityPlan != nil {
		if err = writePatchPlans([]*patchPlan{integrityPlan}); err != nil {
			return "", fmt.Errorf("写入 app.asar 完整性元数据失败: %w", err)
		}
	}
	if err = verifyDarwinAgentASARIntegrity(target); err != nil {
		return "", fmt.Errorf("写入后的 app.asar 完整性验证失败: %w", err)
	}
	if languagePlan != nil && languagePlan.changed {
		if err = signDarwinLanguageServer(target.language); err != nil {
			return "", fmt.Errorf("macOS 语言服务器签名失败: %w", err)
		}
	}
	if err = verifyDarwinExecutable(target.language); err != nil {
		return "", fmt.Errorf("macOS 语言服务器权限验证失败: %w", err)
	}
	if _, _, _, patched := darwinTargetPatchState(target); !patched {
		return "", fmt.Errorf("写入后的 app.asar 补丁未通过完整校验")
	}
	warning := ""
	if target.language == "" {
		warning = "\n提示：该版本没有独立 Language Server，已通过 app.asar 中受验证的启动脚本传递代理地址。"
	}
	return fmt.Sprintf("%s 补丁应用成功。\n应用: %s\nASAR: %s\n语言服务器: %s%s", target.name, target.app, target.asar, darwinOptionalPath(target.language), warning), nil
}

func prepareDarwinASARCandidate(sourcePath, destinationPath string) (string, error) {
	archive, err := readASAR(sourcePath)
	if err != nil {
		return "", err
	}
	mainData, err := archive.readFile("dist/main.js")
	if err != nil {
		return "", err
	}
	launcherData, err := archive.readFile("dist/languageServer.js")
	if err != nil {
		return "", err
	}
	endpoint := currentPatchProxyEndpoint()
	mainSource := strings.ReplaceAll(string(mainData), legacyDarwinASARMarker, darwinASARMarker)
	mainSource = strings.ReplaceAll(mainSource, legacyTextProxyEndpoint, endpoint.Text)
	if !strings.Contains(mainSource, darwinASARMarker) {
		if !strings.HasPrefix(mainSource, `"use strict";`) {
			return "", fmt.Errorf("当前 app.asar 主入口结构已变化，已停止补丁")
		}
		mainSource = strings.Replace(mainSource, `"use strict";`, `"use strict";`+"\n// "+darwinASARMarker, 1)
	}
	mainSource = strings.ReplaceAll(mainSource, productionEndpoint, endpoint.Text)
	mainSource = strings.ReplaceAll(mainSource, authEligibilityOriginal, authEligibilityPatched)
	if updated, result := patchImagePreviewRenderer(mainSource); result.Changed {
		mainSource = updated
	}
	launcherSource := patchDarwinCloudCodeLauncher(string(launcherData))
	if !strings.Contains(launcherSource, endpoint.Base) {
		return "", fmt.Errorf("app.asar 中的 cloud_code_endpoint 结构已变化")
	}
	// A clean backup can live in the home directory while the application is in
	// /Applications. Build beside the destination to retain same-volume rename
	// semantics when applying the candidate.
	temp, err := os.CreateTemp(filepath.Dir(destinationPath), ".antigravity-wf-asar-*")
	if err != nil {
		return "", err
	}
	candidate := temp.Name()
	if err := temp.Close(); err != nil {
		return "", err
	}
	_ = os.Remove(candidate)
	replacements := map[string][]byte{
		"dist/main.js": []byte(mainSource), "dist/languageServer.js": []byte(launcherSource),
	}
	patchImagePreviewASARRenderers(archive, replacements)
	if err := archive.write(candidate, replacements); err != nil {
		return "", err
	}
	if !darwinASARPatched(candidate) {
		_ = os.Remove(candidate)
		return "", fmt.Errorf("app.asar 补丁候选未通过完整性校验")
	}
	return candidate, nil
}

func preparePatch(path string, replacements []byteReplacement) (*patchPlan, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取 %s 失败: %w", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}

	updated := append([]byte(nil), data...)
	changed := false
	hasPatchedMarker := false
	for _, replacement := range replacements {
		if len(replacement.old) == len(replacement.new) || !strings.Contains(filepath.Base(path), "language_server") {
			if bytes.Contains(updated, replacement.old) {
				updated = bytes.ReplaceAll(updated, replacement.old, replacement.new)
				changed = true
			}
		} else {
			return nil, fmt.Errorf("语言服务器补丁长度不一致: %d != %d", len(replacement.old), len(replacement.new))
		}
		hasPatchedMarker = hasPatchedMarker || bytes.Contains(data, replacement.new)
	}
	if !changed && !hasPatchedMarker {
		return nil, fmt.Errorf("%s 中未找到受支持的 Antigravity 接口地址；该版本可能尚未适配", path)
	}
	return &patchPlan{path: path, original: data, updated: updated, mode: info.Mode(), changed: changed}, nil
}

// darwinLanguagePatchState treats a Language Server that no longer embeds a
// Cloud Code URL as valid: current releases can receive the endpoint from a
// verified JavaScript launcher. When an embedded URL exists, it must either be
// a supported Google cloudcode-pa host or this helper's fixed-size local path.
func darwinLanguagePatchState(path string) (patched, hasEmbeddedEndpoint bool) {
	if path == "" {
		return true, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false, false
	}
	hasOriginal := darwinCloudCodeURLPattern.Find(data) != nil
	endpoint := currentPatchProxyEndpoint()
	hasPatched := bytes.Contains(data, []byte(endpoint.Base+"/v1internal/"))
	hasManaged := darwinManagedBinaryEndpointPattern.Match(data)
	hasImageArchive, imageUIPatched := darwinAgentEmbeddedUIPatchStateData(data)
	if !hasOriginal && !hasManaged {
		return !hasImageArchive || imageUIPatched, false
	}
	return !hasOriginal && hasPatched && (!hasImageArchive || imageUIPatched), true
}

// darwinBinaryEndpointFor produces a byte-for-byte replacement for one
// supported Cloud Code URL. The standard production/sandbox values retain
// their readable legacy route; other supported hostnames receive an opaque
// filler segment that the local proxy strips before routing. Fixed length is
// mandatory for a Mach-O Language Server binary.
func darwinBinaryEndpointFor(original string) (string, error) {
	endpoint := currentPatchProxyEndpoint()
	switch original {
	case productionEndpoint:
		return endpoint.Binary, nil
	case sandboxEndpoint:
		return endpoint.BinarySandbox, nil
	}
	prefix := endpoint.Base + "/v1internal/"
	if len(prefix) > len(original) {
		return "", fmt.Errorf("Language Server 地址过短，无法安全替换: %s", original)
	}
	return prefix + strings.Repeat("x", len(original)-len(prefix)), nil
}

// prepareDarwinLanguagePatch updates only explicit Google cloudcode-pa URLs
// in a standalone Language Server binary. It intentionally leaves all other
// URLs untouched, preserving unknown vendor/network structures. A binary with
// no embedded Cloud Code endpoint is also valid: its verified launcher owns
// endpoint delivery, so no byte rewrite is needed.
func prepareDarwinLanguagePatch(path string) (*patchPlan, bool, error) {
	if path == "" {
		return nil, false, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false, fmt.Errorf("读取 %s 失败: %w", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, false, err
	}
	updated := []byte(normalizeDarwinManagedEndpoints(string(data)))
	changed, embedded := !bytes.Equal(data, updated), false
	seen := make(map[string]bool)
	for _, match := range darwinCloudCodeURLPattern.FindAll(data, -1) {
		original := string(match)
		if seen[original] {
			continue
		}
		seen[original] = true
		replacement, replacementErr := darwinBinaryEndpointFor(original)
		if replacementErr != nil {
			return nil, false, replacementErr
		}
		oldValue, newValue := []byte(original), []byte(replacement)
		if len(oldValue) != len(newValue) {
			return nil, false, fmt.Errorf("Language Server 补丁长度不一致: %d != %d", len(oldValue), len(newValue))
		}
		if bytes.Contains(updated, oldValue) {
			embedded = true
			updated = bytes.ReplaceAll(updated, oldValue, newValue)
			changed = true
		}
	}
	if bytes.Contains(updated, []byte(currentPatchProxyEndpoint().Base+"/v1internal/")) {
		embedded = true
	}
	plan := &patchPlan{
		path: path, original: data, updated: updated, mode: info.Mode(), changed: changed,
	}
	plan, _, err = prepareDarwinAgentEmbeddedUIPlan(plan)
	if err != nil {
		return nil, embedded, err
	}
	return plan, embedded, nil
}

func patchPlansChanged(plans []*patchPlan) bool {
	for _, plan := range plans {
		if plan != nil && plan.changed {
			return true
		}
	}
	return false
}

func saveApplyBackups(plans []*patchPlan, signingPaths []string) error {
	for _, plan := range plans {
		if plan != nil && plan.changed {
			if err := writeCurrentBackup(plan.path, plan.original); err != nil {
				return err
			}
		}
	}
	for _, path := range signingPaths {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := writeCurrentBackup(path, data); err != nil {
			return err
		}
	}
	return nil
}

func containsKnownDarwinPatch(data []byte) bool {
	if darwinManagedBinaryEndpointPattern.Match(data) {
		return true
	}
	for _, marker := range [][]byte{
		[]byte(baseProxyEndpoint), []byte(textProxyEndpoint), []byte(binaryProxyEndpoint),
		[]byte(binarySandboxProxyEndpoint), []byte(authEligibilityPatched),
		[]byte(darwinExtensionMarker), []byte(darwinASARMarker),
		[]byte(legacyTextProxyEndpoint), []byte(legacyBinaryProxyEndpoint),
		[]byte(legacyBinarySandboxProxyEndpoint), []byte(legacyDarwinExtensionMarker),
		[]byte(legacyDarwinASARMarker),
		// Every released fallback/UI marker represents an already-modified
		// renderer and remains recognisable for status/restore decisions.
		[]byte(imagePreviewPatchV2Marker), []byte(imagePreviewPatchV3Marker),
		[]byte(imagePreviewPatchV4Marker), []byte(imagePreviewPatchV5Marker),
		[]byte(imagePreviewPatchV6Marker), []byte(imagePreviewPatchV7Marker),
		[]byte(imagePreviewPatchMarker), []byte(imagePreviewNativeCompatibleMarker),
		[]byte(imageGenerationUIPatchV1Marker), []byte(imageGenerationUIPatchV2Marker),
		[]byte(imageGenerationUIPatchMarker),
		[]byte(imageGenerationDedupePatchV2Marker), []byte(imageGenerationDedupePatchV3Marker),
		[]byte(imageGenerationDedupePatchV4Marker), []byte(imageGenerationDedupePatchV5Marker),
		[]byte(imageGenerationDedupePatchMarker),
		[]byte(agentImageGenerationUIPatchMarker), []byte(agentImageGenerationDedupePatchV1Marker),
		[]byte(agentImageGenerationDedupePatchV2Marker), []byte(agentImageGenerationDedupePatchV3Marker),
		[]byte(agentImageGenerationDedupePatchMarker),
	} {
		if bytes.Contains(data, marker) {
			return true
		}
	}
	return false
}

func writePatchPlans(plans []*patchPlan) error {
	for _, plan := range plans {
		if !plan.changed {
			continue
		}
		if err := writeFileAtomic(plan.path, plan.updated, plan.mode); err != nil {
			return fmt.Errorf("写入 %s 失败: %w", plan.path, err)
		}
	}
	return nil
}

func rollbackPatchPlans(plans []*patchPlan) error {
	for _, plan := range plans {
		if plan.changed {
			if err := writeFileAtomic(plan.path, plan.original, plan.mode); err != nil {
				return err
			}
		}
	}
	return nil
}

func restoreDarwinPatch(targets darwinTargets) (message string, err error) {
	// A vendor update or a later third-party replacement must not be downgraded
	// from a stale helper backup. Restore is available while the active Agent
	// still carries a recognised WF-managed archive marker.
	if targets.kind == "agent" && !darwinASARContainsKnownPatch(targets.asar) {
		return "未发现可恢复的 macOS 升级前状态。", nil
	}
	paths := []string{targets.main, targets.extensionEntry, targets.asar, targets.language}
	if targets.kind == "agent" && targets.app != "" {
		paths = append(paths, filepath.Join(targets.app, "Contents", "Info.plist"))
	}
	paths = append(paths, darwinImagePreviewRendererPaths(targets)...)
	paths = append(paths, darwinASARUnpackedImagePreviewRendererPaths(targets)...)

	plans := make([]*patchPlan, 0, len(paths))
	seen := map[string]bool{}
	for _, path := range paths {
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		plan, changed, planErr := prepareDarwinRestorePlan(path)
		if planErr != nil {
			return "", fmt.Errorf("读取 %s 的升级前备份失败: %w", path, planErr)
		}
		if !changed {
			continue
		}
		if path == targets.language && plan.mode.Perm()&0o111 == 0 {
			plan.mode = (plan.mode &^ os.ModePerm) | 0o755
		}
		plans = append(plans, plan)
	}
	if len(plans) == 0 {
		return "未发现可恢复的 macOS 升级前状态。", nil
	}
	snapshots, snapshotErr := snapshotDarwinPatchTargets(plans)
	if snapshotErr != nil {
		return "", fmt.Errorf("创建恢复事务快照失败: %w", snapshotErr)
	}
	defer func() {
		if err == nil {
			return
		}
		if rollbackErr := restoreDarwinPatchSnapshots(snapshots); rollbackErr != nil {
			err = fmt.Errorf("%w；恢复事务回滚失败: %v", err, rollbackErr)
		}
	}()
	if err = writePatchPlans(plans); err != nil {
		return "", err
	}
	for _, plan := range plans {
		actual, readErr := os.ReadFile(plan.path)
		if readErr != nil || !bytes.Equal(actual, plan.updated) {
			if readErr != nil {
				return "", fmt.Errorf("校验恢复文件 %s 失败: %w", plan.path, readErr)
			}
			return "", fmt.Errorf("恢复文件未通过逐字节校验: %s", plan.path)
		}
	}
	return fmt.Sprintf("%s 已恢复到本次升级前状态。升级前备份已保留。", targets.name), nil
}

func restoreDarwinPatches(targets []darwinTargets) (string, error) {
	var messages []string
	for _, target := range targets {
		message, err := restoreDarwinPatch(target)
		if err != nil {
			return strings.Join(messages, "\n"), fmt.Errorf("%s 恢复失败: %w", target.name, err)
		}
		messages = append(messages, message)
	}
	return strings.Join(messages, "\n") + "\n请完全退出并重启对应的 Antigravity。", nil
}

// signPatchedDarwinLanguageServer deliberately leaves the top-level Electron
// executable untouched. Re-signing the entire app ad-hoc changes its code
// identity and blocks access to Antigravity's existing Keychain credentials,
// which makes the UI appear frozen during startup. The modified nested binary
// still needs its own valid signature to execute under macOS.
func signPatchedDarwinLanguageServer(languagePath string) error {
	if os.Getenv("ANTIGRAVITY_WF_SKIP_CODESIGN") == "1" {
		return nil
	}
	args := []string{"--force", "--sign", "-", "--preserve-metadata=entitlements,flags,runtime"}
	if out, err := exec.Command("codesign", append(args, languagePath)...).CombinedOutput(); err != nil {
		return fmt.Errorf("language_server: %s: %w", strings.TrimSpace(string(out)), err)
	}
	if out, err := exec.Command("codesign", "--verify", "--strict", languagePath).CombinedOutput(); err != nil {
		return fmt.Errorf("language_server 签名验证: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

func writeBackup(sourcePath string, data []byte) error {
	path := backupPath(sourcePath)
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return writeFileAtomic(path, data, 0o600)
}

// writeCurrentBackup keeps the restore point aligned with the state active
// immediately before this upgrade. An older restore point is archived by
// content hash before the primary backup is refreshed.
func writeCurrentBackup(sourcePath string, data []byte) error {
	path := backupPath(sourcePath)
	existing, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return writeFileAtomic(path, data, 0o600)
	}
	if err != nil {
		return err
	}
	if bytes.Equal(existing, data) {
		return nil
	}
	if err := archivePreviousBackup(path, existing); err != nil {
		return err
	}
	return writeFileAtomic(path, data, 0o600)
}

func archivePreviousBackup(path string, data []byte) error {
	digest := sha256.Sum256(data)
	history := strings.TrimSuffix(path, ".bak") + fmt.Sprintf(".previous-%x.bak", digest[:8])
	if _, err := os.Stat(history); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return writeFileAtomic(history, data, 0o600)
}

func writeFileBackup(sourcePath string) error {
	path := backupPath(sourcePath)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return copyFileAtomic(sourcePath, path, 0o600)
	} else if err != nil {
		return err
	}
	sourceDigest, err := fileSHA256(sourcePath)
	if err != nil {
		return err
	}
	backupDigest, err := fileSHA256(path)
	if err != nil {
		return err
	}
	if sourceDigest == backupDigest {
		return nil
	}
	history := strings.TrimSuffix(path, ".bak") + fmt.Sprintf(".previous-%x.bak", backupDigest[:8])
	if _, err := os.Stat(history); os.IsNotExist(err) {
		if err := copyFileAtomic(path, history, 0o600); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	return copyFileAtomic(sourcePath, path, 0o600)
}

func fileSHA256(path string) ([32]byte, error) {
	var digest [32]byte
	file, err := os.Open(path)
	if err != nil {
		return digest, err
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return digest, err
	}
	copy(digest[:], hasher.Sum(nil))
	return digest, nil
}

func backupPath(sourcePath string) string {
	dir := xiassPatcherBackupDir()
	digest := sha256.Sum256([]byte(filepath.Clean(sourcePath)))
	name := fmt.Sprintf("%s-%x.bak", filepath.Base(sourcePath), digest[:8])
	return filepath.Join(dir, name)
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".antigravity-wf-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(mode.Perm()); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func copyFileAtomic(sourcePath, targetPath string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return err
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	temp, err := os.CreateTemp(filepath.Dir(targetPath), ".antigravity-wf-copy-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(mode.Perm()); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := io.Copy(temp, source); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, targetPath)
}
