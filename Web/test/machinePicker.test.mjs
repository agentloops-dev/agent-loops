// The one machine picker (#5287): the query, the sections (pinned, recent, then groups by label) and what it keeps.
import { test } from "node:test";
import assert from "node:assert/strict";
import { load } from "./load.mjs";

const m = await load("src/model/machinePicker.ts");
const made = (id, state, labels = [], autoLabels = []) => ({ id, name: id, state, labels, autoLabels });

test("the query asks for a page by name, with the words when there are some", () => {
  assert.deepEqual(m.pickerQuery(""), { sort: "name", limit: 30 });
  assert.deepEqual(m.pickerQuery("  gpu "), { sort: "name", limit: 30, query: "gpu" });
});

test("sections: pinned, recent, then a group for each label, then the rest; a machine once", () => {
  const page = [made("a", "online", ["team=x", "os=linux"], ["os=linux"]), made("b", "online", ["gpu"]), made("c", "stopped"),
    made("d", "online", ["team=y"]), made("e", "offline", ["os=linux"], ["os=linux"])];
  const s = m.sections(page, ["c", "zz"], ["d", "c"]);
  assert.deepEqual(s.map((x) => [x.title, x.machines.map((h) => h.id)]),
    [["Pinned", ["c"]], ["Recent", ["d"]], ["gpu", ["b"]], ["team", ["a"]], ["Other machines", ["e"]]]);
});

test("with no labels at all the rest is just Machines, and skip leaves a machine out", () => {
  const s = m.sections([made("a", "online"), made("b", "online")], [], [], (h) => h.id === "a");
  assert.deepEqual(s.map((x) => [x.title, x.machines.map((h) => h.id)]), [["Machines", ["b"]]]);
});

test("the dot says the state", () => {
  assert.equal(m.pickerDot(made("a", "online")), "online");
  assert.equal(m.pickerDot(made("a", "needsUpdate")), "attention");
  assert.equal(m.pickerDot(made("a", "stopped")), "stopped");
  assert.equal(m.pickerDot(made("a", "offline")), "offline");
});

test("recent keeps the last five, newest first; a pin toggles", () => {
  const store = { v: {}, getItem(k) { return this.v[k] ?? null; }, setItem(k, x) { this.v[k] = x; } };
  for (const id of ["1", "2", "3", "4", "5", "6", "3"]) m.rememberMachine(store, id);
  assert.deepEqual(m.recentMachines(store), ["3", "6", "5", "4", "2"]);
  assert.deepEqual(m.togglePin(store, "a"), ["a"]);
  assert.deepEqual(m.togglePin(store, "a"), []);
  assert.deepEqual(m.pinnedMachines(undefined), []);
});

test("a pin past the most is refused, and one whose machine is gone goes (#6797)", () => {
  const store = { v: {}, getItem(k) { return this.v[k] ?? null; }, setItem(k, x) { this.v[k] = x; } };
  for (let i = 0; i < m.pinnedMost; i++) assert.ok(m.togglePin(store, `m${i}`));
  assert.equal(m.togglePin(store, "over"), null);
  assert.equal(m.pinnedMachines(store).length, m.pinnedMost);
  assert.deepEqual(m.keepPins(store, (id) => id === "m1" || id === "m2"), ["m1", "m2"]);
  assert.deepEqual(m.pinnedMachines(store), ["m1", "m2"]);
  assert.equal(m.togglePin(store, "a".repeat(65)), null);
  assert.deepEqual(m.togglePin(store, "a".repeat(64)), ["m1", "m2", "a".repeat(64)]);
});
