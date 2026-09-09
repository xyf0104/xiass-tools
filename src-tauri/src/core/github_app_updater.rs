use crate::core::{app_paths, app_updater, download_http};
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use std::fs::{self, File};
use std::io::Read;
use std::path::{Path, PathBuf};
use std::sync::Mutex;
use std::time::Duration;
use tauri_plugin_opener::OpenerExt;
use url::Url;

const REPOSITORY: &str = "xyf0104/Antigravity-WF-Assistant";
pub const PROGRESS_EVENT: &str = "github-app-update-progress";
static DOWNLOAD_LOCK: Mutex<()> = Mutex::new(());
static DOWNLOADED_INSTALLER: Mutex<Option<VerifiedInstaller>> = Mutex::new(None);

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct GitHubInstaller {
    filename: String,
    url: String,
    size: u64,
    sha256: String,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct GitHubApplicationRelease {
    version: String,
    name: Option<String>,
    url: String,
    published_at: Option<String>,
    installer: Option<GitHubInstaller>,
}

#[derive(Debug, Deserialize)]
struct ReleaseResponse {
    tag_name: String,
    name: Option<String>,
    published_at: Option<String>,
    draft: bool,
    prerelease: bool,
    assets: Vec<AssetResponse>,
}

#[derive(Debug, Deserialize)]
struct AssetResponse {
    name: String,
    browser_download_url: String,
    state: String,
    size: u64,
    digest: Option<String>,
}

#[derive(Clone)]
struct VerifiedInstaller {
    path: PathBuf,
    asset: GitHubInstaller,
}

fn valid_version(version: &str) -> bool {
    let parts: Vec<_> = version.split('.').collect();
    parts.len() == 3
        && parts.iter().all(|part| {
            !part.is_empty() && part.len() <= 9 && part.bytes().all(|b| b.is_ascii_digit())
        })
}

fn release_from_json(json: &str, target: &str) -> Result<GitHubApplicationRelease, String> {
    let release: ReleaseResponse = serde_json::from_str(json)
        .map_err(|err| format!("Invalid GitHub release response: {err}"))?;
    let version = release.tag_name.strip_prefix('v').unwrap_or("");
    if release.draft || release.prerelease || !valid_version(version) {
        return Err("GitHub did not return a valid stable XIASS Tools release.".into());
    }
    let suffix = match target {
        "darwin-aarch64" => Some("aarch64.dmg"),
        "darwin-x86_64" => Some("x64.dmg"),
        "windows-x86_64" => Some("x64-setup.exe"),
        _ => None,
    };
    let installer = if let Some(suffix) = suffix {
        let filename = format!("XIASS.Tools_{version}_{suffix}");
        let mut matches = release
            .assets
            .iter()
            .filter(|asset| asset.state == "uploaded" && asset.name.replace(' ', ".") == filename);
        let installer = matches
            .next()
            .map(|asset| validate_asset(asset, version))
            .transpose()?;
        if matches.next().is_some() {
            return Err("GitHub returned duplicate installers for this platform.".into());
        }
        installer
    } else {
        None
    };
    Ok(GitHubApplicationRelease {
        version: version.into(),
        name: release.name,
        url: format!("https://github.com/{REPOSITORY}/releases/latest"),
        published_at: release.published_at,
        installer,
    })
}

fn validate_asset(asset: &AssetResponse, version: &str) -> Result<GitHubInstaller, String> {
    let sha256 = asset.digest.as_deref().and_then(|value| value.strip_prefix("sha256:"))
        .filter(|value| value.len() == 64 && value.bytes().all(|b| b.is_ascii_hexdigit()))
        .ok_or("The GitHub installer has no valid SHA-256 checksum. Please retry after publication completes.")?;
    let mut expected = Url::parse(&format!(
        "https://github.com/{REPOSITORY}/releases/download/v{version}/"
    ))
    .map_err(|err| err.to_string())?;
    expected
        .path_segments_mut()
        .map_err(|_| "Invalid GitHub download path.")?
        .pop_if_empty()
        .push(&asset.name);
    let actual = Url::parse(&asset.browser_download_url).map_err(|err| err.to_string())?;
    if actual != expected || asset.size == 0 || asset.size > 2 * 1024 * 1024 * 1024 {
        return Err(
            "The installer must be an intact asset from the XIASS Tools GitHub release.".into(),
        );
    }
    Ok(GitHubInstaller {
        filename: asset.name.clone(),
        url: actual.to_string(),
        size: asset.size,
        sha256: sha256.to_ascii_lowercase(),
    })
}

fn load_release(endpoint: &str) -> Result<GitHubApplicationRelease, String> {
    let json = download_http::fetch_text(
        &format!("https://api.github.com/repos/{REPOSITORY}/releases/{endpoint}"),
        Duration::from_secs(10),
        1,
    )?;
    release_from_json(&json, app_updater::application_update_target()?)
}

pub fn check_update() -> Result<GitHubApplicationRelease, String> {
    load_release("latest")
}

fn verify_file(path: &Path, asset: &GitHubInstaller) -> Result<(), String> {
    let mut file =
        File::open(path).map_err(|err| format!("Cannot open the update installer: {err}"))?;
    if file.metadata().map_err(|err| err.to_string())?.len() != asset.size {
        return Err("Installer size verification failed. Please download the update again.".into());
    }
    let mut hasher = Sha256::new();
    let mut buffer = [0_u8; 64 * 1024];
    loop {
        let read = file.read(&mut buffer).map_err(|err| err.to_string())?;
        if read == 0 {
            break;
        }
        hasher.update(&buffer[..read]);
    }
    if format!("{:x}", hasher.finalize()) != asset.sha256 {
        return Err(
            "Installer SHA-256 verification failed. Please download the update again.".into(),
        );
    }
    Ok(())
}

pub fn download_update<F>(version: &str, mut on_progress: F) -> Result<String, String>
where
    F: FnMut(app_updater::AppUpdateProgress),
{
    let _guard = DOWNLOAD_LOCK
        .try_lock()
        .map_err(|_| "An update operation is already in progress.")?;
    if !valid_version(version) {
        return Err("Invalid update version.".into());
    }
    // Fetch authoritative metadata again. The webview cannot supply an arbitrary
    // URL, checksum, filename or destination, and downloading never launches it.
    let release = load_release(&format!("tags/v{version}"))?;
    if release.version != version {
        return Err("GitHub returned a different release. Please check for updates again.".into());
    }
    let asset = release
        .installer
        .ok_or("No installer is available for this platform yet.")?;
    let directory = app_paths::app_paths()
        .map_err(|err| err.to_string())?
        .downloads_dir
        .join("github-updates")
        .join(version);
    fs::create_dir_all(&directory).map_err(|err| err.to_string())?;
    let path = directory.join(&asset.filename);
    let progress = |phase: &str, downloaded_bytes, total_bytes| app_updater::AppUpdateProgress {
        phase: phase.into(),
        downloaded_bytes,
        total_bytes,
    };
    if verify_file(&path, &asset).is_err() {
        on_progress(progress("downloading", 0, Some(asset.size)));
        download_http::download_to_file(
            &asset.url,
            &path,
            &directory.join(format!("{}.part", asset.filename)),
            Some(asset.size),
            Duration::from_secs(600),
            3,
            |downloaded, total| on_progress(progress("downloading", downloaded, total)),
        )?;
    }
    on_progress(progress("verifying", asset.size, Some(asset.size)));
    if let Err(err) = verify_file(&path, &asset) {
        let _ = fs::remove_file(&path);
        return Err(err);
    }
    *DOWNLOADED_INSTALLER
        .lock()
        .map_err(|_| "Update state is unavailable.")? = Some(VerifiedInstaller {
        path: path.clone(),
        asset,
    });
    Ok(path.to_string_lossy().into_owned())
}

pub fn open_update(app: &tauri::AppHandle) -> Result<(), String> {
    let _guard = DOWNLOAD_LOCK
        .try_lock()
        .map_err(|_| "An update operation is already in progress.")?;
    let installer = DOWNLOADED_INSTALLER
        .lock()
        .map_err(|_| "Update state is unavailable.")?
        .clone()
        .ok_or("Please download and verify the installer first.")?;
    verify_file(&installer.path, &installer.asset)?;
    app.opener()
        .open_path(
            installer.path.to_string_lossy().into_owned(),
            None::<String>,
        )
        .map_err(|err| format!("Could not open the downloaded installer: {err}"))
}

#[cfg(test)]
#[path = "github_app_updater_tests.rs"]
mod tests;
