# Findings

- The user-facing update artifact must be the website-hosted Burn setup EXE on Windows and DMG on macOS.
- The Settings action must appear beside the displayed current version only when a newer version has been detected.
- The application must not exit until the installer download, verification, and process handoff have succeeded.
- `src/lib/appUpdateStore.ts` currently uses `tauri_plugin_updater::check`, `Update.download`, `Update.install`, and `relaunch`; that path installs MSI or Tauri macOS archives directly and cannot hand off to Burn or a DMG.
- Settings already renders an install button when `updateAvailable && installable`, but it is in the update-status row rather than visually adjacent to the displayed version label.
- The current R2 `latest.json` schema is the stock Tauri updater format and accepts `--windows-msi` plus `--macos-archive`; it contains URL and minisign signature but no separate Burn/DMG installer artifact.
- The Tauri window close handler hides to tray, so an update command must explicitly terminate the process after successful installer handoff rather than relying on a normal window close.
- The installer artifact must retain a cryptographic trust chain. Reusing the updater minisign public key and per-artifact `.sig` is preferable to executing a download based only on HTTPS.
- Windows publishing currently signs the en-US MSI, while the Burn EXE has no updater signature. The build must sign the Burn output if it becomes the website updater artifact.
- The macOS updater build currently produces Tauri updater archives; the DMG build is separate. The release flow must sign and publish the normalized DMG instead.
- `@tauri-apps/plugin-updater` exposes `Update.rawJson`, so the existing plugin can continue selecting the correct platform entry and enforcing version semantics while the application reads that entry's installer URL/signature for its own handoff command.
- The repository already has a retrying/resumable `download_http::download_to_file` implementation with macOS direct/system-proxy fallback, suitable for installer downloads.
- Tauri updater accepts arbitrary platform artifact extensions during `check()`, so the stock platform entries can point directly to a signed Burn EXE or DMG; the application simply must not call the plugin's `install()` for those artifacts.
- `minisign-verify 0.2.5` is already present transitively and supports streaming verification. It should become an explicit dependency for the installer updater.
- `npx tauri signer sign <file>` signs arbitrary files using the existing `TAURI_SIGNING_PRIVATE_KEY` and password, so Burn and DMG can use the existing updater key pair.
- Windows should launch Burn with `-quiet -norestart` only after signature verification, then call the same gateway shutdown used for explicit app exit and terminate the Tauri process.
- macOS needs a detached helper that waits for the current PID to exit before mounting the DMG and replacing the running `.app`; it must keep a backup and restore it if copy or launch fails.
