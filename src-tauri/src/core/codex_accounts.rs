//! Staged Codex account credentials. Nothing here writes the user's active
//! Codex home. Only the profile apply flow may activate a saved account.
use base64::{engine::general_purpose::URL_SAFE_NO_PAD, Engine};
use serde::{Deserialize, Serialize};
use serde_json::{json, Value};
use std::collections::HashMap;
use std::fs;
use std::path::{Path, PathBuf};
use std::process::{Child, Command, Stdio};
use std::sync::{Mutex, OnceLock};
use std::time::{Duration, Instant};

const MAX_IMPORT_BYTES: usize = 1024 * 1024;
const MAX_ACCOUNTS: usize = 100;
const SESSION_TTL: Duration = Duration::from_secs(20 * 60);
const LOGIN_TIMEOUT: Duration = Duration::from_secs(5 * 60);

#[derive(Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct AccountInfo {
    pub label: String,
    pub account_id: Option<String>,
    pub warnings: Vec<String>,
}

#[derive(Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct AccountSession {
    pub id: String,
    pub source: String,
    pub status: String,
    pub accounts: Vec<AccountInfo>,
    pub error_code: Option<String>,
}

#[derive(Clone, Debug, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct AccountSelection {
    pub session_id: String,
    pub account_index: usize,
}

// Deliberately not Debug or Serialize: raw tokens must not escape into logs
// or API responses. UI receives AccountInfo and an opaque session handle.
pub(crate) struct AccountCredential {
    pub content: String,
    pub info: AccountInfo,
}

struct Session {
    view: AccountSession,
    credentials: Vec<AccountCredential>,
    created: Instant,
    login: Option<LoginProcess>,
}

struct LoginProcess {
    child: Child,
    directory: PathBuf,
}

impl Drop for LoginProcess {
    fn drop(&mut self) {
        if matches!(self.child.try_wait(), Ok(None)) {
            // Stop only the process tree created for this login, never a
            // running Codex client or another user's OAuth callback server.
            #[cfg(windows)]
            {
                let _ = crate::core::platform::hidden_command("taskkill.exe")
                    .args(["/PID", &self.child.id().to_string(), "/T", "/F"])
                    .stdout(Stdio::null())
                    .stderr(Stdio::null())
                    .status();
            }
            #[cfg(unix)]
            {
                let _ = Command::new("/bin/kill")
                    .args(["-TERM", "--", &format!("-{}", self.child.id())])
                    .stdout(Stdio::null())
                    .stderr(Stdio::null())
                    .status();
            }
            let _ = self.child.kill();
        }
        let _ = self.child.wait();
        // Exact, app-created temporary directory; never supplied by a user.
        let _ = fs::remove_dir_all(&self.directory);
    }
}

static SESSIONS: OnceLock<Mutex<HashMap<String, Session>>> = OnceLock::new();

fn sessions() -> &'static Mutex<HashMap<String, Session>> {
    SESSIONS.get_or_init(|| {
        std::thread::spawn(|| loop {
            std::thread::sleep(Duration::from_secs(10));
            if let Some(store) = SESSIONS.get() {
                if let Ok(mut store) = store.lock() {
                    store.retain(|_, session| session.created.elapsed() < SESSION_TTL);
                    for session in store.values_mut() {
                        update_login(session);
                    }
                }
            }
        });
        Mutex::new(HashMap::new())
    })
}

fn text<'a>(value: &'a Value, names: &[&str]) -> Option<&'a str> {
    names.iter().find_map(|name| {
        value
            .get(name)?
            .as_str()
            .map(str::trim)
            .filter(|s| !s.is_empty())
    })
}

fn claims(token: &str) -> Value {
    token
        .split('.')
        .nth(1)
        .and_then(|part| URL_SAFE_NO_PAD.decode(part.trim_end_matches('=')).ok())
        .and_then(|bytes| serde_json::from_slice(&bytes).ok())
        .unwrap_or(Value::Null)
}

fn display_label(value: &str) -> String {
    value
        .chars()
        .filter(|c| !c.is_control())
        .take(160)
        .collect()
}

