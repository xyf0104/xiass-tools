//go:build windows

package patcher

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	winapi "golang.org/x/sys/windows"
)

var windowsOperationMu sync.Mutex

// windowsASARPostReplaceHook is deliberately nil in production. Windows-only
// regression tests use it to force a validation failure after app.asar has
// been replaced, proving that a migration rolls back to the user's immediate
// pre-upgrade state rather than to the canonical clean backup.
var windowsASARPostReplaceHook func()

func runWindows(action string) (message string, err error) {
	windowsOperationMu.Lock()
	defer windowsOperationMu.Unlock()
	// Restore only consumes canonical backups; it must remain available even
	// when a hand-edited runtime port state is corrupt.
	if action == "status" {
		_ = refreshPatchProxyEndpoint()
	} else if action != "restore" {
		if err := refreshPatchProxyEndpoint(); err != nil {
			return "", err
		}
	}
	targets := locateWindowsInstallations()
	if action == "status" {
		return windowsStatusText(buildWindowsStatus(targets)), nil
	}
	if len(targets) == 0 {
		return "", fmt.Errorf("未找到可支持的 Antigravity 安装（已检查环境变量、LocalAppData、Program Files、运行中进程、注册表和各磁盘常用目录）")
	}
	defer func() {
		if err != nil {
			err = windowsPermissionHint(err)
		}
	}()
	switch action {
	case "apply", "apply-ide":
		return applyWindowsTargets(targets, action == "apply-ide")
	case "restore":
		return restoreWindowsTargets(targets)
	default:
		return "", fmt.Errorf("未知补丁操作: %s", action)
	}
}

func getWindowsStatus() Status {
	// Status remains available even if a hand-edited runtime state is invalid;
	// apply will surface that error and refuse to write an inconsistent patch.
	_ = refreshPatchProxyEndpoint()
	return buildWindowsStatus(locateWindowsInstallations())
}

