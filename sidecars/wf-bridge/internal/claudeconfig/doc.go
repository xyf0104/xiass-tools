// Package claudeconfig manages only the documented Claude Code user settings
// file. It intentionally does not inspect credentials, account state,
// transcripts, project settings, local settings, managed settings, OAuth, or
// quota data.
//
// Claude Code documents the top-level "model" setting and the "env" object in
// settings.json as user-level configuration. This package owns only
// env.ANTHROPIC_BASE_URL, env.ANTHROPIC_AUTH_TOKEN, and model. It preserves
// every other valid JSON member, including unmanaged env values.
package claudeconfig
