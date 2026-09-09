use std::path::{Path, PathBuf};
use std::process::Command;

const SIDECAR_NAME: &str = "xiass-wf-bridge";

fn go_target_from_rust_target(target: &str) -> Option<(&'static str, &'static str)> {
    let goos = if target.contains("windows") {
        "windows"
    } else if target.contains("apple-darwin") {
        "darwin"
    } else if target.contains("linux") {
        "linux"
    } else {
        return None;
    };
    let goarch = if target.starts_with("x86_64") {
        "amd64"
    } else if target.starts_with("aarch64") {
        "arm64"
    } else if target.starts_with("i686") {
        "386"
    } else if target.starts_with("armv7") {
        "arm"
    } else {
        return None;
    };
    Some((goos, goarch))
}

fn emit_rerun_inputs(path: &Path) {
    let Ok(metadata) = std::fs::metadata(path) else {
        return;
    };
    if metadata.is_dir() {
        let Ok(entries) = std::fs::read_dir(path) else {
            return;
        };
        for entry in entries.flatten() {
            let candidate = entry.path();
            if candidate.file_name().and_then(|name| name.to_str()) == Some("bin") {
                continue;
            }
            emit_rerun_inputs(&candidate);
        }
        return;
    }
    let tracked = matches!(
        path.file_name().and_then(|name| name.to_str()),
        Some("go.mod") | Some("go.sum") | Some("wf-embedded-overrides.css")
    ) || path.extension().and_then(|extension| extension.to_str()) == Some("go")
        || path
            .components()
            .any(|component| component.as_os_str() == "dist");
    if tracked {
        println!("cargo:rerun-if-changed={}", path.display());
    }
}

fn build_go_sidecar(
    source_dir: &Path,
    output_dir: &Path,
    output_target: &str,
    goos: &str,
    goarch: &str,
) -> PathBuf {
    let extension = if goos == "windows" { ".exe" } else { "" };
    let output = output_dir.join(format!("{SIDECAR_NAME}-{output_target}{extension}"));
    if std::env::var("XIASS_SKIP_WF_BRIDGE_BUILD").ok().as_deref() == Some("1") && output.is_file()
    {
        return output;
    }
    let status = Command::new("go")
        .current_dir(source_dir)
        .env("GOOS", goos)
        .env("GOARCH", goarch)
        .env("CGO_ENABLED", "0")
        .args([
            "build",
            "-tags",
            "wfbridge",
            "-trimpath",
            "-ldflags",
            "-s -w",
            "-o",
        ])
        .arg(&output)
        .arg(".")
        .status()
        .expect("failed to start Go build for the XIASS WF bridge");
    if !status.success() {
        panic!("XIASS WF bridge build failed with status {status}");
    }
    output
}

#[cfg(target_os = "macos")]
fn build_universal_macos_sidecar(source_dir: &Path, output_dir: &Path) {
    let universal = output_dir.join(format!("{SIDECAR_NAME}-universal-apple-darwin"));
    if std::env::var("XIASS_SKIP_WF_BRIDGE_BUILD").ok().as_deref() == Some("1")
        && universal.is_file()
    {
        return;
    }
    let x64 = build_go_sidecar(
        source_dir,
        output_dir,
        "x86_64-apple-darwin",
        "darwin",
        "amd64",
    );
    let arm64 = build_go_sidecar(
        source_dir,
        output_dir,
        "aarch64-apple-darwin",
        "darwin",
        "arm64",
    );
    let status = Command::new("lipo")
        .args(["-create"])
        .arg(x64)
        .arg(arm64)
        .arg("-output")
        .arg(&universal)
        .status()
        .expect("failed to start lipo for the XIASS WF bridge");
    if !status.success() {
        panic!("XIASS WF universal sidecar build failed with status {status}");
    }
}

fn build_wf_bridge() {
    let manifest_dir =
        PathBuf::from(std::env::var("CARGO_MANIFEST_DIR").expect("CARGO_MANIFEST_DIR is required"));
    let target = std::env::var("TARGET").expect("TARGET is required");
    println!("cargo:rustc-env=XIASS_RUST_TARGET={target}");
    println!("cargo:rerun-if-env-changed=XIASS_SKIP_WF_BRIDGE_BUILD");

    let source_dir = manifest_dir.join("../sidecars/wf-bridge");
    let output_dir = source_dir.join("bin");
    emit_rerun_inputs(&source_dir);
    std::fs::create_dir_all(&output_dir).expect("failed to create WF bridge output directory");
    let (goos, goarch) = go_target_from_rust_target(&target)
        .unwrap_or_else(|| panic!("unsupported XIASS WF bridge target: {target}"));
    build_go_sidecar(&source_dir, &output_dir, &target, goos, goarch);

    #[cfg(target_os = "macos")]
    if target.contains("apple-darwin") {
        build_universal_macos_sidecar(&source_dir, &output_dir);
    }
}

fn main() {
    println!("cargo:rerun-if-changed=build.rs");
    build_wf_bridge();
    tauri_build::build();
}
