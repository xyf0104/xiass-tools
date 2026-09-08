# XIASS Tools workspace notes

- This repository is derived from CodeStudio Lite and keeps the upstream MIT license and attribution.
- The product-facing name is `XIASS Tools`.
- Preserve upstream behavior unless a requested XIASS or Antigravity WF integration requires a scoped change.
- Keep credentials local, redact secrets from logs, and bind any helper service to loopback only.
- Prefer additive, reversible integrations with explicit cleanup on app exit.
- The Antigravity WF integration is sourced from the sibling `Antigravity-WF-Assistant-GitHub` project; do not modify that sibling while porting functionality here.
