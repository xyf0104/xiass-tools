use super::*;

fn jwt(value: Value) -> String {
    format!(
        "fixture.{}.not-a-signature",
        URL_SAFE_NO_PAD.encode(value.to_string())
    )
}

fn fixture(sub: &str, workspace: &str) -> Value {
    json!({"auth_mode":"chatgpt", "tokens":{
        "id_token":jwt(json!({"sub":sub,"email":"test@example.invalid","https://api.openai.com/auth":{"chatgpt_account_id":workspace}})),
        "access_token":jwt(json!({"exp":chrono::Utc::now().timestamp()+3600})),
        "refresh_token":"fixture-refresh-secret", "account_id": workspace}})
}

#[test]
fn accepts_standard_cockpit_flattened_and_wrapped_token_json() {
    let standard = fixture("one", "workspace");
    for input in [
        standard.clone(),
        json!({"auth_mode":"oauth","tokens":standard["tokens"]}),
        standard["tokens"].clone(),
        json!({"credentials":standard["tokens"]}),
        json!({"auth":standard.clone()}),
    ] {
        let account = normalize_account(&input).unwrap();
        let written: Value = serde_json::from_str(&account.content).unwrap();
        assert_eq!(written["auth_mode"], "chatgpt");
        assert_eq!(written["tokens"], standard["tokens"]);
        assert!(written["OPENAI_API_KEY"].is_null());
        assert_eq!(account.info.label, "test@example.invalid");
    }
}

#[test]
fn accepts_legacy_cockpit_nested_containers_and_field_aliases() {
    let standard = fixture("legacy-user", "legacy-workspace");
    let input = json!({
        "name": "Legacy Cockpit account",
        "credentials": {
            "authentication": {
                "tokenSet": {
                    "id-token": standard["tokens"]["id_token"],
                    "access-token": standard["tokens"]["access_token"],
                    "refresh-token": standard["tokens"]["refresh_token"],
                    "chatgptAccountId": "legacy-workspace"
                }
            }
        },
        "password": "must-not-be-copied",
        "cookies": {"session": "must-not-be-copied"}
    });
    let account = normalize_account(&input).unwrap();
    let written: Value = serde_json::from_str(&account.content).unwrap();
    assert_eq!(written["auth_mode"], "chatgpt");
    assert_eq!(written["tokens"]["account_id"], "legacy-workspace");
    assert_eq!(account.info.label, "test@example.invalid");
    assert!(!account.content.contains("must-not-be-copied"));
}

#[test]
fn accepts_cockpit_token_container_arrays() {
    let standard = fixture("array-user", "array-workspace");
    let input = json!({
        "OPENAI_API_KEY": null,
        "tokens": [
            {
                "access_token": standard["tokens"]["access_token"],
                "id_token": standard["tokens"]["id_token"],
                "refresh_token": standard["tokens"]["refresh_token"],
                "account_id": standard["tokens"]["account_id"]
            }
        ]
    });
    let account = normalize_account(&input).unwrap();
    let written: Value = serde_json::from_str(&account.content).unwrap();
    assert_eq!(written["auth_mode"], "chatgpt");
    assert_eq!(written["tokens"], standard["tokens"]);
    assert_eq!(account.info.account_id.as_deref(), Some("array-workspace"));
}

#[test]
fn accepts_cockpit_accounts_with_access_and_refresh_without_id_token() {
    let input = json!({
        "type": "cockpit-export",
        "accounts": [{
            "name": "workspace@example.invalid----myWorkspace",
            "platform": "openai",
            "type": "oauth",
            "credentials": {
                "access_token": "cockpit-access-token",
                "refresh_token": "cockpit-refresh-token",
                "chatgpt_account_id": "cockpit-account",
                "organization_id": "cockpit-workspace",
                "expires_at": 1900000000,
                "expires_in": 3600,
                "plan_type": "plus"
            },
            "extra": {
                "email": "workspace@example.invalid",
                "display_name": "myWorkspace"
            },
            "concurrency": 2,
            "priority": 0
        }]
    });
    let accounts = parse_import(&input.to_string()).unwrap();
    assert_eq!(accounts.len(), 1);
    let written: Value = serde_json::from_str(&accounts[0].content).unwrap();
    assert_eq!(written["auth_mode"], "chatgpt");
    assert_eq!(written["tokens"]["access_token"], "cockpit-access-token");
    assert_eq!(written["tokens"]["refresh_token"], "cockpit-refresh-token");
    assert_eq!(written["tokens"]["account_id"], "cockpit-account");
    assert!(written["tokens"].get("id_token").is_none());
    assert_eq!(
        accounts[0].info.account_id.as_deref(),
        Some("cockpit-account")
    );
}

