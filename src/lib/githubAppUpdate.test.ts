import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { APP_LATEST_RELEASE_API_URL, APP_UPDATER_ENABLED } from "./appInfo";
import { downloadGitHubApplicationUpdate, loadGitHubApplicationRelease, openGitHubApplicationUpdate } from "./githubAppUpdate";

const native = vi.hoisted(() => ({ isTauri: vi.fn(() => true), invoke: vi.fn() }));
vi.mock("@tauri-apps/api/core", () => native);

beforeEach(() => { vi.clearAllMocks(); native.isTauri.mockReturnValue(true); });
afterEach(() => { vi.unstubAllGlobals(); vi.useRealTimers(); });

describe("XIASS GitHub update source", () => {
  it("keeps the inherited signed update channel disabled for ordinary builds", () => {
    expect(APP_UPDATER_ENABLED).toBe(false);
    expect(APP_LATEST_RELEASE_API_URL).toBe("https://api.github.com/repos/xyf0104/xiass-tools/releases/latest");
  });

  it("uses the native authoritative release lookup", async () => {
    native.invoke.mockResolvedValueOnce({ version: "1.8.8" });
    expect(await loadGitHubApplicationRelease()).toEqual({ version: "1.8.8" });
    expect(native.invoke).toHaveBeenCalledWith("check_github_application_update");
  });

  it("passes only a version when downloading, never a caller-selected URL or path", async () => {
    native.invoke.mockResolvedValueOnce("/cache/installer.dmg");
    expect(await downloadGitHubApplicationUpdate("1.8.8")).toBe("/cache/installer.dmg");
    expect(native.invoke).toHaveBeenCalledWith("download_github_application_update", { version: "1.8.8" });
  });

  it("opens only the installer retained by the native verified-download state", async () => {
    await openGitHubApplicationUpdate();
    expect(native.invoke).toHaveBeenCalledWith("open_github_application_update");
  });

  it("reads public metadata in preview without credentials or native architecture guesses", async () => {
    native.isTauri.mockReturnValue(false);
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      tag_name: "v1.8.8", name: "XIASS Tools", draft: false, prerelease: false,
      html_url: "https://example.com/untrusted", assets: [{ name: "anything.exe" }]
    })));
    vi.stubGlobal("fetch", fetchMock);
    const release = await loadGitHubApplicationRelease();
    expect(release.version).toBe("1.8.8");
    expect(release.installer).toBeNull();
    expect(release.url).toBe("https://github.com/xyf0104/xiass-tools/releases/latest");
    expect(fetchMock).toHaveBeenCalledWith(APP_LATEST_RELEASE_API_URL,
      expect.objectContaining({ credentials: "omit", cache: "no-store", signal: expect.any(AbortSignal) }));
  });

  it.each([403, 404, 429, 503])("reports HTTP %i without claiming the version is current", async (status) => {
    native.isTauri.mockReturnValue(false);
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("", { status })));
    await expect(loadGitHubApplicationRelease()).rejects.toThrow(`GitHub HTTP ${status}`);
  });

  it.each([{ draft: true }, { prerelease: true }, { tag_name: "v1.8.8-beta" }, { tag_name: "invalid" }])("rejects invalid stable releases: %j", async (patch) => {
    native.isTauri.mockReturnValue(false);
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      tag_name: "v1.8.8", draft: false, prerelease: false, ...patch
    }))));
    await expect(loadGitHubApplicationRelease()).rejects.toThrow("valid stable");
  });

  it("stops an unresponsive preview lookup after 10 seconds", async () => {
    vi.useFakeTimers(); native.isTauri.mockReturnValue(false);
    vi.stubGlobal("fetch", vi.fn((_url, { signal }: RequestInit) => new Promise((_resolve, reject) => {
      signal!.addEventListener("abort", () => reject(new Error("Aborted")));
    })));
    const result = expect(loadGitHubApplicationRelease()).rejects.toThrow("Aborted");
    await vi.advanceTimersByTimeAsync(10000); await result;
  });

  it("does not pretend browser preview can download or open native installers", async () => {
    native.isTauri.mockReturnValue(false);
    await expect(downloadGitHubApplicationUpdate("1.8.8")).rejects.toThrow("desktop app");
    await expect(openGitHubApplicationUpdate()).rejects.toThrow("desktop app");
    expect(native.invoke).not.toHaveBeenCalled();
  });
});
