# Findings

## Initial State

- Current branch is `main` at `1815eeb`; tracked worktree is clean.
- Existing untracked planning artifacts belong to prior completed work and must not be staged as product changes.
- Current config generation writes `requires_openai_auth = false` for gateway mode, and `true` for direct and official modes.
- The requested invariant is stronger than a config-only flag change: any managed provider set to `true` must have the selected API key available through Codex `auth.json`.

## Auth Boundary Evidence

- Native profile detection already reads `OPENAI_API_KEY` from Codex `auth.json`, and direct-profile matching compares that value with the selected profile secret.
- Gateway config generation and its Rust tests currently assert `requires_openai_auth = false`.
- The TypeScript mock preview mirrors the same split: official/direct show `true`, gateway shows `false`.
- Gateway preview/copy currently says only the local CodeStudio token is stored and the real upstream Provider key is never written to Codex config; after unifying auth, the distinction must be clarified as `auth.json` versus `config.toml`.
- Existing localized diff strings describe preserving `auth.json`, so preview and localization consumers need an audit alongside the backend write plan.
- The backend already models `auth.json` as `NativeConfigWriteKind::CodexAuthJson` and verifies the written file through `verify_codex_auth_json_write`; the fix should extend the existing plan boundary rather than add an independent write path.
- Relevant backend regions are the Codex auth helpers around lines 4513-4752, native plan construction around lines 5593-5704, plan application around lines 5875+, and write verification around lines 7324+.

## Confirmed Gap

- `build_native_apply_plan` currently adds a `CodexAuthJson` plan only when `mode == Config && is_custom_codex_oauth_profile(profile)`.
- Non-official Codex API profiles do not enter that branch, even though their generated provider entry uses `requires_openai_auth = true`.
- `verify_codex_auth_json_write` also returns success without checking content for every profile except a custom official OAuth profile.
- Therefore direct API profiles currently have no enforced guarantee that their selected key is present in `~/.codex/auth.json`; changing gateway to `true` without extending this path would reproduce the same defect there.
- The fix must support two auth payload sources: stored OAuth JSON for custom official profiles, and the selected profile secret for non-official API profiles.

## Required Credential by Mode

- Direct non-official Codex config must place the selected profile's stored Provider API key in `auth.json` because the provider entry uses `requires_openai_auth = true`.
- Gateway Codex config must place the tool-scoped local Gateway client token in `auth.json`; writing the real upstream Provider key would leak the wrong credential to the local relay boundary and would not match Gateway authentication.
- Official profiles should continue using their ChatGPT/OAuth auth JSON rather than synthesizing an API key.
- The auth writer should preserve unrelated OAuth fields when adding/replacing `OPENAI_API_KEY`, and official-profile behavior needs explicit regression coverage so switching modes does not silently destroy the login cache.

## Gateway Token Evidence

- `gateway::client_config_for_tool("codex")` returns the tool-scoped local base URL and the persisted `codestudio-local-*` Gateway token.
- The Gateway token is distinct from the active profile's upstream key; it is the correct value for Codex `auth.json` when the managed gateway provider requires OpenAI auth.
- The safest API-key payload update is to parse an existing JSON object, replace only the root `OPENAI_API_KEY`, and retain OAuth/token metadata and other unknown fields.
- The write verifier must compare the resulting root API key against the expected direct or gateway credential instead of returning unconditional success.

## Regression History

- Before commit `73c6bf4`, Codex direct/gateway profiles carried credentials in `config.toml` through `experimental_bearer_token`; gateway also used `requires_openai_auth = true`.
- Commit `73c6bf4` removed plaintext/bearer tokens from `config.toml` and changed gateway auth to `false`, but did not replace API profile credentials with an `auth.json` write path.
- Later code restored `requires_openai_auth = true` for direct profiles, leaving the credential source missing. This is the concrete regression the user reported.
- The fix must not restore `experimental_bearer_token`; secrets should remain absent from `config.toml` and be supplied only through `auth.json`.
- Auth plans should be ordered before the provider config plan so a newly written `requires_openai_auth = true` config is never activated before its auth value is available.

