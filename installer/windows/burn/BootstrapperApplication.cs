using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.Globalization;
using System.IO;
using System.Runtime.InteropServices;
using System.Security;
using System.Text;
using System.Threading;
using System.Windows;
using Microsoft.Win32;
using Microsoft.Tools.WindowsInstallerXml.Bootstrapper;

[assembly: BootstrapperApplication(typeof(CodeStudioLite.Installer.CodeStudioBootstrapperApplication))]

namespace CodeStudioLite.Installer
{
    internal enum InstallerPage
    {
        Welcome,
        Options,
        Confirm,
        Progress,
        Complete,
    }

    internal sealed class InstallerLanguage
    {
        public InstallerLanguage(string code, string label)
        {
            Code = code;
            Label = label;
        }

        public string Code { get; }
        public string Label { get; }
        public override string ToString() => Label;
    }

    public sealed class CodeStudioBootstrapperApplication : BootstrapperApplication
    {
        /// Matches the grace period the application itself uses when closing
        /// other tools before updating them.
        private const int GracefulExitMilliseconds = 8000;
        private const int ForcedExitMilliseconds = 3000;

        private InstallerWindow form;
        private LaunchAction plannedAction;
        private string selectedLanguage = "en-US";
        private string installFolder;
        private bool applying;
        private bool hasFolderOverride;
        private bool verifyPlanOnly;
        private bool launchAfterInstall;
        private bool removeUserData;
        private readonly HashSet<string> sameVersionRelatedBundles = new HashSet<string>(StringComparer.OrdinalIgnoreCase);

        protected override void Run()
        {
            int exitCode = unchecked((int)0x80004005);
            try
            {
                exitCode = RunInstaller();
            }
            catch (Exception error)
            {
                exitCode = error.HResult != 0 ? error.HResult : unchecked((int)0x80004005);
                Engine.Log(LogLevel.Error, "Installer initialization failed: " + error);
                if (Command.Display == Display.Full)
                {
                    try
                    {
                        MessageBox.Show(
                            Localized(selectedLanguage,
                                "Installer could not start. Review the setup log and try again.",
                                "安装程序无法启动。请检查安装日志后重试。",
                                "安裝程式無法啟動。請檢查安裝記錄後重試。"),
                            Localized(selectedLanguage, "CodeStudio Lite Setup", "CodeStudio Lite 安装程序", "CodeStudio Lite 安裝程式"),
                            MessageBoxButton.OK,
                            MessageBoxImage.Error);
                    }
                    catch
                    {
                        // The Burn log still contains the original initialization failure.
                    }
                }
            }

            Engine.Quit(exitCode);
        }

        private int RunInstaller()
        {
            string configuredLanguage = CommandLineValue("SelectedLanguage");
            string configuredFolder = CommandLineValue("InstallFolder") ?? Engine.StringVariables["InstallFolder"];
            hasFolderOverride = !string.IsNullOrWhiteSpace(configuredFolder);
            verifyPlanOnly = CommandLineValue("VerifyPlanOnly") == "1" || Engine.NumericVariables["VerifyPlanOnly"] == 1;
            launchAfterInstall = CommandLineValue("LaunchAfterInstall") == "1";
            // A silent run never removes user data on its own: the in-app
            // updater uses that path, and it must not take the data with it.
            // A script can still ask for it explicitly.
            removeUserData = CommandLineValue("RemoveUserData") == "1"
                || Engine.NumericVariables["RemoveUserData"] == 1;

            selectedLanguage = ResolveLanguageCode(configuredLanguage);
            ApplySupportedUiCulture(selectedLanguage);
            installFolder = ResolveInitialInstallFolder(configuredFolder);
            Engine.StringVariables["SelectedLanguage"] = selectedLanguage;
            Engine.StringVariables["InstallFolder"] = installFolder;

            var application = new Application { ShutdownMode = ShutdownMode.OnMainWindowClose };
            form = new InstallerWindow(this, selectedLanguage, installFolder, Command.Action, Command.Display == Display.Full);
            DetectRelatedBundle += OnDetectRelatedBundle;
            DetectRelatedMsiPackage += OnDetectRelatedMsiPackage;
            DetectComplete += OnDetectComplete;
            PlanPackageBegin += OnPlanPackageBegin;
            PlanRelatedBundle += OnPlanRelatedBundle;
            PlanComplete += OnPlanComplete;
            Progress += OnProgress;
            ApplyComplete += OnApplyComplete;

            form.Loaded += (_, __) => Engine.Detect();
            application.Run(form);
            return form.ExitCode;
        }

