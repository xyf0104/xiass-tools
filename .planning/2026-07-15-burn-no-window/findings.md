# Findings

- The updater `.sig` is not required for direct Burn execution.
- Interactive Burn uses a clean-room child process; the parent has no window while the child owns the WPF window.
- `Run()` reads and normalizes legacy registry installation paths before constructing the WPF application/window, without a top-level exception boundary.
- The managed BA targets `net45`; `WixMbaPrereqPackageId` is currently empty.
- The final Burn EXE is not Authenticode-signed; this remains a separate distribution trust concern.
- WiX 3.14's official `NetFx48.wxs` provides `PackageGroup Id="NetFx48Web"`, sets `WixMbaPrereqPackageId` to `NetFx48Web`, and uses a 1.4 MB remote web bootstrapper rather than embedding the 117 MB offline runtime.
- The build must load both `WixBalExtension.dll` and `WixNetFxExtension.dll` for candle and light.
- The built `NetFx48Web` manifest uses `Permanent=yes`, `PerMachine=yes`, `Protocol=netfx4`, detects release `528040`, and references Microsoft's external 1,439,328-byte web payload.
