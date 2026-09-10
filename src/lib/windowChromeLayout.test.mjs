import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import postcss from "postcss";

const read = (path) => readFileSync(new URL(`../../${path}`, import.meta.url), "utf8");
const config = JSON.parse(read("src-tauri/tauri.conf.json"));
const capability = JSON.parse(read("src-tauri/capabilities/default.json"));
const styles = postcss.parse(read("src/styles.css"));

function declarations(selector) {
  const result = {};
  styles.walkRules(selector, (rule) => {
    if (rule.parent.type !== "root") return;
    rule.walkDecls((declaration) => { result[declaration.prop] = declaration.value; });
  });
  return result;
}

function declarationsFromCombinedSelector(selector) {
  const result = {};
  styles.walkRules((rule) => {
    if (rule.parent.type !== "root") return;
    const selectors = rule.selector.split(",").map((item) => item.trim());
    if (!selectors.includes(selector)) return;
    rule.walkDecls((declaration) => { result[declaration.prop] = declaration.value; });
  });
  return result;
}

test("macOS keeps native traffic lights in the platform default position", () => {
  const window = config.app.windows[0];
  assert.equal(window.titleBarStyle, "Overlay");
  assert.equal(window.hiddenTitle, true);
  assert.equal(window.decorations, true);
  assert.equal(window.trafficLightPosition, undefined);
  assert.deepEqual(capability.windows, ["main"]);
  assert.ok(capability.permissions.includes("core:window:allow-start-dragging"));
  assert.ok(capability.permissions.includes("core:window:allow-toggle-maximize"));
});

test("the frontend distinguishes Windows from Linux before native detection", () => {
  const main = read("src/main.ts");
  assert.match(main, /navigatorPlatform/);
  assert.match(main, /\/Win\/i\.test\(navigatorPlatform\)/);
  assert.match(main, /\? "windows"/);
});

test("Windows uses the integrated title area and native window permissions", () => {
  const drag = declarationsFromCombinedSelector(':root[data-platform="windows"] .xiass-window-drag-region');
  assert.equal(drag.position, "fixed");
  assert.equal(drag.height, "var(--xiass-window-drag-height)");
  assert.ok(capability.permissions.includes("core:window:allow-minimize"));
  assert.ok(capability.permissions.includes("core:window:allow-close"));
  assert.ok(capability.permissions.includes("core:window:allow-set-decorations"));
  assert.match(read("src/components/WindowDragRegion.svelte"), /windowMinimize/);
});

test("transparent drag area spans the window at every sidebar breakpoint", () => {
  const drag = declarationsFromCombinedSelector(':root[data-platform="macos"] .xiass-window-drag-region');
  assert.equal(drag.position, "fixed");
  assert.equal(drag.inset, "0 0 auto 0");
  assert.equal(drag.height, "var(--xiass-window-drag-height)");
  assert.equal(drag.background, "transparent");
  styles.walkRules((rule) => {
    if (!rule.selector.includes(".xiass-window-drag-region")) return;
    rule.walkDecls(/^(width|max-width)$/, () => assert.fail("Drag area must not be constrained to the sidebar width"));
  });
});

test("sidebar merges with the window edge and scroll controls remain below the drag area", () => {
  const sidebar = declarationsFromCombinedSelector(':root[data-platform="macos"] .cs-app-sidebar');
  assert.equal(sidebar.margin, "0");
  assert.equal(sidebar["border-radius"], "0");
  assert.equal(sidebar["box-shadow"], "none");
  assert.equal(sidebar.background, "var(--sidebar-bg)");
  const workspace = declarationsFromCombinedSelector(':root[data-platform="macos"] .cs-app-workspace');
  assert.equal(workspace["margin-top"], "var(--xiass-window-drag-height)");
  assert.equal(workspace.height, "calc(100vh - var(--xiass-window-drag-height))");
  assert.match(read("src/App.svelte"), /<WindowDragRegion\s*\/>/);
});
