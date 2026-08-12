# Progress

## 2026-07-12

- Started tracing gateway profile creation, activation pointers, and mode-specific action rendering.
- Preserved all existing uncommitted download and PowerShell fixes in the shared worktree.
- Located the backend save function and the primary apply modal; confirmed auto-activation is missing and began auditing mode-state transitions for restart visibility.
- Read the full save and apply-modal flows; identified frontend summary propagation as part of the auto-activation contract and selected a profile-mode hard guard for restart visibility.
- Verified that profile-change callbacks perform an immediate summary refresh, allowing a backend-only active-pointer update to appear without an API contract expansion.
- Found the Setup Wizard exception to that rule and selected the shared profile refresh callback for its post-save transition.
- Confirmed route navigation already performs the missing backend refresh, so no duplicate Wizard request is required; narrowed the implementation to backend activation plus a hard UI guard.
- Resumed from the persisted plan, re-checked the dirty worktree, and confirmed unrelated macOS download and Windows PowerShell apply/restart fixes must remain untouched.
- Ran planning session catch-up and traced the Rust, mock API, and apply-dialog code paths; no unsynced implementation work was missing.
- Completed the implementation design and selected pure Rust helper tests plus static frontend/mock ownership assertions as the focused regression layer.
- Added four Rust activation-rule tests and one frontend/mock ownership test; confirmed the focused RED run fails only because the new behavior is not implemented yet.
- Implemented Rust save-time auto-activation, browser mock parity, and the gateway-safe restart-button guard.
- Focused verification passed: 4 Rust activation tests and all 7 profile-management ownership tests.
- Inspected the scoped diff and confirmed both real and mock apply APIs already reject gateway restart requests independently of the UI.
- Verified native-config synchronization affects only direct Config pointers and that route navigation refreshes the newly persisted gateway pointer immediately.
- Full Rust tests passed 330/330, frontend tests passed 155/155, and Svelte check reported 0 errors and 0 warnings; rustfmt requested one mechanical line-wrap adjustment.
- Formatting, Cargo check, production build, and diff checks passed after formatting.
- Started a fresh dev server at `http://127.0.0.1:1420/`; the browser walkthrough reached the gateway write preview and found its active-pointer description contradicted the new save behavior, so verification paused before saving.
- Updated real and mock write previews to share the auto-activation predicate, added localized first-gateway pointer copy, and localized the preview `update` action.
- Focused preview/activation verification passed: all 4 Rust rule tests and all 7 profile-management ownership tests.
- Repeated the browser workflow end to end after the preview fix: first profile auto-activated immediately, second profile preserved the first selection, gateway apply exposed no restart action, and the browser console stayed clean.
- Final validation passed: Rust 330/330, frontend 155/155, Svelte check 0 errors/0 warnings, rustfmt check, Cargo check, Vite production build, and `git diff --check`.
- Added a final static contract assertion that the real Tauri preview, not only the browser mock, uses the shared auto-activation predicate and emits an `update` pointer action.
- Closed all temporary browser verification tabs while leaving the Vite dev server running for user inspection.
