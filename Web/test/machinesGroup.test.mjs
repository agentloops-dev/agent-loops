// The sidebar's Machines group (#5286): the same cases as MachinesGroupTests in AgentsKitCore.
import { test } from "node:test";
import assert from "node:assert/strict";
import { load } from "./load.mjs";

const g = await load("src/model/machinesGroup.ts");
const m = (id, pinned = false, active = false) => ({ id, pinned, active });

test("seven or fewer are all shown in their order, with no All Machines row", () => {
  const rows = g.machinesGroupRows([m("mac"), m("a"), m("b", false, true), m("c", true)], 2, "mac");
  assert.deepEqual(rows, { machines: ["mac", "a", "b", "c"], making: 2, total: 6, bounded: false });
  assert.equal(g.machinesGroupRows(Array.from({ length: 7 }, (_, i) => m(`m${i}`)), 0, "mac").bounded, false);
});

test("more than seven: This Mac, then pinned, then active, then the rest", () => {
  const list = [m("mac"), ...Array.from({ length: 20 }, (_, i) => m(`m${i}`, i === 15, i === 3 || i === 12))];
  const rows = g.machinesGroupRows(list, 0, "mac");
  assert.deepEqual(rows.machines, ["mac", "m15", "m3", "m12", "m0", "m1", "m2"]);
  assert.equal(rows.total, 21);
  assert.equal(rows.bounded, true);
  assert.equal(g.machinesGroupWords.all(rows.total), "All Machines (21)");
});

test("machines being made come after the active ones and count", () => {
  const list = [m("mac"), ...Array.from({ length: 10 }, (_, i) => m(`m${i}`, false, i === 9))];
  const rows = g.machinesGroupRows(list, 2, "mac");
  assert.deepEqual(rows.machines, ["mac", "m9", "m0", "m1", "m2"]);
  assert.equal(rows.making, 2);
  assert.equal(rows.total, 13);
});

test("never more than seven, also when many are pinned or active", () => {
  const rows = g.machinesGroupRows(Array.from({ length: 30 }, (_, i) => m(`m${i}`, i < 5, true)), 3, "mac");
  assert.equal(rows.machines.length, 7);
  assert.equal(rows.making, 0);
  assert.deepEqual(rows.machines.slice(0, 5), ["m0", "m1", "m2", "m3", "m4"]);
});
