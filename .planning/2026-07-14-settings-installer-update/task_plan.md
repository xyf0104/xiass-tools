# Settings Installer Update

Goal: when a newer CodeStudio Lite version is detected, show an immediate-update button beside the current version in Settings, download the platform installer from R2, then hand off to Burn on Windows or perform a DMG-based update on macOS.

## Phase 1: Existing updater contract
Status: complete

- [x] Map the Settings update UI and current Tauri updater state flow.
- [x] Map R2 manifest generation/publishing and platform artifact naming.
- [x] Define a signed installer-update manifest contract for Burn EXE and macOS DMG.

## Phase 2: Regression tests
Status: complete

- [x] Add red tests for the version-adjacent immediate-update button.
- [x] Add red tests for Windows Burn and macOS DMG artifact selection and handoff.
- [x] Add red tests for localized progress, failure, and retry states.

## Phase 3: Implementation
Status: complete

- [x] Implement the installer download and integrity-verification backend.
- [x] On Windows, launch Burn and exit only after successful handoff.
- [x] On macOS, mount the DMG, replace the application safely, and relaunch.
- [x] Wire Settings UI state and localization.
- [x] Update R2 manifest/build/publish scripts to use Burn and DMG artifacts.

## Phase 4: Verification
Status: complete

- [x] Run targeted frontend/backend tests and type checks.
- [x] Run full tests and production build.
- [x] Verify Windows packaging and inspect macOS scripts statically on Windows.

## Phase 5: Relaunch after automatic installation
Status: complete

- [x] Add a regression test for the updater-to-Burn relaunch contract.
- [x] Pass an explicit relaunch flag and launch only after a successful Burn apply.
- [x] Re-run installer tests and a real Burn build.

## Phase 6: Version 1.5.0 release manifest
Status: complete

- [x] Update all five release version surfaces to 1.5.0.
- [x] Flatten R2 artifact keys to `releases/<version>/<filename>` while retaining Tauri platform keys.
- [x] Build and sign the 1.5.0 Windows Burn artifact.
- [x] Generate and validate the 1.5.0 `latest.json` from real artifacts.

## Errors Encountered

| Error | Attempt | Resolution |
|---|---|---|
| PowerShell malformed an `rg` alternation while checking Cargo.lock | Dependency inspection | Switched to targeted manifest and crate-source reads; no product action was repeated. |
| Login-shell prompt initialization locked an oh-my-posh cache file | First signer help lookup | Re-ran the read-only command with `login=false`; signer help succeeded. |
| Rust checks were first invoked from the repository root | Verification | Re-ran with `--manifest-path src-tauri/Cargo.toml`; updater unit tests passed. |
| macOS signer command and following `if` were joined by a patch newline error | First script edit | Inspected the script immediately and restored the missing newline before verification. |
