# Cross-platform download architecture audit

Goal: audit every managed software download/update path on Windows and macOS for process-architecture versus hardware/platform-architecture mistakes, then fix confirmed defects with regression coverage.

## Phase 1: Inventory and feedback loops
Status: complete

- [x] Enumerate managed software and download/update/install entry points.
- [x] Identify architecture-selection seams and build deterministic regression tests.

## Phase 2: Diagnosis and fixes
Status: complete

- [x] Classify each path as safe, affected, or architecture-independent.
- [x] Fix every confirmed Windows/macOS architecture-selection defect.

## Phase 3: Verification
Status: complete

- [x] Run focused regressions, backend tests, frontend tests, checks, and diff hygiene checks.
- [x] Report audited coverage and residual hardware-only verification limits.

## Errors Encountered

| Error | Attempt | Resolution |
|---|---|---|
| `cargo fmt -- --check` could not find `Cargo.toml` | Parallel verification | Re-run from `src-tauri` with `cargo fmt -- --check` after edits. |
| `cargo test` rejected two positional filters | Parallel verification | Run a single shared filter or two separate test invocations. |
| Claude ARM64 redirect probe timed out while downloading after HTTP 200 | Endpoint capability probe | Treat the final ARM64 MSIX URL and transferred bytes as availability proof; use metadata endpoint for small-body checks. |
| Two source-slicing tests failed after a top-level `#[cfg(test)]` import | Full Rust test suite | Removed the top-level marker and referenced the test helper by its full module path. |
| Two frontend static tests expected process-architecture wording and a fixed Claude x64 constant | Frontend unit suite | Updated the contracts to assert native hardware detection and dynamic x64/arm64 release routing. |
| `cargo check --target aarch64-apple-darwin` could not find an Apple-target `cc` compiler | macOS cross-check from Windows | Recorded as an environment limitation; pure architecture tests cover macOS selection and macOS must be compiled on the release Mac. |
