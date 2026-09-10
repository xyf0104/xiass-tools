use std::io;
use std::path::PathBuf;
use std::path::Path;

#[derive(Debug, Clone)]
pub struct AppPaths {
    pub home_dir: PathBuf,
    pub config_dir: PathBuf,
    pub downloads_dir: PathBuf,
    pub database_file: PathBuf,
}

pub fn app_paths() -> io::Result<AppPaths> {
    let home_dir = dirs::home_dir()
        .ok_or_else(|| io::Error::new(io::ErrorKind::NotFound, "home directory not found"))?;
    let config_dir = home_dir.join(".codestudio-lite");
    let downloads_dir = config_dir.join("downloads");
    let database_file = config_dir.join("app_state.sqlite");

    Ok(AppPaths {
        home_dir,
        config_dir,
        downloads_dir,
        database_file,
    })
}

/// Resolve the Codex home directory exactly as Codex does: CODEX_HOME wins
/// when set, otherwise the per-user `.codex` directory is used. Keeping this
/// in one place prevents Windows and macOS from silently writing different
/// config/auth locations.
pub fn codex_home_dir(home_dir: &Path) -> PathBuf {
    std::env::var_os("CODEX_HOME")
        .map(PathBuf::from)
        .filter(|path| !path.as_os_str().is_empty())
        .unwrap_or_else(|| home_dir.join(".codex"))
}

pub fn ensure_dirs(paths: &AppPaths) -> io::Result<()> {
    std::fs::create_dir_all(&paths.config_dir)?;
    std::fs::create_dir_all(&paths.downloads_dir)?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        // The SQLite database contains protected OAuth snapshots. Keep the
        // app state directory and database private to the current user.
        std::fs::set_permissions(&paths.config_dir, std::fs::Permissions::from_mode(0o700))?;
        std::fs::set_permissions(&paths.downloads_dir, std::fs::Permissions::from_mode(0o700))?;
        if paths.database_file.exists() {
            std::fs::set_permissions(&paths.database_file, std::fs::Permissions::from_mode(0o600))?;
        }
    }
    Ok(())
}

pub fn display_path(path: &std::path::Path) -> String {
    path.to_string_lossy().replace('\\', "/")
}
