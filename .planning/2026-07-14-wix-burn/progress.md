# Progress

- Restored the existing Windows installer changes and verified the three localized MSI outputs.
- Reflected the bundled WiX 3.14 `BootstrapperCore.dll` to confirm the managed BA API surface.
- Added red static installer contracts and implemented the first managed BA, bundle source, and build script.
- The first real build exposed an argument-splitting bug in `--no-restore:$false`; reduced it to the build script invocation and removed that argument.
- Built the 55 MB Burn EXE and verified its manifest contains the stable Bundle UpgradeCode, three embedded localized MSI packages, and managed BA payloads.
- Opened the final EXE and visually confirmed the Simplified Chinese, Traditional Chinese, and English selector without starting installation.
- Addressed review findings for Repair, layout/cache, unattended language overrides, cancellation exit status, runtime target compatibility, and reproducible artifact inspection.
- Final checks: 167 unit tests passed, Svelte check reported zero errors/warnings, Burn build/verification passed, and `git diff --check` passed.
- Resumed Phase 4 to remove the now-unnecessary MST layer and generate a single en-US MSI while preserving the multilingual WPF bootstrapper.
- Added red installer contracts for one Tauri MSI and no transform authoring; four assertions failed on the old three-language/MST implementation as expected.
- Simplified Tauri, Burn authoring, managed BA, build script, and verifier to use one en-US MSI while retaining `SelectedLanguage` for the WPF UI. The nine targeted installer tests now pass.
- Rebuilt the Burn bundle from the existing MSI: actual manifest verification and plan-only install planning passed; the setup EXE is 19,501,395 bytes.
- Ran the full `tauri:build:windows` entry point. Tauri reported exactly one en-US MSI plus updater signature, then Burn rebuilt and passed verification.
- Added current-version stale-localization cleanup so old multi-MSI build artifacts do not remain beside the single en-US output; historical versions are untouched.
- Final verification: `npm test` passed 185 tests with zero Svelte errors, the current-version MSI directory contains only en-US MSI plus signature, and `git diff --check` passed.
- Two-axis review found no blocking standards or spec issues. Existing BA responsibility breadth and raw language strings remain non-blocking architecture debt outside this simplification.
