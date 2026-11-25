// Work's cache on the web page (#6356, epic #6348), as the Mac's WorkCache (#6352): items by id, query answers as ids in
// the server's order, a refresh that keeps the pages read after page 1, a newer copy in every answer that holds it, an
// optimistic change until its write ends, the bounds, and nothing written signed out.
import { test } from "node:test";
import assert from "node:assert/strict";
import { load } from "./load.mjs";

const { WorkCache, MemoryWorkStore, queryKey, prefixBytes, webBounds } = await load("test/workCache-entry.ts");

const q = { kind: "issue", state: "open", sort: "updated" };
const item = (n, updatedAt = 1, extra = {}) => ({
  node: { kind: "issue", ref: `github:o/r#${n}` }, title: `Issue ${n}`, state: "open", labels: [], assignees: [], updatedAt, ...extra,
});
function cache(bounds = webBounds) {
  let at = 1_000;
  const c = new WorkCache(new MemoryWorkStore(), bounds, () => at);
  c.writing = true;
  return { c, tick: (ms = 1) => { at += ms; }, at: () => at };
}
const refs = (answer, host = "mac") => answer.hosts.find((h) => h.host === host).items.map((i) => i.node.ref.split("#")[1]);

test("query: one key for equal queries, whatever the order of their parts; no board is no part", () => {
  assert.equal(queryKey({ sort: "updated", state: "open", kind: "issue", board: "" }), queryKey(q));
  assert.notEqual(queryKey({ ...q, board: "b" }), queryKey(q));
});

test("answer: none until kept; then the items in the server's order, with the read time and the cursor", async () => {
  const { c } = cache();
  assert.equal(await c.answer(q, ["mac"]), undefined);
  await c.keepPage(q, "mac", 0, { items: [item(3), item(1), item(2)], cursor: "p2", total: 9 });
  const got = await c.answer(q, ["mac", "other"]);
  assert.deepEqual(refs(got), ["3", "1", "2"]);
  assert.equal(got.hosts.length, 1);
  assert.equal(got.hosts[0].cursor, "p2");
  assert.equal(got.hosts[0].total, 9);
  assert.equal(got.read, 1_000);
  assert.equal(await c.answer({ ...q, state: "closed" }, ["mac"]), undefined);
});

test("refresh: page 1 is replaced and the pages read before stay, stale, each with its own time (#6358, #6636)", async () => {
  const { c, tick } = cache();
  await c.keepPage(q, "mac", 0, { items: [item(1), item(2)], cursor: "p2" });
  tick();
  await c.keepPage(q, "mac", 1, { items: [item(3), item(4)], cursor: "p3" });
  tick();
  await c.keepPage(q, "mac", 0, { items: [item(5), item(1)], cursor: "new2" });
  const got = (await c.answer(q, ["mac"])).hosts[0];
  assert.deepEqual(got.items.map((i) => i.node.ref.split("#")[1]), ["5", "1", "2", "3", "4"]);
  assert.deepEqual(got.pages.map((p) => [p.read, !!p.stale]), [[1_002, false], [1_000, true], [1_001, true]]);
  // The next page is asked by the new page 1's cursor, never a stale one, and is kept after it.
  assert.equal(got.cursor, "new2");
  assert.equal(got.fresh, 1);
  // A page past the one after the fresh ones is not kept.
  await c.keepPage(q, "mac", 5, { items: [item(9)] });
  assert.equal((await c.answer(q, ["mac"])).hosts[0].items.length, 5);
});

