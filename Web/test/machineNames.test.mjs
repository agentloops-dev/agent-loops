// A list of machine names stops at three, then "and N more" with a link (#5289).
import { test } from "node:test";
import assert from "node:assert/strict";
import { load } from "./load.mjs";

const m = await load("src/model/machineNames.ts");

test("up to three names, then the rest counted", () => {
  assert.equal(m.namesWords([]), "");
  assert.equal(m.namesWords(["a"]), "a");
  assert.equal(m.namesWords(["a", "b"]), "a, b");
  assert.equal(m.namesWords(["a", "b", "c"]), "a, b, c");
  assert.equal(m.namesWords(["a", "b", "c", "d"]), "a, b, c and 1 more");
  assert.equal(m.namesWords(Array.from({ length: 100 }, (_, i) => `m${i}`)), "m0, m1, m2 and 97 more");
});

test("the more link opens the Machines page with the set as its filter", () => {
  const four = ["a", "b", "c", "d"].map((id) => ({ id, name: id }));
  assert.equal(m.moreOf(four.slice(0, 3)), null);
  assert.deepEqual(m.moreOf(four), { count: 1, to: m.filteredTo(["a", "b", "c", "d"]) });
  assert.deepEqual(m.hostsOf(m.filteredTo(["a", "b"]).query), ["a", "b"]);
  assert.deepEqual(m.hostsOf(undefined), []);
});

test("and before the last name, up to three (#5620)", () => {
  assert.equal(m.namesAnd(["a"]), "a");
  assert.equal(m.namesAnd(["a", "b"]), "a and b");
  assert.equal(m.namesAnd(["a", "b", "c"]), "a, b and c");
  assert.equal(m.namesAnd(["a", "b", "c", "d", "e"]), "a, b, c and 2 more");
});

test("a per-machine section shows three, the open one too, then all (#5620)", () => {
  const five = ["a", "b", "c", "d", "e"].map((id) => ({ id }));
  assert.deepEqual(m.firstOf(five.slice(0, 3), false).map((x) => x.id), ["a", "b", "c"]);
  assert.deepEqual(m.firstOf(five, false).map((x) => x.id), ["a", "b", "c"]);
  assert.deepEqual(m.firstOf(five, false, "e").map((x) => x.id), ["a", "b", "c", "e"]);
  assert.deepEqual(m.firstOf(five, false, "b").map((x) => x.id), ["a", "b", "c"]);
  assert.equal(m.firstOf(five, true).length, 5);
});
