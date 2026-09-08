import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const readJson = (path) =>
  JSON.parse(readFileSync(new URL(`../../${path}`, import.meta.url), "utf8").replace(/^\uFEFF/, ""));

test("the rebranded base build keeps updater disabled until XIASS release signing is configured", () => {
  const tauriConfig = readJson("src-tauri/tauri.conf.json");
  const updaterConfig = tauriConfig.plugins?.updater;

  assert.equal(typeof updaterConfig, "object");
  assert.ok(Array.isArray(updaterConfig.endpoints));
  assert.deepEqual(updaterConfig.endpoints, []);
  assert.equal(typeof updaterConfig.pubkey, "string");
  assert.equal(updaterConfig.pubkey, "");
});
