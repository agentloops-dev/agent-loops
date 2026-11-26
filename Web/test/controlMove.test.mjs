// Make This the Control Plane… (#6614): the strip's steps and words, as the window's ControlMoveTests.
import { test } from "node:test";
import assert from "node:assert/strict";
import { load } from "./load.mjs";

const m = await load("src/model/controlMove.ts");
// WireDate: seconds since 2001-01-01.
const wire = (ms) => ms / 1000 - 978_307_200;
const move = (phase, extra = {}) => ({ id: "m1", from: "copy", to: "droplet", toName: "droplet-ams3-1", phase,
  startedAt: wire(Date.now()), updatedAt: wire(Date.now()), by: "alex", ...extra });

test("the steps go in the order the move runs them", () => {
  assert.deepEqual(m.steps.map((s) => s.title), ["Check", "Receive", "Freeze", "Copy store", "Forward"]);
});

test("a running move has its step current and the earlier ones done", () => {
  assert.deepEqual(m.stepStates(move("frozen")), ["done", "done", "current", "todo", "todo"]);
  assert.equal(m.moveLine(move("copying")), "Moving the control plane to droplet-ams3-1: copying the store.");
  assert.equal(m.running(move("checking")), true);
});

test("a failed move says which step and that the old one serves", () => {
  const failed = move("failed", { failedPhase: "copying", error: "The store did not copy: no space left." });
  assert.deepEqual(m.stepStates(failed), ["done", "done", "done", "failed", "todo"]);
  assert.equal(m.moveLine(failed),
    "The move to droplet-ams3-1 stopped at “Copy store”: The store did not copy: no space left. The control plane still serves where it was.");
});

test("an ended move has every step done and says until when it forwards", () => {
  const forwarding = move("forwarding", { forwardingUntil: wire(Date.UTC(2026, 11, 21, 12)) });
  assert.ok(m.stepStates(forwarding).every((s) => s === "done"));
  assert.equal(m.moveLine(forwarding), "droplet-ams3-1 is the control plane.");
  assert.match(m.forwardingLine(forwarding, "en-GB"), /^The old address forwards to it until 21 Dec 2026\.$/);
  assert.equal(m.forwardingLine(move("done")), null);
});

test("an ended move shows for a day, and the strip leaves out one the person closed", () => {
  const now = Date.now();
  assert.equal(m.shown(move("done", { updatedAt: wire(now - 3_600_000) }), now), true);
  assert.equal(m.shown(move("done", { updatedAt: wire(now - 90_000_000) }), now), false);
  const running = move("copying");
  m.takeMoves([running]);
  assert.equal(m.stripMove()?.id, "m1");
  assert.equal(m.moveTo("droplet")?.id, "m1");
  assert.equal(m.runningMove()?.id, "m1");
  m.takeMove({ ...running, phase: "done" });
  assert.equal(m.runningMove(), null);
  m.closeMove(running);
  assert.equal(m.stripMove(), null);
});

test("the checks and the confirmation say what happens", () => {
  assert.equal(m.meetsLine({ ok: true, reasons: [], address: "ams3-1.example.dev", memoryBytes: 2 * 2 ** 30, diskFreeBytes: 40 * 2 ** 30 }),
    "It meets the checks: reachable at ams3-1.example.dev, always on, 2 GB memory, 40 GB free disk.");
  const moving = m.happens("droplet-ams3-1", "ams3-1.example.dev", "control.example.dev");
  assert.ok(moving.some((l) => l.startsWith("The address changes to ams3-1.example.dev. The old address forwards")));
  const same = m.happens("droplet-ams3-1", "control.example.dev", "control.example.dev");
  assert.ok(same.includes("The address stays control.example.dev."));
  assert.equal(same.length, 4);
});

// #7393: a control-only machine that runs no copy is the one a team moves to, so a copy says which runs it.
test("a copy, not the role, says which machine runs the control plane", () => {
  const machine = (id, roles) => ({ id, name: id, roles });
  const running = machine("vm-1", ["control"]);
  const target = machine("vm-2", ["control"]);
  const agents = machine("box", ["agents"]);
  const copies = [{ copy: "a", machine: "vm-1" }];
  assert.equal(m.runsControl(running, copies, 2), true);
  assert.equal(m.runsControl(target, copies, 2), false);
  assert.equal(m.runsControl(agents, copies, 2), false);
  // Before the copies are read, the role says it: nothing offers a move to the copy that runs.
  assert.equal(m.runsControl(target, null, 2), true);
  assert.equal(m.runsControl(agents, null, 2), false);
  // A copy from before #6591 names no machine: the only control machine runs it.
  const unnamed = [{ copy: "b" }];
  assert.equal(m.runsControl(running, unnamed, 1), true);
  assert.equal(m.runsControl(agents, unnamed, 1), false);
  assert.equal(m.runsControl(running, unnamed, 2), false);
  assert.equal(m.controlRole(machine("old", undefined)), false);
});
