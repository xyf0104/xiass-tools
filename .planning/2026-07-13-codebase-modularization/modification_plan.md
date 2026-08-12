# Codebase Modularization Modification Plan

## Objective

Replace large tool-id dispatch clusters with deep modules while keeping Tauri commands, request/response shapes, SQLite schema, native config formats, credential behavior, gateway routes, and user-visible behavior stable.

This is not a line-count exercise. Extract only modules that hide meaningful behavior behind a smaller interface and pass the deletion test.

## Non-Goals

- No repository-wide rewrite.
- No behavior, branding, protocol, model, or config-format changes mixed into the refactor.
- No generic trait or port for a dependency with only one real adapter.
- No immediate rewrite of desktop-client binary patch implementations.
- No move-only commit that leaves the same tool matrix duplicated across callers.

## Target Backend Shape

    src-tauri/src/core/
      tool_catalog.rs
      profile/
        mod.rs
        manager.rs
        store.rs
        policy.rs
        provider_http.rs
        restart.rs
        native/
          mod.rs
          plan.rs
          document.rs
          codex.rs
          claude_desktop.rs
          claude_code.rs
          gemini_cli.rs
          gemini_code_assist.rs
          opencode.rs
          openclaw.rs
          hermes.rs
          grok.rs
          pi.rs

profile/mod.rs remains the stable facade used by Tauri command adapters.

## Primary Interfaces

### Tool Catalog

The ToolCatalog module owns canonical identity and capabilities:

    canonical_tool_id(value) -> ToolId
    tool(tool_id) -> ToolDefinition
    profile_capabilities(tool_id) -> ProfileCapabilities

It owns aliases, display name, category, executable/version metadata, install metadata, profile support, supported direct protocols, official protocol, and capabilities such as review model or model mappings. Platform-specific computed install commands remain implementation details.

All backend consumers use this module. Remove private canonicalization copies from profile, tool launch, environment health, and gateway after migration.

### Native Profile Adapter

Keep the interface intent-oriented and small:

    targets(context) -> NativeTarget[]
    plan(intent, input) -> NativePlan
    inspect(input) -> NativeInspection

- targets describes required native documents, including multi-file tools.
- plan handles direct apply, gateway apply, official restore, and managed-state removal.
- inspect handles detection, matching, and post-write verification from the same parsed knowledge.
- NativeInput supplies current documents and already-resolved gateway/credential values.
- Adapters do not access SQLite or the keychain.
- Parsing structures stay private to each adapter.

Shared orchestration selects an adapter once. It no longer maintains parallel app-id matches for path, render, cleanup, match, and verify.

### Profile Store And Manager

- ProfileStore is a concrete module over existing storage operations. It owns drafts, built-ins, ordering, active pointers, and persisted-profile migrations.
- ProfileManager owns save, update, duplicate, delete, preview, apply, switch, and native reconciliation.
- Do not introduce a ProfileStore trait initially. Add it only when a genuine second adapter is needed.
- Provider HTTP/model listing and restart process control are separate modules, not native-adapter methods.

## Target Frontend Shape

    src/lib/
      api/
        index.ts
        tauri.ts
        browserMock/
          index.ts
          state.ts
          profiles.ts
          tools.ts
          gateway.ts
      profiles/
        catalog.ts
        form.ts
        grouping.ts
        presentation.ts

    src/components/profiles/
      ProfileTypeSwitch.svelte
      ProfileToolTabs.svelte
      ProfileList.svelte
      ProfileCard.svelte
      ProfileEditDialog.svelte
      ProfileApplyDialog.svelte
      ProfileUsageDialog.svelte
      ProfileModelMappings.svelte

- Profiles.svelte becomes route composition and state coordination.
- SetupWizard.svelte and Profiles.svelte consume the same catalog and form modules.
- api/index.ts defines one frontend interface. The Tauri adapter and browser fake are the two real adapters.
- Keep a frontend catalog with a contract test against backend ids/capabilities initially. A runtime backend-provided catalog is optional later.

## Migration Waves

### Wave 0: Clean Baseline And Characterization

1. Finish and commit the current 1.4.0/Pi/ChatGPT/gateway work, or create a separate worktree from that exact commit.
2. Record baseline test/build output.
3. Add table-driven characterization tests for every profile tool and supported mode.
4. Capture redacted native write-plan fixtures for paths, actions, content, warnings, cleanup, and verification.
5. Add an architecture guard preventing new private canonical-tool-id functions outside the catalog.

Acceptance: the refactor starts from a reproducible green commit, not the current mixed worktree.

### Wave 1: Tool Identity And Capabilities

1. Turn tool_registry.rs into or replace it with tool_catalog.rs while preserving detector/installer callers.
2. Introduce ToolId without changing serialized string values.
3. Move aliases and profile capability metadata into the catalog.
4. Migrate profile, launch, detector, installer, environment health, and gateway call sites one at a time.
5. Add backend alias/capability matrix tests.
6. Extract frontend profiles/catalog.ts and migrate route-local maps and canonicalization.

Acceptance: aliases and capabilities have one owner per runtime, with backend/frontend parity tests. No route-local alias function remains.

### Wave 2: Native Plan Execution

