# Burn Post-install Launch Fix

Goal: ensure Burn's Open CodeStudio Lite action resolves and starts the executable from the MSI's actual installed location after install, repair, and unattended update flows.

## Phase 1: Reproduction and Cause
Status: complete

- [x] Reproduce the installed-path mismatch on the local machine.
- [x] Add a red regression test for post-Apply executable resolution.
- [x] Confirm the smallest failing path contract.

## Phase 2: Fix
Status: complete

- [x] Resolve the executable from authoritative installed-product/registry state after Apply.
- [x] Preserve custom install folders and legacy compatibility.
- [x] Improve launch failure diagnostics with attempted locations.

## Phase 3: Verification
Status: complete

- [x] Run focused installer tests.
- [x] Compile and verify the Burn bundle.
- [x] Run relevant full tests and whitespace checks.

## Errors Encountered

| Error | Attempt | Resolution |
|---|---|---|
| Standalone `dotnet build` could not resolve WiX BootstrapperCore types | First compile check | Re-run with the same `WixToolsPath` property used by `build-burn.ps1`. |
| Current sandbox denied access to the local updater signing key | Published Burn rebuild | Build and verify an isolated bundle under `target/burn/verification` without overwriting the signed release artifact. |
| `verify-burn.ps1` could not observe the interactive window | Isolated bundle verification | Logs showed successful BA initialization/detection; direct probing confirmed this environment denies the verifier's `Win32_Process` CIM query. Retained successful compile and plan-only verification as the non-installing checks. |
| Recursive cleanup of generated verification output was rejected by environment policy | Post-verification cleanup | Left the generated files under ignored `src-tauri/target/burn`; no tracked or release artifact was affected. |
