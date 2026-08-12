# Burn No-Window Startup Fix

Goal: make the Windows Burn installer reliably show a foreground UI, tolerate legacy installation metadata, and surface initialization/runtime failures instead of silently exiting.

## Agreed Behavior Seams

- Interactive double-click launches a visible, foreground-capable installer window.
- Invalid legacy `InstallLocation` values fall back safely instead of aborting startup.
- Bootstrapper initialization exceptions are logged and shown to the user.
- Missing managed runtime is handled by an authored Burn prerequisite contract.

## Phase 1: Regression tests
Status: complete

- Add red installer contract tests for the four seams.

## Phase 2: Startup hardening
Status: complete

- Add initialization boundary and legacy path fallback.
- Activate and foreground the WPF window after source initialization.

## Phase 3: Runtime prerequisite
Status: complete

- Author and build a compatible .NET prerequisite without embedding a large runtime.

## Phase 4: Build and runtime verification
Status: complete

- Run focused/full tests, build Burn, run plan-only, and verify interactive child-window visibility.

## Phase 5: Review and commit
Status: complete

- Run two-axis review and commit only product/test files.

Completed in local commit `d97d0da` after Standards and Spec reviews reported no blocking findings.

## Errors Encountered

| Error | Attempt | Resolution |
|---|---|---|
| WiX 3 `LogLevel` has no `Warning` member | First managed BA compile | Use `LogLevel.Standard` with an explicit `Warning:` message prefix. |
| Burn verification expected `NetFx48Web` cache mode `remove` | First production bundle verification | Inspect the built manifest and verify its actual security/behavior contract: permanent, per-machine, `netfx4`, release threshold, external Microsoft payload. |
| Internal WPF ready-event probe did not exit | Three runtime verification attempts | Replace the test-only BA hook with an external clean-room process probe that verifies a non-zero window handle, expected title, and a responsive UI without starting Apply. |
| Probe cleanup raced the Burn log handle | First external window probe | Wait for both parent and clean-room child processes to exit before reading and deleting the verification log. |
