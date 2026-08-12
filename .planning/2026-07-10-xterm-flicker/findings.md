# Findings

## API Login GPT-5.6 Entry Follow-up

- The requested behavior is a new independent launch option: API-key login should receive a GPT-5.6 entry that looks and behaves like the official Auth model entry; ChatGPT Auth already supports it and must remain unchanged.
- The existing `modelWhitelistUnlockOnLaunch` option is unsuitable by itself. It loads models from environment/config `/v1/models` catalogs and constructs entries with the provider name or `Custom model`, matching the user's reported custom-model presentation.
- The new setting must flow through the existing ChatGPT Desktop-only settings/request/injection protocol and must not alter Codex CLI model configuration or `config.toml` semantics.
- The locally downloaded Codex++ source contains GPT-5.6 catalog tests but no separate official-entry toggle in the inspected settings surface, so its generic model-whitelist behavior is evidence for what not to reuse directly.
- Memory confirms the relevant prior boundary: renderer injection is transported over CDP and Codex++ model/plugin behavior is login-state and version gated; current source still needs to be rechecked before implementation.
- The injected settings currently contain plugin, auto-expand, generic model-whitelist, Fast-tier, and model-catalog fields only; there is no auth-mode signal available inside the renderer.
- Launch-option persistence is a direct parallel path across `ChatGPTDesktop.svelte`, `chatgptDesktopStore.ts`, TypeScript settings/update types, Rust settings/update structs/defaults, and `CodexEnhancementInjectionSettings` serialization.
- The current app-server/JSON/React patch layers already see official model descriptor arrays. A separate GPT-5.6 patch can reuse those interception points without enabling the generic config catalog.
- The safest official presentation strategy is to clone an existing visible GPT-5.x official descriptor and override only model identity/display fields, rather than constructing the generic descriptor whose description is `Custom model`.

## macOS Mirror TLS Follow-up

- The reported failure is `curl: (56)` from macOS LibreSSL 3.3.6 while reading `codexapp.agentsmirror.com`, with `sslv3 alert bad record mac`. This is a transport read failure, not an HTTP status or SHA-256 mismatch.
- The safe recovery boundary must keep HTTPS certificate validation and final SHA-256 verification intact; disabling TLS checks is excluded.
- The tracked frontend mock exposes mirror package/checksum/manifest URLs, but the real renamed ChatGPT Desktop backend is currently untracked and therefore absent from `git grep` results. Direct file inspection is required.
- Existing planning context confirms macOS ChatGPT Desktop packages are downloaded, cached, and SHA-256 verified before installation.
- The exact `Failed to read <host>` prefix comes from `fetch_text`, so the reported failure occurred while reading the mirror manifest or checksums before the DMG transfer began.
- `fetch_text` currently uses `curl -fsSL --connect-timeout 20 --retry 2`; curl error 56 is not reliably retried without `--retry-all-errors`, and the command has no macOS HTTP/1.1 fallback.
- `download_to_file` uses the same two-retry policy for large packages, writes to a unique temporary path, but does not request resume with `--continue-at -`; after final failure it deletes the partial file.
- The chosen recovery policy is bounded retry-all-errors for both metadata and packages, HTTP/1.1 on macOS, and within-command resume for package downloads. Existing temporary-file replacement and SHA-256 verification remain the final correctness boundary.
- ChatGPT Desktop tests are compiled as a child module on Windows and can access private helpers, so a pure curl-argument helper can verify the macOS branch without a macOS runner.
- The observed metadata path can also avoid LibreSSL entirely by using the repository's Rustls-backed `reqwest` dependency; existing HTTP helper patterns must be inspected before choosing between direct replacement and curl fallback.
- The implemented transport now uses the crate's Rustls-backed reqwest client for both metadata and packages. macOS calls `http1_only()`, so the failing LibreSSL path and HTTP/2 negotiation are both removed.
- Retry policy is limited to four total attempts for connection/read errors, HTTP 408/429, and 5xx responses. Other HTTP failures return immediately.
- Package retries send `Range: bytes=<partial-size>-`; a 206 response appends, a full 200 response truncates and restarts safely, and a matching 416 treats an already complete temporary file as complete.
- Local HTTP regression tests prove both recovery paths: truncated metadata is fetched successfully on the second request, and a truncated package resumes from byte 5 to produce the exact 10-byte payload.
- All 22 ChatGPT Desktop Rust tests pass after the transport migration.
- Full verification now passes: all 309 Rust library tests, Cargo check, all 143 frontend unit tests, and Svelte checking complete successfully.
- Final transport audit confirms `fetch_text` and `download_to_file` contain no system-curl launch, macOS forces HTTP/1.1, package transfers use Range resume, and the SHA-256 failure gate remains unchanged.
- `cargo fmt --check` and `git diff --check` pass; the latter emits only the repository's existing Windows line-ending notices.
- Live reproduction of the original LibreSSL error is not possible on the current Windows host, but the affected macOS code path no longer links the operation to system curl/LibreSSL and is covered by interrupted-response integration tests.

## Version 1.3.0 Follow-up

