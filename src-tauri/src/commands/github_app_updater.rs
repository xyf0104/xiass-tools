use crate::core::{app_updater::AppUpdateProgress, github_app_updater};
use tauri::Emitter;

#[tauri::command]
pub async fn check_github_application_update(
) -> Result<github_app_updater::GitHubApplicationRelease, String> {
    tauri::async_runtime::spawn_blocking(github_app_updater::check_update)
        .await
        .map_err(|err| err.to_string())?
}

#[tauri::command]
pub async fn download_github_application_update(
    app: tauri::AppHandle,
    version: String,
) -> Result<String, String> {
    tauri::async_runtime::spawn_blocking(move || {
        github_app_updater::download_update(&version, |progress: AppUpdateProgress| {
            let _ = app.emit(github_app_updater::PROGRESS_EVENT, progress);
        })
    })
    .await
    .map_err(|err| err.to_string())?
}

#[tauri::command]
pub async fn open_github_application_update(app: tauri::AppHandle) -> Result<(), String> {
    tauri::async_runtime::spawn_blocking(move || github_app_updater::open_update(&app))
        .await
        .map_err(|err| err.to_string())?
}
