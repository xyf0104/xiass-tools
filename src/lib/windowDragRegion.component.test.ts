import { cleanup, fireEvent, render } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import WindowDragRegion from "../components/WindowDragRegion.svelte";

const native = vi.hoisted(() => ({
  isTauri: vi.fn(() => true),
  startDragging: vi.fn(() => Promise.resolve()),
  toggleMaximize: vi.fn(() => Promise.resolve()),
  isMaximized: vi.fn(() => Promise.resolve(false)),
  setDecorations: vi.fn(() => Promise.resolve()),
  minimize: vi.fn(() => Promise.resolve()),
  close: vi.fn(() => Promise.resolve())
}));

vi.mock("@tauri-apps/api/core", () => ({ isTauri: native.isTauri }));
vi.mock("@tauri-apps/api/window", () => ({ getCurrentWindow: () => native }));

beforeEach(() => {
  vi.clearAllMocks();
  native.isTauri.mockReturnValue(true);
  document.documentElement.dataset.platform = "macos";
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  delete document.documentElement.dataset.platform;
});

function region() {
  return render(WindowDragRegion).container.querySelector(".xiass-window-drag-region")!;
}

describe("macOS integrated window dragging", () => {
  it.each([24, 550, 1080])("starts a native drag at horizontal position %i", async (clientX) => {
    await fireEvent.mouseDown(region(), { button: 0, detail: 1, clientX, clientY: 28 });
    expect(native.startDragging).toHaveBeenCalledOnce();
    expect(native.toggleMaximize).not.toHaveBeenCalled();
  });

  it.each([1, 2])("does not drag with mouse button %i", async (button) => {
    await fireEvent.mouseDown(region(), { button, detail: 1 });
    expect(native.startDragging).not.toHaveBeenCalled();
  });

  it("ignores browser preview presses", async () => {
    native.isTauri.mockReturnValue(false);
    await fireEvent.mouseDown(region(), { button: 0, detail: 1 });
    expect(native.startDragging).not.toHaveBeenCalled();
  });

  it("leaves unknown native title bars alone", async () => {
    document.documentElement.dataset.platform = "other";
    await fireEvent.mouseDown(region(), { button: 0, detail: 1 });
    expect(native.startDragging).not.toHaveBeenCalled();
  });

  it("uses the integrated title area on Windows", async () => {
    document.documentElement.dataset.platform = "windows";
    const ui = render(WindowDragRegion);
    const target = ui.container.querySelector(".xiass-window-drag-region")!;
    await fireEvent.mouseDown(target, { button: 0, detail: 1, clientX: 550, clientY: 20 });
    expect(native.startDragging).toHaveBeenCalledOnce();
    expect(native.setDecorations).toHaveBeenCalledWith(false);
    expect(ui.container.querySelectorAll(".xiass-window-control")).toHaveLength(3);
  });

  it("routes Windows window controls to native operations", async () => {
    document.documentElement.dataset.platform = "windows";
    const ui = render(WindowDragRegion);
    const controls = ui.container.querySelectorAll<HTMLButtonElement>(".xiass-window-control");
    await fireEvent.click(controls[0]);
    await fireEvent.click(controls[1]);
    await fireEvent.click(controls[2]);
    expect(native.minimize).toHaveBeenCalledOnce();
    expect(native.toggleMaximize).toHaveBeenCalledOnce();
    expect(native.close).toHaveBeenCalledOnce();
  });

  it("toggles maximize only on the stationary second release", async () => {
    const target = region();
    const event = { button: 0, detail: 2, clientX: 550, clientY: 20 };
    await fireEvent.mouseDown(target, event);
    expect(native.startDragging).not.toHaveBeenCalled();
    expect(native.toggleMaximize).not.toHaveBeenCalled();
    await fireEvent.mouseUp(target, event);
    expect(native.toggleMaximize).toHaveBeenCalledOnce();
  });

  it("cancels maximize if the second click moves, even if it returns", async () => {
    const target = region();
    const event = { button: 0, detail: 2, clientX: 550, clientY: 20 };
    await fireEvent.mouseDown(target, event);
    await fireEvent.mouseMove(target, { clientX: 552, clientY: 20 });
    await fireEvent.mouseUp(target, event);
    expect(native.toggleMaximize).not.toHaveBeenCalled();
  });

  it("cancels maximize after leaving the drag area", async () => {
    const target = region();
    const event = { button: 0, detail: 2, clientX: 550, clientY: 20 };
    await fireEvent.mouseDown(target, event);
    await fireEvent.mouseLeave(target);
    await fireEvent.mouseUp(target, event);
    expect(native.toggleMaximize).not.toHaveBeenCalled();
  });

  it("never drags when clicking an adjacent app control", async () => {
    const target = region();
    const button = document.createElement("button");
    target.parentElement!.appendChild(button);
    await fireEvent.mouseDown(button, { button: 0, detail: 1 });
    expect(native.startDragging).not.toHaveBeenCalled();
  });

  it("does not duplicate the native drag listener or keyboard controls", () => {
    const target = region();
    expect(target.hasAttribute("data-tauri-drag-region")).toBe(false);
    expect(target.hasAttribute("tabindex")).toBe(false);
    expect(target.getAttribute("aria-hidden")).toBe("true");
    expect(target.childElementCount).toBe(0);
  });

  it("handles a rejected native operation without an unhandled promise", async () => {
    const warning = vi.spyOn(console, "warn").mockImplementation(() => {});
    native.startDragging.mockRejectedValueOnce(new Error("Window closed"));
    await fireEvent.mouseDown(region(), { button: 0, detail: 1 });
    expect(warning).toHaveBeenCalledOnce();
  });
});
