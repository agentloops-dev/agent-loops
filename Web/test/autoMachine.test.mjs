// New Session's Auto row (#5375): offered, usable and chosen by the control plane's pick.
import { test } from "node:test";
import assert from "node:assert/strict";
import { load } from "./load.mjs";

const a = await load("test/autoMachine.ts");
const pick = { machine: "m1", machineName: "Mac mini", text: "Auto chose Mac mini: you last started here." };
const none = { none: "noFolder", text: "No machine has this project. Add a folder on one." };

test("auto: a pick is offered, usable and chosen until a machine is named", () => {
  assert.equal(a.autoOffered(pick), true);
  assert.equal(a.autoUsable(pick), true);
  assert.equal(a.autoIsChosen("h|f", pick), true);
  assert.equal(a.autoRowWords(pick), "Auto · Mac mini");
  a.namedMachine.value = "h|f";
  assert.equal(a.autoIsChosen("h|f", pick), false);
  assert.equal(a.autoIsChosen("h|g", pick), true);
  a.namedMachine.value = undefined;
});

test("auto: one machine has no row; none fits greys it with the control plane's words", () => {
  assert.equal(a.autoOffered({ single: true }), false);
  assert.equal(a.autoOffered(null), false);
  assert.equal(a.autoOffered(none), true);
  assert.equal(a.autoUsable(none), false);
  assert.equal(a.autoIsChosen("h|f", none), false);
  assert.equal(a.autoOffWords(none), none.text);
  assert.equal(a.autoOffWords(pick), undefined);
});
