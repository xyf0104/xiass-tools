# Progress

- Restored the prior PowerShell restart fix context and inspected current Codex package process topology.
- Traced both UI symptoms to the profile restart script and shared AppX process termination code.
- Added a harmless copied-process runtime regression that reproduces the `remaining > 0` branch when primary termination fails.
- Implemented shared `taskkill /T /F` fallback plus bounded post-force polling; the runtime regression now passes.
- Changed profile restart ordering to stop every matched target before launching replacements.
- Verified 356 Rust library tests, Cargo check/format, 167 frontend unit tests, frontend checks, explicit diff checks, and repeated Windows runtime fallback execution.
- Intentionally skipped terminating the real Codex/ChatGPT process tree because it hosts this task.
