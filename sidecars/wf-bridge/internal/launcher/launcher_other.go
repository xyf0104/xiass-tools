//go:build !darwin

package launcher

import (
	"fmt"
	"time"
)

func platformSupported() bool { return false }

func platformIsRunning(string) (bool, error) { return false, nil }

func platformQuitGracefully(string, time.Duration) error {
	return fmt.Errorf("当前平台暂不支持安全重启")
}

func platformLaunch(string) error {
	return fmt.Errorf("当前平台暂不支持应用启动")
}

func platformWaitUntilRunning(string, time.Duration) error {
	return fmt.Errorf("当前平台暂不支持运行状态检测")
}
