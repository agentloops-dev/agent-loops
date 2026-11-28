// The control plane as a machine (#6592): the same words and choices as the window's ControlPlaneMachineTests (#6591).
import { test } from "node:test";
import assert from "node:assert/strict";
import { load } from "./load.mjs";

const m = await load("src/model/controlPlaneMachine.ts");
// WireDate: seconds since 2001-01-01.
const wire = (ms) => ms / 1000 - 978_307_200;
const look = (level, extra = {}) => ({ copy: "c1", machine: "cp", level, reason: "", loadPerCore: 1.42, loadPerCore5: 1.1,
  pressureMemoryFullAvg60: 0, pressureIOFullAvg60: 16.7, memoryAvailableBytes: 512 * 1024 * 1024, swapUsedPercent: 3.4,
  heapInuseBytes: 184 * 1024 * 1024, goroutines: 1284, openFiles: 40, storeFiles: 900, storeBytes: 0, at: wire(Date.now()), ...extra });
const machine = (id, roles, name = id) => ({ id, name, roles, platform: "Linux", state: "online" });

test("the control plane's machines go first, the others in their order", () => {
  const rows = [machine("a", ["agents"]), machine("cp", ["control"]), machine("b", ["agents", "control"])];
  assert.deepEqual(m.first(rows).map((r) => r.id), ["cp", "b", "a"]);
  assert.equal(m.controlOnly(rows[1]), true);
  assert.equal(m.controlOnly(rows[2]), false);
});

test("one control-only machine is Control plane; several are told apart by name, else id", () => {
  const one = [machine("cp", ["control"])];
  assert.equal(m.titleOf(one[0], one), "Control plane");
  const two = [machine("cp1", ["control"], "box-1"), machine("cp2", ["control"], "box-2")];
  assert.equal(m.titleOf(two[1], two), "Control plane · box-2");
  const same = [machine("cp1", ["control"], "box"), machine("cp2", ["control"], "box")];
  assert.equal(m.titleOf(same[0], same), "Control plane · cp1");
  assert.equal(m.metaLine(one[0], one, "control.example.dev"), "control.example.dev · cp · Linux · Admin view");
  assert.equal(m.metaLine(two[1], two, "control.example.dev"), "Linux · Admin view");
});

test("the row's line at near says the pressures that are there, then the load", () => {
  assert.equal(m.rowLine(look("near")), "Near capacity · I/O pressure 16.7% · Load 1.4 per core");
  assert.equal(m.shortRowLine(look("near")), "Near capacity · I/O 16.7%");
  assert.equal(m.rowLine(look("ok")), "OK · Load 1.4 per core");
  assert.equal(m.shortRowLine(look("ok")), "OK · Load 1.4");
  assert.equal(m.rowLine(null), "No capacity reading yet");
  assert.equal(m.agentsRowLine(look("full")), "Control plane at capacity");
  assert.equal(m.agentsRowLine(look("ok")), null);
});

test("the sentence is the control plane's reason, and names the machine among several", () => {
  const near = look("near", { reason: "the control plane is near capacity: its disk is busy" });
  assert.equal(m.sentence(near), "The control plane is near capacity: its disk is busy.");
  assert.equal(m.sentence(near, "box-2"), "The control plane on box-2 is near capacity: its disk is busy.");
  assert.equal(m.sentence(look("full")), "The control plane is at capacity.");
  assert.equal(m.sentence(look("ok")), "The control plane is within capacity.");
});

test("the strip names the worst copy, near or full only, and none while the control plane is away", () => {
  const copies = [look("near", { copy: "a" }), look("full", { copy: "b" }), look("ok", { copy: "c" })];
  assert.equal(m.warning(copies, false).copy, "b");
  assert.equal(m.warning(copies, true), null);
  assert.equal(m.warning([look("ok")], false), null);
});

test("a copy that names no machine is the only control machine's, when it is the only copy", () => {
  const old = look("near", { machine: undefined });
  assert.equal(m.lookFor("cp", [old], 1), old);
  assert.equal(m.lookFor("cp", [old], 2), null);
  assert.equal(m.lookFor("cp", [look("ok", { machine: "other" })], 1), null);
});

test("the figures are in the wireframe's order", () => {
  assert.deepEqual(m.summary(look("near")).map((f) => [f.label, f.value]),
    [["Capacity", "Near"], ["Load per core", "1.42"], ["Memory pressure", "0.0%"], ["I/O pressure", "16.7%"]]);
  const sections = m.sections(look("ok"));
  assert.deepEqual(sections.map((s) => s.title), ["Memory", "Process", "Store"]);
  assert.equal(sections[1].figures[0].value, "1,284");
  assert.equal(sections[2].figures[1].value, "0 bytes");
});

test("no reading: one comes, the read failed, or the control plane gives none", () => {
  assert.deepEqual(m.noReadingWords(false, null), { line: "No capacity reading yet", detail: m.noReadingDetail });
  assert.deepEqual(m.noReadingWords(true, null), { line: "No capacity reading", detail: m.readFailedDetail });
  assert.deepEqual(m.noReadingWords(false, 15_000), { line: "No capacity reading", detail: m.emptyDetail });
});

test("a board keeps the newest look of each copy, and a notice moves one", () => {
  const now = Date.now();
  m.takeBoard([look("ok", { at: wire(now - 60_000) }), look("near", { at: wire(now) })], now);
  assert.equal(m.looks().length, 1);
  assert.equal(m.looks()[0].level, "near");
  assert.equal(m.readAt.value, now);
  m.takeLook(look("full"));
  assert.equal(m.looks()[0].level, "full");
  const rows = [machine("cp", ["control"])];
  assert.equal(m.runsCopy(rows[0], rows), true);
  assert.equal(m.lookOn("cp", rows).level, "full");
});
