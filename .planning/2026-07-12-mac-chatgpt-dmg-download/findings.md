# Findings

## Scope Update

- The requested scope now covers every download initiated by the macOS build, not only the ChatGPT Desktop DMG.
- The initial host-restricted regression test is provisional and must be broadened after all download entry points are mapped.

## macOS Download Inventory (Initial)

- ChatGPT Desktop packages use the resumable `chatgpt_desktop.rs::download_to_file` path.
- Claude Desktop DMGs use `tool_installer.rs::download_url_to_file`; the same helper also serves Windows Claude packages.
- Many CLI/tool installs launch external official installers, npm, Homebrew, or shell commands. Their network transport is owned by those external tools rather than the app's Rust HTTP client.
- Frontend update checks fetch metadata in the WebView; the binary package downloads currently identified in the Rust backend are the primary scope for a shared macOS fallback.

## Claude Download Path

- Claude Desktop latest metadata and DMG bytes are both fetched in `tool_installer.rs` with independent reqwest clients.
- The current Claude DMG helper performs one request only, deletes any previous partial file, and has no retry/resume or system-proxy fallback.
- The same helper is also used by the Windows Claude MSIX path, so macOS behavior must be selected without changing Windows semantics.
- A shared transport-mode helper can cover ChatGPT and Claude while each module keeps its existing progress and verification ownership.

## Final Inventory Boundary

- App-owned remote byte transfers on macOS are ChatGPT manifest/checksum/package reads plus Claude latest-metadata and DMG reads; detector performs a separate Claude latest-version metadata read.
- These paths can all use one shared direct-first/system-proxy-second HTTP module.
- Node, Hermes, Bun, npm, Homebrew, VS Code, and similar installations delegate networking to external installers or package managers. Replacing those transports would be a separate installer redesign; this fix will not rewrite third-party network stacks.
- The shared fallback will apply to every remote URL used by app-owned download functions on macOS, not only `persistent.oaistatic.com`.

## Implemented Architecture

- Added `core/download_http.rs` as the shared app-owned HTTP transfer layer.
- The shared layer explicitly forces rustls, calls `no_proxy()` for the first transport, enables HTTP/1.1 on macOS, and then performs a second pass using macOS system proxy discovery.
- Metadata fetches and file downloads both use bounded transient retries and preserve non-retryable HTTP errors.
- File downloads share partial files between direct and proxy passes, support HTTP range resume, validate known/response content lengths, and atomically replace the final path.
- ChatGPT and Claude retain their existing progress events and downstream SHA-256 verification.
- Claude package downloads now also gain bounded retry and HTTP range resume, replacing the previous single-shot transfer without changing its final SHA-256 gate.
- No system curl invocation was added; the previous LibreSSL regression boundary remains intact.
- Enabling reqwest system-proxy support can affect default clients on macOS, so all localhost inspector callers were audited: ChatGPT already used `no_proxy()`, and Claude now uses the shared direct transport explicitly.
- Cargo feature resolution was verified per target: Windows retains only the existing rustls/blocking/json features, while Apple arm64 additionally enables `macos-system-configuration` and `system-proxy`.

## Initial State

- The reported macOS failure occurs after four attempts to download the official arm64 ChatGPT DMG from `persistent.oaistatic.com`.
- The product worktree is synchronized with `origin/main` at `09d655e`; only local planning/workflow files are untracked.

## Download Path

- `src-tauri/src/core/chatgpt_desktop.rs` owns ChatGPT Desktop release discovery and DMG download handling.
- `download_to_file` retries its `reqwest` request up to `MIRROR_HTTP_MAX_ATTEMPTS` (four attempts) and emits the exact reported error when every request fails before a response is available.
- The current DMG download path has no macOS-native transport fallback after `reqwest` exhausts its retries.
- The repository already uses the system `curl` executable for macOS upstream HTTP requests, establishing a local precedent for working around Rust TLS/network-stack incompatibilities on macOS.

## Constraints

- The ChatGPT downloader deliberately uses `reqwest` with `rustls-tls`; on macOS it also forces HTTP/1.1.
- Existing regression coverage explicitly rejects `curl` in the normal metadata and package transfer paths because a prior mirror failure came from macOS system curl/LibreSSL.
- A blanket transport switch would regress that fix. Any fallback must be macOS-only, bounded, and restricted to the official `persistent.oaistatic.com` package host after the primary Rust transport has exhausted its retries.
- The fallback must keep the current temporary-file promotion model so downstream size and SHA-256 validation still gate installation.

