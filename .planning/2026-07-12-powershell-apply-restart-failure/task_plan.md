# PowerShell Apply-and-Restart Failure

Goal: make profile "apply and restart" launch reliably on Windows and surface actionable PowerShell diagnostics instead of an empty `PowerShell execution failed:` message.

### Phase 1: Trace and Reproduce
**Status:** complete

- [x] Locate the exact error producer and every apply-and-restart caller.
- [x] Identify the failing PowerShell command, exit-code handling, and lost stderr boundary.
- [x] Add a focused regression test that demonstrates the failure.

### Phase 2: Implement Fix
**Status:** complete

- [x] Correct the PowerShell execution or restart command construction.
- [x] Preserve useful stdout/stderr and exit-code diagnostics.
- [x] Keep other profile apply and launch behavior unchanged.

### Phase 3: Verify
**Status:** complete

- [x] Run focused Rust and frontend tests.
- [x] Run relevant formatting, check, and build validation.
- [x] Inspect the final diff and Windows behavior boundary.

## Errors Encountered

| Error | Attempt | Resolution |
|---|---|---|
| User-facing error is only `PowerShell execution failed:` with no detail | Initial report | Trace the producer and retain structured process diagnostics in the fix. |
| PowerShell `rg` received wildcard file paths literally and returned Windows error 123 | Search existing restart tests | Re-run against directories with `-g '*tests.rs'` and `-g '*.test.mjs'` filters. |
| First MSIX metadata probe used a double-quoted outer PowerShell string, which expanded inner `$pkg`/`$null` variables and caused a parser error | Read-only package probe | Re-run with a literal single-quoted script string so the child receives the intended PowerShell source. |
| New ChatGPT restart-target regression test fails because `ChatGPT.exe` is missing | RED test | Expected failure; update the compatibility process list and rerun after implementation. |
