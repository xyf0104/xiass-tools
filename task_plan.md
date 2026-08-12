# Panda CSS Full Migration Plan

Goal: migrate CodeStudio Lite's UI styling to Panda CSS as the long-term styling system, replacing the current large global CSS file with generated tokens, recipes, and component-scoped class composition.

## Phase 1: Infrastructure and Token Baseline
Status: in_progress

- [x] Install Panda CSS and PostCSS integration.
- [x] Add `panda.config.ts`, PostCSS config, generated styled-system ignore rules, and entry CSS layers.
- [x] Mirror the first existing theme CSS variables needed by migrated shared components as Panda semantic tokens.
- [x] Add recipes for the first shared controls: notices, status pills, secret inputs, problem rows, and tool status cards.
- [x] Add recipes for shared buttons, icon buttons, panels, section headings, empty rows, and activity rows.
- [x] Add recipes for the remaining shared controls that appear across pages: tabs, progress primitives, settings rows, and card primitives.
- [x] Add static tests that assert Panda is wired into package scripts, config, and source imports.

## Phase 2: Shared Component Migration
Status: in_progress

- [x] Migrate reusable components first: `DismissibleNotice`, `StatusPill`, `ToolStatusCard`, `SecretInput`, and `ProblemList`.
- [x] Migrate `ActivityLog` and shared button/panel primitives used by shared components.
- [ ] Migrate layout helpers and remaining shared primitives.
- [x] Replace broad shared global classes with recipe usage where the component owns the selector.
- [x] Keep existing page markup working during the transition.

## Phase 3: Shell and Navigation Migration
Status: in_progress

- [x] Migrate `App.svelte` shell, sidebar, navigation, workspace frame, and route transition wrapper.
- [x] Migrate remaining route shell primitives: `route-stack`, `top-strip`, `panel-band`, top actions, status strips, shared action buttons, section headings, and empty rows.
- [x] Move migrated shell-specific layout rules from global CSS into Panda recipes/classes.

## Phase 4: Route Migration
Status: complete

- [x] Migrate the Dashboard main content cards before the install/launch modals.
- [x] Migrate the remaining Dashboard install and launch modal surfaces.
- [x] Migrate the Codex Client main route surfaces.
- [x] Migrate the Codex Client uninstall confirmation modal.
- [x] Migrate the Claude Desktop route surfaces, including progress, plan, logs, and modal surfaces.
- [x] Migrate the Profiles modal/diff surfaces.
- [x] Migrate the Profiles main tool switcher and cards/lists.
- [x] Migrate the Setup Wizard route surfaces.
- [x] Migrate the Gateway route surfaces.
- [x] Migrate the Settings route surfaces.
- [x] Migrate the Terminal Panel route surfaces.
- Prefer recipes and shared helpers over page-only class names when patterns repeat.

## Phase 5: Global CSS Reduction
Status: complete

- Remove migrated selectors from `src/styles.css`.
- [x] Remove migrated desktop-client progress/log/global modal/native diff/native toggle selectors from `src/styles.css`.
- [x] Remove migrated Profiles tool switcher and main card/list selectors from `src/styles.css`.
- [x] Remove migrated Setup Wizard stepper/choice/OAuth/write-preview selectors from `src/styles.css`.
- [x] Remove migrated Gateway metrics/privacy/request-log selectors from `src/styles.css`.
- [x] Remove migrated Settings list/about/update-pill selectors from `src/styles.css`.
- [x] Remove migrated App shell/sidebar/navigation/workspace/route-transition selectors from `src/styles.css`.
- [x] Remove migrated route shell/action/heading/button/empty-row selectors from `src/styles.css`.
- [x] Remove migrated Profiles embedded stack, edit/usage form, icon editor, usage result, and write-preview selectors from `src/styles.css`.
- [x] Remove unused `preview-list` compatibility selectors from `src/styles.css`.
- [x] Remove Dashboard environment-conflict inline notice compatibility selectors from `src/styles.css`.
- [x] Remove shared `eyebrow` and `spin` utility class selectors after moving them to Panda recipes.
- [x] Remove `tool-icon` compatibility selectors after moving `ToolIcon` tone and size styling to Panda.
- [x] Remove unowned legacy tool/provider/backup/test/OAuth/profile-choice compatibility selectors and empty media blocks from `src/styles.css`.
- [x] Move the final bare `error-banner` and `wide-field` helpers to Panda recipes.
- Keep only actual global resets, app theme bootstrapping, base form/code rules, and keyframes in `src/styles.css`.

## Phase 6: Verification and Visual QA
Status: in_progress

- [x] Run Panda codegen, TypeScript/Svelte checks, unit tests, and production build.
- Start the dev server and inspect key pages in desktop and narrow viewports.
- Fix regressions before considering the migration complete.

## Phase 7: Desktop Client Plan Cache UX Fix
Status: complete

- [x] Reproduce the Codex Client and Claude Desktop cached-plan foreground refresh placeholder regression with failing tests.
- [x] Keep cached update-plan details visible while background refresh runs.
- [x] Remove foreground "plan refreshing" placeholder rows/status pills from cached plan sections.
- [x] Verify with targeted desktop-client tests, Svelte check, full unit tests, production build, and diff whitespace checks.

