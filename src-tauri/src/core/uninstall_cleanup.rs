//! Optional cleanups performed when an agent is uninstalled.
//!
//! Ported from funtoolkit, which shares this project's lineage. Two properties
//! carry the whole design:
//!
//! A directory shared with unrelated tools is never removed. `~/.local/bin` and
//! the Homebrew prefixes hold binaries from pipx, uv, Claude Code's native
//! installer and much else; taking one off PATH while uninstalling a single
//! agent breaks software the user never asked us to touch.
//!
//! The Windows user PATH is read from the registry unexpanded and written back
//! with its original value kind. `[Environment]::GetEnvironmentVariable('PATH','User')`
//! expands `%VAR%`, so pruning that result and writing it back would freeze
//! `%JAVA_HOME%\bin` into whatever it resolved to at the time.

#![cfg_attr(not(target_os = "windows"), allow(dead_code))]

use serde::Deserialize;
use std::path::{Path, PathBuf};

/// The host a lookup is resolving for.
///
/// A parameter rather than a `cfg!()` branch so the macOS and Unix search
/// orders can be exercised from any development machine.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum HostPlatform {
    Windows,
    Macos,
    Other,
}

impl HostPlatform {
    pub fn current() -> Self {
        if cfg!(target_os = "windows") {
            Self::Windows
        } else if cfg!(target_os = "macos") {
            Self::Macos
        } else {
            Self::Other
        }
    }
}

/// Directories that belong to no single agent.
///
/// `~/.local/bin`, the Homebrew prefixes, and every npm prefix are shared by
/// unrelated tools, so removing one while uninstalling a single agent would
/// break software the user never asked us to touch. Both the path-removal
/// strategies and the PATH pruning check against this list.
pub const SHARED_BIN_DIRECTORIES: &[&str] = &[
    ".local/bin",
    ".local/share/mise/shims",
    ".asdf/shims",
    ".bun/bin",
    ".yarn/bin",
    ".volta/bin",
    ".npm-global/bin",
    "Library/pnpm",
    "AppData/Roaming/npm",
];

/// Absolute directories that are shared regardless of the user's home.
pub const SHARED_ABSOLUTE_DIRECTORIES: &[&str] = &[
    "/usr/local/bin",
    "/usr/bin",
    "/bin",
    "/opt/homebrew/bin",
    "/usr/local/sbin",
];

/// Whether a directory may be deleted or removed from PATH while uninstalling.
///
/// Compares against the shared list after normalization so a trailing
/// separator or a mixed separator style cannot smuggle a shared directory
/// through.
pub fn is_shared_directory(candidate: &Path, home_dir: &Path) -> bool {
    let normalized = normalize_directory(candidate);
    if SHARED_ABSOLUTE_DIRECTORIES
        .iter()
        .any(|shared| normalize_directory(Path::new(shared)) == normalized)
    {
        return true;
    }
    SHARED_BIN_DIRECTORIES
        .iter()
        .any(|shared| normalize_directory(&home_dir.join(shared)) == normalized)
}

/// Lowercased, separator-normalized, trailing-separator-free form.
///
/// Windows paths are compared case-insensitively; doing the same on Unix is a
/// deliberate over-match, because treating `~/.Local/bin` as shared is the safe
/// direction to be wrong in.
pub fn normalize_directory(path: &Path) -> String {
    let text = path.to_string_lossy().replace('\\', "/");
    let trimmed = text.trim().trim_matches('"');
    trimmed
        .trim_end_matches('/')
        .to_string()
        .to_ascii_lowercase()
}

/// Installer overrides read from the environment.
///
/// These are resolved by the caller and passed in rather than read here, so
/// every path-deciding function stays a pure function of its arguments. A
/// function that consults the environment cannot be tested: it silently
/// answers for the developer's machine instead of the case under test.
#[derive(Debug, Clone, Default)]
pub struct InstallerOverrides {
    /// `GROK_BIN_DIR`
    pub grok_bin_dir: Option<PathBuf>,
    /// `HERMES_HOME`
    pub hermes_home: Option<PathBuf>,
}

impl InstallerOverrides {
    pub fn from_env() -> Self {
        Self {
            grok_bin_dir: std::env::var_os("GROK_BIN_DIR").map(PathBuf::from),
            hermes_home: std::env::var_os("HERMES_HOME").map(PathBuf::from),
        }
    }
}

/// The Grok install root, honoring the installer's own override.
fn grok_root(home_dir: &Path) -> PathBuf {
    home_dir.join(".grok")
}

fn grok_bin_dir(home_dir: &Path, overrides: &InstallerOverrides) -> PathBuf {
    overrides
        .grok_bin_dir
        .clone()
        .unwrap_or_else(|| grok_root(home_dir).join("bin"))
}

/// The Hermes data root. The installers default this to `~/.hermes` on Unix and
/// `%LOCALAPPDATA%\hermes` on Windows, both overridable by `HERMES_HOME`.
fn hermes_root(
    home_dir: &Path,
    local_app_data_dir: &Path,
    platform: HostPlatform,
    overrides: &InstallerOverrides,
) -> PathBuf {
    overrides.hermes_home.clone().unwrap_or_else(|| {
        if platform == HostPlatform::Windows {
            local_app_data_dir.join("hermes")
        } else {
            home_dir.join(".hermes")
        }
    })
}

/// Directories the Grok installer owns.
///
/// `~/.grok/config.toml`, `managed_config.toml` and `requirements.toml` are the
/// agent's configuration and are deliberately absent: config removal is a
/// separate, opt-in step, so the install root itself is never a target.
pub fn grok_removable_paths(
    home_dir: &Path,
    overrides: &InstallerOverrides,
) -> Vec<PathBuf> {
    let root = grok_root(home_dir);
    let mut paths = vec![
        grok_bin_dir(home_dir, overrides),
        root.join("downloads"),
        root.join("completions"),
        // funtoolkit's own version cache, not the installer's.
        root.join("version.json"),
    ];
    paths.retain(|path| !is_shared_directory(path, home_dir));
    paths.dedup();
    paths
}

