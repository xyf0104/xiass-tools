# WiX Burn Multilingual UI With One MSI

Goal: build one Windows Burn EXE whose WPF interface supports Simplified Chinese, Traditional Chinese, and English while chaining exactly one en-US base MSI and preserving MSI and bundle upgrades.

## Phase 1: Contract and API validation
Status: complete

- [x] Add failing static tests for the Burn source, language packages, stable identity, and Windows build entry point.
- [x] Verify the WiX 3.14 BootstrapperCore event and engine APIs available on this machine.

## Phase 2: Managed bootstrapper and bundle
Status: complete

- [x] Add the WinForms managed bootstrapper application.
- [x] Add the WiX bundle authoring with three embedded MSI packages.
- [x] Add the Windows build script and npm entry point.

## Phase 3: Build and verification
Status: complete

- [x] Run targeted and full project checks.
- [x] Build the managed BA and Burn bundle.
- [x] Inspect the built bundle manifest and launch the selector without installing.
- [x] Review the complete worktree diff.

## Phase 4: Remove MSI language transforms
Status: complete

- [x] Replace invalid Burn string conditions and lock the syntax with a regression test.
- [x] Prototype language transforms from the generated localized MSI files.
- [x] Replace three full MSI payloads with one MSI plus embedded transforms.
- [x] Generate only one en-US MSI from Tauri.
- [x] Remove MST generation, storage, selection, and verification from Burn.
- [x] Keep the three-language Burn WPF selector and system-language detection unchanged.
- [x] Rebuild, inspect size, and verify non-installing planning.

## Errors Encountered

| Error | Attempt | Resolution |
|---|---|---|
| `MSB1008` with an extra `False` project argument | First managed BA build | Removed the PowerShell-incompatible `--no-restore:$false` argument; restore is the default. |
| `CS0117` for `ApplyRestart.Required` | Second managed BA build | Reflected WiX 3.14 and used its actual `ApplyRestart.RestartRequired` member. |
| `LGHT0197` for missing `WixMbaPrereq*` variables, then `CNDL0010` requiring a license | First WiX link | Added WiX 3.14's dedicated managed-host element with the project MIT license URL. |
| Review found repair language switching, layout/cache normalization, unattended override, close-code, and artifact-verification gaps | Two-axis review | Added regression contracts and tightened each runtime/build boundary. |
| StrictMode rejected `.Count` on a null `Compare-Object` result | First artifact verification run | Wrapped comparison results in `@(...)` so an equal set has a deterministic count of zero. |
| Burn planning failed with `0x8007000d` | First real Install click | Log showed single-quoted string literals are invalid in Burn conditions; use XML-escaped double quotes. |
| Completed quiet layout left Burn processes running and locked the EXE | Layout smoke test | Route all unattended close paths through the WinForms UI thread. |
| `check-complete.ps1 -Gate` reported 0/0 phases | Final planning check | The helper did not resolve the isolated `.active_plan`; verified the active plan file directly instead. |
| PowerShell malformed a combined quoted `rg` expression | Final line-reference lookup | Replaced it with separate fixed-string searches. |
