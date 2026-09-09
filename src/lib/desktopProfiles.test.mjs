import assert from "node:assert/strict";
import test from "node:test";
import { desktopCodexProfiles, activeDesktopCodexProfileId, resolveDesktopCodexSelection } from "../../.tmp-tests/lib/chatgptDesktopProfiles.js";
import { profileNameErrorKey, profileProviderFromName, createXiassApiTemplate, isXiassApiProfile, canReuseProfileKeyForModels } from "../../.tmp-tests/lib/profiles/xiass.js";
import { profileIconValue, profileIconIsImage } from "../../.tmp-tests/lib/profiles/presentation.js";

const custom = { ...createXiassApiTemplate(), id: "saved", authRef: "keychain:test" };
const official = { ...custom, id: "official", provider: "official", isBuiltin: true };
const makeSummary = (config = { codex: custom.id }) => ({
  drafts: [custom, { ...custom, id: "gateway", mode: "gateway" }, { ...custom, id: "other", app: "claude" }, official],
  activeProfilesByMode: { config, gateway: {} }
});
test("desktop profiles only list Codex configs with built-in first", () => {
  assert.deepEqual(desktopCodexProfiles(makeSummary()).map((p) => p.id), ["official", "saved"]);
  assert.deepEqual(desktopCodexProfiles(null), []);
});
test("desktop selection restores active Codex aliases and recovers missing choices", () => {
  for (const app of ["codex", "codex-app", "chatgpt-desktop", "codex-client", "codex-desktop"]) {
    const summary = makeSummary({ [app]: "saved" });
    assert.equal(activeDesktopCodexProfileId(summary), "saved");
    assert.equal(resolveDesktopCodexSelection(summary, "gone"), "saved");
    assert.equal(resolveDesktopCodexSelection(summary, "official"), "official");
  }
  assert.equal(resolveDesktopCodexSelection(makeSummary({ claude: "other" }), null), "official");
  assert.equal(resolveDesktopCodexSelection(null, null), null);
});
test("profile names are the provider display name, including spaces and Unicode", () => {
  for (const name of ["XIASS API", "我的 API", 'Test "Name"']) {
    assert.equal(profileNameErrorKey(name), null);
    assert.equal(profileProviderFromName(" " + name + " "), name);
  }
  assert.equal(profileNameErrorKey("official"), "profiles.nameReserved");
  assert.equal(profileNameErrorKey("Official"), "profiles.nameReserved");
  assert.equal(profileNameErrorKey("a\nb"), "profiles.nameControlCharacters");
  assert.equal(profileNameErrorKey(" "), "profiles.nameRequired");
  assert.equal(profileProviderFromName("Codex Official", true), "official");
});
test("the XIASS template has no key and gets the brand logo without affecting ordinary profiles", () => {
  assert.equal(createXiassApiTemplate().authRef, null);
  assert.equal(isXiassApiProfile(custom), true);
  assert.equal(profileIconValue(custom, custom.name), "/xiass-tools-logo.png");
  assert.equal(profileIconIsImage(profileIconValue(custom, custom.name)), true);
  assert.equal(profileIconValue({ ...custom, name: "Work" }, "Work"), "W");
  assert.equal(profileIconValue({ ...custom, icon: "Z" }, custom.name), "Z");
});
test("renaming preserves model-fetch key reuse but changing destinations does not", () => {
  assert.equal(canReuseProfileKeyForModels(custom, custom.protocol, custom.baseUrl + "/"), true);
  assert.equal(canReuseProfileKeyForModels(custom, custom.protocol, "https://other.test"), false);
  assert.equal(canReuseProfileKeyForModels(custom, "anthropic-messages", custom.baseUrl), false);
});