- CodeStudio Lite's current 1.2.3 version is owned by six metadata locations: `package.json`, two root-package fields in `package-lock.json`, `src-tauri/tauri.conf.json`, `src-tauri/Cargo.toml`, and the `codestudio-lite` entry in `src-tauri/Cargo.lock`.
- The 1.2.3 values belonging to `queue-microtask`, `update-browserslist-db`, and the macOS plist parser test are unrelated third-party/fixture data and must remain unchanged.
- Frontend runtime display uses `packageJson.version` in `vite.config.ts` to define `__APP_VERSION__`, so updating the npm package metadata is the canonical frontend version change.
- Session catch-up found the completed bundle follow-up plus the current version inventory; it did not reveal hidden product-code edits.
- Post-edit residual search finds 1.3.0 in all six application-owned metadata fields. Other 1.3.0 matches are pre-existing third-party dependency versions.
- The remaining 1.2.3 matches are limited to `@nodelib/fs.walk`'s range, `queue-microtask`, `update-browserslist-db`, and the generic plist parser fixture.
- `npm pkg get` and `cargo metadata --no-deps` both resolve the local project as `codestudio-lite 1.3.0`.
- The focused frontend version-injection test passes, `svelte-check` reports zero errors/warnings, and the production build runs under `codestudio-lite@1.3.0` without bundle warnings.
- The version-only change preserves the previous chunk layout; the largest generated JavaScript chunk remains about 424.20 kB.
- `cargo check` recognizes and successfully compiles the local package as `codestudio-lite v1.3.0`.
- The complete frontend unit suite passes 143/143 and `git diff --check` reports no whitespace errors.
- A consolidated six-source check confirms `package.json`, both root `package-lock.json` fields, Tauri config, Cargo manifest, and the local Cargo lock entry all equal 1.3.0; the built entry chunk contains the injected 1.3.0 value.
- The restarted production preview returns HTTP 200 for the page and entry chunk, and the served entry contains the injected 1.3.0 version.

## Bundle Warning Follow-up

- The current production build emits a roughly 1,000.05 kB minified entry chunk and triggers Vite's 1000 kB warning.
- The requested outcome is a real bundle split; increasing `chunkSizeWarningLimit` would only hide the symptom and is excluded.
- The existing xterm, desktop migration, launch, macOS, and dynamic-branding worktree changes are intentional and must be preserved.
- `vite.config.ts` already raises `chunkSizeWarningLimit` from Vite's default to 1000 and documents a deliberate single-bundle policy; the observed 1,000.05 kB output has now exceeded even that raised threshold.
- Runtime dependencies are limited to Svelte, Iconify, Tauri APIs/plugins, xterm plus FitAddon, and `svelte-dnd-action`, so stable dependency-family chunks can be evaluated without introducing route-level behavior changes.
- Session catch-up found only one unsynced tool event; the plan and current worktree already contain the substantive prior implementation state.
- Rollup's module report attributes about 1,075,797 rendered bytes to application modules, 292,035 to `@xterm/xterm`, 242,970 to Svelte, 101,242 to `svelte-dnd-action`, and about 35 kB to Iconify.
- The largest application contributors are `Profiles.svelte` (176,290 rendered bytes), `api.ts` (145,057), `Dashboard.svelte` (142,334), `SetupWizard.svelte` (78,266), and the three locale catalogs (about 147 kB combined).
- The existing output has one 1,000,050-byte entry JavaScript file plus only tiny Tauri dynamic-import chunks; xterm and drag/drop are statically imported by dashboard/profile/terminal views and therefore remain in the entry without explicit grouping.
- A dependency-family-only simulation produced a 588,438-byte entry and therefore would still exceed Vite's default 500 kB warning threshold.
- Splitting the static locale catalogs as well reduced the entry to about 424 kB; a single vendor chunk was about 412 kB and the locales chunk about 164 kB, all below the default threshold.
- Separating Svelte and Iconify into different manual chunks created a mutual chunk import because Iconify's Svelte component depends on Svelte while Rollup also placed an Iconify-linked helper in the Svelte chunk. These libraries should remain together in one general vendor chunk.
- The conservative target boundary is therefore `terminal` for both xterm packages, `vendor` for all remaining `node_modules`, and `locales` for the three static translation catalogs. This keeps each measured chunk below 500 kB without changing route lifecycle semantics.
- The real production build completed without chunk-size or circular-dependency warnings. Its JavaScript output is: entry 424.20 kB (112.86 kB gzip), terminal 292.19 kB (72.86 kB gzip), locales 164.26 kB (41.62 kB gzip), and vendor 119.10 kB (42.70 kB gzip).
- Xterm CSS was also extracted into a 4.15 kB terminal stylesheet while the main stylesheet is 90.08 kB; this is a normal consequence of the terminal manual chunk.
- Post-change `svelte-check` reports zero errors and zero warnings, and all 143 frontend unit tests pass.
- `git diff --check` reports no whitespace errors; its output contains only the pre-existing Windows LF-to-CRLF conversion notices.
- A recursive production asset check reached `index.html` and all four JavaScript chunks with no missing references.
- The Vite production preview at `http://127.0.0.1:4174/` returned HTTP 200 for the document, entry, vendor, locales, terminal, both stylesheets, and the icon.
- Final scoped diff inspection shows only the intended `vite.config.ts` replacement of the raised threshold with the manual chunk function; `dist` remains ignored.

## Initial State

- The worktree already contains uncommitted changes in `src/routes/TerminalPanel.svelte`, `src/lib/terminalSessionStore.ts`, `src/lib/toolInstallConsole.test.mjs`, and `src-tauri/Cargo.toml`.
- The user reports that the attempted fix still leaves the embedded terminal continuously flickering, stalled, and unable to accept text input.
- Existing root planning files belong to the earlier Panda CSS migration and are intentionally left unchanged.

## Current Regression Shape

