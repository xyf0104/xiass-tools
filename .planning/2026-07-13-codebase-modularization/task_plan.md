# Codebase Modularization Plan

Goal: produce an evidence-based modification plan that turns tool-specific profile and lifecycle logic into deep modules with small interfaces, without changing product behavior or disturbing unrelated work.

### Phase 1: Inventory Architecture And Coupling
**Status:** complete

- [x] Measure the largest backend and frontend files and identify tool-specific branch clusters.
- [x] Trace profile creation, preview, apply, detection, cleanup, gateway, install, update, and launch call paths.
- [x] Identify duplicated tool knowledge and current test seams.

### Phase 2: Design Deep Modules
**Status:** complete

- [x] Define the smallest useful interfaces and their seams.
- [x] Separate shared orchestration from tool adapters without adding pass-through layers.
- [x] Define ownership for aliases, protocols, config paths, previews, native writes, cleanup, and verification.

### Phase 3: Plan Safe Migration
**Status:** complete

- [x] Break the refactor into behavior-preserving, independently testable stages.
- [x] Define compatibility, regression, and rollback gates for each stage.
- [x] Account for the current dirty worktree and avoid mixing architecture work with active feature changes.

### Phase 4: Deliver Modification Plan
**Status:** complete

- [x] Record target directory structure and module responsibilities.
- [x] Record ordered implementation steps, risks, and acceptance criteria.
- [x] Review the plan against codebase-design depth, leverage, locality, and deletion-test criteria.

### Phase 5: Establish Implementation Baseline
**Status:** complete

- [x] Record the current green Rust/frontend/build baseline before structural edits.
- [x] Preserve all existing feature changes and constrain migration diffs to owned modules.
- [x] Add implementation-oriented architecture guards where current tests only inspect old file locations.

### Phase 6: Implement Tool Catalog
**Status:** complete

- [x] Centralize backend canonical ids, aliases, and profile capabilities.
- [x] Migrate backend profile, launch, detector, installer, environment-health, and gateway consumers.
- [x] Extract shared frontend profile catalog and migrate Setup Wizard, Profiles, and browser mock consumers.

### Phase 7: Implement Native Plan And Adapters
**Status:** complete

- [x] Extract native plan execution.
- [x] Migrate every tool native adapter from Pi through Claude Desktop.
- [x] Replace implementation tests with adapter-interface coverage.

### Phase 8: Implement Profile Modules
**Status:** complete

- [x] Extract profile store and policy ownership.
- [x] Extract profile manager, provider HTTP, and restart ownership.
- [x] Reduce profile/mod.rs to the stable facade.

### Phase 9: Implement Frontend Modules
**Status:** completed

- [x] Split Tauri and browser fake adapters behind one frontend interface.
- [x] Extract shared profile domain logic.
- [x] Extract the profile tool-tab module and move its styling ownership tests to the new owner.
- [x] Extract the profile card module with display, capability, busy-state, and action ownership.
- [x] Extract focused profile UI modules and keep routes as composition modules.

### Phase 10: Implement Gateway Modules
**Status:** complete

- [x] Extract the canonical protocol request, response, content, tool, and usage model.
- [x] Extract local bearer authentication and scoped Codex loopback policy.
- [x] Extract tool-scoped route resolution and client/tool inference.
- [x] Extract protocol-aware privacy filtering and metadata decisions.
- [x] Extract route-to-protocol selection and Gemini route parsing.
- [x] Extract upstream endpoint and versioned Base URL construction.
- [x] Extract upstream authentication headers and safe passthrough policy.
- [x] Extract the OpenAI Chat request decoder into its protocol adapter.
- [x] Extract the OpenAI Responses request decoder into its protocol adapter.
- [x] Extract the Anthropic Messages request decoder into its protocol adapter.
- [x] Extract the Gemini request decoder into its protocol adapter.
- [x] Extract canonical request encoding for all four upstream protocols.
- [x] Extract upstream response decoding into the canonical protocol interface.
- [x] Extract canonical response encoding for all four client protocols.
- [x] Extract protocol-independent SSE frame buffering and parsing.
- [x] Extract canonical stream-update composition behind the stream interface.
- [x] Extract protocol-specific streaming text-delta decoding.
- [x] Extract protocol-specific streaming tool-call decoding and delete the old cluster.
- [x] Extract streaming usage decoding, merging, and validity rules.
- [x] Extract client-stream identity and lifecycle state.
- [x] Extract client stream-start encoding and delete the old implementation.
- [x] Extract client text-delta encoding and delete the old implementation.
- [x] Extract client tool-call stream encoding and delete the old implementation.
- [x] Extract client stream completion and SSE framing, then delete the old implementation.
- [x] Separate runtime/server, route/auth, upstream, privacy, and protocol conversion.
- [x] Separate streaming conversion by protocol.
- [x] Preserve the full protocol and route matrix.

### Phase 11: Reassess Remaining Hotspots
**Status:** complete

- [x] Apply the deletion test to installer, detector, ChatGPT Desktop, and Claude Desktop patching.
- [x] Extract only real strategy/platform seams.
- [x] Add architecture guards for the resulting ownership.

### Phase 12: Verify And Audit Completion
**Status:** complete

