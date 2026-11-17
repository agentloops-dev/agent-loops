// The Environment page (#5260): the same rules as the window's EnvironmentPage and EnvFileText (EnvironmentPage.swift), and
// the edit rule of Go/internal/envfile's Edit.
import { test } from "node:test";
import assert from "node:assert/strict";
import { load } from "./load.mjs";

const p = await load("src/model/environmentPage.ts");

const file = "# The region for the deploy scripts.\nAWS_REGION=eu-west-2\n\n# Go\nGOFLAGS=-mod=mod\nexport EDITOR='code --wait'\n";

test("edit adds a variable at the end, after a blank line, with its description", () => {
  assert.equal(p.edit(file, "PORT", "8081", "The port."), file + "\n# The port.\nPORT=8081\n");
  assert.equal(p.edit("", "PORT", "8081", null), "PORT=8081\n");
  assert.equal(p.edit("A=1", "B", "two words", ""), "A=1\n\nB='two words'\n");
  assert.equal(p.edit("", "Q", "it's", null), "Q=\"it's\"\n", "a value with ' is in double quotes");
});

test("edit replaces a value and keeps every other line and comment", () => {
  assert.equal(p.edit(file, "AWS_REGION", "us-east-1", null), file.replace("eu-west-2", "us-east-1"), "nil keeps the description");
  assert.equal(p.edit(file, "AWS_REGION", "us-east-1", "Where."), file.replace("eu-west-2", "us-east-1").replace("The region for the deploy scripts.", "Where."));
  assert.equal(p.edit(file, "AWS_REGION", "us-east-1", ""), file.replace("# The region for the deploy scripts.\n", "").replace("eu-west-2", "us-east-1"));
  assert.equal(p.edit("A=1\nA=2\n", "A", "3", null), "A=3\n", "the last of a name is the one kept");
  assert.equal(p.edit("A=1\r\nB=2\r\n", "B", "3", null), "A=1\nB=3\n");
});

test("edit removes a variable with its description, leaving one blank line", () => {
  assert.equal(p.edit(file, "AWS_REGION", null, null), "# Go\nGOFLAGS=-mod=mod\nexport EDITOR='code --wait'\n");
  assert.equal(p.edit("A=1\n\n# b\nB=2\n\nC=3\n", "B", null, null), "A=1\n\nC=3\n");
  assert.equal(p.edit("A=1\n\nB=2\n", "B", null, null), "A=1\n", "no blank lines at the end");
  assert.equal(p.edit("A=1\n", "A", null, null), "");
  assert.equal(p.edit("A=1\n", "Z", null, null), "A=1\n", "a name not there changes nothing");
});

test("the text of a file's variables, by line, with descriptions", () => {
  assert.equal(p.textOf([{ name: "B", value: "x y", line: 4, state: "given" }, { name: "A", value: "1", description: "One", line: 2, state: "given" }]),
    "# One\nA=1\n\nB='x y'\n");
  assert.equal(p.textOf([]), "");
});

test("names, and the refusals before a write", () => {
  assert.equal(p.isName("AWS_REGION"), true);
  assert.equal(p.isName("1A"), false);
  assert.equal(p.refusal("", "x"), null);
  assert.match(p.refusal("1A", "x"), /letters, digits and _/);
  assert.equal(p.refusal("GITHUB_TOKEN", "abc"), p.w.looksSecret);
  assert.equal(p.refusal("GITHUB_TOKEN", ""), null, "a name with no value is not a secret");
  assert.equal(p.refusal("PORT", "x".repeat(4097)), "The value is over 4 KB.");
  assert.equal(p.refusedAsSecret("The value of X looks like a token."), true);
  assert.equal(p.refusedAsSecret(p.w.looksSecret), true);
  assert.equal(p.refusedAsSecret("Something else."), false);
  assert.equal(p.refusedAsSecret(null), false);
});

const v = (name, value, extra = {}) => ({ name, value, line: 1, state: "given", ...extra });
const files = [
  { layer: "yours", path: "/Users/me/.agents/env", exists: true, variables: [v("EDITOR", "code --wait"), v("AWS_REGION", "us-east-1")] },
  { layer: "repo", path: "/src/Agents/.agents/env", folder: "/src/Agents", exists: true,
    variables: [v("GOFLAGS", "-mod=mod", { description: "Go modules" }), v("AWS_REGION", "eu-west-2", { description: "The region" })] },
  { layer: "repo", path: "/src/shop/.agents/env", folder: "/src/shop", exists: true, uncommitted: true,
    variables: [v("HTTP_PROXY", "x", { state: "policyBlocked" }), v("NODE_OPTIONS", "--x", { state: "refused" }), v("PORT", "8081")] },
  { layer: "repo", path: "/src/big/.agents/env", folder: "/src/big", exists: true, variables: [], problem: "The file is over 64 KB, so it is not read." },
];

