use super::*;
use serde_json::{json, Value};

fn fixture() -> Value {
    let assets: Vec<_> = ["aarch64.dmg", "x64.dmg", "x64-setup.exe"].iter().map(|suffix| {
        let name = format!("XIASS.Tools_1.8.7_{suffix}");
        json!({"name": name, "state":"uploaded", "size":9,
            "digest":format!("sha256:{:x}", Sha256::digest(b"installer")),
            "browser_download_url":format!("https://github.com/{REPOSITORY}/releases/download/v1.8.7/{name}")})
    }).collect();
    json!({"tag_name":"v1.8.7", "name":"XIASS Tools v1.8.7", "published_at":null,
        "draft":false, "prerelease":false, "assets":assets})
}

#[test]
fn matches_all_three_supported_platform_installers() {
    for (target, suffix) in [
        ("darwin-aarch64", "aarch64.dmg"),
        ("darwin-x86_64", "x64.dmg"),
        ("windows-x86_64", "x64-setup.exe"),
    ] {
        let release = release_from_json(&fixture().to_string(), target).unwrap();
        assert_eq!(release.version, "1.8.7");
        assert_eq!(
            release.installer.unwrap().filename,
            format!("XIASS.Tools_1.8.7_{suffix}")
        );
        assert_eq!(
            release.url,
            format!("https://github.com/{REPOSITORY}/releases/latest")
        );
    }
}

#[test]
fn page_fallback_discovers_the_latest_version_and_platform_asset_without_api_metadata() {
    let page = r#"<include-fragment src="https://github.com/xyf0104/Antigravity-WF-Assistant/releases/expanded_assets/v1.8.7"></include-fragment>"#;
    let assets = r#"<li><a href="/xyf0104/Antigravity-WF-Assistant/releases/download/v1.8.7/XIASS.Tools_1.8.7_aarch64.dmg">XIASS.Tools_1.8.7_aarch64.dmg</a>
      <clipboard-copy value="sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"></clipboard-copy></li>"#;
    let version = page_release_version(page).unwrap();
    let asset = installer_from_assets_page(assets, &version, "darwin-aarch64")
        .unwrap()
        .unwrap();
    assert_eq!(version, "1.8.7");
    assert_eq!(asset.size, 0);
    assert_eq!(asset.sha256, "a".repeat(64));
    assert_eq!(asset.url, format!("https://github.com/{REPOSITORY}/releases/download/v1.8.7/XIASS.Tools_1.8.7_aarch64.dmg"));
}

#[test]
fn page_fallback_reports_a_missing_platform_asset_without_guessing_a_filename() {
    let assets = r#"<li><a href="/xyf0104/Antigravity-WF-Assistant/releases/download/v1.8.7/XIASS.Tools_1.8.7_x64.dmg">XIASS.Tools_1.8.7_x64.dmg</a></li>"#;
    assert!(
        installer_from_assets_page(assets, "1.8.7", "darwin-aarch64")
            .unwrap()
            .is_none()
    );
}

#[test]
fn missing_or_unsupported_platform_is_never_guessed() {
    let mut value = fixture();
    value["assets"] = json!([]);
    assert!(release_from_json(&value.to_string(), "darwin-aarch64")
        .unwrap()
        .installer
        .is_none());
    assert!(release_from_json(&fixture().to_string(), "linux-x86_64")
        .unwrap()
        .installer
        .is_none());
}

#[test]
fn rejects_drafts_prereleases_and_invalid_tags() {
    for (key, replacement) in [
        ("draft", json!(true)),
        ("prerelease", json!(true)),
        ("tag_name", json!("v1.8.7-beta")),
        ("tag_name", json!("../../bad")),
        ("tag_name", json!("v1.8")),
    ] {
        let mut value = fixture();
        value[key] = replacement;
        assert!(release_from_json(&value.to_string(), "darwin-aarch64").is_err());
    }
}

#[test]
fn rejects_invalid_or_missing_checksums_and_empty_assets() {
    for (key, replacement) in [
        ("digest", Value::Null),
        ("digest", json!("sha256:bad")),
        ("digest", json!(format!("sha256:{}", "g".repeat(64)))),
        ("size", json!(0)),
    ] {
        let mut value = fixture();
        value["assets"][0][key] = replacement;
        assert!(release_from_json(&value.to_string(), "darwin-aarch64").is_err());
    }
}

