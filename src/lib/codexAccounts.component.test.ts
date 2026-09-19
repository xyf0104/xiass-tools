import { cleanup, fireEvent, render, waitFor, within } from "@testing-library/svelte";
import { tick } from "svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import CodexAccountPanel from "../components/CodexAccountPanel.svelte";
import SetupWizard from "../routes/SetupWizard.svelte";
import Profiles from "../routes/Profiles.svelte";
import { codexAccountErrorKey, importCodexAccountJson, selectedCodexAccount, startCodexAccountLogin } from "./codexAccounts";
import { setLocale } from "./i18n";
import type { CodexAccountSession, DetectionSnapshot, ProfileDraft, ProfileSummary } from "../types";

const mocks = vi.hoisted(() => ({ invoke: vi.fn(), kind: "tauri", save: vi.fn(), update: vi.fn(), preview: vi.fn() }));
vi.mock("./api/runtime", () => ({ runtimeAdapter: () => ({ kind: mocks.kind, invoke: mocks.invoke }) }));
// jsdom has no Web Animations API. These tests exercise state transitions,
// not timing or visual animation, so finish the existing transitions immediately.
vi.mock("svelte/transition", () => ({ fly: () => ({ duration: 0 }), fade: () => ({ duration: 0 }) }));
vi.mock("./api", async (original) => ({
  ...await original<typeof import("./api")>(),
  saveProfileDraft: mocks.save, updateProfileDraft: mocks.update, previewProfileWrite: mocks.preview
}));

const fixtureText = '{"tokens":{"id_token":"synthetic-id","access_token":"synthetic-access","refresh_token":"synthetic-refresh"}}';
function ready(id = "staged-test", source: CodexAccountSession["source"] = "json", multiple = false): CodexAccountSession {
  return { id, source, status: "ready", accounts: [
    { label: "one@example.invalid", accountId: "team-a", warnings: [] },
    ...(multiple ? [{ label: "two@example.invalid", accountId: "team-b", warnings: [] }] : [])
  ], errorCode: null };
}
function waiting(): CodexAccountSession { return { id: "waiting-test", source: "oauth", status: "waiting", accounts: [], errorCode: null }; }
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}
const profile: ProfileDraft = {
  id: "saved-official", name: "My official account", app: "codex", isBuiltin: false, mode: "config", provider: "official",
  protocol: "openai-responses", model: "", baseUrl: "", icon: null, remark: null, authRef: null,
  webSearch: "live", imageModel: null, modelContextWindow: 372000, modelAutoCompactTokenLimit: 334800,
  reviewModel: null, modelMappings: [], createdAt: null, updatedAt: null, lastTestStatus: "pending", usageEnabled: false, sortOrder: 1
};
const detection = { tools: [{ id: "codex", name: "Codex", installState: "installed" }], system: [] } as unknown as DetectionSnapshot;
function wizard(locked = true) {
  return render(SetupWizard, { prefill: { toolId: "codex", mode: "config", lockTool: locked }, snapshot: detection });
}
beforeEach(() => {
  vi.resetAllMocks(); mocks.kind = "tauri"; setLocale("zh-CN");
  mocks.invoke.mockImplementation(async (command: string) => {
    if (command === "start_codex_account_login") return waiting();
    if (command === "discard_codex_account_session") return;
    return ready();
  });
  mocks.save.mockResolvedValue(profile); mocks.update.mockResolvedValue(profile);
  mocks.preview.mockResolvedValue({ generatedAt: "2026-09-10T00:00:00Z", profileId: "test-preview", profilePath: "/fixture/state.sqlite",
    targetToolPath: "/fixture/config.toml", backupRequired: false, items: [], warnings: [] });
});
afterEach(() => { cleanup(); vi.useRealTimers(); });

