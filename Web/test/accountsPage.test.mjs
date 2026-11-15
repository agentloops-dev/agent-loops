// The Accounts page (#5258): the same rules as the window's AccountsPage (AccountsPageTests.swift).
import { test } from "node:test";
import assert from "node:assert/strict";
import { load } from "./load.mjs";

const p = await load("src/model/accountsPage.ts");

const now = Date.UTC(2027, 0, 15, 12);
const ref = Date.UTC(2001, 0, 1);
const day = 86_400_000;
const wire = (msAgo) => (now - msAgo - ref) / 1000;
const dayIn = (days) => new Date(now + days * day).toISOString().slice(0, 10);
const secret = (name, machines = ["m1", "m2"], backedUp = true) => ({ name, updated: "2027-01-01T00:00:00Z", machines, hint: "ab12", backedUp });

const sample = p.items([
  { service: "GitHub", description: "Pushes, PRs and Actions", address: "alex@example.com", username: "alexec", plan: "Team",
    monthlyCostUsd: 4, secret: "GITHUB_TOKEN", projects: ["Agents", "agents-web"], lastOp: { what: "read", at: wire(7200_000) } },
  { service: "npm", description: "Publish to npm", address: "agentloops", monthlyCostUsd: 0, secret: "NPM_TOKEN", askBeforeUse: false,
    lastOp: { what: "read", at: wire(5 * day) } },
  { service: "Cloudflare", address: "agentloops@example.com", secret: "CF_API_TOKEN", tokenExpires: dayIn(6) },
  { service: "Google Cloud", address: "agentloops@example.com", monthlyCostUsd: 12, secret: "GCP_KEY", projects: ["Agents"] },
  { service: "GitHub", address: "bot@example.com", secret: "BOT_TOKEN", askBeforeUse: false },
], [secret("GITHUB_TOKEN"), secret("NPM_TOKEN"), secret("CF_API_TOKEN"), secret("GCP_KEY", ["m2"], false)],
{ m1: "mac-studio", m2: "devbox" }, now);

const names = (list) => list.map((i) => p.accountName(i.account));

test("accounts page: Needs you is on top whatever the sort, and says why", () => {
  for (const sort of p.sorts) {
    const groups = p.groups(sample, "", "all", null, null, sort);
    assert.equal(groups[0].id, "needsYou");
    assert.deepEqual(new Set(groups[0].items.map((i) => i.account.service)), new Set(["Cloudflare", "Google Cloud"]));
    assert.equal(groups.flatMap((g) => g.items).length, sample.length);
  }
  assert.equal(sample.find((i) => i.account.service === "Cloudflare").needs, `Its token expires on ${dayIn(6)}.`);
  assert.equal(sample.find((i) => i.account.service === "Google Cloud").needs, "Its key, GCP_KEY, is only on devbox. Not backed up.");
  assert.equal(sample[0].needs, null);
  assert.equal(sample[0].id, "github#alex@example.com");
});

test("accounts page: every secret on one machine is not each account's warning", () => {
  const items = p.items([{ service: "AWS", secret: "AWS_KEY" }], [secret("AWS_KEY", ["m1"], false)], {}, now);
  assert.equal(items[0].needs, null);
});

test("accounts page: search matches each field and the secret's name, every word", () => {
  const found = (search) => names(p.found(sample, search, null, null));
  assert.deepEqual(found("alexec"), ["GitHub · alexec"]);
  assert.deepEqual(found("team"), ["GitHub · alexec"]);
  assert.deepEqual(found("publish"), ["npm · agentloops"]);
  assert.deepEqual(found("gcp_key"), ["Google Cloud · agentloops@example.com"]);
  assert.deepEqual(found("agents-web"), ["GitHub · alexec"]);
  assert.deepEqual(found("github bot"), ["GitHub · bot@example.com"]);
  assert.deepEqual(found("stripe"), []);
});

test("accounts page: chips and menus add together", () => {
  assert.deepEqual(p.counts(sample, ""), { all: 5, needsYou: 2, asks: 3 });
  assert.equal(p.counts(sample, "", "agents").all, 5);
  assert.equal(p.counts(sample, "", "agents-web").all, 4);
  assert.deepEqual(p.counts(sample, "", null, "github"), { all: 2, needsYou: 0, asks: 1 });
  assert.deepEqual(p.servicesOf(sample), ["Cloudflare", "GitHub", "Google Cloud", "npm"]);
});

test("accounts page: sorts", () => {
  assert.deepEqual(names(p.sorted(sample, "service")), ["Cloudflare · agentloops@example.com", "GitHub · alexec", "GitHub · bot@example.com",
    "Google Cloud · agentloops@example.com", "npm · agentloops"]);
  assert.deepEqual(names(p.sorted(sample, "lastUsed")).slice(0, 2), ["GitHub · alexec", "npm · agentloops"]);
  assert.deepEqual(names(p.sorted(sample, "cost")).slice(0, 3), ["Google Cloud · agentloops@example.com", "GitHub · alexec", "npm · agentloops"]);
});

test("accounts page: the foot says the total of what shows", () => {
  assert.equal(p.foot(sample, 5), "5 accounts · $16 a month");
  assert.equal(p.foot(p.found(sample, "github", null, null), 5), "2 of 5 · $4 a month");
  assert.equal(p.shortCost(sample[0].account), "$4/mo");
  assert.equal(p.shortCost(sample[1].account), "free");
  assert.equal(p.planLine(sample[0].account), "Team · $4 a month");
  assert.equal(p.noMatch("stripe live"), "No account matches “stripe live”.");
});
