import type { ProfileDraft } from "../../types";
import { canonicalProfileToolId } from "./catalog.js";

export const XIASS_PROFILE_LOGO = "/xiass-tools-logo.png";

// Codex's official desktop client does not expose its model directory through
// the normal OpenAI-compatible `/models` endpoint. Keep the XIASS-managed
// built-in list in one place so the setup wizard, profile editor, and the
// desktop enhancement layer all offer the same models on macOS and Windows.
// Users can still type any additional model ID into ModelSelectInput.
export const XIASS_CODEX_MODEL_IDS = [
  "gpt-6-astra",
  "gpt-6-sol",
  "gpt-6-luna",
  "gpt-5.6-sol",
  "gpt-5.6-terra",
  "gpt-5.6-luna"
] as const;

export const XIASS_CODEX_MODEL_OPTIONS = XIASS_CODEX_MODEL_IDS.map((id) => ({
  id,
  name: id,
  supports1m: false
}));

export const XIASS_API_PRESET = {
  name: "XIASS API",
  baseUrl: "https://api.xiass.com",
  protocol: "openai-responses",
  model: "gpt-6-astra",
  reviewModel: "codex-auto-review",
  webSearch: "live",
  modelContextWindow: 372000,
  modelAutoCompactTokenLimit: 334800
} as const;

export const XIASS_API_TEMPLATE_ID = "preset-xiass-api-codex";

export function profileProviderFromName(name: string, official = false): string {
  return official ? "official" : name.trim();
}

export function profileNameErrorKey(name: string, official = false):
  "profiles.nameRequired" | "profiles.nameControlCharacters" | "profiles.nameReserved" | null {
  if (!name.trim()) return "profiles.nameRequired";
  if (/[\u0000-\u001f\u007f-\u009f]/u.test(name)) return "profiles.nameControlCharacters";
  if (!official && name.trim().toLowerCase() === "official") return "profiles.nameReserved";
  return null;
}

// A template is never persisted or applied before the user supplies a key.
export function createXiassApiTemplate(): ProfileDraft {
  return {
    id: XIASS_API_TEMPLATE_ID,
    ...XIASS_API_PRESET,
    provider: XIASS_API_PRESET.name,
    app: "codex", mode: "config", isBuiltin: false,
    icon: null, remark: null, imageModel: null, modelMappings: [],
    authRef: null, createdAt: null, updatedAt: null,
    lastTestStatus: "preset", usageEnabled: false, sortOrder: 1
  };
}

export function isXiassApiProfile(profile: ProfileDraft): boolean {
  try {
    const endpoint = new URL(profile.baseUrl);
    return !profile.isBuiltin && canonicalProfileToolId(profile.app) === "codex"
      && profile.mode === "config" && profile.name.trim() === XIASS_API_PRESET.name
      && endpoint.origin === XIASS_API_PRESET.baseUrl
      && ["", "/", "/v1", "/v1/"].includes(endpoint.pathname);
  } catch {
    return false;
  }
}

export function canReuseProfileKeyForModels(profile: ProfileDraft, protocol: string, baseUrl: string): boolean {
  return Boolean(profile.authRef) && profile.protocol.trim() === protocol.trim()
    && profile.baseUrl.trim().replace(/\/+$/, "") === baseUrl.trim().replace(/\/+$/, "");
}
