import assert from "node:assert/strict";
import fs from "node:fs";
import test from "node:test";

const config = JSON.parse(fs.readFileSync("src-tauri/tauri.conf.json", "utf8"));
const packageJson = JSON.parse(fs.readFileSync("package.json", "utf8"));

const burnDirectory = "installer/windows/burn";
const bundleSourcePath = `${burnDirectory}/bundle.wxs`;
const bootstrapperSourcePath = `${burnDirectory}/BootstrapperApplication.cs`;
const bootstrapperProjectPath = `${burnDirectory}/CodeStudioBootstrapper.csproj`;
const installerWindowXamlPath = `${burnDirectory}/InstallerWindow.xaml`;
const installerWindowSourcePath = `${burnDirectory}/InstallerWindow.xaml.cs`;
const buildScriptPath = `${burnDirectory}/build-burn.ps1`;
const verifyScriptPath = `${burnDirectory}/verify-burn.ps1`;

test("Windows installers keep stable upgrade identity and reject downgrades", () => {
  assert.equal(config.identifier, "com.codestudio.lite");
  assert.equal(config.bundle.windows.allowDowngrades, false);
  assert.equal(config.bundle.windows.wix.upgradeCode, "83dcf1cf-93d9-57d3-b567-bf98f108a380");
  assert.deepEqual(config.bundle.windows.wix.language, ["en-US"]);
  assert.equal(config.bundle.windows.nsis.installMode, "currentUser");
});

test("NSIS uses branded icons and a Chinese-English language selector", () => {
  assert.equal(config.bundle.windows.nsis.installerIcon, "icons/icon.ico");
  assert.equal(config.bundle.windows.nsis.uninstallerIcon, "icons/icon.ico");
  assert.ok(fs.statSync("src-tauri/icons/icon.ico").size > 0);
  assert.deepEqual(config.bundle.windows.nsis.languages, ["SimpChinese", "TradChinese", "English"]);
  assert.equal(config.bundle.windows.nsis.displayLanguageSelector, true);
  assert.equal(config.bundle.windows.nsis.startMenuFolder, "CodeStudio Lite");
});