pub(crate) fn normalize_account(value: &Value) -> Result<AccountCredential, String> {
    // Standard auth.json, Cockpit export and a flattened token JSON bundle.
    let value = value
        .get("auth")
        .filter(|v| v.is_object())
        .or_else(|| value.get("credentials").filter(|v| v.is_object()))
        .unwrap_or(value);
    if !value.is_object() {
        return Err("codexAccount.invalidJson".into());
    }
    if matches!(
        text(value, &["auth_mode", "authMode"]),
        Some("apikey" | "api_key")
    ) {
        return Err("codexAccount.apiKeyOnly".into());
    }
    let tokens = value
        .get("tokens")
        .filter(|v| v.is_object())
        .unwrap_or(value);
    let access = text(tokens, &["access_token", "accessToken"]);
    let id = text(tokens, &["id_token", "idToken"]);
    let (Some(access), Some(id)) = (access, id) else {
        if text(value, &["OPENAI_API_KEY", "openai_api_key", "api_key"]).is_some() {
            return Err("codexAccount.apiKeyOnly".into());
        }
        return Err("codexAccount.missingTokens".into());
    };
    let refresh = text(tokens, &["refresh_token", "refreshToken"]).unwrap_or("");
    let id_claims = claims(id);
    let access_claims = claims(access);
    // An ID token must at least be a JWT with a subject. This is structural
    // validation only, not signature verification or a server login check.
    if id.split('.').count() != 3
        || text(&id_claims, &["sub"]).is_none()
        || [id, access, refresh]
            .iter()
            .any(|token| token.chars().any(char::is_whitespace))
    {
        return Err("codexAccount.invalidTokens".into());
    }
    let account_id = text(tokens, &["account_id", "accountId"])
        .or_else(|| {
            text(
                &id_claims["https://api.openai.com/auth"],
                &["chatgpt_account_id"],
            )
        })
        .or_else(|| {
            text(
                &access_claims["https://api.openai.com/auth"],
                &["chatgpt_account_id"],
            )
        })
        .map(str::to_string);
    let expired = access_claims
        .get("exp")
        .and_then(Value::as_i64)
        .is_some_and(|exp| exp <= chrono::Utc::now().timestamp());
    if expired && refresh.is_empty() {
        return Err("codexAccount.expired".into());
    }
    let mut warnings = Vec::new();
    if refresh.is_empty() {
        warnings.push("codexAccount.noRefresh".into());
    }
    if expired {
        warnings.push("codexAccount.needsRefresh".into());
    }
    let label = text(&id_claims, &["email"])
        .or_else(|| text(value, &["email", "account_name", "name"]))
        .map(display_label)
        .filter(|s| !s.is_empty())
        .unwrap_or_else(|| "Codex OAuth".into());
    let mut auth = json!({"auth_mode":"chatgpt", "OPENAI_API_KEY":null,
        "tokens": {"access_token":access, "id_token":id, "refresh_token":refresh, "account_id":account_id}});
    if let Some(date) =
        text(value, &["last_refresh"]).filter(|s| chrono::DateTime::parse_from_rfc3339(s).is_ok())
    {
        auth["last_refresh"] = json!(date);
    }
    Ok(AccountCredential {
        content: format!(
            "{}\n",
            serde_json::to_string_pretty(&auth).map_err(|_| "codexAccount.invalidJson")?
        ),
        info: AccountInfo {
            label,
            account_id,
            warnings,
        },
    })
}

fn parse_import(content: &str) -> Result<Vec<AccountCredential>, String> {
    if content.len() > MAX_IMPORT_BYTES {
        return Err("codexAccount.tooLarge".into());
    }
    // Never return serde's error text: it may contain part of a secret value.
    let value: Value = serde_json::from_str(content.trim().trim_start_matches('\u{feff}'))
        .map_err(|_| "codexAccount.invalidJson")?;
    let values = value
        .as_array()
        .or_else(|| value.get("accounts").and_then(Value::as_array));
    match values {
        Some(values) if values.is_empty() || values.len() > MAX_ACCOUNTS => {
            Err("codexAccount.accountCount".into())
        }
        Some(values) => values.iter().map(normalize_account).collect(),
        None => normalize_account(&value).map(|item| vec![item]),
    }
}

fn insert_session(
    source: &str,
    credentials: Vec<AccountCredential>,
    login: Option<LoginProcess>,
) -> Result<AccountSession, String> {
    let view = AccountSession {
        id: uuid::Uuid::new_v4().to_string(),
        source: source.into(),
        status: if login.is_some() { "waiting" } else { "ready" }.into(),
        accounts: credentials.iter().map(|item| item.info.clone()).collect(),
        error_code: None,
    };
    let mut store = sessions().lock().map_err(|_| "codexAccount.unavailable")?;
    store.retain(|_, session| session.created.elapsed() < SESSION_TTL);
    if store.len() >= 8 {
        return Err("codexAccount.tooManySessions".into());
    }
    store.insert(
        view.id.clone(),
        Session {
            view: view.clone(),
            credentials,
            created: Instant::now(),
            login,
        },
    );
    Ok(view)
}