/// Directories the Hermes installer owns.
///
/// Only the tooling subdirectories are listed. The data root also holds
/// `sessions`, `memories`, `logs`, `hooks` and `skills`, which are the user's
/// work, plus `.env` and `config.yaml`, which are configuration — none of those
/// belong to a program-only uninstall, so the root is never removed wholesale.
///
/// The Unix launchers live in the shared `~/.local/bin`; the caller removes
/// those individually after confirming each one belongs to Hermes.
pub fn hermes_removable_paths(
    home_dir: &Path,
    local_app_data_dir: &Path,
    platform: HostPlatform,
    overrides: &InstallerOverrides,
) -> Vec<PathBuf> {
    let root = hermes_root(home_dir, local_app_data_dir, platform, overrides);
    let mut paths = vec![root.join("hermes-agent"), root.join("bin")];
    if platform == HostPlatform::Windows {
        // The Windows installer bundles its own PortableGit and Node.js.
        paths.push(root.join("git"));
        paths.push(root.join("node"));
    } else {
        paths.push(root.join("node"));
    }
    paths.retain(|path| !is_shared_directory(path, home_dir));
    paths
}

/// The Unix launcher files Hermes writes into the shared `~/.local/bin`.
///
/// These are generated bash wrappers, not symlinks, so they cannot be
/// identified by link target — the caller must confirm the contents reference
/// the Hermes install root before removing them. The `node`, `npm` and `npx`
/// symlinks the installer also places there are deliberately excluded:
/// removing those would break the user's Node.js.
pub fn hermes_launcher_files(home_dir: &Path) -> Vec<PathBuf> {
    ["hermes", "hermes-agent", "hermes-acp"]
        .iter()
        .map(|name| home_dir.join(".local/bin").join(name))
        .collect()
}

/// The launcher files Grok writes into the shared `~/.local/bin`.
///
/// `agent` is an alarmingly generic name for a file in a directory shared with
/// every other tool on the machine, so neither of these is removed without
/// first confirming it points back into the Grok install root.
pub fn grok_launcher_files(home_dir: &Path) -> Vec<PathBuf> {
    ["grok", "agent"]
        .iter()
        .map(|name| home_dir.join(".local/bin").join(name))
        .collect()
}

/// Whether a launcher file in a shared directory belongs to the agent.
///
/// Grok's launchers are symlinks into its install root; Hermes writes generated
/// shell wrappers that reference its own install directory. A file matching
/// neither shape is somebody else's and is left alone — the cost of keeping a
/// stale launcher is trivial next to deleting an unrelated `agent` binary.
pub fn launcher_belongs_to_agent(
    link_target: Option<&Path>,
    contents: Option<&str>,
    agent_root: &Path,
) -> bool {
    let root = normalize_directory(agent_root);
    if root.is_empty() {
        return false;
    }
    if let Some(target) = link_target {
        if normalize_directory(target).starts_with(&format!("{root}/")) {
            return true;
        }
    }
    let Some(contents) = contents else {
        return false;
    };
    let contents = contents.replace('\\', "/").to_ascii_lowercase();
    if contents.contains(&root) {
        return true;
    }
    // The wrapper may reference the root through `$HOME` rather than an
    // expanded absolute path, so also accept the install root's own directory
    // name. Bounded by separators so `.hermes-backup` cannot match `.hermes`.
    agent_root
        .file_name()
        .map(|name| format!("/{}/", name.to_string_lossy().to_ascii_lowercase()))
        .is_some_and(|marker| contents.contains(&marker))
}

/// Splits requested removals into what may be deleted and what must not be.
///
/// The shared-directory check runs again here rather than trusting the caller:
/// this is the last point before something is deleted from the user's disk.
pub fn partition_removable(
    paths: &[PathBuf],
    home_dir: &Path,
) -> (Vec<PathBuf>, Vec<String>) {
    let mut allowed = Vec::new();
    let mut refused = Vec::new();
    for path in paths {
        if is_shared_directory(path, home_dir) {
            refused.push(format!(
                "{} was left in place because it is shared with other tools.",
                path.display()
            ));
        } else {
            allowed.push(path.clone());
        }
    }
    (allowed, refused)
}

/// PATH directories an agent's installer added, and that its uninstall may
/// therefore remove.
///
/// Verified against the vendor install scripts: Grok adds its bin directory to
/// the Windows User PATH and to the shell profile; Hermes adds its bundled Git
/// and Node.js directories on Windows. npm, Homebrew and the desktop installers
/// add nothing, so they return an empty list and the PATH option is not offered.
pub fn installer_path_entries(
    tool_id: &str,
    home_dir: &Path,
    local_app_data_dir: &Path,
    platform: HostPlatform,
    overrides: &InstallerOverrides,
) -> Vec<PathBuf> {
    let mut entries = match tool_id {
        "grok" => vec![grok_bin_dir(home_dir, overrides)],
        "hermes" if platform == HostPlatform::Windows => {
            let root = hermes_root(home_dir, local_app_data_dir, platform, overrides);
            vec![
                root.join("git/cmd"),
                root.join("git/bin"),
                root.join("git/usr/bin"),
                root.join("node"),
            ]
        }
        // The Unix Hermes installer only ever adds `~/.local/bin`, which is
        // shared with pipx, uv and Claude Code's native installer.
        _ => Vec::new(),
    };
    entries.retain(|path| !is_shared_directory(path, home_dir));
    entries
}

/// One PATH entry, in both the form stored in the registry and the form it
/// resolves to.
///
/// Matching considers both: the entry may be stored as `%USERPROFILE%\.grok\bin`
/// while the directory being removed is known only as an absolute path.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct PathEntry {
    pub raw: String,
    pub expanded: String,
}

