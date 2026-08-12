# macOS ChatGPT DMG Download Resilience

Goal: make every macOS in-app download path recover from direct transport failures by honoring the macOS system proxy, while preserving integrity checks and existing install behavior.

### Phase 1: Trace and Reproduce
**Status:** complete

- [x] Trace all macOS download entry points, retries, and error propagation.
- [x] Identify shared and module-specific network boundaries and available platform-native fallbacks.
- [x] Define a narrow fix with regression coverage.

### Phase 2: Implement Resilient Download
**Status:** complete

- [x] Add a bounded macOS system-proxy fallback across all in-app download paths.
- [x] Preserve SHA-256 verification, progress reporting, cancellation, and caching.
- [x] Add focused tests for fallback selection and failure messages.

### Phase 3: Verify
**Status:** complete

- [x] Run focused Rust and frontend tests.
- [x] Run formatting/check/build validation relevant to the changed paths.
- [x] Inspect the final diff and macOS-specific behavior.

## Errors Encountered

| Error | Attempt | Resolution |
|---|---|---|
| `init-session.ps1` reused the existing root planning files instead of creating an isolated plan | Initialize task plan | Created an isolated `.planning` directory manually and updated `.planning/.active_plan`. |
| A PowerShell `rg` expression for macOS cfg markers was parsed with an unclosed group | Inventory platform-specific callers | Replaced it with narrower literal searches per file/function. |
| New macOS proxy fallback regression test could not resolve the not-yet-implemented transport type/functions | RED test | Expected compile failure; proceed with the shared download module implementation. |
| Offline Cargo test could not resolve the newly required `system-configuration` crate | First shared-module test | Retry dependency resolution with network access, then return to offline-capable tests after the lockfile is updated. |
| `cargo fmt --check` reported formatting differences in the new module and adjusted tests | First formatting check | Run `cargo fmt --all`, then rerun the check after compilation issues are resolved. |
| Warning-cleanup patch assumed no platform cfg between the old helper and test module | Remove obsolete imports/helper | Re-read the exact tail and applied a smaller context-aware patch; the failed patch made no changes. |
| First Claude Inspector patch assumed different import grouping | Protect localhost traffic from system proxy | Re-read the actual cfg-gated imports and applied the direct-client change with exact context. |
| PowerShell wildcard paths were passed literally to `rg` while checking tests | Search stale test expectations | Re-ran the search against directories with `-g` filters. |
| Offline Apple target tree lacked cached macOS-only crates; Windows reqwest tree query was ambiguous between versions | Verify target feature isolation | Specified `reqwest@0.12.28`, verified Windows offline, then resolved the Apple target tree online. |
| `cargo fmt --check` reordered the new Claude direct-client import/call | Post-audit formatting | Ran `cargo fmt --all` and continued with target checks. |
| Apple arm64 `cargo check` stopped in `ring` because the Windows host has no Apple-target `cc`/SDK | Cross-target compile check | Verified Apple dependency/features with `cargo tree`; record the host limitation and rely on full native tests plus target-specific metadata in this environment. |
| A combined PowerShell `rg` regex for final network-call inventory lost escaped quotes and failed with an unclosed group | Final diff review | Switched to separate literal searches so shell parsing cannot alter the patterns. |
