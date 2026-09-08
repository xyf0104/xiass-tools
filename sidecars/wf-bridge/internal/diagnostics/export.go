// Package diagnostics owns credential-safe local logging and support exports.
package diagnostics

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	goruntime "runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	applicationLogName           = "xiass-tools.log"
	legacyApplicationLogName     = "wf-assistant.log"
	preUpgradeApplicationLogName = "xiass-tools-pre-upgrade.log"
	maxApplicationLogBytes       = int64(4 << 20)
	maxExportedFileBytes         = int64(2 << 20)
	maxAntigravityLogBytes       = int64(12 << 20)
	diagnosticArchiveFormat      = 1
)

var (
	logMu           sync.Mutex
	logFile         *os.File
	logStorageDir   string
	logHomeDir      string
	jsonSecret      = regexp.MustCompile(`(?i)("(?:api[_-]?key|authorization|access[_-]?token|refresh[_-]?token|id[_-]?token|csrf[_-]?token|client[_-]?secret|password|cookie|set-cookie|credential)"\s*:\s*")[^"]*(")`)
	jsonArraySecret = regexp.MustCompile(`(?i)("(?:authorization|x-api-key|api-key|access[_-]?token|refresh[_-]?token|id[_-]?token|csrf[_-]?token|client[_-]?secret|password|cookie|set-cookie)"\s*:\s*\[\s*")[^"]*(")`)
	jsonImageData   = regexp.MustCompile(`(?i)("(?:b64_json|b64Json|image_base64|imageBase64|base64)"\s*:\s*")[^"]*(")`)
	headerSecret    = regexp.MustCompile(`(?i)((?:authorization|x-api-key|api-key|access[_ -]?token|refresh[_ -]?token|csrf[_ -]?token|client[_ -]?secret|password|cookie|set-cookie)\s*[:=]\s*)(?:Bearer\s+)?[^\s,;]+`)
	commandSecret   = regexp.MustCompile(`(?i)((?:--(?:csrf_token|extension_server_csrf_token|api_key|access_token|refresh_token|id_token|client_secret|password))\s+)[^\s]+`)
	oauthCodeSecret = regexp.MustCompile(`(?i)((?:oauth|authorization)\s+code\s*[:=]\s*)[^\s,;]+`)
	urlSecret       = regexp.MustCompile(`(?i)([?&](?:key|api_key|token|access_token|refresh_token|id_token|csrf_token|code|client_secret)=)[^&\s"']+`)
	knownSecret     = regexp.MustCompile(`\b(?:(?:sk|ghp|github_pat|xox[baprs])[-_A-Za-z0-9]{12,}|AIza[A-Za-z0-9_-]{20,})\b`)
	jwtSecret       = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{12,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`)
	longBase64Data  = regexp.MustCompile(`\b[A-Za-z0-9+/]{256,}={0,2}\b`)
)

type applicationLogWriter struct{}

func (applicationLogWriter) Write(data []byte) (int, error) {
	logMu.Lock()
	defer logMu.Unlock()
	originalLength := len(data)
	if logFile == nil {
		return originalLength, nil
	}
	data = redact(data, logHomeDir)
	if int64(len(data)) > maxApplicationLogBytes {
		marker := []byte("[oversized log record truncated]\n")
		data = append(marker, data[len(data)-int(maxApplicationLogBytes)+len(marker):]...)
	}
	if info, err := logFile.Stat(); err == nil && info.Size()+int64(len(data)) > maxApplicationLogBytes {
		if err := rotateApplicationLogLocked(); err != nil {
			return 0, err
		}
	}
	if _, err := logFile.Write(data); err != nil {
		return 0, err
	}
	return originalLength, nil
}

// Options contains only renderer-safe metadata. Snapshot should be a redacted
// runtime status object; Export applies a second redaction pass before writing.
type Options struct {
	Version             string
	Snapshot            any
	HomeDir             string
	AntigravityLogRoots []string
}

type archiveSummary struct {
	Format       int    `json:"format"`
	ExportedAt   string `json:"exportedAt"`
	AppVersion   string `json:"appVersion"`
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	Snapshot     any    `json:"snapshot,omitempty"`
}

type CollectedLog struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

// CollectLocalLogs returns the same fixed, bounded and redacted helper-owned
// log set used by Export. It is intended for the authenticated Tauri bridge so
// the parent can build one diagnostic archive without receiving storage paths.
func CollectLocalLogs(storageDir, home string) ([]CollectedLog, error) {
	result := make([]CollectedLog, 0, 6)
	for _, name := range []string{
		preUpgradeApplicationLogName + ".1",
		preUpgradeApplicationLogName,
		applicationLogName + ".1",
		applicationLogName,
		"proxy-trace.jsonl",
		"proxy_runtime.json",
	} {
		data, err := readTail(filepath.Join(storageDir, name), maxExportedFileBytes)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("无法收集 WF 诊断日志")
		}
		result = append(result, CollectedLog{
			Name:    filepath.ToSlash(filepath.Join("wf-helper", name)),
			Content: string(boundTail(redact(data, home), maxExportedFileBytes)),
		})
	}
	return result, nil
}

// Init installs a bounded persistent application log. Existing log calls do
// not need to change, and the file is retained solely in the private XIASS
// Tools data directory for an explicit user-initiated diagnostic export.
func Init(storageDir string) error {
	logMu.Lock()
	defer logMu.Unlock()
	if logFile != nil {
		return nil
	}
	if err := os.MkdirAll(storageDir, 0o700); err != nil {
		return err
	}
	if err := migrateLegacyApplicationLogs(storageDir); err != nil {
		return err
	}
	logStorageDir = storageDir
	logHomeDir, _ = os.UserHomeDir()
	path := filepath.Join(storageDir, applicationLogName)
	if info, exists, err := regularFileInfo(path); err != nil {
		return err
	} else if exists && info.Size() > maxApplicationLogBytes {
		if err := rotateApplicationLogPath(path); err != nil {
			return err
		}
	}
	file, err := openApplicationLogFile(path)
	if err != nil {
		return err
	}
	_ = file.Chmod(0o600)
	logFile = file
	log.SetFlags(log.LstdFlags | log.Lmicroseconds | log.LUTC)
	log.SetOutput(io.MultiWriter(applicationLogWriter{}, os.Stderr))
	return nil
}

// migrateLegacyApplicationLogs preserves the bounded log history created by
// earlier app names without ever replacing a current XIASS Tools log. Current
// rotation slots remain owned by the active logger; old rotated history moves
// to a bounded pre-upgrade slot so a later normal rotation cannot erase it.
func migrateLegacyApplicationLogs(storageDir string) error {
	for _, name := range []string{
		applicationLogName,
		applicationLogName + ".1",
		preUpgradeApplicationLogName,
		preUpgradeApplicationLogName + ".1",
	} {
		if _, _, err := regularFileInfo(filepath.Join(storageDir, name)); err != nil {
			return err
		}
	}
	if err := moveLegacyApplicationLog(storageDir, legacyApplicationLogName, applicationLogName, preUpgradeApplicationLogName); err != nil {
		return err
	}
	return moveLegacyApplicationLog(storageDir, legacyApplicationLogName+".1", preUpgradeApplicationLogName+".1", "")
}

// moveLegacyApplicationLog never overwrites a current or already-preserved
// entry. If both names are occupied, the legacy source remains untouched.
func moveLegacyApplicationLog(storageDir, legacyName, primaryName, fallbackName string) error {
	legacyPath := filepath.Join(storageDir, legacyName)
	legacyInfo, legacyExists, err := legacyLogFileInfo(legacyPath)
	if err != nil || !legacyExists {
		return err
	}
	targetPath := filepath.Join(storageDir, primaryName)
	if _, targetExists, err := regularFileInfo(targetPath); err != nil {
		return err
	} else if targetExists {
		if strings.TrimSpace(fallbackName) == "" {
			return nil
		}
		targetPath = filepath.Join(storageDir, fallbackName)
		if _, fallbackExists, err := regularFileInfo(targetPath); err != nil {
			return err
		} else if fallbackExists {
			return nil
		}
	}
	if current, exists, err := legacyLogFileInfo(legacyPath); err != nil {
		return err
	} else if !exists || !os.SameFile(legacyInfo, current) {
		return fmt.Errorf("诊断日志文件在迁移时发生不安全变更")
	}
	if _, exists, err := regularFileInfo(targetPath); err != nil {
		return err
	} else if exists {
		return nil
	}
	if err := os.Rename(legacyPath, targetPath); err != nil {
		return err
	}
	if moved, exists, err := regularFileInfo(targetPath); err != nil {
		return err
	} else if !exists || !os.SameFile(legacyInfo, moved) {
		return fmt.Errorf("诊断日志文件在迁移后无法验证")
	}
	return nil
}

func rotateApplicationLogLocked() error {
	if logFile == nil || strings.TrimSpace(logStorageDir) == "" {
		return nil
	}
	path := filepath.Join(logStorageDir, applicationLogName)
	if err := logFile.Close(); err != nil {
		return err
	}
	logFile = nil
	if err := rotateApplicationLogPath(path); err != nil {
		file, reopenErr := openApplicationLogFile(path)
		if reopenErr == nil {
			logFile = file
		}
		return err
	}
	file, err := openApplicationLogFile(path)
	if err != nil {
		return err
	}
	_ = file.Chmod(0o600)
	logFile = file
	return nil
}

func rotateApplicationLogPath(path string) error {
	current, exists, err := regularFileInfo(path)
	if err != nil || !exists {
		return err
	}
	previousPath := path + ".1"
	previous, previousExists, err := regularFileInfo(previousPath)
	if err != nil {
		return err
	}
	if latest, latestExists, err := regularFileInfo(path); err != nil {
		return err
	} else if !latestExists || !os.SameFile(current, latest) {
		return fmt.Errorf("诊断日志文件在轮转时发生不安全变更")
	}
	if previousExists {
		if latest, latestExists, err := regularFileInfo(previousPath); err != nil {
			return err
		} else if !latestExists || !os.SameFile(previous, latest) {
			return fmt.Errorf("诊断轮转日志文件在轮转时发生不安全变更")
		}
		if err := os.Remove(previousPath); err != nil {
			return err
		}
	}
	if err := os.Rename(path, previousPath); err != nil {
		return err
	}
	if moved, movedExists, err := regularFileInfo(previousPath); err != nil {
		return err
	} else if !movedExists || !os.SameFile(current, moved) {
		return fmt.Errorf("诊断日志文件在轮转后无法验证")
	}
	return nil
}

// regularFileInfo rejects a symlink or special file. It is used for every
// current XIASS Tools log path before it is opened, moved, or exported.
func regularFileInfo(path string) (fs.FileInfo, bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("诊断日志文件不是常规文件")
	}
	return info, true, nil
}

// legacyLogFileInfo deliberately ignores old symlinks and special files so an
// upgrade preserves the original item without ever following or replacing it.
func legacyLogFileInfo(path string) (fs.FileInfo, bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, false, nil
	}
	return info, true, nil
}

func openApplicationLogFile(path string) (*os.File, error) {
	initial, existed, err := regularFileInfo(path)
	if err != nil {
		return nil, err
	}
	flags := os.O_APPEND | os.O_WRONLY
	if !existed {
		// Do not follow a path that appears after Lstat. O_EXCL makes a
		// concurrent creation (including a symlink) a safe initialization
		// failure instead of an opportunity to create a file elsewhere.
		flags |= os.O_CREATE | os.O_EXCL
	}
	// No write happens until the opened descriptor has been checked below. A
	// replacement with a symlink will therefore fail closed before any data is
	// emitted or permissions are changed.
	file, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return nil, err
	}
	opened, statErr := file.Stat()
	if statErr != nil || !opened.Mode().IsRegular() || (existed && !os.SameFile(initial, opened)) {
		_ = file.Close()
		return nil, fmt.Errorf("诊断日志文件在打开时发生不安全变更")
	}
	final, finalExists, finalErr := regularFileInfo(path)
	if finalErr != nil || !finalExists || !os.SameFile(opened, final) {
		_ = file.Close()
		if finalErr != nil {
			return nil, finalErr
		}
		return nil, fmt.Errorf("诊断日志文件在打开后无法验证")
	}
	return file, nil
}

// Export creates one ZIP chosen by the user. It deliberately excludes model,
// account and OAuth storage files; every included text file is redacted again.
func Export(destination, storageDir string, options Options) error {
	destination = strings.TrimSpace(destination)
	if destination == "" {
		return fmt.Errorf("诊断日志保存位置为空")
	}
	if !strings.EqualFold(filepath.Ext(destination), ".zip") {
		destination += ".zip"
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return fmt.Errorf("无法创建诊断日志目录: %w", err)
	}
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("无法创建诊断日志: %w", err)
	}
	success := false
	defer func() {
		_ = file.Close()
		if !success {
			_ = os.Remove(destination)
		}
	}()

	writer := zip.NewWriter(file)
	summary := archiveSummary{
		Format: diagnosticArchiveFormat, ExportedAt: time.Now().UTC().Format(time.RFC3339),
		AppVersion: strings.TrimSpace(options.Version), OS: goruntime.GOOS,
		Architecture: goruntime.GOARCH, Snapshot: options.Snapshot,
	}
	summaryData, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return fmt.Errorf("无法生成诊断摘要: %w", err)
	}
	if err := addArchiveBytes(writer, "diagnostic-summary.json", boundTail(redact(summaryData, options.HomeDir), maxExportedFileBytes)); err != nil {
		return err
	}

	for _, name := range []string{
		preUpgradeApplicationLogName + ".1",
		preUpgradeApplicationLogName,
		applicationLogName + ".1",
		applicationLogName,
		"proxy-trace.jsonl",
		"proxy_runtime.json",
	} {
		path := filepath.Join(storageDir, name)
		if err := addSanitizedFile(writer, filepath.ToSlash(filepath.Join("xiass-tools", name)), path, maxExportedFileBytes, options.HomeDir); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("无法收集 %s: %w", name, err)
		}
	}
	roots := options.AntigravityLogRoots
	if len(roots) == 0 {
		roots = defaultAntigravityLogRoots(options.HomeDir)
	}
	if err := addAntigravityLogs(writer, roots, options.HomeDir); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("无法完成诊断日志压缩: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("无法保存诊断日志: %w", err)
	}
	_ = os.Chmod(destination, 0o600)
	success = true
	return nil
}

func defaultAntigravityLogRoots(home string) []string {
	configDir, _ := os.UserConfigDir()
	if configDir == "" && home != "" {
		if goruntime.GOOS == "darwin" {
			configDir = filepath.Join(home, "Library", "Application Support")
		} else {
			configDir = home
		}
	}
	var roots []string
	for _, name := range []string{"Antigravity", "Antigravity 2", "Antigravity2"} {
		if configDir != "" {
			roots = append(roots, filepath.Join(configDir, name, "logs"))
		}
	}
	return roots
}

func addAntigravityLogs(writer *zip.Writer, roots []string, home string) error {
	seen := map[string]struct{}{}
	var total int64
	for rootIndex, root := range roots {
		root = filepath.Clean(strings.TrimSpace(root))
		if root == "." || root == "" {
			continue
		}
		key := strings.ToLower(root)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		session := latestLogSession(root)
		if session == "" {
			continue
		}
		err := filepath.WalkDir(session, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil || entry.IsDir() || !allowedAntigravityLog(entry.Name()) || total >= maxAntigravityLogBytes {
				return nil
			}
			remaining := maxAntigravityLogBytes - total
			limit := maxExportedFileBytes
			if remaining < limit {
				limit = remaining
			}
			data, err := readTail(path, limit)
			if err != nil {
				return nil
			}
			data = boundTail(redact(data, home), limit)
			relative, err := filepath.Rel(session, path)
			if err != nil || strings.HasPrefix(relative, "..") {
				return nil
			}
			name := filepath.ToSlash(filepath.Join("antigravity", fmt.Sprintf("source-%d", rootIndex+1), filepath.Base(session), relative))
			if err := addArchiveBytes(writer, name, data); err != nil {
				return err
			}
			total += int64(len(data))
			return nil
		})
		if err != nil {
			return fmt.Errorf("无法收集 Antigravity 日志: %w", err)
		}
	}
	return nil
}

func latestLogSession(root string) string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	type candidate struct {
		path string
		mod  time.Time
	}
	var sessions []candidate
	hasDirectLogs := false
	for _, entry := range entries {
		if !entry.IsDir() {
			hasDirectLogs = hasDirectLogs || allowedAntigravityLog(entry.Name())
			continue
		}
		info, err := entry.Info()
		if err == nil {
			sessions = append(sessions, candidate{path: filepath.Join(root, entry.Name()), mod: info.ModTime()})
		}
	}
	if len(sessions) == 0 {
		if hasDirectLogs {
			return root
		}
		return ""
	}
	sort.Slice(sessions, func(i, j int) bool {
		if sessions[i].mod.Equal(sessions[j].mod) {
			return sessions[i].path > sessions[j].path
		}
		return sessions[i].mod.After(sessions[j].mod)
	})
	return sessions[0].path
}

func allowedAntigravityLog(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "ls-main.log", "cloudcode.log", "main.log", "auth.log", "artifacts.log", "renderer.log", "views.log", "antigravity.log", "antigravity cockpit.log":
		return true
	default:
		return false
	}
}

func addSanitizedFile(writer *zip.Writer, archiveName, path string, limit int64, home string) error {
	data, err := readTail(path, limit)
	if err != nil {
		return err
	}
	return addArchiveBytes(writer, archiveName, boundTail(redact(data, home), limit))
}

func readTail(path string, limit int64) ([]byte, error) {
	if limit <= 0 {
		return nil, nil
	}
	expected, exists, err := regularFileInfo(path)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, fs.ErrNotExist
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(expected, info) {
		return nil, fmt.Errorf("诊断日志文件在读取时发生不安全变更")
	}
	marker := []byte("[earlier content truncated]\n")
	truncated := info.Size() > limit
	readLimit := limit
	if truncated {
		readLimit -= int64(len(marker))
		if readLimit < 0 {
			readLimit = 0
		}
		if _, err := file.Seek(-readLimit, io.SeekEnd); err != nil {
			return nil, err
		}
	}
	data, err := io.ReadAll(io.LimitReader(file, readLimit))
	if err != nil {
		return nil, err
	}
	if truncated {
		data = append(marker, data...)
	}
	final, finalExists, finalErr := regularFileInfo(path)
	if finalErr != nil || !finalExists || !os.SameFile(expected, final) {
		if finalErr != nil {
			return nil, finalErr
		}
		return nil, fmt.Errorf("诊断日志文件在读取后无法验证")
	}
	return data, nil
}

func boundTail(data []byte, limit int64) []byte {
	if limit <= 0 || int64(len(data)) <= limit {
		return data
	}
	marker := []byte("[earlier content truncated]\n")
	keep := int(limit) - len(marker)
	if keep <= 0 {
		return marker[:min(len(marker), int(limit))]
	}
	return append(marker, data[len(data)-keep:]...)
}

func addArchiveBytes(writer *zip.Writer, name string, data []byte) error {
	header := &zip.FileHeader{Name: filepath.ToSlash(name), Method: zip.Deflate}
	header.SetMode(0o600)
	header.SetModTime(time.Now())
	entry, err := writer.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = io.Copy(entry, bytes.NewReader(data))
	return err
}

func redact(data []byte, home string) []byte {
	value := string(data)
	value = jsonSecret.ReplaceAllString(value, `${1}[REDACTED]${2}`)
	value = jsonArraySecret.ReplaceAllString(value, `${1}[REDACTED]${2}`)
	value = jsonImageData.ReplaceAllString(value, `${1}[REDACTED]${2}`)
	value = headerSecret.ReplaceAllString(value, `${1}[REDACTED]`)
	value = commandSecret.ReplaceAllString(value, `${1}[REDACTED]`)
	value = oauthCodeSecret.ReplaceAllString(value, `${1}[REDACTED]`)
	value = urlSecret.ReplaceAllString(value, `${1}[REDACTED]`)
	value = knownSecret.ReplaceAllString(value, `[REDACTED]`)
	value = jwtSecret.ReplaceAllString(value, `[REDACTED]`)
	value = longBase64Data.ReplaceAllString(value, `[REDACTED_BINARY]`)
	home = strings.TrimSpace(home)
	if home != "" {
		value = strings.ReplaceAll(value, home, "~")
		value = strings.ReplaceAll(value, strings.ReplaceAll(home, `\`, `\\`), `~`)
	}
	return []byte(value)
}