func buildWindowsStatus(targets []windowsTarget) Status {
	status := Status{ProxyListening: windowsProxyListening()}
	agentInstalled, ideInstalled := false, false
	agentPatched, idePatched, ideMainPatched := true, true, true
	for _, target := range targets {
		mainPatched, _, _, patched := windowsTargetPatchState(target)
		status.Targets = append(status.Targets, TargetStatus{
			Name: target.name, Kind: target.kind, Version: target.version, AppPath: target.root,
			ExecutablePath: target.executable,
			MainPath:       target.main, ASARPath: target.asar, ExtensionPath: target.extensionEntry,
			LanguageServerPath: target.language, Patched: patched,
		})
		if status.AsarPath == "" {
			status.AsarPath = firstWindowsValue(target.asar, target.main)
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
		status.AgentPatched = windowsBoolPointer(agentPatched)
	}
	if ideInstalled {
		status.IDEPatched = windowsBoolPointer(idePatched)
		status.IdeMainPatched = windowsBoolPointer(ideMainPatched)
	}
	if !agentInstalled && ideInstalled {
		status.AgentPatched = windowsBoolPointer(idePatched)
	}
	if !ideInstalled && agentInstalled {
		status.IDEPatched = windowsBoolPointer(agentPatched)
		status.IdeMainPatched = windowsBoolPointer(agentPatched)
	}
	return status
}

func windowsStatusText(status Status) string {
	return fmt.Sprintf(
		"agent_patched=%s\nide_patched=%s\nide_main_patched=%s\nproxy_listening=%t\nasar=%s\nlanguage_server=%s\nide_extension=%s\nide_language_server=%s\n",
		windowsBoolText(status.AgentPatched), windowsBoolText(status.IDEPatched), windowsBoolText(status.IdeMainPatched),
		status.ProxyListening, status.AsarPath, status.LSPath, status.IDEExtensionPath, status.IDELSPath,
	)
}

func windowsBoolPointer(value bool) *bool { return &value }

func windowsBoolText(value *bool) string {
	if value == nil {
		return "None"
	}
	if *value {
		return "true"
	}
	return "false"
}

func firstWindowsValue(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func applyWindowsTargets(targets []windowsTarget, onlyIDE bool) (string, error) {
	var selected []windowsTarget
	for _, target := range targets {
		if !onlyIDE || target.kind == "ide" {
			selected = append(selected, target)
		}
	}
	if len(selected) == 0 {
		return "", fmt.Errorf("未找到独立 IDE 类型的 Antigravity 安装")
	}
	stopWindowsProducts(selected)
	if windowsTargetsNeedHistoryMerge(selected) {
		if err := mergeWindowsHistory(); err != nil {
			return "", fmt.Errorf("合并历史会话失败: %w", err)
		}
	}
	var messages []string
	for _, target := range selected {
		message, err := applyWindowsTarget(target)
		if err != nil {
			return strings.Join(messages, "\n\n"), fmt.Errorf("%s 补丁失败: %w", target.name, err)
		}
		messages = append(messages, message)
	}
	return strings.Join(messages, "\n\n") + "\n\n请保持本工具运行，然后重新打开对应的 Antigravity。", nil
}

func windowsTargetsNeedHistoryMerge(targets []windowsTarget) bool {
	for _, target := range targets {
		if target.kind == "ide" {
			return true
		}
	}
	return false
}

func applyWindowsTarget(target windowsTarget) (message string, err error) {
	if target.executable == "" {
		return "", fmt.Errorf("%s 安装不完整，缺少主程序", target.root)
	}
	if target.kind == "agent" {
		return applyWindowsASARTarget(target)
	}
	if target.main == "" {
		return "", fmt.Errorf("%s 中未找到主进程脚本", target.root)
	}

	mainSource, err := windowsPatchSource(target.main)
	if err != nil {
		return "", err
	}
	mainPlan, err := prepareWindowsMainPatch(mainSource)
	if err != nil {
		return "", err
	}
	mainPlan.path = target.main
	plans := []*windowsPatchPlan{mainPlan}
	if target.extensionEntry != "" {
		extensionSource, err := windowsPatchSource(target.extensionEntry)
		if err != nil {
			return "", err
		}
		extensionPlan, err := prepareWindowsExtensionPatch(extensionSource)
		if err != nil {
			return "", err
		}
		extensionPlan.path = target.extensionEntry
		plans = append(plans, extensionPlan)
	}
	languageSource, err := windowsPatchSource(target.language)
	if err != nil {
		return "", err
	}
	languagePlan, embedded, err := prepareWindowsLanguagePatch(languageSource)
	if err != nil {
		return "", err
	}
	if languagePlan != nil {
		languagePlan.path = target.language
		plans = append(plans, languagePlan)
	}
	for _, rendererPath := range windowsImagePreviewRendererPaths(target) {
		if rendererPath == target.main {
			// prepareWindowsMainPatch already covers out/main.js. The other
			// renderer bundles receive a narrowly scoped compatibility plan.
			continue
		}
		rendererSource, err := windowsPatchSource(rendererPath)
		if err != nil {
			return "", err
		}
		rendererPlan, err := prepareWindowsImagePreviewPatch(rendererSource)
		if err != nil {
			return "", err
		}
		rendererPlan.path = rendererPath
		plans = append(plans, rendererPlan)
	}
	if !windowsPlansChanged(plans) {
		return fmt.Sprintf("%s 补丁已处于激活状态，无需重复应用。", target.name), nil
	}
	if err := saveWindowsPlanBackups(plans); err != nil {
		return "", fmt.Errorf("创建补丁备份失败: %w", err)
	}
	rollbackSnapshots, snapshotErr := windowsRollbackSnapshots(plans)
	if snapshotErr != nil {
		return "", fmt.Errorf("创建事务回滚快照失败: %w", snapshotErr)
	}
	defer func() {
		if err != nil {
			if rollbackErr := restoreWindowsRollbackSnapshots(rollbackSnapshots); rollbackErr != nil {
				err = fmt.Errorf("%w；回滚到操作前状态失败: %v", err, rollbackErr)
			}
		}
	}()
	if err = writeWindowsPlans(plans); err != nil {
		return "", err
	}
	if _, _, _, patched := windowsTargetPatchState(target); !patched {
		return "", fmt.Errorf("写入后的 Windows IDE 补丁未通过完整校验")
	}
	warning := windowsLanguageWarning(target.language, embedded)
	return fmt.Sprintf(
		"%s 补丁应用成功。\n应用: %s\n主进程: %s\n扩展: %s\n语言服务器: %s%s",
		target.name, target.root, target.main, target.extensionEntry, windowsOptionalPath(target.language), warning,
	), nil
}

func applyWindowsASARTarget(target windowsTarget) (message string, err error) {
	if target.asar == "" {
		return "", fmt.Errorf("%s 中未找到 app.asar", target.root)
	}
	asarSource, err := windowsPatchSource(target.asar)
	if err != nil {
		return "", err
	}
	// Rebuild the archive only when its own endpoint or packed renderer needs
	// work. Unpacked renderer entries are patched as separate files so their
	// ASAR manifest semantics remain unchanged.
	asarChanged := !windowsASARPatched(target.asar) || asarSource != target.asar || imagePreviewASARArchiveNeedsPatch(target.asar)
	var candidate string
	if asarChanged {
		candidate, err = prepareWindowsASARCandidate(asarSource, target.asar)
		if err != nil {
			return "", err
		}
		defer os.Remove(candidate)
	}
	languageSource, err := windowsPatchSource(target.language)
	if err != nil {
		return "", err
	}
	languagePlan, embedded, err := prepareWindowsLanguagePatch(languageSource)
	if err != nil {
		return "", err
	}
	if languagePlan != nil {
		languagePlan.path = target.language
	}
	previewPlans := make([]*windowsPatchPlan, 0)
	for _, rendererPath := range windowsASARUnpackedImagePreviewRendererPaths(target) {
		rendererSource, sourceErr := windowsPatchSource(rendererPath)
		if sourceErr != nil {
			return "", sourceErr
		}
		plan, planErr := prepareWindowsImagePreviewPatch(rendererSource)
		if planErr != nil {
			return "", planErr
		}
		plan.path = rendererPath
		previewPlans = append(previewPlans, plan)
	}
	if !asarChanged && (languagePlan == nil || !languagePlan.changed) && !windowsPlansChanged(previewPlans) {
		return fmt.Sprintf("%s 补丁已处于激活状态，无需重复应用。", target.name), nil
	}
	if asarChanged {
		if err = saveWindowsBackupFrom(target.asar, asarSource); err != nil {
			return "", fmt.Errorf("创建 app.asar 备份失败: %w", err)
		}
	}
	plans := append([]*windowsPatchPlan{}, previewPlans...)
	if languagePlan != nil {
		plans = append(plans, languagePlan)
	}
	if err = saveWindowsPlanBackups(plans); err != nil {
		return "", fmt.Errorf("创建补丁备份失败: %w", err)
	}
	rollbackExtraPaths := make([]string, 0, 1)
	if asarChanged {
		rollbackExtraPaths = append(rollbackExtraPaths, target.asar)
	}
	rollbackSnapshots, snapshotErr := windowsRollbackSnapshots(plans, rollbackExtraPaths...)
	if snapshotErr != nil {
		return "", fmt.Errorf("创建事务回滚快照失败: %w", snapshotErr)
	}
	defer func() {
		if err == nil {
			return
		}
		if rollbackErr := restoreWindowsRollbackSnapshots(rollbackSnapshots); rollbackErr != nil {
			err = fmt.Errorf("%w；回滚到操作前状态失败: %v", err, rollbackErr)
		}
	}()
	if err = writeWindowsPlans(plans); err != nil {
		return "", err
	}
	if asarChanged {
		if err = windowsReplaceFile(candidate, target.asar); err != nil {
			return "", fmt.Errorf("替换 app.asar 失败: %w", err)
		}
		if windowsASARPostReplaceHook != nil {
			windowsASARPostReplaceHook()
		}
	}
	if _, _, _, patched := windowsTargetPatchState(target); !patched {
		return "", fmt.Errorf("写入后的 Windows app.asar 补丁未通过完整校验")
	}
	warning := windowsLanguageWarning(target.language, embedded)
	return fmt.Sprintf(
		"%s 补丁应用成功。\n应用: %s\nASAR: %s\n语言服务器: %s%s",
		target.name, target.root, target.asar, windowsOptionalPath(target.language), warning,
	), nil
}

func windowsPatchSource(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if !windowsContainsKnownPatch(data) {
		return path, nil
	}
	if backup := windowsExistingFile(windowsBackupPath(path)); backup != "" {
		// Re-apply every migrated patch from the clean canonical backup. This
		// keeps “恢复原始文件” byte-for-byte original instead of rotating a
		// previous helper patch into the canonical restore point.
		return backup, nil
	}
	for _, candidate := range windowsLegacyBackupPaths(path) {
		if windowsExistingFile(candidate) != "" {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%s 已带有旧版助手补丁但缺少原始备份；请重新安装该 Antigravity 后再补丁", path)
}

func windowsPlansChanged(plans []*windowsPatchPlan) bool {
	for _, plan := range plans {
		if plan != nil && plan.changed {
			return true
		}
	}
	return false
}

// windowsRollbackSnapshots captures the actual active files immediately
// before an apply operation starts writing. Patch plans may be prepared from a
// canonical clean backup during a v2/v3 migration, so plan.original is not a
// safe transaction rollback source: it can be S0 while the user's pre-upgrade
// installation is S1. The canonical backup remains solely for the explicit
// "恢复原始文件" operation.
func windowsRollbackSnapshots(plans []*windowsPatchPlan, extraPaths ...string) ([]windowsRollbackSnapshot, error) {
	paths := append([]string(nil), extraPaths...)
	for _, plan := range plans {
		if plan != nil && plan.changed {
			paths = append(paths, plan.path)
		}
	}
	seen := make(map[string]bool, len(paths))
	snapshots := make([]windowsRollbackSnapshot, 0, len(paths))
	for _, path := range paths {
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("读取 %s: %w", path, err)
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("检查 %s: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("%s 不是常规文件", path)
		}
		snapshots = append(snapshots, windowsRollbackSnapshot{
			path: path, data: data, mode: info.Mode(),
		})
	}
	return snapshots, nil
}

type windowsRollbackSnapshot struct {
	path string
	data []byte
	mode os.FileMode
}

func restoreWindowsRollbackSnapshots(snapshots []windowsRollbackSnapshot) error {
	// Restore in reverse write order. The files are independent today, but the
	// ordering keeps the helper correct if a future target introduces a loader
	// that observes a companion file while app.asar is being restored.
	for index := len(snapshots) - 1; index >= 0; index-- {
		snapshot := snapshots[index]
		if err := windowsWriteFileAtomic(snapshot.path, snapshot.data, snapshot.mode); err != nil {
			return fmt.Errorf("恢复 %s: %w", snapshot.path, err)
		}
	}
	return nil
}

func saveWindowsPlanBackups(plans []*windowsPatchPlan) error {
	for _, plan := range plans {
		if plan == nil || !plan.changed {
			continue
		}
		if err := saveWindowsBackup(plan.path, plan.original); err != nil {
			return err
		}
	}
	return nil
}

func saveWindowsBackupFrom(targetPath, sourcePath string) error {
	data, err := os.ReadFile(sourcePath)
	if err != nil {
		return err
	}
	return saveWindowsBackup(targetPath, data)
}

func saveWindowsBackup(sourcePath string, data []byte) error {
	path := windowsBackupPath(sourcePath)
	existing, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return windowsWriteFileAtomic(path, data, 0o600)
	}
	if err != nil {
		return err
	}
	if bytes.Equal(existing, data) {
		return nil
	}
	digest := sha256.Sum256(existing)
	history := strings.TrimSuffix(path, ".bak") + fmt.Sprintf(".previous-%x.bak", digest[:8])
	if windowsExistingFile(history) == "" {
		if err := windowsWriteFileAtomic(history, existing, 0o600); err != nil {
			return err
		}
	}
	return windowsWriteFileAtomic(path, data, 0o600)
}

func writeWindowsPlans(plans []*windowsPatchPlan) error {
	for _, plan := range plans {
		if plan == nil || !plan.changed {
			continue
		}
		if err := windowsWriteFileAtomic(plan.path, plan.updated, plan.mode); err != nil {
			return fmt.Errorf("写入 %s 失败: %w", plan.path, err)
		}
	}
	return nil
}

func restoreWindowsTargets(targets []windowsTarget) (string, error) {
	stopWindowsProducts(targets)
	var messages []string
	for _, target := range targets {
		restored := 0
		paths := []string{target.main, target.extensionEntry, target.asar, target.language}
		paths = append(paths, windowsImagePreviewRendererPaths(target)...)
		paths = append(paths, windowsASARUnpackedImagePreviewRendererPaths(target)...)
		seen := map[string]bool{}
		for _, path := range paths {
			if path == "" || seen[path] {
				continue
			}
			seen[path] = true
			ok, err := restoreWindowsFileIfAvailable(path)
			if err != nil {
				return strings.Join(messages, "\n"), fmt.Errorf("%s 恢复失败: %w", target.name, err)
			}
			if ok {
				restored++
			}
		}
		if restored == 0 {
			messages = append(messages, fmt.Sprintf("%s 未发现可恢复的补丁备份。", target.name))
		} else {
			messages = append(messages, fmt.Sprintf("%s 已恢复 %d 个原始文件。", target.name, restored))
		}
	}
	return strings.Join(messages, "\n") + "\n请重新打开对应的 Antigravity。", nil
}

func restoreWindowsFile(path string) error {
	ok, err := restoreWindowsFileIfAvailable(path)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("没有找到原始备份: %s", path)
	}
	return nil
}

func restoreWindowsFileIfAvailable(path string) (bool, error) {
	backup := windowsBackupPath(path)
	if windowsExistingFile(backup) == "" {
		for _, legacy := range windowsLegacyBackupPaths(path) {
			if windowsExistingFile(legacy) != "" {
				backup = legacy
				break
			}
		}
	}
	if windowsExistingFile(backup) == "" {
		return false, nil
	}
	data, err := os.ReadFile(backup)
	if err != nil {
		return false, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	if err := windowsWriteFileAtomic(path, data, info.Mode()); err != nil {
		return false, err
	}
	return true, nil
}

func windowsWriteFileAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".antigravity-wf-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(mode.Perm()); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
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
	return windowsReplaceFile(tempPath, path)
}

func windowsReplaceFile(source, target string) error {
	from, err := winapi.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := winapi.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	return winapi.MoveFileEx(from, to, winapi.MOVEFILE_REPLACE_EXISTING|winapi.MOVEFILE_WRITE_THROUGH)
}

func windowsProxyListening() bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", currentPatchProxyEndpoint().Port), 400*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func stopWindowsProducts(targets []windowsTarget) {
	names := map[string]bool{}
	for _, target := range targets {
		if target.kind == "agent" {
			names["antigravity.exe"] = true
		} else {
			names["antigravity ide.exe"] = true
		}
		for _, path := range []string{target.executable, target.language} {
			if path != "" {
				names[strings.ToLower(filepath.Base(path))] = true
			}
		}
	}
	var sorted []string
	for name := range names {
		sorted = append(sorted, name)
	}
	sort.Strings(sorted)
	for _, name := range sorted {
		cmd := exec.Command("taskkill.exe", "/F", "/T", "/IM", name)
		configureCommand(cmd)
		_ = cmd.Run()
	}
	time.Sleep(700 * time.Millisecond)
}

func windowsLanguageWarning(path string, embedded bool) string {
	if path == "" {
		return "\n提示：该版本没有独立 Language Server，已通过入口脚本传递代理地址。"
	}
	if !embedded {
		return "\n提示：该 Language Server 不再内置固定地址，已跳过二进制替换并通过入口脚本传递代理地址。"
	}
	return ""
}

func windowsOptionalPath(path string) string {
	if path == "" {
		return "（此版本无独立文件）"
	}
	return path
}

func windowsPermissionHint(err error) error {
	if errors.Is(err, os.ErrPermission) || errors.Is(err, winapi.ERROR_ACCESS_DENIED) ||
		strings.Contains(strings.ToLower(err.Error()), "access is denied") {
		return fmt.Errorf("%w；安装目录需要管理员权限，请右键 XIASS Tools 并选择“以管理员身份运行”", err)
	}
	return err
}

func mergeWindowsHistory() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	return mergeWindowsHistoryAt(home)
}

