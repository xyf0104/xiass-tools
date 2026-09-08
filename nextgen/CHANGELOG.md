# Changelog

## [1.8.0] - 2026-09-08

### Release

- Continued the complete integrated Antigravity WF workspace with runtime overview, models and image generation, upstream routing, official accounts, multi-instance launch, wakeup tasks, account verification, and terminal permissions.
- Continued the Codex API Key configuration flow with an isolated provider, model catalog, Web Search, and `gpt-image-2` image-generation capability handling plus verified recovery points.
- Unified macOS Universal and Windows x64 installer publishing with application startup, WF bridge, and uninstall lifecycle validation.
- Continued the XIASS dark liquid-glass theme, transparent brand mark, and high-contrast interactive controls.

## [1.7.6] - 2026-09-07

### Fixed

- Restored the complete integrated Antigravity WF workspace entry in XIASS Tools, bringing runtime overview, models and image generation, upstream routing, official accounts, instances, wakeup tasks, account verification, and terminal permissions back into one module.
- Preserved the v1.7.5 direct Cockpit page hierarchy for Codex, Claude, Cursor, and Windsurf instead of wrapping the other Agents in the retired workspace shell again.
- Added a release regression gate requiring exactly one Antigravity WF workspace with all four account-oriented panels, preventing a future release from silently falling back to the standalone account page.

## [1.7.5] - 2026-09-01

### Fixed

- Isolated the local CLI proxy runtime authentication state so a fresh account session cannot inherit another runtime's auth directory or model registration.
- Delayed runtime credential refresh until manifest-backed accounts are fully registered, and made OAuth/token auth-file updates atomic so a concurrent request never reads a partial JSON document.
- Wait for every background credential-refresh worker to stop during runtime shutdown, preventing Windows from deleting or replacing an auth directory while a final token-file write is still in flight.
- Fixed the Windows installer lifecycle verifier on GitHub hosted runners by releasing Windows Installer COM objects without calling an unsupported `Close()` method. MSI and Setup validation still checks the embedded offline WebView2 payload before exercising clean install, app startup, bridge startup and uninstall.
- Fixed explicit quoting for Windows MSI and installer-log paths during lifecycle validation so a product filename containing spaces cannot leave `msiexec` waiting without a UI.
- Restored the transparent XIASS brand mark in the Antigravity account empty state and fixed light-theme control, hover and selected-tab contrast without changing Cockpit layout or account workflows.
- Restored the exact transparent XIASS Tools logo asset used by the original v1.7.0 workspace across the shell and both embedded WF frontends; kept the matching readable light mark, removed the synthetic logo tile, and strengthened light-material secondary text contrast.
- Made every static Tauri release resource self-contained, including license notices, the Claude helper and native-menu icons, so both Windows installer formats ship them reliably; release checks now reject external resource paths and verify every bundled copy against its canonical source.
- Restored the original Cockpit account, OAuth, 2FA, quota, provider-template, model-discovery, and model-testing entry points in all five workspaces. XIASS changes now remain limited to branding and visual material rather than replacing original functions or layout.
- Unified the embedded macOS and Windows WF frontend, public assets, bridge contract, and version projections, with byte-for-byte cross-platform parity enforced during release preflight.

## [1.7.4] - 2026-09-01

### Fixed

- Kept the offline Windows WebView2 runtime while fixing WiX 3.14 MSI linking on GitHub hosted runners missing the legacy ICE scripting engine. The build now verifies the official WiX SHA-256 and still performs extraction, installation, app-startup, and local bridge smoke validation.

## [1.7.3] - 2026-09-01

### Fixed

- Pinned Windows package builds to the stable Windows Server 2022 runner after the current Windows Server 2025 image repeatedly failed while linking the MSI with WiX 3.14.

## [1.7.2] - 2026-09-01

### Added

- Added resilient TOTP QR migration for standard and Google Authenticator migration payloads on macOS and Windows, including staged multi-image import and deduplication.
- Added a recoverable Antigravity OAuth flow with loopback callback, PKCE, restart recovery and manual callback URL fallback.

### Changed

- Kept the original Cockpit sidebar, account header, platform switcher, tab strip and full-page scroll hierarchy as the common shell for every Agent; XIASS capabilities are integrated within the corresponding Agent page.
- Synchronized the embedded macOS and Windows assistant version sources with the desktop release version.

### Fixed

- Packaged the macOS app and both local sidecars as Universal binaries and added a DMG installation/startup smoke check.
- Kept the embedded workspace first frame aligned with the selected theme and preserved a single main XIASS scroll surface.

## [1.7.1] - 2026-08-31

### Added

- Added one unified Chinese-first workspace for each of the five primary Agents: Antigravity WF, Codex, Claude Code, Cursor and Windsurf.
- Added a complete in-app manual for the Antigravity proxy, model, image, account, patch, diagnostics and launch workflow.
- Added release tests for UI accessibility, configuration persistence, Tauri command wiring, explicit exit behavior and real frontend readiness.

### Changed

- Redesigned the shell and embedded workspaces with the XIASS deep-sea background, transparent brand mark, rounded liquid-glass surfaces and consistent light, dark and system themes.
- Standardized buttons, tabs, dialogs and form actions on a 44 px interaction target with explicit hover, focus, active, disabled and loading states.
- Improved responsive wrapping, long Chinese labels, URL fields, modal layering and Codex session controls at narrow workspace widths and 125% UI scale.
- Updated the macOS lifecycle so closing the window keeps the service available in the background while explicit Quit closes the bridge and releases its ports.

### Fixed

- Prevented provider and account configuration reads from silently replacing existing data after malformed or failed storage responses.
- Fixed the WF bridge shutdown path so stdin closure stops the proxy and releases local listeners.
- Fixed macOS CI/updater startup configuration and added a React commit readiness marker so an alive process without a mounted interface cannot pass release smoke tests.
- Isolated Rust tests from shared environment-variable state so the full workspace test suite passes with the default parallel runner.
- Fixed dashboard title contrast, remote MCP field clipping, small or unnamed controls, modal accessibility and first-frame theme flashes.

## [1.7.0] - 2026-08-31

### Added

- Unified local account management for Antigravity, Codex, Claude Code, Cursor, Windsurf and other supported clients.
- OAuth authorization flows with automatic callback handling and manual callback fallback for supported providers.
- Secure local account storage, quota display, account testing, import/export and instance management.

### Changed

- Rebranded the desktop application, data directories and public service defaults as XIASS Tools.
- Preserved read-only compatibility with account data, shortcuts, extension storage and protocol identifiers from earlier supported releases.

### Fixed

- Removed upstream advertisement, announcement and private remote-configuration requests.
- Improved cross-platform installer migration and OAuth launch progress reporting.
