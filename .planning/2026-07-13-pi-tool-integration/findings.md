# Findings

## Initial Constraints

- Pi integration is already partially present in the live dirty worktree; inspect and extend it rather than restarting or reverting.
- Preserve unrelated ChatGPT Desktop, gateway, profile, review-model, Grok, restart, and download changes.
- No commit or push was requested.

## Initial Audit

- Pi currently exists in backend tool registry and large portions of `profile.rs`: canonical aliases, official/custom/gateway config routing, restart targets, native path, detection, matching, verification, previews, and cleanup are partially present.
- Gateway aliases and display naming already recognize `pi`, `pi-agent`, and `pi-coding-agent`; detector metadata points at npm package `@earendil-works/pi-coding-agent`.
- No Pi frontend/tool icon surface was found under `src/` or `public/tool-icons`; this is the largest obvious missing layer.
- The live worktree also contains unrelated Grok, review-model, ChatGPT Desktop, gateway, restart, download, and release/version edits; do not revert or overwrite them.

## Backend Contract Found

- Tool registry defines `pi` / `Pi Agent`, command `pi`, version flag `--version`, config path `~/.pi/agent/models.json`, and npm install command `npm install -g --ignore-scripts @earendil-works/pi-coding-agent`.
- Detector/update metadata recognizes the same npm package and updates it with `@latest`.
- Native custom config uses a managed provider under `providers.codestudio`, with `baseUrl`, `api`, `apiKey`, `compat`, and a one-entry `models` array.
- Gateway config uses the tool-scoped local gateway URL/token, API mode `openai-completions`, and tells the user to select the injected model through Pi's `/model` command.
- Official restore removes only CodeStudio-managed Pi provider entries; detection skips local gateway providers and imports the first external provider/model.
- Restart targets, gateway aliases, user-agent inference, and display labels already include Pi.
- The Rust built-in Pi official profile intentionally uses `anthropic-messages`; frontend built-in definitions must mirror that protocol even if the custom-profile form defaults to `openai-responses`.
- The gateway client factory canonicalizes all Pi aliases and produces the expected tool-scoped URL ending in `/tools/pi/v1`.

## Upstream Pi Contract Verification

- Verified against `@earendil-works/pi-coding-agent` 0.80.6 package documentation from the downloaded npm tarball.
- `~/.pi/agent/models.json` custom providers require `baseUrl`, an `api` value at provider or model level, and model entries with `id`; model `name` is optional and defaults to the id.
- Provider-level literal `apiKey` is supported and makes the custom model available without a separate `/login` entry.
- The implemented API identifiers match Pi's documented values: `openai-completions`, `openai-responses`, `anthropic-messages`, and `google-generative-ai`.
- Pi reloads `models.json` whenever `/model` opens, so profile application itself does not require a restart; retaining Pi as a restart-capable tool is still useful for the application's explicit apply-and-restart workflow.

## Frontend Gaps

- The freshly completed Grok integration provides the closest frontend template.
- Pi is missing from `builtinOfficialProfileDefinitions`, mock detection/status, preferred active-app order, canonical alias mapping, restart labels, config path mapping, native config previews, and mock gateway previews.
- Pi is missing from Setup Wizard tool choices/default profile naming and Profiles tool order/display/warning translation mapping.
- `ToolIcon.svelte`, Panda icon tones, `public/tool-icons`, all three locale dictionaries, README tool lists, and frontend ownership tests have no Pi entry.
- There are no Pi-specific Rust tests in `profile_tests.rs` yet, despite extensive implementation code.

## Frontend Mock Integration Map

- `src/lib/api.ts` needs Pi in mock versions/updates, built-in official definitions, detected tool status, install definitions, preferred active-app order, both config-path resolvers, direct and gateway native previews, Config protocol validation, canonical aliases, restart labels, and tool config paths.
- Direct Pi profiles support all four upstream protocols. Gateway profiles should preview a managed provider using `openai-completions`, the tool-scoped gateway URL/token, and the selected virtual model, with `/model` selection guidance.