func mergeWindowsHistoryOnStartup() error {
	windowsOperationMu.Lock()
	defer windowsOperationMu.Unlock()
	return mergeWindowsHistory()
}

func mergeWindowsHistoryAt(home string) error {
	geminiRoot := filepath.Join(home, ".gemini")
	target := filepath.Join(geminiRoot, "antigravity")
	resources := []string{
		"annotations", "brain", "browser_recordings", "context_state", "conversations", "html_artifacts",
		"implicit", "knowledge", "playground", "plugins", "prompting", "scratch",
	}
	entries, err := os.ReadDir(geminiRoot)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var sources []string
	for _, entry := range entries {
		name := strings.ToLower(entry.Name())
		if !entry.IsDir() || !strings.HasPrefix(name, "antigravity") ||
			name == "antigravity" || strings.Contains(name, "antigravity-wf-backup") || strings.Contains(name, "antigravity-"+legacyPatcherProductToken()+"-backup") {
			continue
		}
		source := filepath.Join(geminiRoot, entry.Name())
		for _, resource := range resources {
			if info, statErr := os.Stat(filepath.Join(source, resource)); statErr == nil && info.IsDir() {
				sources = append(sources, source)
				break
			}
		}
	}
	if len(sources) == 0 {
		return nil
	}
	sort.Strings(sources)
	if err := os.MkdirAll(target, 0o700); err != nil {
		return err
	}
	for _, source := range sources {
		backup := source + ".antigravity-wf-backup"
		if _, statErr := os.Stat(backup); os.IsNotExist(statErr) {
			if err := copyWindowsTreeMissing(source, backup); err != nil {
				return err
			}
		}
		for _, resource := range resources {
			if err := copyWindowsTreeMissing(filepath.Join(source, resource), filepath.Join(target, resource)); err != nil {
				return err
			}
		}
		config := filepath.Join(source, "mcp_config.json")
		if windowsExistingFile(config) != "" && windowsExistingFile(filepath.Join(target, "mcp_config.json")) == "" {
			if err := copyWindowsFile(config, filepath.Join(target, "mcp_config.json")); err != nil {
				return err
			}
		}
	}
	return nil
}

