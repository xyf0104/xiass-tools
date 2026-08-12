# Gateway Profile Auto Apply

Goal: automatically activate the first gateway profile created for a tool whose gateway active-profile pointer is empty, and remove apply-and-restart from every gateway-profile action surface.

### Phase 1: Trace Behavior
**Status:** complete

- [x] Trace profile creation, active gateway pointers, and save-result refresh.
- [x] Locate every apply-and-restart UI surface and its mode guards.
- [x] Add focused regression tests for both requested behaviors.

### Phase 2: Implement
**Status:** complete

- [x] Auto-activate a newly created gateway profile only when that tool has no gateway pointer.
- [x] Preserve an existing active gateway profile when additional profiles are created.
- [x] Remove apply-and-restart from gateway profile UI without changing direct configuration actions.
- [x] Make the create-preview pointer row describe first-gateway auto-activation accurately.

### Phase 3: Verify
**Status:** complete

- [x] Run focused Rust and frontend tests.
- [x] Run formatting, check, and production build validation.
- [x] Inspect final state propagation and diff boundaries.

## Errors Encountered

| Error | Attempt | Resolution |
|---|---|---|
| `rg` received `src/lib/*.test.mjs` as a literal Windows path and returned error 123 | Search UI tests | Re-run against `src/lib` with `-g '*.test.mjs'`. |
| Focused Rust and frontend regression tests failed before implementation | RED test run | Expected: missing Rust helper and missing mock/UI guards prove the tests cover the requested gaps. |
| `cargo fmt -- --check` requested standard wrapping for the new gateway-pointer scan | Full verification | Run `cargo fmt`, then re-run the formatting check. |
| Browser preview still said saving never changes the active pointer | End-to-end gateway creation walkthrough | Update real and mock preview generation to reflect conditional first-gateway activation, then repeat verification. |
| Cleanup reported an earlier failed browser tab was no longer part of the session | Close temporary verification tabs | Confirmed the browser tab list is empty; no product or test state was affected. |
