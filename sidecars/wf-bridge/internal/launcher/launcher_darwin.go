//go:build darwin

package launcher

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func platformSupported() bool { return true }

func platformIsRunning(appPath string) (bool, error) {
	path, err := validateDarwinAppPath(appPath)
	if err != nil {
		return false, err
	}
	output, err := exec.Command("/bin/ps", "-axo", "pid=,command=").Output()
	if err != nil {
		return false, fmt.Errorf("读取运行状态失败: %w", err)
	}
	return len(darwinRunningPIDs(string(output), path)) > 0, nil
}

func platformQuitGracefully(appPath string, timeout time.Duration) error {
	path, err := validateDarwinAppPath(appPath)
	if err != nil {
		return err
	}
	running, err := platformIsRunning(path)
	if err != nil || !running {
		return err
	}

	script := fmt.Sprintf("tell application \"%s\" to quit", escapeAppleScriptString(path))
	if output, err := exec.Command("/usr/bin/osascript", "-e", script).CombinedOutput(); err != nil {
		return fmt.Errorf("无法请求 %s 正常退出: %s: %w", filepath.Base(path), strings.TrimSpace(string(output)), err)
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		running, err = platformIsRunning(path)
		if err != nil {
			return err
		}
		if !running {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("%s 未在 %s 内正常退出；已停止重启且不会强制结束进程，请处理未保存内容后重试", filepath.Base(path), timeout.Round(time.Second))
}

func platformLaunch(appPath string) error {
	path, err := validateDarwinAppPath(appPath)
	if err != nil {
		return err
	}
	if output, err := exec.Command("/usr/bin/open", path).CombinedOutput(); err != nil {
		return fmt.Errorf("启动 %s 失败: %s: %w", filepath.Base(path), strings.TrimSpace(string(output)), err)
	}
	return nil
}

func platformWaitUntilRunning(appPath string, timeout time.Duration) error {
	path, err := validateDarwinAppPath(appPath)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		running, err := platformIsRunning(path)
		if err != nil {
			return err
		}
		if running {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("%s 启动超时，请从 Finder 手动打开并检查系统提示", filepath.Base(path))
}

func validateDarwinAppPath(appPath string) (string, error) {
	clean := filepath.Clean(strings.TrimSpace(appPath))
	if clean == "." || !strings.EqualFold(filepath.Ext(clean), ".app") {
		return "", fmt.Errorf("无效的 macOS 应用路径: %s", appPath)
	}
	abs, err := filepath.Abs(clean)
	if err != nil {
		return "", fmt.Errorf("解析应用路径失败: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("应用不存在: %s", abs)
	}
	return abs, nil
}

func darwinRunningPIDs(psOutput, appPath string) []int {
	prefix := filepath.Clean(appPath) + string(os.PathSeparator) + "Contents" + string(os.PathSeparator)
	var pids []int
	for _, line := range strings.Split(psOutput, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, prefix) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err == nil {
			pids = append(pids, pid)
		}
	}
	return pids
}

func escapeAppleScriptString(value string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value)
}
