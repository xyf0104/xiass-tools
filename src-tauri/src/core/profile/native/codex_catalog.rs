//! Codex loads model_catalog_json, not model_providers.<id>.models.
//! Build a private, content-addressed copy so previews are read-only and rolling
//! back config.toml also restores its previous catalog without modifying it.
use super::plan::{NativeConfigWriteKind, NativeConfigWritePlan};
use crate::core::types::ProfileDraft;
use serde_json::{json, Value};
use sha2::{Digest, Sha256};
use std::{
    collections::HashSet,
    fs,
    path::{Path, PathBuf},
};

pub(super) const MODEL_IDS: &[&str] = &[
    "gpt-6-astra",
    "gpt-6-sol",
    "gpt-6-luna",
    "gpt-5.6-sol",
    "gpt-5.6-terra",
    "gpt-5.6-luna",
];

fn parse_catalog(text: &str) -> Result<Value, String> {
    let value: Value = serde_json::from_str(text).map_err(|_| {
        "Codex model catalog is not valid JSON; no configuration was changed.".to_string()
    })?;
    let models = value
        .get("models")
        .and_then(Value::as_array)
        .ok_or("Codex model catalog must contain a models array.")?;
    if models.iter().any(|entry| {
        entry
            .get("slug")
            .and_then(Value::as_str)
            .is_none_or(|s| s.trim().is_empty())
    }) {
        return Err("Codex model catalog contains an entry without a model slug.".into());
    }
    Ok(value)
}

fn resolve_path(home: &Path, value: &str) -> PathBuf {
    let path = PathBuf::from(value.trim());
    if path.is_absolute() {
        path
    } else {
        home.join(path)
    }
}

fn catalog_setting(document: &toml_edit::DocumentMut) -> Option<&str> {
    document
        .get("profile")
        .and_then(|v| v.as_str())
        .and_then(|name| {
            document
                .get("profiles")?
                .get(name)?
                .get("model_catalog_json")
        })
        .or_else(|| document.get("model_catalog_json"))
        .and_then(|v| v.as_str())
        .filter(|s| !s.trim().is_empty())
}

// This is a conservative protocol-compatible fallback, not a claim of backend
// entitlement. Existing exact/family catalog metadata is preferred below.
fn fallback_entry(id: &str, window: u64) -> Value {
    json!({
        "slug": id, "display_name": id, "description": id,
        "default_reasoning_level": "medium",
        "supported_reasoning_levels": [
            {"effort": "low", "description": "Low"},
            {"effort": "medium", "description": "Medium"},
            {"effort": "high", "description": "High"}
        ],
        "shell_type": "shell_command", "visibility": "list", "supported_in_api": true,
        "priority": 100, "availability_nux": null, "upgrade": null,
        "base_instructions": "", "supports_reasoning_summaries": false,
        "support_verbosity": false, "default_verbosity": null,
        "apply_patch_tool_type": "freeform", "context_window": window,
        "truncation_policy": {"mode": "tokens", "limit": 10000},
        "supports_parallel_tool_calls": false, "input_modalities": ["text", "image"],
        "experimental_supported_tools": []
    })
}

fn merge_catalog(mut catalog: Value, profile: &ProfileDraft) -> Value {
    let models = catalog["models"].as_array_mut().expect("validated models");
    let mut seen = HashSet::new();
    models.retain(|entry| seen.insert(entry["slug"].as_str().unwrap().trim().to_string()));
    // Use only templates present before adding anything, never another fallback.
    let templates = models.clone();
    let selected = profile.model.trim();
    let review = profile.review_model.as_deref().unwrap_or("").trim();
    for id in MODEL_IDS.iter().copied().chain([selected, review]) {
        if id.is_empty() || !seen.insert(id.to_string()) {
            continue;
        }
        let family = match id {
            "gpt-6-sol" => Some("gpt-5.6-sol"),
            "gpt-6-luna" => Some("gpt-5.6-luna"),
            _ => None,
        };
        let template = family
            .and_then(|slug| templates.iter().find(|m| m["slug"] == slug))
            .or_else(|| {
                id.starts_with("gpt-")
                    .then(|| templates.iter().find(|m| m["slug"] == "gpt-6-astra"))
                    .flatten()
            });
        let mut entry = template
            .cloned()
            .unwrap_or_else(|| fallback_entry(id, profile.model_context_window.unwrap_or(272000)));
        entry["slug"] = json!(id);
        entry["display_name"] = json!(id);
        entry["description"] = json!(id);
        entry["visibility"] = json!(if id == "codex-auto-review" {
            "hide"
        } else {
            "list"
        });
        entry["supported_in_api"] = json!(true);
        // A new ID is not the old template's upgrade target or subscription gate.
        if let Some(object) = entry.as_object_mut() {
            for key in ["upgrade", "availability_nux", "available_in_plans"] {
                object.remove(key);
            }
        }
        models.push(entry);
    }
    catalog
}

