import { cleanup, fireEvent, render, waitFor } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import ChatGPTDesktop from "../routes/ChatGPTDesktop.svelte";
import SetupWizard from "../routes/SetupWizard.svelte";
import { chatgptDesktopView } from "./chatgptDesktopStore";
import { setLocale } from "./i18n";
import type { ProfileDraft, ProfileSummary, DetectionSnapshot } from "../types";

const calls = vi.hoisted(() => ({
  applyProfile: vi.fn(), launch: vi.fn(), create: vi.fn(), applied: vi.fn(),
  preview: vi.fn(), save: vi.fn(), listModels: vi.fn()
}));
vi.mock("./api", () => ({
  applyProfile: calls.applyProfile, openChatGPTDesktopPath: vi.fn(),
  detectEnvironment: vi.fn(), listProfileModels: calls.listModels,
  openExternalUrl: vi.fn(), previewProfileWrite: calls.preview,
  saveProfileDraft: calls.save, startCodexOAuthLogin: vi.fn()
}));
vi.mock("./chatgptDesktopStore", async () => {
  const { writable } = await import("svelte/store");
  return {
    chatgptDesktopView: writable({}),
    launchManagedChatGPTDesktop: calls.launch,
    installOrUpdateChatGPTDesktop: vi.fn(), refreshChatGPTDesktop: vi.fn(),
    removeChatGPTDesktop: vi.fn(), setChatGPTDesktopConfirmUninstall: vi.fn(),
    setChatGPTDesktopSelectedKind: vi.fn(), stageChatGPTDesktopPackage: vi.fn(),
    startChatGPTDesktopProgressListener: vi.fn(), updateChatGPTDesktopDraft: vi.fn()
  };
});

const makeProfile = (id: string, overrides: Partial<ProfileDraft> = {}): ProfileDraft => ({
  id, name: id, provider: id, app: "codex", mode: "config", isBuiltin: false,
  protocol: "openai-responses", model: "test-model", baseUrl: "https://example.test/v1",
  icon: null, remark: null, webSearch: "live", imageModel: null,
  modelContextWindow: 372000, modelAutoCompactTokenLimit: 334800, reviewModel: null,
  modelMappings: [], authRef: "keychain:test-only", createdAt: null, updatedAt: null,
  lastTestStatus: null, usageEnabled: false, sortOrder: 1, ...overrides
});
const official = makeProfile("official", { name: "Codex Official", isBuiltin: true, sortOrder: 0, authRef: null, baseUrl: "", model: "" });
const custom = makeProfile("XIASS API", { baseUrl: "https://api.xiass.com" });
function summary(profiles = [official, custom]): ProfileSummary {
  return { configDir: "/test/config", activeProfile: official.id, activeProfileName: official.name,
    activeProfilesByMode: { config: { codex: official.id }, gateway: {} },
    codexAuth: {} as ProfileSummary["codexAuth"], drafts: profiles };
}
function screen(profiles = summary()) {
  return render(ChatGPTDesktop, { profileSummary: profiles, onCreateCodexProfile: calls.create, onProfileApplied: calls.applied });
}
async function next(ui: ReturnType<typeof screen>) {
  await fireEvent.click(ui.getByRole("button", { name: /XIASS API/ }));
  await fireEvent.click(ui.getByRole("button", { name: "下一步" }));
}
beforeEach(() => {
  vi.clearAllMocks(); setLocale("zh-CN");
  const kind = { state: { installed: { path: "/test/ChatGPT.app", version: "test" }, platform: "macos", generatedAt: "2026-01-01" },
    loading: false, busyAction: null, planRefreshing: false, planStale: false };
  chatgptDesktopView.set({ kindViews: { msix: { ...kind }, portable: { ...kind } }, selectedKind: "msix",
    settingsDraft: { syncHistoryOnLaunch: false, pluginMarketplaceUnlockOnLaunch: true, officialRemotePluginCacheOnLaunch: true,
      pluginAutoExpandOnLaunch: true, modelWhitelistUnlockOnLaunch: true, serviceTierControlsOnLaunch: false },
    error: null, success: null, confirmUninstall: false } as unknown as Parameters<typeof chatgptDesktopView.set>[0]);
  calls.applyProfile.mockResolvedValue({ verified: true, nativeVerified: true, mode: "config", summary: summary() });
  calls.launch.mockResolvedValue(undefined);
  calls.applied.mockResolvedValue(undefined);
});
afterEach(cleanup);