- [x] Run full Rust/frontend tests, checks, formatting, and production build.
- [x] Run browser verification for representative profile and gateway flows.
- [x] Prove every modification-plan completion criterion from current-state evidence.

## Errors Encountered

| Error | Attempt | Resolution |
|---|---|---|
| Recursive skill discovery across multiple protected roots was rejected by filesystem ACL handling | 1 | Inspect known skill roots individually and read the discovered `codebase-design` skill directly. |
| A combined `rg` alternation for backend match-arm searches was parsed as an unclosed group through PowerShell | 1 | Use separate fixed-string searches and targeted line ranges instead of repeating the compound expression. |
| The first large add-file patch omitted one required `+` marker inside a code block | 1 | Generate the add-file patch by prefixing every content line programmatically before calling `apply_patch`. |
| Initial backend catalog format check reported standard rustfmt layout differences | 1 | Run the crate's standard `cargo fmt`, then rerun `cargo fmt --check`. |
| Cargo rejected two independent test-name filters in one invocation | 1 | Run each focused filter separately or use one broader module filter. |
| Large NativePlan move patch failed exact-context verification | 1 | Split the extraction into independent add/remove patches with focused verification. |
| NativePlan tests assumed the unavailable `tempfile` crate | 1 | Use process-id/timestamp-scoped temporary directories from the Rust standard library. |
| A compound Pi `rg` expression was parsed as an unclosed group by PowerShell | 1 | Use separate fixed-string searches and targeted source ranges for adapter inventory. |
| Initial Pi move range included the next function declaration but not its body | 1 | Remove the stray declaration from `pi.rs`, restore it before the remaining parameters in `profile.rs`, then compile before further routing changes. |
| Combined Hermes symbol-range extraction matched repeated end markers and produced array line numbers | 1 | Do not apply the generated patch; extract each Hermes function cluster independently with `Select-Object -First 1` or exact line ranges, then combine only verified blocks. |
| Gemini Code Assist deletion search used a repository-root path while the command already ran from `src-tauri` | 1 | Rerun fixed-string searches from the repository root; focused tests had still completed successfully. |
| First Claude Desktop path extraction failed `cargo fmt --check` on standard wrapping | 1 | Run the repository formatter, rerun both focused tests, then delete the two obsolete shared constants reported by the compiler. |
| Claude Desktop model extraction initially hid two functions behind `cfg(test)` although `gateway.rs` is a production caller | 1 | Promote the selectors to explicit `profile` facade re-exports; keep the module as the single implementation owner and defer direct gateway dependency cleanup to the Gateway wave. |
| Claude Desktop 3P render extraction initially removed model helpers still used by the legacy preview implementation | 1 | Re-export the module-owned helpers through the profile facade temporarily; remove the imports when full Claude Desktop preview ownership moves. |
| A small import patch inserted the metadata test alias after the test module declaration because its context was too broad | 1 | Inspect the exact tail immediately, move the alias into the `cfg(test)` import block, delete the stray line, then rerun formatting and compilation. |
| Claude Desktop detection verification initially used a nonexistent broad test filter and then read a repository-root path from the `src-tauri` working directory | 1 | Read `src/core/profile_tests.rs` from the actual working directory, identify the exact regression test name, and rerun that focused test plus no-run compilation. |
| Claude Desktop verification inventory search repeated the same repository-root-relative path while already in `src-tauri` | 1 | Let the verification commands continue, then use the correct `src/core/...` path for any follow-up search; the 96-test Claude Desktop filter provided stronger coverage. |
| Initial ProfileStore extraction made `load_profile_by_id` private through a glob import even though two sibling modules use the profile facade | 1 | Add an explicit public-crate facade wrapper and reduce the store function to profile-module visibility; keep the implementation only in `store.rs`. |
| Generated Provider HTTP add-file patch double-prefixed moved lines with `+`, leaving invalid Rust source | 1 | Remove one literal prefix from every affected line using an exact whole-file patch, then format and compile. |
| Provider HTTP extraction range included adjacent `switch_active_profile` manager orchestration and hid test helpers | 1 | Temporarily re-export the public switch function, expose URL/payload helpers only inside profile tests, and require moving switch orchestration to `manager.rs` before Phase 8 closes. |
| First manager preview/apply move could not find its end marker because the full `profile.rs` read was truncated | 1 | Read the exact current line interval from the two function markers and generate the patch from that bounded output. |
| Preview tests initially compiled against the pre-fix manager helper visibility while a previous command session was still running | 1 | Make the helper profile-visible, import it only under `cfg(test)`, then run a fresh focused verification session. |
| Restart extraction initially left all module types/functions private, so manager and strategy tests could not cross the new seam | 1 | Mark the restart outcome/context/target interface and tested script builder profile-visible, including required fields, then rerun focused tests. |
| Frontend runtime seam caused a source-ownership test to keep searching the old backend facade for manager logic | 1 | Point the test at `profile/manager.rs`, preserving the behavior assertion while respecting the new implementation owner. |
| Profile grouping extraction left one old active-state call and a Pi ownership assertion tied to the route | 1 | Route both through `profiles/grouping.ts` and make the ownership test inspect the new implementation owner. |
| Whole-cluster deletion patch for browser native preview exceeded the tool output/matching window | 1 | Keep the verified new module active, then delete the old implementation in bounded function clusters rather than one 56 KB patch. |