pub(in crate::core::profile) fn prepare(
    config_path: &Path,
    content: &str,
    profile: &ProfileDraft,
) -> Result<(String, Vec<NativeConfigWritePlan>), String> {
    let parent = config_path
        .parent()
        .ok_or("Codex config has no parent directory.")?;
    // Store an absolute pointer even when CODEX_HOME was supplied relatively.
    // Otherwise Codex would resolve "relative-home/xiass-tools/..." twice.
    let absolute_home = if parent.is_absolute() {
        parent.to_path_buf()
    } else {
        std::env::current_dir()
            .map_err(|e| e.to_string())?
            .join(parent)
    };
    let home = absolute_home.as_path();
    let mut document = content
        .parse::<toml_edit::DocumentMut>()
        .map_err(|e| e.to_string())?;
    // Recent Codex also supports separate <profile>.config.toml files. Do not
    // let either kind of selected profile silently shadow the repaired pointer.
    let selected = document
        .get("profile")
        .and_then(|v| v.as_str())
        .map(str::to_string);
    let mut overlay = None;
    if let Some(name) = selected.as_deref() {
        if name.is_empty() || name.contains(['/', '\\']) || name == "." || name == ".." {
            return Err(
                "Invalid selected Codex profile name; no configuration was changed.".into(),
            );
        }
        let path = home.join(format!("{name}.config.toml"));
        if path.exists() {
            let text = fs::read_to_string(&path)
                .map_err(|_| "Could not read the selected Codex profile.")?;
            let doc = text
                .parse::<toml_edit::DocumentMut>()
                .map_err(|_| "The selected Codex profile is invalid TOML.")?;
            overlay = Some((path, doc));
        }
    }
    let configured_path = overlay
        .as_ref()
        .and_then(|(_, doc)| catalog_setting(doc))
        .or_else(|| catalog_setting(&document))
        .map(|value| resolve_path(home, value));
    let source = match configured_path.as_ref().filter(|path| path.exists()) {
        Some(path) => parse_catalog(
            &fs::read_to_string(path)
                .map_err(|_| "Could not read the configured Codex model catalog.")?,
        )?,
        None => {
            // Cache metadata is optional; a broken cache must not prevent repair
            // of a missing custom catalog. An explicitly malformed catalog does.
            fs::read_to_string(home.join("models_cache.json"))
                .ok()
                .and_then(|text| parse_catalog(&text).ok())
                .unwrap_or_else(|| json!({"models": []}))
        }
    };
    let catalog = merge_catalog(source, profile);
    let catalog_content = format!(
        "{}\n",
        serde_json::to_string_pretty(&catalog).map_err(|e| e.to_string())?
    );
    let hash = format!("{:x}", Sha256::digest(catalog_content.as_bytes()));
    let path = home
        .join("xiass-tools")
        .join("model-catalogs")
        .join(format!("catalog-{}.json", &hash[..16]));
    let mut plans = vec![NativeConfigWritePlan::write(
        path.clone(),
        catalog_content,
        NativeConfigWriteKind::CodexModelCatalog,
    )];
    let pointer = path.to_string_lossy().to_string();
    document["model_catalog_json"] = toml_edit::value(&pointer);
    if let Some(name) = selected.as_deref() {
        if let Some(table) = document
            .get_mut("profiles")
            .and_then(|v| v.get_mut(name))
            .and_then(|v| v.as_table_like_mut())
        {
            table.insert("model_catalog_json", toml_edit::value(&pointer));
        }
    }
    if let Some((path, mut doc)) = overlay {
        doc["model_catalog_json"] = toml_edit::value(&pointer);
        plans.push(NativeConfigWritePlan::write(
            path,
            doc.to_string(),
            NativeConfigWriteKind::CodexCatalogProfile,
        ));
    }
    Ok((document.to_string(), plans))
}

pub(in crate::core::profile) fn verify(plan: &NativeConfigWritePlan) -> Result<bool, String> {
    let content = fs::read_to_string(&plan.path).map_err(|e| e.to_string())?;
    if content != plan.content {
        return Ok(false);
    }
    match plan.kind {
        NativeConfigWriteKind::CodexModelCatalog => {
            let catalog = parse_catalog(&content)?;
            Ok(MODEL_IDS.iter().all(|id| {
                catalog["models"]
                    .as_array()
                    .unwrap()
                    .iter()
                    .any(|m| m["slug"] == *id)
            }))
        }
        NativeConfigWriteKind::CodexCatalogProfile => {
            Ok(content.parse::<toml_edit::DocumentMut>().is_ok())
        }
        _ => Ok(false),
    }
}

