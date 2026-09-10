import { runtimeAdapter } from "./api/runtime";
import type { CodexAccountSession, CodexAccountSelection } from "../types";
export type { CodexAccountInfo, CodexAccountSession, CodexAccountSelection } from "../types";

const accountErrorKeys = ["invalidJson", "invalidTokens", "apiKeyOnly", "missingTokens", "expired", "tooLarge", "accountCount",
  "localMissing", "cliMissing", "startFailed", "loginBusy", "loginFailed", "loginTimeout", "sessionExpired", "chooseAccount",
  "desktopOnly", "unavailable", "tooManySessions", "officialOnly", "useAccountPanel"] as const;
export function codexAccountErrorKey(error: unknown) {
  const message = error instanceof Error ? error.message : String(error);
  const key = accountErrorKeys.find((key) => message === `codexAccount.${key}`);
  return `codexAccount.${key ?? "unavailable"}` as const;
}

function native<T>(command: string, args?: Record<string, unknown>): Promise<T> {
  const runtime = runtimeAdapter();
  // Browser previews must never persist credentials in mock localStorage or
  // claim to have completed a real login.
  if (runtime.kind !== "tauri") return Promise.reject(new Error("codexAccount.desktopOnly"));
  return runtime.invoke<T>(command, args);
}

export const importCodexAccountJson = (content: string) => native<CodexAccountSession>("import_codex_account_json", { content });
export const importLocalCodexAccount = () => native<CodexAccountSession>("import_local_codex_account");
export const startCodexAccountLogin = () => native<CodexAccountSession>("start_codex_account_login");
export const pollCodexAccountSession = (id: string) => native<CodexAccountSession>("poll_codex_account_session", { id });
export const discardCodexAccountSession = (id: string) => native<void>("discard_codex_account_session", { id });

export function selectedCodexAccount(session: CodexAccountSession | null, index: number): CodexAccountSelection | null {
  return session?.status === "ready" && Number.isInteger(index) && index >= 0 && session.accounts[index]
    ? { sessionId: session.id, accountIndex: index } : null;
}