#[test]
fn accepts_sub2api_and_legacy_batch_envelopes() {
    let first = fixture("one", "team-a");
    let second = fixture("two", "team-b");
    let sub2api = json!({
        "type": "sub2api-data",
        "accounts": [
            {"type":"oauth", "credentials": first["tokens"]},
            {"type":"oauth", "credentials": second["tokens"]}
        ]
    });
    let nested = json!({"data":{"items":sub2api["accounts"]}});
    for input in [sub2api, nested] {
        let accounts = parse_import(&input.to_string()).unwrap();
        assert_eq!(accounts.len(), 2);
        assert_eq!(accounts[0].info.account_id.as_deref(), Some("team-a"));
        assert_eq!(accounts[1].info.account_id.as_deref(), Some("team-b"));
    }
}

#[test]
fn credential_container_traversal_is_bounded() {
    let standard = fixture("deep", "team-deep");
    let accepted = json!({"credentials":{"auth":{"tokens":standard["tokens"]}}});
    assert!(normalize_account(&accepted).is_ok());
    let rejected = json!({"credentials":{"auth":{"tokens":{"tokenSet":standard["tokens"]}}}});
    assert_eq!(
        normalize_account(&rejected).err().as_deref(),
        Some("codexAccount.missingTokens")
    );
}

#[test]
fn keeps_only_native_auth_fields_and_recovers_account_id_from_claims() {
    let mut input = fixture("one", "workspace");
    input["tokens"]
        .as_object_mut()
        .unwrap()
        .remove("account_id");
    input["password"] = json!("never-copy-this");
    input["api_base_url"] = json!("https://not-the-official-backend.invalid");
    input["last_refresh"] = json!("2026-01-02T03:04:05Z");
    let account = normalize_account(&input).unwrap();
    assert!(!account.content.contains("never-copy-this"));
    assert!(!account.content.contains("not-the-official"));
    assert_eq!(account.info.account_id.as_deref(), Some("workspace"));
    assert!(account.content.contains("2026-01-02T03:04:05Z"));
}

#[test]
fn rejects_api_key_missing_tokens_malformed_and_oversized_input_without_echoing_secrets() {
    for (input, expected) in [
        (
            "{\"tokens\":\"fixture-sensitive-value\",}",
            "codexAccount.invalidJson",
        ),
        (
            "{\"auth_mode\":\"apikey\",\"OPENAI_API_KEY\":\"fixture-secret\"}",
            "codexAccount.apiKeyOnly",
        ),
        ("{\"auth_mode\":\"chatgpt\"}", "codexAccount.missingTokens"),
        (
            "{\"refresh_token\":\"fixture-secret\"}",
            "codexAccount.missingTokens",
        ),
        ("[]", "codexAccount.accountCount"),
        ("null", "codexAccount.invalidJson"),
    ] {
        assert_eq!(parse_import(input).err().as_deref(), Some(expected));
    }
    assert_eq!(
        parse_import(&" ".repeat(MAX_IMPORT_BYTES + 1))
            .err()
            .as_deref(),
        Some("codexAccount.tooLarge")
    );
    let mut api = fixture("one", "workspace");
    api["auth_mode"] = json!("apikey");
    assert_eq!(
        normalize_account(&api).err().as_deref(),
        Some("codexAccount.apiKeyOnly")
    );
}

#[test]
fn expired_access_requires_refresh_and_short_lived_tokens_are_explicit() {
    let mut input = fixture("one", "workspace");
    input["tokens"]["access_token"] = json!(jwt(json!({"exp":1})));
    assert_eq!(
        normalize_account(&input).unwrap().info.warnings,
        ["codexAccount.needsRefresh"]
    );
    input["tokens"]
        .as_object_mut()
        .unwrap()
        .remove("refresh_token");
    assert_eq!(
        normalize_account(&input).err().as_deref(),
        Some("codexAccount.expired")
    );
    input["tokens"]["access_token"] =
        json!(jwt(json!({"exp":chrono::Utc::now().timestamp()+3600})));
    assert_eq!(
        normalize_account(&input).unwrap().info.warnings,
        ["codexAccount.noRefresh"]
    );
}

