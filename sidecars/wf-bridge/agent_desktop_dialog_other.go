//go:build !darwin

package main

import (
	"context"
	"errors"

	"github.com/xyf0104/xiass-tools/wf-bridge/internal/agent"
)

func selectAgentDesktopNativeTarget(*App, context.Context, agent.ID) (string, bool, error) {
	return "", false, errors.New("native desktop application selection is unavailable on this platform")
}
