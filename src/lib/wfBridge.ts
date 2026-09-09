import { invoke, isTauri } from "@tauri-apps/api/core";

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

export async function callWfMethod<T = unknown>(method: string, args: unknown[] = []): Promise<T> {
  if (!isTauri()) throw new Error("浏览器预览不会访问本机验证器；请在 XIASS Tools 应用中使用查询与保存功能。");
  return invoke<T>("wf_bridge_call", { method, args });
}
