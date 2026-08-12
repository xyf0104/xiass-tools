# Findings

- Latest upstream baseline: BigPizzaV3/CodexPlusPlus commit `285f40e4d19ba90c6571f56afc1caefe19798eac` from 2026-07-16.
- Current CodeStudio Lite provider sync rewrites rollout provider metadata and updates `threads.model_provider`, `threads.has_user_event`, and `threads.cwd`.
- Missing parity areas confirmed before implementation: projectless filtering, global-state workspace normalization, SQLite/WAL/SHM backups, encrypted-content warnings, explicit/discovered provider targets, structured non-blocking outcomes, and guarded session-index cleanup.
- Current local sync module has 2 unit tests; upstream provider-sync suite has 22 passing integration tests, including 18 main-sync tests and 4 session-index tests.