        internal void BeginAction(string languageCode, string installFolder, LaunchAction action)
        {
            if (applying)
            {
                return;
            }

            if (!TryNormalizeInstallFolder(installFolder, out string normalizedFolder))
            {
                form.ShowInvalidInstallFolder();
                return;
            }

            selectedLanguage = IsSupportedLanguage(languageCode) ? languageCode : "en-US";
            removeUserData = form.RemoveUserDataRequested;
            this.installFolder = normalizedFolder;
            Engine.StringVariables["SelectedLanguage"] = selectedLanguage;
            Engine.StringVariables["InstallFolder"] = this.installFolder;
            plannedAction = NormalizeAction(action);
            form.ShowPlanning(plannedAction);
            Engine.Plan(plannedAction);
        }

        internal void Cancel()
        {
            if (!applying)
            {
                form.ExitCode = 1602;
                form.Close();
            }
        }

        private void OnDetectRelatedBundle(object sender, DetectRelatedBundleEventArgs e)
        {
            if (e.RelationType == RelationType.Upgrade && e.Operation == RelatedOperation.None)
            {
                sameVersionRelatedBundles.Add(e.ProductCode);
            }
        }

        private void OnDetectRelatedMsiPackage(object sender, DetectRelatedMsiPackageEventArgs e)
        {
            if (e.PackageId != "MsiBase")
            {
                return;
            }

            if (!hasFolderOverride)
            {
                string installedFolder = InstalledProductLocation(e.ProductCode);
                if (!string.IsNullOrWhiteSpace(installedFolder))
                {
                    if (TryNormalizeInstallFolder(installedFolder, out string normalizedFolder))
                    {
                        installFolder = normalizedFolder;
                        Engine.StringVariables["InstallFolder"] = installFolder;
                        form.SetInstallFolder(installFolder);
                    }
                    else
                    {
                        Engine.Log(LogLevel.Standard, "Warning: ignoring invalid legacy installation location reported by MSI: " + installedFolder);
                    }
                }
            }
        }

        private void OnDetectComplete(object sender, DetectCompleteEventArgs e)
        {
            if (e.Status < 0)
            {
                form.ShowFailure(e.Status, Localized(selectedLanguage, "Detection failed.", "检测失败。", "偵測失敗。"));
                CloseIfUnattended();
                return;
            }

            if (Command.Display != Display.Full)
            {
                BeginAction(selectedLanguage, installFolder, Command.Action);
                return;
            }

            form.ShowWelcome();
        }

        private void OnPlanPackageBegin(object sender, PlanPackageBeginEventArgs e)
        {
            if (e.PackageId != "MsiBase" || plannedAction == LaunchAction.Layout || plannedAction == LaunchAction.Cache)
            {
                return;
            }

            if (plannedAction == LaunchAction.Uninstall)
            {
                e.State = RequestState.Absent;
                return;
            }

            e.State = plannedAction == LaunchAction.Repair ? RequestState.Repair : RequestState.Present;
        }

        private void OnPlanRelatedBundle(object sender, PlanRelatedBundleEventArgs e)
        {
            if (plannedAction == LaunchAction.Install && sameVersionRelatedBundles.Contains(e.BundleId))
            {
                e.State = RequestState.Absent;
            }
        }