test("refresh: a stale end does not stop the list; a fresh page takes out what it covers, and one at the end drops the rest (#6636)", async () => {
  const { c } = cache();
  await c.keepPage(q, "mac", 0, { items: [item(3, 30), item(2, 20)], cursor: "p2" });
  await c.keepPage(q, "mac", 1, { items: [item(1, 10)] });
  // 9 is new, and 2 has closed.
  await c.keepPage(q, "mac", 0, { items: [item(9, 90), item(3, 30)], cursor: "n2" });
  let got = (await c.answer(q, ["mac"])).hosts[0];
  assert.equal(got.cursor, "n2");
  // 2 is after the last fresh item in the server's order, so it may be on the next page: it stays, stale, until read.
  assert.deepEqual(refs({ hosts: [got] }), ["9", "3", "2", "1"]);
  await c.keepPage(q, "mac", 1, { items: [item(1, 10)] });
  got = (await c.answer(q, ["mac"])).hosts[0];
  assert.deepEqual(refs({ hosts: [got] }), ["9", "3", "1"]);
  assert.equal(got.cursor, undefined);
  assert.ok(got.pages.every((p) => !p.stale));
});

test("items by id: a newer copy shows in every answer that holds it; an older one does not undo it", async () => {
  const { c } = cache();
  const epics = { ...q, sort: "priority" };
  await c.keepPage(q, "mac", 0, { items: [item(1, 1)] });
  await c.keepPage(epics, "mac", 0, { items: [item(1, 1)] });
  await c.keepItems([item(1, 5, { title: "New" })]);
  assert.equal((await c.answer(epics, ["mac"])).hosts[0].items[0].title, "New");
  await c.keepPage(q, "mac", 0, { items: [item(1, 2, { title: "Late" })] });
  assert.equal((await c.answer(q, ["mac"])).hosts[0].items[0].title, "New");
});

test("failure: the kept pages stay, with the errors and when it failed", async () => {
  const { c, tick } = cache();
  await c.keepPage(q, "mac", 0, { items: [item(1)] });
  tick(60_000);
  await c.keepFailure(q, "mac", [{ forge: "", message: "It did not answer." }]);
  const got = await c.answer(q, ["mac"]);
  assert.deepEqual(refs(got), ["1"]);
  assert.equal(got.read, 1_000);
  assert.equal(got.hosts[0].failed, 61_000);
  assert.equal(got.hosts[0].errors[0].message, "It did not answer.");
});

test("edit: an optimistic change shows at once; a copy as old does not replace it until the write ends", async () => {
  const { c } = cache();
  await c.keepPage(q, "mac", 0, { items: [item(1, 3)] });
  await c.edit(item(1).node, (i) => ({ ...i, priority: "high" }));
  assert.equal((await c.item(item(1).node)).priority, "high");
  await c.keepItems([item(1, 3)]);
  assert.equal((await c.item(item(1).node)).priority, "high");
  c.settle(item(1).node);
  await c.keepItems([item(1, 3)]);
  assert.equal((await c.item(item(1).node)).priority, undefined);
});

test("detail: the item's page with its machine, its body and comments to their bounds, and the newest kept item", async () => {
  const { c } = cache({ ...webBounds, bodyBytes: 4, comments: 1 });
  await c.keepDetail("mac", { item: item(1, 1), body: "abcdéf", comments: [{ id: "1" }, { id: "2" }] });
  await c.keepItems([item(1, 2, { title: "Newer" })]);
  const got = await c.detail(item(1).node);
  assert.equal(got.host, "mac");
  assert.equal(got.got.body, "abcd");
  assert.equal(got.got.comments.length, 1);
  assert.equal(got.got.item.title, "Newer");
  assert.equal(await c.detail(item(2).node), undefined);
  assert.equal(prefixBytes("aé", 2), "a");
});

test("pages: a page's own answer is kept by what it asked, with when it was read; one over its bound is not", async () => {
  const { c } = cache({ ...webBounds, recordBytes: 100 });
  await c.keep("plan", "github:o/r#1", { children: [] });
  assert.deepEqual(await c.kept("plan", "github:o/r#1"), { value: { children: [] }, at: 1_000 });
  assert.equal(await c.kept("plan", "github:o/r#2"), undefined);
  await c.keep("plan", "github:o/r#1", { children: ["x".repeat(200)] });
  assert.equal(await c.kept("plan", "github:o/r#1"), undefined);
});