pub fn import_json(content: String) -> Result<AccountSession, String> {
    insert_session("json", parse_import(&content)?, None)
}

fn read_auth(path: &Path) -> Result<String, String> {
    use std::io::Read;
    let file = fs::File::open(path).map_err(|_| "codexAccount.localMissing")?;
    let mut content = String::new();
    file.take((MAX_IMPORT_BYTES + 1) as u64)
        .read_to_string(&mut content)
        .map_err(|_| "codexAccount.invalidJson")?;
    if content.len() > MAX_IMPORT_BYTES {
        return Err("codexAccount.tooLarge".into());
    }
    Ok(content)
}

pub fn import_local() -> Result<AccountSession, String> {
    let paths = crate::core::app_paths::app_paths().map_err(|_| "codexAccount.localMissing")?;
    let home = crate::core::app_paths::codex_home_dir(&paths.home_dir);
    let content = read_auth(&home.join("auth.json"))?;
    insert_session("local", parse_import(&content)?, None)
}

fn login_executable() -> Result<String, String> {
    if let Some(cli) = crate::core::platform::resolve_command("codex") {
        return Ok(cli);
    }
    let mut roots = Vec::new();
    if let Ok(Some(cache)) = crate::core::storage::load_detection_cache() {
        for tool in cache.tools {
            if matches!(
                tool.id.as_str(),
                "chatgpt-desktop" | "codex-app" | "codex-client"
            ) {
                if let Some(path) = tool.install_path {
                    roots.push(PathBuf::from(path));
                }
            }
        }
    }
    #[cfg(target_os = "macos")]
    {
        roots.extend([
            PathBuf::from("/Applications/Codex.app"),
            PathBuf::from("/Applications/ChatGPT.app"),
        ]);
        if let Some(home) = dirs::home_dir() {
            roots.extend([
                home.join("Applications/Codex.app"),
                home.join("Applications/ChatGPT.app"),
            ]);
        }
    }
    for root in roots {
        let root = if root.is_file() {
            root.parent().unwrap_or(&root).to_path_buf()
        } else {
            root
        };
        for relative in [
            "Contents/Resources/codex",
            "resources/codex.exe",
            "app/resources/codex.exe",
            "resources/bin/codex.exe",
        ] {
            let path = root.join(relative);
            if path.is_file() {
                return Ok(path.to_string_lossy().into());
            }
        }
    }
    Err("codexAccount.cliMissing".into())
}

fn login_command(executable: &str, directory: &Path) -> Command {
    let mut command = crate::core::platform::hidden_command_with_args(
        executable,
        &[
            "login",
            "-c",
            "cli_auth_credentials_store=\"file\"",
            "-c",
            "forced_login_method=\"chatgpt\"",
        ],
    );
    command
        .env("CODEX_HOME", directory)
        .current_dir(directory)
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null());
    for key in [
        "OPENAI_API_KEY",
        "CODEX_API_KEY",
        "CODEX_ACCESS_TOKEN",
        "OPENAI_BASE_URL",
        "CHATGPT_BASE_URL",
    ] {
        command.env_remove(key);
    }
    #[cfg(unix)]
    {
        use std::os::unix::process::CommandExt;
        command.process_group(0);
    }
    command
}

pub fn start_login() -> Result<AccountSession, String> {
    // Reserve the single loopback login flow before starting the child.
    static START_LOCK: Mutex<()> = Mutex::new(());
    let _guard = START_LOCK.lock().map_err(|_| "codexAccount.unavailable")?;
    if sessions()
        .lock()
        .map_err(|_| "codexAccount.unavailable")?
        .values()
        .any(|s| s.login.is_some())
    {
        return Err("codexAccount.loginBusy".into());
    }
    let executable = login_executable()?;
    let directory =
        std::env::temp_dir().join(format!("xiass-codex-login-{}", uuid::Uuid::new_v4()));
    let mut builder = fs::DirBuilder::new();
    #[cfg(unix)]
    {
        use std::os::unix::fs::DirBuilderExt;
        builder.mode(0o700);
    }
    builder
        .create(&directory)
        .map_err(|_| "codexAccount.startFailed")?;
    let child = match login_command(&executable, &directory).spawn() {
        Ok(child) => child,
        Err(_) => {
            let _ = fs::remove_dir(&directory);
            return Err("codexAccount.startFailed".into());
        }
    };
    insert_session("oauth", Vec::new(), Some(LoginProcess { child, directory }))
}

