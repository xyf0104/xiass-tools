# Findings

- The exact apply-and-restart message is emitted when the generated restart script reports `remaining > 0` after force termination.
- Profile restart waits only 300 ms after `Stop-Process -Force`; shared AppX close waits a fixed 1500 ms and has no process-tree fallback.
- ChatGPT Desktop launch uses the shared AppX close path, explaining the similar launch-button error.
- Codex restart currently launches each target immediately before stopping later targets, so a newly launched app-server can overlap subsequent backend cleanup.
- The fix shares one termination tail across both paths: `Stop-Process -Force`, `taskkill /T /F` fallback, and bounded polling for actual process exit.
- Stopping all matched targets before launching any replacement avoids terminating a newly started ChatGPT app-server during later target cleanup.
