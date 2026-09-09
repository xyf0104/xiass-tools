use crate::core::wf_bridge::{
    self, WfBridgeHostActionRequest, WfBridgeSession, WfBridgeStatus, WfHelperDiagnosticSnapshot,
    WfHelperTransferRestoreResult,
};
use tauri::AppHandle;

#[tauri::command]
pub async fn wf_bridge_get_session() -> Result<WfBridgeSession, String> {
    tauri::async_runtime::spawn_blocking(wf_bridge::get_or_start_session)
        .await
        .map_err(|error| format!("启动 WF 原生组件任务失败：{error}"))?
}

#[tauri::command]
pub fn wf_bridge_get_status() -> Result<WfBridgeStatus, String> {
    wf_bridge::get_status()
}

#[tauri::command]
pub fn wf_bridge_handle_host_action(
    app: AppHandle,
    port: u16,
    request: WfBridgeHostActionRequest,
) -> Result<(), String> {
    wf_bridge::handle_host_action(app, port, request)
}

#[tauri::command]
pub async fn wf_bridge_stop() -> Result<(), String> {
    tauri::async_runtime::spawn_blocking(wf_bridge::stop)
        .await
        .map_err(|error| format!("停止 WF 原生组件任务失败：{error}"))?
}

#[tauri::command]
pub async fn wf_bridge_export_helper_transfer() -> Result<serde_json::Value, String> {
    tauri::async_runtime::spawn_blocking(wf_bridge::export_helper_transfer)
        .await
        .map_err(|error| format!("导出 WF 备份任务失败：{error}"))?
}

#[tauri::command]
pub async fn wf_bridge_restore_helper_transfer(
    bundle: serde_json::Value,
) -> Result<WfHelperTransferRestoreResult, String> {
    tauri::async_runtime::spawn_blocking(move || wf_bridge::restore_helper_transfer(bundle))
        .await
        .map_err(|error| format!("恢复 WF 备份任务失败：{error}"))?
}

#[tauri::command]
pub async fn wf_bridge_get_helper_diagnostics() -> Result<WfHelperDiagnosticSnapshot, String> {
    tauri::async_runtime::spawn_blocking(wf_bridge::get_helper_diagnostics)
        .await
        .map_err(|error| format!("收集 WF 诊断任务失败：{error}"))?
}

#[tauri::command]
pub async fn wf_bridge_call(
    method: String,
    args: Vec<serde_json::Value>,
) -> Result<serde_json::Value, String> {
    tauri::async_runtime::spawn_blocking(move || wf_bridge::call_method(&method, args))
        .await
        .map_err(|error| format!("调用 WF 原生方法失败：{error}"))?
}
