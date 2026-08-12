# Xterm Flicker and Input Freeze Fix

Goal: eliminate the embedded xterm console render loop that causes continuous flicker, UI stalls, and lost keyboard input while preserving terminal session recovery and correct resizing.

Follow-up goal: rename the desktop application domain from Codex Client/Desktop to ChatGPT Desktop across user-visible copy, frontend technical identifiers, Tauri commands, Rust modules, events, and cache keys without renaming Codex CLI concepts or `~/.codex` data.

macOS follow-up goal: bring ChatGPT Desktop detection, installation, launch/restart, process handling, CDP injection, and supported Codex++ enhancements up to the current macOS implementation while preserving real upstream artifact aliases and Codex CLI boundaries.

branding follow-up goal: derive the desktop product generation from the detected installation so current ChatGPT Desktop uses an inverted Codex CLI icon and ChatGPT naming, while legacy Codex desktop installations retain the original desktop icon and Codex desktop naming everywhere.

version follow-up goal: update every CodeStudio Lite application/package version source to 1.3.0 while preserving unrelated dependency versions and generic package-parser fixtures.

download resilience follow-up goal: make ChatGPT Desktop mirror downloads on macOS recover from transient LibreSSL/curl TLS record failures without weakening HTTPS or SHA-256 verification.

GPT-5.6 entry follow-up goal: add an independent ChatGPT Desktop launch option that exposes GPT-5.6 as an official model entry for API-key login only, without changing ChatGPT Auth behavior, Codex CLI, or config.toml model semantics.

profile management follow-up goal: move gateway profile management into the Access Profiles page, add a header-level configuration-file/gateway switch, and leave the Gateway page focused on runtime status, controls, and request activity.

GPT-5.6 removal follow-up goal: remove the temporary ChatGPT Desktop GPT-5.6 launch switch and renderer model-entry patch now that the official desktop client exposes the models itself, while preserving unrelated Codex++ enhancements and CLI boundaries.

### Phase 1: Reconstruct the Regression
- **Status:** complete

- [x] Review the current uncommitted terminal changes and surrounding lifecycle code.
- [x] Identify the event or reactive loop that repeatedly recreates, clears, focuses, or resizes xterm.
- [x] Add a focused regression test for the confirmed loop.

### Phase 2: Implement the Minimal Fix
- **Status:** complete

- [x] Remove the repeated lifecycle trigger without weakening session persistence.
- [x] Keep xterm attachment, fit, focus, and backend resize behavior deterministic.
- [x] Avoid unrelated terminal or styling refactors.

### Phase 3: Verify
- **Status:** complete

- [x] Run targeted terminal tests.
- [x] Run Svelte/type checks and the relevant unit suite.
- [x] Inspect the final diff for accidental churn.

### Phase 4: Map ChatGPT Desktop Domain Boundaries
- **Status:** complete

- [x] Inventory frontend route, store, type, API, locale, test, and file identifiers owned by the desktop app domain.
- [x] Inventory Rust modules, commands, request/response types, events, caches, tests, and user-visible errors owned by the desktop app domain.
- [x] Record explicit exclusions for Codex CLI, `~/.codex`, CLI profiles, CLI launch panels, and Codex++ feature terminology.

### Phase 5: Rename Desktop Domain
- **Status:** complete

- [x] Rename files, symbols, route IDs, API wrappers, Tauri commands, Rust modules, types, events, and caches to ChatGPT Desktop naming.
- [x] Update user-visible copy and documentation in English, simplified Chinese, and traditional Chinese.
- [x] Update tests while retaining regression assertions that Codex CLI identifiers remain unchanged.

### Phase 6: Verify Desktop Domain Migration
- **Status:** complete

- [x] Run residual searches for obsolete desktop-domain names and forbidden CLI renames.
- [x] Run Svelte/type checks, unit tests, production build, Rust formatting/checks, and relevant Rust tests.
- [x] Inspect the final diff for accidental changes outside the desktop app domain and the prior xterm fix.

### Phase 7: Diagnose ChatGPT Desktop Launch Exit
- **Status:** complete

- [x] Trace where `(code=4294967295, signal=null)` is produced and distinguish the fatal exit from the final plugin-sync warning.
- [x] Inspect API-key authentication, official remote plugin cache registration, and launch-time patch ordering.
- [x] Add a focused regression test for the confirmed failure mode.

### Phase 8: Fix and Verify ChatGPT Desktop Launch
- **Status:** complete

