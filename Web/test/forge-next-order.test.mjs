// The order of forge/next on the Work pages and the On hold chip's words (#6093), as ForgeNextOrder.swift and
// WorkPlan's on the window (#6087, #6090, #6092).
import { test } from "node:test";
import assert from "node:assert/strict";
import { load } from "./load.mjs";

const o = await load("src/model/forgeNextOrder.ts");

test("the rank: not on hold first, then High, Medium, none, Low", () => {
  assert.deepEqual([{ priority: "high" }, { priority: "medium" }, {}, { priority: "low" }, { priority: "high", onHold: true }, { onHold: true }]
    .map(o.nextRank), [0, 1, 2, 3, 4, 6]);
});

test("a row with the On hold chip does not draw the on hold label again", () => {
  const labels = ["bug", "on hold", "On-Hold", "on_hold", "story"].map((name) => ({ name }));
  assert.deepEqual(o.shownLabels(labels, true).map((l) => l.name), ["bug", "story"]);
  assert.equal(o.shownLabels(labels, false).length, 5);
  assert.equal(o.onHoldWords, "On hold");
  assert.equal(o.onHoldHelp, "On hold: not picked up next");
});

test("the Priority section: its labels, its choices and where it writes (#6092)", () => {
  const labels = ["bug", "Priority:High", "priority:low", "priority:urgent", "on hold", "On-Hold", "story"].map((name) => ({ name }));
  assert.deepEqual(o.sideLabels(labels).map((l) => l.name), ["bug", "priority:urgent", "story"]);
  assert.deepEqual(o.priorityChoices.map(o.priorityChoiceWords), ["High", "Medium", "None", "Low"]);
  assert.deepEqual(o.setsPriority(["label", "priority", "hold"], true), { priority: true, hold: true });
  assert.deepEqual(o.setsPriority(["label", "priority", "hold"], false), { priority: false, hold: false });
  assert.deepEqual(o.setsPriority(["priority"], true), { priority: true, hold: false });
  assert.deepEqual(o.setsPriority(undefined, true), { priority: false, hold: false });
});
