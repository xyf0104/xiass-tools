// Publish completed CI artifacts without routing binaries through a developer's
// connection. Never rebuild or move the release tag in this job.
import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { spawnSync } from "node:child_process";
import { mkdtempSync, readdirSync, readFileSync, statSync } from "node:fs";
import { tmpdir } from "node:os";
import { basename, join } from "node:path";

const repo = process.env.GITHUB_REPOSITORY;
const tag = process.env.RELEASE_TAG;
assert.equal(repo, "xyf0104/xiass-tools", "Unexpected release repository");
assert.match(tag ?? "", /^v\d+\.\d+\.\d+$/, "Expected a stable version tag");
const runIds = [process.env.MACOS_RUN_ID, process.env.WINDOWS_RUN_ID];
for (const id of runIds) assert.match(id ?? "", /^\d+$/, "Invalid source run ID");

function command(bin, args, allowFailure = false) {
  const result = spawnSync(bin, args, { encoding: "utf8", maxBuffer: 8 * 1024 * 1024, timeout: 180000 });
  if (result.status !== 0 && !allowFailure) throw Error(`${bin} failed (${result.status}): ${(result.stderr ?? "").slice(-1500)}`);
  return result;
}
const gh = (...args) => command("gh", args).stdout.trim();
const api = (path) => JSON.parse(gh("api", `repos/${repo}/${path}`));
// The workflow checkout may already have materialized the release tag locally.
// Force-refresh the local tag instead of using a plain fetch, which fails with
// "would clobber existing tag" even when the remote tag points to the exact
// same commit. The source-run SHA checks below still fail closed on a mismatch.
command("git", ["fetch", "--force", "origin", `refs/tags/${tag}:refs/tags/${tag}`]);
const sha = command("git", ["rev-parse", `${tag}^{commit}`]).stdout.trim();
const workflows = [".github/workflows/build-macos.yml", ".github/workflows/build-windows.yml"];
for (let index = 0; index < runIds.length; index++) {
  const run = api(`actions/runs/${runIds[index]}`);
  assert.equal(run.status, "completed");
  assert.equal(run.conclusion, "success");
  assert.equal(run.head_sha, sha, "Build is from a different commit than the release tag");
  assert.equal(run.path, workflows[index], "Unexpected source workflow");
  assert.equal(run.repository.full_name, repo);
}
const version = tag.slice(1);
const metadata = JSON.parse(command("git", ["show", `${tag}:package.json`]).stdout);
assert.equal(metadata.version, version, "Tag and application version differ");
const temp = mkdtempSync(join(tmpdir(), "xiass-release-"));
const artifactSpecs = [
  [runIds[0], "XIASS-Tools-macOS-arm64"],
  [runIds[0], "XIASS-Tools-macOS-x64"],
  [runIds[1], "XIASS-Tools-Windows"]
];
for (const [runId, name] of artifactSpecs) {
  gh("run", "download", runId, "--repo", repo, "--name", name, "--dir", join(temp, name));
}
function filesAt(path) {
  return readdirSync(path, { withFileTypes: true }).flatMap((item) => {
    const file = join(path, item.name);
    return item.isDirectory() ? filesAt(file) : [file];
  });
}
const files = filesAt(temp);
const names = files.map((file) => basename(file).replaceAll(" ", "."));
const expected = [
  `XIASS.Tools_${version}_aarch64.dmg`,
  `XIASS.Tools_${version}_x64.dmg`,
  `XIASS.Tools_${version}_x64-setup.exe`
];
assert.deepEqual([...names].sort(), [...expected].sort(), "Only the three requested installers may be published");
const hashes = files.map((file) => `sha256:${createHash("sha256").update(readFileSync(file)).digest("hex")}`);
for (const file of files) assert(statSync(file).size > 1024 * 1024, "Installer is unexpectedly small");

const current = command("gh", ["release", "view", tag, "--repo", repo, "--json", "isDraft,isPrerelease"], true);
if (current.status !== 0) {
  const notes = command("git", ["show", `${tag}:docs/releases/${tag}.md`]).stdout;
  gh("release", "create", tag, "--repo", repo, "--verify-tag", "--draft", "--title", `XIASS Tools ${tag}`, "--notes", notes);
} else {
  const release = JSON.parse(current.stdout);
  assert(!release.isPrerelease, "Refusing to mutate a prerelease");
}
// GitHub's by-tag endpoint does not resolve draft releases. Resolve the draft
// by its numeric ID from the authenticated release list until it is published.
const releaseId = api("releases?per_page=100").find((item) => item.tag_name === tag)?.id;
assert(releaseId, "Could not resolve the draft release");
let release = api(`releases/${releaseId}`);
assert(release.assets.every((asset) => expected.includes(asset.name)), "Unexpected existing release asset");
for (let index = 0; index < files.length; index++) {
  const existing = release.assets.find((asset) => asset.name === names[index]);
  if (existing) {
    assert.equal(existing.digest, hashes[index], "Existing asset differs; never silently overwrite it");
  } else {
    gh("release", "upload", tag, files[index], "--repo", repo);
  }
}
release = api(`releases/${releaseId}`);
assert.equal(release.assets.length, 3);
for (let index = 0; index < files.length; index++) {
  const asset = release.assets.find((item) => item.name === names[index]);
  assert(asset, `Missing asset ${names[index]}`);
  assert.equal(asset.state, "uploaded");
  assert.equal(asset.size, statSync(files[index]).size);
  assert.equal(asset.digest, hashes[index], "Release installer checksum mismatch");
}
gh("release", "edit", tag, "--repo", repo, "--draft=false", "--latest");
assert.equal(api("releases/latest").tag_name, tag);
console.log(JSON.stringify({ tag, sourceCommit: sha, assets: release.assets.map(({ name, size, digest }) => ({ name, size, digest })), published: true }, null, 2));