- The attempted fix added a `ResizeObserver`, animation-frame fitting, a 180 ms backend settle timer, an 80 ms store debounce, and direct reads from xterm private render dimensions.
- `doFit()` calls `term.resize()` whenever its manually calculated grid changes. A resize can alter xterm viewport/scrollbar geometry, which can notify the same container observer again.
- If the available width crosses a cell boundary as the viewport changes, columns can oscillate and repeatedly send PTY resize events. Full-screen terminal programs react by repainting, matching the reported flicker and apparent input loss.
- The existing static test only asserts that the new scheduling code exists; it does not prove that a resize cycle converges.
- The terminal frame has an 8 px padded, overflow-hidden content box and forces `.xterm` to `height: 100%`. Grid calculation must therefore use the exact rendered xterm cell metrics and the frame content box; approximate constants are unsafe near a row/column boundary.
- Backend `resize_install_terminal` directly calls `MasterPty::resize`. Every distinct frontend grid size therefore becomes a real console resize event and can make interactive/full-screen programs redraw.
- The checked-in dependency set contains `@xterm/xterm` 5.5 but not the official `@xterm/addon-fit` package.
- Local xterm 5.5 source confirms render cell dimensions live under `renderService.dimensions.css.cell`; the attempted `_renderService._dimensions.cellWidth` path is invalid and always falls back to guessed constants.
- The user explicitly agreed to introducing the official FitAddon. The implementation will use `FitAddon.fit()` and xterm's public `onResize` event instead of private renderer fields.
- The implemented resize path now has one source of truth: FitAddon applies the grid, xterm emits `onResize` only when the grid changes, and the store debounces/deduplicates the resulting backend request.
- Initial focus now happens after the first successful fit rather than before layout settles.
- The dependency tree resolves `@xterm/addon-fit` 0.10.0 with `@xterm/xterm` 5.5.0, matching the addon's declared `@xterm/xterm ^5.0.0` peer range.
- `npm install` changed the lockfile only by adding the FitAddon root dependency and package record; the reported node_modules removals produced no unrelated tracked lockfile churn.
- Windows reserves TCP ports 1341-1440 on this machine, explaining the Vite `EACCES` on both 1420 and 1421. Port 5173 is available and was used for browser verification.
- The browser mock flow reached the dedicated Codex CLI terminal route. Xterm mounted successfully, displayed the mock session output, and exposed its `Terminal input` textbox as the active element.
- Browser geometry sampling exposed the decisive layout bug: the terminal frame grew to roughly 29,747 px high. `TerminalPanel` used `height: 100%`, but the route wrapper had no definite height and the terminal grid used an intrinsic `1fr` track, allowing xterm screen height to enlarge its own measurement container.
- FitAddon then faithfully fitted the enlarged container, causing a positive feedback loop. The route wrapper needs a definite height and the terminal content track must be `minmax(0, 1fr)` so xterm cannot contribute an expanding minimum size.
- The route wrapper now fills the workspace content box with a definite height, while the terminal grid's content row is shrinkable and overflow-contained. This breaks the content-size feedback loop before FitAddon observes the frame.
- Browser verification after the constraint fix measured the terminal frame at 1280x368 and the xterm screen/viewport at 368 px high in two samples one second apart; every value was identical.
- The xterm helper textarea stayed focused before and after the interval, accepted `fit-addon-input-ok`, and the frame remained 368 px high after input.
- Browser console inspection reported no warnings or errors during the embedded-terminal flow.

## Tooling Notes