fn update_login(session: &mut Session) {
    let Some(login) = session.login.as_mut() else {
        return;
    };
    let result = if session.created.elapsed() >= LOGIN_TIMEOUT {
        Some(Err("codexAccount.loginTimeout".to_string()))
    } else {
        match login.child.try_wait() {
            Ok(Some(status)) if status.success() => Some(
                read_auth(&login.directory.join("auth.json")).and_then(|text| parse_import(&text)),
            ),
            Ok(Some(_)) | Err(_) => Some(Err("codexAccount.loginFailed".into())),
            Ok(None) => None,
        }
    };
    if let Some(result) = result {
        session.login = None;
        match result {
            Ok(credentials) => {
                session.view.status = "ready".into();
                session.view.accounts = credentials.iter().map(|item| item.info.clone()).collect();
                session.credentials = credentials;
            }
            Err(code) => {
                session.view.status = "failed".into();
                session.view.error_code = Some(code);
            }
        }
    }
}

pub fn poll_session(id: String) -> Result<AccountSession, String> {
    let mut store = sessions().lock().map_err(|_| "codexAccount.unavailable")?;
    let session = store
        .get_mut(&id)
        .filter(|s| s.created.elapsed() < SESSION_TTL)
        .ok_or("codexAccount.sessionExpired")?;
    update_login(session);
    Ok(session.view.clone())
}

pub fn discard_session(id: String) -> Result<(), String> {
    sessions()
        .lock()
        .map_err(|_| "codexAccount.unavailable")?
        .remove(&id);
    Ok(())
}

pub fn cancel_all() {
    if let Some(store) = SESSIONS.get() {
        if let Ok(mut store) = store.lock() {
            store.clear();
        }
    }
}

pub(crate) fn selected_content(selection: &AccountSelection) -> Result<String, String> {
    let store = sessions().lock().map_err(|_| "codexAccount.unavailable")?;
    let session = store
        .get(&selection.session_id)
        .filter(|s| s.created.elapsed() < SESSION_TTL && s.view.status == "ready")
        .ok_or("codexAccount.sessionExpired")?;
    session
        .credentials
        .get(selection.account_index)
        .map(|account| account.content.clone())
        .ok_or_else(|| "codexAccount.chooseAccount".into())
}

/// Identity comparison, not token verification. Decode claims only to avoid
/// replacing account A's saved rotating token with account B's live token.
pub(crate) fn same_account(left: &Value, right: &Value) -> bool {
    if text(left, &["auth_mode"]) == Some("apikey") || text(right, &["auth_mode"]) == Some("apikey")
    {
        return false;
    }
    let left = &left["tokens"];
    let right = &right["tokens"];
    let l_id = text(left, &["id_token"]);
    let r_id = text(right, &["id_token"]);
    let (Some(l_id), Some(r_id)) = (l_id, r_id) else {
        return false;
    };
    let l_claims = claims(l_id);
    let r_claims = claims(r_id);
    let l_account = text(left, &["account_id"]).or_else(|| {
        text(
            &l_claims["https://api.openai.com/auth"],
            &["chatgpt_account_id"],
        )
    });
    let r_account = text(right, &["account_id"]).or_else(|| {
        text(
            &r_claims["https://api.openai.com/auth"],
            &["chatgpt_account_id"],
        )
    });
    if l_account != r_account {
        return false;
    }
    if let (Some(l_sub), Some(r_sub)) = (text(&l_claims, &["sub"]), text(&r_claims, &["sub"])) {
        return l_sub == r_sub;
    }
    let l_refresh = text(left, &["refresh_token"]);
    let r_refresh = text(right, &["refresh_token"]);
    (l_refresh.is_some() && l_refresh == r_refresh)
        || (l_id == r_id
            && text(left, &["access_token"]).is_some()
            && text(left, &["access_token"]) == text(right, &["access_token"]))
}

pub(crate) fn refreshed_account_content(saved: &str, live: &Value) -> Option<String> {
    let saved_value = serde_json::from_str::<Value>(saved).ok()?;
    if !same_account(&saved_value, live) {
        return None;
    }
    let account = normalize_account(live).ok()?;
    (account.content != saved).then_some(account.content)
}

#[cfg(test)]
mod tests;
