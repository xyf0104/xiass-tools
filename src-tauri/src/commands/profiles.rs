use crate::core::types::{
    ApplyProfileRequest, ApplyProfileResult, ClearEnvironmentVariablesRequest,
    ClearEnvironmentVariablesResult, DeleteProfileDraftRequest, DuplicateProfileDraftRequest,
    ListProfileModelsRequest, ListProfileModelsResult, PreviewProfileApplyRequest,
    PreviewProfileApplyResult, PreviewProfileWriteRequest, PreviewProfileWriteResult, ProfileDraft,
    ProfileSummary, ReorderProfileDraftsRequest, SaveProfileDraftRequest,
    StartCodexOAuthLoginResult, SwitchActiveProfileRequest, TestProfileConnectionRequest,
    TestProfileConnectionResult, UpdateProfileDraftRequest,
};
use crate::core::{env_health, profile};

#[tauri::command]
pub async fn load_profile_summary() -> Result<ProfileSummary, String> {
    tauri::async_runtime::spawn_blocking(profile::load_profile_summary)
        .await
        .map_err(|err| err.to_string())?
}

#[tauri::command]
pub async fn start_codex_oauth_login() -> Result<StartCodexOAuthLoginResult, String> {
    tauri::async_runtime::spawn_blocking(profile::start_codex_oauth_login)
        .await
        .map_err(|err| err.to_string())?
}

#[tauri::command]
pub async fn save_profile_draft(request: SaveProfileDraftRequest) -> Result<ProfileDraft, String> {
    tauri::async_runtime::spawn_blocking(move || profile::save_profile_draft(request))
        .await
        .map_err(|err| err.to_string())?
}

#[tauri::command]
pub async fn update_profile_draft(
    request: UpdateProfileDraftRequest,
) -> Result<ProfileDraft, String> {
    tauri::async_runtime::spawn_blocking(move || profile::update_profile_draft(request))
        .await
        .map_err(|err| err.to_string())?
}

#[tauri::command]
pub async fn duplicate_profile_draft(
    request: DuplicateProfileDraftRequest,
) -> Result<ProfileDraft, String> {
    tauri::async_runtime::spawn_blocking(move || profile::duplicate_profile_draft(request))
        .await
        .map_err(|err| err.to_string())?
}

#[tauri::command]
pub async fn delete_profile_draft(
    request: DeleteProfileDraftRequest,
) -> Result<ProfileSummary, String> {
    tauri::async_runtime::spawn_blocking(move || profile::delete_profile_draft(request))
        .await
        .map_err(|err| err.to_string())?
}

#[tauri::command]
pub async fn reorder_profile_drafts(
    request: ReorderProfileDraftsRequest,
) -> Result<ProfileSummary, String> {
    tauri::async_runtime::spawn_blocking(move || profile::reorder_profile_drafts(request))
        .await
        .map_err(|err| err.to_string())?
}

#[tauri::command]
pub async fn preview_profile_write(
    request: PreviewProfileWriteRequest,
) -> Result<PreviewProfileWriteResult, String> {
    tauri::async_runtime::spawn_blocking(move || profile::preview_profile_write(request))
        .await
        .map_err(|err| err.to_string())?
}

#[tauri::command]
pub async fn preview_profile_apply(
    request: PreviewProfileApplyRequest,
) -> Result<PreviewProfileApplyResult, String> {
    tauri::async_runtime::spawn_blocking(move || profile::preview_profile_apply(request))
        .await
        .map_err(|err| err.to_string())?
}

#[tauri::command]
pub async fn apply_profile(request: ApplyProfileRequest) -> Result<ApplyProfileResult, String> {
    tauri::async_runtime::spawn_blocking(move || profile::apply_profile(request))
        .await
        .map_err(|err| err.to_string())?
}

#[tauri::command]
pub async fn test_profile_connection(
    request: TestProfileConnectionRequest,
) -> Result<TestProfileConnectionResult, String> {
    tauri::async_runtime::spawn_blocking(move || profile::test_profile_connection(request))
        .await
        .map_err(|err| err.to_string())?
}

#[tauri::command]
pub async fn list_profile_models(
    request: ListProfileModelsRequest,
) -> Result<ListProfileModelsResult, String> {
    tauri::async_runtime::spawn_blocking(move || profile::list_profile_models(request))
        .await
        .map_err(|err| err.to_string())?
}

#[tauri::command]
pub async fn switch_active_profile(
    request: SwitchActiveProfileRequest,
) -> Result<ProfileSummary, String> {
    tauri::async_runtime::spawn_blocking(move || profile::switch_active_profile(request))
        .await
        .map_err(|err| err.to_string())?
}

#[tauri::command]
pub async fn clear_environment_variables(
    request: ClearEnvironmentVariablesRequest,
) -> Result<ClearEnvironmentVariablesResult, String> {
    tauri::async_runtime::spawn_blocking(move || env_health::clear_environment_variables(request))
        .await
        .map_err(|err| err.to_string())?
}