test("bounds: over its size, the least recently drawn answer goes with the items no other answer holds", async () => {
  const { c, tick } = cache({ ...webBounds, bytes: 1_100 });
  const other = { ...q, state: "closed" };
  await c.keepPage(q, "mac", 0, { items: [item(1), item(2), item(3)] });
  tick();
  await c.keepPage(other, "mac", 0, { items: [item(4), item(5), item(6)] });
  assert.ok(c.bytes() <= 1_100, `${c.bytes()} bytes`);
  assert.equal(await c.answer(q, ["mac"]), undefined);
  assert.deepEqual(refs(await c.answer(other, ["mac"])), ["4", "5", "6"]);
  assert.equal(await c.item(item(1).node), undefined);
});

test("signed out: nothing is written, and forget drops what is in memory", async () => {
  const store = new MemoryWorkStore();
  const c = new WorkCache(store, webBounds, () => 1);
  await c.keepPage(q, "mac", 0, { items: [item(1)] });
  assert.equal(store.held.size, 0);
  c.writing = true;
  await c.keepPage(q, "mac", 0, { items: [item(1)] });
  c.forget();
  store.held.clear();
  assert.equal(await c.item(item(1).node), undefined);
});

test("a kept answer of 300 items draws in under 10 ms once read", async () => {
  const { c } = cache();
  await c.keepPage(q, "mac", 0, { items: Array.from({ length: 300 }, (_, i) => item(i)) });
  await c.answer(q, ["mac"]);
  const start = performance.now();
  const got = await c.answer(q, ["mac"]);
  assert.equal(got.hosts[0].items.length, 300);
  assert.ok(performance.now() - start < 10);
});

const day = 24 * 60 * 60 * 1000;

test("age: each kind of record goes 90 days after it was read, and drawing it does not keep it (#6976)", async () => {
  const { c, tick } = cache();
  const closed = { ...q, state: "closed" };
  await c.keepPage(q, "mac", 0, { items: [item(1)] });
  await c.keepFailure(closed, "mac", []);
  await c.keepDetail("mac", { item: item(9), body: "body", comments: [] });
  await c.keep("plan", "a", "plan");
  tick(90 * day);
  await c.sweep();
  assert.equal((await c.stats()).oldest, 1_000, "90 days is kept");
  assert.deepEqual(refs(await c.answer(q, ["mac"])), ["1"]);
  assert.ok(await c.detail(item(9).node));
  assert.ok(await c.kept("plan", "a"));
  tick();
  // Before a sweep too, nothing old draws.
  assert.equal(await c.answer(q, ["mac"]), undefined);
  assert.equal(await c.answer(closed, ["mac"]), undefined);
  assert.equal(await c.detail(item(9).node), undefined);
  assert.equal(await c.kept("plan", "a"), undefined);
  assert.equal(await c.item(item(1).node), undefined);
  await c.sweep();
  assert.deepEqual(await c.stats(), { count: 0, bytes: 0, oldest: undefined });
  assert.deepEqual([...c.store.held.keys()], ["@index"]);
});

test("age: a write ages old records out, a page opened later does too, and no bound is over 90 days", async () => {
  const { c, tick, at } = cache({ ...webBounds, age: 400 * day });
  assert.equal(c.bounds.age, 90 * day);
  await c.keep("plan", "a", "plan");
  tick(91 * day);
  await c.keepItems([item(50)]);
  assert.deepEqual([...c.store.held.keys()].sort(), ["@index", "item issue github:o/r#50"]);
  tick(91 * day);
  const again = new WorkCache(c.store, webBounds, at);
  again.writing = true;
  await again.sweep();
  assert.deepEqual([...c.store.held.keys()], ["@index"]);
});

