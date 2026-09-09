import { invoke, isTauri } from "@tauri-apps/api/core";
import { APP_LATEST_RELEASE_API_URL, APP_LATEST_RELEASE_URL } from "./appInfo";

export interface GitHubInstaller {
  filename: string;
  url: string;
  size: number;
  sha256: string;
}

export interface GitHubApplicationRelease {
  version: string;
  name: string | null;
  url: string;
  publishedAt: string | null;
  installer: GitHubInstaller | null;
}

export async function loadGitHubApplicationRelease(): Promise<GitHubApplicationRelease> {
  if (isTauri()) return invoke("check_github_application_update");

  // Preview may inspect public release metadata, but never guesses the native
  // architecture or claims it can stage/open desktop installers.
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), 10000);
  try {
    const response = await fetch(APP_LATEST_RELEASE_API_URL, {
      headers: { Accept: "application/vnd.github+json" },
      cache: "no-store",
      credentials: "omit",
      signal: controller.signal
    });
    if (!response.ok) throw new Error(`GitHub HTTP ${response.status}`);
    const release = await response.json();
    if (release.draft !== false || release.prerelease !== false ||
        typeof release.tag_name !== "string" || !/^v\d+\.\d+\.\d+$/.test(release.tag_name)) {
      throw new Error("GitHub did not return a valid stable XIASS Tools release.");
    }
    return {
      version: release.tag_name.slice(1),
      name: typeof release.name === "string" ? release.name : null,
      url: APP_LATEST_RELEASE_URL,
      publishedAt: typeof release.published_at === "string" ? release.published_at : null,
      installer: null
    };
  } finally {
    clearTimeout(timeout);
  }
}

export async function downloadGitHubApplicationUpdate(version: string): Promise<string> {
  if (!isTauri()) throw new Error("Please download updates inside the XIASS Tools desktop app.");
  return invoke("download_github_application_update", { version });
}

export async function openGitHubApplicationUpdate(): Promise<void> {
  if (!isTauri()) throw new Error("Please open the installer inside the XIASS Tools desktop app.");
  await invoke("open_github_application_update");
}
