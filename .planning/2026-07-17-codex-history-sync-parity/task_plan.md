# Codex History Sync Parity

Goal: bring CodeStudio Lite's ChatGPT/Codex Desktop history preservation and provider synchronization to feature parity with the latest Codex++ implementation while preserving current cross-platform launch behavior.

## Phase 1: Contract and Red Tests
Status: complete

- [x] Map current Tauri/frontend seams and latest Codex++ provider-sync contracts.
- [x] Add red regression coverage for projectless threads, global-state path normalization, database sidecar backups, provider targets, encrypted-content warnings, non-blocking launch, and session-index cleanup safety.

## Phase 2: Provider Sync Core
Status: complete

- [x] Add structured sync statuses/results and explicit provider targets.
- [x] Add provider discovery from config, rollout files, SQLite, and manual input.
- [x] Add projectless filtering, workspace state normalization, encrypted-content warnings, complete SQLite/WAL/SHM backups, and rollback behavior.

## Phase 3: Session Index Management
Status: complete

- [x] Add preview and selected cleanup with live-thread discovery.
- [x] Add snapshot concurrency protection, process guard, backup, and atomic replacement.

## Phase 4: Tauri and UI Integration
Status: complete

- [x] Expose provider targets, explicit sync, preview, and cleanup commands.
- [x] Extend ChatGPT Desktop settings UI with sync results, risk warnings, target selection, and index cleanup controls.
- [x] Keep automatic launch sync non-blocking while logging useful diagnostics.

## Phase 5: Verification and Review
Status: complete

- [x] Run targeted Rust and frontend tests during implementation.
- [x] Run type checking, full unit suite, production build, and relevant Rust test suite.
- [x] Review the final diff against the requested Codex++ parity scope.

## Errors Encountered

| Error | Attempt | Resolution |
|---|---|---|
| Unterminated Rust character literal in Windows path normalization | First core test compile | Corrected the path separator character to an escaped backslash literal. |
| API type-import patch context did not match current ordering | Frontend API integration | Re-read the import block and inserted the new types at their actual neighboring entries. |
| Strict Clippy reported warnings across unrelated dirty-worktree modules | Final verification | Fixed both warnings in the new sync module and verified that module has no Clippy output; retained unrelated user changes untouched. |
| Spec review found four session-index cleanup safety gaps | Final review | Preserved invalid UTF-8 byte-for-byte, added a pre-write process recheck, propagated non-lock rollout errors, and restricted apply requests to current preview candidates. |
