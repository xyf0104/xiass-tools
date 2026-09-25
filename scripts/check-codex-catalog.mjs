// Exercise the installed Codex parser/model list without a login or inference.
// Usage: node scripts/check-codex-catalog.mjs /absolute/catalog.json [codex-binary]
import assert from "node:assert/strict";
import { mkdtempSync, readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { isAbsolute, join } from "node:path";
import { spawn } from "node:child_process";

const [catalogPath, binary = "codex"] = process.argv.slice(2);
assert(catalogPath && isAbsolute(catalogPath), "Pass an absolute model catalog path");
const catalog = JSON.parse(readFileSync(catalogPath, "utf8"));
const required = ["gpt-6-sol", "gpt-6-luna"];
for (const id of required) assert(catalog.models.some((m) => m.slug === id), `Catalog missing ${id}`);
const home = mkdtempSync(join(tmpdir(), "xiass-codex-catalog-check-"));
const env = { ...process.env, CODEX_HOME: home };
delete env.OPENAI_API_KEY;
delete env.CODEX_API_KEY;
const child = spawn(binary, ["app-server", "--stdio", "-c", `model_catalog_json=${JSON.stringify(catalogPath)}`], {
  env, stdio: ["pipe", "pipe", "pipe"]
});
let buffer = "";
let complete = false;
const returned = new Set();
const send = (message) => child.stdin.write(`${JSON.stringify(message)}\n`);
const fail = (message) => {
  if (complete) return;
  complete = true;
  process.exitCode = 1;
  console.error(message);
  clearTimeout(timeout);
  child.kill();
};
const timeout = setTimeout(() => fail("Codex model/list timed out"), 20000);
child.on("error", () => fail("Could not start Codex; check the supplied binary path"));
// Don't print arbitrary server logs, credentials, or catalog instruction text.
child.stderr.on("data", () => {});
child.stdout.on("data", (chunk) => {
  buffer += chunk;
  let index;
  while ((index = buffer.indexOf("\n")) >= 0) {
    const line = buffer.slice(0, index);
    buffer = buffer.slice(index + 1);
    let message;
    try { message = JSON.parse(line); } catch { continue; }
    if (message.error) { fail("Codex rejected the configuration or model/list request"); return; }
    if (message.id === 1) {
      send({ method: "initialized", params: {} });
      send({ id: 2, method: "model/list", params: { includeHidden: true } });
    }
    if (message.id === 2) {
      for (const model of message.result?.data ?? []) returned.add(model.model ?? model.id);
      if (message.result?.nextCursor) {
        send({ id: 2, method: "model/list", params: { includeHidden: true, cursor: message.result.nextCursor } });
        continue;
      }
      const missing = required.filter((id) => !returned.has(id));
      if (missing.length) { fail(`Codex did not load: ${missing.join(", ")}`); return; }
      complete = true;
      clearTimeout(timeout);
      console.log(JSON.stringify({ verified: true, requiredModels: required, modelCount: returned.size, isolatedHome: home }));
      child.kill();
    }
  }
});
child.on("close", () => {
  if (!complete) fail("Codex exited before model/list completed");
});
send({ id: 1, method: "initialize", params: { clientInfo: { name: "xiass_catalog_check", version: "1.0" }, capabilities: { experimentalApi: true } } });
