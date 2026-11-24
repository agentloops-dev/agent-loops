// A warm pool's card, its form and New Machine's Start from, in words (#5354, boards 1, 4 and 5): the same words as the
// window's WarmPoolWords (WarmPoolWordsTests), and the Machines group leaving a pool's machines out.
import { test } from "node:test";
import assert from "node:assert/strict";
import { load } from "./load.mjs";

const w = await load("src/model/warmPools.ts");
const { makingRows } = await load("src/model/machineRow.ts");

const now = new Date(Date.UTC(2027, 0, 1));
const wire = (date) => (date.getTime() - Date.UTC(2001, 0, 1)) / 1000;
const pool = (more = {}) => ({ id: "p", name: "dev-large", scope: "user", provider: "do", size: "large", region: "fra1",
  runtimes: ["claude", "codex"], projects: ["/Users/me/Agents", "/Users/me/website"], minReady: 2, maxSize: 4, target: 2,
  state: "keeping", ready: 2, settingUp: 1, claims: { count: 41, empty: 3, medianSeconds: 22 }, ...more });

test("the card says what the pool holds", () => {
  assert.equal(w.poolChip(pool()), "Healthy");
  assert.equal(w.poolChip(pool({ ready: 1 })), "Filling");
  assert.equal(w.poolChip(pool({ state: "stopped" })), "Stopped");
  assert.equal(w.poolSummary(pool()), "large · fra1 · claude, codex · projects Agents, website · Agents kept at the latest release");
  assert.equal(w.readyLine(pool()), "ready (min 2, max 4)");
  assert.equal(w.claimsLine(pool().claims), "41 claims · median 22 s · 3 found it empty");
  assert.equal(w.claimsLine(undefined), "No claims yet");
  assert.equal(w.poolStateLine(pool()), null);
  assert.equal(w.poolStateLine(pool({ waitingBecause: "Mac is offline, and it has app" })), "Waiting to make a machine: Mac is offline, and it has app.");
  assert.match(w.poolStateLine(pool({ state: "off-hours" })), /^Outside its hours/);
  assert.equal(w.poolStateLine(pool({ state: "stopped", stoppedBecause: "DigitalOcean refused the size" })),
    "Stopped: DigitalOcean refused the size. Save the pool to try again.");
});

test("each machine is a line", () => {
  const made = wire(new Date(now.getTime() - (2 * 3600 + 38 * 60) * 1000));
  assert.equal(w.poolMachineLine({ name: "pool-a", state: "ready", madeAt: made }, now), "pool-a · ready 2 h 38 min");
  assert.equal(w.poolMachineLine({ name: "pool-c", state: "setting-up", madeAt: wire(new Date(now.getTime() - 41_000)),
    detail: "installing Claude" }, now), "pool-c · installing Claude · 41 s");
  assert.equal(w.poolMachineLine({ name: "pool-d", state: "failed", madeAt: made, detail: "quota" }, now), "pool-d · failed: quota");
});

test("costs come from the provider's price", () => {
  assert.equal(w.hourly("$0.19/hour"), 0.19);
  assert.equal(w.hourly("about $1.5 an hour"), 1.5);
  assert.equal(w.hourly(undefined), null);
  assert.equal(w.idleCost(pool(), 0.19), "Idle ones cost about $0.57 an hour");
  assert.equal(w.idleCost(pool(), null), "Cost not known");
  assert.equal(w.formCost(2, 4, 0.19), "Min 2: about $0.38 an hour · at the max of 4: $0.76 an hour");
});

test("the form refuses min above max", () => {
  assert.equal(w.formProblem("dev", 3, 2), "Min ready (3) is above max size (2).");
  assert.equal(w.formProblem(" ", 1, 2), "A warm pool needs a name.");
  assert.equal(w.formProblem("dev", 0, 0), "Max size is at least 1.");
  assert.equal(w.formProblem("dev", 0, 1), null);
});

test("the form's hours and age are the wire's", () => {
  assert.equal(w.runsOf(undefined), "always");
  assert.equal(w.runsOf([1, 2, 3, 4, 5]), "weekdays");
  assert.equal(w.runsOf([0, 1, 2, 3, 4, 5, 6]), "everyDay");
  assert.deepEqual(w.daysOf("weekdays"), [1, 2, 3, 4, 5]);
  assert.equal(w.daysOf("always"), null);
  assert.equal(w.nearestReplace(undefined), 86400);
  assert.equal(w.nearestReplace(40_000), 12 * 3600);
});

test("Start from offers pools of the same size, ready first", () => {
  const empty = pool({ id: "e", ready: 0 });
  const all = [empty, pool(), pool({ id: "s", size: "small" }), pool({ id: "x", state: "stopped" })];
  assert.deepEqual(w.matchingPools(all, "do", "large").map((p) => p.id), ["p", "e"]);
  assert.deepEqual(w.matchingPools(all, "aws", "large"), []);
  assert.equal(w.startFromLine(pool()), "2 ready · about 22 s");
  assert.equal(w.startFromLine(pool({ claims: { count: 0, empty: 0 } })), "2 ready · in seconds");
  assert.equal(w.startFromLine(empty), "none ready now · a new machine instead");
});

test("a pool's machine is known by its name, and the Machines group leaves it out", () => {
  assert.equal(w.isPoolMachine("dev-large-f00196", ["dev-large"]), true);
  assert.equal(w.isPoolMachine("dev-large-f00196", ["Dev Large!"]), true);
  assert.equal(w.isPoolMachine("pool-2026-a1b2c3", ["2026"]), true);
  assert.equal(w.isPoolMachine("dev-large-1", ["dev-large"]), false);
  assert.equal(w.isPoolMachine("machine-3-abcdef", ["dev-large"]), false);
  const providers = [{ id: "do", name: "DigitalOcean", kind: "digitalocean", capabilities: ["create", "delete"], workspaces: [
    { id: "1", name: "dev-large-f00196", state: "running" }, { id: "2", name: "machine-4", state: "running", joining: true },
  ] }];
  assert.deepEqual(makingRows([], providers, [], ["dev-large"]).map((r) => r.name), ["machine-4"]);
  assert.deepEqual(makingRows([], providers, []).map((r) => r.name), ["machine-4", "dev-large-f00196"]);
});