        private void OnPlanComplete(object sender, PlanCompleteEventArgs e)
        {
            if (e.Status < 0)
            {
                form.ShowFailure(e.Status, Localized(selectedLanguage, "Planning failed.", "安装规划失败。", "安裝規劃失敗。"));
                CloseIfUnattended();
                return;
            }

            if (verifyPlanOnly)
            {
                form.CloseAfterPlanVerification();
                return;
            }

            IntPtr applyWindowHandle = form.Handle;
            if (applyWindowHandle == IntPtr.Zero)
            {
                form.ShowFailure(unchecked((int)0x80004005), Localized(selectedLanguage, "Setup window initialization failed.", "安装窗口初始化失败。", "安裝視窗初始化失敗。"));
                CloseIfUnattended();
                return;
            }

            applying = true;
            form.ShowApplying();
            CloseRunningApplication();
            Engine.Apply(applyWindowHandle);
        }

        private void OnProgress(object sender, ProgressEventArgs e) => form.SetProgress(e.OverallPercentage);

        private void OnApplyComplete(object sender, ApplyCompleteEventArgs e)
        {
            applying = false;
            if (e.Status >= 0 && plannedAction == LaunchAction.Uninstall && removeUserData)
            {
                RemoveUserDataFolder();
            }
            form.ExitCode = e.Status;
            form.ShowComplete(e.Status, e.Restart);
            if (Command.Display != Display.Full)
            {
                if (e.Status >= 0 && e.Restart == ApplyRestart.None && launchAfterInstall &&
                    (plannedAction == LaunchAction.Install || plannedAction == LaunchAction.Repair))
                {
                    try
                    {
                        LaunchInstalledApp();
                    }
                    catch (Exception error)
                    {
                        Engine.Log(LogLevel.Error, "CodeStudio Lite was updated but could not be restarted: " + error.Message);
                    }
                }
                form.CloseOnUiThread();
            }
        }

        internal void LaunchInstalledApp()
        {
            string executablePath = ResolveInstalledExecutablePath();
            string workingDirectory = Path.GetDirectoryName(executablePath);

            Process.Start(new ProcessStartInfo
            {
                FileName = executablePath,
                WorkingDirectory = workingDirectory,
                UseShellExecute = true,
            });
        }

        private string ResolveInstalledExecutablePath()
        {
            var folders = new List<string> { installFolder };
            folders.AddRange(RegistryInstallLocations(null));
            folders.Add(DefaultInstallFolder());

            var attemptedLocations = new List<string>();
            var visited = new HashSet<string>(StringComparer.OrdinalIgnoreCase);
            foreach (string folder in folders)
            {
                if (!TryNormalizeInstallFolder(folder, out string normalizedFolder))
                {
                    continue;
                }

                string candidate = Path.Combine(normalizedFolder, "codestudio-lite.exe");
                if (!visited.Add(candidate))
                {
                    continue;
                }

                attemptedLocations.Add(candidate);
                if (File.Exists(candidate))
                {
                    installFolder = normalizedFolder;
                    Engine.StringVariables["InstallFolder"] = installFolder;
                    return candidate;
                }
            }

            string attempted = attemptedLocations.Count == 0
                ? "No valid installation locations were available."
                : "Tried: " + string.Join("; ", attemptedLocations);
            throw new FileNotFoundException(
                "The installed application executable was not found. " + attempted,
                attemptedLocations.Count > 0 ? attemptedLocations[0] : null);
        }