- [x] Prevent unsupported remote plugin catalog synchronization from terminating or destabilizing API-key launches.
- [x] Preserve plugin marketplace/model enhancements for supported ChatGPT-authenticated launches.
- [x] Run targeted tests, frontend checks, Rust checks/tests, and final diff inspection.

### Phase 9: Inventory Current macOS Compatibility Gaps
- **Status:** complete

- [x] Compare CodeStudio macOS app discovery, bundle metadata, executable resolution, DMG install, process handling, and launch arguments with current Codex++.
- [x] Compare current-page/future-page injection, reinjection, marketplace/plugin behavior, and other macOS-relevant enhancement paths.
- [x] Record canonical ChatGPT Desktop identifiers and intentional legacy Codex artifact aliases.

### Phase 10: Implement macOS ChatGPT Desktop Parity
- **Status:** complete

- [x] Add regression tests for every confirmed macOS identifier, launch, process, installation, and injection gap.
- [x] Update the macOS implementation without changing Codex CLI or `.codex` concepts.
- [x] Keep old bundle/app/executable identifiers only as compatibility fallbacks where current artifacts still require them.

### Phase 11: Verify macOS Parity
- **Status:** complete

- [x] Run targeted frontend and Rust tests for macOS ChatGPT Desktop behavior.
- [x] Run the full frontend suite, Svelte/type checks, production build, Rust formatting/checks/tests, and whitespace checks.
- [x] Audit the final diff and residual identifiers; document any verification that cannot be executed without a macOS host.

### Phase 12: Define Legacy Desktop Branding Detection
- **Status:** complete

- [x] Inventory the Codex CLI icon, current desktop icon, icon rendering API, desktop detection payload, and every user-visible desktop product-name consumer.
- [x] Define deterministic Windows and macOS rules for legacy versus current ChatGPT Desktop installations.
- [x] Add regression tests for the detected branding metadata and frontend consumption boundary.

### Phase 13: Implement Generation-Aware Branding
- **Status:** complete

- [x] Expose canonical desktop product generation/name/icon metadata from the backend detection result.
- [x] Render current ChatGPT Desktop with the inverted Codex CLI icon and legacy Codex desktop with the original icon.
- [x] Replace fixed desktop product names across dashboard, detail page, actions, notices, and operation copy with the detected name while preserving Codex CLI terminology.

### Phase 14: Verify Dynamic Branding
- **Status:** complete

- [x] Run targeted icon, detection, store, dashboard, route, locale, and Rust tests.
- [x] Run full frontend checks/tests/build plus Rust formatting/checks/tests.
- [x] Audit residual fixed ChatGPT Desktop/Codex desktop strings and inspect both icon assets or rendered states.

### Phase 15: Analyze Frontend Bundle Composition
- **Status:** complete

- [x] Inspect the Vite/Rollup configuration, package graph, and current production chunk output.
- [x] Identify stable dependency families responsible for the oversized entry chunk.
- [x] Select a conservative split that preserves application loading behavior.

### Phase 16: Implement Production Code Splitting
- **Status:** complete

- [x] Add scoped Rollup chunk grouping without raising the warning threshold.
- [x] Keep application code and lazy-loading behavior unchanged unless analysis proves a route-level split is needed.
- [x] Record the resulting production chunk boundaries and sizes.

### Phase 17: Verify Bundle and Frontend Regressions
- **Status:** complete

- [x] Run the production build and confirm no chunk exceeds Vite's default 500 kB warning threshold.
- [x] Run Svelte/type checks and the complete frontend unit suite.
- [x] Run whitespace/diff checks and verify the generated application loads from the production output.

### Phase 18: Inventory Application Version Sources
- **Status:** complete

- [x] Locate npm, Tauri, Cargo, lockfile, source, and test occurrences of the previous version.
- [x] Separate CodeStudio Lite metadata from third-party dependency versions and parser fixtures.
- [x] Confirm runtime version injection derives from package metadata.

### Phase 19: Update Application Version to 1.3.0
- **Status:** complete

- [x] Update npm package metadata and its lockfile root package entries.
- [x] Update Tauri and Cargo package metadata plus the local Cargo lock entry.
- [x] Verify no application-owned 1.2.3 metadata remains.

### Phase 20: Verify Version Consistency and Builds
- **Status:** complete

- [x] Run the focused version metadata/runtime-injection tests.
- [x] Run frontend checks, production build, and relevant Rust metadata/compile checks.
- [x] Run final residual searches and whitespace/diff checks.

### Phase 21: Trace macOS Mirror Download Failure
- **Status:** complete

