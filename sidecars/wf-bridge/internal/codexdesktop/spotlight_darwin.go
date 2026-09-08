//go:build darwin

package codexdesktop

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
)

const maxSpotlightOutputBytes = 256 << 10

var errSpotlightOutputLimit = errors.New("Spotlight output exceeded the fixed discovery limit")

// systemBundleFinder invokes only the system Spotlight executable with an
// exact, built-in bundle identifier. It does not use a shell, search a user
// profile, or pass any user-controlled query text to the operating system.
// The detector validates every returned path independently before reporting an
// installation.
type systemBundleFinder struct{}

func (systemBundleFinder) FindBundles(ctx context.Context, bundleIdentifier string, limit int) ([]string, error) {
	if bundleIdentifier != expectedBundleIdentifier || limit <= 0 {
		return nil, errors.New("unsupported Spotlight lookup")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	command := exec.CommandContext(ctx, "/usr/bin/mdfind", spotlightBundleQuery(bundleIdentifier))
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := command.Start(); err != nil {
		return nil, err
	}

	// Command.Output would retain unbounded output in memory. Read at most one
	// byte beyond the limit; if that bound is crossed, cancel the command before
	// waiting so a pathological index cannot keep discovery running.
	data, readErr := io.ReadAll(io.LimitReader(stdout, maxSpotlightOutputBytes+1))
	if len(data) > maxSpotlightOutputBytes {
		if command.Cancel != nil {
			_ = command.Cancel()
		}
		_ = command.Wait()
		return nil, errSpotlightOutputLimit
	}
	waitErr := command.Wait()
	if readErr != nil {
		return nil, readErr
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if waitErr != nil {
		return nil, waitErr
	}

	paths := make([]string, 0, limit)
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		path := strings.TrimSpace(string(line))
		if path == "" {
			continue
		}
		paths = append(paths, path)
		if len(paths) == limit {
			break
		}
	}
	return paths, nil
}

func spotlightBundleQuery(bundleIdentifier string) string {
	// bundleIdentifier is checked against the built-in expected identifier by
	// FindBundles. It is deliberately not configurable by callers or UI input.
	return "kMDItemCFBundleIdentifier == '" + bundleIdentifier + "'"
}
