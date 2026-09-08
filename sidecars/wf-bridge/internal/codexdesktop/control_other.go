//go:build !darwin

package codexdesktop

import "context"

func platformSelectionAvailable() bool { return false }

func platformDiscoverDesktopTarget(*Detector) *desktopTarget { return nil }

func platformValidateDesktopTarget(*Detector, string) (desktopTarget, error) {
	return desktopTarget{}, ErrSelectionRejected
}

func platformRevalidateDesktopTarget(*Detector, desktopTarget) (desktopTarget, error) {
	return desktopTarget{}, ErrSelectionRejected
}

func platformTargetMatchesProcess(*Detector, desktopTarget, Process) bool { return false }

func systemControlOperations() controlOperations {
	return controlOperations{
		launch: func(context.Context, desktopTarget) error { return ErrLifecycleUnavailable },
		stop:   func(context.Context, desktopTarget) error { return ErrLifecycleUnavailable },
	}
}
