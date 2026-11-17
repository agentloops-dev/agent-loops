// The Files changed page's words and list (#5477, specs/pr-files-changed-page/spec.md).
import { test } from "node:test";
import assert from "node:assert/strict";
import { load } from "./load.mjs";

const p = await load("src/model/filesPage.ts");
const file = (path, additions = 1, deletions = 0, extra = {}) => ({ path, change: "modified", additions, deletions, ...extra });

test("the header counts the files and the lines", () => {
  const files = [file("a/b.ts", 10, 2), file("c.md", 2, 16)];
  assert.equal(p.filesCounts(files), "2 files · +12 −18");
  assert.equal(p.filesCountsSpoken(files), "2 files, 12 lines added, 18 removed");
  assert.equal(p.filesCounts([file("c.md")]), "1 file · +1 −0");
  assert.equal(p.filesCounts([]), "0 files · +0 −0");
});

test("a row shows the name, with the folder apart", () => {
  assert.deepEqual(p.nameAndFolder("Web/src/views/Work.tsx"), { name: "Work.tsx", folder: "Web/src/views" });
  assert.deepEqual(p.nameAndFolder("README.md"), { name: "README.md", folder: "" });
});

test("a rename shows both paths, and a modified file no change word", () => {
  assert.equal(p.cardPath(file("b.ts", 0, 0, { previousPath: "a.ts", change: "renamed" })), "a.ts → b.ts");
  assert.equal(p.cardPath(file("b.ts")), "b.ts");
  assert.equal(p.changeWord(file("b.ts")), undefined);
  assert.equal(p.changeWord(file("b.ts", 1, 0, { change: "added" })), "added");
});

test("the filter matches every word in the path, any case, the old path too", () => {
  const files = [file("Web/src/views/Work.tsx"), file("App/Sources/Work.swift"), file("new.ts", 0, 0, { previousPath: "Web/old.ts" })];
  assert.deepEqual(p.matchFiles(files, ""), [0, 1, 2]);
  assert.deepEqual(p.matchFiles(files, "work"), [0, 1]);
  assert.deepEqual(p.matchFiles(files, "web TSX"), [0]);
  assert.deepEqual(p.matchFiles(files, "old"), [2]);
  assert.deepEqual(p.matchFiles(files, "nothing"), []);
});

test("Collapse all turns into Expand all when every card is collapsed", () => {
  assert.equal(p.collapseWords(3, 0), "Collapse all");
  assert.equal(p.collapseWords(3, 2), "Collapse all");
  assert.equal(p.collapseWords(3, 3), "Expand all");
  assert.equal(p.collapseWords(0, 0), "Collapse all");
});
