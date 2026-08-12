# Findings

## Initial State

- `Profiles.svelte` calls `updateProfileDraft(...)`, discards the returned `ProfileDraft`, then awaits `onProfileSwitched()` before closing the editor.
- `SetupWizard.svelte` retains the saved profile only for its name and also relies on the parent callback to reload the profile summary.
- `App.svelte` owns `profileSummary` and passes it to `Profiles`; route navigation triggers another profile refresh, matching the reported delayed update symptom.
- The active worktree has no tracked changes after the previous `1.3.1` commit; existing untracked planning/workflow files must remain uncommitted.

## Root Cause

- Tauri `save_profile_draft` and `update_profile_draft` return the persisted `ProfileDraft` directly, and `ensure_app_dirs` reloads the same SQLite-backed profile list; there is no backend cache boundary to repair.
- `SetupWizard.svelte` converts the saved draft into only `profile.mode`, while `Profiles.svelte` discards the updated draft entirely.
- `App.svelte` previously could not update its owned `profileSummary` until an asynchronous reload finished. Returning the saved draft enables an immediate immutable upsert.
- Browser verification exposed a second stale-state layer: `Profiles.svelte` caches its drag-sort array using only profile IDs. An edit preserves the ID, so even a fresh parent summary did not replace the old card object.
- The complete narrow fix is an immutable parent-summary upsert plus a drag-list cache key derived from profile content, not IDs alone. If the draft is active, the summary and Gateway display names should be updated at the same time.
- Existing background refresh sequencing should remain as the final source-of-truth reconciliation and should not be removed.
- Live browser verification confirmed the complete fix: after editing `已即时更新名称` to `二次即时更新`, the card heading changed immediately after save on the same route with no navigation or reload.
- The browser console reported no warnings or errors during create/edit verification.
- Final diff review confirmed that create, edit, and duplicate flows all forward the persisted draft into the shared summary owner, while the existing lightweight refresh remains the source-of-truth reconciliation for profile-page mutations.
- Unrelated root planning/workflow files remain untracked and outside the product diff.
