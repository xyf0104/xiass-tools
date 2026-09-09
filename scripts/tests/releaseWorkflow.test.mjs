import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const workflow = readFileSync(new URL("../../.github/workflows/build-macos.yml", import.meta.url), "utf8");
const windowsWorkflow = readFileSync(new URL("../../.github/workflows/build-windows.yml", import.meta.url), "utf8");

test("macOS workflow gates packaging on frontend and Rust quality checks", () => {
  assert.match(workflow, /npm test/);
  assert.match(workflow, /cargo test --locked/);
  assert.match(workflow, /cargo check --locked --all-targets/);
  assert.match(workflow, /needs: quality/);
});

test("macOS release builds only branded DMG installers without the upstream updater", () => {
  assert.match(workflow, /npm run tauri:build -- --target \$\{\{ matrix\.target \}\} --bundles dmg --ci/);
  assert.match(workflow, /XIASS-Tools-macOS-\$\{\{ matrix\.arch \}\}/);
  assert.match(workflow, /bundle\/dmg\/\*\.dmg/);
  assert.doesNotMatch(workflow, /build-macos-updater-target|TAURI_SIGNING_PRIVATE_KEY|CODESTUDIO_UPDATE_BASE_URL|\.app\.tar\.gz|\.dmg\.sig/);
  assert.match(workflow, /retention-days: 14/);
});

test("Windows release builds only the XIASS NSIS installer", () => {
  assert.match(windowsWorkflow, /npm run tauri:build -- --bundles nsis --ci/);
  assert.match(windowsWorkflow, /XIASS-Tools-Windows/);
  assert.match(windowsWorkflow, /bundle\/nsis\/\*\.exe/);
  assert.doesNotMatch(windowsWorkflow, /tauri:build:windows|build-burn|bundle\/msi|\.sig/);
  assert.match(windowsWorkflow, /needs: quality/);
});
