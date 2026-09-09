import { callWfMethod } from "./wfBridge";

export interface TOTPInput {
  uri?: string;
  secret?: string;
  label?: string;
  issuer?: string;
  account?: string;
  algorithm?: string;
  digits?: number;
  period?: number;
}
export interface TOTPEntry {
  id: string;
  label: string;
  issuer?: string;
  account?: string;
  algorithm: string;
  digits: number;
  period: number;
}
export interface TOTPCode { value: string; validUntil: string; validFrom: string }
export interface TOTPResult { ok: boolean; message: string; entries?: TOTPEntry[] }
export interface TOTPCodeResult extends TOTPResult { code: TOTPCode }
export interface TOTPPreviewResult extends TOTPCodeResult { entry: TOTPEntry }

export const twoFactorApi = {
  list: () => callWfMethod<TOTPResult>("GetTOTPEntries"),
  preview: (input: TOTPInput) => callWfMethod<TOTPPreviewResult>("PreviewTOTP", [input]),
  add: (input: TOTPInput) => callWfMethod<TOTPResult>("AddTOTPEntry", [input]),
  code: (id: string) => callWfMethod<TOTPCodeResult>("GenerateTOTPCode", [id]),
  remove: (id: string) => callWfMethod<TOTPResult>("DeleteTOTPEntry", [id]),
  export: (password: string, destination: string) => callWfMethod<TOTPResult>("ExportTOTPEncryptedToPath", [password, destination]),
  import: (password: string, source: string) => callWfMethod<TOTPResult>("ImportTOTPEncryptedFromPath", [password, source])
};

export function codeRemaining(code: TOTPCode | undefined, now: number): number {
  const until = Date.parse(code?.validUntil || "");
  return Number.isFinite(until) ? Math.max(0, Math.ceil((until - now) / 1000)) : 0;
}

export function totpInput(value: string, label: string, algorithm = "SHA1", digits = 6, period = 30): TOTPInput {
  const raw = value.trim();
  if (!raw || raw.length > 8192) throw new Error("请填写有效的 TOTP 链接或 Base32 密钥（最长 8192 字符）。");
  if (/^otpauth:\/\//i.test(raw)) return { uri: raw, ...(label.trim() ? { label: label.trim() } : {}) };
  if (raw.includes("://")) throw new Error("仅支持 TOTP 链接、Google Authenticator 迁移链接或 Base32 密钥。");
  return { secret: raw, label: label.trim() || "未命名验证器", algorithm, digits, period };
}