describe("native account API boundary", () => {
  it("refuses browser mock logins and never writes credentials to browser storage", async () => {
    mocks.kind = "browser";
    const store = vi.spyOn(Storage.prototype, "setItem");
    await expect(importCodexAccountJson(fixtureText)).rejects.toThrow("codexAccount.desktopOnly");
    await expect(startCodexAccountLogin()).rejects.toThrow("codexAccount.desktopOnly");
    expect(mocks.invoke).not.toHaveBeenCalled(); expect(store).not.toHaveBeenCalled(); store.mockRestore();
  });
  it("only exposes an opaque handle and valid selected index to profile requests", () => {
    expect(selectedCodexAccount(ready(), 0)).toEqual({ sessionId: "staged-test", accountIndex: 0 });
    for (const index of [-1, 1, NaN, 0.5]) expect(selectedCodexAccount(ready(), index)).toBeNull();
    expect(selectedCodexAccount(waiting(), 0)).toBeNull();
    expect(selectedCodexAccount(null, 0)).toBeNull();
  });
  it("does not echo arbitrary backend error text", () => {
    expect(codexAccountErrorKey(new Error(`native failure: ${fixtureText}`))).toBe("codexAccount.unavailable");
    expect(codexAccountErrorKey("codexAccount.sessionExpired")).toBe("codexAccount.sessionExpired");
  });
});

describe("Codex account panel", () => {
  it("imports pasted JSON, clears the raw input and presents only the account summary", async () => {
    const ui = render(CodexAccountPanel, { mode: "json" });
    const input = ui.getByLabelText("账号 JSON（包含 Token 字段）") as HTMLTextAreaElement;
    expect(input.getAttribute("autocomplete")).toBe("off");
    await fireEvent.input(input, { target: { value: fixtureText } });
    await fireEvent.click(ui.getByRole("button", { name: "校验并导入" }));
    await waitFor(() => expect(ui.getByText(/one@example.invalid/)).toBeTruthy());
    expect(mocks.invoke).toHaveBeenCalledWith("import_codex_account_json", { content: fixtureText });
    expect(ui.container.textContent).not.toContain("synthetic-refresh");
    await fireEvent.click(ui.getByRole("button", { name: "清除本次选择" }));
    expect((ui.getByLabelText("账号 JSON（包含 Token 字段）") as HTMLTextAreaElement).value).toBe("");
  });
  it("imports a selected JSON file", async () => {
    const ui = render(CodexAccountPanel, { mode: "json" });
    const file = new File([fixtureText], "fixture-account.json", { type: "application/json" });
    Object.defineProperty(file, "text", { value: async () => fixtureText });
    await fireEvent.change(ui.container.querySelector('input[type="file"]')!, { target: { files: [file] } });
    await waitFor(() => expect(mocks.invoke).toHaveBeenCalledWith("import_codex_account_json", { content: fixtureText }));
    await waitFor(() => expect(ui.getByText(/one@example.invalid/)).toBeTruthy());
  });
  it("rejects oversized files before reading or sending their contents", async () => {
    const ui = render(CodexAccountPanel, { mode: "json" });
    const read = vi.fn();
    await fireEvent.change(ui.container.querySelector('input[type="file"]')!, { target: { files: [{ size: 1048577, text: read }] } });
    expect(ui.getByRole("alert").textContent).toContain("1 MB");
    expect(read).not.toHaveBeenCalled(); expect(mocks.invoke).not.toHaveBeenCalled();
  });
  it("reports invalid JSON inline without exposing the input", async () => {
    mocks.invoke.mockRejectedValueOnce("codexAccount.invalidJson");
    const ui = render(CodexAccountPanel, { mode: "json" });
    await fireEvent.input(ui.getByRole("textbox"), { target: { value: fixtureText } });
    await fireEvent.click(ui.getByRole("button", { name: "校验并导入" }));
    await waitFor(() => expect(ui.getByRole("alert").textContent).toContain("JSON 格式无效"));
    expect(ui.queryByText(/校验通过/)).toBeNull();
    expect(ui.getByRole("alert").textContent).not.toContain("synthetic");
  });
  it("lets the user select one account from an imported array", async () => {
    const ui = render(CodexAccountPanel, { mode: "json", session: ready("multi", "json", true) });
    await fireEvent.change(ui.getByLabelText("选择要保存到此配置的账号"), { target: { value: "1" } });
    expect(ui.getByRole("status").textContent).toContain("two@example.invalid");
  });
  it("polls official authorization to completion, then stops polling", async () => {
    vi.useFakeTimers();
    const ui = render(CodexAccountPanel);
    await fireEvent.click(ui.getByRole("button", { name: "浏览器授权" }));
    expect(ui.getByRole("status").textContent).toContain("等待浏览器授权");
    mocks.invoke.mockResolvedValueOnce(ready("waiting-test", "oauth"));
    await vi.advanceTimersByTimeAsync(1500); await tick();
    expect(ui.getByRole("status").textContent).toContain("官方授权已完成");
    const count = mocks.invoke.mock.calls.length;
    await vi.advanceTimersByTimeAsync(6000);
    expect(mocks.invoke.mock.calls.length).toBe(count);
  });
  it("offers retry after failed or timed-out authorization", async () => {
    vi.useFakeTimers();
    const ui = render(CodexAccountPanel);
    await fireEvent.click(ui.getByRole("button", { name: "浏览器授权" }));
    mocks.invoke.mockResolvedValueOnce({ ...waiting(), status: "failed", errorCode: "codexAccount.loginTimeout" });
    await vi.advanceTimersByTimeAsync(1500); await tick();
    expect(ui.getByRole("alert").textContent).toContain("超时");
    expect(ui.getByRole("button", { name: "浏览器授权" }).hasAttribute("disabled")).toBe(false);
  });
  it("cancels a pending login and discards a late response", async () => {
    const pending = deferred<CodexAccountSession>(); mocks.invoke.mockReturnValueOnce(pending.promise);
    const ui = render(CodexAccountPanel);
    const login = ui.getByRole("button", { name: "浏览器授权" });
    await fireEvent.click(login); await fireEvent.click(login);
    expect(mocks.invoke).toHaveBeenCalledTimes(1);
    await fireEvent.click(ui.getByRole("button", { name: "取消" }));
    pending.resolve(waiting());
    await waitFor(() => expect(mocks.invoke).toHaveBeenCalledWith("discard_codex_account_session", { id: "waiting-test" }));
    expect(ui.queryByRole("status")).toBeNull();
  });
  it("cleans up a waiting session on unmount but preserves ready sessions for parent navigation", async () => {
    const pending = render(CodexAccountPanel, { session: waiting() });
    pending.unmount();
    expect(mocks.invoke).toHaveBeenCalledWith("discard_codex_account_session", { id: "waiting-test" });
    mocks.invoke.mockClear();
    const complete = render(CodexAccountPanel, { session: ready() }); complete.unmount();
    expect(mocks.invoke).not.toHaveBeenCalled();
  });
  it("imports the local login only on an explicit click", async () => {
    const ui = render(CodexAccountPanel);
    expect(mocks.invoke).not.toHaveBeenCalled();
    await fireEvent.click(ui.getByRole("button", { name: "使用本机登录" }));
    expect(mocks.invoke).toHaveBeenCalledWith("import_local_codex_account", undefined);
    expect(ui.getByText(/one@example.invalid/)).toBeTruthy();
  });
});

