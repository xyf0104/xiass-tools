package patcher

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// Status holds the parsed output of patch_core.py status.
type Status struct {
	AgentPatched     *bool
	IDEPatched       *bool
	IdeMainPatched   *bool
	ProxyListening   bool
	AsarPath         string
	LSPath           string
	IDEExtensionPath string
	IDELSPath        string
	Targets          []TargetStatus
}

// TargetStatus describes one detected macOS Antigravity installation. The
// legacy aggregate fields above remain populated for UI/API compatibility.
type TargetStatus struct {
	Name               string
	Kind               string
	Version            string
	AppPath            string
	ExecutablePath     string
	MainPath           string
	ASARPath           string
	ExtensionPath      string
	LanguageServerPath string
	// Supported describes whether this exact target passed the bounded
	// structural checks required for its connection method. Discovery alone is
	// never enough to authorise a write.
	Supported      bool
	ConnectionMode string
	Reason         string
	Patched        bool
}

func patchCoreScript() string {
	// walk up from this file's location at build time; at runtime find relative to executable
	exe, _ := os.Executable()
	dir := filepath.Dir(exe)
	// try: exe/../../../patch_core.py  (when running from app/build/bin/)
	for _, rel := range []string{"../patch_core.py", "../../patch_core.py", "../../../patch_core.py"} {
		p := filepath.Join(dir, rel)
		if _, err := os.Stat(p); err == nil {
			return filepath.Clean(p)
		}
	}
	return ""
}

func pythonExe() string {
	if runtime.GOOS == "windows" {
		for _, name := range []string{"py", "python", "python3"} {
			if p, err := exec.LookPath(name); err == nil {
				return p
			}
		}
	}
	for _, name := range []string{"python3", "python"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return "python"
}

// Run executes patch_core.py with the given action and returns stdout+stderr.
func Run(action string) (string, error) {
	if runtime.GOOS == "darwin" {
		return runDarwin(action)
	}
	if runtime.GOOS == "windows" {
		return runWindows(action)
	}

	script := patchCoreScript()
	if script == "" {
		return "", fmt.Errorf("patch_core.py not found")
	}

	var cmd *exec.Cmd
	py := pythonExe()
	if py == "py" || strings.HasSuffix(py, "\\py.exe") {
		cmd = exec.Command(py, "-3", script, action)
	} else {
		cmd = exec.Command(py, script, action)
	}
	configureCommand(cmd)

	out, err := cmd.CombinedOutput()
	return string(out), err
}

// GetStatus parses the output of patch_core.py status into a Status struct.
func GetStatus() Status {
	if runtime.GOOS == "darwin" {
		return getDarwinStatus()
	}
	if runtime.GOOS == "windows" {
		return getWindowsStatus()
	}
	out, _ := Run("status")
	return parseStatus(out)
}

// GetQuickStatus is the lightweight status API used during the first UI paint.
// On macOS it checks only standard/saved bundle paths (or a metadata-valid
// discovery cache) and deliberately skips process/Spotlight and deep
// ASAR/renderer compatibility scans.
func GetQuickStatus() Status {
	if runtime.GOOS == "darwin" {
		return getDarwinQuickStatus()
	}
	return GetStatus()
}

// RefreshStatus explicitly performs a fresh authoritative discovery pass.
func RefreshStatus() Status {
	if runtime.GOOS == "darwin" {
		return refreshDarwinStatus()
	}
	return GetStatus()
}

func parseBoolPtr(s string) *bool {
	s = strings.TrimSpace(s)
	if s == "True" || s == "true" {
		v := true
		return &v
	}
	if s == "False" || s == "false" {
		v := false
		return &v
	}
	return nil
}

func parseStatus(out string) Status {
	var s Status
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		switch k {
		case "agent_patched":
			s.AgentPatched = parseBoolPtr(v)
		case "ide_patched":
			s.IDEPatched = parseBoolPtr(v)
		case "ide_main_patched":
			s.IdeMainPatched = parseBoolPtr(v)
		case "proxy_listening":
			s.ProxyListening, _ = strconv.ParseBool(v)
		case "asar":
			s.AsarPath = v
		case "language_server":
			s.LSPath = v
		case "ide_extension":
			s.IDEExtensionPath = v
		case "ide_language_server":
			s.IDELSPath = v
		}
	}
	return s
}
