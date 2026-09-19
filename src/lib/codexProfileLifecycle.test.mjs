import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const read = (path) => readFileSync(new URL(`../../${path}`, import.meta.url), "utf8");

test("Codex profile apply rebuilds and backs up only after the desktop stop boundary", () => {
  const manager = read("src-tauri/src/core/profile/manager.rs");
  const body = manager.slice(
    manager.indexOf("pub fn apply_profile"),
    manager.indexOf("activity_log::append", manager.indexOf("pub fn apply_profile"))
  );

  const preflight = body.indexOf("let preflight_native_plans = build_native_apply_plan");
  const stop = body.indexOf("prepare_restart_before_profile_write");
  const checkpoint = body.indexOf("checkpoint_active_codex_account(&paths)?", stop);
  const finalBuild = body.indexOf("let all_native_plans = build_native_apply_plan", stop);
  const backup = body.indexOf('backup::backup_files("apply-profile"', finalBuild);
  const write = body.indexOf("apply_native_config_write_plan(plan)", backup);

  assert.ok(preflight >= 0 && preflight < stop, "preflight must happen before stopping Codex");
  assert.ok(stop < checkpoint, "OAuth checkpoint must be refreshed after Codex exits");
  assert.ok(checkpoint < finalBuild, "final plans must use the post-exit OAuth/config state");
  assert.ok(finalBuild < backup, "backup must capture the post-exit files");
  assert.ok(backup < write, "no native write may happen before the final backup");
  assert.match(body, /codex_desktop_only:[\s\S]*codex_write_guard && !request\.restart_after_apply/);
});

test("active Codex edits and gateway lifecycle writes use the same desktop guard", () => {
  const profile = read("src-tauri/src/core/profile.rs");
  const rewrite = profile.slice(
    profile.indexOf("fn rewrite_native_configs_for_profile"),
    profile.indexOf("fn read_app_config", profile.indexOf("fn rewrite_native_configs_for_profile"))
  );
  const lifecycle = profile.slice(
    profile.indexOf("fn apply_active_native_configs"),
    profile.indexOf("fn lifecycle_target_apps", profile.indexOf("fn apply_active_native_configs"))
  );

  const assertGuardOrder = (body, finalBuildNeedle) => {
    const stop = body.indexOf("prepare_restart_before_profile_write");
    const checkpoint = body.indexOf("checkpoint_active_codex_account", stop);
    const finalBuild = body.indexOf(finalBuildNeedle, checkpoint);
    const backup = body.indexOf("backup::backup_files", finalBuild);
    assert.ok(stop >= 0, "Codex desktop stop guard must be present");
    assert.ok(stop < checkpoint, "post-exit OAuth state must be checkpointed");
    assert.ok(checkpoint < finalBuild, "plans must be rebuilt after the stop");
    assert.ok(finalBuild < backup, "post-exit files must be backed up before writing");
    assert.match(body, /codex_desktop_only:\s*true/);
  };
  assertGuardOrder(rewrite, "build_native_apply_plan");
  assertGuardOrder(lifecycle, "let lifecycle_plans = build_active_native_lifecycle_plans");
});

test("Codex launch settings flush before profile apply and running-app activation is read-only", () => {
  const route = read("src/routes/ChatGPTDesktop.svelte");
  const store = read("src/lib/chatgptDesktopStore.ts");
  const core = read("src-tauri/src/core/chatgpt_desktop.rs");

  const launchRoute = route.slice(
    route.indexOf("async function launchCodex"),
    route.indexOf("function displayProfileName")
  );
  assert.ok(
    launchRoute.indexOf("flushChatGPTDesktopSettingsForLaunch") < launchRoute.indexOf("applyProfile"),
    "settings must flush before applyProfile can close Codex"
  );
  assert.match(store, /export async function flushChatGPTDesktopSettingsForLaunch/);
  assert.match(store, /launchManagedChatGPTDesktop[\s\S]*await flushChatGPTDesktopSettingsForLaunch\(\)[\s\S]*await launchChatGPTDesktop/);

  const launchCore = core.slice(core.indexOf("pub fn launch()"), core.indexOf("fn launch_with_restart_notes"));
  const focus = launchCore.indexOf("launch_installed_codex(&installed, &[])?");
  const focusReturn = launchCore.indexOf("return Ok(())", focus);
  const enhancedLaunch = launchCore.indexOf("launch_detected_chatgpt_desktop", focus);
  assert.ok(focus >= 0 && focus < focusReturn && focusReturn < enhancedLaunch);
  assert.match(launchCore, /Activated ChatGPT Desktop/);
});

test("restart recovery attempts every previously stopped target", () => {
  const restart = read("src-tauri/src/core/profile/restart.rs");
  const body = restart.slice(
    restart.indexOf("fn finish_stopped_targets"),
    restart.indexOf("pub(in crate::core::profile) fn with_restart_restore_error")
  );

  assert.match(body, /let mut errors = Vec::new\(\)/);
  assert.match(body, /for \(target, result\) in stopped_targets/);
  assert.match(body, /Err\(err\) => errors\.push/);
  assert.ok(body.indexOf("if !errors.is_empty()") > body.indexOf("for (target, result)"));
});
