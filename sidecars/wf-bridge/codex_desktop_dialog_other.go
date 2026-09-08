//go:build !darwin

package main

import (
	"context"
	"errors"
)

func selectCodexDesktopNativeTarget(*App, context.Context) (string, bool, error) {
	return "", false, errors.New("native Codex Desktop selection is unavailable on this platform")
}