- `rg.exe` failed to launch from the WinGet link in this PowerShell environment. Use `Select-String` or `git grep` for subsequent searches.
- The former local `D:\迅雷下载\CodexPlusPlus-main` checkout is no longer present. The repository URL must be recovered from the archived research session, then a fresh temporary checkout can be used for source comparison.
- The archived research session contains the repository URL `https://github.com/BigPizzaV3/CodexPlusPlus`.
- Historical source evidence separates two plugin paths: version-gated marketplace/list patching and the independent `forcePluginInstall` layer. Both are renderer-injection features; they do not require registering a bundled snapshot as the official `openai-curated-remote` catalog.
- Fresh Codex++ commit `4523d63801398b038de83f60bdbfdef63477088c` (2026-07-10) explicitly tests `injection_script_expands_api_key_plugin_marketplace_requests`.
- Codex++ still registers a local `openai-curated-remote` marketplace. Its injected renderer merges local snapshot entries into `list-plugins`, expands marketplace kinds, bypasses official marketplace/build-flavor hiding filters, and rewrites install requests.
- Direct ZIP comparison corrected an earlier source-search inference: CodeStudio and current Codex++ embed the exact same archive (`SHA-256 7211EA4B6D6921ED45FC7FEC557906C0633F4F46359CE76D11961F1F3E88A666`), including all `remotePluginId` values and ten `.codex-remote-plugin-install.json` markers.
- Because Codex++ works with that identical snapshot under API Key auth, the remote bundle sync warning is not sufficient evidence of the fatal cause. The remaining comparison must focus on process reuse, launch arguments/environment, helper/bridge readiness, and injection ordering.
- Both projects use Windows `IApplicationActivationManager::ActivateApplication` for packaged installs and pass the same two base arguments: `--remote-debugging-port=<port>` and `--remote-allow-origins=http://127.0.0.1:<port>`.
- CodeStudio terminates the prior desktop process, prepares the plugin cache, activates the package, then starts a background `Runtime.evaluate` injection retry. Codex++ additionally owns a long-lived launcher/helper runtime, single-instance recovery, current-and-future-page bridge injection, and a reinjection watchdog.
- Since the packaged launch primitive and base arguments match, the reported remote plugin warning must be correlated with nearby local log entries before treating it as fatal.
- Live process inspection shows the Electron desktop (`ChatGPT.exe`) remains running with multiple subprocesses, while its bundled `resources\codex.exe` is a separate child. The UI's `(code=4294967295, signal=null)` therefore plausibly refers to the internal Codex app-server child, not the desktop application activation result.
- Current local timestamps place the reported 17:56 CST warning shortly before the presently running desktop/app-server group started at about 17:58. The structured `~/.codex/logs_2.sqlite` store is the next source of truth for the fatal child timeline.
- Structured log query found 19 identical `remote_installed_plugin_sync` warnings between 09:50 and 10:02 UTC across multiple app-server processes.
- The warning appears three times for process `pid:22504:8a0d9931-6c20-4265-ab4e-0f2d17e160bc` at 09:56:13-15 UTC. A new process `pid:45204:99175a86-fb87-4c2d-aca9-5a971687fa95` starts at 09:56:53, logs the same warning, and continues serving plugin/MCP requests. This proves the warning is recoverable and is not itself the reason for the `-1` child exit.
- The follow-up process also reports expected API-key limitations such as a featured-plugin HTTP 401, again as warnings while remaining operational.
- Process `pid:22504` has no ERROR rows in its entire 09:55:56-09:56:51 UTC lifetime. It continues handling config and account requests for 36 seconds after the user's quoted 09:56:15 warning.
- The old app-server's final log is at 09:56:51 and the replacement process begins at 09:56:53. This timing strongly indicates a deliberate restart/termination boundary. If CodeStudio kills the `codex.exe` app-server before its parent `ChatGPT.exe`, the still-visible desktop can report the forced `-1` child exit and attach the last unrelated stderr warning.
- The correct fix should make desktop shutdown parent-first/graceful and avoid treating the remote plugin warning as a capability failure. API Key remote-plugin cache registration must remain enabled.
- Confirmed Windows root cause: `terminate_codex_process_for_restart` enumerates only `Get-Process -Name Codex`. In the installed MSIX, the Electron main process is `app\ChatGPT.exe`, while the matched `app\resources\codex.exe` is the app-server child.
- The current flow therefore kills the app-server, leaves the visible Electron parent alive, and then calls package activation. Windows reuses the surviving parent without the new CDP arguments, producing both the visible forced child exit (`0xffffffff`) and a failed/ineffective enhancement injection.
- Codex++ process discovery intentionally accepts supported package main executables under `WindowsApps\...\app\` and explicitly excludes `app\resources\...`; this is the behavior CodeStudio must reproduce. Portable installs still need their main `Codex.exe` process handled within the configured install root.
- The repository already has `process_control::close_appx_package_for_update`, which first asks visible package windows to close and waits before force-closing any remaining package processes. Reusing it fixes ordering without introducing a second Windows process enumerator.
- CodeStudio's existing injection removed `marketplaceKinds` and only renamed returned marketplaces. Current Codex++ instead requests `local` plus `vertical` kinds and merges its serialized local marketplace snapshot into `list-plugins`; this difference was ported to make API Key plugin availability deterministic.

## ChatGPT Desktop Rename Boundary

- The user requires a full technical-domain rename, not only copy changes: frontend identifiers and backend protocol must move from Codex Client/Desktop naming to ChatGPT Desktop naming.
- The rename must not propagate into Codex CLI. Keep CLI tool IDs, launch flows, `Codex CLI` labels, `~/.codex`, CLI profile/config terminology, and Codex++ enhancement features unchanged.
- Likely desktop-domain surfaces include the `codexClient` route/store/API/types, `CodexClient.svelte`, Tauri `codex_client` commands/module/types/events/cache keys, desktop-install detection fields, and user-facing documentation/locales.
- The desktop tool currently has the dedicated ID `codex-app`; this should become `chatgpt-desktop`. Historical aliases such as `codex-app`, `codex-client`, and `codex-desktop` may remain only where compatibility/migration logic intentionally accepts old persisted values.
- Frontend target naming: route `chatgptDesktop`, component `ChatGPTDesktop.svelte`, store `chatgptDesktopStore.ts`, `ChatGPTDesktop*` TypeScript types/functions, locale namespace `chatgptDesktop.*`, refresh key `chatgptDesktop`, and detection field `chatgptDesktopInstallKinds`.
- Backend target naming: module/commands `chatgpt_desktop`, Rust types `ChatGptDesktop*`, commands such as `inspect_chatgpt_desktop`, event `chatgpt-desktop://progress`, staging path `.codestudio-chatgpt-desktop-staging`, and storage table `chatgpt_desktop_state` with migration from the old table.
- CLI exclusions include the `codex` / `codex-cli` tool IDs, Codex CLI launch functions, `.codex` user-data paths, profile provider/protocol names, CLI model/plugin enhancement terminology, and the upstream macOS download URL path `codex-app-prod`.
- External integration identifiers are also compatibility boundaries, not internal domain names: installed package family `OpenAI.Codex_*`, executable/app bundle names, process names, upstream `Codex.dmg` URLs, and `.codex` user data must remain as required by the actual upstream artifacts.
- The frontend API surface is cleanly separable: all desktop operations use `CodexClient*` types/functions and `codex-client://progress`; these can move to `ChatGPTDesktop*`, `chatgpt_desktop` commands, and `chatgpt-desktop://progress` without touching Codex OAuth/CLI types located alongside them.
- Detection snapshot currently serializes `codexInstallKinds`; the migrated field should be `chatgptDesktopInstallKinds`, while `codexAuth` remains unchanged because it represents Codex CLI authentication.
- Rust desktop settings and managed-marker state currently use `codex_client.settings` and `codex_client.managed_marker`; new keys should use `chatgpt_desktop.*` while reads fall back to the old keys and persist forward, preserving existing installations.
- SQLite currently creates and reads `codex_client_state`. The migration should create `chatgpt_desktop_state`, copy/rename compatible rows from the legacy table, and keep only migration-time references to the old table name.
- Rust types in the desktop module are entirely domain-owned (`CodexClientSettings`, state, release, plan, progress, requests, operation result). They can be mechanically renamed to `ChatGptDesktop*`; adjacent shared `ConfigState`, install-kind info, and Codex plugin/provider modules remain unchanged.
- The Rust module mixes internal domain names with external artifact constants. Rename internal constants such as settings/marker/event/cache identifiers, but retain `PACKAGE_IDENTITY = OpenAI.Codex`, `Codex.exe`, `Codex.app`, bundle ID, uninstall registry key, shortcut name, and upstream download URLs.
- The desktop `ToolStatus` should switch from ID/name `codex-app` / `Codex` to `chatgpt-desktop` / `ChatGPT Desktop`, while its command/path detection continues to target the external Codex executable/package.
- App routing is isolated enough to rename `codexClient` to `chatgptDesktop` and `CodexClient` component/store imports to `ChatGPTDesktop`; Dashboard navigation and managed-desktop dispatch must follow the new tool ID.
- Profile-apply restart enum variant `RestartLaunch::CodexClient` represents the desktop app and should become `ChatGptDesktop`, while profile provider/protocol normalization to the Codex gateway scope remains a CLI/model compatibility concern.
- Claude Desktop currently reuses several `codexClient.*` locale keys and the `CodexClientCapability` shape. These are genuinely shared desktop-client concepts and should move to generic identifiers (`desktopClient.*`, `DesktopClientCapability`) instead of being renamed to ChatGPT-specific identifiers that Claude would consume.
- Existing profile/gateway canonicalization deliberately maps desktop aliases to the Codex model/config scope. Add `chatgpt-desktop` as the new accepted alias but retain historical `codex-app` / `codex-client` / `codex-desktop` aliases; these compatibility branches are allowed residual old names.
- The current active-profile fallback from `codex` to `codex-app` is legacy compatibility. Prefer `chatgpt-desktop`, then fall back to `codex-app` while migrating persisted maps through existing canonicalization cleanup.
- The shared capability DTO should move out of Codex naming to `DesktopClientCapability` in both TypeScript and Rust-facing APIs because Claude Desktop consumes the same shape.
- Existing generic locale namespace `desktopClient.*` can absorb shared plan/status/install labels currently borrowed from `codexClient.*`; ChatGPT-only labels will use `chatgptDesktop.*`.
- File/module rename set is complete: frontend component/store/tests, Rust command/core/test modules, and the dedicated `codex-app.png` asset all have ChatGPT Desktop target names.
- After mechanical migration, both frontend and Rust compile. Remaining work is semantic compatibility and copy cleanup rather than unresolved symbol wiring.
- Allowed residual `codex-app` text currently consists of upstream `codex-app-prod` download URLs and legacy request/header aliases. The duplicate alias warnings identify exactly where old aliases must be restored beside the new canonical ID.
- The mechanically renamed settings keys and SQLite table currently lack fallback migration; these must be added before the domain migration is safe for existing users.
- Storage schema version is 7 and initialization already calls a dedicated table ensure function, making it straightforward to copy legacy `codex_client_state` rows into `chatgpt_desktop_state` during every schema initialization without a destructive global migration.
- English ChatGPT locale values still contain standalone `Codex` product copy that the identifier pass could not distinguish from CLI terminology; these need explicit key-scoped edits. Simplified Chinese is largely correct after the phrase replacement, while traditional Chinese requires the same audit.
- Claude Desktop has 16 references to shared ChatGPT namespace keys; those exact references should move to generic `desktopClient.*` keys, leaving ChatGPT-specific install/progress/launch copy isolated.
- Resumption audit confirmed duplicate `chatgpt-desktop` entries in the frontend profile canonicalization arrays where legacy `codex-app` and `codex-client` aliases must be restored.
- Frontend active-profile lookup currently checks only the canonical `chatgpt-desktop` key for Codex-scoped profiles; it must then fall back to legacy `codex-app` (and equivalent cached aliases where maps are normalized).
- The browser mock tool already uses the canonical `chatgpt-desktop` ID but still exposes the user-visible name `Codex`; change only that desktop display name to `ChatGPT Desktop` while leaving `Codex.exe` and `OpenAI.Codex` artifacts intact.
- Renamed replacement files are currently untracked while their old names appear deleted. Final verification must include untracked files and interpret the pairs as intended renames.
- Storage schema version 8 now creates `chatgpt_desktop_state`, copies compatible legacy `codex_client_state` rows with `INSERT OR IGNORE`, and removes the legacy table; inline storage tests cover the install-kind keyed cache and migration path.
- Claude Desktop currently has 18 references to shared plan/status/install labels under `chatgptDesktop.*`. Add exact generic `desktopClient.*` counterparts and switch only Claude Desktop to those keys.
- English ChatGPT Desktop copy still contains desktop-product uses of `Codex` in the eyebrow, launch/ready/progress/settings/uninstall strings. Preserve the explicit `Codex CLI` and `~/.codex` wording in the user-data retention hint.
- `Codex*` names inside the injected enhancement script describe compatibility with the upstream app's internal dispatcher, DOM hooks, and service-tier implementation. They are not CodeStudio desktop protocol identifiers and should remain stable unless a distinct upstream API migration requires changing them.
- The Rust detection command is already `detect_chatgpt_desktop_install_kinds`, but the frontend API wrapper still exports `detectCodexInstallKinds` and invokes the removed `detect_codex_install_kinds` command. This is a concrete protocol mismatch that must be renamed on the frontend.
- Residual `codex-app`, `codex-client`, and `codex_client_state` occurrences are now concentrated in explicit gateway/profile/tool/usage aliases, legacy settings keys, legacy SQLite migration tests, and upstream `codex-app-prod` URLs. These are compatibility exceptions, not canonical identifiers.
- Post-implementation residual audit confirms obsolete desktop IDs remain only in compatibility aliases/migrations, tests asserting those paths, and upstream `codex-app-prod` URLs. No stale canonical command or event string remains.
- Two ordinary display fixtures still used `Codex`: the browser mock install-page/success copy and Rust detector test `ToolStatus.name` values. These should use `ChatGPT Desktop`; the adjacent `Codex.exe` command remains an upstream artifact identifier.
- The first complete Rust run exposed a migration race not visible in single-connection storage tests: concurrent schema initialization can both observe `codex_client_state`, after which one connection drops it before the other executes the copy. The second connection must treat `no such table: codex_client_state` as an already-completed migration and the drop must use `IF EXISTS`.
- Final residual audit found only approved legacy aliases/migration keys and upstream artifact URLs. Settings and managed-marker fallback now remove their legacy `codex_client.*` rows only after the new `chatgpt_desktop.*` value saves successfully, making the old keys true one-time migration inputs.

