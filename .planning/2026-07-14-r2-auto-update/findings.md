# Cloudflare R2 Automatic Update Findings

## Current State

- CodeStudio Lite uses Tauri 2 but does not currently include `tauri-plugin-updater` or `@tauri-apps/plugin-updater`.
- `src-tauri/tauri.conf.json` has no updater endpoint, updater public key, or updater artifact generation setting.
- The current Settings update UI only queries GitHub Releases through `src/lib/appUpdateStore.ts`; it can report availability but cannot securely download or install an update.
- Windows manual distribution is a branded WiX Burn executable that chains a stable-UpgradeCode Tauri MSI. The MSI remains the likely automatic-update payload; Burn should remain the interactive/manual installer.
- The macOS workflow currently uploads `.app` and `.dmg` bundles only. No `.app.tar.gz`, updater signature, or `latest.json` is produced.
- Existing local release output contains MSI, NSIS, and Burn installers but no `.sig` files.

## Initial R2 Design Direction

- Use immutable versioned object keys for artifacts and a small mutable channel manifest such as `stable/latest.json`.
- Upload artifacts and signatures first, validate their public URLs, then publish the manifest last as the release commit point.
- Keep updater signing keys separate from Cloudflare credentials. R2 transport integrity is not a replacement for Tauri artifact signature verification.
- Do not embed R2 write credentials in the desktop application; the app needs public read access only.

## Confirmed Contract

- Tauri's updater supports Windows and macOS and exposes `check`, `downloadAndInstall`, and process relaunch integration.
- Tauri updater builds must enable `bundle.createUpdaterArtifacts`; the installed CLI schema confirms that this produces updater artifacts and signatures.
- The current compatible plugin releases are `tauri-plugin-updater` / `@tauri-apps/plugin-updater` 2.10.1 and process plugin 2.3.1.
- Cloudflare documents `r2.dev` as rate-limited and intended for development. Production should use an R2 custom domain.
- R2 custom domains support Cloudflare Cache controls; the S3-compatible upload endpoint is `https://<ACCOUNT_ID>.r2.cloudflarestorage.com` with region `auto`.
- The first release channel will be stable-only. Versioned artifacts are immutable and `stable/latest.json` is published last and retained for rollback.
- Windows updater delivery will use the Tauri-generated MSI updater artifact. The branded Burn executable remains the manual installer.
- macOS updater delivery will use the signed Tauri app archive; the DMG remains the manual installer.
- The user explicitly chose not to implement incremental updates. The initial system will download complete signed updater packages on every platform.
- The update system is R2-only. Builds with both the R2 base URL and updater public key invoke the signed Tauri updater; unconfigured and browser builds perform no remote update request and report an explicit local state.
- A verified Windows updater build produced three localized MSI files and one `.sig` per MSI. The stable manifest uses the en-US MSI as the silent Windows updater payload while Burn remains the manual installer.
- R2 immutable object reuse must compare SHA-256 metadata as well as content length; size-only checks can accept different content with the same byte count.
- GitHub was only acting as an optional macOS build runner; it is not required by Tauri updater or R2. Platform build commands and the R2 publisher can run locally, on a self-hosted runner, or in any CI provider.
- The initial `updater:build:windows` command reused the general Burn script without an enforcement flag, so an empty environment silently selected ordinary `tauri build` and produced an updater-disabled installer. Explicit updater builds must fail closed when R2 endpoint or signing inputs are absent.
- `https://download.codestudio.build` resolves through Cloudflare and serves HTTPS, while `/stable/latest.json` currently returns 404 because the first channel manifest has not been uploaded yet.
- The production signing trust root now lives under `%USERPROFILE%\.codestudio-lite\updater`: the encrypted private key and a CurrentUser-DPAPI password blob are protected by a user-only ACL. Portable migration uses a separate user-entered passphrase and an authenticated encrypted `.csl-updater-key` bundle; the production signing password is never printed or committed.
- Tauri protocol platform identifiers such as `windows-x86_64` retain underscores because they are manifest keys/directories, not filenames. Every final artifact basename is kebab-case, and publication rejects artifact basenames containing spaces or underscores.
- The new release filename contract is stricter than kebab-case: the canonical prefix is `CodeStudio-Lite-<version>-<OS>-<arch>`, with exact OS tokens `Windows`, `Linux`, and `macOS`. Tauri manifest platform identifiers such as `windows-x86_64` remain protocol keys rather than artifact basenames.
- Linux normalization may be invoked from a WSL shell that sees Windows Node paths but cannot execute `node`; reading `package.json` through Node first and Python 3's standard JSON parser second keeps the script portable without resorting to ad hoc JSON text parsing.
