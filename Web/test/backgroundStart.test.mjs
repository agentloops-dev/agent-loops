// New Session in Background (#5798): the folder it starts in, and the key that starts it.
import { test } from "node:test";
import assert from "node:assert/strict";
import { load } from "./load.mjs";

const b = await load("src/model/backgroundStart.ts");

const live = [{ host: "mac", folder: "/Users/a/one" }, { host: "mac", folder: "/Users/a/two" }, { host: "box", folder: "/home/a/two" }];

test("it starts in the folder open, on its own machine", () => {
  assert.deepEqual(b.backgroundStartFolder(live, { host: "box", folder: "/home/a/two" }), live[2]);
  assert.deepEqual(b.backgroundStartFolder(live, { host: "mac", folder: "/Users/a/two/" }), live[1]);
});

test("with no folder open, or one that has gone, it starts in the first", () => {
  assert.deepEqual(b.backgroundStartFolder(live, {}), live[0]);
  assert.deepEqual(b.backgroundStartFolder(live, { host: "mac", folder: "/gone" }), live[0]);
  assert.equal(b.backgroundStartFolder([], {}), null);
});

test("Return starts; Shift or Option and Return, or Return while composing, does not", () => {
  const key = (over) => ({ key: "Enter", shiftKey: false, altKey: false, isComposing: false, ...over });
  assert.equal(b.returnStarts(key({})), true);
  assert.equal(b.returnStarts(key({ shiftKey: true })), false);
  assert.equal(b.returnStarts(key({ altKey: true })), false);
  assert.equal(b.returnStarts(key({ isComposing: true })), false);
  assert.equal(b.returnStarts(key({ key: "a" })), false);
});
