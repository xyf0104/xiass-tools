# Progress

## 2026-07-19

- Started diagnosis with `diagnosing-bugs`.
- Reproduced the path contract mismatch using local ARP and filesystem state: the MSI location contains the executable while the per-user candidate does not.
- Identified the current test seam as insufficient because it checks source text rather than post-Apply path resolution behavior.
- Added and ran the focused post-Apply path-resolution regression test; it failed on the direct `installFolder` path join as expected.
- Implemented authoritative executable resolution across the selected folder, all MSI/ARP locations, and the Program Files default; the focused regression test is now green.
- A raw `dotnet build` lacked the WiX reference path; verification will use the build script's `WixToolsPath` contract.
- Recompiled the managed BA with WiX 3.14 references: 0 warnings and 0 errors.
- All 15 focused Windows installer tests pass, including the new stale-folder regression.
- Published rebuild could not read the machine-local updater private key under current permissions; switched to an isolated unsigned verification bundle so the existing signed release artifact remains untouched.
- Isolated candle/light bundle creation succeeded. Normal and unsupported-language plan-only checks completed with result `0x0` and preserved the selected install folder.
- The interactive-window portion of `verify-burn.ps1` was blocked by denied CIM access in the current execution environment; Burn logs showed normal BA startup and detection with no initialization failure.
- Final verification: 15/15 focused Windows installer tests, 204/204 full unit tests, Svelte check 0 errors/0 warnings, C# build 0 warnings/0 errors, and targeted whitespace checks passed.
