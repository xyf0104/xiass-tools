use rand::{distributions::Alphanumeric, Rng};
use serde::{Deserialize, Serialize};
use std::collections::HashSet;
use std::io::{BufRead, BufReader, Read};
use std::path::{Path, PathBuf};
use std::process::{Child, Command, Stdio};
use std::sync::{mpsc, Mutex, OnceLock};
use std::time::{Duration, Instant};

use tauri::{AppHandle, Manager};
use tauri_plugin_dialog::DialogExt;
use tauri_plugin_opener::OpenerExt;

const WF_BRIDGE_BIN_NAME: &str = "xiass-wf-bridge";
const WF_BRIDGE_START_TIMEOUT: Duration = Duration::from_secs(15);
const WF_BRIDGE_STOP_TIMEOUT: Duration = Duration::from_secs(3);
const WF_BRIDGE_RESPONSE_LIMIT_BYTES: usize = 40 << 20;

fn log_info(message: &str) {
    eprintln!("[XIASS WF] {message}");
}

fn log_warn(message: &str) {
    eprintln!("[XIASS WF] {message}");
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct WfBridgeSession {
    pub url: String,
    pub token: String,
    pub host: String,
    pub port: u16,
    pub schema_version: u32,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct WfBridgeStatus {
    pub running: bool,
    pub url: Option<String>,
    pub last_error: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct WfHelperTransferRestoreResult {
    pub ok: bool,
    pub account_count: usize,
    pub model_count: usize,
    pub rolled_back: bool,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct WfHelperDiagnosticLog {
    pub name: String,
    pub content: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct WfHelperDiagnosticSnapshot {
    pub schema_version: u32,
    pub storage_owner: String,
    pub storage_root_kind: String,
    pub account_store_readable: bool,
    pub account_count: usize,
    pub model_count: usize,
    pub proxy_listening: bool,
    pub logs: Vec<WfHelperDiagnosticLog>,
    pub exclusions: Vec<String>,
}

#[derive(Debug, Deserialize)]
struct WfBridgeResponseEnvelope {
    ok: bool,
    result: Option<serde_json::Value>,
    error: Option<String>,
}

#[derive(Debug, Clone, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct WfBridgeHostActionFilter {
    name: String,
    pattern: String,
}

#[derive(Debug, Clone, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct WfBridgeHostActionRequest {
    request_id: String,
    kind: String,
    #[serde(default)]
    title: String,
    #[serde(default)]
    default_directory: String,
    #[serde(default)]
    default_filename: String,
    #[serde(default)]
    filters: Vec<WfBridgeHostActionFilter>,
    #[serde(default)]
    url: String,
    #[serde(default)]
    account_id: String,
    #[serde(default)]
    model: String,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
struct WfBridgeHostActionResult {
    request_id: String,
    ok: bool,
    canceled: bool,
    #[serde(skip_serializing_if = "Option::is_none")]
    value: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    error: Option<String>,
}

#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
struct WfBridgeReadySignal {
    event: String,
    service: String,
    host: String,
    port: u16,
    #[serde(default)]
    schema_version: u32,
}

#[derive(Default)]
struct WfBridgeRuntime {
    child: Option<Child>,
    session: Option<WfBridgeSession>,
    last_error: Option<String>,
    host_action_ids: HashSet<String>,
}

static WF_BRIDGE_RUNTIME: OnceLock<Mutex<WfBridgeRuntime>> = OnceLock::new();

fn runtime() -> &'static Mutex<WfBridgeRuntime> {
    WF_BRIDGE_RUNTIME.get_or_init(|| Mutex::new(WfBridgeRuntime::default()))
}

fn bridge_binary_file_names() -> Vec<String> {
    let target = env!("XIASS_RUST_TARGET");
    if cfg!(target_os = "windows") {
        vec![
            format!("{WF_BRIDGE_BIN_NAME}.exe"),
            format!("{WF_BRIDGE_BIN_NAME}-{target}.exe"),
        ]
    } else {
        vec![
            WF_BRIDGE_BIN_NAME.to_string(),
            format!("{WF_BRIDGE_BIN_NAME}-{target}"),
        ]
    }
}

fn push_binary_candidates(candidates: &mut Vec<PathBuf>, directory: &Path) {
    for name in bridge_binary_file_names() {
        let path = directory.join(name);
        if !candidates.iter().any(|candidate| candidate == &path) {
            candidates.push(path);
        }
    }
}

fn bridge_binary_candidates() -> Result<Vec<PathBuf>, String> {
    let executable = std::env::current_exe()
        .map_err(|error| format!("读取 XIASS Tools 程序路径失败：{error}"))?;
    let executable_dir = executable
        .parent()
        .ok_or_else(|| format!("XIASS Tools 程序路径缺少父目录：{}", executable.display()))?;
    let manifest_dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    let development_dir = manifest_dir.join("../sidecars/wf-bridge/bin");
    let mut candidates = Vec::new();

    if cfg!(debug_assertions) {
        push_binary_candidates(&mut candidates, &development_dir);
    }
    push_binary_candidates(&mut candidates, executable_dir);
    if let Some(contents_dir) = executable_dir.parent() {
        push_binary_candidates(&mut candidates, &contents_dir.join("Resources"));
    }
    if !cfg!(debug_assertions) {
        push_binary_candidates(&mut candidates, &development_dir);
    }
    Ok(candidates)
}

fn bridge_binary_path() -> Result<PathBuf, String> {
    let candidates = bridge_binary_candidates()?;
    candidates
        .iter()
        .find(|candidate| candidate.is_file())
        .cloned()
        .ok_or_else(|| {
            format!(
                "WF 原生组件不存在；请重新安装 XIASS Tools。已检查：{}",
                candidates
                    .iter()
                    .map(|candidate| candidate.display().to_string())
                    .collect::<Vec<_>>()
                    .join("、")
            )
        })
}

fn sanitize_bridge_environment(command: &mut Command) {
    #[cfg(target_os = "macos")]
    {
        command.env_remove("__CFBundleIdentifier");
        command.env_remove("XPC_SERVICE_NAME");
    }
    #[cfg(not(target_os = "macos"))]
    let _ = command;
}

fn refresh_process_status(runtime: &mut WfBridgeRuntime) {
    let Some(child) = runtime.child.as_mut() else {
        runtime.session = None;
        return;
    };
    match child.try_wait() {
        Ok(Some(status)) => {
            runtime.last_error = Some(format!("WF 原生组件已退出：{status}"));
            runtime.child = None;
            runtime.session = None;
            runtime.host_action_ids.clear();
        }
        Ok(None) => {}
        Err(error) => {
            runtime.last_error = Some(format!("检查 WF 原生组件状态失败：{error}"));
            if let Some(mut child) = runtime.child.take() {
                stop_child(&mut child);
            }
            runtime.session = None;
            runtime.host_action_ids.clear();
        }
    }
}

fn redact_log_line(line: &str) -> String {
    let line = line.trim();
    if line.len() <= 2_000 {
        line.to_string()
    } else {
        format!("{}…", line.chars().take(2_000).collect::<String>())
    }
}

fn spawn_stdout_reader(stdout: std::process::ChildStdout, sender: mpsc::Sender<String>) {
    std::thread::spawn(move || {
        for line in BufReader::new(stdout).lines() {
            match line {
                Ok(line) => {
                    let _ = sender.send(line);
                }
                Err(error) => {
                    log_warn(&format!("[WFBridge] 读取组件输出失败：{error}"));
                    break;
                }
            }
        }
    });
}

fn spawn_stderr_reader(stderr: std::process::ChildStderr) {
    std::thread::spawn(move || {
        for line in BufReader::new(stderr).lines() {
            match line {
                Ok(line) if !line.trim().is_empty() => {
                    log_warn(&format!("[WFBridge] {}", redact_log_line(&line)))
                }
                Ok(_) => {}
                Err(error) => {
                    log_warn(&format!("[WFBridge] 读取组件诊断失败：{error}"));
                    break;
                }
            }
        }
    });
}

fn parse_ready_signal(line: &str) -> Option<WfBridgeReadySignal> {
    let signal = serde_json::from_str::<WfBridgeReadySignal>(line.trim()).ok()?;
    if signal.event != "ready" || signal.service != WF_BRIDGE_BIN_NAME {
        return None;
    }
    Some(signal)
}

fn verify_ready_signal(signal: &WfBridgeReadySignal) -> Result<(), String> {
    if signal.host != "127.0.0.1" {
        return Err("WF 原生组件拒绝了非本机监听地址".to_string());
    }
    if signal.port == 0 {
        return Err("WF 原生组件返回了无效端口".to_string());
    }
    if signal.schema_version != 1 {
        return Err(format!(
            "WF 原生组件协议版本不兼容：{}",
            signal.schema_version
        ));
    }
    Ok(())
}

fn probe_health(url: &str) -> Result<(), String> {
    let client = reqwest::blocking::Client::builder()
        .timeout(Duration::from_secs(3))
        .no_proxy()
        .build()
        .map_err(|error| format!("创建 WF 本机健康检查失败：{error}"))?;
    let response = client
        .get(format!("{url}/health"))
        .send()
        .map_err(|error| format!("WF 原生组件健康检查失败：{error}"))?;
    if !response.status().is_success() {
        return Err(format!("WF 原生组件健康检查返回 {}", response.status()));
    }
    Ok(())
}

fn authenticated_request(
    session: &WfBridgeSession,
    method: reqwest::Method,
    path: &str,
    body: Option<&serde_json::Value>,
) -> Result<Vec<u8>, String> {
    let client = reqwest::blocking::Client::builder()
        .timeout(Duration::from_secs(20))
        .no_proxy()
        .build()
        .map_err(|_| "创建 WF 本机数据请求失败".to_string())?;
    let mut request = client
        .request(method, format!("{}{}", session.url, path))
        .bearer_auth(&session.token);
    if let Some(body) = body {
        request = request.json(body);
    }
    let response = request
        .send()
        .map_err(|_| "WF 本机数据请求失败".to_string())?;
    if response
        .content_length()
        .is_some_and(|length| length > WF_BRIDGE_RESPONSE_LIMIT_BYTES as u64)
    {
        return Err("WF 本机数据响应超过安全大小限制".to_string());
    }
    let status = response.status();
    let mut bytes = Vec::new();
    response
        .take((WF_BRIDGE_RESPONSE_LIMIT_BYTES + 1) as u64)
        .read_to_end(&mut bytes)
        .map_err(|_| "无法读取 WF 本机数据响应".to_string())?;
    if bytes.len() > WF_BRIDGE_RESPONSE_LIMIT_BYTES {
        return Err("WF 本机数据响应超过安全大小限制".to_string());
    }
    if !status.is_success() {
        let message = serde_json::from_slice::<WfBridgeResponseEnvelope>(&bytes)
            .ok()
            .and_then(|envelope| envelope.error)
            .filter(|message| message.chars().count() <= 200)
            .unwrap_or_else(|| format!("WF 本机数据请求返回 {status}"));
        return Err(message);
    }
    Ok(bytes)
}

pub fn export_helper_transfer() -> Result<serde_json::Value, String> {
    let session = get_or_start_session()?;
    let bytes = authenticated_request(&session, reqwest::Method::GET, "/transfer/export", None)?;
    let envelope = serde_json::from_slice::<WfBridgeResponseEnvelope>(&bytes)
        .map_err(|_| "WF 备份响应格式无效".to_string())?;
    if !envelope.ok {
        return Err(envelope
            .error
            .unwrap_or_else(|| "WF 备份导出失败".to_string()));
    }
    envelope
        .result
        .ok_or_else(|| "WF 备份响应缺少数据".to_string())
}

pub fn restore_helper_transfer(
    bundle: serde_json::Value,
) -> Result<WfHelperTransferRestoreResult, String> {
    let session = get_or_start_session()?;
    let encoded = serde_json::to_vec(&bundle).map_err(|_| "WF 备份无法编码".to_string())?;
    if encoded.len() > WF_BRIDGE_RESPONSE_LIMIT_BYTES {
        return Err("WF 备份超过安全大小限制".to_string());
    }
    let bytes = authenticated_request(
        &session,
        reqwest::Method::POST,
        "/transfer/restore",
        Some(&bundle),
    )?;
    let envelope = serde_json::from_slice::<WfBridgeResponseEnvelope>(&bytes)
        .map_err(|_| "WF 恢复响应格式无效".to_string())?;
    let result = envelope
        .result
        .ok_or_else(|| "WF 恢复响应缺少结果".to_string())?;
    serde_json::from_value(result).map_err(|_| "WF 恢复结果格式无效".to_string())
}

pub fn get_helper_diagnostics() -> Result<WfHelperDiagnosticSnapshot, String> {
    let session = get_or_start_session()?;
    let bytes = authenticated_request(&session, reqwest::Method::GET, "/diagnostics", None)?;
    let snapshot = serde_json::from_slice::<WfHelperDiagnosticSnapshot>(&bytes)
        .map_err(|_| "WF 诊断响应格式无效".to_string())?;
    if snapshot.schema_version != 1
        || snapshot.storage_owner != "embedded_wf_helper"
        || snapshot.logs.len() > 6
        || snapshot.logs.iter().any(|log| {
            log.content.len() > 2 << 20
                || !log.name.starts_with("wf-helper/")
                || log.name.contains("..")
        })
    {
        return Err("WF 诊断响应未通过安全验证".to_string());
    }
    Ok(snapshot)
}

fn valid_host_action_id(value: &str) -> bool {
    value.len() == 64 && value.bytes().all(|byte| byte.is_ascii_hexdigit())
}

fn validate_host_action_request(request: &WfBridgeHostActionRequest) -> Result<(), String> {
    if !valid_host_action_id(&request.request_id) {
        return Err("WF 原生操作请求标识无效".to_string());
    }
    if request.title.chars().count() > 200
        || request.default_directory.chars().count() > 4_096
        || request.default_filename.chars().count() > 255
        || request.filters.len() > 8
    {
        return Err("WF 原生操作参数超出限制".to_string());
    }
    if request.default_filename.contains(['/', '\\']) {
        return Err("WF 原生操作默认文件名无效".to_string());
    }
    if request
        .filters
        .iter()
        .any(|filter| filter.name.chars().count() > 100 || filter.pattern.chars().count() > 256)
    {
        return Err("WF 原生操作文件过滤器无效".to_string());
    }
    match request.kind.as_str() {
        "open_file" | "open_directory" | "save_file" => Ok(()),
        "open_url" => {
            if request.url.len() > 8_192 {
                return Err("WF 原生操作链接超出限制".to_string());
            }
            let parsed = url::Url::parse(request.url.trim())
                .map_err(|_| "WF 原生操作链接无效".to_string())?;
            if !matches!(parsed.scheme(), "http" | "https") {
                return Err("WF 原生操作只允许打开 HTTP(S) 链接".to_string());
            }
            Ok(())
        }
        "claude_code_account_candidates" => {
            if !request.title.is_empty()
                || !request.default_directory.is_empty()
                || !request.default_filename.is_empty()
                || !request.filters.is_empty()
                || !request.url.is_empty()
                || !request.account_id.is_empty()
                || !request.model.is_empty()
            {
                return Err("WF Claude Code 账户请求参数无效".to_string());
            }
            Ok(())
        }
        "claude_code_apply_account" => {
            if !request.title.is_empty()
                || !request.default_directory.is_empty()
                || !request.default_filename.is_empty()
                || !request.filters.is_empty()
                || !request.url.is_empty()
                || request.account_id.len() > 128
                || request.model.len() > 256
                || request.account_id.chars().any(char::is_control)
                || request.model.chars().any(char::is_control)
            {
                return Err("WF Claude Code 账户选择参数无效".to_string());
            }
            Ok(())
        }
        _ => Err("WF 原生操作类型无效".to_string()),
    }
}

fn host_action_filter_extensions(pattern: &str) -> Vec<String> {
    let mut result = Vec::new();
    for part in pattern.split([';', ',']) {
        let part = part.trim();
        let extension = part
            .strip_prefix("*.")
            .or_else(|| part.rsplit_once('.').map(|(_, extension)| extension))
            .unwrap_or_default()
            .trim()
            .to_ascii_lowercase();
        if extension.is_empty()
            || extension.len() > 24
            || !extension
                .bytes()
                .all(|byte| byte.is_ascii_alphanumeric() || byte == b'-')
            || result.iter().any(|existing| existing == &extension)
        {
            continue;
        }
        result.push(extension);
    }
    result
}

fn host_action_path_result(
    request_id: String,
    path: Option<tauri_plugin_dialog::FilePath>,
) -> WfBridgeHostActionResult {
    match path {
        Some(path) => match path.into_path() {
            Ok(path) => WfBridgeHostActionResult {
                request_id,
                ok: true,
                canceled: false,
                value: Some(path.to_string_lossy().to_string()),
                error: None,
            },
            Err(_) => WfBridgeHostActionResult {
                request_id,
                ok: false,
                canceled: false,
                value: None,
                error: Some("主应用无法解析所选位置".to_string()),
            },
        },
        None => WfBridgeHostActionResult {
            request_id,
            ok: false,
            canceled: true,
            value: None,
            error: None,
        },
    }
}

fn post_host_action_result(session: WfBridgeSession, result: WfBridgeHostActionResult) {
    std::thread::spawn(move || {
        let client = match reqwest::blocking::Client::builder()
            .timeout(Duration::from_secs(3))
            .no_proxy()
            .build()
        {
            Ok(client) => client,
            Err(_) => return,
        };
        let endpoint = format!("{}/host-action-result", session.url);
        for attempt in 0..3 {
            let sent = client
                .post(&endpoint)
                .bearer_auth(&session.token)
                .json(&result)
                .send()
                .map(|response| response.status().is_success())
                .unwrap_or(false);
            if sent {
                return;
            }
            if attempt < 2 {
                std::thread::sleep(Duration::from_millis(100));
            }
        }
        log_warn("[WFBridge] 主应用无法回传原生操作结果；组件会自动取消等待");
    });
}

fn configure_host_file_dialog<R: tauri::Runtime>(
    app: &AppHandle<R>,
    request: &WfBridgeHostActionRequest,
) -> tauri_plugin_dialog::FileDialogBuilder<R> {
    let mut dialog = app.dialog().file();
    if !request.title.trim().is_empty() {
        dialog = dialog.set_title(request.title.trim());
    }
    if !request.default_directory.trim().is_empty() {
        dialog = dialog.set_directory(request.default_directory.trim());
    }
    if !request.default_filename.trim().is_empty() {
        dialog = dialog.set_file_name(request.default_filename.trim());
    }
    for filter in &request.filters {
        let extensions = host_action_filter_extensions(&filter.pattern);
        if extensions.is_empty() {
            continue;
        }
        let extension_refs = extensions.iter().map(String::as_str).collect::<Vec<_>>();
        dialog = dialog.add_filter(filter.name.trim(), &extension_refs);
    }
    if let Some(window) = app.get_webview_window("main") {
        dialog = dialog.set_parent(&window);
    }
    dialog
}

pub fn handle_host_action(
    app: AppHandle,
    port: u16,
    request: WfBridgeHostActionRequest,
) -> Result<(), String> {
    validate_host_action_request(&request)?;
    let session = {
        let mut runtime = runtime()
            .lock()
            .map_err(|_| "WF 原生组件运行状态锁不可用".to_string())?;
        refresh_process_status(&mut runtime);
        let session = runtime
            .session
            .clone()
            .filter(|session| session.port == port)
            .ok_or_else(|| "WF 原生操作会话已失效".to_string())?;
        if !runtime.host_action_ids.insert(request.request_id.clone()) {
            return Ok(());
        }
        session
    };

    match request.kind.as_str() {
        "open_url" => {
            let result = match app.opener().open_url(request.url.trim(), None::<String>) {
                Ok(()) => WfBridgeHostActionResult {
                    request_id: request.request_id,
                    ok: true,
                    canceled: false,
                    value: None,
                    error: None,
                },
                Err(_) => WfBridgeHostActionResult {
                    request_id: request.request_id,
                    ok: false,
                    canceled: false,
                    value: None,
                    error: Some("主应用无法打开浏览器".to_string()),
                },
            };
            post_host_action_result(session, result);
        }
        "open_file" => {
            let request_id = request.request_id.clone();
            configure_host_file_dialog(&app, &request).pick_file(move |path| {
                post_host_action_result(session, host_action_path_result(request_id, path));
            });
        }
        "open_directory" => {
            let request_id = request.request_id.clone();
            configure_host_file_dialog(&app, &request).pick_folder(move |path| {
                post_host_action_result(session, host_action_path_result(request_id, path));
            });
        }
        "save_file" => {
            let request_id = request.request_id.clone();
            configure_host_file_dialog(&app, &request).save_file(move |path| {
                post_host_action_result(session, host_action_path_result(request_id, path));
            });
        }
        "claude_code_account_candidates" => {
            let result = WfBridgeHostActionResult {
                request_id: request.request_id,
                ok: false,
                canceled: false,
                value: None,
                error: Some("XIASS Tools 尚未把 Claude Code 账户桥接到 WF 组件。".to_string()),
            };
            post_host_action_result(session, result);
        }
        "claude_code_apply_account" => {
            let result = WfBridgeHostActionResult {
                request_id: request.request_id,
                ok: false,
                canceled: false,
                value: None,
                error: Some("XIASS Tools 尚未把 Claude Code 账户桥接到 WF 组件。".to_string()),
            };
            post_host_action_result(session, result);
        }
        _ => return Err("WF 原生操作类型无效".to_string()),
    }
    Ok(())
}

fn stop_child(child: &mut Child) {
    child.stdin.take();
    let deadline = Instant::now() + WF_BRIDGE_STOP_TIMEOUT;
    loop {
        match child.try_wait() {
            Ok(Some(_)) => return,
            Ok(None) if Instant::now() < deadline => {
                std::thread::sleep(Duration::from_millis(25));
            }
            _ => break,
        }
    }
    let _ = child.kill();
    let _ = child.wait();
}

fn start_bridge(runtime: &mut WfBridgeRuntime) -> Result<WfBridgeSession, String> {
    let binary = bridge_binary_path()?;
    let token: String = rand::thread_rng()
        .sample_iter(&Alphanumeric)
        .take(64)
        .map(char::from)
        .collect();
    let mut command = Command::new(&binary);
    sanitize_bridge_environment(&mut command);
    command
        .env("XIASS_WF_RPC_TOKEN", &token)
        .env("XIASS_WF_RPC_PORT", "0")
        .env("XIASS_PARENT_PID", std::process::id().to_string())
        // 子进程读取此匿名管道。父进程正常退出、崩溃或被强制终止时，操作系统
        // 都会关闭写端，WF bridge 随即释放 HTTP 监听和本地代理端口。
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped());
    #[cfg(target_os = "windows")]
    {
        use std::os::windows::process::CommandExt;
        command.creation_flags(0x0800_0000);
    }

    let mut child = command
        .spawn()
        .map_err(|error| format!("启动 WF 原生组件失败：{error}"))?;
    let Some(stdout) = child.stdout.take() else {
        stop_child(&mut child);
        return Err("WF 原生组件没有输出启动状态".to_string());
    };
    if let Some(stderr) = child.stderr.take() {
        spawn_stderr_reader(stderr);
    }
    let (sender, receiver) = mpsc::channel();
    spawn_stdout_reader(stdout, sender);
    let deadline = Instant::now() + WF_BRIDGE_START_TIMEOUT;
    let ready = loop {
        if let Ok(Some(status)) = child.try_wait() {
            return Err(format!("WF 原生组件启动前已退出：{status}"));
        }
        let now = Instant::now();
        if now >= deadline {
            stop_child(&mut child);
            return Err("WF 原生组件启动超时".to_string());
        }
        match receiver.recv_timeout((deadline - now).min(Duration::from_millis(250))) {
            Ok(line) => {
                if let Some(signal) = parse_ready_signal(&line) {
                    break signal;
                }
                if !line.trim().is_empty() {
                    log_info(&format!("[WFBridge] {}", redact_log_line(&line)));
                }
            }
            Err(mpsc::RecvTimeoutError::Timeout) => continue,
            Err(mpsc::RecvTimeoutError::Disconnected) => {
                stop_child(&mut child);
                return Err("WF 原生组件启动输出已关闭".to_string());
            }
        }
    };
    if let Err(error) = verify_ready_signal(&ready) {
        stop_child(&mut child);
        return Err(error);
    }
    let url = format!("http://{}:{}", ready.host, ready.port);
    if let Err(error) = probe_health(&url) {
        stop_child(&mut child);
        return Err(error);
    }
    let session = WfBridgeSession {
        url,
        token,
        host: ready.host,
        port: ready.port,
        schema_version: ready.schema_version,
    };
    runtime.child = Some(child);
    runtime.session = Some(session.clone());
    runtime.last_error = None;
    runtime.host_action_ids.clear();
    log_info(&format!(
        "[WFBridge] 原生组件已就绪：host={} port={}",
        session.host, session.port
    ));
    Ok(session)
}

pub fn get_or_start_session() -> Result<WfBridgeSession, String> {
    let mut runtime = runtime()
        .lock()
        .map_err(|_| "WF 原生组件运行状态锁不可用".to_string())?;
    refresh_process_status(&mut runtime);
    if let Some(session) = runtime.session.clone() {
        if probe_health(&session.url).is_ok() {
            return Ok(session);
        }
        runtime.last_error = Some("WF 原生组件健康检查失败，正在重新连接".to_string());
        if let Some(mut child) = runtime.child.take() {
            stop_child(&mut child);
        }
        runtime.session = None;
        runtime.host_action_ids.clear();
    }
    match start_bridge(&mut runtime) {
        Ok(session) => Ok(session),
        Err(error) => {
            runtime.last_error = Some(error.clone());
            Err(error)
        }
    }
}

pub fn get_status() -> Result<WfBridgeStatus, String> {
    let mut runtime = runtime()
        .lock()
        .map_err(|_| "WF 原生组件运行状态锁不可用".to_string())?;
    refresh_process_status(&mut runtime);
    Ok(WfBridgeStatus {
        running: runtime.child.is_some(),
        url: runtime.session.as_ref().map(|session| session.url.clone()),
        last_error: runtime.last_error.clone(),
    })
}

pub fn stop() -> Result<(), String> {
    let mut runtime = runtime()
        .lock()
        .map_err(|_| "WF 原生组件运行状态锁不可用".to_string())?;
    if let Some(mut child) = runtime.child.take() {
        stop_child(&mut child);
    }
    runtime.session = None;
    runtime.last_error = None;
    runtime.host_action_ids.clear();
    log_info("[WFBridge] 原生组件已停止");
    Ok(())
}

pub fn stop_for_app_exit() {
    if let Ok(mut runtime) = runtime().lock() {
        if let Some(mut child) = runtime.child.take() {
            stop_child(&mut child);
        }
        runtime.session = None;
        runtime.host_action_ids.clear();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn accepts_only_expected_ready_signal() {
        let signal = parse_ready_signal(
            r#"{"event":"ready","service":"xiass-wf-bridge","host":"127.0.0.1","port":50991,"schemaVersion":1}"#,
        )
        .expect("ready signal");
        assert!(verify_ready_signal(&signal).is_ok());
        assert!(parse_ready_signal(r#"{"event":"log"}"#).is_none());
    }

    #[test]
    fn rejects_non_loopback_ready_signal() {
        let signal = WfBridgeReadySignal {
            event: "ready".to_string(),
            service: WF_BRIDGE_BIN_NAME.to_string(),
            host: "0.0.0.0".to_string(),
            port: 50991,
            schema_version: 1,
        };
        assert!(verify_ready_signal(&signal).is_err());
    }

    #[test]
    fn candidate_names_include_current_target() {
        let names = bridge_binary_file_names();
        assert!(names
            .iter()
            .any(|name| name.contains(env!("XIASS_RUST_TARGET"))));
    }

    fn host_action_request(kind: &str) -> WfBridgeHostActionRequest {
        WfBridgeHostActionRequest {
            request_id: "a".repeat(64),
            kind: kind.to_string(),
            title: String::new(),
            default_directory: String::new(),
            default_filename: String::new(),
            filters: Vec::new(),
            url: String::new(),
            account_id: String::new(),
            model: String::new(),
        }
    }

    #[test]
    fn validates_host_action_kind_identifier_and_url() {
        for kind in [
            "open_file",
            "open_directory",
            "save_file",
            "claude_code_account_candidates",
        ] {
            assert!(validate_host_action_request(&host_action_request(kind)).is_ok());
        }

        let mut open_url = host_action_request("open_url");
        open_url.url = "https://example.com/oauth/callback".to_string();
        assert!(validate_host_action_request(&open_url).is_ok());
        open_url.url = "file:///tmp/private".to_string();
        assert!(validate_host_action_request(&open_url).is_err());

        let mut invalid = host_action_request("launch_process");
        assert!(validate_host_action_request(&invalid).is_err());
        invalid.kind = "open_file".to_string();
        invalid.request_id = "short".to_string();
        assert!(validate_host_action_request(&invalid).is_err());

        let mut account_apply = host_action_request("claude_code_apply_account");
        account_apply.account_id = "claude_test".to_string();
        account_apply.model = "claude-sonnet-4-6".to_string();
        assert!(validate_host_action_request(&account_apply).is_ok());
        account_apply.url = "https://example.test".to_string();
        assert!(validate_host_action_request(&account_apply).is_err());
    }

    #[test]
    fn validates_host_action_filename_and_extracts_safe_filter_extensions() {
        let mut request = host_action_request("save_file");
        request.default_filename = "../credentials.json".to_string();
        assert!(validate_host_action_request(&request).is_err());

        assert_eq!(
            host_action_filter_extensions("*.JSON;*.json, archive.tar.gz; *.*; *.bad/ext"),
            vec!["json", "gz"]
        );
    }
}
