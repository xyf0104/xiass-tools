# Progress

## 2026-07-13

- Started tracing the profile data contract, Codex TOML generation, and UI ownership for an optional review model.
- Created a separate active plan and recorded the dirty-worktree preservation boundary.
- Traced the shared profile/request types and SQLite schema/load/save path; selected a nullable additive column with blank/non-Codex normalization for backward compatibility.
- Located the Codex native config write/match/detect/verify surfaces and both frontend form owners; continued toward exact behavior and test points.
- Finalized backend semantics for direct, gateway, official, import, matching, verification, and preview behavior, including legacy official-profile compatibility.
- Finalized frontend ownership: independent Codex-only review-model inputs for create/edit, shared model options, OAuth support, request-key invalidation, and conditional card display.
- Restored the interrupted session, confirmed no implementation edits had landed, and reviewed the existing dirty diff so the gateway activation and restart fixes remain intact.
- Completed Phase 1 contract tracing and moved to regression-test-first implementation.
- Added regression coverage for schema roundtrip, Codex review-model normalization, TOML set/remove behavior, native detection, matching, verification, preview diffs, and frontend/mock ownership. Production code is intentionally unchanged pending the RED run.
- Confirmed the RED state: Rust failed on the missing review-model contract and helpers; the frontend suite failed only the new ownership test. Phase 2 is complete and implementation has started.
- Added Rust/TypeScript `review_model` contracts and SQLite schema v9 support with additive migration plus connection-level load/save helpers for roundtrip testing.
- Implemented backend normalization, save/update/duplicate/preview propagation, Codex TOML set/remove behavior, detection/import, custom and official matching semantics, apply verification, and native preview diffs. Rust now compiles with the new contract.
- Added Codex-only create/edit controls, card display, mock save/update/preview/native-diff parity, SQL preview output, and localized copy. Focused Rust/frontend tests and `npm run check` pass.
- Full verification passed: Rust 337/337, frontend 156/156, Svelte check, rustfmt check, production build, and `git diff --check`. Started the Vite app at `http://127.0.0.1:1420/` for browser verification.
- Browser-tested Codex API, OAuth, and gateway forms; verified non-Codex isolation, SQL preview output, edit persistence, immediate card refresh, and zero console errors.
- Final stable-worktree validation passed after concurrent edits settled: Rust 337/337, frontend 156/156, Svelte check, rustfmt check, production build, and diff checks. The Vite server remains available at `http://127.0.0.1:1420/`.
- Follow-up requirement received: blank review model must follow the primary model. Reopened the plan with Phase 5 and selected a nullable persisted sentinel plus effective-value resolution at Codex config boundaries.
- Added Phase 5 regression expectations for direct/gateway/official fallback writing, legacy missing-key matching, detected-profile equivalence, native preview behavior, mock ownership, and localized follow-primary copy. Production behavior is still unchanged pending the RED run.
- Confirmed the intended RED state: both focused Rust failures were blank review models still resolving to `None`, and the frontend ownership test failed only on the not-yet-added effective mock helper/new copy.
- Implemented shared Rust effective-value resolution, writer/matcher/detection/preview integration, mock preview parity, follow-primary locale copy, and corrected the Rust gateway preview to use the same primary model value as the real writer.
- Focused validation is green: 7 Rust review-model tests, 8 frontend ownership tests, and `cargo fmt --check` all passed after correcting the misplaced preview variable.
- Full frontend validation passed at 156/156 with zero Svelte diagnostics. The full Rust run was interrupted by unrelated concurrent partial Grok profile edits that currently reference not-yet-defined functions; those edits are being preserved.
- The concurrent Grok work continued through the next full run: it temporarily left one Rust roundtrip and one stale two-option Panda assertion failing. The Grok roundtrip was fixed by its owner and now passes in isolation; review-model tests remain green throughout.
- Latest full validation is green at Rust 338/338, frontend 156/156, and zero Svelte diagnostics. Production build passed; applied standard Rust formatting to the concurrent Grok additions after `cargo fmt --check` identified only style differences.
- Browser verification completed in the local mock app: the field placeholder reads `留空时跟随主模型`, persisted preview keeps `review_model: null` as the dynamic sentinel, and the client apply preview resolves both `model` and `review_model` to `gpt-5.5`. The temporary mock profile was deleted afterward.
- Phase 5 final gate passed on the settled worktree: Rust 338/338, frontend 156/156, Svelte check with zero diagnostics, production build, `cargo fmt --check`, and `git diff --check` all succeeded.
