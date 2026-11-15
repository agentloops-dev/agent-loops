// The Machines page's list (#5285): the query of a page, the chips, the ticked machines' actions and Delete's question.
import { test } from "node:test";
import assert from "node:assert/strict";
import { load } from "./load.mjs";

const m = await load("src/model/machineList.ts");

const made = (id, state, canStart, canStop) => ({ id, name: id, state, platform: "darwin", version: "0.18.1",
  workspace: { provider: "do", providerName: "DigitalOcean", workspace: id, canStart, canStop } });

test("a page asks only for what is chosen, then the cursor", () => {
  assert.deepEqual(m.pageQuery(m.noChoice), { sort: "name", limit: 50 });
  const c = m.toggled(m.toggled({ ...m.noChoice, search: " box " }, "state", "online"), "labels", "gpu");
  assert.deepEqual(m.pageQuery(c, "c1"), { sort: "name", limit: 50, query: "box", state: "online", labels: ["gpu"], cursor: "c1" });
});

test("a chip toggles: another value replaces it, the same lets it go, labels add up", () => {
  let c = m.toggled(m.noChoice, "state", "online");
  assert.equal(c.state, "online");
  c = m.toggled(c, "state", "stopped");
  assert.equal(c.state, "stopped");
  assert.equal(m.toggled(c, "state", "stopped").state, undefined);
  c = m.toggled(m.toggled(m.noChoice, "labels", "a"), "labels", "b");
  assert.deepEqual(c.labels, ["a", "b"]);
  assert.deepEqual(m.toggled(c, "labels", "a").labels, ["b"]);
  assert.equal(m.narrowed(m.noChoice), false);
  assert.equal(m.narrowed(c), true);
});

test("chips come from the counts, biggest first, and keep a chosen value at 0", () => {
  const counts = { state: { online: 3, stopped: 7 }, labels: {}, provider: {}, os: { darwin: 1 }, version: {} };
  const list = m.chips(counts, { ...m.noChoice, state: "failed" });
  assert.deepEqual(list.map((c) => `${c.key}:${c.value}:${c.count}:${c.on}`),
    ["state:stopped:7:false", "state:online:3:false", "state:failed:0:true", "os:darwin:1:false"]);
  assert.equal(m.chipWords(list[0]), "stopped 7");
  assert.deepEqual(m.chips(null, m.noChoice), []);
});

test("a control plane with no pages is filtered, sorted and counted here", () => {
  const all = [made("b", "online"), made("a", "stopped"), { ...made("c", "online"), version: "0.17.0" }];
  assert.deepEqual(m.localList(all, m.noChoice).map((h) => h.id), ["a", "b", "c"]);
  assert.deepEqual(m.localList(all, { ...m.noChoice, state: "online", sort: "version" }).map((h) => h.id), ["c", "b"]);
  assert.deepEqual(m.localList(all, { ...m.noChoice, search: "B" }).map((h) => h.id), ["b"]);
  assert.deepEqual(m.countsOf(all).state, { online: 2, stopped: 1 });
});

test("an action reaches only the ticked machines it fits", () => {
  const picks = [made("a", "online", true, true), made("b", "stopped", true, true), { id: "r", name: "r", state: "online" }];
  const a = m.bulkActions(picks, "mac");
  assert.deepEqual(a.start.map((h) => h.id), ["b"]);
  assert.deepEqual(a.stop.map((h) => h.id), ["a"]);
  assert.deepEqual(a.delete.map((h) => h.id), ["a", "b"]);
  assert.equal(m.bulkLabel("Stop", 1, 3), "Stop 1");
  assert.equal(m.bulkLabel("Stop", 3, 3), "Stop");
  assert.equal(m.bulkLabel("Stop", 0, 3), "Stop");
});

test("Delete asks, names the machines and lists the work not pushed", () => {
  assert.match(m.bulkDeleteQuestion(["a"], []), /^Delete a\?/);
  const q = m.bulkDeleteQuestion(["a", "b", "c", "d", "e", "f", "g"], ["a: /w"]);
  assert.match(q, /7 machines \(a, b, c, d, e and 2 more\)/);
  assert.match(q, /Work not pushed on it: a: \/w\./);
});
