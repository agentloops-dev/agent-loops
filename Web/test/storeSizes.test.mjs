// Reports ▸ Store sizes (#6907): the places joined, with an honest state for each store and each place.
import { test } from "node:test";
import assert from "node:assert/strict";
import { load } from "./load.mjs";

const m = await load("src/model/storeSizes.ts");
const hour = 3_600_000;
const now = Date.UTC(2026, 10, 25, 12);
const wire = (ms) => (ms - Date.UTC(2001, 0, 1)) / 1000;
const size = (over = {}) => ({ id: "machine.transcripts", owner: "machine", scope: "session", kind: "log", state: "current",
  bytes: 640_000_000, records: 100, averageRecordBytes: 6_400_000, maxBytes: 800_000_000, percentBytes: 80, measuredAt: wire(now - 60_000), ...over });
const source = (stores, over = {}) => ({ id: "H1", name: "Mac mini", got: { stores, total: stores.length }, at: now, ...over });
const status = (over) => m.rowsOf([source([size(over)])], now)[0];

test("a place's pages are all read, and a runaway cursor stops", async () => {
  const asked = [];
  const got = await m.readPlace(async (q) => { asked.push(q); return q.cursor ? { stores: [size({ id: "b" })], total: 2 } : { stores: [size({ id: "a" })], total: 2, cursor: "a" }; });
  assert.deepEqual(asked, [{}, { cursor: "a" }]);
  assert.deepEqual(got.stores.map((s) => s.id), ["a", "b"]);
  let calls = 0;
  await m.readPlace(async () => { calls++; return { stores: [], total: 0, cursor: "x" }; });
  assert.equal(calls, m.mostPages);
});

test("a machine that is offline, old or past the limit is not asked, and says why", () => {
  const places = m.placesOf([{ id: "a", name: "A", answers: false, has: true }, { id: "b", name: "B", answers: true, has: false },
    ...Array.from({ length: 50 }, (_, i) => ({ id: `c${i}`, name: "C", answers: true, has: true }))]);
  assert.equal(places[0].id, m.planeSource);
  assert.equal(places[0].not, undefined);
  assert.match(places[1].not, /offline/);
  assert.match(places[2].not, /Update/);
  assert.equal(places[3].not, undefined);
  assert.match(places.at(-1).not, /first 50/);
});

test("a place that never answers fails as late", async () => {
  await assert.rejects(m.within(new Promise(() => {}), 1), (e) => m.problemWords(e) === "It did not answer in time.");
  assert.equal(await m.within(Promise.resolve(3), 1000), 3);
});

test("a store's status: over wins, an old measurement is said, and a kept answer gets old here", () => {
  assert.equal(status({}).status, "within");
  assert.equal(status({ percentRecords: 93 }).status, "near");
  assert.equal(status({ maxRecords: 1, percentRecords: 100 }).status, "within", "a store of one record is not near its limit");
  assert.equal(status({ state: "stale" }).status, "stale");
  assert.equal(status({ measuredAt: wire(now - 3 * hour) }).status, "stale");
  assert.equal(status({ owner: "controlPlane", measuredAt: wire(now - 3 * hour) }).status, "within");
  assert.equal(status({ owner: "controlPlane", measuredAt: wire(now - 13 * hour) }).status, "stale");
  const over = status({ state: "over", percentBytes: 108, measuredAt: wire(now - 3 * hour) });
  assert.equal(m.statusWords(over), "Over limit, stale");
  assert.equal(m.filtered([over], "stale").length, 1);
  assert.equal(m.filtered([over], "over").length, 1);
  assert.equal(status({ state: "unavailable", measuredAt: undefined }).status, "unavailable");
});

test("both limits are said, and a percent never rounds over or under the limit", () => {
  const s = size({ maxRecords: 20_000, percentRecords: 12.9, limitPer: "session" });
  assert.deepEqual(m.limitsOf(s).map(m.limitWords), ["80% · 800 MB for each session", "12% · 20,000 records for each session"]);
  assert.equal(m.limitTitle(s), "The percent is of the largest session.");
  assert.deepEqual(m.limitsOf(size({ percentBytes: undefined })).map(m.limitWords), ["Limit 800 MB"]);
  assert.deepEqual([100.01, 100, 99.9, 0.2, 0].map(m.percentWords), ["101%", "100%", "99%", "<1%", "0%"]);
});

test("the cards count what is measured, not what is listed", () => {
  const stores = [size(), size({ id: "b", state: "over", percentBytes: 108, percentRecords: 120 }),
    size({ id: "c", state: "unavailable", bytes: undefined, records: undefined, measuredAt: undefined, percentBytes: undefined })];
  const sources = [source(stores), source([], { id: "H2", got: undefined, problem: "It is offline." })];
  const c = m.cardsOf(sources, m.rowsOf(sources, now));
  assert.deepEqual([c.measured, c.unmeasured, c.bytes, c.overWords, c.overKinds],
    ["2 of 3", "1 unavailable · 0 stale", "1.3 GB", "1 store", "1 byte limit · 1 record limit"]);
  assert.equal(m.cardsOf([], []).any, false);
});

test("a place that did not answer is named, with the age of what is drawn of it; the page's age is the oldest", () => {
  const sources = [source([size()]), source([size()], { id: "H2", name: "Box", at: now - 5 * 60_000, problem: "It is offline." }),
    { id: "H3", name: "New", problem: "It did not answer in time." }];
  assert.deepEqual(m.problemLines(sources, now), ["Box: It is offline. Its measurements are from the last read, 5 min ago.",
    "New: It did not answer in time. Its stores are not shown."]);
  assert.equal(m.readAtOf(sources), now - 5 * 60_000);
});

test("99 stores on each of 50 machines join quickly, with a key each", () => {
  const sources = Array.from({ length: 50 }, (_, h) => source(Array.from({ length: 99 }, (_, i) => size({ id: `s${i}` })), { id: `H${h}`, name: `M${h}` }));
  const rows = m.rowsOf(sources, now);
  assert.equal(new Set(rows.map((r) => r.key)).size, 4950);
  assert.equal(m.filtersOf(rows).find(([f]) => f === "in:M7")[1], "M7 (99)");
});
