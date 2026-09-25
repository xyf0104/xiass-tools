import type { ProfileDraft, ProfileModelOption } from "../../types";
import { isXiassApiProfile, XIASS_PROFILE_LOGO } from "./xiass.js";

export function providerIsOfficial(providerId: string): boolean {
  return providerId.trim().toLowerCase() === "official";
}

export function profileUsesToolIcon(profile: ProfileDraft): boolean {
  return profile.isBuiltin && providerIsOfficial(profile.provider);
}

export function profileDisplayName(profile: ProfileDraft, officialName?: string): string {
  if (profileUsesToolIcon(profile) && officialName) {
    return officialName;
  }
  return profile.name;
}

export function profileEndpoint(profile: ProfileDraft): string | null {
  if (providerIsOfficial(profile.provider) && !profile.baseUrl.trim()) {
    return null;
  }
  return profile.baseUrl.trim() || null;
}

export function profileRemark(profile: ProfileDraft): string {
  return profile.remark?.trim() ?? "";
}

export function profileIconValue(profile: ProfileDraft, displayName: string): string {
  const icon = profile.icon?.trim();
  return icon || (isXiassApiProfile(profile) ? XIASS_PROFILE_LOGO : displayName.trim().charAt(0).toUpperCase() || "?");
}

export function profileIconIsImage(value: string): boolean {
  return value === XIASS_PROFILE_LOGO || value.startsWith("data:image/");
}

export function normalizedProfileIcon(value: string): string | null {
  const trimmed = value.trim();
  return trimmed || null;
}

export function profileIconTextTooLong(value: string): boolean {
  const trimmed = value.trim();
  return trimmed.length > 0 && !profileIconIsImage(trimmed) && [...trimmed].length > 4;
}

export function profileModelOptionLabel(option: ProfileModelOption): string {
  // Do not repeat an ID merely because its display name uses capitals/spaces.
  const comparable = (value: string) => value.toLowerCase().replace(/[\s_-]+/g, "");
  const label = option.name && comparable(option.name) !== comparable(option.id)
    ? `${option.id} - ${option.name}` : option.id;
  return option.supports1m ? `${label} (1M)` : label;
}

/** A successful /models response is authoritative, not a request to add presets. */
export function fetchedModelOptions(options: ProfileModelOption[]): ProfileModelOption[] {
  const unique = new Map<string, ProfileModelOption>();
  for (const option of options) {
    const id = option.id.trim();
    if (id && !unique.has(id)) unique.set(id, { ...option, id, name: option.name?.trim() || id });
  }
  return [...unique.values()];
}
