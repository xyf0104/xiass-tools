//go:build !darwin

package codexdesktop

import "context"

// Discover is intentionally inert outside macOS. It exists so the package can
// be cross-compiled by shared tooling without performing an unsupported probe.
func (detector *Detector) Discover(context.Context) Status {
	status := Status{State: StateDegraded, CheckedAt: detector.now()}
	addWarning(&status, WarningEnvironmentUnavailable)
	return status
}
