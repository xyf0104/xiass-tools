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
        private bool cancelRequested;
        private bool hasFolderOverride;
        private bool verifyPlanOnly;
        private bool launchAfterInstall;
        private bool removeUserData;
        private readonly HashSet<string> staleBundleRegistrations = new HashSet<string>(StringComparer.OrdinalIgnoreCase);

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
            // The bundle is named for the installer so that the running process
            // is identifiable as one. What gets left in the installed programs
            // list is the product, so it is named for the product.
            Engine.StringVariables["WixBundleName"] = "CodeStudio Lite";

            bool interactive = Command.Display == Display.Full;
            var application = new Application
            {
                ShutdownMode = interactive
                    ? ShutdownMode.OnMainWindowClose
                    : ShutdownMode.OnExplicitShutdown,
            };
            form = new InstallerWindow(this, selectedLanguage, installFolder, Command.Action, interactive);
            // Note that PlanRelatedBundle is deliberately not handled. Asking
            // the engine to remove a same-version sibling makes it run that
            // sibling's own cached installer, as a separate process with its own
            // window, running whatever code it shipped with — so a leftover from
            // a broken build stays broken and hangs the install that spawned it.
            // The leftovers are cleared here instead, in this process, once the
            // real work has succeeded. One install is one process.
            DetectRelatedBundle += OnDetectRelatedBundle;
            DetectRelatedMsiPackage += OnDetectRelatedMsiPackage;
            DetectComplete += OnDetectComplete;
            PlanPackageBegin += OnPlanPackageBegin;
            PlanComplete += OnPlanComplete;
            Progress += OnProgress;
            ApplyComplete += OnApplyComplete;

            if (interactive)
            {
                form.Loaded += (_, __) => Engine.Detect();
                application.Run(form);
                return form.ExitCode;
            }

            // A silent run puts nothing on screen. It still needs the window,
            // because Apply is handed its handle, but showing it was what left
            // an unpainted frame sitting on the desktop for every child bundle
            // the engine spawned. EnsureHandle builds the handle without a
            // showing, and detection starts from the dispatcher because Loaded
            // only fires for a window that appears.
            form.EnsureHandleWithoutShowing();
            application.Dispatcher.BeginInvoke(new Action(() => Engine.Detect()));
            application.Run();
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

            // Retry comes back through here, and it must not inherit the
            // cancellation that ended the previous attempt.
            cancelRequested = false;
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
                return;
            }

            // Mid-apply the engine owns the work, so cancelling is a request
            // rather than an act: the next progress callback passes it on, and
            // the engine unwinds what it has already done. Until then the run
            // continues, which is why the window reports that it is stopping
            // instead of pretending it has stopped.
            if (cancelRequested)
            {
                return;
            }

            cancelRequested = true;
            Engine.Log(LogLevel.Standard, "Cancellation requested; asking the engine to stop and roll back.");
            form.ShowCancelling();
        }

        private void OnDetectRelatedBundle(object sender, DetectRelatedBundleEventArgs e)
        {
            // An upgrade relation the engine plans to do nothing about is a
            // registration for this same version, left by an earlier install of
            // it. Note it now — once this install registers itself, its own
            // entry would be indistinguishable from these.
            if (e.RelationType == RelationType.Upgrade && e.Operation == RelatedOperation.None)
            {
                staleBundleRegistrations.Add(e.ProductCode);
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

        private void OnProgress(object sender, ProgressEventArgs e)
        {
            form.SetProgress(e.OverallPercentage);
            if (cancelRequested)
            {
                e.Result = Result.Cancel;
            }
        }

        private void OnApplyComplete(object sender, ApplyCompleteEventArgs e)
        {
            applying = false;
            if (e.Status >= 0 && plannedAction == LaunchAction.Uninstall && removeUserData)
            {
                RemoveUserDataFolder();
            }

            // Only once the new install is registered and working: until then
            // the older registration is the one thing that can still uninstall
            // the product.
            if (e.Status >= 0 && (plannedAction == LaunchAction.Install || plannedAction == LaunchAction.Repair))
            {
                RemoveStaleBundleRegistrations();
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
        /// Clears registrations left by earlier installs of this same version,
        /// so installing over one replaces it instead of listing beside it.
        ///
        /// Every build of the installer is stamped with a fresh bundle id, so
        /// two builds of one version look like unrelated siblings to Windows
        /// and both stay listed. The engine can be asked to remove a sibling,
        /// but it does that by launching that sibling's own cached installer:
        /// another process, another window, running the code it shipped with
        /// rather than this one. Deleting the registration directly keeps the
        /// install to a single process, and reaches leftovers whose cached
        /// installer no longer works.
        ///
        /// Only the registration goes. The installed files belong to the MSI,
        /// which this install has already upgraded in place.
        ///
        /// Runs after the install has succeeded, and never fails it: a leftover
        /// listing is untidy, not broken.
        /// </summary>
        private void RemoveStaleBundleRegistrations()
        {
            const string uninstallPath = @"SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall";
            RegistryView[] views = { RegistryView.Registry64, RegistryView.Registry32 };

            foreach (string bundleId in staleBundleRegistrations)
            {
                foreach (RegistryView view in views)
                {
                    try
                    {
                        using (RegistryKey baseKey = RegistryKey.OpenBaseKey(RegistryHive.LocalMachine, view))
                        using (RegistryKey uninstall = baseKey.OpenSubKey(uninstallPath, true))
                        {
                            if (uninstall == null)
                            {
                                continue;
                            }

                            string cachePath;
                            using (RegistryKey entry = uninstall.OpenSubKey(bundleId))
                            {
                                if (entry == null)
                                {
                                    continue;
                                }

                                cachePath = entry.GetValue("BundleCachePath") as string;
                            }

                            uninstall.DeleteSubKeyTree(bundleId, false);
                            Engine.Log(LogLevel.Standard, "Removed the registration left by an earlier install of this version: " + bundleId);
                            RemoveCachedBundle(cachePath);
                        }
                    }
                    catch (Exception error)
                    {
                        Engine.Log(LogLevel.Error, "Could not remove the registration for " + bundleId + ": " + error.Message);
                    }
                }
            }
        }

        /// The cached copy of a bundle exists to service its own uninstall, so
        /// it has no purpose once the registration pointing at it is gone.
        private void RemoveCachedBundle(string cachePath)
        {
            if (string.IsNullOrWhiteSpace(cachePath))
            {
                return;
            }

            try
            {
                string folder = Path.GetDirectoryName(cachePath);
                if (!string.IsNullOrEmpty(folder) && Directory.Exists(folder))
                {
                    Directory.Delete(folder, true);
                }
            }
            catch (Exception error)
            {
                Engine.Log(LogLevel.Error, "Could not remove a cached installer: " + error.Message);
            }
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

        private bool IsProcessInFolder(Process process, string folder)
        {
            if (string.IsNullOrEmpty(folder))
            {
                return false;
            }

            string executablePath = ProcessImagePath(process);
            if (string.IsNullOrEmpty(executablePath))
            {
                // Say so rather than skip in silence. A process whose location
                // cannot be read is the one case where this method reports a
                // stranger and a live copy of the application the same way.
                Engine.Log(LogLevel.Error, "Could not read the location of process " + process.Id + "; leaving it running.");
                return false;
            }

            string processFolder = Path.GetDirectoryName(executablePath);
            return !string.IsNullOrEmpty(processFolder)
                && string.Equals(
                    processFolder.TrimEnd(Path.DirectorySeparatorChar),
                    folder.TrimEnd(Path.DirectorySeparatorChar),
                    StringComparison.OrdinalIgnoreCase);
        }

        /// <summary>
        /// The full path of a running process, read in a way that survives the
        /// bitness gap between this bootstrapper and the application.
        ///
        /// Burn hosts its application as a 32-bit process while the product
        /// ships 64-bit, and <c>Process.MainModule</c> throws across that
        /// boundary. It used to be the only way this class located a process,
        /// so every running copy looked like somebody else's and the installer
        /// left it open — then stalled part-way through, waiting on files the
        /// live process still held. <c>QueryFullProcessImageName</c> has no such
        /// limit and needs only the limited-information right.
        /// </summary>
        private static string ProcessImagePath(Process process)
        {
            IntPtr handle = OpenProcess(ProcessQueryLimitedInformation, false, process.Id);
            if (handle == IntPtr.Zero)
            {
                return null;
            }

            try
            {
                var buffer = new StringBuilder(1024);
                int capacity = buffer.Capacity;
                return QueryFullProcessImageName(handle, 0, buffer, ref capacity)
                    ? buffer.ToString()
                    : null;
            }
            finally
            {
                CloseHandle(handle);
            }
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

        /// Enough to ask a process where it lives, and the only right a 32-bit
        /// caller reliably gets against a 64-bit process.
        private const int ProcessQueryLimitedInformation = 0x1000;

        [DllImport("kernel32.dll", SetLastError = true)]
        private static extern IntPtr OpenProcess(int desiredAccess, bool inheritHandle, int processId);

        [DllImport("kernel32.dll", SetLastError = true, CharSet = CharSet.Unicode)]
        private static extern bool QueryFullProcessImageName(IntPtr process, int flags, StringBuilder imageName, ref int size);

        [DllImport("kernel32.dll", SetLastError = true)]
        private static extern bool CloseHandle(IntPtr handle);

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
