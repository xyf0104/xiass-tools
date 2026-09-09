import type { ProfileDraft, ProfileSummary } from "../types";
import { canonicalProfileToolId } from "./profiles/catalog.js";

/** Use the same saved drafts as the launchpad; never expose gateway or other tools' profiles. */
export function desktopCodexProfiles(summary: ProfileSummary | null): ProfileDraft[] {
  return (summary?.drafts ?? [])
    .filter((profile) => canonicalProfileToolId(profile.app) === "codex" && profile.mode === "config")
    .sort((a, b) => Number(b.isBuiltin) - Number(a.isBuiltin) || a.sortOrder - b.sortOrder);
}

export function activeDesktopCodexProfileId(summary: ProfileSummary | null): string | null {
  const profiles = desktopCodexProfiles(summary);
  const pointers = summary?.activeProfilesByMode?.config ?? {};
  const ids = [pointers.codex, ...Object.entries(pointers)
    .filter(([tool]) => tool !== "codex" && canonicalProfileToolId(tool) === "codex")
    .map(([, id]) => id)];
  return ids.find((id) => profiles.some((profile) => profile.id === id)) ?? null;
}

export function resolveDesktopCodexSelection(summary: ProfileSummary | null, selectedId: string | null): string | null {
  const profiles = desktopCodexProfiles(summary);
  if (profiles.some((profile) => profile.id === selectedId)) return selectedId;
  return activeDesktopCodexProfileId(summary) ?? profiles[0]?.id ?? null;
}
