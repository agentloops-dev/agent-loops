// New Session's Suggested section's words (#5461).
import { test } from "node:test";
import assert from "node:assert/strict";
import { load } from "./load.mjs";

const m = await load("src/model/suggestedLeads.ts");
const row = (o = {}) => ({ node: { kind: "issue", ref: "github:a/b#5457" }, title: "T", folder: "file:///Users/x/Agents/", milestone: "Projects", due: "2026-11-20", done: 2, total: 5, start: { prompt: "", labels: [] }, ...o });

test("a row's detail", () => {
  assert.equal(m.suggestionDetail(row()), "#5457 · Agents · Projects, due 2026-11-20 · 2 of 5 done");
  assert.equal(m.suggestionDetail(row({ milestone: undefined, due: undefined, folder: undefined, standing: true })), "#5457 · standing");
});

test("more in Work", () => {
  assert.equal(m.moreWords(0), undefined);
  assert.equal(m.moreWords(4), "4 more in Work");
});

test("hide is kept", () => {
  const mem = new Map();
  const s = { getItem: (k) => mem.get(k) ?? null, setItem: (k, v) => mem.set(k, v), removeItem: (k) => mem.delete(k) };
  assert.equal(m.suggestedHidden(s), false);
  m.keepSuggestedHidden(s, true);
  assert.equal(m.suggestedHidden(s), true);
  m.keepSuggestedHidden(s, false);
  assert.equal(m.suggestedHidden(s), false);
});
