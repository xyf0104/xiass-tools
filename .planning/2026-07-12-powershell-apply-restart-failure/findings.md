# Findings

## Initial Report

- On Windows, applying an application profile with the "apply and restart" action fails with `PowerShell execution failed:`.
- The empty suffix suggests the current error path formats only stderr even when PowerShell exits without writing stderr, or loses process-spawn/exit-code context before reaching the UI.

## Error Boundary

- The exact message is produced by `core::platform::run_powershell` and a separate Claude debugger helper; profile apply-and-restart uses the common platform helper.
- `run_powershell` reports only trimmed stderr when the process exits unsuccessfully. It omits stdout and the exit code, so a failure with empty stderr becomes exactly `PowerShell execution failed:`.
- `profile::restart_tool_for_profile` first stops matching processes and then launches the selected restart target. Both the Windows process inventory and MSIX activation paths can call `run_powershell`, so both stages must be inspected before selecting the fix.
- The stop stage executes a generated CIM/process-close script through `run_powershell`; CLI-style restart targets later execute `Start-Process` through the same wrapper.
- ChatGPT Desktop uses its dedicated launcher and Claude Desktop uses the MSIX package launcher, while Codex/Claude/Gemini/OpenCode/OpenClaw/Hermes CLI targets use the generic `Start-Process` path.

## Reproduction

- A harmless Windows PowerShell probe confirmed the diagnostic bug: a process that writes `stdout-detail` and exits with code 8 has empty stderr, so the current Rust formatter would emit only `PowerShell execution failed:`.
- A thrown PowerShell exception does populate stderr, proving the blank suffix specifically covers stdout-only or status-only failures rather than decoding all failures incorrectly.
- Existing restart tests validate target selection but do not execute the Windows PowerShell stop/launch path or assert failure diagnostics.
- CodeStudio Lite stores profile state and recent activity in `~/.codestudio-lite/app_state.sqlite`; a narrow read of application/profile identifiers can help identify the likely restart target without exposing stored secrets.
- A local `sqlite3.exe` is available. The main database file exists, though its timestamp predates the report, so the WAL state must be checked before treating the database snapshot as current.
- The database has no WAL and was last updated on July 10. It shows active direct profiles for Codex and Claude Code, with the latest launch activity belonging to Codex, making Codex the leading hypothesis but not proof of the current failing target.
- The activity log does not record the failed apply-and-restart attempt because `apply_profile` returns before appending its success event when restart raises an error.
- Codex restart first closes the profile restart target, then calls `chatgpt_desktop::launch()`, whose launcher performs its own close-before-launch sequence. This duplicates process shutdown work and creates more than one PowerShell-backed failure point.
- Packaged ChatGPT/Claude activation can also reach `platform::package` helpers that call `run_powershell`, so the generic diagnostic fix must cover package launch failures as well as process enumeration.
- A read-only probe in the current Windows environment returns access denied for `Get-CimInstance Win32_Process`. The restart stop script sets `$ErrorActionPreference = 'Stop'`, so the same denial aborts apply-and-restart before any launch occurs.
- The current tool environment is sandbox-restricted, so the CIM denial is a strong reproduction direction rather than proof that the user's normal app process has identical permissions.
- Live process inspection confirms the current official package still has identity `OpenAI.Codex`, but its desktop executable/process name is `ChatGPT.exe`/`ChatGPT`.
- `restart_targets_for_app("codex")` only lists `Codex.exe`/`Codex` for the desktop target. It therefore misses the current ChatGPT generation even when PowerShell succeeds, violating the existing old/new compatibility rule.
- A safe Windows fallback can use `Get-Process` for name-addressable targets when CIM is unavailable. Marker-only backends and targets with exclusion markers must be skipped rather than guessed, to avoid closing an unrelated Codex/Claude app-server process.

## Implemented Fix

- Codex desktop restart targets now recognize both current `ChatGPT.exe`/`ChatGPT` and legacy `Codex.exe`/`Codex` process names.
- The restart inventory attempts CIM first, then safely falls back to `Get-Process` only when the target has process names and no exclusion markers. Marker-only and exclusion-sensitive backends remain untouched when command-line inspection is unavailable.
- PowerShell failures now include the exit code, prefer stderr, fall back to stdout, and explicitly state when neither stream contains output.
- Stop-stage and launch-stage failures now identify the restart target, so any remaining error reports where the failure occurred.
- The same diagnostic formatter is reused by the Claude debugger PowerShell runner to remove the second empty-error source.

## Verification

- The Windows runtime regression executes the generated restart inventory with an impossible process name; it succeeds even though CIM access is denied in the current sandbox, proving the safe fallback path works without touching real processes.
- A second runtime regression runs a stdout-only PowerShell failure and confirms the returned message contains both exit code 8 and `stdout detail`.
- Full validation passed: 326 Rust tests, 154 frontend unit tests, Cargo check, rustfmt check, Svelte check with zero diagnostics, Vite production build, and `git diff --check`.
- A destructive end-to-end click was intentionally skipped because the active target is the ChatGPT process hosting this task; exercising it would terminate the current session.