## ChatGPT Desktop API-Key Launch Exit

- Reported launch failure: `(code=4294967295, signal=null)` with the most recent app log warning `remote installed plugin bundle sync failed` because the remote plugin catalog requires ChatGPT authentication and does not support API-key auth.
- The warning is evidence of an unsupported optional remote-catalog path, but it is not yet proven to be the process exit cause; trace both the exit-code formatter and remote plugin cache/config path before changing behavior.
- `chatgpt_desktop::launch()` registers the official remote plugin cache before launching, then `launch_installed_codex()` uses `spawn()` and returns immediately. CodeStudio does not wait for or format the child exit code, so `4294967295` is emitted by the ChatGPT Desktop child/runtime rather than the Tauri launch command.
- `official_remote_plugin_cache_on_launch` defaults to `true`, and `ensure_official_remote_plugin_cache_if_enabled()` currently performs no Codex/ChatGPT auth-method check before adding the `openai-curated-remote` marketplace to `~/.codex/config.toml`.
- The reported warning is therefore consistent with a launch-time configuration incompatibility: API-key authentication reaches an official remote catalog path that explicitly requires ChatGPT authentication.
- Safe local inspection confirms the current `~/.codex/auth.json` is API-key-only: it has `OPENAI_API_KEY`, no `tokens`, and no ChatGPT auth mode. No secret values were read or printed.
- The current `~/.codex/config.toml` contains `[marketplaces.openai-curated-remote]` with a local source path written by CodeStudio. This reproduces the exact precondition for the reported remote bundle synchronization warning.
- A correct fix must remove or skip the CodeStudio-managed `openai-curated-remote` marketplace for API-key-only authentication; merely swallowing the warning would leave the incompatible sync path active on every launch.
- The embedded marketplace is not purely local despite local source paths: every catalog entry contains `remotePluginId`, and plugin directories include `.codex-remote-plugin-install.json`. ChatGPT Desktop therefore treats them as installed remote bundles and attempts authenticated remote synchronization.
- The safe compatibility rule is auth-aware configuration reconciliation: register `openai-curated-remote` only for explicitly detected ChatGPT OAuth; remove the CodeStudio-managed marketplace entry for API-key, unknown, absent auth, or when the feature toggle is disabled. Keep the cached files on disk so a later ChatGPT-authenticated launch can re-enable them without re-extraction.
- Existing auth detection is reusable through public `profile::codex_auth_status()`. It reports API-key-only files as `CodexAuthMethod::ApiKey`; keyring/auto credentials remain `Unknown`, which should also skip the remote cache because support cannot be verified safely.
- Current locale hints incorrectly promise that API mode can show/install Product Design. The copy must state that the bundled remote catalog is enabled only with ChatGPT account authentication.
- User correction: the equivalent Codex++ feature is known to work with API-key authentication. Therefore auth-gating/removing the remote marketplace is not an acceptable fix; the implementation must match Codex++'s cache/metadata/injection behavior closely enough to avoid the official remote sync failure while retaining API-mode plugin availability.

