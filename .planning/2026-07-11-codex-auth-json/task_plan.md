# Codex Auth JSON Consistency

Goal: keep managed Codex credentials in `auth.json` while every generated provider uses `requires_openai_auth = false` and the CodeStudio Lite actor-authorization header.

### Phase 1: Trace Config and Auth Write Plans
**Status:** complete

- [x] Map direct, official, and gateway `config.toml` generation.
- [x] Map `auth.json` write-plan creation, preservation, cleanup, and application.
- [x] Identify any preview/mock paths that must stay consistent with actual writes.

### Phase 2: Implement the Invariant
**Status:** complete

- [x] Set managed Codex provider entries to `requires_openai_auth = true`.
- [x] Ensure non-official profiles with API keys write the expected `auth.json` payload.
- [x] Preserve official ChatGPT authentication and unrelated Codex CLI behavior.

### Phase 3: Verify
**Status:** complete

- [x] Add focused Rust regression tests for direct and gateway write plans.
- [x] Run targeted profile tests and relevant frontend static/unit tests.
- [x] Run formatting, compile checks, and final diff inspection.

### Phase 4: Apply the Updated Provider Contract
**Status:** complete

- [x] Set official, direct, and gateway Codex providers to `requires_openai_auth = false`.
- [x] Add `http_headers = { "x-openai-actor-authorization" = "codestudio-lite" }` immediately after the auth flag.
- [x] Keep existing Key/Token writes to `auth.json` and keep credentials out of `config.toml`.
- [x] Synchronize backend previews, frontend mock previews, localization, and verification.

### Phase 5: Re-verify the Updated Contract
**Status:** complete

- [x] Run focused Rust and frontend regression tests.
- [x] Run full Rust/frontend checks and production build.
- [x] Run formatting and final diff/residual scans.

## Errors Encountered

| Error | Attempt | Resolution |
|---|---|---|
| PowerShell `rg` rejected `src/lib/*.test.mjs` as a path | Broad test search | Switched to explicit files/directories and native `rg` glob options. |
| Codex manual helper rejected the response because `x-content-sha256` was missing | Official behavior lookup | Used the official OpenAI Codex configuration reference as the documented fallback. |
| Sandboxed `Invoke-WebRequest` failed TLS authentication | Official behavior lookup | Retried the same official-domain read with approved network access. |
| Sandbox denied creating the isolated Codex home under `C:\tmp` | API-key auth format probe | Retried under the session's allowed system temporary directory. |
| Initial large regression-test patch missed one existing assertion context | Add red tests | No product/test changes were applied; split the patch by function boundary and re-read exact slices. |
| Initial mock-preview patch assumed a different `before` value | Sync frontend preview | No frontend changes were applied; re-read the exact preview array and patched by stable neighboring keys. |
| Localization patch assumed different existing Chinese wording | Sync auth preview copy | No localization changes were applied; searched exact locale values and replaced them by stable keys. |
| Residual auth scan regex was malformed by PowerShell quoting | Check for stale `false` branches | Replaced the combined escaped regex with separate single-quoted patterns. |
| First stale-copy cleanup patch assumed different Chinese translations | Remove dead `false` auth copy | Located the entries by translation key and removed the exact lines. |
| First implementation-status plan patch used mismatched context | Update phase status | Re-read the active plan and applied smaller exact hunks. |
| Completion checker initially reported no recognized phases | Validate plan completion | Changed phase headings and status markers to the skill's required format, then reran the checker. |
| Final line-number regex was malformed by PowerShell quoting | Gather handoff references | Switched to separate fixed-string searches. |
| Initial combined contract-test patch matched an imprecise legacy fixture context | Update regression tests | No test changes were applied; split the patch by helper and individual test function. |
| Empty Codex configs rendered `model_providers` as one root inline dotted table | Targeted Rust tests | Changed provider-table normalization to initialize a real TOML table even when the original config has no `model_providers` entry. |
| Direct config verification redacted `api.apikey.fun` before comparing the Base URL | Targeted Rust tests | Kept redaction in previews only and compared parsed TOML Base URLs directly during write verification. |