## Verification Weakness

- `codex_direct_config_matches_profile_with_secret_match` currently uses `unwrap_or(true)` when `auth.json` is unavailable, so a direct profile can be reported as matching even though its required auth key is missing.
- With the new invariant, direct-profile matching should require an `auth.json` key and verify it against the selected profile secret/reference.
- Gateway native verification currently explicitly expects `requires_openai_auth == false`; it must be changed to `true`, while the separate auth write verifier checks the local Gateway token.

## Compatibility Boundary

- The Gateway currently allows tokenless requests only on the Codex-scoped compatibility route; other routes still require the local token.
- Writing the local token to `auth.json` lets new managed configs use normal Bearer auth while retaining the existing tokenless Codex route as a fallback for older configs/clients.
- Startup matching intentionally avoids loading keychain plaintext through `SecretMatchMode::KeychainReference`; tests can preserve that boundary while still requiring an `auth.json` key to be present.

## Official Codex Configuration Evidence

- OpenAI's current Codex configuration reference defines `cli_auth_credentials_store = file | keyring | auto` as the control for file-based `auth.json` versus OS keychain credential storage.
- The same reference defines `model_providers.<id>.requires_openai_auth` as using OpenAI authentication and discourages direct `experimental_bearer_token` configuration.
- Therefore managed profiles that write an API key or Gateway token to `auth.json` must also select `cli_auth_credentials_store = "file"`; otherwise an explicit keyring setting can make the file write ineffective.
- Existing JSON fields will be preserved while `OPENAI_API_KEY` is replaced, so file-backed OAuth metadata survives direct/gateway switches when it was already present in `auth.json`.

## Local Auth JSON Shape

- A non-secret inspection of the current machine's `~/.codex/auth.json` shows a single root `OPENAI_API_KEY` field, with no `auth_mode` and no OAuth token object.
- This confirms the managed API-key payload should use the root uppercase field and does not need to synthesize `auth_mode`.
- The merge helper will set `auth_mode = "apikey"` and update `OPENAI_API_KEY`; existing OAuth `tokens` and unknown fields remain unchanged so official auth can be restored later.

## Codex-Generated API Key Format

- An isolated run of installed `codex-cli 0.144.0` with `CODEX_HOME` redirected to a temporary directory generated `auth.json` with `auth_mode = "apikey"` and root `OPENAI_API_KEY`.
- Managed API-key and Gateway writes should follow that exact format, replacing stale lowercase/legacy key aliases while preserving unrelated fields and OAuth tokens.
- When applying an official profile and preserved OAuth tokens are present, the writer can safely restore `auth_mode = "chatgpt"` and remove API-key fields; if no OAuth markers exist, it should leave the existing auth file unchanged.
- `infer_codex_auth_method` should honor explicit `auth_mode` before structural markers so a merged file with preserved OAuth tokens still reports the active API-key mode correctly.

## Updated Managed Provider Contract

- The latest requirement supersedes the earlier `requires_openai_auth = true` invariant: all managed official, direct, and gateway provider entries must now write `false`.
- Key/Token handling does not change: direct Provider keys and local Gateway tokens still belong in `auth.json`, never in `config.toml`.
- Every managed provider entry must include an inline `http_headers` table immediately after `requires_openai_auth`, with `x-openai-actor-authorization = "codestudio-lite"`.
- Config generation, config verification, backend previews, mock previews, localization, and tests must all describe and enforce the same contract.
- Empty Codex configs need an explicitly initialized `model_providers` table; otherwise `toml_edit` can render all provider fields as one root inline dotted table instead of the requested adjacent lines.
- Write verification must compare parsed Base URL strings directly. Applying display redaction before comparison incorrectly rejects valid hosts such as `api.apikey.fun`.
- Active-profile recognition accepts legacy managed provider entries with `requires_openai_auth = true`, while newly written configs and strict post-write verification require `false` plus the actor header.
