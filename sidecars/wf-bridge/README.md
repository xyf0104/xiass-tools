# XIASS WF bridge

This sidecar is the embedded Antigravity WF helper used by XIASS Tools. It is
built with the `wfbridge` Go build tag and listens only on `127.0.0.1`.

The implementation is ported from the sibling `Antigravity-WF-Assistant-GitHub`
project. Its upstream license and the original source boundary are preserved
in `UPSTREAM-LICENSE.txt` and the repository-level `ORIGIN_AND_LICENSE.md`.

The parent Tauri process creates a per-process bearer token, passes it through
the environment, validates the ready signal and health endpoint, and stops the
child when the application exits. The token is never placed in a URL.
