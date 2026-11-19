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

test("a row's menu offers Priority and On hold where its forge sets them, on an issue, when its machine writes (epic #6086)", () => {
  const github = { forge: "github", priority: true, hold: true };
  const jira = { forge: "jira", priority: true };
  const issue = { node: { kind: "issue", ref: "github:alexec/Agents#1" } };
  assert.deepEqual(o.rowSetsPriority(issue, [jira, github], true), { priority: true, hold: true });
  assert.deepEqual(o.rowSetsPriority(issue, [github], false), { priority: false, hold: false });
  assert.deepEqual(o.rowSetsPriority(issue, [jira], true), { priority: false, hold: false });
  assert.deepEqual(o.rowSetsPriority(issue, undefined, true), { priority: false, hold: false });
  assert.deepEqual(o.rowSetsPriority({ node: { kind: "pr", ref: "github:alexec/Agents#2" } }, [github], true), { priority: false, hold: false });
  assert.deepEqual(o.rowSetsPriority({ node: { kind: "issue", ref: "jira:PROJ-7" } }, [github, jira], true), { priority: true, hold: false });
});

test("a list's next page keeps each forge's capabilities, and adds one the first page did not name (epic #6086)", () => {
  const github = { forge: "github", priority: true };
  const gitlab = { forge: "gitlab", hold: true };
  assert.equal(o.joinForges(undefined, undefined), undefined);
  assert.deepEqual(o.joinForges([github], undefined), [github]);
  assert.deepEqual(o.joinForges(undefined, [gitlab]), [gitlab]);
  assert.deepEqual(o.joinForges([github], [{ forge: "github" }, gitlab]), [github, gitlab]);
});