describe("wizard account integration", () => {
  it("keeps API Key fields and model choices intact when switching login methods", async () => {
    const ui = wizard();
    await fireEvent.input(ui.getByLabelText("配置名称"), { target: { value: "My API" } });
    const keyInput = ui.container.querySelector('input[type="password"]')!;
    await fireEvent.input(keyInput, { target: { value: "fixture-api-key" } });
    await fireEvent.click(ui.getByRole("button", { name: "JSON 账号导入" }));
    expect(ui.queryByLabelText("配置名称")).toBeNull();
    expect(ui.queryByLabelText("模型（可选）")).toBeNull();
    expect(ui.queryByLabelText("审查模型（可选）")).toBeNull();
    const accountPanel = ui.container.querySelector('section[aria-label="登录方式"]') as HTMLElement;
    expect(accountPanel).toBeTruthy();
    const contextPanel = ui.container.querySelector('section[aria-label="上下文窗口"]') as HTMLElement;
    expect(contextPanel).toBeTruthy();
    expect(accountPanel.compareDocumentPosition(contextPanel) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0);
    expect(ui.getByRole("button", { name: "下一步" }).hasAttribute("disabled")).toBe(true);
    await fireEvent.click(ui.getByRole("button", { name: "自定义 API 配置" }));
    expect((ui.getByLabelText("配置名称") as HTMLInputElement).value).toBe("My API");
    expect((ui.container.querySelector('input[type="password"]') as HTMLInputElement).value).toBe("fixture-api-key");
    await fireEvent.click(ui.getByRole("button", { name: "JSON 账号导入" }));
    expect(ui.queryByLabelText("配置名称")).toBeNull();
  });
  it("saves a selected JSON account through an opaque handle and retains it across Next/Back", async () => {
    const ui = wizard();
    await fireEvent.click(ui.getByRole("button", { name: "JSON 账号导入" }));
    await fireEvent.input(ui.getByLabelText("账号 JSON（包含 Token 字段）"), { target: { value: fixtureText } });
    await fireEvent.click(ui.getByRole("button", { name: "校验并导入" }));
    await fireEvent.click(ui.getByRole("button", { name: "下一步" }));
    await waitFor(() => expect(ui.getByRole("button", { name: "保存" }).hasAttribute("disabled")).toBe(false));
    await fireEvent.click(ui.getByRole("button", { name: "返回" }));
    await waitFor(() => expect(ui.getByText(/one@example.invalid/)).toBeTruthy());
    await fireEvent.click(ui.getByRole("button", { name: "下一步" }));
    await waitFor(() => expect(ui.getByRole("button", { name: "保存" }).hasAttribute("disabled")).toBe(false));
    await fireEvent.click(ui.getByRole("button", { name: "保存" }));
    await waitFor(() => expect(mocks.save).toHaveBeenCalledOnce());
    expect(mocks.save.mock.calls[0][0]).toMatchObject({ provider: "official", baseUrl: "", apiKey: "", model: "", reviewModel: null,
      codexAccount: { sessionId: "staged-test", accountIndex: 0 } });
    expect(JSON.stringify(mocks.save.mock.calls)).not.toContain("synthetic");
    expect(JSON.stringify(mocks.preview.mock.calls)).not.toContain("synthetic");
  });
  it("does not get stuck waiting after returning to tool selection during OAuth", async () => {
    const ui = wizard(false);
    await fireEvent.click(ui.getByRole("button", { name: "下一步" }));
    await fireEvent.click(ui.getByRole("button", { name: "官方 OAuth 配置" }));
    await fireEvent.click(ui.getByRole("button", { name: "浏览器授权" }));
    await fireEvent.click(ui.getByRole("button", { name: "返回" }));
    await waitFor(() => expect(ui.queryByRole("button", { name: "浏览器授权" })).toBeNull());
    await fireEvent.click(ui.getByRole("button", { name: "下一步" }));
    expect(ui.getByRole("button", { name: "浏览器授权" }).hasAttribute("disabled")).toBe(false);
    expect(ui.queryByText(/等待浏览器授权/)).toBeNull();
    expect(mocks.invoke).toHaveBeenCalledWith("discard_codex_account_session", { id: "waiting-test" });
  });
});

