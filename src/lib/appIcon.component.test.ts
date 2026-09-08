import { cleanup, render } from "@testing-library/svelte";
import { afterEach, describe, expect, it } from "vitest";
import AppIcon from "../components/AppIcon.svelte";

afterEach(cleanup);

describe("XIASS coloured icons", () => {
  it.each(["arrowRight", "play", "power", "update", "install", "apply", "add"])("renders %s with an orange-blue SVG gradient", (name) => {
    const { container } = render(AppIcon, { name });
    expect(container.querySelector("svg")?.classList.contains("xiass-icon--action")).toBe(true);
    expect(container.querySelectorAll("linearGradient stop")).toHaveLength(4);
    expect(container.querySelector("path")?.getAttribute("fill")).toMatch(/^url\(#.+\)$/);
    expect(container.querySelector("svg")?.getAttribute("aria-hidden")).toBe("true");
  });

  it("keeps each gradient reference unique across repeated icons", () => {
    const first = render(AppIcon, { name: "play" });
    const second = render(AppIcon, { name: "play" });
    const firstId = first.container.querySelector("linearGradient")?.id;
    const secondId = second.container.querySelector("linearGradient")?.id;
    expect(firstId).toBeTruthy();
    expect(firstId).not.toBe(secondId);
    expect(first.container.querySelector("path")?.getAttribute("fill")).toBe(`url(#${firstId})`);
  });

  it.each([["check", "success"], ["warning", "warning"], ["error", "danger"], ["settings", "violet"], ["info", "info"]])("uses semantic colour for %s", (name, tone) => {
    const { container } = render(AppIcon, { name, title: "Status" });
    expect(container.querySelector("svg")?.classList.contains(`xiass-icon--${tone}`)).toBe(true);
    expect(container.querySelector("svg")?.getAttribute("aria-label")).toBe("Status");
    expect(container.querySelector("linearGradient")).toBeNull();
  });

  it("can distinguish a save action from a success status", () => {
    const { container } = render(AppIcon, { name: "check", tone: "action" });
    expect(container.querySelector("linearGradient")).not.toBeNull();
  });
});
