//go:build windows

package updater

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// LaunchInstaller starts the already verified XIASS Windows installer. The
// updater only calls this with an exact release asset selected by
// DownloadLatestInstaller; keep the launch path explicit and avoid shell
// interpolation so spaces and user-controlled environment values cannot alter
// the command.
func LaunchInstaller(path string) error {
	path = strings.TrimSpace(path)
	if path == "" || !strings.EqualFold(filepath.Ext(path), ".exe") {
		return fmt.Errorf("更新安装程序路径无效")
	}
	return exec.Command(path).Start()
}