## UI Integration Map

- Setup Wizard and Profiles should place Pi after Grok, default to `openai-responses`, and expose all four Config protocols.
- UI canonicalization and icon lookup should recognize `pi`, `pi-agent`, and `pi-coding-agent` as the same tool.
- `ToolIcon.svelte` and Panda need a dedicated Pi SVG/tone with stable card, choice, and heading dimensions; existing Grok styling should remain unchanged.

## Asset And Copy Audit

- The npm package does not ship a Pi logo asset. Upstream metadata/README points to the official `https://pi.dev/logo-auto.svg`, which should be vendored into `public/tool-icons/pi.svg`.
- Each locale needs a Pi built-in official name, direct-config warning, `/model` selection/reload guidance, and default custom-profile name.
- Both Chinese and English README introductions and supported-tool lists currently stop at Grok and need Pi Agent added.

## Backend Gap Found During Resume

- `detect_pi_native_profile` currently uses early-return `?` operators inside the provider loop. A keyless, incomplete, local-gateway, or unsupported provider encountered before a valid external provider can terminate detection instead of being skipped; regression coverage should require scanning onward to the first valid importable provider.
- The existing `profileManagementOwnership.test.mjs` is an appropriate home for a Pi frontend ownership/parity test because it already reads the profile forms, mock API, locales, Rust types, and Panda configuration as one contract.
- Rust direct native preview warnings/path support mention Pi, but the Config-mode change builder has no `"pi"` match arm and falls into `unreachable!()`; direct preview must emit provider `baseUrl`, `api`, masked `apiKey`, and model changes.
- Frontend mock official/direct preview branches also need explicit Pi handling so browser preview behavior mirrors the backend instead of returning `null`.

## Test Placement

- `profile_tests.rs` already groups the relevant ownership boundaries: official non-Codex matching, native-config roundtrips, gateway model emission, native config paths, cleanup, previews, built-in official protocols, and restart targets.
- Pi regression tests can be added alongside those existing tests without creating a new test harness, while frontend/static assertions can cover tool registry, detector update metadata, generic installer/launcher ownership, mock API, UI forms, asset wiring, README, and all locale dictionaries.

## Tool Lifecycle Audit

- The Rust registry entry is complete: `pi`, `Pi Agent`, command/version probe `pi --version`, config path `.pi/agent/models.json`, and the upstream npm install command with `--ignore-scripts`.
- Detector metadata resolves the npm package and update command with `@latest`.
- The registry alone is not sufficient for installation: `tool_installer::install_definition` still needs an explicit `InstallAction::NpmGlobal` arm. Pi currently has no arm, so real install/update planning rejects it despite the registry entry.
- Generic launching can use the registry command directly, but `tool_launch::canonical_profile_app` does not yet canonicalize `pi-agent` / `pi-coding-agent`; aliases can otherwise lose Pi profile ownership in the launch panel.

## Baseline

- Frontend baseline remains green at 156/156 and `svelte-check` reports zero diagnostics because no current test requires Pi parity.
- The repository version has concurrently moved to `1.4.0`; preserve that release change.

## Browser Verification

- The development launcher tracks 10 application tools and renders Pi Agent with the vendored icon, missing/installed state, install command, launch action, and new-profile shortcut.
- The new-profile chooser includes Pi Agent. After the development mock install, Pi can be selected without touching the real machine installation.
- Pi direct profiles expose `openai-chat-completions`, `openai-responses`, `anthropic-messages`, and `google-gemini`; the default selection is `openai-responses`.
- A valid direct preview stores only the keychain reference and identifies `~/.pi/agent/models.json` as the later native configuration target.
- A valid gateway preview is generated for Pi, and saving the first gateway profile automatically selects it as the current gateway profile. The resulting profile card has no apply-and-restart action.
- `public/tool-icons/pi.svg` loads completely with a 150x150 intrinsic size and a 36x36 rendered size in the profile tool tab.
- The browser diagnostic log contains only Vite connect/connected debug entries; no warning or error entries were emitted during the Pi workflow.
