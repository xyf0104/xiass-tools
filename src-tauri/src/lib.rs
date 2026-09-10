pub mod commands;
pub mod core;

pub fn run() {
    if crate::core::macos_app_scope::run_codestudio_self_cleanup_helper_from_args() {
        return;
    }
    tauri::Builder::default()
        // Single-instance guard: must be registered before any other plugin.
        // If a second instance is launched, the callback fires in the first
        // (already-running) instance — we show and focus the main window so
        // the user is brought back to the existing app instead of spawning a
        // duplicate.
        .plugin(tauri_plugin_single_instance::init(|app, _argv, _cwd| {
            crate::core::tray::show_main_window(app);
        }))
        .plugin(tauri_plugin_updater::Builder::new().build())
        .plugin(tauri_plugin_process::init())
        .plugin(tauri_plugin_opener::init())
        .plugin(tauri_plugin_dialog::init())
        .invoke_handler(tauri::generate_handler![
            commands::activity_log::load_activity_log,
            commands::app_updater::application_update_target,
            commands::app_updater::install_application_update,
            commands::github_app_updater::check_github_application_update,
            commands::github_app_updater::download_github_application_update,
            commands::github_app_updater::open_github_application_update,
            commands::backup::list_backups,
            commands::backup::restore_backup,
            commands::claude_desktop::inspect_claude_desktop_page,
            commands::claude_desktop::launch_claude_desktop,
            commands::claude_desktop::open_claude_desktop_path,
            commands::claude_desktop::plan_claude_desktop_update,
            commands::claude_desktop::restart_claude_desktop_after_accessibility_grant,
            commands::claude_desktop::take_pending_claude_desktop_launch_after_restart,
            commands::chatgpt_desktop::inspect_chatgpt_desktop,
            commands::chatgpt_desktop::install_chatgpt_desktop,
            commands::chatgpt_desktop::load_cached_chatgpt_desktop_state,
            commands::chatgpt_desktop::load_cached_chatgpt_desktop_states,
            commands::chatgpt_desktop::launch_chatgpt_desktop,
            commands::chatgpt_desktop::open_chatgpt_desktop_path,
            commands::chatgpt_desktop::plan_chatgpt_desktop_update,
            commands::chatgpt_desktop::stage_chatgpt_desktop_update,
            commands::chatgpt_desktop::uninstall_chatgpt_desktop,
            commands::chatgpt_desktop::update_chatgpt_desktop_settings,
            commands::detect::detect_environment,
            commands::detect::detect_environment_fresh,
            commands::detect::detect_claude_install_kinds,
            commands::detect::detect_claude_capabilities,
            commands::detect::detect_chatgpt_desktop_install_kinds,
            commands::detect::load_cached_detection,
            commands::doctor::run_doctor,
            commands::gateway::load_gateway_status,
            commands::gateway::restart_gateway,
            commands::gateway::start_gateway,
            commands::gateway::stop_gateway,
            commands::gateway::update_gateway_settings,
            commands::gateway_request_log::load_gateway_request_log,
            commands::install_terminal::resize_install_terminal,
            commands::install_terminal::start_install_terminal,
            commands::install_terminal::launch_tool_external,
            commands::install_terminal::stop_install_terminal,
            commands::install_terminal::write_install_terminal,
            commands::macos_app_scope::cleanup_macos_user_application,
            commands::macos_app_scope::load_macos_application_scope_status,
            commands::macos_app_scope::take_codestudio_self_cleanup_failure,
            commands::profiles::apply_profile,
            commands::profiles::import_codex_account_json,
            commands::profiles::import_local_codex_account,
            commands::profiles::start_codex_account_login,
            commands::profiles::poll_codex_account_session,
            commands::profiles::discard_codex_account_session,
            commands::profiles::clear_environment_variables,
            commands::profiles::delete_profile_draft,
            commands::profiles::duplicate_profile_draft,
            commands::profiles::list_profile_models,
            commands::profiles::load_profile_summary,
            commands::profiles::preview_profile_apply,
            commands::profiles::preview_profile_write,
            commands::profiles::reorder_profile_drafts,
            commands::profiles::save_profile_draft,
            commands::profiles::start_codex_oauth_login,
            commands::profiles::switch_active_profile,
            commands::profiles::test_profile_connection,
            commands::profiles::update_profile_draft,
            commands::settings::ensure_app_dirs,
            commands::settings::load_app_settings,
            commands::settings::update_app_settings,
            commands::tool_installer::install_tool,
            commands::tool_installer::plan_tool_install,
            commands::tool_installer::plan_tool_launch,
            commands::tool_installer::plan_tool_update,
            commands::tool_installer::plan_tool_uninstall,
            commands::tool_installer::repair_tool_path,
            commands::tool_installer::uninstall_tool,
            commands::tool_installer::update_tool,
            commands::usage_query::delete_usage_script,
            commands::usage_query::load_usage_script_state,
            commands::usage_query::query_profile_usage,
            commands::usage_query::save_usage_script,
            commands::usage_query::test_usage_script,
            commands::wf_bridge::wf_bridge_get_session,
            commands::wf_bridge::wf_bridge_get_status,
            commands::wf_bridge::wf_bridge_handle_host_action,
            commands::wf_bridge::wf_bridge_export_helper_transfer,
            commands::wf_bridge::wf_bridge_restore_helper_transfer,
            commands::wf_bridge::wf_bridge_get_helper_diagnostics,
            commands::wf_bridge::wf_bridge_call,
            commands::wf_bridge::wf_bridge_stop,
        ])
        .setup(|app| {
            // Windows uses the same integrated, glass title area as macOS.
            // Apply this before the first webview frame so the stock caption
            // bar cannot flash in front of the XIASS controls while the
            // frontend is mounting.
            #[cfg(target_os = "windows")]
            {
                use tauri::Manager;
                if let Some(window) = app.get_webview_window("main") {
                    let _ = window.set_decorations(false);
                }
            }
            // A successful Windows update relaunches before Burn has fully
            // exited, so remove captured updater artifacts with lock retries.
            crate::core::app_updater::schedule_stale_update_cleanup();
            // GUI launches on macOS do not source shell profiles, so restore
            // PATH entries that CodeStudio Lite repaired in earlier sessions.
            let _ = crate::core::env_health::restore_persisted_path_repairs();
            // Register the system tray icon + menu so closing the main window
            // hides it to the tray instead of quitting the app. The tray's
            // "Quit" entry performs the real shutdown (including the gateway).
            crate::core::tray::setup(app.handle());
            Ok(())
        })
        .on_window_event(|window, event| {
            // Intercept the main window close: hide to the tray instead of
            // quitting. The app keeps running with its tray icon; the gateway
            // is only shut down on an explicit Quit from the tray menu.
            if window.label() == "main" {
                if let tauri::WindowEvent::CloseRequested { api, .. } = event {
                    api.prevent_close();
                    // Keep the process' shell/taskbar identity intact while
                    // hiding the window. On Windows, removing the taskbar
                    // entry here makes clicking the pinned app icon start a
                    // second process without a visible restore affordance;
                    // the single-instance callback can only restore reliably
                    // when the original identity remains discoverable.
                    let _ = window.hide();
                }
            }
        })
        .build(tauri::generate_context!())
        .expect("failed to build XIASS Tools")
        .run(|_app, _event| {
            if matches!(_event, tauri::RunEvent::Exit) {
                crate::core::codex_accounts::cancel_all();
            }
            // macOS Dock clicks go through Reopen, not the single-instance callback.
            #[cfg(target_os = "macos")]
            if matches!(_event, tauri::RunEvent::Reopen { .. }) {
                crate::core::tray::show_main_window(_app);
            }
        });
}
