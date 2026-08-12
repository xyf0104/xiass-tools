# Codex Review Model Configuration

Goal: allow Codex access profiles to define an optional `review_model`, persist it compatibly, preview it, and write/remove it in Codex `config.toml` without affecting other tools.

### Phase 1: Trace Current Contract
**Status:** complete

- [x] Trace profile types, SQLite schema/migrations, create/edit forms, and mock parity.
- [x] Trace Codex native config detection, preview, apply, and cleanup behavior.
- [x] Decide compatibility and validation rules for optional `review_model`.

### Phase 2: Add Regression Tests
**Status:** complete

- [x] Add Rust coverage for Codex `review_model` write, removal, detection, and non-Codex isolation.
- [x] Add frontend/static coverage for create/edit visibility and request propagation.

### Phase 3: Implement
**Status:** complete

- [x] Extend shared profile/request/storage contracts with an optional review model.
- [x] Add Codex-only create/edit controls and localized copy.
- [x] Update real and mock preview/apply paths.

### Phase 4: Verify
**Status:** complete

- [x] Run focused and full Rust/frontend tests.
- [x] Run formatting, checks, production build, and diff validation.
- [x] Verify the Codex workflow in the local dev app.

### Phase 5: Make Blank Review Model Follow Primary Model
**Status:** complete

- [x] Represent blank Codex review model as a dynamic fallback to the profile `model`.
- [x] Align native config generation, preview, matching, and verification with the fallback.
- [x] Update UI copy and regression coverage, then rerun focused and full validation.

## Errors Encountered

| Error | Attempt | Resolution |
|---|---|---|
| `rg` with Windows wildcard paths returned OS error 123 | 1 | Use `rg --files` and pass concrete file paths instead of shell-style wildcard path arguments. |
| New Rust review-model tests failed to compile on missing fields/helpers | 1 | Expected RED state; implement shared contracts, storage helpers, and Codex behavior next. |
| New frontend ownership test failed on missing `reviewModel` contract | 1 | Expected RED state; add form, mock, type, and locale support next. |
| Combined `profile.rs` patch missed the SQL preview context | 1 | No partial edit landed; split the change into smaller exact-context patches. |
| `npm run check` reported seven unknown review-model translation keys | 1 | Expected while UI landed before locales; add the keys to all three locale dictionaries. |
| `cargo fmt --check` found review-model line wrapping differences | 1 | Apply the exact rustfmt output only to the three files changed for this task. |
| Dev server background launch could not redirect logs into `C:\tmp` | 1 | Retry with log files inside the active planning directory; Vite started on port 1420. |
| Concurrent native-profile reuse edits changed during formatting | 1 | Preserve the new logic, verify its review-model comparison, then run rustfmt and the full Rust suite against the latest file state. |
| Phase 5 focused Rust/frontend tests failed on missing follow-primary behavior | 1 | Expected RED state; implement effective review-model resolution, mock parity, and localized copy next. |
| Rust compile failed because preview-only `primary_model` was inserted in `codex_gateway_config_content` | 1 | Move the variable to `build_native_config_preview`, where `mode` exists and the preview consumes it. |
| Full Rust suite encountered concurrent partial Grok profile edits | 1 | Preserve the unrelated live edits; wait for the concurrent change to settle, inspect current state, and rerun rather than reverting it. |
| Panda ownership test failed after `panda.config.ts` changed to CRLF | 1 | Make the recipe-boundary regex accept both LF and CRLF; the actual two-column recipe remains unchanged. |
| `cargo fmt --check` found unformatted concurrent Grok additions | 1 | Run standard `cargo fmt`; the changes are mechanical and leave the verified behavior unchanged. |
