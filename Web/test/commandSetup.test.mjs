// The ⌘K menu's secrets, accounts and environment variables (#5261): the same rules as the window's CommandMenuSetup
// (CommandMenuSetup.swift, CommandMenuSetupTests): found by name, a row says the kind and the scope, never a value, and each
// opens on its page.
import { test } from "node:test";
import assert from "node:assert/strict";
import { load } from "./load.mjs";

const s = await load("src/model/commandSetup.ts");
const m = await load("src/model/commandMenu.ts");

const secrets = [
  { name: "GITHUB_TOKEN", updated: "", machines: [], hint: "ab12" },
  { name: "VISA", updated: "", machines: [], kind: "card" },
];
const accounts = [{ service: "GitHub", address: "agents@example.com", username: "octo" }];
const files = [
  { layer: "yours", path: "~/.agents/env", exists: true, variables: [{ name: "EDITOR", value: "vim", line: 1, state: "given" }] },
  { layer: "repo", path: "/src/Agents/.agents/env", folder: "/src/Agents", exists: true,
    variables: [{ name: "GITHUB_ORG", value: "secret-org-name", line: 2, state: "given" }] },
];
const items = s.setupItems(secrets, accounts, files);

test("each row says its kind and scope", () => {
  assert.deepEqual(items.map((i) => `${m.commandKindWord[i.kind]}|${i.title}|${i.detail}`), [
    "Secret|GITHUB_TOKEN|Secret · Yours",
    "Secret|VISA|Card · Yours",
    "Account|GitHub · octo|Account",
    "Environment variable|EDITOR|Environment · Yours",
    "Environment variable|GITHUB_ORG|Environment · Agents",
  ]);
});

test("a name finds them, and a value never", () => {
  assert.deepEqual(m.commandMatches(items, "github").map((i) => i.title), ["GITHUB_TOKEN", "GitHub · octo", "GITHUB_ORG"]);
  assert.deepEqual(m.commandMatches(items, "secret-org"), []);
  assert.deepEqual(m.commandMatches(items, "vim"), []);
  assert.deepEqual(m.commandMatches(items, "ab12"), []);
  assert.deepEqual(m.commandMatches(items, "agents@example").map((i) => i.title), ["GitHub · octo"], "an account's address finds it");
});

test("each opens on its page, selected, at the page's own address", () => {
  assert.deepEqual(items.map((i) => s.setupPlace(i)), [
    { section: "secrets", item: "GITHUB_TOKEN" },
    { section: "secrets", item: "VISA" },
    { section: "accounts", item: "github#agents@example.com" },
    { section: "environment", item: "yours/EDITOR" },
    { section: "environment", item: "repo//src/Agents/GITHUB_ORG" },
  ]);
  assert.equal(s.setupPlace({ id: "session:1" }), undefined);
});

test("Yours from the settings when no file was read", () => {
  assert.deepEqual(s.variableItems([], "# The editor\nEDITOR=vim\nexport PAGER='less -R'\n").map((i) => i.id),
    ["variable:yours/EDITOR", "variable:yours/PAGER"]);
  assert.deepEqual(s.variableItems(files, "OTHER=1\n").map((i) => i.title), ["EDITOR", "GITHUB_ORG"], "a file read wins");
  assert.deepEqual(s.variableItems([{ layer: "yours", path: "", exists: false, variables: [] }], "OTHER=1\n").map((i) => i.id),
    ["variable:yours/OTHER"], "an empty one does not");
});