- [x] Locate the real ChatGPT Desktop mirror metadata and package download code, including untracked renamed backend files.
- [x] Identify current curl protocol, retry, resume, timeout, and error-reporting behavior.
- [x] Define a recovery path specifically for transient TLS/read failures while preserving fatal HTTP and integrity failures.

### Phase 22: Implement Resilient Mirror Download
- **Status:** complete

- [x] Add focused regression coverage for the macOS transport, retry, and resume policy.
- [x] Implement bounded retry/resume and an HTTP/1.1 fallback at the shared package-download boundary.
- [x] Preserve HTTPS certificate validation, SHA-256 verification, cache semantics, and Windows behavior.

### Phase 23: Verify Download Regression
- **Status:** complete

- [x] Run targeted Rust tests for ChatGPT Desktop/package downloads.
- [x] Run Rust formatting/checks plus frontend checks affected by command/copy assertions.
- [x] Audit the final diff, residual curl commands, and whitespace.

### Phase 24: Map Auth and Official Model Entry Boundaries
- **Status:** complete

- [x] Trace launch-option persistence from Svelte/types through Tauri requests into the injected renderer settings.
- [x] Trace API-key versus ChatGPT Auth detection available to the renderer injection.
- [x] Identify the official GPT-5.6 model object/filter path and distinguish it from custom catalog injection.

### Phase 25: Implement API GPT-5.6 Launch Option
- **Status:** complete

- [x] Add localized frontend control, TypeScript/Rust settings fields, defaults, persistence, and launch propagation.
- [x] Add API-login-only renderer injection that preserves the official model presentation and leaves ChatGPT Auth untouched.
- [x] Add focused frontend and Rust regression coverage, including Codex CLI/config.toml boundaries.

### Phase 26: Verify GPT-5.6 Entry Regression
- **Status:** complete

- [x] Run targeted launch-option and renderer-injection tests.
- [x] Run full frontend checks/tests/build and Rust formatting/checks/tests.
- [x] Audit residual identifiers, auth gating, final diff, and whitespace.

### Phase 27: Map Profile and Gateway Ownership
- **Status:** complete

- [x] Inventory profile-mode state, CRUD, ordering, activation, and navigation currently owned by Profiles and Gateway.
- [x] Define the Access Profiles header switch and route/state behavior for configuration-file versus gateway profiles.
- [x] Identify Gateway-page profile-management UI that must move or be removed while preserving runtime controls and logs.

### Phase 28: Consolidate Gateway Profiles into Access Profiles
- **Status:** complete

- [x] Add focused regression coverage for the header switch, gateway-profile management, and Gateway-page ownership boundary.
- [x] Implement the unified Access Profiles view using existing profile APIs and mode semantics.
- [x] Remove gateway profile management from the Gateway page without breaking active-profile application or gateway runtime behavior.

### Phase 29: Verify Unified Profile Management
- **Status:** complete

- [x] Run targeted Profiles/Gateway navigation and state tests.
- [x] Run full frontend checks/tests/build and affected Rust tests.
- [x] Verify desktop/mobile layout, final ownership boundaries, diff, and whitespace.

### Phase 30: Map Temporary GPT-5.6 Override Removal
- **Status:** complete

- [x] Inventory the frontend setting, API transport, persisted Rust field, auth gate, and renderer descriptor patch.
- [x] Separate temporary GPT-5.6 entry logic from model-whitelist and service-tier enhancements.
- [x] Define backward-compatible handling for already persisted settings containing the removed field.

### Phase 31: Remove Temporary GPT-5.6 Override
- **Status:** complete

- [x] Replace positive GPT-5.6 injection tests with upstream-ownership regression coverage.
- [x] Remove the launch toggle, locale copy, TypeScript/Rust settings field, and update propagation.
- [x] Remove the API-key auth gate and renderer descriptor injection without changing Codex CLI/config.toml behavior.

### Phase 32: Verify Official GPT-5.6 Ownership
- **Status:** complete

- [x] Run targeted frontend and Rust tests for ChatGPT Desktop launch enhancements.
- [x] Run full frontend and Rust verification together with the profile-management regression.
- [x] Audit residual GPT-5.6 identifiers and confirm any remaining model IDs serve only independent official-model capabilities.

## Errors Encountered

