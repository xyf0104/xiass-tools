# Panda CSS Migration Findings

## Burn WPF Theme Findings

- The current bootstrapper owns its complete UI in `BootstrapperApplication.cs`, so the Burn engine and MSI chain can remain unchanged while replacing only `InstallerForm`.
- The application theme uses dark surfaces `#0A0B0D`, `#101216`, `#14171D`, blue accent `#1F8FFF`, and yellow brand ink `#F4D94E`.
- WPF is part of .NET Framework and does not require bundling a third-party UI runtime, so the existing compact single-MSI-plus-MST design can remain.
- The existing `src-tauri/icons/icon.ico` can be passed into the managed BA as a UX payload and loaded from the Burn working directory.
- The ICO provides the window/taskbar icon, while `128x128.png` provides a sharper in-window brand image for only about 15 KB additional payload.
- A custom WPF ComboBox template is required because the default Windows theme ignores dark background setters and renders a white control.
- Native folder selection remains the system `FolderBrowserDialog`, but it must receive the WPF `WindowInteropHelper` handle so the dialog stays owned and foregrounded.

## Burn Duplicate Registration Findings

- The MSI uninstall record is not the visible duplicate: it has `WindowsInstaller=1` and `SystemComponent=1`.
- Two visible Burn records exist under separate generated BundleIds, both version `1.4.1`, with the same UpgradeCode family.
- Burn logs classify both as `type: Upgrade`, `version: 1.4.1.0`, `operation: None`; default planning leaves both registered because equal versions are not major upgrades.
- WiX 3 does not expose a Bundle `Id` authoring attribute, so the safe fix is to request same-version related bundles as absent during the current install plan.

## Burn System Language Findings

- `SelectedLanguage` was persisted and then treated as an explicit override, so an old English selection could suppress the current Windows `zh-CN` UI language.
- The current machine reports `zh-CN` from both `GetUserDefaultUILanguage` and `GetSystemDefaultUILanguage`.
- The multilingual MSI is normalized to ProductLanguage 1033 before transforms are embedded, so `MsiGetProductInfo(..., "Language")` cannot identify whether zh-CN or zh-TW was selected.

## Burn Apply Start Findings

- The real install log reaches `Plan complete, result: 0x0` but contains no `Apply begin`, no MSI execution, and no active `msiexec` process.
- `OnPlanComplete` changes the WPF page asynchronously and then reads `form.Handle`; the old property constructs `WindowInteropHelper` on the Burn callback thread.
- WPF window interop must be initialized on the UI thread and exposed to Burn as a cached plain `IntPtr`.

## Current UI Structure

- The app is a Svelte + Vite project with one global stylesheet imported from `src/main.ts`.
- The current styling model is a large `src/styles.css` file containing theme variables, shell layout, shared controls, route panels, modals, lists, progress UI, and page-specific selectors.
- The UI is an operational desktop tool, so the migration should preserve dense, predictable information layouts instead of introducing marketing-style or highly decorative components.

## Panda CSS Integration Notes

- Official Panda docs recommend PostCSS integration for Svelte/Vite projects.
- The entry CSS needs Panda layers: `@layer reset, base, tokens, recipes, utilities;`.
- Package scripts should include `prepare: panda codegen` so generated styled-system files exist after install.
- Panda scans source files and generates styling primitives at build time; config must include Svelte and TypeScript source globs.
- This checkout now uses `@pandacss/dev/postcss` through `postcss.config.cjs`, with `preflight: false` so the global reset does not change while the migration is partial.
- `styled-system/` is generated locally for type checking and build extraction, and is ignored by git.

## Migration Strategy

