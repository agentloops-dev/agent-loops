// The Work footer's load states (#5971, epic #6348), as WorkLookTests has them for the Mac window and the Remote (#5968,
// #5969, #5970): what a Work page's body draws, the left of its footer, and the age of a page of several reads.
import { test } from "node:test";
import assert from "node:assert/strict";
import { load } from "./load.mjs";

const f = await load("src/model/workFooter.ts");
const words = (s) => f.footerWords(s);

test("a list with a read in flight shows placeholders, never an empty state", () => {
  assert.equal(f.listBody(false, 0, 0, true, false), "placeholders", "the first read");
  assert.equal(f.listBody(true, 12, 0, true, false), "placeholders", "a filter change reads again: no 'No closed issues' from old rows");
  assert.equal(f.listBody(true, 12, 4, true, false), "rows", "rows to show stay while it reads");
  assert.equal(f.listBody(true, 12, 0, false, false), "empty", "every read ended");
  assert.equal(f.listBody(true, 0, 0, false, true), "failed", "nothing read and a forge said why");
  assert.equal(f.listBody(true, 3, 0, false, true), "empty", "rows were read, the filters leave none");
});

test("the footer says Loading, Updating, From this device or nothing", () => {
  assert.equal(words(f.footerState(false, 0, true, false, [])), "Loading…");
  assert.equal(words(f.footerState(true, 42, true, false, [])), "Updating…");
  assert.equal(words(f.footerState(true, 42, false, true, [])), "From this device. Reading it again.");
  assert.deepEqual(f.footerState(true, 42, false, false, []), { kind: "none" });
  assert.equal(f.footerBusy(f.footerState(true, 42, true, false, [])), true);
  assert.equal(f.footerShowsRight(f.footerState(false, 0, true, false, [])), false, "no count or age before a read");
  assert.equal(f.footerShowsRight(f.footerState(true, 42, true, false, [])), true);
});

test("a failed read after a good one keeps the rows and says what failed, with every line on hover", () => {
  const one = f.footerState(true, 42, false, false, ["GitHub said 502."]);
  assert.equal(words(one), "Could not update: GitHub said 502.");
  assert.equal(f.footerFailure(one), true);
  const two = f.footerState(true, 42, false, false, ["GitHub said 502.", "GitLab said 401."]);
  assert.equal(words(two), "2 forges could not update");
  assert.equal(two.help, "GitHub said 502.\nGitLab said 401.");
  assert.equal(words(f.footerState(true, 0, false, false, ["GitHub said 401."])), "Could not read.", "nothing read: the middle says it");
  assert.equal(f.footerShowsRight(f.footerState(true, 0, false, false, ["x"])), false);
  assert.equal(words(f.footerState(true, 0, true, false, ["x"])), "Loading…", "a read again of nothing still loads");
});

test("a page of several machines says how many answered", () => {
  assert.equal(words(f.pageFooterState(true, true, false, [], 1, 3)), "Reading 1 of 3 machines…");
  assert.equal(words(f.pageFooterState(true, true, false, [], 0, 3)), "Updating…", "none answered yet");
  assert.equal(words(f.pageFooterState(true, true, false, [], 3, 3)), "Updating…", "all answered");
  assert.equal(words(f.pageFooterState(false, true, false, [], 1, 1)), "Loading…", "one machine");
  assert.equal(words(f.pageFooterState(true, false, false, ["Mac: GitHub said 502."], 3, 3)), "Could not update: Mac: GitHub said 502.");
  assert.equal(words(f.pageFooterState(false, false, false, ["GitHub said 502."])), "Could not read.");
});

test("an item's page says the older read, and a notice has no Try again", () => {
  assert.equal(f.oldestRead([2000, 1000]), 1000);
  assert.equal(f.oldestRead([2000, undefined]), undefined, "a part not read yet");
  assert.equal(f.oldestRead([]), undefined);
  const notice = { kind: "notice", words: f.noLineage };
  assert.equal(words(notice), "No machine keeps lineage.");
  assert.equal(f.footerFailure(notice), true);
  assert.equal(f.footerBusy(notice), false);
  assert.equal(f.pageCount(1, "epic"), "1 epic");
  assert.equal(f.pageCount(4, "target"), "4 targets");
});