describe("editing saved official configurations", () => {
  async function edit() {
    const summary: ProfileSummary = { drafts: [profile], activeProfilesByMode: { config: {}, gateway: {} }, configDir: "/fixture",
      activeProfile: null, activeProfileName: null, codexAuth: { available: false } as ProfileSummary["codexAuth"] };
    const ui = render(Profiles, { summary, snapshot: detection });
    await fireEvent.click(ui.getByRole("button", { name: /编辑/ }));
    return { ...ui, dialog: within(ui.getByRole("dialog")) };
  }
  it("preserves the saved account on ordinary edits", async () => {
    const ui = await edit();
    await fireEvent.input(ui.dialog.getByLabelText("配置名称"), { target: { value: "Renamed official" } });
    await fireEvent.click(ui.dialog.getByRole("button", { name: "保存修改" }));
    await waitFor(() => expect(mocks.update).toHaveBeenCalledOnce());
    expect(mocks.update.mock.calls[0][0]).toMatchObject({ profileId: profile.id, name: "Renamed official", codexAccount: null });
    expect(mocks.invoke).not.toHaveBeenCalledWith("import_local_codex_account", expect.anything());
  });
  it("only replaces the account after explicit import and save", async () => {
    const ui = await edit();
    await fireEvent.click(ui.dialog.getByRole("button", { name: "JSON 账号导入" }));
    await fireEvent.input(ui.dialog.getByLabelText("账号 JSON（包含 Token 字段）"), { target: { value: fixtureText } });
    await fireEvent.click(ui.dialog.getByRole("button", { name: "校验并导入" }));
    expect(mocks.update).not.toHaveBeenCalled();
    await fireEvent.click(ui.dialog.getByRole("button", { name: "保存修改" }));
    await waitFor(() => expect(mocks.update).toHaveBeenCalledOnce());
    expect(mocks.update.mock.calls[0][0].codexAccount).toEqual({ sessionId: "staged-test", accountIndex: 0 });
    expect(JSON.stringify(mocks.update.mock.calls)).not.toContain("synthetic");
  });
});