test("age: an answer loses only its aged pages, and their items", async () => {
  const { c, tick } = cache();
  await c.keepPage(q, "mac", 0, { items: [item(1, 30), item(2, 20)], cursor: "p2" });
  await c.keepPage(q, "mac", 1, { items: [item(3, 10)], cursor: "p3" });
  tick(60 * day);
  await c.keepPage(q, "mac", 0, { items: [item(1, 30), item(2, 20)], cursor: "p2" });
  assert.deepEqual(refs(await c.answer(q, ["mac"])), ["1", "2", "3"]);
  tick(31 * day);
  await c.sweep();
  const got = await c.answer(q, ["mac"]);
  assert.deepEqual(refs(got), ["1", "2"]);
  assert.equal(got.hosts[0].cursor, "p2");
  assert.equal(await c.item(item(3).node), undefined);
  assert.ok(!c.store.held.has("item issue github:o/r#3"));
});

test("age: rows from before #6976 take their records' own read time", async () => {
  const { c, tick, at } = cache();
  await c.keepPage(q, "mac", 0, { items: [item(1)] });
  await c.keep("plan", "a", "plan");
  for (const row of Object.values(c.store.held.get("@index"))) delete row.read;
  const again = new WorkCache(c.store, webBounds, at);
  again.writing = true;
  tick(day);
  await again.sweep();
  assert.deepEqual(refs(await again.answer(q, ["mac"])), ["1"]);
  assert.equal((await again.stats()).oldest, 1_000);
  assert.ok(Object.entries(c.store.held.get("@index")).filter(([k]) => !k.startsWith("item ")).every(([, r]) => r.read === 1_000));
});

test("tabs: one does not take the other's records out of the index; a record no index names goes", async () => {
  const { c, at } = cache();
  const other = new WorkCache(c.store, webBounds, at);
  other.writing = true;
  await other.stats();
  await c.keep("plan", "a", "one");
  await other.keep("plan", "b", "two");
  c.store.held.set("page plan lost", { value: 1, at: 1 });
  await c.sweep();
  assert.equal((await c.stats()).count, 2);
  assert.deepEqual([...c.store.held.keys()].sort(), ["@index", "page plan a", "page plan b"]);
});

test("largest record: a list's answer loses its last pages; an item's page over it is not kept (#6976)", async () => {
  const { c } = cache({ ...webBounds, recordBytes: 1024 });
  await c.keepPage(q, "mac", 0, { items: Array.from({ length: 10 }, (_, i) => item(i)), cursor: "p2" });
  await c.keepPage(q, "mac", 1, { items: Array.from({ length: 50 }, (_, i) => item(i + 10)), cursor: "p3" });
  assert.equal((await c.answer(q, ["mac"])).hosts[0].items.length, 10);
  await c.keepDetail("mac", { item: item(1), body: "body", comments: [] });
  await c.keepDetail("mac", { item: item(1), body: "body", comments: [{ id: "1", body: "c".repeat(2048) }] });
  assert.equal(await c.detail(item(1).node), undefined);
});

test("largest record: a list's answer, a page and an item at their limit are kept, and over it are not (#6908)", async () => {
  // A page's record is {"value":…} with its read time: find the size that fits, and one character more.
  const { c } = cache({ ...webBounds, recordBytes: 1024 });
  let n = 1024;
  for (;; n--) {
    await c.keep("plan", "a", "x".repeat(n));
    if (await c.kept("plan", "a")) break;
  }
  assert.ok(n > 900, `${n}`);
  await c.keep("plan", "a", "x".repeat(n + 1));
  assert.equal(await c.kept("plan", "a"), undefined, "the earlier one is not shown as the new one");
  await c.keep("plan", "a", "é".repeat(n));
  assert.equal(await c.kept("plan", "a"), undefined, "the limit is in bytes");
});

test("largest record: an item over its limit after the cut does not show its earlier copy as new (#6908)", { skip: "#7198" }, async () => {
  const { c, tick } = cache();
  await c.keepPage(q, "mac", 0, { items: [item(1), item(2)] });
  tick();
  const long = item(1, 2, { state: "closed", url: `https://x/${"u".repeat(5000)}` });
  await c.keepPage(q, "mac", 0, { items: [long, item(2)] });
  const drawn = (await c.answer(q, ["mac"])).hosts[0].items.find((i) => i.node.ref === long.node.ref);
  assert.ok(!drawn || drawn.state === "closed", "the answer read now draws the item as it was before");
});
