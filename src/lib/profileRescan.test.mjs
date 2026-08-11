import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const read = (path) => readFileSync(new URL(`../../${path}`, import.meta.url), "utf8").replace(/\r\n/g, "\n");

test("the profiles page can rescan tool configs on demand", () => {
  const profiles = read("src/routes/Profiles.svelte");
  const app = read("src/App.svelte");

  // The button has to exist and be wired to the handler, not to a local
  // reload that skips detection.
  assert.match(profiles, /export let onRescan/);
  assert.match(profiles, /export let rescanning/);
  assert.match(profiles, /data-rescan-button="true"/);
  assert.match(profiles, /on:click=\{onRescan\}/);
  assert.match(profiles, /disabled=\{rescanning\}/);

  assert.match(app, /onRescan=\{rescanNativeConfigs\}/);
  assert.match(app, /rescanning=\{profileRescanning\}/);
});

test("rescanning runs the detecting summary load, not the quiet one", () => {
  const app = read("src/App.svelte");
  const handler = app
    .split("async function rescanNativeConfigs()")[1]
    ?.split("async function refreshAfterProfileChange")[0];

  assert.ok(handler, "the rescan handler should exist");
  // loadProfileSummary is the entry point that reconciles native configs and
  // imports drafts; a quiet variant would leave the page just as stale.
  assert.match(handler, /loadProfileSummary\(\)/);
  assert.match(handler, /applyProfileSummary/);
  // It must guard against overlapping runs and always clear its busy flag.
  assert.match(handler, /if \(profileRescanning\)/);
  assert.match(handler, /finally/);
});

test("profile mutations still avoid triggering detection", () => {
  const app = read("src/App.svelte");
  const handler = app
    .split("async function refreshAfterProfileChange")[1]
    ?.split("async function refreshCurrentRouteAfterSwitch")[0];

  assert.ok(handler, "the profile change handler should exist");
  // Detection on a mutation can import a ghost draft from a config file that
  // has not been rewritten yet, so only the explicit button may run it.
  assert.doesNotMatch(handler, /loadProfileSummary\(\)/);
});

test("the rescan button is translated in every locale", () => {
  for (const locale of ["en-US", "zh-CN", "zh-TW"]) {
    const source = read(`src/lib/locales/${locale}.ts`);
    for (const key of ["profiles.rescan", "profiles.rescanning", "profiles.rescanHint"]) {
      assert.match(source, new RegExp(`"${key}":`), `${locale} is missing ${key}`);
    }
  }
});