        /// <summary>
        /// Deletes the user data folder after a successful uninstall, when the
        /// user asked for it. Failing to delete it does not fail the uninstall
        /// that already succeeded, so this only logs.
        /// </summary>
        private void RemoveUserDataFolder()
        {
            string folder = InstallerWindow.UserDataFolder;
            string userProfile = Environment.GetFolderPath(Environment.SpecialFolder.UserProfile);

            // Guard the path before a recursive delete: it has to be the
            // folder we own, directly inside this user's profile. A blank or
            // unexpected value must never reach Directory.Delete.
            bool isExpectedFolder = !string.IsNullOrEmpty(userProfile)
                && string.Equals(
                    folder,
                    Path.Combine(userProfile, ".codestudio-lite"),
                    StringComparison.OrdinalIgnoreCase);
            if (!isExpectedFolder)
            {
                Engine.Log(LogLevel.Error, "Refusing to delete an unexpected user data folder: " + folder);
                return;
            }

            try
            {
                if (Directory.Exists(folder))
                {
                    Directory.Delete(folder, true);
                    Engine.Log(LogLevel.Standard, "Removed the CodeStudio Lite user data folder.");
                }
            }
            catch (Exception error)
            {
                Engine.Log(LogLevel.Error, "Could not remove the user data folder: " + error.Message);
            }
        }

        /// <summary>
        /// Ends any running copy of the application before the engine touches
        /// its files. Uninstall used to leave it resident, and an update
        /// replaced files underneath a live process, because nothing in the
        /// chain closed it: the bundle has no CloseApplication element and the
        /// MSI runs with its internal UI suppressed, so the files-in-use
        /// prompt never appears. Only the copy in the install folder we are
        /// operating on is touched.
        /// </summary>
        private void CloseRunningApplication()
        {
            if (plannedAction == LaunchAction.Layout || plannedAction == LaunchAction.Cache)
            {
                return;
            }

            string installRoot;
            try
            {
                installRoot = Path.GetDirectoryName(ResolveInstalledExecutablePath());
            }
            catch (Exception error)
            {
                // A first install has nothing to close, and a missing
                // executable must never block the action.
                Engine.Log(LogLevel.Standard, "No installed executable to close: " + error.Message);
                return;
            }

            foreach (Process process in Process.GetProcessesByName("codestudio-lite"))
            {
                using (process)
                {
                    try
                    {
                        if (!IsProcessInFolder(process, installRoot))
                        {
                            continue;
                        }

                        Engine.Log(LogLevel.Standard, "Closing CodeStudio Lite process " + process.Id + " before applying changes.");
                        if (process.CloseMainWindow() && process.WaitForExit(GracefulExitMilliseconds))
                        {
                            continue;
                        }

                        process.Kill();
                        process.WaitForExit(ForcedExitMilliseconds);
                    }
                    catch (Exception error)
                    {
                        // Access denied, or the process exited between the
                        // enumeration and the call. Neither is fatal: the
                        // engine still gets its normal chance to proceed.
                        Engine.Log(LogLevel.Error, "Could not close a running CodeStudio Lite process: " + error.Message);
                    }
                }
            }
        }

        private static bool IsProcessInFolder(Process process, string folder)
        {
            if (string.IsNullOrEmpty(folder))
            {
                return false;
            }

            string executablePath;
            try
            {
                executablePath = process.MainModule.FileName;
            }
            catch
            {
                // Reading the module of a process owned by another user or a
                // different bitness throws. Skip it rather than guess.
                return false;
            }

            string processFolder = Path.GetDirectoryName(executablePath);
            return !string.IsNullOrEmpty(processFolder)
                && string.Equals(
                    processFolder.TrimEnd(Path.DirectorySeparatorChar),
                    folder.TrimEnd(Path.DirectorySeparatorChar),
                    StringComparison.OrdinalIgnoreCase);
        }

        private void CloseIfUnattended()
        {
            if (Command.Display != Display.Full)
            {
                form.CloseOnUiThread();
            }
        }

        private static LaunchAction NormalizeAction(LaunchAction action)
        {
            switch (action)
            {
                case LaunchAction.Uninstall:
                case LaunchAction.Repair:
                case LaunchAction.Layout:
                case LaunchAction.Cache:
                    return action;
                default:
                    return LaunchAction.Install;
            }
        }