test("Burn bundle embeds one base MSI without language transforms", () => {
  const source = fs.readFileSync(bundleSourcePath, "utf8");

  assert.match(source, /UpgradeCode="\{6B78C4D8-8C90-4C11-A1D8-893160DA17A7\}"/);
  assert.match(source, /Id="MsiBase"/);
  assert.doesNotMatch(source, /TRANSFORMS|SelectedTransform|\.mst/);
  assert.match(source, /MsiProperty Name="INSTALLDIR" Value="\[InstallFolder\]"/);
  assert.doesNotMatch(source, /<Payload Id="Transform/);
  assert.doesNotMatch(source, /InstallCondition=/);
  assert.match(source, /Name="SelectedLanguage" Type="string" Value="" bal:Overridable="yes"/);
  assert.doesNotMatch(source, /Name="SelectedLanguage"[^>]*Persisted="yes"/);
  assert.match(source, /Name="InstallFolder" Type="string" Value="" Persisted="yes"/);
  assert.match(source, /Name="VerifyPlanOnly" Type="numeric" Value="0"/);
  assert.match(source, /Compressed="yes"/);
});

test("managed Burn UI exposes the three-language selector", () => {
  const source = [bootstrapperSourcePath, installerWindowSourcePath, installerWindowXamlPath]
    .map((path) => fs.existsSync(path) ? fs.readFileSync(path, "utf8") : "")
    .join("\n");

  assert.match(source, /简体中文/);
  assert.match(source, /繁體中文/);
  assert.match(source, /English/);
  assert.match(source, /StringVariables\["SelectedLanguage"\]/);
  assert.match(source, /Engine\.Detect\(\)/);
  assert.match(source, /Engine\.Plan\(/);
  assert.match(source, /Engine\.Apply\(/);
  assert.match(source, /applyWindowHandle == IntPtr\.Zero/);
  assert.match(source, /LaunchAction\.Layout/);
  assert.match(source, /LaunchAction\.Cache/);
  assert.match(source, /commandAction != LaunchAction\.Repair/);
  assert.match(source, /ExitCode = 1602/);
  assert.match(source, /CloseOnUiThread/);
  assert.match(source, /MsiGetProductInfo/);
  assert.match(source, /RegistryView\.Registry64/);
  assert.match(source, /RegistryView\.Registry32/);
  assert.match(source, /SOFTWARE\\Microsoft\\Windows\\CurrentVersion\\Uninstall/);
  assert.match(source, /"InstallLocation"/);
  assert.match(source, /"DisplayName"/);
  assert.match(source, /"Language"/);
  assert.match(source, /Command\.GetCommandLineArgs\(\)/);
  assert.match(source, /CommandLineValue/);
  assert.match(source, /GetUserDefaultUILanguage/);
  assert.match(source, /GetSystemDefaultUILanguage/);
  assert.doesNotMatch(source, /InstalledProductLanguage/);
});

test("managed Burn falls back unsupported Windows UI languages to English before WPF starts", () => {
  const source = fs.readFileSync(bootstrapperSourcePath, "utf8");
  const script = fs.readFileSync(verifyScriptPath, "utf8");
  const buildScript = fs.readFileSync(buildScriptPath, "utf8");

  assert.match(source, /ResolveLanguageCode\(configuredLanguage\)/);
  assert.match(source, /NormalizeLanguageCode\(name\)/);
  assert.match(source, /return "en-US";/);
  assert.match(source, /ApplySupportedUiCulture\(selectedLanguage\)/);
  assert.match(source, /CultureInfo\.DefaultThreadCurrentUICulture\s*=\s*culture/);
  assert.match(source, /Thread\.CurrentThread\.CurrentUICulture\s*=\s*culture/);
  assert.ok(
    source.indexOf("ApplySupportedUiCulture(selectedLanguage)") < source.indexOf("new Application")
  );
  assert.match(script, /-SelectedLanguage=ja-JP/);
  assert.match(script, /Variable: SelectedLanguage = en-US/);
  assert.match(buildScript, /Release\\net48\\CodeStudioBootstrapper\.dll/);
  assert.doesNotMatch(buildScript, /Get-ChildItem \$baOutputDir -Recurse -Filter "CodeStudioBootstrapper\.dll"/);
});

test("managed Burn reports initialization failures and tolerates legacy install paths", () => {
  const source = fs.readFileSync(bootstrapperSourcePath, "utf8");

  assert.match(source, /protected override void Run\(\)[\s\S]*try[\s\S]*RunInstaller\(\)[\s\S]*catch \(Exception error\)/);
  assert.match(source, /Installer initialization failed:/);
  assert.match(source, /MessageBox\.Show\([\s\S]*Installer could not start/);
  assert.match(source, /ResolveInitialInstallFolder\(/);
  assert.match(source, /TryNormalizeInstallFolder\(/);
  assert.match(source, /ignoring invalid legacy installation location/i);
});

test("interactive Burn brings its clean-room child window to the foreground", () => {
  const source = fs.readFileSync(installerWindowSourcePath, "utf8");

  assert.match(source, /private readonly bool showFullUi/);
  assert.doesNotMatch(source, /ContentRendered \+=/);
  assert.match(source, /Loaded \+= \(_, __\) =>[\s\S]*BringToForeground\(\)/);
  assert.match(source, /BringToForeground\(\)[\s\S]*if \(!showFullUi/);
  assert.match(source, /WindowState = WindowState\.Normal/);
  assert.match(source, /Activate\(\)/);
  assert.match(source, /Topmost = true[\s\S]*Topmost = false/);
  assert.match(source, /SetForegroundWindow\(windowHandle\)/);
});

test("managed Burn UI uses a localized multi-step install wizard", () => {
  const source = [bootstrapperSourcePath, installerWindowSourcePath, installerWindowXamlPath]
    .map((path) => fs.existsSync(path) ? fs.readFileSync(path, "utf8") : "")
    .join("\n");

  assert.match(source, /enum InstallerPage[\s\S]*Welcome[\s\S]*Options[\s\S]*Confirm[\s\S]*Progress[\s\S]*Complete/);
  assert.match(source, /ShowWelcome\(/);
  assert.match(source, /ShowOptions\(/);
  assert.match(source, /ShowConfirmation\(/);
  assert.match(source, /FolderBrowserDialog/);
  assert.match(source, /Engine\.StringVariables\["InstallFolder"\]/);
  assert.match(source, /BeginAction\(string languageCode, string installFolder, LaunchAction action\)/);
  assert.match(source, /"Installation location"/);
  assert.match(source, /"安装位置"/);
  assert.match(source, /"安裝位置"/);
  assert.doesNotMatch(source, /primaryButton\.Click \+= \(_, __\) => bootstrapper\.BeginAction/);
});

test("Burn completion copy reflects a successful uninstall", () => {
  const source = fs.readFileSync(installerWindowSourcePath, "utf8");
  const localizationMethod = source.match(
    /private void ApplyLocalization\(\)[\s\S]*?(?=\n        private void SetPageVisibility\()/,
  )?.[0] ?? "";
  const completePage = localizationMethod.match(
    /case InstallerPage\.Complete:[\s\S]*?break;/,
  )?.[0] ?? "";
  const showComplete = source.match(
    /internal void ShowComplete\(int status, ApplyRestart restart\)[\s\S]*?(?=\n        internal void ShowFailure\()/,
  )?.[0] ?? "";

  assert.match(completePage, /commandAction == LaunchAction\.Uninstall/);
  assert.match(completePage, /"Uninstall complete", "卸载完成", "解除安裝完成"/);
  assert.match(showComplete, /commandAction == LaunchAction\.Uninstall/);
  assert.match(showComplete, /"CodeStudio Lite was uninstalled successfully\.", "CodeStudio Lite 已成功卸载。", "CodeStudio Lite 已成功解除安裝。"/);
  assert.match(showComplete, /"Restart Windows to finish uninstalling CodeStudio Lite\.", "请重启 Windows 以完成 CodeStudio Lite 卸载。", "請重新啟動 Windows 以完成 CodeStudio Lite 解除安裝。"/);
});

test("managed Burn project excludes stale local build intermediates", () => {
  const source = fs.readFileSync(bootstrapperProjectPath, "utf8");

  assert.match(source, /Compile Remove="obj\\\*\*\\\*\.cs"/);
});

test("Burn bootstraps the compact .NET 4.8 web prerequisite", () => {
  const project = fs.readFileSync(bootstrapperProjectPath, "utf8");
  const bundle = fs.readFileSync(bundleSourcePath, "utf8");
  const build = fs.readFileSync(buildScriptPath, "utf8");
  const verify = fs.readFileSync(verifyScriptPath, "utf8");

  assert.match(project, /<TargetFramework>net48<\/TargetFramework>/);
  assert.match(bundle, /<PackageGroupRef Id="NetFx48Web"\s*\/>/);
  assert.doesNotMatch(bundle, /WixMbaPrereqPackageId" Value=""/);
  assert.match(build, /WixNetFxExtension\.dll/);
  assert.ok((build.match(/-ext \$netFxExtension/g) ?? []).length >= 2);
  assert.match(verify, /@\("NetFx48Web", "MsiBase"\)/);
  assert.match(verify, /burn:ExePackage/);
  assert.match(verify, /WixMbaPrereqInformation/);
  assert.match(verify, /PackageId -ne "NetFx48Web"/);
});

test("managed Burn UI uses the CodeStudio Lite WPF theme", () => {
  const project = fs.readFileSync(bootstrapperProjectPath, "utf8");
  const bundle = fs.readFileSync(bundleSourcePath, "utf8");
  const bootstrapper = fs.readFileSync(bootstrapperSourcePath, "utf8");
  const xaml = fs.existsSync(installerWindowXamlPath) ? fs.readFileSync(installerWindowXamlPath, "utf8") : "";

  assert.match(project, /<UseWPF>true<\/UseWPF>/);
  assert.match(bundle, /Name="CodeStudioLite\.ico"/);
  assert.match(bundle, /Name="CodeStudioLite\.png"/);
  assert.match(bootstrapper, /InstallerWindow/);
  assert.doesNotMatch(bootstrapper, /InstallerForm/);
  assert.match(xaml, /WindowStyle="None"/);
  assert.match(xaml, /#0A0B0D/);
  assert.match(xaml, /#101216/);
  assert.match(xaml, /#1F8FFF/);
  assert.match(xaml, /#F4D94E/);
  assert.match(xaml, /x:Name="BrandIcon"/);
  assert.match(xaml, /x:Name="PrimaryButton"/);
  assert.match(xaml, /x:Name="ProgressTrack"/);
  assert.match(xaml, /TargetType="ComboBoxItem"/);
  assert.match(fs.readFileSync(installerWindowSourcePath, "utf8"), /SourceInitialized \+=/);
  assert.match(fs.readFileSync(installerWindowSourcePath, "utf8"), /private IntPtr windowHandle/);
  assert.match(fs.readFileSync(installerWindowSourcePath, "utf8"), /internal IntPtr Handle => windowHandle/);
  assert.doesNotMatch(fs.readFileSync(installerWindowSourcePath, "utf8"), /internal IntPtr Handle => new WindowInteropHelper/);
  assert.match(bootstrapper, /codestudio-lite\.exe/);
  assert.match(bootstrapper, /ProcessStartInfo/);
  assert.match(fs.readFileSync(installerWindowSourcePath, "utf8"), /"Open CodeStudio Lite"/);
  assert.match(fs.readFileSync(installerWindowSourcePath, "utf8"), /"打开 CodeStudio Lite"/);
  assert.match(fs.readFileSync(installerWindowSourcePath, "utf8"), /"Retry"/);
  assert.match(fs.readFileSync(installerWindowSourcePath, "utf8"), /"重试"/);
  assert.match(fs.readFileSync(installerWindowSourcePath, "utf8"), /RetryInstallation\(/);
  assert.match(fs.readFileSync(installerWindowSourcePath, "utf8"), /page == InstallerPage\.Complete[\s\S]*canLaunchInstalledApp \|\| canRetryInstallation/);
});

test("application updates relaunch CodeStudio Lite only after Burn succeeds", () => {
  const backend = fs.readFileSync("src-tauri/src/core/app_updater.rs", "utf8");
  const bootstrapper = fs.readFileSync(bootstrapperSourcePath, "utf8");
  const window = fs.readFileSync(installerWindowSourcePath, "utf8");

  assert.match(backend, /-LaunchAfterInstall=1/);
  assert.match(bootstrapper, /CommandLineValue\("LaunchAfterInstall"\) == "1"/);
  assert.match(bootstrapper, /OnApplyComplete[\s\S]*e\.Status >= 0[\s\S]*launchAfterInstall/);
  assert.match(bootstrapper, /OnApplyComplete[\s\S]*LaunchInstalledApp\(\)/);
  assert.match(bootstrapper, /Process\.Start\(new ProcessStartInfo/);
  assert.match(bootstrapper, /ResolveInstalledExecutablePath\(\)/);
  assert.doesNotMatch(window, /Process\.Start/);
});

test("Burn resolves the post-install executable from authoritative installed locations", () => {
  const bootstrapper = fs.readFileSync(bootstrapperSourcePath, "utf8");
  const launchMethod = bootstrapper.match(/internal void LaunchInstalledApp\(\)[\s\S]*?\n        }/)?.[0] ?? "";

  assert.match(launchMethod, /ResolveInstalledExecutablePath\(\)/);
  assert.doesNotMatch(launchMethod, /Path\.Combine\(installFolder, "codestudio-lite\.exe"\)/);
  assert.match(bootstrapper, /ResolveInstalledExecutablePath\(\)[\s\S]*RegistryInstallLocations\(/);
  assert.match(bootstrapper, /File\.Exists\(candidate\)/);
  assert.match(bootstrapper, /attemptedLocations/);
});

test("Windows build script creates the Burn bundle from one en-US MSI", () => {
  const script = fs.readFileSync(buildScriptPath, "utf8");

  assert.equal(packageJson.scripts["tauri:build:windows"], "powershell -NoProfile -ExecutionPolicy Bypass -File installer/windows/burn/build-burn.ps1");
  assert.match(script, /npm\.cmd run tauri:build/);
  assert.match(script, /_en-US\.msi/);
  assert.doesNotMatch(script, /_zh-(?:CN|TW)\.msi|torch\.exe|-t language|_Storages|SetStream|Set-MsiProperty|normalized-zh/);
  assert.match(script, /WixBalExtension/);
  assert.match(script, /verify-burn\.ps1/);
  assert.match(script, /CodeStudio-Lite-\$\{version\}-Windows-x64-en-US\.msi/);
  assert.match(script, /CodeStudio-Lite-\$\{version\}-Windows-x64-setup\.exe/);
  assert.match(script, /tauri signer sign.*\$bundleOutput/s);
  assert.match(script, /\$bundleOutput\.sig/);
  assert.match(script, /Remove-StaleLocalizedMsiArtifacts/);
  assert.match(script, /Normalize-WindowsPublishedDirectory/);
  assert.match(script, /\[ _\]\+/);
});

test("Burn verification inspects the built manifest instead of trusting source authoring", () => {
  const script = fs.readFileSync(verifyScriptPath, "utf8");

  assert.match(script, /dark\.exe/);
  assert.match(script, /6B78C4D8-8C90-4C11-A1D8-893160DA17A7/);
  assert.match(script, /MsiBase/);
  assert.doesNotMatch(script, /\.mst|_Storages|TRANSFORMS|SelectedTransform/);
  assert.match(script, /SetARPINSTALLLOCATION/);
  assert.match(script, /ARPINSTALLLOCATION/);
  assert.match(script, /\[INSTALLDIR\]/);
  assert.match(script, /CodeStudioBootstrapper\.dll/);
  assert.match(script, /CodeStudioLite\.ico/);
  assert.match(script, /CodeStudioLite\.png/);
  assert.match(script, /MsiProperty\[@Id='INSTALLDIR'\]/);
  assert.match(script, /-VerifyPlanOnly=1/);
  assert.match(script, /MainWindowHandle/);
  assert.match(script, /MainWindowTitle/);
  assert.match(script, /\.Responding/);
  assert.match(script, /Plan complete, result: 0x0/);
  assert.match(script, /Variable: InstallFolder/);
  assert.match(script, /Variable: SelectedLanguage/);
  assert.doesNotMatch(script, /-SelectedLanguage=zh-CN/);
  // The plan must never ask for a related bundle to go: that is what makes the
  // engine launch a second installer process.
  assert.match(script, /if \(\$planLogContent -match 'ba requested: Absent'\)/);
  assert.match(script, /spawns a second installer process/);
});

test("Burn locates a running app across the 32/64-bit gap before touching its files", () => {
  const source = fs.readFileSync(bootstrapperSourcePath, "utf8");

  // Burn hosts a 32-bit bootstrapper while the product ships 64-bit, and
  // Process.MainModule throws across that boundary. Reaching for it made every
  // running copy look like an unrelated process, so the installer left the app
  // open and then stalled on files it still held.
  assert.doesNotMatch(source, /MainModule\.FileName/);
  assert.match(source, /QueryFullProcessImageName/);
  assert.match(source, /OpenProcess/);
  assert.match(source, /CloseHandle/);
  assert.match(source, /ProcessQueryLimitedInformation\s*=\s*0x1000/);

  // The close runs for real installs and upgrades, not only for uninstall.
  assert.match(source, /CloseRunningApplication\(\);\s*\n\s*Engine\.Apply\(/);

  // A process whose location cannot be read must be reported, never skipped in
  // silence: that silence is what made the original stall undiagnosable.
  assert.match(source, /Could not read the location of process/);
});

test("silent Burn runs neither draw a window nor read controls off the UI thread", () => {
  const source = fs.readFileSync(installerWindowSourcePath, "utf8");

  const bootstrapper = fs.readFileSync(bootstrapperSourcePath, "utf8");

  // A silent run must not put a window on screen at all. Hiding one after the
  // fact does not work — Opacity needs AllowsTransparency, and Visibility set
  // during Show is overwritten by that same Show — so every child bundle the
  // engine spawned left an unpainted frame on the desktop.
  assert.doesNotMatch(source, /Opacity\s*=\s*0/);
  assert.match(bootstrapper, /EnsureHandleWithoutShowing\(\)/);
  assert.match(source, /EnsureHandleWithoutShowing\(\)[\s\S]{0,160}EnsureHandle\(\)/);
  assert.match(bootstrapper, /ShutdownMode\.OnExplicitShutdown/);
  // Loaded never fires for a window that is never shown, so detection cannot
  // hang off it, and the run has to be told to stop explicitly or it lingers.
  assert.match(bootstrapper, /if \(interactive\)[\s\S]{0,200}form\.Loaded \+=/);
  assert.match(bootstrapper, /Dispatcher\.BeginInvoke[\s\S]{0,80}Engine\.Detect\(\)/);
  assert.match(source, /Application\.Current\?\.Shutdown\(\)/);

  // A silent run reaches RemoveUserDataRequested from the engine thread, where
  // touching a WPF control throws and takes that thread down with it.
  const property = source.match(
    /internal bool RemoveUserDataRequested[\s\S]*?\n        \}/,
  );
  assert.ok(property, "RemoveUserDataRequested should still exist");
  assert.match(property[0], /Dispatcher\.CheckAccess\(\)/);
  assert.match(property[0], /!showFullUi/);
});

test("a running install can be cancelled and reports itself as cancelled", () => {
  const bootstrapper = fs.readFileSync(bootstrapperSourcePath, "utf8");
  const window = fs.readFileSync(installerWindowSourcePath, "utf8");

  // Cancelling mid-apply is a request to the engine, which then rolls back.
  assert.match(bootstrapper, /e\.Result = Result\.Cancel/);
  assert.match(bootstrapper, /cancelRequested = true/);
  assert.match(bootstrapper, /form\.ShowCancelling\(\)/);
  // Retry must not inherit the cancellation that ended the last attempt.
  assert.match(bootstrapper, /cancelRequested = false/);

  // The button stays live while changes are being applied. Taking it away is
  // what left the user watching a progress bar with no way out.
  assert.match(window, /applyingChanges && !cancelling/);
  assert.doesNotMatch(window, /CancelButton\.IsEnabled = false/);
  assert.match(window, /internal void ShowCancelling\(\)/);

  // Closing the window mid-apply means cancel, not "disappear and leave the
  // machine half-written".
  assert.match(window, /if \(applyingChanges\)[\s\S]{0,600}bootstrapper\.Cancel\(\)/);

  // A cancelled run is not a failure and must not be reported with an error
  // code the user is invited to look up.
  assert.match(window, /UserCancelledHResult\s*=\s*unchecked\(\(int\)0x80070642\)/);
  assert.match(window, /Setup was cancelled and no changes were kept\./);
  assert.match(window, /安装已取消，未保留任何更改。/);
  assert.match(window, /安裝已取消，未保留任何變更。/);
});

test("one install is one process", () => {
  const source = fs.readFileSync(bootstrapperSourcePath, "utf8");

  // Asking the engine to remove a same-version sibling makes it run that
  // sibling's own cached installer as a separate process. A machine holding
  // several stale registrations then gets one extra process each, every one of
  // them running the code it shipped with rather than this build's — so no fix
  // here can reach them, and any that hangs hangs the install that spawned it.
  // Duplicate entries in the installed-programs list, which only repeat
  // installs of a single version produce, are the smaller problem.
  // Uninstall still plans the product's own package absent; what must not come
  // back is doing that to a *related bundle*.
  assert.doesNotMatch(source, /PlanRelatedBundle \+=/);
  assert.doesNotMatch(source, /e\.State = RequestState\.Absent;\s*\n\s*\}\s*\n\s*\}\s*\n\s*private void OnPlanComplete/);
  assert.match(source, /One install is one process\./);

  // Same-version leftovers are still cleared, but in this process: the
  // registration is deleted directly, which also reaches leftovers whose own
  // cached installer no longer works.
  assert.match(source, /RemoveStaleBundleRegistrations\(\)/);
  assert.match(source, /staleBundleRegistrations\.Add\(e\.ProductCode\)/);
  assert.match(source, /DeleteSubKeyTree\(bundleId, false\)/);
  assert.match(source, /BundleCachePath/);
  // Only after the new install is registered and working.
  assert.match(source, /e\.Status >= 0 && \(plannedAction == LaunchAction\.Install[\s\S]{0,120}RemoveStaleBundleRegistrations/);
});

test("the installer names itself, and the installed entry names the product", () => {
  const bundle = fs.readFileSync(bundleSourcePath, "utf8");
  const source = fs.readFileSync(bootstrapperSourcePath, "utf8");

  // Bundle/@Name becomes the executable's file description, which is what
  // Windows shows for the running process — "CodeStudio Lite" there reads as
  // the application itself rather than its installer.
  assert.match(bundle, /<Bundle Name="CodeStudio Lite Installer"/);
  // The registration is the product, so it keeps the product's name.
  assert.match(source, /StringVariables\["WixBundleName"\] = "CodeStudio Lite"/);
});
