package codexdesktop

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

// LifecycleConfirmation is deliberately a human-readable acknowledgement, not
// a secret or an authorization mechanism. Wails callers must pass it verbatim
// to an explicitly invoked Stop or Restart operation. This prevents ordinary
// status/configuration calls from ever becoming a hidden process-control path.
const LifecycleConfirmation = "CONFIRM_CODEX_DESKTOP_LIFECYCLE"

const (
	defaultLifecycleTimeout = 15 * time.Second
	lifecyclePollInterval   = 250 * time.Millisecond
)

// ControlStatus is the complete redacted state required by a desktop-control
// UI. It intentionally omits the selected filesystem path, launch target,
// process identifiers, command arguments, and operating-system errors.
type ControlStatus struct {
	Installation Installation `json:"installation"`
	Discovered   bool         `json:"discovered"`
	Selected     bool         `json:"selected"`
	Running      bool         `json:"running"`
	CanSelect    bool         `json:"canSelect"`
	CanLaunch    bool         `json:"canLaunch"`
	CanStop      bool         `json:"canStop"`
	CanRestart   bool         `json:"canRestart"`
	Warnings     []Warning    `json:"warnings,omitempty"`
}

// ErrSelectionRejected is intentionally generic. In particular it never
// includes a user-provided filesystem path.
var ErrSelectionRejected = errors.New("the selected application is not a verified Codex Desktop installation")

var (
	ErrNoVerifiedInstallation = errors.New("no verified Codex Desktop installation is available")
	ErrConfirmationRequired   = errors.New("explicit lifecycle confirmation is required")
	ErrDesktopAlreadyRunning  = errors.New("Codex Desktop is already running")
	ErrDesktopNotRunning      = errors.New("Codex Desktop is not running")
	ErrLifecycleUnavailable   = errors.New("Codex Desktop lifecycle control is unavailable")
	ErrLifecycleTimeout       = errors.New("Codex Desktop did not reach the requested state in time")
)

// desktopTarget is never serialized or returned from this package. It is kept
// only in memory and is revalidated immediately before every launch, stop, or
// restart operation so a later path substitution cannot turn the controller
// into an arbitrary-program launcher.
type desktopTarget struct {
	location     string
	executable   string
	launchTarget string
	storePackage string
	installation Installation
}

type controlOperations struct {
	launch func(context.Context, desktopTarget) error
	stop   func(context.Context, desktopTarget) error
}

// Controller provides a narrow, explicit lifecycle surface on top of the
// existing read-only detector. It does not persist a selected path and never
// reads or writes Codex configuration, history, credentials, or account data.
type Controller struct {
	detector   *Detector
	operations controlOperations

	mu       sync.Mutex
	selected *desktopTarget
}

// NewController creates a controller using only the platform's verified
// public application shapes. A manual selection exists in memory for the
// current XIASS Tools process only; subsequent calls still revalidate it.
func NewController(options ...Options) *Controller {
	return &Controller{
		detector:   New(options...),
		operations: systemControlOperations(),
	}
}

func newControllerForTest(detector *Detector, operations controlOperations) *Controller {
	if detector == nil {
		detector = New()
	}
	if operations.launch == nil || operations.stop == nil {
		operations = systemControlOperations()
	}
	return &Controller{detector: detector, operations: operations}
}