## macOS ChatGPT Desktop Parity

- The latest downloaded Codex++ source is commit `4523d63801398b038de83f60bdbfdef63477088c` from 2026-07-10.
- Current Codex++ macOS discovery accepts `Codex.app`, `OpenAI Codex.app`, `OpenAI.Codex.app`, and `ChatGPT.app`. The legacy names are still intentional compatibility candidates rather than canonical internal product naming.
- Current Codex++ resolves a macOS bundle's main executable from `Info.plist` key `CFBundleExecutable`; only bundles without that value fall back to `Contents/MacOS/Codex`. A `ChatGPT.app` fixture with executable `ChatGPT` is covered by upstream tests.
- Current Codex++ macOS launcher carries an app-specific cleanup policy, detects whether the selected bundle was already running, launches with `open`, and can enable the native-menu inspector through a dedicated command path.
- The first repository search used `git grep`, which does not include the newly renamed untracked `chatgpt_desktop.rs`; subsequent local inspection must read/search the filesystem directly.
- The implementation boundary remains: use ChatGPT Desktop for internal domain names and current app discovery, but retain legacy Codex bundle names, bundle identifiers, executable fallbacks, upstream download paths, and `.codex` only where actual artifact compatibility requires them.
- CodeStudio's current macOS implementation is concretely stale: it detects only `/Applications/Codex.app` and `~/Applications/Codex.app`, installs a DMG by requiring `Codex.app` plus `com.openai.codex`, defaults the install target to `/Applications/Codex.app`, reports the tool command as `Codex.app`, and detects/terminates only a process named `Codex`.
- A current `ChatGPT.app` whose main executable/process is `ChatGPT` is therefore invisible to detection, running-state checks, restart shutdown, and the DMG installation result path.
- CodeStudio already injects into both the current renderer (`Runtime.evaluate`) and future documents (`Page.addScriptToEvaluateOnNewDocument`). It retries initial attachment but does not keep Codex++'s long-lived bridge/reinjection watchdog after a successful first injection.
- Current CodeStudio exposes history sync, plugin marketplace unlock/cache/auto-expand, model whitelist, service-tier controls, and computer-use guard. Current Codex++ contains many additional features, but several depend on Codex++-specific helper routes and backend modules; copying only their renderer UI would create nonfunctional controls.
- Native-menu localization in current Codex++ is a separate Electron main-process inspector path (`--inspect=127.0.0.1:<port>` plus a native-menu localizer). It is not part of CodeStudio's current ChatGPT Desktop settings or backend and must be treated as a distinct feature rather than folded into simple bundle-name compatibility.
- The shared macOS DMG installer currently searches the mounted image for exactly one app directory name. ChatGPT Desktop passes only `Codex.app`, so a DMG containing `ChatGPT.app` fails before copy. The generic helper should gain a candidate-list entry point while retaining its single-name wrapper for existing Claude Desktop callers.
- The shared macOS detector already reads `CFBundleIdentifier` and version from `Info.plist`, but it does not expose `CFBundleExecutable`. ChatGPT process detection/termination therefore falls back to a hard-coded `Codex` process even when the selected bundle's executable is `ChatGPT`.
- The CodeStudio launcher architecture intentionally closes the selected desktop app before launch and does not own the app lifetime. Codex++'s `open -W` cleanup policy cannot be copied literally without leaving an untracked waiting child; the compatible adaptation is app-specific launch arguments plus a target-aware reinjection watchdog.
- Current Codex++ additionally recognizes ChatGPT Desktop CDP pages by the exact `ChatGPT` title plus `chatgpt.com`, `chat.openai.com`, or a `data:text/html` error page. CodeStudio previously preferred only targets containing `codex` and then injected into the first arbitrary page; this was a second concrete cause of missing enhancements after the product rename.
- The implemented target picker now follows Codex++'s strict ChatGPT/Codex page recognition and rejects unrelated page targets. The watchdog tracks the selected WebSocket URL and reapplies only when a new target replaces it.
- Final verification on Windows covers all pure macOS discovery, plist, command construction, fallback, target-selection, and shared-package behavior. A macOS host is still required for an end-to-end DMG mount/copy, AppleScript quit, process-signal, and real ChatGPT.app launch smoke test.