        private static bool IsSupportedLanguage(string languageCode) =>
            languageCode == "zh-CN" || languageCode == "zh-TW" || languageCode == "en-US";

        private string CommandLineValue(string name)
        {
            string prefix = name + "=";
            foreach (string argument in Command.GetCommandLineArgs())
            {
                string normalized = argument.TrimStart('-', '/');
                if (normalized.StartsWith(prefix, StringComparison.OrdinalIgnoreCase))
                {
                    return normalized.Substring(prefix.Length).Trim('"');
                }
            }
            return null;
        }

        private string ResolveInitialInstallFolder(string configuredFolder)
        {
            string candidate = hasFolderOverride ? configuredFolder : RegistryInstallLocation(null);
            if (TryNormalizeInstallFolder(candidate, out string normalizedFolder))
            {
                return normalizedFolder;
            }

            if (!string.IsNullOrWhiteSpace(candidate))
            {
                Engine.Log(LogLevel.Standard, "Warning: ignoring invalid legacy installation location: " + candidate);
            }

            return NormalizeInstallFolder(DefaultInstallFolder());
        }

        private static string InstalledProductLocation(string productCode)
        {
            string location = InstalledProductValue(productCode, "InstallLocation");
            return !string.IsNullOrWhiteSpace(location) ? location : RegistryInstallLocation(productCode);
        }

        private static string RegistryInstallLocation(string productCode)
        {
            IList<string> locations = RegistryInstallLocations(productCode);
            return locations.Count > 0 ? locations[0] : null;
        }

        private static IList<string> RegistryInstallLocations(string productCode)
        {
            const string uninstallPath = @"SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall";
            RegistryHive[] hives = { RegistryHive.LocalMachine, RegistryHive.CurrentUser };
            RegistryView[] views = { RegistryView.Registry64, RegistryView.Registry32 };
            var locations = new List<string>();
            var visited = new HashSet<string>(StringComparer.OrdinalIgnoreCase);

            foreach (RegistryHive hive in hives)
            {
                foreach (RegistryView view in views)
                {
                    try
                    {
                        using (RegistryKey baseKey = RegistryKey.OpenBaseKey(hive, view))
                        using (RegistryKey uninstallKey = baseKey.OpenSubKey(uninstallPath))
                        {
                            if (uninstallKey == null)
                            {
                                continue;
                            }

                            if (!string.IsNullOrWhiteSpace(productCode))
                            {
                                using (RegistryKey productKey = uninstallKey.OpenSubKey(productCode))
                                {
                                    string exactLocation = RegistryValue(productKey, "InstallLocation");
                                    if (!string.IsNullOrWhiteSpace(exactLocation) && visited.Add(exactLocation))
                                    {
                                        locations.Add(exactLocation);
                                    }
                                }
                            }

                            foreach (string subKeyName in uninstallKey.GetSubKeyNames())
                            {
                                using (RegistryKey productKey = uninstallKey.OpenSubKey(subKeyName))
                                {
                                    string displayName = RegistryValue(productKey, "DisplayName");
                                    if (!string.Equals(displayName, "CodeStudio Lite", StringComparison.OrdinalIgnoreCase))
                                    {
                                        continue;
                                    }

                                    string location = RegistryValue(productKey, "InstallLocation");
                                    if (!string.IsNullOrWhiteSpace(location) && visited.Add(location))
                                    {
                                        locations.Add(location);
                                    }
                                }
                            }
                        }
                    }
                    catch (UnauthorizedAccessException)
                    {
                        // Continue with the remaining registry scopes.
                    }
                    catch (SecurityException)
                    {
                        // Continue with the remaining registry scopes.
                    }
                    catch (IOException)
                    {
                        // Continue with the remaining registry scopes.
                    }
                }
            }

            return locations;
        }

        private static string RegistryValue(RegistryKey key, string name) => key == null ? null : key.GetValue(name) as string;

