//go:build !windows

package patcher

import "os/exec"

func configureCommand(_ *exec.Cmd) {}