impl PathEntry {
    #[cfg(test)]
    fn new(raw: &str, expanded: &str) -> Self {
        Self {
            raw: raw.to_string(),
            expanded: expanded.to_string(),
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct PathPrune {
    /// The value to write back, still holding every unexpanded reference that
    /// was not removed.
    pub updated_raw: String,
    pub removed: Vec<String>,
    pub changed: bool,
}

/// Whether a PATH entry names exactly the given directory.
///
/// Deliberately an equality test after normalization, never a prefix test:
/// `C:\Users\dev\.grok\bin` must not match `C:\Users\dev\.grok\bin-old`.
pub fn path_entry_matches(entry: &PathEntry, target: &Path) -> bool {
    let target = normalize_directory(target);
    if target.is_empty() {
        return false;
    }
    normalize_directory(Path::new(&entry.raw)) == target
        || normalize_directory(Path::new(&entry.expanded)) == target
}

/// Removes the given directories from a PATH value.
///
/// Entries that are not removed are preserved verbatim and in order, including
/// unexpanded variable references and empty segments — a PATH is a user's
/// property, and "helpfully" normalizing the parts we were not asked to touch
/// is how it gets corrupted.
///
/// Shared directories are dropped from the target list before any matching, so
/// a caller that passes `~/.local/bin` cannot strip it.
pub fn prune_windows_path(
    entries: &[PathEntry],
    targets: &[PathBuf],
    home_dir: &Path,
) -> PathPrune {
    let targets: Vec<&PathBuf> = targets
        .iter()
        .filter(|target| !is_shared_directory(target, home_dir))
        .collect();
    let mut removed = Vec::new();
    let mut kept = Vec::new();
    for entry in entries {
        if targets
            .iter()
            .any(|target| path_entry_matches(entry, target))
        {
            removed.push(entry.raw.clone());
        } else {
            kept.push(entry.raw.clone());
        }
    }
    PathPrune {
        changed: !removed.is_empty(),
        updated_raw: kept.join(";"),
        removed,
    }
}

/// The marker block the Grok installer writes into a shell profile.
///
/// Verified against the vendor install script, which brackets its additions
/// with these two sentinel lines and strips the range between them before
/// re-appending on reinstall.
pub const GROK_BLOCK_OPEN: &str = "# >>> grok installer >>>";
pub const GROK_BLOCK_CLOSE: &str = "# <<< grok installer <<<";

/// Shell startup files a vendor installer may have edited.
///
/// Which one it actually chose depends on the user's login shell, so all of
/// them are examined and the ones without our markers are left untouched.
pub fn shell_profile_candidates(home_dir: &Path) -> Vec<PathBuf> {
    [
        ".bashrc",
        ".zshrc",
        ".zprofile",
        ".bash_profile",
        ".profile",
        ".config/fish/config.fish",
    ]
    .iter()
    .map(|relative| home_dir.join(relative))
    .collect()
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct StripResult {
    pub updated: String,
    pub removed_lines: Vec<String>,
}

/// Removes a delimited installer block from a shell profile.
///
/// Returns `None` when the markers are missing, unbalanced, or repeated. A
/// half-open block means the file is not in the shape the installer left it in,
/// and guessing at the boundary risks deleting the user's own configuration —
/// leaving a stale PATH entry is by far the cheaper mistake.
pub fn strip_marker_block(content: &str, open: &str, close: &str) -> Option<StripResult> {
    let lines: Vec<&str> = content.lines().collect();
    let opens: Vec<usize> = lines
        .iter()
        .enumerate()
        .filter(|(_, line)| line.contains(open))
        .map(|(index, _)| index)
        .collect();
    let closes: Vec<usize> = lines
        .iter()
        .enumerate()
        .filter(|(_, line)| line.contains(close))
        .map(|(index, _)| index)
        .collect();
    if opens.len() != 1 || closes.len() != 1 {
        return None;
    }
    let (start, end) = (opens[0], closes[0]);
    if end < start {
        return None;
    }
    // Also take a single blank line the installer left in front of its block.
    let start = if start > 0 && lines[start - 1].trim().is_empty() {
        start - 1
    } else {
        start
    };
    let removed_lines = lines[start..=end]
        .iter()
        .map(|line| line.to_string())
        .collect();
    let mut kept: Vec<&str> = Vec::with_capacity(lines.len());
    kept.extend_from_slice(&lines[..start]);
    kept.extend_from_slice(&lines[end + 1..]);
    let mut updated = kept.join("\n");
    if content.ends_with('\n') && !updated.is_empty() {
        updated.push('\n');
    }
    Some(StripResult {
        updated,
        removed_lines,
    })
}

/// Finds PATH lines that reference a directory shared with other tools.
///
/// Hermes adds `~/.local/bin` to PATH, which pipx, uv and Claude Code's native
/// installer all rely on as well. Those lines are reported so the user can
/// decide, never removed.
pub fn find_shared_path_lines(content: &str, fragment: &str) -> Vec<(usize, String)> {
    content
        .lines()
        .enumerate()
        .filter(|(_, line)| {
            let trimmed = line.trim_start();
            !trimmed.starts_with('#') && trimmed.contains("PATH") && line.contains(fragment)
        })
        .map(|(index, line)| (index + 1, line.to_string()))
        .collect()
}

/// The registry value backing the Windows user PATH.
#[derive(Debug, Clone, Deserialize)]
struct UserPathValue {
    /// The value's actual name, which may be `Path` or `PATH`. Writing back
    /// under a different casing would create a second, competing value.
    value_name: Option<String>,
    /// `String` or `ExpandString`. Writing an `ExpandString` value back as a
    /// plain `String` is what turns `%JAVA_HOME%\bin` into a frozen path.
    kind: Option<String>,
    raw: String,
    entries: Vec<UserPathEntry>,
}

#[derive(Debug, Clone, Deserialize)]
struct UserPathEntry {
    raw: String,
    expanded: String,
}

/// Reads the user PATH without expanding variable references.
///
/// `[Environment]::GetEnvironmentVariable('Path','User')` cannot be used here:
/// it expands `%VAR%`, and the expanded text is what would be written back.
#[cfg(target_os = "windows")]
const READ_USER_PATH_SCRIPT: &str = r#"
$key = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey('Environment', $false)
if (-not $key) { '{"valueName":null,"kind":null,"raw":"","entries":[]}'; exit 0 }
try {
  $name = @($key.GetValueNames() | Where-Object { $_ -ieq 'Path' }) | Select-Object -First 1
  if (-not $name) { '{"valueName":null,"kind":null,"raw":"","entries":[]}'; exit 0 }
  $kind = $key.GetValueKind($name).ToString()
  $raw = [string]$key.GetValue($name, '', [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
  $entries = @($raw -split ';' | ForEach-Object {
    [pscustomobject]@{ raw = $_; expanded = [Environment]::ExpandEnvironmentVariables($_) }
  })
  [pscustomobject]@{ valueName = $name; kind = $kind; raw = $raw; entries = $entries } |
    ConvertTo-Json -Compress -Depth 4
} finally { $key.Dispose() }
"#;

/// Writes the pruned PATH back, refusing if it changed in the meantime.
///
/// Values arrive through the environment rather than string interpolation, so
/// a PATH containing quotes cannot break out of the script.
#[cfg(target_os = "windows")]
const WRITE_USER_PATH_SCRIPT: &str = r#"
$key = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey('Environment', $true)
if (-not $key) { Write-Error 'The user environment key could not be opened.'; exit 1 }
try {
  $current = [string]$key.GetValue($env:FUNTOOLKIT_PATH_NAME, '',
    [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
  if ($current -cne $env:FUNTOOLKIT_PATH_EXPECTED) {
    Write-Error 'The user PATH changed while the uninstall was preparing; nothing was written.'
    exit 1
  }
  $kind = [Microsoft.Win32.RegistryValueKind]::$($env:FUNTOOLKIT_PATH_KIND)
  $key.SetValue($env:FUNTOOLKIT_PATH_NAME, $env:FUNTOOLKIT_PATH_UPDATED, $kind)
} finally { $key.Dispose() }
"#;

/// Removes the given directories from the Windows user PATH.
///
/// Returns the entries that were removed. Does nothing at all when none of them
/// are present, so an uninstall never rewrites a PATH it had no reason to
/// touch.
#[cfg(target_os = "windows")]
pub fn remove_from_user_path(
    targets: &[PathBuf],
    home_dir: &Path,
) -> Result<Vec<String>, String> {
    if targets.is_empty() {
        return Ok(Vec::new());
    }
    let output = run_powershell(READ_USER_PATH_SCRIPT, &[])?;
    let value: UserPathValue = serde_json::from_str(output.trim())
        .map_err(|error| format!("The user PATH could not be read: {error}"))?;
    let (Some(name), Some(kind)) = (value.value_name.clone(), value.kind.clone()) else {
        return Ok(Vec::new());
    };
    let entries: Vec<PathEntry> = value
        .entries
        .iter()
        .map(|entry| PathEntry {
            raw: entry.raw.clone(),
            expanded: entry.expanded.clone(),
        })
        .collect();
    let prune = prune_windows_path(&entries, targets, home_dir);
    if !prune.changed {
        return Ok(Vec::new());
    }
    run_powershell(
        WRITE_USER_PATH_SCRIPT,
        &[
            ("FUNTOOLKIT_PATH_NAME", name.as_str()),
            ("FUNTOOLKIT_PATH_KIND", kind.as_str()),
            ("FUNTOOLKIT_PATH_EXPECTED", value.raw.as_str()),
            ("FUNTOOLKIT_PATH_UPDATED", prune.updated_raw.as_str()),
        ],
    )?;
    broadcast_environment_change();
    Ok(prune.removed)
}

#[cfg(not(target_os = "windows"))]
pub fn remove_from_user_path(
    _targets: &[PathBuf],
    _home_dir: &Path,
) -> Result<Vec<String>, String> {
    Ok(Vec::new())
}

#[cfg(target_os = "windows")]
fn run_powershell(script: &str, environment: &[(&str, &str)]) -> Result<String, String> {
    use std::os::windows::process::CommandExt;
    use std::process::{Command, Stdio};
    const CREATE_NO_WINDOW: u32 = 0x0800_0000;

    let mut command = Command::new("powershell");
    command
        .args([
            "-NoLogo",
            "-NoProfile",
            "-NonInteractive",
            "-ExecutionPolicy",
            "Bypass",
            "-Command",
            script,
        ])
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .creation_flags(CREATE_NO_WINDOW);
    for (name, value) in environment {
        command.env(name, value);
    }
    let output = command
        .output()
        .map_err(|error| format!("The PATH cleanup could not run: {error}"))?;
    if !output.status.success() {
        return Err(format!(
            "The PATH cleanup failed: {}",
            String::from_utf8_lossy(&output.stderr).trim()
        ));
    }
    Ok(String::from_utf8_lossy(&output.stdout).into_owned())
}

/// Tells running processes that the environment changed.
///
/// A direct registry write does not notify anyone, so without this Explorer
/// keeps handing the stale PATH to every shell it launches until the user
/// signs out.
#[cfg(target_os = "windows")]
fn broadcast_environment_change() {
    use windows_sys::Win32::Foundation::{HWND, LPARAM, WPARAM};
    use windows_sys::Win32::UI::WindowsAndMessaging::{
        SendMessageTimeoutW, SMTO_ABORTIFHUNG, WM_SETTINGCHANGE,
    };

    let label: Vec<u16> = "Environment\0".encode_utf16().collect();
    let mut result: usize = 0;
    // SAFETY: the label outlives the call and the broadcast is bounded by the
    // five-second timeout, so a hung window cannot block the uninstall.
    unsafe {
        SendMessageTimeoutW(
            0xFFFF as HWND,
            WM_SETTINGCHANGE,
            0 as WPARAM,
            label.as_ptr() as LPARAM,
            SMTO_ABORTIFHUNG,
            5_000,
            &mut result,
        );
    }
}

/// What a shell-profile cleanup did, or declined to do.
#[derive(Debug, Clone, Default)]
pub struct ProfileCleanup {
    pub removed: Vec<String>,
    pub warnings: Vec<String>,
}

/// Removes an installer's marker block from the user's shell profiles.
///
/// Each edited file is copied to a `.funtoolkit-backup-<uuid>` sibling first,
/// and the replacement is written to a temporary file in the same directory and
/// renamed over the original, so an interrupted run cannot leave a truncated
/// profile behind. Files without the markers are not touched at all.
pub fn remove_profile_block(
    home_dir: &Path,
    open: &str,
    close: &str,
) -> ProfileCleanup {
    let mut cleanup = ProfileCleanup::default();
    for profile in shell_profile_candidates(home_dir) {
        let Ok(content) = std::fs::read_to_string(&profile) else {
            continue;
        };
        let Some(result) = strip_marker_block(&content, open, close) else {
            if content.contains(open) || content.contains(close) {
                cleanup.warnings.push(format!(
                    "{} was left unchanged because its installer block is incomplete; remove the PATH line by hand.",
                    profile.display()
                ));
            }
            continue;
        };
        match replace_file_contents(&profile, &result.updated) {
            Ok(()) => {
                for line in result.removed_lines {
                    cleanup
                        .removed
                        .push(format!("{}: {}", profile.display(), line.trim()));
                }
            }
            Err(error) => cleanup.warnings.push(error),
        }
    }
    cleanup
}

/// Reports shell-profile lines that put a shared directory on PATH.
pub fn report_shared_profile_lines(home_dir: &Path, fragment: &str) -> Vec<String> {
    let mut reported = Vec::new();
    for profile in shell_profile_candidates(home_dir) {
        let Ok(content) = std::fs::read_to_string(&profile) else {
            continue;
        };
        for (line_number, _) in find_shared_path_lines(&content, fragment) {
            reported.push(format!(
                "{}:{line_number} was left in place because {fragment} is shared with other tools.",
                profile.display()
            ));
        }
    }
    reported
}

/// Backs the file up, then replaces it through a same-directory rename.
fn replace_file_contents(path: &Path, updated: &str) -> Result<(), String> {
    // The installers resolve symlinked profiles and edit the real file, so
    // follow the link here too rather than replacing the link itself.
    let target = std::fs::canonicalize(path).unwrap_or_else(|_| path.to_path_buf());
    let directory = target
        .parent()
        .ok_or_else(|| format!("{} has no parent directory.", target.display()))?;
    let name = target
        .file_name()
        .and_then(|name| name.to_str())
        .ok_or_else(|| format!("{} has no file name.", target.display()))?;
    let token = uuid::Uuid::new_v4();
    let backup = directory.join(format!("{name}.funtoolkit-backup-{token}"));
    std::fs::copy(&target, &backup)
        .map_err(|error| format!("{} could not be backed up: {error}", target.display()))?;
    let temporary = directory.join(format!(".{name}.funtoolkit-tmp-{token}"));
    std::fs::write(&temporary, updated)
        .map_err(|error| format!("{} could not be rewritten: {error}", target.display()))?;
    copy_file_permissions(&target, &temporary);
    std::fs::rename(&temporary, &target).map_err(|error| {
        let _ = std::fs::remove_file(&temporary);
        format!(
            "{} could not be replaced: {error}. The original is unchanged and a copy is at {}.",
            target.display(),
            backup.display()
        )
    })
}

#[cfg(unix)]
fn copy_file_permissions(from: &Path, to: &Path) {
    if let Ok(metadata) = std::fs::metadata(from) {
        let _ = std::fs::set_permissions(to, metadata.permissions());
    }
}

#[cfg(not(unix))]
fn copy_file_permissions(_from: &Path, _to: &Path) {}

#[cfg(test)]
mod tests {
    use super::*;

    fn home() -> PathBuf {
        PathBuf::from("C:/Users/dev")
    }

    fn windows_path() -> Vec<PathEntry> {
        vec![
            PathEntry::new("%SystemRoot%\\system32", "C:\\Windows\\system32"),
            PathEntry::new("%JAVA_HOME%\\bin", "C:\\Program Files\\Java\\jdk-21\\bin"),
            PathEntry::new(
                "%USERPROFILE%\\.grok\\bin",
                "C:\\Users\\dev\\.grok\\bin",
            ),
            PathEntry::new("C:\\tools", "C:\\tools"),
        ]
    }

    /// The corruption this module exists to prevent. Reading PATH through the
    /// expanding API and writing it back would replace `%SystemRoot%` and
    /// `%JAVA_HOME%` with whatever they resolved to, permanently breaking the
    /// user's PATH the next time either variable changes.
    #[test]
    fn unexpanded_variable_references_survive_pruning() {
        let prune = prune_windows_path(
            &windows_path(),
            &[PathBuf::from("C:\\Users\\dev\\.grok\\bin")],
            &home(),
        );

        assert!(prune.changed);
        assert_eq!(
            prune.updated_raw,
            "%SystemRoot%\\system32;%JAVA_HOME%\\bin;C:\\tools"
        );
        assert_eq!(prune.removed, vec!["%USERPROFILE%\\.grok\\bin"]);
    }

    #[test]
    fn an_entry_matches_through_separator_case_and_quoting_noise() {
        let target = Path::new("C:/Users/dev/.grok/bin");
        for raw in [
            "C:\\Users\\dev\\.grok\\bin",
            "c:/users/dev/.grok/bin",
            "C:\\Users\\Dev\\.grok\\bin\\",
            "\"C:\\Users\\dev\\.grok\\bin\"",
            "  C:\\Users\\dev\\.grok\\bin  ",
        ] {
            assert!(
                path_entry_matches(&PathEntry::new(raw, raw), target),
                "{raw} should match"
            );
        }
    }

    /// Equality, never prefix — a sibling directory whose name merely starts
    /// with the target must survive.
    #[test]
    fn a_sibling_directory_with_a_shared_prefix_is_kept() {
        let entries = vec![
            PathEntry::new("C:\\Users\\dev\\.grok\\bin-old", "C:\\Users\\dev\\.grok\\bin-old"),
            PathEntry::new("C:\\Users\\dev\\.grok\\binary", "C:\\Users\\dev\\.grok\\binary"),
        ];
        let prune = prune_windows_path(&entries, &[PathBuf::from("C:/Users/dev/.grok/bin")], &home());

        assert!(!prune.changed);
        assert_eq!(
            prune.updated_raw,
            "C:\\Users\\dev\\.grok\\bin-old;C:\\Users\\dev\\.grok\\binary"
        );
    }

    /// Even if a caller asks, a directory shared with unrelated tools is never
    /// taken off PATH.
    #[test]
    fn shared_directories_are_never_pruned_even_when_requested() {
        let entries = vec![
            PathEntry::new("C:\\Users\\dev\\.local\\bin", "C:\\Users\\dev\\.local\\bin"),
            PathEntry::new("C:\\Users\\dev\\AppData\\Roaming\\npm", "C:\\Users\\dev\\AppData\\Roaming\\npm"),
            PathEntry::new("/usr/local/bin", "/usr/local/bin"),
        ];
        let prune = prune_windows_path(
            &entries,
            &[
                home().join(".local/bin"),
                home().join("AppData/Roaming/npm"),
                PathBuf::from("/usr/local/bin"),
            ],
            &home(),
        );

        assert!(!prune.changed);
        assert!(prune.removed.is_empty());
    }

    /// No match means no registry write at all.
    #[test]
    fn nothing_to_remove_reports_unchanged() {
        let prune = prune_windows_path(
            &windows_path(),
            &[PathBuf::from("C:\\Users\\dev\\.hermes\\bin")],
            &home(),
        );

        assert!(!prune.changed);
        assert!(prune.removed.is_empty());
    }

    /// A trailing separator is a real thing users have; rewriting it would be
    /// an unrequested change.
    #[test]
    fn empty_segments_are_preserved_verbatim() {
        let entries = vec![
            PathEntry::new("C:\\tools", "C:\\tools"),
            PathEntry::new("", ""),
            PathEntry::new("C:\\Users\\dev\\.grok\\bin", "C:\\Users\\dev\\.grok\\bin"),
        ];
        let prune = prune_windows_path(&entries, &[PathBuf::from("C:/Users/dev/.grok/bin")], &home());

        assert_eq!(prune.updated_raw, "C:\\tools;");
    }

    #[test]
    fn an_empty_target_never_matches_anything() {
        assert!(!path_entry_matches(
            &PathEntry::new("C:\\tools", "C:\\tools"),
            Path::new("")
        ));
    }

    const ZSHRC: &str = "# user settings\nexport EDITOR=vim\n\n# >>> grok installer >>>\nexport PATH=\"$HOME/.grok/bin:$PATH\"\nfpath=(~/.grok/completions/zsh $fpath)\n# <<< grok installer <<<\n\nalias ll='ls -la'\n";

    #[test]
    fn the_installer_block_is_removed_with_its_leading_blank_line() {
        let result = strip_marker_block(ZSHRC, GROK_BLOCK_OPEN, GROK_BLOCK_CLOSE).unwrap();

        assert_eq!(
            result.updated,
            "# user settings\nexport EDITOR=vim\n\nalias ll='ls -la'\n"
        );
        assert_eq!(result.removed_lines.len(), 5);
        assert!(result.removed_lines[1].contains("grok installer"));
        // Nothing the user wrote may be touched.
        assert!(result.updated.contains("export EDITOR=vim"));
        assert!(result.updated.contains("alias ll="));
    }

    /// A file that is not in the shape the installer left it in must be left
    /// alone: guessing at the boundary risks deleting the user's own lines.
    #[test]
    fn an_unbalanced_or_repeated_block_is_refused() {
        let missing_close = "# >>> grok installer >>>\nexport PATH=\"$HOME/.grok/bin:$PATH\"\n";
        assert!(strip_marker_block(missing_close, GROK_BLOCK_OPEN, GROK_BLOCK_CLOSE).is_none());

        let doubled = format!("{ZSHRC}{ZSHRC}");
        assert!(strip_marker_block(&doubled, GROK_BLOCK_OPEN, GROK_BLOCK_CLOSE).is_none());

        let inverted = "# <<< grok installer <<<\nfoo\n# >>> grok installer >>>\n";
        assert!(strip_marker_block(inverted, GROK_BLOCK_OPEN, GROK_BLOCK_CLOSE).is_none());
    }

    #[test]
    fn a_profile_without_the_block_is_reported_as_unchanged() {
        assert!(strip_marker_block("export EDITOR=vim\n", GROK_BLOCK_OPEN, GROK_BLOCK_CLOSE).is_none());
    }

    /// Hermes puts `~/.local/bin` on PATH, and so do pipx, uv and Claude Code's
    /// native installer. That line is reported, never removed.
    #[test]
    fn shared_path_lines_are_located_but_never_stripped() {
        let content = "# Hermes Agent — ensure ~/.local/bin is on PATH\nexport PATH=\"$HOME/.local/bin:$PATH\"\n# export PATH=\"$HOME/.local/bin:$PATH\"\n";
        let found = find_shared_path_lines(content, ".local/bin");

        // The comment line and the commented-out export are not matches.
        assert_eq!(found.len(), 1);
        assert_eq!(found[0].0, 2);
        assert!(found[0].1.contains("export PATH"));
        // Hermes writes no marker block, so there is nothing to strip even if
        // a caller tried: the shared line is only ever reported.
        assert!(strip_marker_block(content, GROK_BLOCK_OPEN, GROK_BLOCK_CLOSE).is_none());
    }

    /// The rewrite itself, against real files. Only the permission copy is
    /// platform-specific, so the backup, the same-directory rename and the
    /// selectivity are all verifiable here.
    #[test]
    fn rewriting_a_profile_backs_it_up_and_leaves_other_files_alone() {
        let home = std::env::temp_dir().join(format!("funtoolkit-profile-{}", uuid::Uuid::new_v4()));
        std::fs::create_dir_all(&home).unwrap();
        let zshrc = home.join(".zshrc");
        std::fs::write(&zshrc, ZSHRC).unwrap();
        let bashrc = home.join(".bashrc");
        std::fs::write(&bashrc, "export EDITOR=nano\n").unwrap();

        let cleanup = remove_profile_block(&home, GROK_BLOCK_OPEN, GROK_BLOCK_CLOSE);

        assert!(cleanup.warnings.is_empty(), "{:?}", cleanup.warnings);
        assert!(!cleanup.removed.is_empty());
        assert!(cleanup.removed.iter().all(|line| line.contains(".zshrc")));
        // The edited file lost only the block.
        let rewritten = std::fs::read_to_string(&zshrc).unwrap();
        assert!(!rewritten.contains("grok installer"));
        assert!(rewritten.contains("export EDITOR=vim"));
        assert!(rewritten.contains("alias ll="));
        // A profile without the markers is untouched, and gets no backup.
        assert_eq!(std::fs::read_to_string(&bashrc).unwrap(), "export EDITOR=nano\n");
        let backups: Vec<_> = std::fs::read_dir(&home)
            .unwrap()
            .filter_map(Result::ok)
            .map(|entry| entry.file_name().to_string_lossy().into_owned())
            .filter(|name| name.contains("funtoolkit-backup"))
            .collect();
        assert_eq!(backups.len(), 1, "exactly the edited file is backed up");
        assert!(backups[0].starts_with(".zshrc"));
        // No temporary file survives a successful rewrite.
        assert!(!std::fs::read_dir(&home)
            .unwrap()
            .filter_map(Result::ok)
            .any(|entry| entry.file_name().to_string_lossy().contains("funtoolkit-tmp")));
        std::fs::remove_dir_all(home).unwrap();
    }

    /// An incomplete block is reported rather than guessed at, and the file is
    /// left exactly as it was.
    #[test]
    fn an_incomplete_block_is_reported_and_the_file_is_untouched() {
        let home = std::env::temp_dir().join(format!("funtoolkit-profile-{}", uuid::Uuid::new_v4()));
        std::fs::create_dir_all(&home).unwrap();
        let zshrc = home.join(".zshrc");
        let original = "# >>> grok installer >>>\nexport PATH=\"$HOME/.grok/bin:$PATH\"\n";
        std::fs::write(&zshrc, original).unwrap();

        let cleanup = remove_profile_block(&home, GROK_BLOCK_OPEN, GROK_BLOCK_CLOSE);

        assert_eq!(cleanup.removed.len(), 0);
        assert_eq!(cleanup.warnings.len(), 1);
        assert!(cleanup.warnings[0].contains("by hand"));
        assert_eq!(std::fs::read_to_string(&zshrc).unwrap(), original);
        std::fs::remove_dir_all(home).unwrap();
    }

    #[test]
    fn every_shell_profile_the_installers_touch_is_examined() {
        let candidates = shell_profile_candidates(Path::new("/home/dev"));
        for expected in [".bashrc", ".zshrc", ".zprofile", ".bash_profile", ".profile", ".config/fish/config.fish"] {
            assert!(
                candidates.contains(&PathBuf::from("/home/dev").join(expected)),
                "{expected} is not examined"
            );
        }
    }

    /// The registry scripts are the one place where a well-meaning
    /// simplification silently corrupts every user's PATH, so their shape is
    /// pinned here rather than left to review.
    #[test]
    fn the_registry_scripts_never_expand_or_lose_the_value_kind() {
        let source = include_str!("uninstall_cleanup.rs");
        // Split on the test module itself: an earlier `#[cfg(test)]` attribute
        // on a helper would otherwise cut away most of the production code and
        // make every assertion below vacuous.
        // Split on the test module itself: an earlier `#[cfg(test)]` attribute
        // on a helper would otherwise cut away most of the production code and
        // make every assertion below vacuous. Comments are dropped so the
        // prose explaining which API to avoid is not mistaken for a use of it.
        let production: String = source
            .split_once("mod tests {")
            .expect("the test module marks the end of production code")
            .0
            .lines()
            .filter(|line| !line.trim_start().starts_with("//"))
            .collect::<Vec<_>>()
            .join("\n");
        assert!(
            production.contains("remove_from_user_path"),
            "the production half must include the registry code being pinned"
        );

        assert!(production.contains("DoNotExpandEnvironmentNames"));
        assert!(production.contains("GetValueKind"));
        // Reading PATH through the .NET environment API expands %VAR%, and
        // writing it back through the same API would persist the expansion.
        assert!(!production.contains("GetEnvironmentVariable('Path'"));
        assert!(!production.contains("SetEnvironmentVariable"));
        // A registry write notifies nobody on its own.
        assert!(production.contains("SendMessageTimeoutW"));
        assert!(production.contains("WM_SETTINGCHANGE"));
        // The write must refuse if PATH moved underneath us.
        assert!(production.contains("-cne"));
    }
}

#[cfg(test)]
mod removal_tests {
    use super::*;

    fn home() -> PathBuf {
        PathBuf::from("C:/Users/dev")
    }

    fn local_app_data() -> PathBuf {
        PathBuf::from("C:/Users/dev/AppData/Local")
    }


    /// `agent` sits in the same directory as every other tool's launcher. A
    /// file that does not point back into the Grok install root belongs to
    /// somebody else.
    #[test]
    fn a_launcher_is_only_removed_when_it_points_back_at_the_agent() {
        let grok = home().join(".grok");
        assert!(launcher_belongs_to_agent(
            Some(&home().join(".grok/downloads/grok-darwin-arm64")),
            None,
            &grok
        ));
        // Hermes writes generated wrappers, not symlinks, so the contents are
        // the only evidence available. The wrapper may name the root either as
        // an expanded absolute path or through `$HOME`.
        let hermes = home().join(".hermes");
        assert!(launcher_belongs_to_agent(
            None,
            Some("#!/usr/bin/env bash\nexec \"C:/Users/dev/.hermes/hermes-agent/venv/bin/python\" ...\n"),
            &hermes
        ));
        assert!(launcher_belongs_to_agent(
            None,
            Some("#!/usr/bin/env bash\nexec \"$HOME/.hermes/hermes-agent/venv/bin/python\" ...\n"),
            &hermes
        ));
        // A neighbouring directory whose name merely starts the same way.
        assert!(!launcher_belongs_to_agent(
            None,
            Some("#!/bin/sh\nexec \"$HOME/.hermes-backup/bin/hermes\"\n"),
            &hermes
        ));
        // An unrelated `agent` binary from another vendor.
        assert!(!launcher_belongs_to_agent(
            Some(&PathBuf::from("/opt/other-vendor/bin/agent")),
            None,
            &grok
        ));
        assert!(!launcher_belongs_to_agent(
            None,
            Some("#!/bin/sh\nexec /usr/bin/some-other-agent \"$@\"\n"),
            &grok
        ));
        assert!(!launcher_belongs_to_agent(None, None, &grok));
    }


    /// A path prefix must not be enough: `~/.grok-backup` is not inside
    /// `~/.grok`.
    #[test]
    fn a_sibling_root_does_not_count_as_belonging_to_the_agent() {
        assert!(!launcher_belongs_to_agent(
            Some(&home().join(".grok-backup/bin/grok")),
            None,
            &home().join(".grok")
        ));
    }


    /// The last checkpoint before anything leaves the disk.
    #[test]
    fn removal_refuses_shared_directories_even_when_asked() {
        let (allowed, refused) = partition_removable(
            &[
                home().join(".grok/bin"),
                home().join(".local/bin"),
                PathBuf::from("/usr/local/bin"),
            ],
            &home(),
        );
        assert_eq!(allowed, vec![home().join(".grok/bin")]);
        assert_eq!(refused.len(), 2);
        assert!(refused.iter().all(|message| message.contains("shared")));
    }


    #[test]
    fn grok_removes_only_its_own_directories() {
        let paths = grok_removable_paths(&home(), &InstallerOverrides::default());
        assert!(paths.contains(&home().join(".grok/bin")));
        // The config lives at ~/.grok/config.toml and is opt-in only, so the
        // parent must never be a blanket removal target.
        assert!(!paths.contains(&home().join(".grok")));
        for path in &paths {
            assert!(!is_shared_directory(path, &home()), "{path:?} is shared");
        }
    }


    /// The Hermes data root also holds sessions, memories and logs, which are
    /// the user's work, so only the tooling subdirectories may be removed.
    #[test]
    fn hermes_removes_its_tooling_but_never_the_data_root() {
        let windows = hermes_removable_paths(&home(), &local_app_data(), HostPlatform::Windows, &InstallerOverrides::default());
        let root = local_app_data().join("hermes");
        assert!(windows.contains(&root.join("hermes-agent")));
        assert!(windows.contains(&root.join("git")));
        assert!(windows.contains(&root.join("node")));
        assert!(!windows.contains(&root), "the data root must never be removed");

        let unix = hermes_removable_paths(&home(), &local_app_data(), HostPlatform::Macos, &InstallerOverrides::default());
        assert!(unix.contains(&home().join(".hermes/hermes-agent")));
        assert!(!unix.contains(&home().join(".hermes")));
    }


    /// The Hermes installer puts `node`, `npm` and `npx` symlinks in the same
    /// shared directory as its own launchers. Removing those would break the
    /// user's Node.js installation.
    #[test]
    fn hermes_launchers_never_include_the_node_toolchain() {
        let launchers = hermes_launcher_files(&home());
        for shared in ["node", "npm", "npx"] {
            assert!(
                !launchers.contains(&home().join(".local/bin").join(shared)),
                "{shared} must not be a removal target"
            );
        }
        assert!(launchers.contains(&home().join(".local/bin/hermes")));
    }


    /// Verified against the vendor install scripts.
    #[test]
    fn path_entries_match_what_each_installer_actually_adds() {
        assert_eq!(
            installer_path_entries("grok", &home(), &local_app_data(), HostPlatform::Windows, &InstallerOverrides::default()),
            vec![home().join(".grok/bin")]
        );
        let hermes = installer_path_entries("hermes", &home(), &local_app_data(), HostPlatform::Windows, &InstallerOverrides::default());
        let root = local_app_data().join("hermes");
        assert_eq!(
            hermes,
            vec![
                root.join("git/cmd"),
                root.join("git/bin"),
                root.join("git/usr/bin"),
                root.join("node"),
            ]
        );
        // The Unix Hermes installer only adds the shared ~/.local/bin.
        assert!(installer_path_entries("hermes", &home(), &local_app_data(), HostPlatform::Macos, &InstallerOverrides::default()).is_empty());
        // npm, Homebrew and desktop installers never touch PATH.
        for tool_id in ["codex-cli", "claude", "openclaw", "codex-desktop", "claude-desktop"] {
            assert!(
                installer_path_entries(tool_id, &home(), &local_app_data(), HostPlatform::Windows, &InstallerOverrides::default())
                    .is_empty(),
                "{tool_id} should not report PATH entries"
            );
        }
    }


    /// The single rule that protects every unrelated tool on the machine.
    #[test]
    fn shared_directories_are_recognized_through_separator_and_case_noise() {
        for shared in [
            "C:/Users/dev/.local/bin",
            "C:\\Users\\dev\\.local\\bin",
            "C:/Users/dev/.local/bin/",
            "C:/Users/DEV/.LOCAL/BIN",
            "/usr/local/bin",
            "/opt/homebrew/bin",
            "C:/Users/dev/AppData/Roaming/npm",
        ] {
            assert!(
                is_shared_directory(Path::new(shared), &home()),
                "{shared} should be treated as shared"
            );
        }
    }


    #[test]
    fn an_agent_owned_directory_is_not_treated_as_shared() {
        for owned in [
            "C:/Users/dev/.grok/bin",
            "C:/Users/dev/.hermes/hermes-agent",
            "C:/Users/dev/AppData/Local/hermes",
        ] {
            assert!(!is_shared_directory(Path::new(owned), &home()), "{owned}");
        }
    }
}
