# Findings

## Constraints

- This turn produces a modification plan only; do not edit product code.
- Preserve all existing dirty-worktree changes, including the just-completed Pi integration and unrelated ChatGPT, gateway, download, and release work.
- Use the `codebase-design` vocabulary: module, interface, implementation, seam, adapter, depth, leverage, and locality.
- Do not create hypothetical seams around a single implementation. Extract a seam when multiple tool adapters genuinely vary behind it.
- The user-provided debugging rule is acknowledged; this architecture-planning task does not require a debugging workflow.

## Initial Hypothesis

- `src-tauri/src/core/profile.rs` likely combines orchestration, persistence-facing domain rules, protocol normalization, and tool-native configuration adapters in one implementation.
- Similar tool-specific knowledge may be duplicated across detector, installer, launcher, gateway, frontend mock API, and profile forms.
- The target should not be one file per tool by itself. The useful design is a small shared orchestration interface with deep tool adapters that own complete native behavior.

## Size And Responsibility Evidence

- `src-tauri/src/core/profile.rs` is approximately 10,957 lines and 395 KB. It contains public profile commands, draft persistence orchestration, preview/apply lifecycle, active-profile state, native detection and import, provider/model HTTP logic, keychain interaction, restart process control, and per-tool config rendering/matching/cleanup/verification.
- Other large backend implementations are also mixed: `gateway.rs` is about 6,549 lines, `chatgpt_desktop.rs` 5,423, `claude_desktop_patch.rs` 4,790, and `tool_installer.rs` 3,869.
- Frontend hotspots mirror the backend matrix: `src/lib/api.ts` is about 5,333 lines, `Profiles.svelte` 2,916, `Dashboard.svelte` 1,904, and `SetupWizard.svelte` 1,403.
- The strongest extraction candidate inside `profile.rs` is the repeated tool-native lifecycle cluster. Each tool has variants of detect, render direct config, render gateway config, match, clean managed entries, preview, and verify, while shared orchestration repeatedly dispatches on canonical app id and mode.
- Restart process ownership and remote provider/model listing are different responsibilities from native profile configuration. They should not be folded into the same extracted interface merely because they currently live in `profile.rs`.

## Codebase-Design Assessment

- `profile.rs` currently provides locality only at the file level; tool changes still require editing many distant match arms and helper clusters inside the same implementation.
- Splitting helpers into files without consolidating dispatch would fail the deletion test: the same complexity would remain spread across orchestration call sites.
- A useful deep module must let callers request a native profile operation through a small interface while each tool adapter owns the complete implementation for its native format.
- Native files are local-substitutable dependencies. Adapters should operate primarily on supplied current content and return plans/results; filesystem and keychain effects stay in shared orchestration or internal seams so tests can exercise the same external interface with fixtures.

## Dispatch And Knowledge Duplication

- `build_native_apply_plan` performs filesystem reads and then dispatches through large `(mode, canonical app)` matches to tool-specific render functions. A separate verification dispatcher repeats the same tool/mode matrix.
- Native path selection, official-profile behavior, preview support, apply rendering, cleanup, matching, detection, and verification each maintain their own app-id dispatch. Adding a tool therefore requires coordinated edits across distant regions of `profile.rs`.
- Canonical tool aliases are independently implemented in backend `profile.rs`, `tool_launch.rs`, and `env_health.rs`, plus frontend `api.ts`, `Profiles.svelte`, and `SetupWizard.svelte`.
- Frontend tool labels, order, supported protocols, review-model support, official-profile behavior, and special warnings are expressed as route-local maps and conditionals. This creates a second capability catalog that can drift from the backend.
- `tool_registry.rs` already centralizes detection and install metadata, but it is a flat list of data definitions. It is a suitable owner for canonical identity and aliases, not for native profile implementation details.

## Proposed Primary Seams

- `ToolCatalog` module: canonical identity, aliases, display name, category, executable metadata, config capability flags, and protocol capabilities. Its interface should answer questions about a tool without exposing storage or native document formats.
- `NativeProfileAdapter` seam: one adapter per genuinely different native format/tool behavior. Shared orchestration selects an adapter once, then asks it for path resolution, detection, plan rendering, matching, cleanup, and verification.
- `ProfileStore` module: draft CRUD, ordering, active pointers, built-in official profiles, and migration of persisted profile data. This stays independent of native file formats.
- `ProfileManager` module: public use-case orchestration for preview, apply, switch, import/sync, and delete. It coordinates the store, credentials, gateway settings, and native adapter registry.
- Restart ownership and provider HTTP/model listing should become separate deep modules, not methods on `NativeProfileAdapter`.

## Public Interface And Compatibility

- The existing `profile` module's public functions mix application settings, Codex OAuth, profile draft lifecycle, preview/apply, connection testing, model listing, native reconciliation, and active-profile switching.
- Tauri command names and request/response types are already the external compatibility interface. The refactor should keep those commands stable and replace their internal delegation only.
- `ProfileManager` should expose use-case-level methods rather than every helper currently in `profile.rs`. Command wrappers remain thin by design because they are transport adapters at an existing seam.
- Profile draft persistence already has concrete SQLite operations in `storage.rs`. Do not add a generic repository port solely for abstraction; first move profile-specific persistence functions behind a concrete `ProfileStore` module. Introduce a trait only if a second adapter is justified for tests or migration.
- Tool-native content functions should become pure where possible: accept current content, profile, mode, and resolved gateway/credential inputs; return a write plan and verification expectation. This maximizes testability without exposing parser details.

## Secondary Hotspots

- `gateway.rs` combines runtime lifecycle, raw HTTP parsing, authorization, route selection, model endpoints, canonical request/response conversion for four protocols, streaming conversion, privacy filtering, upstream transport, and response writing. Its natural deep modules are runtime/server, routing/auth, canonical protocol conversion, streaming conversion, and upstream transport.
- `Profiles.svelte` combines profile grouping, apply/edit/delete workflows, usage-script management, drag ordering, model mapping forms, icon import, preview/error translation, and rendering. It should become a route composition module over focused state modules and UI modules.
- `SetupWizard.svelte` duplicates profile capability rules and form helpers from `Profiles.svelte`. Shared frontend profile-domain functions should be extracted before splitting visual modules.
- `src/lib/api.ts` contains both the production Tauri invoke adapter and an extensive browser-development fake with its own state, catalog, validation, native preview generation, install lifecycle, and gateway behavior. This is a real two-adapter seam: production Tauri adapter and browser fake adapter should satisfy one frontend interface.
- Detector and installer are large, but their tool-specific variation is already partly data-driven through `ToolDefinition`. They should consume the shared `ToolCatalog` first; deeper extraction follows only where platform adapters or install strategies genuinely vary.

## Scope Ordering Decision

- Avoid a simultaneous rewrite of profile, gateway, installer, detector, and frontend. First establish shared identity/capability ownership, then migrate native profile behavior tool by tool, then simplify callers.
- Treat gateway protocol conversion as a separate program after profile modularization because its canonical-message model and streaming state need independent regression coverage.
- Treat desktop-client patch modules as later specialized programs; their platform and binary-patching risks are unrelated to profile-native configuration and should not share the same migration.

## Errors During Audit

- A combined PowerShell `rg` alternation for match-arm searches was parsed as an unclosed group. Use separate fixed-string searches or targeted line reads for the remaining audit.