- Full migration should be staged by ownership boundary rather than by visual page alone: infrastructure, reusable components, shell, pages, cleanup.
- Existing theme CSS variables can be mirrored as Panda semantic tokens first, allowing recipes to use stable token names while the current UI still renders.
- Global CSS should shrink gradually. Removing it immediately would be too risky because many pages share legacy class names.
- The first migrated shared components are `DismissibleNotice`, `StatusPill`, `SecretInput`, `ProblemList`, and `ToolStatusCard`.
- The second migrated shared component batch added `ActivityLog` plus shared recipes for panels, section headings, action buttons, icon buttons, empty rows, activity lists, and activity rows.
- Removed global CSS only where the component was the sole owner: `.notice*`, `.secret-input`, `.problem-*`, and `.activity-*`.
- Kept global `.tool-*` selectors at this stage because some non-shared surfaces still used that vocabulary; `.pill` was later removed once Settings moved to `settingsUpdatePillRecipe`.
- Panda recipe prop inference can require explicit Svelte-side union types when a reactive value is passed to a generated recipe function.
- Shared button recipes should be reused by component-level migrations instead of copying the old `.primary-button`, `.secondary-button`, or `.icon-button` declarations into local `css(...)` calls.
- Dashboard main cards now own their grid, card, status, action-row, overflow, and route-surface behavior through Panda recipes. The legacy `.system-grid`, `.system-card`, `.system-main`, `.system-copy`, `.system-card-state`, `.client-card-actions`, `.clickable-card`, and `.card-action-*` rules can stay deleted while `src/lib/dashboardLayout.test.mjs` guards the replacement recipe shape.
- Route-surface animation for migrated Dashboard cards now lives in `dashboardGridRecipe`; the shared global route-transition rules should keep covering only legacy selectors until the shell is migrated.
- Dashboard install/launch modals now use Panda recipes for the modal shell, progress panels, command previews, info grids, preview lists, terminal/log cards, and launch option tiles. The Dashboard-specific legacy selectors `.install-command-*`, `.install-meta`, `.install-result-grid`, `.install-terminal-*`, `.launch-section*`, `.launch-option*`, and `.launch-directory-field` can stay deleted.
- Shared legacy modal/progress selectors such as `.install-progress`, `.progress-*`, `.install-log`, `.preview-list`, `.inline-error`, and `.inline-success` still had consumers during the early page slices, so they were removed only after the owning page markup moved to recipes.
- Codex Client now uses Panda recipes for its main route surfaces and uninstall confirmation modal: tabs, top actions, panels, section headings, settings grids, native checkbox rows, metrics, action rows, progress, preview lists, capability rows, empty rows, modal shell, modal actions, and modal buttons.
- The new Codex Client recipes are intentionally reusable by Claude Desktop because `ClaudeDesktop.svelte` still shares the same desktop-client layout vocabulary: install-kind tabs, metrics, action rows, progress panels, plan preview lists, native toggle rows, and doctor rows.
- The first Codex Client slice did not remove global selectors because `ClaudeDesktop.svelte`, `Profiles.svelte`, and `SetupWizard.svelte` still consumed them; later Claude/Profile/Codex-modal slices removed the desktop-client, modal, native diff, and native toggle globals once those consumers were gone.
- Claude Desktop now uses the shared `desktopClient*` Panda recipes for its main route surfaces, including install-kind tabs, launch-option settings, status metrics/actions, progress, update plan preview, capability rows, live logs, Accessibility authorization, and uninstall confirmation.
- After both Codex Client and Claude Desktop moved off the old desktop-client global classes, the legacy `.install-kind-tabs`, `.install-progress`, `.progress-*`, `.doctor-list`, `.doctor-row`, `.install-log`, `.live-install-log`, `.install-log-viewport`, and `.install-log-stage` selectors could be removed from `src/styles.css`.
- Profiles modal/diff surfaces now use Panda recipes for modal shells, preview lists, native toggles, the official OAuth usage notice, diff panels/headings/rows, inline notices, and modal action buttons.
- Profiles main tool switcher and card list now use Panda recipes for the selected-tool panel, sortable grid, row wrapper, compact card, identity/avatar/drag handle, status placement, and action rows. Drag styling no longer depends on legacy classes: `styleDraggedProfileElement` sets `data-sortable-active` on the row and `data-drag-active` on `[data-profile-card]`.
- After the Profiles main list migration, legacy selectors such as `.profile-mode-layout`, `.profile-tool-switcher`, `.profile-tool-tabs`, `.profile-grid`, `.profile-tool-section`, `.profile-sortable-row`, `.compact-profile-card`, `.profile-card`, `.profile-card-main`, `.profile-drag-handle`, `.profile-avatar`, `.profile-identity`, `.profile-card-status`, `.sortable-active-row`, and `.sortable-active-card` can stay deleted from `src/styles.css`.
- The remaining `.profile-choice-item.active-profile` selector is a separate apply-modal choice-list surface and should not be treated as part of the now-migrated main card list.
- After Profiles modal/diff migration and the Codex Client uninstall modal migration, `.modal-backdrop`, `.modal-panel`, `.modal-body`, `.modal-actions`, `.native-diff`, `.native-diff-heading`, and `.native-write-toggle` no longer have production Svelte consumers and can stay deleted from `src/styles.css`.
- `preview-list`, `inline-error`, and `inline-success` still had production consumers during the Profiles cleanup; after Setup Wizard and Settings migrated, the remaining inline-error compatibility consumer is the Dashboard environment conflict banner.
- `usage-official-panel` no longer has production consumers after the Profiles usage modal moved to `profileUsageOfficialPanelRecipe`, so that legacy selector can stay deleted.
- The `progress-pulse` keyframes must stay global for now because Panda recipes still reference that animation name for Dashboard and desktop-client indeterminate progress bars.
- Setup Wizard now owns its route stack, action bar, stepper, step panels, tool/profile-mode choices, OAuth card, inline notices, security note, preview box, write-preview rows, metadata, content preview, and warning list through `wizard*Recipe` entries.
- Setup Wizard state styling now uses `data-step-state` and `data-selected` attributes instead of Svelte `class:active`, `class:done`, and `class:selected` bindings, which keeps state selectors local to Panda recipes.
- After Setup Wizard migration, legacy `.stepper`, `.wizard-panel`, `.wizard-step-content`, `.wizard-tool-choices`, `.wizard-mode-choice`, `.codex-auth-card`, `.preview-box`, `.preview-heading`, `.write-preview-list`, `.write-preview-row`, `.write-preview-meta`, `.preview-warnings`, `.button-row`, `.security-note`, `.choices`, `.compact-choices`, and `.field-grid` selectors no longer have production consumers and can stay deleted from `src/styles.css`.
- Profiles still uses `.form-grid`, `.field-error`, and `.write-content-preview`, so those compatibility selectors should remain until the next Profiles apply/edit form cleanup slice.
- Gateway now owns its page-specific status panel, hero action row, metrics cards, privacy segmented control, inline errors, request-log panel, request rows, and privacy action chip colors through `gateway*Recipe` entries.
- Gateway now composes the shared route shell recipes with `gatewayHeroRecipe`; the older `gatewayRouteRecipe` and `gatewayActionsRecipe` were removed in favor of `routeStackRecipe` and `topActionsRecipe`.
- Gateway privacy selection moved from `class:selected` to `data-selected`; request log privacy coloring moved from generated `privacy-*` classes to `data-privacy-action`, matching the actual `none | detected | redacted | blocked` request type.
- After Gateway migration, legacy `.gateway-*`, `.sidebar-gateway-error`, `.compact-heading`, and the stale `.wizard-actions` group reference no longer have production consumers and can stay deleted from `src/styles.css`.
- Settings now owns its preference list, rows, row values, about panel, about summary, app mark/title/update row, and update status pill through `settings*Recipe` entries.
- After Settings migration, legacy `.settings-list`, `.settings-row`, `.settings-row-value`, `.settings-toggle-row`, `.about-*`, and `.pill*` selectors no longer have production consumers and can stay deleted from `src/styles.css`.
- The shared sidebar `.brand-mark` selector must remain because `App.svelte` still owns the sidebar logo mark, while Settings now uses `settingsAboutMarkRecipe` directly.
- The global `.inline-error` and `.inline-success` compatibility selectors should remain for now because the Dashboard environment-conflict surface still uses `class="inline-error env-conflict-banner"` outside the migrated Settings route.
- Terminal Panel now owns its route shell, header, title, status, action row, and xterm frame through `terminalPanel*Recipe` entries.
- `TerminalPanel.svelte` no longer needs a component-local `<style>` block; it still imports `@xterm/xterm/css/xterm.css` because that is third-party terminal rendering CSS, while the app-specific frame and viewport adjustments live in `terminalPanelFrameRecipe`.
- Phase 4 route migration is complete: Dashboard, Codex Client, Claude Desktop, Profiles, Setup Wizard, Gateway, Settings, and Terminal Panel have route-owned surfaces on Panda recipes. Shell/navigation globals remain for Phase 3.
- App shell/sidebar/navigation/workspace now use `app*Recipe` entries from Panda. Active navigation state moved from Svelte `class:active` to `data-active`, which keeps active/hover/sidebar mobile behavior owned by `appNavButtonRecipe`.
- The route transition wrapper now uses `appRouteTransitionRecipe`; it owns the surface-rise animation for nested `.cs-top-strip`, `.cs-panel`, and Panda-owned `.cs-tool-card` surfaces while Dashboard card sequencing is owned by `dashboardGridRecipe`.
- `.brand-logo` must not remain as a global selector because the App and Settings logo containers now provide the nested sizing rule through `appBrandMarkRecipe` and `settingsAboutMarkRecipe`.
- Shared route shell primitives now live in Panda recipes: `routeStackRecipe`, `topStripRecipe`, `topActionsRecipe`, `statusStripRecipe`, and `sectionActionsRecipe`. Dashboard, Codex Client, Claude Desktop, Settings, Profiles, Setup Wizard, and Gateway use these instead of global `route-stack`, `top-strip`, `panel-band`, `top-actions`, and `status-strip` classes.
- Shared action/heading/empty state globals (`primary-button`, `secondary-button`, `icon-button`, `section-heading`, `section-actions`, `empty-row`) no longer have production Svelte consumers and can stay deleted from `src/styles.css`.
- Profiles edit and usage forms now use Panda recipes for the embedded stack, form grids, field errors, icon editor/actions, usage template selector, usage code textarea, usage result grid/cards, and native write-content preview.
- After the Profiles form cleanup, legacy selectors such as `.embedded-profile-stack`, `.form-grid`, `.field-error`, `.edit-profile-form`, `.profile-icon-editor`, `.profile-icon-actions`, `.edit-mode-field`, `.edit-mode-toggle`, `.usage-template-row`, `.usage-form`, `.usage-code-field`, `.usage-result-grid`, `.usage-result-card`, `.usage-balance-value`, and `.write-content-preview` no longer have production Svelte consumers and can stay deleted from `src/styles.css`.
- The unused `preview-list` compatibility selectors were removed after confirming no production Svelte route/component still emitted `class="preview-list..."`; the remaining `preview-list` text is limited to migration-test guards and Panda recipe class names such as `desktopClientPreviewListRecipe`.
- Dashboard environment-conflict notices now use `dashboardEnvConflictRecipe` and `data-dashboard-env-conflict-chips`, so legacy `.inline-error`, `.inline-success`, `.error-banner`, `.env-conflict-banner`, and `.conflict-chip-list` selectors can stay deleted from `src/styles.css`.
- Shared utility styling for route/modal eyebrows and loading icons now lives in `eyebrowRecipe` and `spinRecipe`. The `.eyebrow` and `.spin` global selectors are gone; `@keyframes spin` remains global because the Panda recipe references that animation name.
- ToolIcon tone, image sizing, fallback text, and card/choice/heading size variants now live in `toolIconRecipe`; `ToolIcon.svelte` exposes state through `data-tool-icon-variant`, `data-tool-icon-tone`, and `data-tool-icon-fallback-text`. The old `.tool-icon*` selectors and the recipe-local dead `.tool-icon` selectors can stay deleted.
- The last unowned legacy tool/provider/backup/test/OAuth/profile-choice compatibility selectors were dead CSS: production Svelte routes/components no longer emitted `.tool-card`, `.tool-grid`, `.provider-summary`, `.provider-mode-grid`, `.backup-row`, `.test-panel`, `.profile-choice-*`, `.oauth-*`, or `.quiet`. They can stay deleted from `src/styles.css`.
- The final bare helper classes were `class="error-banner"` in `App.svelte` and `class="wide-field"` in `SetupWizard.svelte`; these now live in `appErrorBannerRecipe` and `wizardWideFieldRecipe`, respectively.
- `src/styles.css` is now reduced to theme variables, base reset/scrollbar/form/code styling, and global keyframes (`spin`, `progress-pulse`, `surface-rise`).
- Codex Client and Claude Desktop plan caches were present, but cached pages still looked uncached because `planRefreshing` was rendered as foreground plan-section text, status pills, and loading rows while background refresh ran.
- Cached desktop-client update plans should render stable cached details first and let refresh run silently. The visible refresh affordance can remain on the explicit refresh button; the plan detail section should only swap to stale/unavailable messaging when `planStale` or missing details require it.
- For Codex Client, `planRefreshing` remains useful only for stale/unavailable plan feedback. When `effectivePlan` exists, the update plan and capabilities sections should show package URL, SHA-256, install root, warnings, and capabilities without an extra "plan refreshing" row or pill.
- For Claude Desktop, cached `activePlanDetails` should show download URL, SHA-256, and install location without a foreground refresh row. The store can still track `kindView.planRefreshing` internally for background refresh state.
