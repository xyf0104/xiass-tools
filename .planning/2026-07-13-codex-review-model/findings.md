# Findings

## Initial State

- No `review_model` or `reviewModel` field currently exists in the repository.
- Profile contracts currently expose a primary `model` plus optional `model_mappings`; the latter is a Claude Code-specific feature and should not be reused for Codex review behavior.
- The worktree already contains verified but uncommitted gateway, macOS download, and Windows PowerShell changes; this task must preserve them and remain narrowly scoped.

## Requested Behavior

- Codex profiles should optionally define `review_model` in their managed `config.toml` output.
- Other tools must not expose or persist Codex-specific review model behavior.
- Follow-up clarification: a blank Codex review-model field means "follow the current primary `model`", not "use an unrelated Codex default" or simply remove the effective setting.
- Keep the persisted optional value blank as the dynamic-follow sentinel; resolve it against the profile model at config/matching/preview boundaries so later model edits stay coupled.
- Codex gateway output may substitute the primary model with the gateway fallback (`default`), so review-model fallback must resolve against the actual model written by each output path rather than blindly reading `profile.model`.
- Existing configs with no `review_model` should be interpreted as effectively using their top-level `model`; this keeps legacy configs compatible with the new follow-primary semantics.
- Native detection/matching should treat an omitted review model and an explicit review model equal to the primary model as equivalent, while preserving an explicit different review model as an override.
- Official OAuth profiles commonly leave the primary model blank. In that case there is no concrete model to mirror, so both model keys remain absent and legacy official matching stays permissive.
- Existing test fixtures default the primary model to blank, so fallback regression cases must set a concrete model explicitly (or assert the gateway `default` fallback) rather than accidentally continuing to test only key removal.
- The Rust native preview currently displays `gateway::client_config_for_tool(...).model` (`codestudio-default`), while the real Codex gateway writer uses `gateway_config_model_for_profile(profile)`. Phase 5 should derive both the previewed primary model and fallback review model from the writer's value so preview and apply agree.
- During full verification, an unrelated Grok tool integration was written concurrently across Rust and frontend files. Its initial partial state broke Rust compilation and repeated Vite HMR resets interrupted browser navigation; preserve it and rerun only after its writes become quiet.

## Contract Trace

- `ProfileDraft`, save/update/preview requests, mock profiles, and SQLite currently carry `model` and `model_mappings` but no review-model field.
- SQLite uses additive `ensure_profiles_*_column` migrations, so a nullable `review_model TEXT` column fits the existing compatibility pattern without changing old rows.
- Create, load, update, duplicate, built-in profiles, previews, and test fixtures all construct `ProfileDraft`; every constructor must receive a defaulted review-model value.
- The safe boundary is an optional string normalized to `None`/`null` when blank or when the target app is not the canonical Codex family.
- Session catch-up found no missed implementation context beyond an inspection tool call; the new active plan remains authoritative.
- Codex has separate direct, gateway, official, verification, detection, matching, and native-diff functions; review-model support must be consistent across all relevant paths or active-profile synchronization will drift after restart.
- Both Setup Wizard and Profiles edit modal own independent model state and request builders, so create and edit propagation require separate coverage.
- Browser mock save/update/preview and built-in mock profiles construct the same shared `ProfileDraft` shape and need explicit `reviewModel` parity.
- Direct, gateway, and official Codex writers all edit top-level `model`; the review model should use the same top-level optional-key pattern in all three writers and in verification.
- Custom direct-profile matching should compare normalized `review_model` exactly, while official matching should mirror existing main-model compatibility: an unspecified official review model does not invalidate an otherwise matching legacy config.
- Codex native profile detection/import must read `review_model`; all non-Codex `DetectedNativeProfile` constructors should set it to `None`.
- Native apply previews are assembled as explicit diff lines, so `review_model` needs a set/remove diff in direct official, direct custom, and gateway branches in addition to final content generation.
- Schema version is currently 8 and migrations are additive; this change should bump it to 9 and add `ensure_profiles_review_model_column` plus column/roundtrip tests.
- The Setup Wizard currently forces the primary model empty for Codex OAuth; review model should stay independently editable because it is a valid Codex top-level preference for official configurations too.
- Both forms can reuse their existing fetched model options through a second `ModelSelectInput`; visibility should be keyed to canonical Codex tool identity, not provider or profile mode.
- The Profiles card should render the review model only when present so saved state is inspectable without reopening Edit.
- Auto-import matching helpers currently key on provider/protocol/model/base URL/API key; review model must join the Codex detected-profile comparison so imported profiles update instead of duplicating or silently losing the field.
- Existing `profile_tests.rs` coverage already isolates Codex detection, direct matching, direct/gateway/official TOML generation, verification/apply planning, and native preview behavior, so review-model regression tests can extend those seams without new harnesses.
- The shared Rust and TypeScript request contracts each have three write surfaces: save, update, and preview. Duplicate copies the stored profile directly and therefore must copy the new field too.
- SQLite currently selects `model` followed by `model_mappings_json`; adding `review_model` beside `model` will shift all later row indexes and SQL parameter positions, so load/save tests must guard both the nullable field and existing mapping deserialization.
- Native auto-import creation and provider-correction update are separate paths. The creation path must store the detected review model, while the correction path must also refresh it before saving the imported profile.
- Codex custom matching currently treats an empty primary model as requiring no top-level `model`; review-model matching should use the same explicit optional-key comparison for custom profiles, while official matching remains lenient only when the stored review model is blank.
- Setup Wizard hides the primary model picker for Codex OAuth, so the review-model picker must sit outside the `!codexOAuthConfig` block while still reusing the same `modelOptions`; otherwise OAuth profiles cannot configure it.
- Both form preview keys currently include the primary model and mappings. `reviewModel.trim()` must join those keys so changing only the review model invalidates and refreshes the preview.
- The mock native preview has distinct official, custom direct, and gateway change arrays, mirroring Rust. Each branch needs a `review_model` set/remove change plus matching generated content.
- Immediate card refresh needs no new cache logic: `profileListContentKey` serializes the complete profile objects, so adding `reviewModel` to `ProfileDraft` automatically changes the list content key after an edit.
- Browser verification confirmed the review-model control appears for Codex direct API, official OAuth, and gateway profiles, remains hidden for Claude Code, is included in the SQLite write preview, and refreshes the profile card immediately after edit-save.
