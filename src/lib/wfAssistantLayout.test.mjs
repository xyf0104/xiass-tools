import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";

const source = readFileSync(new URL("../routes/WfAssistant.svelte", import.meta.url), "utf8");

test("WF section navigation rebuilds the embedded page and clears the blocking overlay after load", () => {
  assert.match(
    source,
    /\$:\s*frameUrl\s*=\s*session\s*\?\s*buildFrameUrl\(session,\s*activeSection,\s*theme\)\s*:\s*""/,
  );
  assert.match(source, /on:load=\{handleFrameLoad\}/);
  assert.match(source, /\{:else if loading \|\| !frameReady\}/);
  assert.doesNotMatch(source, /\{#if loading \|\| \(session && !frameReady\) \|\| error \|\| !session\}/);
});
