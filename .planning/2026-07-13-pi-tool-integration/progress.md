# Progress

## 2026-07-13

- Created a dedicated Pi integration plan and began auditing the live partial implementation.
- Initial search found substantial partial backend Pi support but no frontend registration or icon asset. Began tracing the native config contract and missing test coverage.
- Traced the intended Pi npm package and `models.json` provider schema, plus gateway, restore, detection, matching, verification, preview, and restart behavior.
- Compared the partial integration against Grok and enumerated the missing frontend, asset, documentation, and test surfaces.
- Ran baseline frontend tests/check successfully and recorded the missing-coverage problem; adjusted Windows search/test strategy after broad filters proved noisy.
- Resumed the existing plan from the partial-integration handoff, confirmed Phase 1 is still active, and re-audited the dirty worktree before making any Pi-specific edits.
- Verified the native Pi provider/model schema from the 0.80.6 npm package docs; the current minimal model representation is valid and all four configured API identifiers are upstream-supported.
- Reconfirmed that all current Pi references are backend-only, while Grok supplies a complete frontend parity template; retained the recorded Windows `rg` glob workaround for test-file discovery.
- Located the exact backend and frontend test homes for Pi parity and identified the native detector's early-return loop bug as the first implementation gap to drive with regression coverage.
- Found two additional real backend gaps: Pi is missing from `tool_installer::install_definition`, and launch canonicalization does not recognize Pi aliases.
- Completed Phase 1 audit and started Phase 2 regression coverage across Pi native profiles, lifecycle ownership, frontend parity, assets, localization, and documentation.
- Added failing-first regression coverage for Pi's four native APIs, provider scanning, official cleanup, gateway verification/cleanup, native paths/previews, installer/update/uninstall commands, detector metadata, launch aliases, and frontend/assets/locales/docs parity.
- Ran the failing-first suites: 15/19 Rust matches passed and the four expected backend gaps failed; frontend parity failed first on the missing Pi icon asset.
- Implemented the four backend fixes: safe provider scanning, direct Pi native preview, npm install/update support with `--ignore-scripts`, update process ownership, and launch alias canonicalization.
- Added Pi across the frontend mock lifecycle, official/direct/gateway previews, Setup Wizard, Profiles, icon/Panda styling, official SVG asset, three locale dictionaries, and both README language sections.
- Completed the Pi ownership audit after implementation; focused Rust tests, Pi frontend parity, Panda icon tests, and `svelte-check` are green, so Phases 2 and 3 are complete.
- Completed the full verification gate: Rust 346/346, frontend 157/157, focused Pi Rust 19/19, `npm run check`, `npm run build`, `cargo fmt --check`, and `git diff --check` all passed.
- Completed in-app browser verification at `http://127.0.0.1:1420/`: Pi appears in the 10-tool launcher and new-profile chooser, the development mock install uses the expected npm command, and the direct profile defaults to Responses while exposing all four supported protocols.
- Generated direct and gateway Pi previews. Direct mode targets `~/.pi/agent/models.json`; the first gateway profile becomes active automatically, and the gateway profile page exposes no apply-and-restart action.
- Verified the vendored Pi SVG loads successfully at 150x150 intrinsic size and renders at 36x36. Browser logs contain only Vite connection debug entries with no warnings or errors.
- Phase 4 is complete. No commit or push was requested; all unrelated dirty-worktree changes remain intact.
