# Claude Code user-settings manager

This internal package follows Claude Code's documented user-settings model:

- Claude Code reads a user-level `settings.json`; this package resolves only
  `CLAUDE_CONFIG_DIR/settings.json` when `CLAUDE_CONFIG_DIR` is set, otherwise
  `~/.claude/settings.json`.
- The documented `env` object applies environment variables to Claude Code
  sessions. This package writes only `ANTHROPIC_BASE_URL` and
  `ANTHROPIC_AUTH_TOKEN` there.
- The documented top-level `model` setting selects the default model for new
  sessions. It is an initial selection, not an account or policy mechanism.

It deliberately does not read or change `~/.claude.json`, `.credentials.json`,
transcripts, project settings, local settings, managed settings, OAuth state,
quota, 2FA, or accounts. `ANTHROPIC_AUTH_TOKEN` is an input only: snapshots,
results, manifests, and errors never contain it.

New backups use XIASS Tools-owned directories and filenames. Known legacy
Antigravity WF Assistant backup directories are discovered read-only; an
explicit migration copies a verified backup to the XIASS Tools store and never
deletes or rewrites the legacy source.

Official references:

- https://code.claude.com/docs/en/settings
- https://code.claude.com/docs/en/env-vars
- https://code.claude.com/docs/en/model-config