describe("Codex desktop two-step workflow", () => {
  it("only lists Codex config profiles and keeps management out of the initial screen", () => {
    const ui = screen(summary([custom, makeProfile("Claude-only", { app: "claude" }), makeProfile("Gateway-only", { mode: "gateway" }), official]));
    expect(ui.getByRole("button", { name: /Codex 官方/ })).toBeTruthy();
    expect(ui.queryByRole("button", { name: /Claude-only|Gateway-only/ })).toBeNull();
    expect(ui.queryByRole("heading", { name: "启动选项" })).toBeNull();
    expect(calls.applyProfile).not.toHaveBeenCalled();
  });
  it("makes New Configuration a first-class entrypoint", async () => {
    const ui = screen();
    await fireEvent.click(ui.getByRole("button", { name: "新建配置" }));
    expect(calls.create).toHaveBeenCalledOnce();
  });
  it("writes and verifies the selected profile before restarting the desktop", async () => {
    const ui = screen(); await next(ui);
    expect(calls.applyProfile).not.toHaveBeenCalled();
    await fireEvent.click(ui.getByRole("button", { name: "启动" }));
    await waitFor(() => expect(calls.launch).toHaveBeenCalledWith(true));
    expect(calls.applyProfile).toHaveBeenCalledWith({ profileId: custom.id, restartAfterApply: false, reapply: true });
    expect(calls.applyProfile.mock.invocationCallOrder[0]).toBeLessThan(calls.launch.mock.invocationCallOrder[0]);
  });
  it("revalidates an already active profile instead of trusting a stale active pointer", async () => {
    const ui = screen();
    await fireEvent.click(ui.getByRole("button", { name: "下一步" }));
    await fireEvent.click(ui.getByRole("button", { name: "启动" }));
    await waitFor(() => expect(calls.applyProfile).toHaveBeenCalledWith({ profileId: official.id, restartAfterApply: false, reapply: true }));
  });
  it.each(["write", "verification"])("does not launch after %s failure", async (failure) => {
    if (failure === "write") calls.applyProfile.mockRejectedValueOnce(new Error("test write failed"));
    else calls.applyProfile.mockResolvedValueOnce({ verified: true, nativeVerified: false, mode: "config", summary: summary() });
    const ui = screen(); await next(ui);
    await fireEvent.click(ui.getByRole("button", { name: "启动" }));
    await waitFor(() => expect(ui.getByRole("button", { name: "启动" }).hasAttribute("disabled")).toBe(false));
    expect(calls.launch).not.toHaveBeenCalled();
    expect(ui.getByText(failure === "write" ? "test write failed" : /配置写入未通过校验/)).toBeTruthy();
  });
  it("blocks duplicate launches while config writing is pending", async () => {
    let finish!: (value: unknown) => void;
    calls.applyProfile.mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }));
    const ui = screen(); await next(ui);
    await fireEvent.click(ui.getByRole("button", { name: "启动" }));
    const busy = ui.getByRole("button", { name: "正在应用配置…" });
    expect(busy.hasAttribute("disabled")).toBe(true);
    expect(calls.launch).not.toHaveBeenCalled();
    finish({ verified: true, nativeVerified: true, mode: "config", summary: summary() });
    await waitFor(() => expect(calls.launch).toHaveBeenCalledOnce());
  });
  it("keeps the choice when moving back and forth", async () => {
    const ui = screen(); await next(ui);
    await fireEvent.click(ui.getByRole("button", { name: "返回选择配置" }));
    expect(ui.getByRole("button", { name: /XIASS API/ }).getAttribute("aria-pressed")).toBe("true");
  });
  it("preserves install, launch enhancement, update and settings panels in Utilities", async () => {
    const ui = screen();
    await fireEvent.click(ui.getByRole("button", { name: "辅助功能" }));
    for (const name of ["启动选项", "安装状态", "更新计划"]) expect(ui.getByRole("heading", { name })).toBeTruthy();
    expect(ui.getByRole("checkbox", { name: "同步历史会话" })).toBeTruthy();
    expect(ui.getByRole("checkbox", { name: /官方远端插件缓存/ })).toBeTruthy();
  });
  it("shows an installation action instead of a dead launch button when missing", async () => {
    chatgptDesktopView.update((view) => ({ ...view, kindViews: { ...view.kindViews, msix: { ...view.kindViews.msix, state: null } } }));
    const ui = screen(); await next(ui);
    expect(ui.queryByRole("button", { name: "启动" })).toBeNull();
    expect(ui.getByRole("button", { name: "前往安装" })).toBeTruthy();
  });
});

describe("Codex-only configuration wizard", () => {
  const detection = { tools: [{ id: "codex", name: "Codex", installState: "installed" }], system: [] } as unknown as DetectionSnapshot;
  it("skips tool selection for the desktop entry and uses the XIASS defaults", async () => {
    const cancel = vi.fn();
    const ui = render(SetupWizard, { prefill: { toolId: "codex", mode: "config", lockTool: true }, snapshot: detection, onCancel: cancel });
    expect((ui.getByLabelText("配置名称") as HTMLInputElement).value).toBe("XIASS API");
    expect(ui.queryByRole("button", { name: /Claude Code/ })).toBeNull();
    expect(ui.queryByText("选择工具")).toBeNull();
    await fireEvent.click(ui.getByRole("button", { name: "返回" }));
    expect(cancel).toHaveBeenCalledOnce();
  });
  it("keeps the original tool selection step for the launchpad", () => {
    const ui = render(SetupWizard, { prefill: { toolId: "codex", mode: "config" }, snapshot: detection });
    expect(ui.queryByLabelText("配置名称")).toBeNull();
    expect(ui.getByRole("button", { name: /Codex/ })).toBeTruthy();
  });
});
