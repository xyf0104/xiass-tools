# Codex Restart Still Running

Goal: make profile apply-and-restart and the ChatGPT Desktop launch button reliably terminate Codex package processes without false `Codex is still running` failures.

## Phase 1: Reproduce and isolate
Status: complete

- [x] Trace both error producers to profile restart and shared Windows process control.
- [x] Add a harmless runtime test that simulates primary termination failure.

## Phase 2: Implement
Status: complete

- [x] Add a process-tree force fallback and bounded post-force wait.
- [x] Make profile restart stop all targets before launching replacements.
- [x] Keep current ChatGPT and Codex CLI process boundaries intact.

## Phase 3: Verify
Status: complete

- [x] Run focused Rust tests and Windows runtime regression.
- [x] Run full unit/check validation and review the diff.

## Errors Encountered

| Error | Attempt | Resolution |
|---|---|---|
