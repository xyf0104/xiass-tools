# macOS 1.5.0 Release Packaging

Goal: validate the two macOS 1.5.0 updater artifacts on the desktop, produce the complete release artifact set, and update Desktop/latest.json for the split macOS architectures.

## Phase 1: Inspect inputs and release contract
Status: complete

- Identify both macOS artifacts and signatures.
- Confirm current version, naming rules, and manifest platform mapping.

## Phase 2: Package and generate manifest
Status: complete

- Normalize or copy deliverables without altering valid signed bytes.
- Generate the updated latest.json using repository tooling.

## Phase 3: Verify outputs
Status: complete

- Validate signatures, hashes, filenames, URLs, and JSON platform entries.
- Report exact Desktop outputs and any remaining upload step.

## Errors Encountered

| Error | Attempt | Resolution |
|---|---|---|
| `npx tauri signer verify` is unsupported | Signature CLI inspection | Inspect available signer commands and rely on supported manifest/publish validation; do not assume a verify subcommand. |
| First planning-file patch targeted the error table in the wrong file | Progress update | Corrected the patch to update `task_plan.md` for errors and the other files for findings/progress. |
| `verify-burn.ps1` was invoked without required paths | Standalone Burn verification | Resolve the generated Bundle path and WiX tools path from `build-burn.ps1`, then rerun with explicit parameters. |
| `cargo test app_updater --lib` was run from the repository root | Rust updater test | Re-run from `src-tauri`, where `Cargo.toml` is located. |