## Generation-Aware Desktop Branding

- The existing Codex CLI icon is `public/tool-icons/codex.svg`, a transparent SVG with a white filled Codex mark. The current desktop icon is `public/tool-icons/chatgpt-desktop.png`, the original blue-purple terminal cloud artwork.
- Current `ToolIcon.svelte` maps the canonical `chatgpt-desktop` tool ID directly to the original PNG and has no generation/variant input. It also contains a duplicated `case "chatgpt-desktop"` left by the earlier rename.
- The requested current-generation icon can reuse the CLI SVG with a desktop-only inverted visual treatment; the original PNG should remain available as the legacy desktop icon rather than being overwritten.
- Dynamic naming must come from one backend-owned generation decision and be propagated to the frontend. Inferring legacy state independently in Dashboard, the detail route, Profiles, and Setup Wizard would allow icon/name drift.
- Current Codex++ still uses `OpenAI.Codex`/`OpenAI.CodexBeta` package identities and supports both `ChatGPT.exe` and `Codex.exe`; package identity alone therefore cannot distinguish generations. Prefer an accessible `ChatGPT.exe` main executable as current, a `Codex.exe`-only install as legacy, and default unknown/uninstalled state to current.
- On macOS, prefer `CFBundleExecutable=ChatGPT` as current and `Codex` as legacy. Bundle/app name is a fallback only, because a current app can be installed over a legacy destination path.
- The generation should be serialized as `current` or `legacy` in the installed desktop record and the detection snapshot. This lets cached detail-page and dashboard data drive the same frontend branding state.
- The i18n store can derive translations from both locale and desktop generation, replacing only the exact desktop product phrase (`ChatGPT Desktop` / `ChatGPT 桌面端`) when legacy is active. This updates navigation and all localized desktop-route copy without touching `Codex CLI` terminology.
- Dashboard renders backend `tool.name` directly, so its ChatGPT desktop card needs a localized product-name projection. Other action titles already consume that projected `tool.name` and then stay consistent automatically.
- Profiles and Setup Wizard intentionally collapse desktop aliases into the Codex configuration family. Their `Codex` labels represent the CLI/provider protocol scope and must not be renamed to ChatGPT or Codex Desktop.
- Source verification confirms `DetectionSnapshot` currently ends with install-kind metadata and `InstalledChatGPTDesktop` has no generation field yet; both TypeScript interfaces must be extended in lockstep with Rust serialization.
- `ToolIcon.svelte` hard-codes the legacy PNG, has no generation prop, and contains a duplicate canonical `chatgpt-desktop` case. Panda currently has one `chatgpt-desktop` tone, so current and legacy desktop artwork need distinct tones while Codex CLI keeps its existing tone unchanged.
- Rust has one `DetectionSnapshot` construction site in `core/detector.rs` and four production `InstalledChatGptDesktop` construction paths covering MSIX, macOS apps, portable detection, and completed installs. Adding generation at these boundaries will keep cache, dashboard, and detail-state metadata consistent.
- Existing Windows detection already distinguishes `ChatGPT.exe` from the legacy `Codex.exe` fallback, and macOS detection already exposes `CFBundleExecutable`; generation can therefore be derived from artifacts without a version threshold or package-identity heuristic.
- Frontend `t` is currently derived only from `locale`. A separate desktop-generation store can be added as its second dependency so exact desktop product phrases switch reactively without altering locale dictionaries or Codex CLI strings.
- `App.svelte` owns all live/cached `DetectionSnapshot` application, while `chatgptDesktopStore.ts` separately hydrates cached detail state. Updating the single branding store from both paths covers cold dashboard startup, direct detail-page navigation, installs, and refreshes.
- Dashboard already receives the same snapshot and uses backend `tool.name` in action copy. A narrow `chatgpt-desktop` name projection plus a generation prop to `ToolIcon` will keep card headings, dialogs, and artwork aligned while leaving every other tool untouched.
- Backend `tool_status()` currently hard-codes both `name: "ChatGPT Desktop"` and current-product missing/install copy. Generation-aware `ToolStatus.name` is required so raw backend action/problem copy cannot leak the current brand while a legacy desktop is installed.
- `InstalledMacosApp` stores the bundle path, version, and identifier but not `CFBundleExecutable`; the existing package helper re-reads the executable from the bundle, so generation should be derived before moving the app fields into `InstalledChatGptDesktop`.
- The English locale still defines `app.nav.chatgptDesktop` as `Codex`, while the Chinese locales use `ChatGPT 桌面端`. Current generation must use `ChatGPT Desktop` in English; only the legacy branding transform may produce `Codex Desktop`.
- Browser-preview detection caches bypass Rust deserialization, so `readMockDetectionCache()` must explicitly default a missing generation to `current`; mock installed records and fresh snapshots must also emit the field.
- The detail store receives `InstalledChatGPTDesktop` in both inspected state and install/uninstall operation results. Updating the shared branding store at `applyState` plus operation completion covers direct page use without a dashboard refresh.
- Raw backend copy reaches the detail page through `error`, operation-result notes, plan warnings, and capability details; Dashboard can also surface desktop action errors directly. Apply the exact display-only brand transform at these markup boundaries instead of mutating paths, action codes, URLs, or persisted state.
- Browser verification loaded the current desktop card with `src=/tool-icons/codex.svg`, tone `chatgpt-desktop-current`, and `filter: invert(1)`, but the frame computed to the dark theme surface instead of white. The current tone needs an attribute-level white background override, matching the existing Codex attribute-level safeguard, so the black inverted mark remains legible.
- After Panda codegen, the running Vite page entered a fully unstyled state: the icon frame computed to `width:auto`, the SVG expanded to 1280 px, and unrelated application layout classes also stopped applying. This is a dev-server/generated-style reload failure, not evidence about the new tone; restart the local preview before further visual conclusions.
- A clean Vite preview on port 5174 restored all styles. Current generation renders navigation text `ChatGPT 桌面端`, `/tool-icons/codex.svg`, tone `chatgpt-desktop-current`, a 40 px white frame, a 22 px mark, and `filter: invert(1)` exactly as intended.
- The in-app browser screenshot CDP command timed out twice even for a card-sized clip. DOM, asset URL, dimensions, tone, background, and filter were verified from stable computed styles instead; do not infer a page failure from the screenshot transport timeout.
- Direct asset inspection confirms `public/tool-icons/chatgpt-desktop.png` remains the original blue-purple terminal-cloud artwork with transparency and is reserved for legacy generation; the current generation references `codex.svg` separately.
- Final source audit found no production fixed `Codex` English navigation label, old single `chatgpt-desktop` tone, or fixed `name: "ChatGPT Desktop"` tool construction. Remaining fixed names are test fixtures or the intentional current/legacy mapping; Codex CLI terminology remains present and unmodified.

