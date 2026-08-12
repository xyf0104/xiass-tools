# Findings

- The local MSI ARP entry reports `C:\Program Files\CodeStudio Lite\` and that directory contains `codestudio-lite.exe`.
- The commonly assumed per-user path under `%LOCALAPPDATA%\Programs\CodeStudio Lite` does not contain the executable.
- `LaunchInstalledApp` currently trusts the bootstrapper's mutable `installFolder` field and does not re-read the MSI/ARP location after Apply.
- The existing unit test only matches the literal `Path.Combine(installFolder, "codestudio-lite.exe")`, so it cannot catch a stale or divergent folder.
- Red-capable command: `node --test --test-name-pattern "Burn resolves the post-install executable" src/lib/windowsInstallerConfig.test.mjs` failed because `LaunchInstalledApp` directly combined the stale field with the executable name.
- Minimal repro contract: one stale/non-authoritative `installFolder` plus one valid MSI ARP `InstallLocation`; UI, locale, and update mode are not load-bearing.
- Confirmed cause: the completion action never reconciles its launch path with post-Apply MSI/ARP state.
- The fixed resolver checks the selected folder first, then all matching MSI/ARP locations across HKLM/HKCU and 64/32-bit registry views, then the Program Files default.
- The working directory now comes from the resolved executable path rather than the stale field.
- Failure diagnostics include every validated candidate path.
