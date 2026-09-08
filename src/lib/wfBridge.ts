import { invoke } from "@tauri-apps/api/core";

export interface WfBridgeSession {
  url: string;
  token: string;
  host: string;
  port: number;
  schemaVersion: number;
}

export interface WfBridgeStatus {
  running: boolean;
  url?: string | null;
  lastError?: string | null;
}

export interface WfBridgeHostActionFilter {
  name: string;
  pattern: string;
}

export interface WfBridgeHostActionRequest {
  requestId: string;
  kind:
    | "open_url"
    | "open_file"
    | "open_directory"
    | "save_file"
    | "claude_code_account_candidates"
    | "claude_code_apply_account";
  title?: string;
  defaultDirectory?: string;
  defaultFilename?: string;
  filters?: WfBridgeHostActionFilter[];
  url?: string;
  accountId?: string;
  model?: string;
}

export async function getWfBridgeSession(): Promise<WfBridgeSession> {
  return invoke<WfBridgeSession>("wf_bridge_get_session");
}

export async function getWfBridgeStatus(): Promise<WfBridgeStatus> {
  return invoke<WfBridgeStatus>("wf_bridge_get_status");
}

export async function handleWfBridgeHostAction(
  port: number,
  request: WfBridgeHostActionRequest
): Promise<void> {
  await invoke("wf_bridge_handle_host_action", { port, request });
}

export async function stopWfBridge(): Promise<void> {
  await invoke("wf_bridge_stop");
}

export async function exportWfHelperTransfer(): Promise<unknown> {
  return invoke<unknown>("wf_bridge_export_helper_transfer");
}

export async function restoreWfHelperTransfer(bundle: unknown): Promise<{
  ok: boolean;
  accountCount: number;
  modelCount: number;
  rolledBack: boolean;
}> {
  return invoke("wf_bridge_restore_helper_transfer", { bundle });
}

export async function getWfHelperDiagnostics(): Promise<unknown> {
  return invoke<unknown>("wf_bridge_get_helper_diagnostics");
}
