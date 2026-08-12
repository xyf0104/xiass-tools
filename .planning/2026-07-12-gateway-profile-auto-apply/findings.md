# Findings

## Requested Behavior

- Creating a gateway profile should make it the active gateway profile only when the corresponding tool currently has no active gateway profile pointer.
- Creating later gateway profiles must not replace an existing active selection.
- Gateway profile actions must not expose apply-and-restart; restart only has meaning for direct native configuration writes.

## Initial Trace

- `profile::save_profile_draft` currently persists and returns the draft without setting any active-profile pointer.
- The main apply confirmation condition already checks `selectedApplyMode === "config" && writesNativeConfig`, but every entrypoint and selected-mode reset still needs review to prevent stale direct-mode state from leaking into gateway actions.
- `save_profile_draft` returns only `ProfileDraft`; if backend auto-activates it, the existing immediate frontend upsert may still lack the updated active gateway pointer unless the callback refreshes or the API result is expanded.
- `openApply` resets `selectedApplyMode` from `profile.mode`, and the visible restart button is currently guarded by selected mode plus native-write support. A hard guard based on `pendingApply.mode` will make gateway restart absence invariant against stale UI state.
- `App.svelte::refreshAfterProfileChange` immediately reloads the backend profile summary after applying the local draft upsert. Backend auto-activation will therefore propagate without changing the `save_profile_draft` return type.
- Profile editing preserves the original mode and duplication has a separate flow, so the requested auto-activation belongs specifically in new draft creation rather than generic save/upsert storage.
- Setup Wizard's save callback currently performs only `applySavedProfile(profile)` before navigating to Profiles; unlike the Profiles route callbacks, it does not explicitly await `refreshAfterProfileChange`. The callback should use the shared refresh path so the new backend active pointer is visible immediately.
- `App.svelte` already refreshes profile/gateway state reactively whenever the route changes to Profiles, so adding another awaited Wizard refresh would duplicate the request. The backend pointer update plus existing route refresh is sufficient.
- `activate_profile_for_tool` already canonicalizes and cleans active maps. A narrow helper can call it only for new gateway drafts whose tool has no valid non-empty gateway pointer, while preserving all existing selections.
- The current branch is `main` with existing product changes plus untracked planning artifacts; this task must use narrowly scoped edits and must not commit or push unless separately requested.
- `session-catchup.py` found no missed implementation context, only one unsynced inspection tool call; the active plan pointer is `2026-07-12-gateway-profile-auto-apply`.
- The browser/dev mock `saveProfileDraft` has its own profile persistence path and currently does not establish the first gateway active pointer, so it needs parity with the Rust behavior.
- The only visible apply-and-restart action found in `Profiles.svelte` is guarded by `selectedApplyMode === "config" && selectedModePreview?.writesNativeConfig`; adding `pendingApply.mode === "config"` prevents any gateway profile from exposing it even if preview state becomes stale.
- `save_profile_draft` persists the profile and optional credential, then logs success and returns; it currently never loads or writes `AppConfig`, so activation must be inserted after durable profile/credential storage and before the success return.
- A pure helper can use `active_profile_id_for_app` plus the saved draft list to distinguish a valid existing gateway selection from an empty or stale pointer, then reuse `activate_profile_for_tool` only when activation is needed.
- The Rust unit-test module imports private profile helpers through `use super::*`, so the activation rule can be tested without filesystem state.
- Both Rust `apply_profile` and browser mock `applyProfile` already reject `restartAfterApply` whenever the profile's actual mode is not Config, so the new UI guard is backed by API invariants.
- `git diff --check` passes; the frontend files contain only the intended mock activation, static regression test, and restart-button guard changes.
- `sync_active_profiles_from_native_configs` delegates only to Config-mode synchronization and does not mutate gateway pointers, so the route-triggered summary refresh preserves the newly auto-selected gateway profile.
- Wizard navigation to Profiles triggers `refreshAfterProfileChange`, which reloads both profile summary and gateway status after the immediate local draft upsert; no API response expansion or duplicate Wizard callback request is needed.
- End-to-end browser verification exposed stale preview copy: both Rust and mock `previewProfileWrite` still emit `not_modified` plus "Saving a draft does not switch the active profile" for every mode.
- The preview pointer item must be conditional: a Gateway draft with no valid active gateway profile for that tool should preview an `update`; direct profiles and gateway tools with an existing valid selection should remain `not_modified`.
- `SetupWizard.svelte` localizes pointer details without reading backend detail text, so it needs a second translation key selected by the preview item's `action === "update"`; `actionLabel` also needs an explicit `update -> common.update` branch.
- The save and preview paths should share a pure `gateway_profile_will_auto_activate` predicate (and mock equivalent) so stale-pointer handling cannot drift between what the user sees and what persistence does.
- Browser verification confirmed the first gateway preview shows action `更新` with the localized auto-activation explanation, then the saved card immediately renders `已启用` without navigation refresh tricks.
- A second gateway profile preview correctly returns `不写入` for the pointer, and saving it preserves the first profile as active.
- The inactive second gateway profile's apply dialog contains exactly one `应用网关配置` button, zero `应用并重启` buttons, and no browser console errors.
