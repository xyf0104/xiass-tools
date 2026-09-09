import { beforeEach, describe, expect, it, vi } from "vitest";
import { get } from "svelte/store";
import type { GitHubApplicationRelease } from "./githubAppUpdate";

const backend = vi.hoisted(() => ({
  load: vi.fn(), download: vi.fn(), open: vi.fn(), listen: vi.fn(), unlisten: vi.fn(), signedCheck: vi.fn()
}));
vi.mock("@tauri-apps/api/core", () => ({ isTauri: () => true }));
vi.mock("@tauri-apps/api/event", () => ({ listen: backend.listen }));
vi.mock("@tauri-apps/plugin-updater", () => ({ check: backend.signedCheck }));
vi.mock("./api", () => ({ applicationUpdateTarget: vi.fn(), installApplicationUpdate: vi.fn() }));
vi.mock("./githubAppUpdate", () => ({
  loadGitHubApplicationRelease: backend.load, downloadGitHubApplicationUpdate: backend.download,
  openGitHubApplicationUpdate: backend.open
}));

let updates: typeof import("./appUpdateStore");
const release: GitHubApplicationRelease = {
  version: "1.8.9", name: "XIASS Tools v1.8.9", publishedAt: null,
  url: "https://github.com/xyf0104/Antigravity-WF-Assistant/releases/latest",
  installer: { filename: "XIASS.Tools_1.8.9_aarch64.dmg", url: "https://github.com/fixture", size: 100, sha256: "a".repeat(64) }
};

beforeEach(async () => {
  vi.resetModules(); vi.clearAllMocks();
  backend.load.mockResolvedValue(release);
  backend.download.mockResolvedValue("/cache/XIASS.Tools_1.8.9_aarch64.dmg");
  backend.open.mockResolvedValue(undefined);
  backend.listen.mockResolvedValue(backend.unlisten);
  updates = await import("./appUpdateStore");
});

describe("GitHub release check and online download state", () => {
  it("offers a newer GitHub release as downloadable, never as a signed silent install", async () => {
    const result = await updates.checkForAppUpdate();
    expect(result).toMatchObject({ status: "available", latestVersion: "1.8.9", downloadable: true, installable: false });
    expect(backend.signedCheck).not.toHaveBeenCalled();
  });

  it.each(["1.8.7", "1.8.6", "1.7.99"])("does not downgrade to %s", async (version) => {
    backend.load.mockResolvedValue({ ...release, version });
    expect(await updates.checkForAppUpdate()).toMatchObject({ status: "upToDate", updateAvailable: false, downloadable: false });
  });

  it("handles numeric version order rather than comparing strings", async () => {
    backend.load.mockResolvedValue({ ...release, version: "1.10.0" });
    expect((await updates.checkForAppUpdate()).updateAvailable).toBe(true);
  });

  it("does not invent an installer for a missing platform", async () => {
    backend.load.mockResolvedValue({ ...release, installer: null });
    expect(await updates.checkForAppUpdate()).toMatchObject({ status: "available", downloadable: false });
    await updates.downloadAppUpdate(); expect(backend.download).not.toHaveBeenCalled();
  });

  it("deduplicates checks even when the second request is forced", async () => {
    let complete!: (value: GitHubApplicationRelease) => void;
    backend.load.mockReturnValue(new Promise<GitHubApplicationRelease>((resolve) => { complete = resolve; }));
    const first = updates.checkForAppUpdate(); const second = updates.checkForAppUpdate(true);
    expect(backend.load).toHaveBeenCalledOnce();
    complete(release); await Promise.all([first, second]);
  });

  it("caches automatic checks but allows an explicit recheck", async () => {
    await updates.checkForAppUpdate(); await updates.checkForAppUpdate();
    expect(backend.load).toHaveBeenCalledOnce();
    await updates.checkForAppUpdate(true); expect(backend.load).toHaveBeenCalledTimes(2);
  });

  it("shows network errors without claiming an update check succeeded, and can retry", async () => {
    backend.load.mockRejectedValueOnce(new Error("GitHub HTTP 503"));
    expect(await updates.checkForAppUpdate()).toMatchObject({ status: "error", error: "GitHub HTTP 503", updateAvailable: false });
    expect((await updates.checkForAppUpdate()).status).toBe("available");
  });

  it("subscribes before downloading, handles progress and verifies before offering Open", async () => {
    await updates.checkForAppUpdate();
    backend.download.mockImplementationOnce(async () => {
      const callback = backend.listen.mock.calls[0][1];
      callback({ payload: { phase: "downloading", downloadedBytes: 50, totalBytes: 100 } });
      expect(get(updates.appUpdateState)).toMatchObject({ status: "downloading", downloadedBytes: 50, downloadedPath: null });
      callback({ payload: { phase: "verifying", downloadedBytes: 100, totalBytes: 100 } });
      expect(get(updates.appUpdateState).status).toBe("verifying");
      return "/cache/installer.dmg";
    });
    expect(await updates.downloadAppUpdate()).toMatchObject({ status: "downloaded", downloadedPath: "/cache/installer.dmg", downloadedBytes: 100 });
    expect(backend.listen).toHaveBeenCalledWith("github-app-update-progress", expect.any(Function));
    expect(backend.download).toHaveBeenCalledWith("1.8.9");
    expect(backend.unlisten).toHaveBeenCalledOnce();
    expect(backend.open).not.toHaveBeenCalled();
    await updates.openDownloadedAppUpdate(); expect(backend.open).toHaveBeenCalledOnce();
  });

  it("keeps download failures retryable and never opens an unverified file", async () => {
    await updates.checkForAppUpdate();
    backend.download.mockRejectedValueOnce(new Error("Checksum mismatch"));
    expect(await updates.downloadAppUpdate()).toMatchObject({ status: "error", downloadedPath: null, downloadable: true });
    await updates.openDownloadedAppUpdate(); expect(backend.open).not.toHaveBeenCalled();
    expect(backend.unlisten).toHaveBeenCalledOnce();
    expect((await updates.downloadAppUpdate()).status).toBe("downloaded");
  });

  it("does not launch concurrent downloads or reset a download with Check", async () => {
    await updates.checkForAppUpdate();
    let complete!: (value: string) => void;
    backend.download.mockReturnValue(new Promise<string>((resolve) => { complete = resolve; }));
    const first = updates.downloadAppUpdate(); const second = updates.downloadAppUpdate();
    const check = updates.checkForAppUpdate(true);
    await vi.waitFor(() => expect(backend.download).toHaveBeenCalledOnce());
    expect(backend.load).toHaveBeenCalledOnce();
    complete("/cache/installer.dmg"); await Promise.all([first, second, check]);
  });

  it("retains the verified installer on recheck and allows redownload after an open error", async () => {
    await updates.checkForAppUpdate(); await updates.downloadAppUpdate();
    expect((await updates.checkForAppUpdate(true)).status).toBe("downloaded");
    backend.open.mockRejectedValueOnce(new Error("Cannot open installer"));
    expect(await updates.openDownloadedAppUpdate()).toMatchObject({ status: "error", downloadedPath: null, downloadable: true });
  });
});