1. Move NativeConfigWritePlan, lifecycle plan types, unchanged filtering, backup/write/delete execution, and verification scheduling into profile/native/plan.rs.
2. Keep existing tool render functions temporarily; change only the executor call path.
3. Test ordering, auth-before-config behavior, delete behavior, backups, unchanged filtering, and verification failures through the new plan interface.

Acceptance: profile orchestration applies one NativePlan; no direct filesystem write loop remains in profile.rs.

### Wave 3: Native Adapters

Migrate from lowest coupling to highest coupling:

1. Pi
2. Grok
3. Hermes
4. OpenClaw
5. OpenCode
6. Gemini CLI
7. Gemini Code Assist
8. Claude Code
9. Codex
10. Claude Desktop

For each tool:

1. Move path, render, official restore, gateway render, detection, matching, cleanup, preview, and verification knowledge together.
2. Add adapter-interface fixture tests.
3. Route the registry to the new adapter.
4. Delete old match arms and implementation-specific tests after interface tests cover observable behavior.
5. Run the full verification gate before migrating the next tool.

Codex migrates late because of auth.json, review models, OAuth preservation, and multiple clients. Claude Desktop migrates last because it is multi-file and platform-specific.

Acceptance per tool: its name disappears from shared native dispatch code; all native-format behavior is local to its adapter.

### Wave 4: Profile Store And Manager

1. Move built-ins, ordering, active-pointer cleanup, auto-activation, and CRUD persistence into profile/store.rs.
2. Move provider/mode/protocol validation into profile/policy.rs using catalog capabilities.
3. Move use-case orchestration into profile/manager.rs.
4. Move connection testing/model listing into profile/provider_http.rs.
5. Move restart target/process code into profile/restart.rs.
6. Keep profile/mod.rs as the compatibility facade.

Acceptance: profile/mod.rs contains exports and delegation, not tool formats, process scripts, HTTP parsing, or SQL orchestration.

### Wave 5: Frontend Domain And Adapters

1. Extract shared catalog, form validation, model mappings, grouping, active-state, and presentation helpers.
2. Split api.ts into interface, Tauri adapter, and browser fake without changing route-facing function names.
3. Move browser fake profile/native preview logic into browserMock/profiles.ts and tool lifecycle into tools.ts.
4. Extract Profiles dialogs and list/tool-tab UI only after domain rules are shared.
5. Replace source-text ownership tests with behavioral/import tests where possible.

Acceptance: Setup Wizard and Profiles share one capability/form implementation; Tauri and browser fake satisfy the same frontend interface.

### Wave 6: Gateway Protocol Conversion

Target shape:

    gateway/
      mod.rs
      runtime.rs
      server.rs
      route.rs
      auth.rs
      upstream.rs
      privacy.rs
      protocol/
        mod.rs
        canonical.rs
        openai_chat.rs
        openai_responses.rs
        anthropic.rs
        gemini.rs
        stream.rs

1. Extract the canonical request/assistant/tool/usage model first.
2. Define protocol adapters that decode client input and encode client output.
3. Move streaming conversion behind the same protocol ownership.
4. Separate raw HTTP/runtime concerns from routing and upstream transport.
5. Preserve routes, headers, privacy, streaming, usage, and error behavior with protocol matrix tests.

Acceptance: protocol changes do not require editing HTTP server runtime; server tests do not know protocol payload internals.

### Wave 7: Reassess Remaining Hotspots

Apply the deletion test to tool_installer.rs, detector.rs, chatgpt_desktop.rs, and claude_desktop_patch.rs.

- Extract install strategy adapters only where npm, winget, DMG, script, extension, and manual flows genuinely vary.
- Extract detector platform adapters only where Windows/macOS/Linux implementations share an interface.
- Keep specialized desktop-client implementations separate from profile modules.
- Never split solely to meet a line-count target.

## Verification Gate

Run after every wave and every native adapter:

    cargo fmt --check
    cargo test
    npm run test:unit
    npm run check
    npm run build
    git diff --check

Focused gates:

- Tool catalog alias/capability matrix.
- Native adapter fixtures for official/direct/gateway/detect/match/cleanup/verify.
- Cross-adapter redaction and credential tests.
- Temporary golden write-plan comparison during migration, removed with the old implementation.
- Frontend adapter contract tests for Tauri and browser fake.
- Browser verification using representative Codex, Claude Desktop, and Pi profiles.
- Gateway non-streaming and streaming matrix for OpenAI Chat, Responses, Anthropic Messages, and Gemini.

## Risk Controls

- Refactor only from a clean commit or dedicated worktree.
- One migration wave per commit or small commit series.
- Preserve serialized ids, config paths, provider ids, gateway URLs, headers, and error wording.
- Keep secrets redacted and keychain access outside pure adapter tests.
- Use exact path-based staging; exclude planning and local work-record files.
- Do not retain old/new implementations as permanent layers. Temporary comparison must be removed before a wave closes.

## Completion Criteria

- No tool-specific native-format logic remains in shared profile orchestration.
- Canonical ids and capabilities have clear owners and no route-local/backend-local copies.
- Each native adapter is tested through the same small interface.
- profile/mod.rs is a stable facade; ProfileManager and ProfileStore have distinct responsibilities.
- Setup Wizard and Profiles use shared profile-domain modules.
- Production Tauri access and browser fake satisfy one frontend interface.
- Gateway runtime and protocol conversion are separate deep modules.
- Full verification remains green after each migration wave.