## Integrity and Source Behavior

- `stage_from_plan` always computes the staged package SHA-256 and deletes the file on mismatch before installation begins.
- Official-source macOS installs use a stable official URL that redirects to a versioned `ChatGPT-<version>-<arch>.dmg`, explaining why the reported final URL differs from the source constant.
- The mirror manifest remains the source of expected content length, version, and SHA-256 even when the official CDN URL is selected.

## Root Cause Direction

- `reqwest` is declared with `default-features = false` and does not enable `system-proxy` / `macos-system-configuration`.
- Consequently, the downloader ignores macOS System Settings proxy configuration. This matches the observed failure boundary: mirror metadata can load directly, while the official CDN request fails before a response is received.
- Reqwest can use macOS system proxy discovery while retaining the existing rustls transport; no curl/LibreSSL regression is required.
- The conservative design is to preserve the current direct client as the primary path and add one bounded system-proxy transport pass only for official macOS CDN DMGs.

## Selected Design

- Introduce explicit direct and macOS-system-proxy transfer modes while keeping rustls for both.
- The direct client will call `no_proxy()` so enabling reqwest system-proxy support does not silently change existing Windows, Linux, mirror-metadata, or first-attempt behavior.
- Only an HTTPS DMG on `persistent.oaistatic.com` receives the second transport mode, and each mode remains bounded by the existing four-attempt retry policy.
- Both modes reuse the same partial temporary file, response-mode logic, progress callback, size check, atomic promotion, and downstream SHA-256 verification.

## Live Manifest Check

- The current schema-5 manifest lists macOS arm64 `ChatGPT-26.707.51957-arm64.dmg` directly on `persistent.oaistatic.com`, matching the user's failing URL.
- The manifest's macOS source URL is not a package proxy hosted by `codexapp.agentsmirror.com`; selecting the mirror source therefore does not avoid the official CDN.
- The current arm64 package metadata supplies content length `560735555` and SHA-256 `a66a0645fd4de6f4f22e75ca87bedb1585aeb57f3457a3df55509bf8da110879`, so the existing verification path remains usable after transport fallback.

## Final Review Checkpoint

- The shared module keeps the first transport explicitly proxy-free and only adds macOS system-proxy discovery as the second transport.
- File transfers reuse one partial file across retries and transport fallback, while the ChatGPT and Claude callers retain their downstream SHA-256 verification.
- The final repository inventory is being repeated with literal searches after one PowerShell regex escaping failure; no product files were changed by that failed search.
- The literal Rust inventory found no additional app-owned artifact downloader: remaining reqwest clients serve profile/API traffic or localhost Inspector discovery rather than remote package transfer.
- Frontend network inventory found the GitHub releases metadata check; its action path still needs a focused review to confirm whether the app downloads release assets itself or delegates to the browser.
- Node, Bun, Hermes, Homebrew, npm, and similar install actions visibly delegate to shell scripts or package managers and therefore remain outside the shared Rust transport layer.
- The GitHub update store fetches release JSON only and retains the release page URL; no release asset download is implemented in that store.
- The remaining ChatGPT reqwest client is explicitly `no_proxy()` and reads only the local CDP endpoint; profile model discovery and usage queries are API traffic, not artifact downloads.
- Final comparison against `HEAD` found that the first shared implementation forced `no_proxy()` on non-macOS Claude metadata/package callers, whereas their previous clients used reqwest's platform-default proxy behavior.
- The transport policy was corrected to use `PlatformDefault` on Windows/Linux, while macOS alone uses explicit direct-first then system-proxy fallback; localhost Inspector callers remain explicit `Direct`.
- The Settings update control only refreshes GitHub release metadata; there is no in-app release-asset download action to migrate.
- The post-correction product diff passes `git diff --check`; Cargo lock additions are limited to reqwest's macOS system-configuration support and its locked transitive packages.
- Per-file review confirmed ChatGPT manifest/checksum/package, Claude metadata/package, and Claude update detection all use the shared layer, while their existing package SHA-256 gates remain downstream and unchanged.
- The Claude and ChatGPT localhost Inspector clients remain proxy-free, preventing the new macOS reqwest feature from redirecting loopback discovery.
- Final target feature resolution confirms Windows keeps the existing reqwest feature set, while Apple arm64 adds only `macos-system-configuration`/`system-proxy` on top of rustls.
- A native macOS runtime transfer could not be exercised from this Windows host; Apple cross-compilation also requires an unavailable Apple C compiler/SDK for `ring`.