        private static string InstalledProductValue(string productCode, string property)
        {
            uint length = 0;
            MsiGetProductInfo(productCode, property, null, ref length);
            if (length == 0)
            {
                return null;
            }

            length++;
            var value = new StringBuilder((int)length);
            return MsiGetProductInfo(productCode, property, value, ref length) == 0 ? value.ToString() : null;
        }

        [DllImport("msi.dll", CharSet = CharSet.Unicode)]
        private static extern uint MsiGetProductInfo(string product, string property, StringBuilder valueBuffer, ref uint valueBufferLength);

        [DllImport("kernel32.dll")]
        private static extern ushort GetUserDefaultUILanguage();

        [DllImport("kernel32.dll")]
        private static extern ushort GetSystemDefaultUILanguage();

        private static string DefaultInstallFolder()
        {
            string programFiles = Environment.GetEnvironmentVariable("ProgramW6432");
            if (string.IsNullOrWhiteSpace(programFiles))
            {
                programFiles = Environment.GetFolderPath(Environment.SpecialFolder.ProgramFiles);
            }
            return Path.Combine(programFiles, "CodeStudio Lite");
        }

        private static string NormalizeInstallFolder(string folder)
        {
            if (string.IsNullOrWhiteSpace(folder) || !Path.IsPathRooted(folder))
            {
                throw new ArgumentException("The installation folder must be an absolute path.");
            }
            return Path.GetFullPath(folder.Trim().Trim('"')).TrimEnd(Path.DirectorySeparatorChar, Path.AltDirectorySeparatorChar);
        }

        private static bool TryNormalizeInstallFolder(string folder, out string normalizedFolder)
        {
            try
            {
                normalizedFolder = NormalizeInstallFolder(folder);
                return true;
            }
            catch (ArgumentException)
            {
            }
            catch (NotSupportedException)
            {
            }
            catch (PathTooLongException)
            {
            }
            catch (SecurityException)
            {
            }

            normalizedFolder = null;
            return false;
        }

        private static string DefaultLanguageCode()
        {
            string name = CultureName(GetUserDefaultUILanguage());
            if (string.IsNullOrWhiteSpace(name))
            {
                name = CultureName(GetSystemDefaultUILanguage());
            }
            if (string.IsNullOrWhiteSpace(name))
            {
                name = CultureInfo.CurrentUICulture.Name;
            }

            return NormalizeLanguageCode(name);
        }

        private static string ResolveLanguageCode(string configuredLanguage)
        {
            return string.IsNullOrWhiteSpace(configuredLanguage)
                ? DefaultLanguageCode()
                : NormalizeLanguageCode(configuredLanguage);
        }

        private static string NormalizeLanguageCode(string name)
        {
            if (string.Equals(name, "zh-TW", StringComparison.OrdinalIgnoreCase) ||
                string.Equals(name, "zh-HK", StringComparison.OrdinalIgnoreCase) ||
                string.Equals(name, "zh-MO", StringComparison.OrdinalIgnoreCase))
            {
                return "zh-TW";
            }
            if (!string.IsNullOrWhiteSpace(name) && name.StartsWith("zh", StringComparison.OrdinalIgnoreCase))
            {
                return "zh-CN";
            }
            return "en-US";
        }

        private static void ApplySupportedUiCulture(string languageCode)
        {
            var culture = CultureInfo.GetCultureInfo(IsSupportedLanguage(languageCode) ? languageCode : "en-US");
            CultureInfo.DefaultThreadCurrentUICulture = culture;
            Thread.CurrentThread.CurrentUICulture = culture;
        }

        private static string CultureName(ushort languageId)
        {
            try
            {
                return CultureInfo.GetCultureInfo(languageId).Name;
            }
            catch (CultureNotFoundException)
            {
                return null;
            }
        }

        internal static string Localized(string language, string english, string simplifiedChinese, string traditionalChinese)
        {
            return language == "zh-CN" ? simplifiedChinese : language == "zh-TW" ? traditionalChinese : english;
        }
    }
}