func mergeWindowsHistorySource(source, target string, resources []string) error {
	for _, resource := range resources {
		if err := copyWindowsTreeMissing(filepath.Join(source, resource), filepath.Join(target, resource)); err != nil {
			return err
		}
	}
	config := filepath.Join(source, "mcp_config.json")
	if windowsExistingFile(config) != "" && windowsExistingFile(filepath.Join(target, "mcp_config.json")) == "" {
		return copyWindowsFile(config, filepath.Join(target, "mcp_config.json"))
	}
	return nil
}

func copyWindowsTreeMissing(source, target string) error {
	info, err := os.Stat(source)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !info.IsDir() {
		return nil
	}
	var files []string
	if err := filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			files = append(files, path)
		}
		return nil
	}); err != nil {
		return err
	}
	sort.Strings(files)
	for _, path := range files {
		relative, _ := filepath.Rel(source, path)
		destination := filepath.Join(target, relative)
		if windowsExistingFile(destination) == "" {
			if err := copyWindowsFile(path, destination); err != nil {
				return err
			}
		}
	}
	return nil
}

func copyWindowsFile(source, target string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(target), ".antigravity-wf-copy-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(info.Mode().Perm()); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := io.Copy(temp, input); err != nil {
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
	return windowsReplaceFile(tempPath, target)
}
