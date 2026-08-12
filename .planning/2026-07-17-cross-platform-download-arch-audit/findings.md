# Findings

- Existing ChatGPT macOS fix selects native arm64 on Apple Silicon even when CodeStudio Lite runs under Rosetta, and x64 on genuine Intel Macs.
- Initial architecture scan found explicit selection in `chatgpt_desktop.rs` and `app_updater.rs`; Claude Desktop Windows install/detection is intentionally or accidentally hard-coded to x64 and needs release-capability confirmation.
- Most CLI tool install/update paths are package-manager commands (npm, pip, cargo, Homebrew, winget), so architecture resolution is delegated to the package manager rather than selected by CodeStudio Lite.
- Confirmed defect: `app_updater::application_update_target` and macOS installer filename validation use the compiled process architecture. An x64 CodeStudio build under Rosetta therefore selects and enforces the x64 CodeStudio DMG instead of the native arm64 DMG.
- Red-capable test command: `cargo test macos_update_target_prefers_native_apple_silicon_over_rosetta_process_architecture`; it failed before implementation with missing `application_update_target_for_runtime` references.
- Confirmed defect: Claude Desktop Windows metadata, MSIX redirect, install command, package discovery, and stale package scans are hard-coded to x64 even though the official `win32/arm64/.latest` and `win32/arm64/msix/latest/redirect` endpoints both returned HTTP 200 on 2026-07-17.
- Claude Desktop macOS uses the official `darwin/universal` release, so it is architecture-independent.
- Confirmed defect: ChatGPT Windows mirror schema v5 publishes both `architectures.x64` and `architectures.arm64`, but the client read only the top-level x64 compatibility entry and always downloaded `/latest/win`. The mirror exposes the ARM64 package at `/latest/win-arm64`.
- Windows native architecture is now resolved with `IsWow64Process2`, with environment/process-architecture fallback; this distinguishes an x64 CodeStudio process emulated on Windows ARM64.
- Audit classification: affected and fixed paths are ChatGPT Desktop on macOS and Windows, Claude Desktop on Windows, and CodeStudio Lite self-update on macOS. Claude Desktop macOS is universal. npm, VS Code, winget, official shell-script, and Node macOS pkg routes delegate architecture selection upstream. CodeStudio Lite Windows currently publishes only x64, so its x64 self-update target is intentional rather than a selector bug.
