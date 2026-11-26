// The commitTrailers rule's words and form (#6365, #6375): the keys, kinds and defaults of
// Go/internal/policylayers/committrailers.go.
import { test } from "node:test";
import assert from "node:assert/strict";
import { load } from "./load.mjs";

const c = await load("src/model/commitTrailers.ts");

test("the keys and defaults are the daemon's: every trailer on, Cost off", () => {
  assert.deepEqual(c.trailers.map((t) => t.key).sort(), ["Approved-by", "Cost", "Duration", "Epic", "Fixes",
    "Managed-by", "Model", "Requested-by", "Runtime", "Session", "Tokens"]);
  assert.deepEqual(c.trailers.filter((t) => !t.on).map((t) => t.key), ["Cost"]);
  assert.deepEqual(c.trailers.filter((t) => t.kind === "spend").map((t) => t.key), ["Cost", "Tokens", "Duration"]);
});

test("Co-authored-by is the runtime's, and an old layer's Co-authored-by and Made-by are kept, with no switch (#7174)", () => {
  assert.equal(c.trailers.some((t) => t.key === "Co-authored-by" || t.key === "Made-by"), false);
  assert.deepEqual(c.ruleOf(c.choicesOf({ "Co-authored-by": "off", "Made-by": "on" }), { "Co-authored-by": "off", "Made-by": "on" }),
    { "Co-authored-by": "off", "Made-by": "on" });
});

test("on binds a record trailer, off a spend trailer", () => {
  const made = c.trailers.find((t) => t.key === "Runtime"), cost = c.trailers.find((t) => t.key === "Cost");
  assert.equal(c.binds(made, "on"), true);
  assert.equal(c.binds(made, "off"), false);
  assert.equal(c.binds(cost, "off"), true);
  assert.equal(c.notSetName(cost), "Not set (Off)");
  assert.equal(c.notSetName(made), "Not set (On)");
});

test("not set keys are left out, and none set is no rule", () => {
  assert.equal(c.ruleOf(c.choicesOf(undefined), undefined), undefined);
  assert.deepEqual(c.ruleOf({ ...c.choicesOf(undefined), Epic: "off" }, { Epic: "on", Odd: "off" }), { Odd: "off", Epic: "off" });
  assert.equal(c.choicesOf({ Epic: "maybe" }).Epic, "");
});