test("the groups: Needs you on top, then Yours, then a group per repo, each by name", () => {
  const g = p.groups(files, "", "all");
  assert.deepEqual(g.map((x) => x.title), ["Needs you", "Yours", "Repo · Agents", "Repo · shop", "Repo · big"]);
  assert.deepEqual(g[0].items.map((i) => i.name), ["HTTP_PROXY", "NODE_OPTIONS"]);
  assert.equal(g[0].items[0].needs, "Blocked by your organisation’s policy.");
  assert.equal(g[0].items[1].needs, "A repo cannot set this. Not given.");
  assert.deepEqual(g[1].items.map((i) => i.name), ["AWS_REGION", "EDITOR"]);
  assert.equal(g[1].file, "~/.agents/env, every machine");
  assert.equal(g[3].uncommitted, true);
  assert.equal(g[4].problem, "The file is over 64 KB, so it is not read.");
  assert.deepEqual(p.groups(files, "", "needsYou").map((x) => x.title), ["Needs you"]);
  assert.deepEqual(p.groups(files, "", "yours").map((x) => x.title), ["Yours"]);
  assert.deepEqual(p.groups(files, "", "repos").map((x) => x.title), ["Needs you", "Repo · Agents", "Repo · shop", "Repo · big"]);
  assert.deepEqual(p.groups(files, "port", "all").map((x) => x.title), ["Repo · shop"], "a search hides a file's problem");
});

test("the search matches names, values and descriptions; the counts follow it", () => {
  const all = p.items(files);
  assert.deepEqual(p.counts(all, ""), { all: 7, yours: 2, repos: 5, needsYou: 2 });
  assert.deepEqual(p.counts(all, "region"), { all: 2, yours: 1, repos: 1, needsYou: 0 }, "a name, and a description");
  assert.deepEqual(p.counts(all, "WAIT"), { all: 1, yours: 1, repos: 0, needsYou: 0 }, "a value, in any case");
  assert.deepEqual(p.counts(all, "go modules"), { all: 1, yours: 0, repos: 1, needsYou: 0 });
});

test("the detail's lines: layer, Also set, and the ids", () => {
  const all = p.items(files);
  const repo = all.find((i) => i.id === "repo//src/Agents/AWS_REGION");
  const yours = all.find((i) => i.id === "yours/AWS_REGION");
  assert.equal(p.layerLine(repo), "Repo · Agents (.agents/env)");
  assert.equal(p.layerLine(yours), "Yours (~/.agents/env, every machine)");
  assert.deepEqual(p.alsoSet(repo, all), ["Yours: us-east-1", "Yours wins: sessions get us-east-1"]);
  assert.deepEqual(p.alsoSet(yours, all), ["Repo · Agents: eu-west-2", "Yours wins over a repo’s"]);
  assert.equal(p.subtitle(repo), "Repo · Agents");
  assert.equal(p.layerShort(yours), "Yours");
  assert.equal(p.repoName("/src/Agents/"), "Agents");
  assert.equal(p.needs({ state: "secretLooking" }, "repo"), "This is in the repo, so it is public. Revoke it, then add it to Secrets.");
  assert.equal(p.needs({ state: "secretLooking" }, "yours"), "This looks like a secret. Move it to Secrets.");
});

test("as a session sees it", () => {
  assert.equal(p.resolvedSource({ name: "A", value: "1", layer: "yours", over: ["repo"], state: "given" }, "web"), "Yours (wins over the repo)");
  assert.equal(p.resolvedSource({ name: "A", value: "1", layer: "repo", state: "given" }, "web"), "Repo web");
  assert.equal(p.resolvedSource({ name: "A", state: "policyBlocked" }, "web"), "Blocked by policy");
  assert.equal(p.resolvedSource({ name: "A", state: "refused", reason: "A repo cannot set this" }, "web"), "A repo cannot set this");
  assert.equal(p.asSessionTitle("/src/agents-web", "devbox"), "As a session sees it: agents-web on devbox");
  assert.equal(p.asSessionTitle("/src/agents-web"), "As a session sees it: agents-web");
  assert.equal(p.cannotReach(""), "The machine can’t be reached.");
});

test("the pickers' folders: the repo files' and the machine's, once each, as paths", () => {
  assert.deepEqual(p.pickerFolders(files, ["file:///src/zed/", "file:///src/Agents/", "/src/My%20Repo"]),
    ["/src/Agents", "/src/big", "/src/My%20Repo", "/src/shop", "/src/zed"]);
  assert.equal(p.folderPath("file:///Users/me/My%20Repo/"), "/Users/me/My Repo");
});