#[test]
fn rejects_downloads_from_other_hosts_repos_tags_and_untrusted_urls() {
    let good = fixture()["assets"][0]["browser_download_url"]
        .as_str()
        .unwrap()
        .to_string();
    for bad in [
        good.replace("https:", "http:"),
        good.replace("github.com", "example.com"),
        good.replace("xyf0104", "someone-else"),
        good.replace("/v1.8.7/", "/v1.8.6/"),
        format!("{good}?redirect=evil"),
        format!("{good}#fragment"),
        good.replace("https://", "https://user:password@"),
        good.replace("github.com/", "github.com:8080/"),
    ] {
        let mut value = fixture();
        value["assets"][0]["browser_download_url"] = json!(bad);
        assert!(release_from_json(&value.to_string(), "darwin-aarch64").is_err());
    }
}

#[test]
fn ignores_unfinished_uploads_and_mismatched_names() {
    for (key, replacement) in [
        ("state", json!("starter")),
        ("name", json!("../../XIASS.Tools_1.8.7_aarch64.dmg")),
        ("name", json!("XIASS.Tools_1.8.6_aarch64.dmg")),
    ] {
        let mut value = fixture();
        value["assets"][0][key] = replacement;
        assert!(release_from_json(&value.to_string(), "darwin-aarch64")
            .unwrap()
            .installer
            .is_none());
    }
}

#[test]
fn rejects_duplicate_platform_assets() {
    let mut value = fixture();
    let duplicate = value["assets"][0].clone();
    value["assets"].as_array_mut().unwrap().push(duplicate);
    assert!(release_from_json(&value.to_string(), "darwin-aarch64").is_err());
}

#[test]
fn accepts_space_names_only_with_the_matching_encoded_url() {
    let mut value = fixture();
    value["assets"][0]["name"] = json!("XIASS Tools_1.8.7_aarch64.dmg");
    value["assets"][0]["browser_download_url"] = json!(format!(
        "https://github.com/{REPOSITORY}/releases/download/v1.8.7/XIASS%20Tools_1.8.7_aarch64.dmg"
    ));
    assert!(release_from_json(&value.to_string(), "darwin-aarch64")
        .unwrap()
        .installer
        .is_some());
}

#[test]
fn verifies_the_file_and_detects_tampering_or_truncation() {
    let root = std::env::temp_dir().join(format!("xiass-update-{}", uuid::Uuid::new_v4()));
    fs::create_dir_all(&root).unwrap();
    let path = root.join("test.dmg");
    let asset = release_from_json(&fixture().to_string(), "darwin-aarch64")
        .unwrap()
        .installer
        .unwrap();
    assert!(verify_file(&path, &asset).is_err());
    fs::write(&path, b"installer").unwrap();
    assert!(verify_file(&path, &asset).is_ok());
    fs::write(&path, b"tamper!!!").unwrap();
    assert!(verify_file(&path, &asset).is_err());
    fs::write(&path, b"short").unwrap();
    assert!(verify_file(&path, &asset).is_err());
    fs::remove_dir_all(&root).unwrap();
}

#[test]
fn accepts_only_bounded_numeric_versions() {
    for good in ["1.8.7", "2.0.0", "10.2.3"] {
        assert!(valid_version(good));
    }
    for bad in [
        "",
        "v1.8.7",
        "1.8",
        "../1.8.7",
        "1.8.7/evil",
        "1.8.7?x",
        "1.8.7-beta",
        "9999999999.0.0",
    ] {
        assert!(!valid_version(bad));
    }
}

#[test]
#[ignore = "Downloads the real public GitHub installer; run explicitly for release smoke testing."]
fn live_github_installer_download_and_checksum() {
    let release = check_update().unwrap();
    assert!(release.installer.is_some());
    let path = download_update(&release.version, |_| {}).unwrap();
    let installer = DOWNLOADED_INSTALLER
        .lock()
        .unwrap()
        .clone()
        .expect("downloaded installer state");
    verify_file(Path::new(&path), &installer.asset).unwrap();
    println!(
        "Verified GitHub v{} installer: {} ({} bytes)",
        release.version, installer.asset.filename, installer.asset.size
    );
    let _ = fs::remove_file(&installer.path);
}