## GPT-5.6 Official Entry Follow-up

- ChatGPT Desktop launch settings persist from `ChatGPTDesktop.svelte` through `chatgptDesktopStore.ts`, `UpdateChatGPTDesktopSettingsRequest`, Rust storage, and `CodexEnhancementInjectionSettings`; a new default-off field can stay entirely inside this desktop launch path.
- `profile::codex_auth_status()` already classifies the active credential cache without exposing secrets. `CodexAuthMethod::ApiKey` is the exact launch-time gate; `ChatGpt`, `AccessToken`, `Unknown`, and `None` must not activate the new entry.
- The existing generic model whitelist path is unsuitable because `codexPlusModelDescriptor()` uses the configured provider name or `Custom model` as its description.
- OpenAI Codex commit `d2d00b6632dc991aa4471db0529773029cae5d68` exposes the current official GPT-5.6 family as `gpt-5.6-sol`, `gpt-5.6-terra`, and `gpt-5.6-luna`, not a synthetic `gpt-5.6` item.
- The official app-server v2 `Model` protocol uses `id`, `model`, `displayName`, `description`, `hidden`, reasoning options, input modalities, service tiers, and `isDefault`. Supplying those fields directly preserves official presentation and avoids the custom-model descriptor path.
- The renderer must patch JSON responses, Statsig availability, React state, MCP `model/list`, and `list-models-for-host`, but must not set GPT-5.6 as the default model or write any Codex CLI/config.toml state.
