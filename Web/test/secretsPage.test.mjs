// The Secrets page (#5257): the same rules as the window's SecretsPage (SecretsPageTests.swift).
import { test } from "node:test";
import assert from "node:assert/strict";
import { load } from "./load.mjs";

const p = await load("src/model/secretsPage.ts");

const now = new Date(Date.UTC(2027, 0, 15, 12));
const ref = Date.UTC(2001, 0, 1);
const day = 86_400_000;
const wire = (daysAgo) => (now.getTime() - daysAgo * day - ref) / 1000;
const iso = (daysAgo) => new Date(now.getTime() - daysAgo * day).toISOString();
const secret = (name, { description, machines = ["m1", "m2"], readDaysAgo, createdDaysAgo = 1, backedUp = true, waiting } = {}) => ({
  name, updated: iso(createdDaysAgo), created: iso(createdDaysAgo), machines, hint: "ab12", backedUp, waiting, description,
  ...(readDaysAgo !== undefined ? { lastOp: { what: "read", at: wire(readDaysAgo) } } : {}),
});

const sample = p.items({
  yours: [
    secret("GITHUB_TOKEN", { description: "GitHub token for alexec", readDaysAgo: 0.1 }),
    secret("NPM_TOKEN", { description: "Publish to npm", readDaysAgo: 5 }),
    secret("AWS_SECRET_KEY", { machines: ["m2"], readDaysAgo: 12, backedUp: false }),
    secret("OLD_KEY", { readDaysAgo: 120, createdDaysAgo: 200 }),
  ],
  sessions: [{ agentID: "A1", title: "Fix the shop sign-in", name: "SHOP_ADMIN_PW", setAt: wire(1 / 72) }],
  local: [{ name: "CF_API_TOKEN", setAt: wire(3), description: "Saved by an agent" }],
  accounts: [{ service: "GitHub", address: "alex@example.com", secret: "GITHUB_TOKEN", projects: ["Agents", "agents-web"] }],
  machineNames: { m1: "mac-studio", m2: "devbox" },
  hostName: "this Mac",
}, now);

test("secrets page: Needs you is on top whatever the sort", () => {
  for (const sort of p.sorts) {
    const groups = p.groups(sample, "", "all", null, sort);
    assert.deepEqual(groups.map((g) => g.id), ["needsYou", "yours", "session", "machine"]);
    assert.deepEqual(new Set(groups[0].items.map((i) => i.name)), new Set(["AWS_SECRET_KEY", "OLD_KEY"]));
  }
  assert.equal(sample.find((i) => i.name === "AWS_SECRET_KEY").needs, "Only on devbox. Not backed up.");
  assert.equal(sample.find((i) => i.name === "OLD_KEY").needs, "Not used in 90 days.");
});

test("secrets page: last used puts the newest read first; name sorts by name", () => {
  const used = p.groups(sample, "", "yours", null, "lastUsed");
  assert.deepEqual(used.at(-1).items.map((i) => i.name), ["GITHUB_TOKEN", "NPM_TOKEN"]);
  const named = p.sorted(sample, "name").map((i) => i.name);
  assert.equal(named[0], "AWS_SECRET_KEY");
  assert.equal(named.at(-1), "SHOP_ADMIN_PW");
});

test("secrets page: search matches name, description, session, machine, project and service, never a value", () => {
  const names = (search) => new Set(sample.filter((i) => p.matches(i, search)).map((i) => i.name));
  assert.deepEqual(names("npm"), new Set(["NPM_TOKEN"]));
  assert.deepEqual(names("shop sign"), new Set(["SHOP_ADMIN_PW"]));
  assert.ok(names("devbox").has("AWS_SECRET_KEY"));
  assert.deepEqual(names("agents-web"), new Set(["GITHUB_TOKEN"]));
  assert.deepEqual(names("github"), new Set(["GITHUB_TOKEN"]));
  assert.equal(names("ab12").size, 0);
});

test("secrets page: chips and the search add together, and each chip counts what the search leaves", () => {
  assert.deepEqual(p.counts(sample, ""), { all: 6, yours: 4, session: 1, machine: 1, needsYou: 2 });
  const found = p.counts(sample, "token");
  assert.equal(found.all, 3);
  assert.equal(found.machine, 1);
  assert.equal(found.needsYou, 0);
  assert.deepEqual(p.groups(sample, "token", "machine", null, "name").map((g) => g.id), ["machine"]);
});

test("secrets page: a secret's projects are those of the accounts that use it", () => {
  assert.deepEqual(p.projectsOf(sample), ["Agents", "agents-web"]);
  assert.deepEqual(p.groups(sample, "", "all", "Agents", "name").flatMap((g) => g.items).map((i) => i.name), ["GITHUB_TOKEN"]);
});

test("secrets page: no copy for this browser needs the person", () => {
  const items = p.items({ yours: [secret("A")], noCopy: new Set(["A"]) }, now);
  assert.equal(items[0].needs, "No copy for this browser yet.");
});

test("secrets page: the detail's words", () => {
  const github = sample.find((i) => i.name === "GITHUB_TOKEN");
  assert.equal(p.whereLine(github), "Yours, on 2 machines");
  assert.equal(p.shortAge(github, now), "2 h");
  assert.equal(p.recentLine(github, now), "2 h ago: read");
  const session = sample.find((i) => i.place === "session");
  assert.equal(p.whereLine(session), "For “Fix the shop sign-in”, on this Mac only");
  assert.equal(p.subtitle(session), "Fix the shop sign-in · this Mac");
  assert.equal(p.rowHint(session), "••••");
});
