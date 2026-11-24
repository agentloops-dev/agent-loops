// The choices that were this browser's own go to the Mac once (#6803, #6806).
import { test } from "node:test";
import assert from "node:assert/strict";
import { load } from "./load.mjs";

function storage(kept) {
  const held = new Map(Object.entries(kept));
  return { held, getItem: (k) => held.get(k) ?? null, removeItem: (k) => held.delete(k) };
}

test("a kept choice is sent when the Mac has none, and goes when the Mac has it", async () => {
  const m = await load("test/preferences-entry.ts");
  const sent = [];
  let answer = () => Promise.reject(new Error("offline"));
  m.sendPreferencesWith((change) => { sent.push(change); return answer(change); }, () => {});
  const kept = storage({ "agents.reviewDiffLayout": "sideBySide", "catalog.personas.marketplace": "mine" });
  m.preferencesSaid({ "catalog.personas.marketplace": "theirs" });
  m.moveOnce(kept);
  assert.deepEqual(sent, [{ reviewDiffLayout: "sideBySide" }], "the Mac's own choice wins");
  assert.deepEqual([...kept.held.keys()], ["agents.reviewDiffLayout"]);
  m.moveOnce(kept);
  assert.equal(sent.length, 1, "on its way: not sent again");
  await new Promise(setImmediate);
  assert.equal(kept.held.size, 1, "not taken: kept, and sent the next time");
  answer = (change) => Promise.resolve({ "catalog.personas.marketplace": "theirs", ...change });
  m.moveOnce(kept);
  await new Promise(setImmediate);
  m.moveOnce(kept);
  assert.equal(sent.length, 2);
  assert.equal(kept.held.size, 0);
  assert.equal(m.stringPreference("reviewDiffLayout"), "sideBySide");
});
