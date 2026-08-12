# Findings

- Tauri generated localized MSI packages for `zh-CN`, `zh-TW`, and `en-US`, all sharing the configured MSI UpgradeCode.
- WiX Standard BA cannot render a user-facing language dropdown; a managed bootstrapper application is required.
- WiX 3.14 exposes `Engine.Detect`, `Engine.Plan`, `Engine.Apply`, package detection events, mutable `PlanPackageBeginEventArgs.State`, and bundle variables.
- The existing root planning files belong to the Panda CSS migration and must remain untouched.
- The final bundle is 55,095,277 bytes and embeds all three localized MSI files plus `CodeStudioBootstrapper.dll` and `BootstrapperCore.config`.
- Burn and MSI upgrade identities are separate and stable; the localized MSI manifests accept same-version related-package replacement, which supports language switching without side-by-side installs.
- The managed BA targets .NET Framework 4.5 and accepts any v4 runtime, covering Windows 8+ and normal Windows 10/11 installations without bundling a large framework redistributable.
- The WPF bootstrapper owns all visible installer copy; MSI localization is not visible because `DisplayInternalUI="no"`.
- Therefore Burn can keep its three-language UI while Tauri generates and Burn chains only the en-US MSI. The language selection remains a bootstrapper UI preference and no longer selects an MSI transform.