// Status performs a read-only status calculation. It does not choose a new
// target, start a process, or close an application.
func (controller *Controller) Status(ctx context.Context) ControlStatus {
	if controller == nil {
		return ControlStatus{CanSelect: false, Warnings: []Warning{WarningEnvironmentUnavailable}}
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	status, _ := controller.statusLocked(ctx)
	return status
}

// SelectPath validates one local candidate obtained through the native picker
// or an explicit, one-shot manual renderer action. The path is never copied
// into ControlStatus or an error message, and the validated target remains in
// memory only for the current process. A caller must always use this strict
// structural validation path before treating any user-provided path as
// launchable.
func (controller *Controller) SelectPath(ctx context.Context, value string) (ControlStatus, error) {
	if controller == nil || strings.TrimSpace(value) == "" {
		return ControlStatus{CanSelect: false}, ErrSelectionRejected
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()

	target, err := platformValidateDesktopTarget(controller.detector, value)
	if err != nil {
		status, _ := controller.statusLocked(ctx)
		return status, ErrSelectionRejected
	}
	controller.selected = &target
	status, _ := controller.statusLocked(ctx)
	return status, nil
}

// Launch starts only a currently structure-validated target. It is an
// explicit action; status/configuration calls never invoke it implicitly.
func (controller *Controller) Launch(ctx context.Context) (ControlStatus, error) {
	if controller == nil {
		return ControlStatus{CanSelect: false}, ErrLifecycleUnavailable
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if ctx != nil && ctx.Err() != nil {
		status, _ := controller.statusLocked(context.Background())
		return status, ErrLifecycleUnavailable
	}
	status, target := controller.statusLocked(ctx)
	if target == nil {
		return status, ErrNoVerifiedInstallation
	}
	if status.Running {
		return status, ErrDesktopAlreadyRunning
	}
	verifiedTarget, err := controller.revalidateTargetLocked(*target)
	if err != nil {
		return controller.statusAfterFailureLocked(ctx), ErrNoVerifiedInstallation
	}
	operationContext, cancel := controller.operationContext(ctx)
	defer cancel()
	if controller.operations.launch == nil || controller.operations.launch(operationContext, verifiedTarget) != nil {
		return controller.statusAfterFailureLocked(ctx), ErrLifecycleUnavailable
	}
	if err := controller.waitForTargetStateLocked(operationContext, verifiedTarget, true); err != nil {
		return controller.statusAfterFailureLocked(ctx), err
	}
	status, _ = controller.statusLocked(ctx)
	return status, nil
}

// Stop asks the validated desktop application to close gracefully. It never
// force-kills a process. The caller must include LifecycleConfirmation in this
// direct method call; otherwise no operating-system action is attempted.
func (controller *Controller) Stop(ctx context.Context, confirmation string) (ControlStatus, error) {
	if controller == nil {
		return ControlStatus{CanSelect: false}, ErrLifecycleUnavailable
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if strings.TrimSpace(confirmation) != LifecycleConfirmation {
		status, _ := controller.statusLocked(ctx)
		return status, ErrConfirmationRequired
	}
	if ctx != nil && ctx.Err() != nil {
		status, _ := controller.statusLocked(context.Background())
		return status, ErrLifecycleUnavailable
	}
	return controller.stopLocked(ctx)
}

// Restart gracefully closes the selected/auto-discovered verified target,
// verifies that it exited, then launches that same revalidated target. A
// failed close never leads to a launch, and no forced termination is used.
func (controller *Controller) Restart(ctx context.Context, confirmation string) (ControlStatus, error) {
	if controller == nil {
		return ControlStatus{CanSelect: false}, ErrLifecycleUnavailable
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if strings.TrimSpace(confirmation) != LifecycleConfirmation {
		status, _ := controller.statusLocked(ctx)
		return status, ErrConfirmationRequired
	}
	if ctx != nil && ctx.Err() != nil {
		status, _ := controller.statusLocked(context.Background())
		return status, ErrLifecycleUnavailable
	}
	status, target := controller.statusLocked(ctx)
	if target == nil {
		return status, ErrNoVerifiedInstallation
	}
	if !status.CanRestart {
		return status, ErrDesktopNotRunning
	}
	verifiedTarget, err := controller.revalidateTargetLocked(*target)
	if err != nil {
		return controller.statusAfterFailureLocked(ctx), ErrNoVerifiedInstallation
	}
	operationContext, cancel := controller.operationContext(ctx)
	defer cancel()
	if controller.operations.stop == nil || controller.operations.stop(operationContext, verifiedTarget) != nil {
		return controller.statusAfterFailureLocked(ctx), ErrLifecycleUnavailable
	}
	if err := controller.waitForTargetStateLocked(operationContext, verifiedTarget, false); err != nil {
		return controller.statusAfterFailureLocked(ctx), err
	}
	if controller.operations.launch == nil || controller.operations.launch(operationContext, verifiedTarget) != nil {
		return controller.statusAfterFailureLocked(ctx), ErrLifecycleUnavailable
	}
	if err := controller.waitForTargetStateLocked(operationContext, verifiedTarget, true); err != nil {
		return controller.statusAfterFailureLocked(ctx), err
	}
	status, _ = controller.statusLocked(ctx)
	return status, nil
}

func (controller *Controller) stopLocked(ctx context.Context) (ControlStatus, error) {
	status, target := controller.statusLocked(ctx)
	if target == nil {
		return status, ErrNoVerifiedInstallation
	}
	if !status.CanStop {
		return status, ErrDesktopNotRunning
	}
	verifiedTarget, err := controller.revalidateTargetLocked(*target)
	if err != nil {
		return controller.statusAfterFailureLocked(ctx), ErrNoVerifiedInstallation
	}
	operationContext, cancel := controller.operationContext(ctx)
	defer cancel()
	if controller.operations.stop == nil || controller.operations.stop(operationContext, verifiedTarget) != nil {
		return controller.statusAfterFailureLocked(ctx), ErrLifecycleUnavailable
	}
	if err := controller.waitForTargetStateLocked(operationContext, verifiedTarget, false); err != nil {
		return controller.statusAfterFailureLocked(ctx), err
	}
	status, _ = controller.statusLocked(ctx)
	return status, nil
}

func (controller *Controller) statusAfterFailureLocked(ctx context.Context) ControlStatus {
	status, _ := controller.statusLocked(ctx)
	return status
}

func (controller *Controller) revalidateTargetLocked(target desktopTarget) (desktopTarget, error) {
	validated, err := platformRevalidateDesktopTarget(controller.detector, target)
	if err != nil {
		if controller.selected != nil {
			controller.selected = nil
		}
		return desktopTarget{}, ErrNoVerifiedInstallation
	}
	// A user-selected target remains marked as manual, while an automatically
	// discovered target keeps its public source classification. Only the path
	// and structure are re-read; no application data is touched.
	validated.installation.Source = target.installation.Source
	return validated, nil
}

func (controller *Controller) statusLocked(ctx context.Context) (ControlStatus, *desktopTarget) {
	if ctx == nil {
		ctx = context.Background()
	}
	discovery := controller.detector.Discover(ctx)
	status := ControlStatus{
		Installation: discovery.Installation,
		Discovered:   discovery.Installation.Present,
		CanSelect:    platformSelectionAvailable(),
		Warnings:     append([]Warning(nil), discovery.Warnings...),
	}

	var target *desktopTarget
	if controller.selected != nil {
		validated, err := platformRevalidateDesktopTarget(controller.detector, *controller.selected)
		if err != nil {
			controller.selected = nil
			addControlWarning(&status, WarningInvalidInstallation)
		} else {
			validated.installation.Source = SourceManualSelection
			controller.selected = &validated
			target = &validated
			status.Selected = true
			status.Discovered = true
			status.Installation = validated.installation
		}
	}
	if target == nil {
		if discovered := platformDiscoverDesktopTarget(controller.detector); discovered != nil {
			target = discovered
			status.Discovered = true
			status.Installation = discovered.installation
		}
	}
	if target == nil {
		// Discover may have found a separately running, verified installation
		// without a deterministic lifecycle target. Preserve that safety state:
		// status callers must not mistake it for a safe opportunity to launch or
		// write while another Codex Desktop instance is active.
		status.Running = discovery.Running
		return status, nil
	}

	targetRunning, err := controller.targetRunningLocked(ctx, *target)
	if err != nil {
		addControlWarning(&status, WarningProcessListUnavailable)
		return status, target
	}
	// The selected/deterministic target is never silently replaced with a
	// different process merely because that process is running. Discovery still
	// contributes its all-verified-instance running state so callers cannot
	// write history/configuration while another valid Codex Desktop is active.
	// Lifecycle actions remain available only when the target itself is the
	// running instance; otherwise stopping or restarting would act on the wrong
	// application target.
	status.Running = discovery.Running || targetRunning
	status.CanLaunch = !status.Running
	status.CanStop = targetRunning
	status.CanRestart = targetRunning
	return status, target
}

func (controller *Controller) targetRunningLocked(ctx context.Context, target desktopTarget) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	processContext, cancel := context.WithTimeout(ctx, controller.detector.processTimeout())
	defer cancel()
	processes, err := controller.detector.processLister().List(processContext)
	if err != nil {
		return false, err
	}
	if err := processContext.Err(); err != nil {
		return false, err
	}
	for _, process := range processes {
		if platformTargetMatchesProcess(controller.detector, target, process) {
			return true, nil
		}
	}
	return false, nil
}

func (controller *Controller) waitForTargetStateLocked(ctx context.Context, target desktopTarget, wanted bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	deadline := time.NewTimer(defaultLifecycleTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(lifecyclePollInterval)
	defer ticker.Stop()
	for {
		running, err := controller.targetRunningLocked(ctx, target)
		if err != nil {
			return ErrLifecycleUnavailable
		}
		if running == wanted {
			return nil
		}
		select {
		case <-ctx.Done():
			return ErrLifecycleUnavailable
		case <-deadline.C:
			return ErrLifecycleTimeout
		case <-ticker.C:
		}
	}
}

func (controller *Controller) operationContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, defaultLifecycleTimeout)
}

func addControlWarning(status *ControlStatus, warning Warning) {
	for _, current := range status.Warnings {
		if current == warning {
			return
		}
	}
	status.Warnings = append(status.Warnings, warning)
}
