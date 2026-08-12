# Progress

## 2026-07-12

- Started tracing the macOS ChatGPT Desktop DMG download failure.
- Restored repository and planning context, then created an isolated plan after the PowerShell initializer reused legacy root files.
- Located the exact four-attempt failure in `chatgpt_desktop.rs` and identified the absence of a macOS native transport fallback.
- Confirmed the primary client uses `rustls` and found an existing test that prevents reintroducing system curl as the default transfer path.
- Verified that every staged DMG remains protected by the existing content-length handling and mandatory SHA-256 check.
- Identified disabled reqwest system-proxy support as the likely macOS-only network compatibility gap.
- Selected a direct-first, system-proxy-second rustls design that avoids reintroducing curl/LibreSSL.
- Checked the live manifest and confirmed both source modes currently resolve the macOS package to the same official CDN host.
- Expanded the task to cover all macOS in-app downloads per the user's follow-up.
- Began a repository-wide inventory and identified separate ChatGPT and shared Claude/tool-installer binary download paths.
- Confirmed Claude metadata and package transfers also require macOS proxy-aware retry coverage.
- Completed the download inventory and moved into implementation of a shared app-owned macOS transfer policy.
- Confirmed RED: the new fallback regression test fails to compile until the shared transport API exists.
- Implemented the first shared transport pass and found the expected new Cargo dependency plus formatting work during validation.
- Added the shared transport module, integrated all app-owned macOS download callers, and passed 8 focused download tests plus rustfmt validation.
- Reviewed the integrated diff and confirmed platform isolation, shared partial-file resume, and unchanged integrity verification.
- Audited localhost reqwest calls and protected Claude's Node Inspector request from macOS system-proxy routing.
- Verified target-specific Cargo feature isolation for Windows and Apple arm64.
- Installed the Apple arm64 Rust standard target; cross-target check reached macOS dependencies but could not pass `ring` without an Apple C compiler/SDK on Windows.
- Full regression passed: 322 Rust library tests, 154 frontend unit tests, and Svelte check with zero errors or warnings.
- Resumed the final review, re-read the active plan, and inspected the complete shared download module with line-level context.
- Confirmed the direct-first/system-proxy-second order, bounded retries, shared partial-file resume, and final progress/promotion path before repeating the repository-wide HTTP inventory.
- Repeated the HTTP/download inventory with literal searches and confirmed all app-owned Rust artifact transfers route through `download_http`; remaining network clients are API/localhost traffic or delegated external installers.
- Reviewed frontend update metadata and remaining reqwest call sites; none adds another application-owned package download path.
- Compared pre-change clients with the shared implementation, found a non-macOS proxy-behavior regression, and corrected the transport list so Windows/Linux retain platform-default behavior.
- Re-ran rustfmt and the focused shared-download test set after that correction; all 6 tests passed.
- Confirmed the app update UI performs metadata checks only and ran a clean whitespace/diff integrity check before the final per-file review.
- Completed the per-file caller diff review with no further product-code changes required.
- Final regression passed after the non-macOS correction: 322/322 Rust library tests and 154/154 frontend unit tests.
- Final validation passed: `cargo check --lib --offline`, rustfmt check, Svelte check with 0 errors/0 warnings, Vite production build, and `git diff --check`.
- Reconfirmed target isolation: Windows reqwest features are unchanged; Apple arm64 includes macOS system proxy support while retaining rustls.
- Marked all phases complete; native Mac runtime verification remains the only environment limitation.
