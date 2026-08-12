# Progress

## 2026-07-12

- Started tracing the Windows profile apply-and-restart PowerShell failure.
- Preserved the existing uncommitted macOS download changes and created an isolated plan for this bug.
- Located the exact error producer and traced the apply-and-restart entrypoint through profile activation into the Windows stop/launch pipeline.
- Confirmed the common PowerShell wrapper drops stdout and exit-code context on failure.
- Mapped both PowerShell-backed stages in the Windows restart flow and prepared a harmless reproduction path that cannot target real application processes.
- Reproduced the empty diagnostic with a stdout-only nonzero PowerShell process and confirmed existing tests do not cover this behavior.
- Located the local state database and prepared a narrow, non-secret query for the most recent profile activity and active application mappings.
- Confirmed a read-only SQLite client is available and noted the need to account for WAL-backed recent state.
- Queried only non-secret profile identifiers and activity records; Codex is the strongest local-state hypothesis, but the snapshot is stale and cannot identify the exact current stage.
- Expanded the Codex/Claude packaged-app launch chain and found duplicated Codex shutdown plus additional PowerShell-backed MSIX activation helpers.
- Reproduced an access-denied failure at the exact `Get-CimInstance Win32_Process` command used by the restart stop stage; prepared exact child-process capture to verify how the error reaches Rust.
- Confirmed a second concrete bug: current `ChatGPT` desktop processes are absent from the Codex restart target's legacy-only process-name list.
- Selected a three-part fix: current-generation process names, safe `Get-Process` fallback for CIM denial, and exit-code/stdout-aware PowerShell diagnostics.
- Added the current-generation restart regression test and confirmed RED: `ChatGPT.exe` is absent from the Codex desktop target.
- Completed Phase 1 and started the implementation phase.
- Implemented current/legacy desktop process compatibility, safe CIM-to-`Get-Process` fallback, stage-specific restart errors, and exit-code/stdout-aware PowerShell diagnostics.
- Passed the three focused pure regression tests after formatting.
- Added harmless Windows runtime tests for stdout-only PowerShell failure reporting and CIM-denied/no-match restart inventory.
- Full regression passed with 326 Rust library tests and 154 frontend unit tests.
- Completed Phase 2 and started final verification.
- Final Cargo check, rustfmt check, Svelte check, production build, and diff integrity check all passed.
- Reviewed the Windows safety boundary: exclusion-sensitive CLI/backend targets are never guessed when CIM command-line inspection is unavailable.
- Marked all phases complete; the only omitted test is the destructive UI end-to-end restart of the process hosting this task.