## Phase 8: CodeStudio Lite Burn WPF Theme
Status: complete

- [x] Replace the plain WinForms installer surface with a branded WPF wizard while preserving Burn detect/plan/apply behavior.
- [x] Reuse the existing CodeStudio Lite icon and application color system without adding heavyweight UI dependencies.
- [x] Preserve localization, install-directory selection, upgrade-location detection, silent mode, and keyboard behavior.
- [x] Add static regression coverage for the WPF theme and remove WinForms-only UI assumptions.
- [x] Rebuild the compact Burn bundle, visually inspect the wizard, and run full verification.

## Phase 9: Same-Version Burn Registration Consolidation
Status: complete

- [x] Reproduce the duplicate registry entries as multiple same-version related bundles with `operation: None`.
- [x] Plan same-version related bundles absent when installing the current bundle.
- [x] Keep the chained MSI hidden and preserve normal upgrade, repair, and uninstall behavior.
- [x] Rebuild and verify the Burn plan without applying changes.

## Phase 10: Burn System Language Auto-Selection
Status: complete

- [x] Remove persisted installer-language state that overrides the current Windows display language.
- [x] Resolve the user UI language through the Windows API with a safe system/culture fallback.
- [x] Keep explicit command-line language selection as the highest-priority override.
- [x] Stop treating the normalized base MSI ProductLanguage as the selected transform language.
- [x] Verify an unattended plan selects the current Windows language without passing `SelectedLanguage`.

## Phase 11: Burn Apply Start Deadlock Fix
Status: complete

- [x] Capture a real install log showing `Plan complete` without `Apply begin`.
- [x] Cache the WPF HWND on the UI thread instead of constructing `WindowInteropHelper` from a Burn callback.
- [x] Preserve the existing progress transition and Apply call.
- [x] Rebuild and verify the BA plus installer configuration tests.

## Phase 12: Burn Completion Launch Action
Status: complete

- [x] Replace the duplicate successful-completion Close action with an Open CodeStudio Lite primary action.
- [x] Keep failure completion and the secondary button as Close actions.
- [x] Resolve the installed executable from the selected installation directory and report launch failures.
- [x] Rebuild and verify the Burn installer.

## Phase 13: Burn Installation Retry
Status: complete

- [x] Show Retry plus Close after an Apply failure without ever rendering duplicate Close actions.
- [x] Preserve the selected language, install directory, and launch action when retrying.
- [x] Reset completion and exit state before replanning.
- [x] Keep non-recoverable detection, planning, and window initialization failures close-only.
- [x] Rebuild and verify the Burn installer.

## Errors Encountered

| Error | Attempt | Resolution |
|---|---|---|
| `planning-with-files` catchup script not found under `.claude` | Restore context | Re-ran the catchup script from the installed `.codex` skill path. |
| `StatusPill.svelte` recipe prop inferred as `string` | `npm run check` | Added an explicit `PillTone` union and typed reactive variable before calling `statusPillRecipe({ tone })`. |
| `iconButtonRecipe({ compact: true })` failed type checking | Dashboard main migration | Added a `compact` variant to `iconButtonRecipe`, regenerated Panda types, and reran `npm run check`. |
| Deleting unused Dashboard modal CSS left a dangling `.install-log strong, {` selector | Dashboard modal CSS cleanup | Rechecked the affected `src/styles.css` slice, fixed the selector to `.install-log strong`, then verified with `npm run build`. |
| PowerShell treated a quoted `rg` regex as an array/index expression | Claude Desktop CSS cleanup | Re-ran the evidence search with simpler fixed/wider searches before deleting unused selectors. |
| PowerShell treated a complex `Select-String -Pattern` as positional arguments | App shell cleanup verification | Re-ran the selector check with `-SimpleMatch` and separate literal patterns. |
| PowerShell parsed a quoted `rg` alternation as a pipeline while checking residual selectors | Final global CSS cleanup | Re-ran selector checks with static migration tests and simpler fixed-string scans. |
| PowerShell treated a quoted `Select-String` alternation as positional arguments | Cached-plan placeholder verification | Re-ran the check with `-SimpleMatch` and separate fixed-string patterns. |
| WPF generated `InstallerWindow` as public while code-behind was internal | Burn WPF compile | Added `x:ClassModifier="internal"` to keep both partial declarations aligned. |
| Footer-copy removal accidentally collapsed the brand-icon row | Burn sidebar cleanup | Restored the first sidebar row to `Auto`; removed only the footer element and localization assignment. |
| Burn output EXE was locked during relink | Burn rebuild after copy cleanup | Stopped only the two visual-QA setup processes, then rebuilt and reran plan-only verification successfully. |
| PowerShell `Get-UICulture` reported `en-US` while Windows UI API reported `zh-CN` | System-language verification | Changed the verifier to use the same `GetUserDefaultUILanguage` / `GetSystemDefaultUILanguage` APIs as the BA. |