| Error | Attempt | Resolution |
|---|---|---|
| Rust format check requested one test `write_all` line collapse | First full download-regression formatting pass | Run `cargo fmt`, then repeat formatting and compile/test checks; no semantic change is required. |
| New mirror transport tests failed with 22 missing-symbol compile errors | Test-first retry/resume validation | Confirmed the red baseline contains only the intentionally absent Rustls client, retry policy, and response-mode helpers; implement those exact boundaries next. |
| Final HTTP smoke check found the previous 4174 production preview had exited | Version 1.3.0 preview verification | Restart the current production output on the same free port and repeat the HTTP check. |
| PowerShell `ConvertFrom-Json` rejected the empty-string root package key in `package-lock.json` | First six-source version consistency script | Re-read the lockfile with `-AsHashtable` and address the root package through `['packages']['']`; application files were valid and unchanged. |
| Parallel PowerShell reads failed with `CreateProcessWithLogonW failed: 1056` | Initial version-source inventory | Reduced the number of concurrent child processes and completed the reads in smaller batches; no files had been modified. |
| PowerShell parsed the inline Node asset-regex character class as its own syntax | First production asset-closure check | Restrict the verifier to Vite's generated double-quoted references and rerun with shell-safe quoting. |
| `rg.exe` failed to launch from the WinGet link | Repository symbol search | Switched to `Select-String` and `git grep` for local searches. |
| Parallel PowerShell source read was rejected while applying read ACLs | Targeted source inspection | Split the commands into smaller reads with simpler quoting. |
| Initial `npm install` could not write to the user npm cache under sandbox permissions | Install FitAddon | Re-ran the scoped npm install with approved elevated access; `@xterm/addon-fit` 0.10.0 installed successfully. |
| Vite could not bind `127.0.0.1:1420` or `:1421` (`EACCES`) even after approval | Browser runtime verification | Check Windows excluded ports and retry on an unreserved high port. |
| Planning completion checker reported 0/0 phases | Final planning validation | Updated phase headings and status markers to the skill's machine-readable template format. |
| First rename pass left snake-case command strings and Rust locale-key strings | Mechanical domain rename | Add a second targeted pass for `codex_client`/`CODEX_CLIENT` in frontend files and `codexClient` locale keys in Rust. |
| `svelte-check` still saw the generated `codex-app` Panda tone | First post-rename check | Regenerate Panda output after source/config identifiers stabilize. |
| Replacing both legacy `codex-app` and `codex-client` aliases produced duplicate `chatgpt-desktop` match arms | First Rust check | Restore the old aliases explicitly beside the new canonical ID, removing unreachable-pattern warnings. |
| Targeted storage read referenced a nonexistent `storage_tests.rs` | Resumption source inspection | Read the inline storage tests from `storage.rs` instead of assuming a separate test module. |
| Rust check reported duplicate `serde(default)` on `chatgpt_desktop_install_kinds` | First post-compatibility compile | Merge the alias, default, and skip-serialization options into one serde attribute. |
| Three desktop static tests failed after the rename | First targeted regression run | Update Rust assertions to the agreed `ChatGptDesktop*` convention and switch the remaining Claude copy assertion to `desktopClient.*`; implementation and Svelte checking were otherwise clean. |
| First full `cargo test` could not rename the generated `.lib` archive (`os error 5`) | Full verification | Treat as a transient Windows file lock, confirm no cargo/rustc process remains, then rerun Rust tests sequentially. |
| Full Rust suite found a parallel schema-initialization race around `codex_client_state` | Isolated-target Rust test run | Make legacy row copy tolerant of another connection already removing the table and use `DROP TABLE IF EXISTS` so migration is idempotent under concurrent initialization. |
| First legacy-key cleanup patch had an invalid empty hunk | Final migration cleanup | Reissued the same scoped changes with valid per-file patch contexts; no source files were changed by the failed attempt. |
| Auth-gating regression test was duplicated in the Rust test file | First launch-fix test run | Removed the duplicate while implementing the missing auth and config reconciliation functions. |
| Initial fix gated the remote plugin cache to ChatGPT OAuth | First launch-fix implementation | Rejected after user correction: Codex++ supports this feature with API-key auth, so preserve the capability and reproduce its implementation instead of disabling it. |
| Shallow clone could not create `C:\tmp\CodexPlusPlus` in the sandbox | Download Codex++ source | Re-ran the authorized public `git clone` outside the sandbox; the temporary checkout completed successfully. |
| Sandbox Git rejected the host-owned temporary checkout as dubious ownership | Search freshly cloned Codex++ source | Use per-command `-c safe.directory=C:/tmp/CodexPlusPlus` for read-only inspection without changing global Git configuration. |
| First removal patch for the obsolete uninstall process script did not match the current formatted error strings | Consolidate package-aware process shutdown | Re-read the exact current block and removed it with a narrower matching patch; the failed attempt changed no files. |
| New macOS parity tests failed to compile on the old implementation | Test-first validation | Confirmed the missing candidate, bundle-executable, process/launch identity, DMG candidate, and injection-watchdog helpers; proceed with the scoped implementation. |
| First residual identifier audit recursively scanned `src-tauri/target` and did not finish promptly | Final source audit | Stopped that agent-owned PowerShell scan and reran the audit only across source extensions under `src` and `src-tauri/src`. |
| First Phase 12 findings append used a progress-file line as context | Record branding interface discoveries | Re-read the actual `findings.md` tail and reissue the append against its real final line; the failed patch changed no files. |
| Phase 12 red tests reported 4 frontend failures and 20 Rust compile errors | Test-first branding boundary | Confirmed every failure is the intentionally missing generation type, helper, store, tone, or consumer wiring; no unrelated test or syntax failure appeared. |
| First post-implementation Rust compile found one test fixture without `generation` | Targeted generation tests | Added `Current` to the existing macOS installed-record fixture; all production constructors were already covered. |
| First frontend branding run had one stale Panda tone type and one over-specific Profiles assertion | Targeted frontend validation | Regenerate Panda output for the new tones and assert the intentional `Codex` configuration-family label rather than `Codex CLI`. |
| Browser current-icon check showed a dark inherited frame behind the inverted black mark | Visual branding verification | Add an attribute-level white frame override for `chatgpt-desktop-current`, then regenerate and recheck computed styles in the browser. |
| Immediate browser reload captured an unstyled transient frame | Post-codegen visual recheck | Wait for network-idle and a stable dashboard DOM before evaluating computed icon styles or taking the next screenshot. |
| In-app browser rejected its documented `networkidle` load state | Stable visual recheck | Use one short post-reload wait followed immediately by `readyState`, stylesheet-count, DOM, and computed-style checks; do not retry `networkidle`. |
| Post-codegen dev-server page lost all Panda class styling | Visual branding verification | Inspect Vite output and restart only the local Vite preview if needed; do not treat the unstyled page as an icon implementation result. |
| First clean Vite restart could not reclaim port 5173 | Visual branding verification | Leave the unknown remaining listener untouched and start the clean preview on 5174, per the repository server-port rule. |
| In-app browser `goto` to the clean 5174 preview timed out and reset its control session | Visual branding verification | Re-bootstrap the browser and select the already-open 5174 tab from the tab list; do not issue another `goto`. |
| In-app browser screenshot timed out for both viewport and card clip | Visual branding verification | Use stable DOM and computed-style assertions for the icon; retain the clean 5174 preview for user inspection instead of retrying screenshot capture. |
| First full frontend suite retained one fixed-name protocol assertion | Full branding verification | Update the test to require generation-driven `ChatGPT Desktop`/`Codex Desktop` names; the other 142 tests and production build already passed. |
| Updated protocol test exposed a second stale migration-layout regex | Full branding verification | Assert save-on-`migrate_legacy || settings_changed` and subsequent legacy-key deletion as two ordered semantic checks; do not change the working migration code. |
| First full Rust suite could not link because D: reported no space left | Full branding verification | Verify and remove only the agent-created `.codex-target-branding` cache, then reuse the repository's existing default target for the complete suite. |
| First target-size PowerShell command had an invalid pipe after `foreach` | Build-cache cleanup audit | Collect rows into an array before piping to `Format-Table`; no filesystem mutation occurred. |
| Default Rust target rejected writing the test fingerprint (`os error 5`) | Full branding verification | Confirm no cargo/rustc process owns the cache, then clean the verified workspace-local default target and rebuild once from a single cache tree. |
| Unprivileged `cargo clean` hit the same stale target ACL | Build-cache cleanup | Re-ran the scoped `cargo clean` with approval; D: recovered about 50.2 GB and the workspace target is ready for a clean rebuild. |
| First final port-audit command repeated the invalid `foreach` pipe form | Preview cleanup | Collect connection rows into an array before formatting, then stop only the stale 5173 Vite process and retain 5174. |
| First Rust format check found only rustfmt layout changes | Full branding verification | Run `cargo fmt`, then repeat the format and compile checks before the full Rust suite. |
| Existing launchpad copy test still required the Gateway page to describe profile selection | First unified-profile targeted run | Update the stale assertion to the new runtime/privacy/log ownership copy; production behavior and Svelte checking were already correct. |
| Rust format check requested one legacy-setting test expression collapse | First GPT-5.6 removal verification | Run `cargo fmt`, then repeat formatting, targeted tests, full check, and the complete Rust suite. |
