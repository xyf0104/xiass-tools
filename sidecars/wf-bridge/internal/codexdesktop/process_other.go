//go:build !darwin

package codexdesktop

import (
	"context"
	"errors"
)

// This keeps the package itself cross-compilable. Discovery is implemented in
// detector_darwin.go and is intentionally unavailable on other targets.
type systemProcessLister struct{}

func (systemProcessLister) List(context.Context) ([]Process, error) {
	return nil, errors.New("process listing is unavailable on this target")
}
