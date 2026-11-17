// A session's priority chip and Priority ▸ menu (#5849), as SessionPriorityWordsTests has them on the window.
import { test } from "node:test";
import assert from "node:assert/strict";
import { load } from "./load.mjs";

const p = await load("src/model/sessionPriority.ts");

const agent = (priority, { set, source, labels = [] } = {}) => ({
  priority, prioritySet: set, prioritySource: source, labels: labels.map((value) => ({ value, owner: "agent", addedAt: "" })),
});

test("a chip for high and low only", () => {
  assert.equal(p.priorityChip(agent("high")), "High");
  assert.equal(p.priorityChip(agent("low")), "Low");
  assert.equal(p.priorityChip(agent("medium")), null);
  assert.equal(p.priorityChip(agent(undefined)), null);
});

test("its help says where it comes from", () => {
  assert.equal(p.priorityHelp(agent("high", { source: "issue", labels: ["lane", "#5849"] })), "High, from #5849");
  assert.equal(p.priorityHelp(agent("high", { source: "issue", labels: ["P#5895"] })), "High, from P#5895");
  assert.equal(p.priorityHelp(agent("high", { source: "issue" })), "High, from its issue");
  assert.equal(p.priorityHelp(agent("low", { source: "starter" })), "Low, from the session that started it");
  assert.equal(p.priorityHelp(agent("high", { set: "high", source: "set" })), "High, set on this session");
});

test("the menu follows the issue, or none", () => {
  const none = agent(undefined);
  assert.equal(p.priorityTitle("follow", none), "None");
  assert.equal(p.priorityChecked(none), "follow");
  const fromIssue = agent("high", { source: "issue", labels: ["#5849"] });
  assert.equal(p.priorityTitle("follow", fromIssue), "From issue (High)");
  const setLow = agent("low", { set: "low", source: "set", labels: ["#5849"] });
  assert.equal(p.priorityChecked(setLow), "low");
  assert.deepEqual(p.priorityChoices.map((c) => p.priorityTitle(c, setLow)), ["High", "Medium", "Low", "From issue"]);
});