pub(in crate::core::profile) fn verify_config_pointer(
    plan: &NativeConfigWritePlan,
) -> Result<bool, String> {
    let expected = plan
        .content
        .parse::<toml_edit::DocumentMut>()
        .map_err(|e| e.to_string())?;
    let Some(pointer) = expected.get("model_catalog_json").and_then(|v| v.as_str()) else {
        return Ok(true); // Non-apply lifecycle plans may not manage the catalog.
    };
    let written = fs::read_to_string(&plan.path)
        .map_err(|e| e.to_string())?
        .parse::<toml_edit::DocumentMut>()
        .map_err(|e| e.to_string())?;
    if written.get("model_catalog_json").and_then(|v| v.as_str()) != Some(pointer)
        || catalog_setting(&written) != Some(pointer)
    {
        return Ok(false);
    }
    if let Some(name) = written.get("profile").and_then(|v| v.as_str()) {
        if name.contains(['/', '\\']) {
            return Ok(false);
        }
        let overlay = plan
            .path
            .parent()
            .unwrap()
            .join(format!("{name}.config.toml"));
        if overlay.exists() {
            let overlay = fs::read_to_string(overlay)
                .map_err(|e| e.to_string())?
                .parse::<toml_edit::DocumentMut>()
                .map_err(|e| e.to_string())?;
            if catalog_setting(&overlay).is_some_and(|value| value != pointer) {
                return Ok(false);
            }
        }
    }
    let catalog = parse_catalog(&fs::read_to_string(pointer).map_err(|e| e.to_string())?)?;
    Ok(MODEL_IDS.iter().all(|id| {
        catalog["models"]
            .as_array()
            .unwrap()
            .iter()
            .any(|m| m["slug"] == *id)
    }))
}

#[cfg(test)]
mod tests {
    use super::super::plan::apply_native_config_write_plan;
    use super::*;
    use crate::core::profile::profile_tests::{test_paths, test_profile};
    use crate::core::types::ProviderApplyMode;

    #[test]
    fn catalog_is_real_private_idempotent_and_preserves_custom_metadata() {
        let paths = test_paths();
        let home = paths.home_dir.join(".codex");
        fs::create_dir_all(&home).unwrap();
        let original = json!({"custom_metadata": true, "models": [
            {"slug":"custom-model", "private_setting": "keep"},
            {"slug":"gpt-5.6-sol", "supports_search_tool":true, "base_instructions":"local template"}
        ]});
        let original_text = original.to_string();
        fs::write(home.join("mine.json"), &original_text).unwrap();
        let profile = test_profile("codex", ProviderApplyMode::Config);
        let (config, plans) = prepare(
            &home.join("config.toml"),
            "model_catalog_json = 'mine.json'\n",
            &profile,
        )
        .unwrap();
        assert!(!plans[0].path.exists(), "planning must be read-only");
        let catalog: Value = serde_json::from_str(&plans[0].content).unwrap();
        assert_eq!(catalog["custom_metadata"], true);
        assert_eq!(catalog["models"][0], original["models"][0]);
        let sol = catalog["models"]
            .as_array()
            .unwrap()
            .iter()
            .find(|m| m["slug"] == "gpt-6-sol")
            .unwrap();
        assert_eq!(sol["base_instructions"], "local template");
        for plan in &plans {
            apply_native_config_write_plan(plan).unwrap();
            assert!(verify(plan).unwrap());
        }
        let (again, next) = prepare(&home.join("config.toml"), &config, &profile).unwrap();
        assert_eq!(config, again);
        assert_eq!(plans[0].path, next[0].path);
        assert_eq!(plans[0].content, next[0].content);
        assert_eq!(
            fs::read_to_string(home.join("mine.json")).unwrap(),
            original_text
        );
        fs::write(&plans[0].path, "{\"models\":[]}").unwrap();
        assert!(!verify(&plans[0]).unwrap());
    }

