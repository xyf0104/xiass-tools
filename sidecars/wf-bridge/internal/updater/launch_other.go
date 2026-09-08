//go:build !darwin && !windows

package updater

import "fmt"

func LaunchInstaller(path string) error {
	return fmt.Errorf("当前系统不支持自动启动安装包")
}
