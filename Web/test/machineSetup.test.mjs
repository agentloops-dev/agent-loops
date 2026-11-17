// A machine's set-up as steps (#5354, epic #5353): the chip, the sidebar's grey line and each step's line, the same
// words as the window's MachineSetupWords (MachineSetupWordsTests), and the sidebar's need.
import { test } from "node:test";
import assert from "node:assert/strict";
import { load } from "./load.mjs";

const w = await load("src/model/machineSetup.ts");
const { machineNeed, machineNeedLine } = await load("src/views/OfflineStrip.tsx");

const start = 800_000_000; // a wire date: seconds since 2001
const at = (s) => new Date((Date.UTC(2001, 0, 1) / 1000 + start + s) * 1000);
const ids = ["create", "join", "install", "compare", "runtimes", "projects", "signIn", "smoke"];
function setup(done, { running = true, failed = false, readyAfter } = {}) {
  const steps = ids.map((id, i) => {
    if (i < done) return { id, state: "done", startedAt: start, endedAt: start + 10 };
    if (i === done && failed) return { id, state: "failed", startedAt: start, error: "No quota" };
    if (i === done && running) return { id, state: "running", startedAt: start };
    return { id, state: "waiting" };
  });
  return { steps, startedAt: start, ...(readyAfter !== undefined ? { readyAt: start + readyAfter } : {}) };
}

test("the chip says where the run is", () => {
  assert.equal(w.setupChip(setup(6)), "Setting up · 7 of 8");
  assert.equal(w.setupChip(setup(8, { readyAfter: 222 })), "Ready · 3 min 42 s");
  assert.equal(w.setupChip(setup(2, { failed: true })), "Set-up failed · step 3 of 8");
});

test("the sidebar line names the step", () => {
  assert.equal(w.setupSidebarLine(setup(6)), "Setting up · 7 of 8 · Sign in");
  assert.equal(w.setupSidebarLine(setup(2, { failed: true })), "Set-up failed · Install Agents");
});

test("a ready machine is not setting up, and the sidebar says a set-up first", () => {
  assert.equal(w.isSettingUp(setup(7)), true);
  assert.equal(w.isSettingUp(setup(8, { readyAfter: 5 })), false);
  assert.equal(w.isSettingUp(undefined), false);
  const row = { id: "dev", name: "dev", platform: "linux", version: "1", state: "online", reach: "direct" };
  assert.equal(machineNeed(row, true, []), null);
  const need = machineNeed({ ...row, setup: setup(3) }, false, []);
  assert.equal(need.kind, "settingUp");
  assert.equal(machineNeedLine(need, row), "Setting up · 4 of 8 · Compare digits");
});

test("a step says what it does", () => {
  assert.equal(w.stepLine({ id: "install", state: "running", detail: "Agents: 62 of 140 MB", slower: true }),
    "Agents: 62 of 140 MB · Slower than usual");
  assert.equal(w.stepLine({ id: "create", state: "failed", error: "Size not in region" }), "Size not in region");
  assert.equal(w.stepLine({ id: "smoke", state: "waiting" }), "Not yet");
  assert.equal(w.stepLine({ id: "signIn", state: "needs-you" }), "Waiting for you");
  assert.equal(w.stepName({ id: "signIn", state: "waiting" }), "Sign in to runtimes");
});

test("a step a warm pool did says pool, and step 1 since when", () => {
  const made = { id: "create", state: "done", startedAt: start, endedAt: start + 30, fromPool: true };
  assert.equal(w.stepTime(made, at(3600)), "pool");
  assert.match(w.stepLine(made, at(30 + 2 * 3600)), /^In the pool since .+ \(2 h ago\)$/);
  assert.equal(w.stepLine({ id: "runtimes", state: "done", fromPool: true }), "In the pool");
});

test("a step's time is how long it took, or has taken", () => {
  assert.equal(w.stepTime({ id: "join", state: "waiting" }), null);
  assert.equal(w.stepTime({ id: "join", state: "done", startedAt: start, endedAt: start + 42 }), "42 s");
  assert.equal(w.stepTime({ id: "join", state: "running", startedAt: start }, at(125)), "2 min 5 s");
});

test("times are short", () => {
  assert.equal(w.duration(42), "42 s");
  assert.equal(w.duration(120), "2 min");
  assert.equal(w.duration(3900), "1 h 5 min");
  assert.equal(w.duration(7200), "2 h");
});

test("the steps fold after a day", () => {
  const ready = setup(8, { readyAfter: 222 });
  assert.equal(w.foldedLine(ready, at(3600)), null);
  assert.equal(w.foldedLine(ready, at(222 + 86400)), "Set up in 3 min 42 s");
  assert.equal(w.foldedLine(setup(6), at(222 + 86400)), null);
});