    #[test]
    fn repairs_missing_catalog_and_selected_inline_or_file_profile() {
        for file_profile in [false, true] {
            let paths = test_paths();
            let home = paths.home_dir.join(".codex");
            fs::create_dir_all(&home).unwrap();
            if file_profile {
                fs::write(
                    home.join("work.config.toml"),
                    "model_catalog_json = 'missing.json'\nweb_search = 'cached'\n",
                )
                .unwrap();
            }
            let profile = test_profile("codex", ProviderApplyMode::Config);
            let (config, plans) = prepare(
                &home.join("config.toml"),
                "profile = 'work'\n[profiles.work]\nmodel_catalog_json = 'missing.json'\n",
                &profile,
            )
            .unwrap();
            let doc = config.parse::<toml_edit::DocumentMut>().unwrap();
            assert_eq!(catalog_setting(&doc), doc["model_catalog_json"].as_str());
            assert_eq!(plans.len(), if file_profile { 2 } else { 1 });
            for plan in &plans {
                apply_native_config_write_plan(plan).unwrap();
                assert!(verify(plan).unwrap());
            }
            if file_profile {
                assert!(fs::read_to_string(home.join("work.config.toml"))
                    .unwrap()
                    .contains("web_search = 'cached'"));
            }
        }
    }

    #[test]
    fn malformed_user_catalog_stops_before_any_write() {
        let paths = test_paths();
        let home = paths.home_dir.join(".codex");
        fs::create_dir_all(&home).unwrap();
        fs::write(home.join("bad.json"), "not JSON").unwrap();
        let profile = test_profile("codex", ProviderApplyMode::Config);
        assert!(prepare(
            &home.join("config.toml"),
            "model_catalog_json = 'bad.json'",
            &profile
        )
        .is_err());
        assert!(!home.join("xiass-tools").exists());
    }

    #[test]
    fn relative_codex_home_is_written_as_an_absolute_pointer() {
        let profile = test_profile("codex", ProviderApplyMode::Config);
        let (config, plans) =
            prepare(Path::new("relative-codex-home/config.toml"), "", &profile).unwrap();
        let doc = config.parse::<toml_edit::DocumentMut>().unwrap();
        assert!(Path::new(doc["model_catalog_json"].as_str().unwrap()).is_absolute());
        assert!(plans[0].path.is_absolute());
        assert!(!plans[0].path.exists());
    }

    #[test]
    fn config_readback_rejects_a_stale_or_overridden_catalog_pointer() {
        let paths = test_paths();
        let home = paths.home_dir.join(".codex");
        let config_path = home.join("config.toml");
        let profile = test_profile("codex", ProviderApplyMode::Config);
        let (content, plans) = prepare(&config_path, "", &profile).unwrap();
        for plan in &plans {
            apply_native_config_write_plan(plan).unwrap();
        }
        let plan = NativeConfigWritePlan::write(
            config_path.clone(),
            content.clone(),
            NativeConfigWriteKind::ProfileConfig,
        );
        apply_native_config_write_plan(&plan).unwrap();
        assert!(verify_config_pointer(&plan).unwrap());
        fs::write(
            &config_path,
            content.replace("model_catalog_json", "old_model_catalog_json"),
        )
        .unwrap();
        assert!(!verify_config_pointer(&plan).unwrap());
        fs::write(
            &config_path,
            format!(
                "profile = 'work'\n{content}\n[profiles.work]\nmodel_catalog_json = 'old.json'\n"
            ),
        )
        .unwrap();
        assert!(!verify_config_pointer(&plan).unwrap());
    }

    #[test]
    fn cache_metadata_is_preserved_and_duplicate_slugs_are_not_added() {
        let paths = test_paths();
        let home = paths.home_dir.join("custom home 空格");
        fs::create_dir_all(&home).unwrap();
        fs::write(home.join("models_cache.json"), json!({"models": [
            {"slug": "gpt-6-sol", "supported_reasoning_levels":[{"effort":"ultra"}], "custom":true},
            {"slug": "gpt-6-sol", "custom":false},
            {"slug": "my-model", "custom":true}
        ]}).to_string()).unwrap();
        let mut profile = test_profile("codex", ProviderApplyMode::Config);
        profile.model = "gpt-6-sol".into();
        let (config, plans) = prepare(&home.join("config.toml"), "", &profile).unwrap();
        let catalog: Value = serde_json::from_str(&plans[0].content).unwrap();
        let matches = catalog["models"]
            .as_array()
            .unwrap()
            .iter()
            .filter(|m| m["slug"] == "gpt-6-sol")
            .collect::<Vec<_>>();
        assert_eq!(matches.len(), 1);
        assert_eq!(matches[0]["custom"], true);
        let doc = config.parse::<toml_edit::DocumentMut>().unwrap();
        assert_eq!(
            Path::new(doc["model_catalog_json"].as_str().unwrap()),
            plans[0].path
        );
    }
}
