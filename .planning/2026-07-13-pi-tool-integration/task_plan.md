# Complete Pi Tool Integration

Goal: finish the partially implemented Pi tool integration across backend, profiles, gateway, frontend, assets, and tests without disturbing unrelated local work.

### Phase 1: Audit Partial Integration
**Status:** complete

- [x] Identify every current Pi-related edit and missing integration surface.
- [x] Compare Pi against the closest existing tool adapters and define its native config contract.
- [x] Record compatibility, install, launch, profile, and gateway expectations.

### Phase 2: Add Regression Coverage
**Status:** complete

- [x] Add focused backend coverage for detection, install/launch, native config, and gateway behavior.
- [x] Add frontend/static coverage for registry, icon, profile forms, previews, and localization.

### Phase 3: Complete Implementation
**Status:** complete

- [x] Finish backend registry, detector, installer, launcher, profile, and gateway integration.
- [x] Finish frontend registry, mock API, UI, localization, and assets.
- [x] Keep all existing unrelated dirty-worktree changes intact.

### Phase 4: Verify
**Status:** complete

- [x] Run focused and full Rust/frontend tests, checks, formatting, and production build.
- [x] Verify Pi flows in the local development app and check browser console output.

## Errors Encountered

| Error | Attempt | Resolution |
|---|---|---|
| `rg` against `src/lib/*.test.mjs` returned Windows path error 123 | 1 | Use `rg --files src/lib` or search the directory directly with a test-file glob filter. |
| Cargo filter `pi` also matches many `api` test names | 1 | Use explicit Pi test names after adding coverage; first confirm the current cargo process has finished. |
| A compound `rg` regex for Pi functions was parsed as an unclosed group through PowerShell | 1 | Split the lookup into simple fixed search terms before reading exact line ranges. |
| A follow-up test search accidentally retained an explicit `src/lib/*.mjs` path glob and hit error 123 again | 2 | Search `src/lib` only and apply `--glob "*.test.mjs"`; do not pass wildcard paths on Windows. |
| A quoted compound `rg` lookup for mock function locations was again parsed as an unclosed group | 2 | Stop composing alternations in one PowerShell argument; use separate fixed-string `rg -F` calls in parallel. |
| Failing-first Pi Rust suite: provider scan returned `None`, direct preview hit `unreachable!()`, installer reported `manual`, and launch aliases stayed uncanonicalized | 1 | Implement the four audited backend gaps, then rerun the same focused suite. |
| Failing-first Pi frontend ownership test stopped at missing `public/tool-icons/pi.svg` | 1 | Vendor the official Pi SVG and complete all asserted frontend ownership surfaces. |
| Sandboxed `Invoke-WebRequest` to `https://pi.dev/logo-auto.svg` failed authentication | 1 | Retried the same read with approved network access and retrieved the official SVG successfully. |
| Combined locale patch failed because existing zh-TW Hermes wording used a different character form | 1 | No partial changes were applied; read exact locale context and split the locale edits per file. |
| Frontend rerun left one Panda regression failure and one generated recipe type error | 1 | Keep Pi heading at 28px to satisfy the stable VS Code sizing guard, then run the existing `npm run prepare` Panda codegen. |
| Panda codegen's optional update check could not access the user config store | 1 | Code generation itself completed successfully; treat the update-check notice as non-blocking and verify generated types through `npm run check`. |
| Full verification found Rust formatting differences in Pi-related snippets | 1 | Run the crate's standard `cargo fmt`, then rerun format and focused behavior checks. |
| PowerShell parsed a quoted fixed-string Pi lookup as a path | 1 | Read the known `mockDetection` line range directly instead of repeating the fragile quoted lookup. |
| The in-app browser locator wrapper does not expose `evaluateAll` or `inputValue` | 1 | Use the supported locator-level `evaluate` method to inspect the protocol select element. |