#[test]
fn multiple_accounts_require_explicit_selection_and_responses_never_include_tokens() {
    let first = fixture("one", "team-a");
    let second = fixture("two", "team-b");
    let session = import_json(json!({"accounts":[first, second]}).to_string()).unwrap();
    assert_eq!(session.accounts.len(), 2);
    let response = serde_json::to_string(&session).unwrap();
    assert!(!response.contains("fixture-refresh-secret"));
    assert!(!response.contains("id_token"));
    assert!(!response.contains("access_token"));
    let selected = selected_content(&AccountSelection {
        session_id: session.id.clone(),
        account_index: 1,
    })
    .unwrap();
    assert_eq!(
        serde_json::from_str::<Value>(&selected).unwrap()["tokens"]["account_id"],
        "team-b"
    );
    assert!(selected_content(&AccountSelection {
        session_id: session.id.clone(),
        account_index: 99
    })
    .is_err());
    discard_session(session.id.clone()).unwrap();
    assert!(selected_content(&AccountSelection {
        session_id: session.id,
        account_index: 0
    })
    .is_err());
}

#[test]
fn refreshed_tokens_match_only_the_same_user_and_workspace() {
    let first = fixture("one", "team-a");
    let mut refreshed = first.clone();
    refreshed["tokens"]["refresh_token"] = json!("rotated-fixture-secret");
    refreshed["tokens"]["access_token"] = json!("rotated-access");
    assert!(same_account(&first, &refreshed));
    assert!(!same_account(&first, &fixture("two", "team-a")));
    assert!(!same_account(&first, &fixture("one", "team-b")));
    assert!(!same_account(&first, &json!({"auth_mode":"chatgpt"})));
    refreshed["auth_mode"] = json!("apikey");
    assert!(!same_account(&first, &refreshed));
}

#[test]
fn login_isolated_from_active_codex_home_and_api_credentials() {
    let directory = PathBuf::from("test-login-home");
    let command = login_command("codex", &directory);
    let args = command
        .get_args()
        .map(|s| s.to_string_lossy())
        .collect::<Vec<_>>()
        .join(" ");
    assert!(args.contains("login"));
    assert!(args.contains("cli_auth_credentials_store"));
    assert!(args.contains("forced_login_method"));
    assert_eq!(command.get_current_dir(), Some(directory.as_path()));
    let env = command.get_envs().collect::<HashMap<_, _>>();
    assert_eq!(
        env.get(std::ffi::OsStr::new("CODEX_HOME")),
        Some(&Some(directory.as_os_str()))
    );
    assert_eq!(env.get(std::ffi::OsStr::new("OPENAI_API_KEY")), Some(&None));
    assert!(!args.contains("fixture-secret"));
}

#[cfg(unix)]
#[test]
fn completed_and_failed_login_children_are_reaped_and_temp_files_removed() {
    for successful in [true, false] {
        let directory =
            std::env::temp_dir().join(format!("xiass-login-test-{}", uuid::Uuid::new_v4()));
        fs::create_dir(&directory).unwrap();
        fs::write(
            directory.join("auth.json"),
            fixture("one", "team-a").to_string(),
        )
        .unwrap();
        let child = Command::new("/bin/sh")
            .args(["-c", if successful { "exit 0" } else { "exit 1" }])
            .spawn()
            .unwrap();
        let session = insert_session(
            "oauth",
            Vec::new(),
            Some(LoginProcess {
                child,
                directory: directory.clone(),
            }),
        )
        .unwrap();
        let mut view = session.clone();
        for _ in 0..100 {
            view = poll_session(session.id.clone()).unwrap();
            if view.status != "waiting" {
                break;
            }
            std::thread::sleep(Duration::from_millis(10));
        }
        assert_eq!(view.status, if successful { "ready" } else { "failed" });
        assert!(!directory.exists());
        discard_session(session.id).unwrap();
    }
}
