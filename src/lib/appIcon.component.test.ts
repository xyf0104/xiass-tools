import { cleanup, render } from "@testing-library/svelte";
import { afterEach, describe, expect, it } from "vitest";
import AppIcon from "../components/AppIcon.svelte";

afterEach(cleanup);

describe("XIASS coloured icons", () => {
  it.each(["arrowRight", "play", "power", "update", "install", "apply", "add"])("renders %s as a solid high-contrast action glyph", (name) => {
    const { container } = render(AppIcon, { name });
    expect(container.querySelector("svg")?.classList.contains("xiass-icon--action")).toBe(true);
    expect(container.querySelector("linearGradient")).toBeNull();
    expect(container.querySelector("path")?.getAttribute("fill")).toBe("currentColor");
    expect(container.querySelector("svg")?.getAttribute("aria-hidden")).toBe("true");
  });

  it("does not create per-instance gradient definitions", () => {
    const first = render(AppIcon, { name: "play" });
    const second = render(AppIcon, { name: "play" });
    expect(first.container.querySelector("linearGradient")).toBeNull();
    expect(second.container.querySelector("linearGradient")).toBeNull();
  });

  it.each([["check", "success"], ["warning", "warning"], ["error", "danger"], ["settings", "violet"], ["shield", "violet"], ["info", "info"]])("uses semantic colour for %s", (name, tone) => {
    const { container } = render(AppIcon, { name, title: "Status" });
    expect(container.querySelector("svg")?.classList.contains(`xiass-icon--${tone}`)).toBe(true);
    expect(container.querySelector("svg")?.getAttribute("aria-label")).toBe("Status");
    expect(container.querySelector("linearGradient")).toBeNull();
  });

  it("can distinguish a save action from a success status", () => {
    const { container } = render(AppIcon, { name: "check", tone: "action" });
    expect(container.querySelector("linearGradient")).toBeNull();
    expect(container.querySelector("svg")?.classList.contains("xiass-icon--action")).toBe(true);
  });
});
