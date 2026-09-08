//go:build darwin

package codexdesktop

import (
	"context"
	"os/exec"
	"strings"
)

// systemProcessLister uses ps only as a bounded, read-only process snapshot.
// The comm field deliberately omits command arguments, which may contain user
// data. The detector uses the value only for an exact path comparison and does
// not return it in Status.
type systemProcessLister struct{}

func (systemProcessLister) List(ctx context.Context) ([]Process, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	command := exec.CommandContext(ctx, "/bin/ps", "-ax", "-o", "comm=")
	output, err := command.Output()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, err
	}

	lines := strings.Split(string(output), "\n")
	processes := make([]Process, 0, len(lines))
	for _, line := range lines {
		path := strings.TrimSpace(line)
		if path != "" {
			processes = append(processes, Process{Executable: path})
		}
	}
	return processes, nil
}
