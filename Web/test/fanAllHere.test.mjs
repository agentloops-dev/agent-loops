// #5996: in its lock, the page asks each machine itself for what the control plane's calls for every machine ask, as
// the control plane's fanall.go and fanout.go answer them, since a machine in a lock refuses the control plane's own asks.
import { test } from "node:test";
import assert from "node:assert/strict";
import { load } from "./load.mjs";

const { answerHere } = await load("src/model/fanAll.ts");

/** A page with machines a, b and c (which answers nothing); `answers` is each machine's answer by method. */
function here(answers, capabilities = { a: ["forge/1", "docs/1", "messages/1", "events/1"], b: ["forge/1", "docs/1", "messages/1"], c: ["forge/1"] }) {
  const asked = [];
  const hosts = [{ id: "a", name: "A" }, { id: "b", name: "B" }, { id: "c", name: "C" }];
  return {
    asked,
    hosts: (capability) => hosts.filter((h) => (capabilities[h.id] ?? []).includes(capability)),
    ask: async (method, params, host) => {
      asked.push({ method, params, host });
      const answer = answers[host]?.[method];
      if (answer instanceof Error) throw answer;
      if (answer === undefined) throw new Error("no such method");
      return typeof answer === "function" ? answer(params) : answer;
    },
  };
}

test("forge/listAll asks each machine's forge/pulls over its own link, whatever its state; a failing one is unreached", async () => {
  const h = here({ a: { "forge/pulls": { items: [{ n: 1 }] } }, b: { "forge/pulls": new Error("not in the lock") } });
  const got = await answerHere("forge/listAll", { kind: "pulls", state: "open", limit: 500 }, h);
  assert.deepEqual(got.lists, [{ machine: "a", machineName: "A", list: { items: [{ n: 1 }] } }]);
  assert.deepEqual(got.unreached, [{ machine: "b", machineName: "B" }, { machine: "c", machineName: "C" }]);
  assert.equal(got.notAsked, 0);
  // The limit is bounded as the control plane bounds it, and every machine is asked.
  assert.deepEqual(h.asked.map((a) => [a.host, a.method, a.params]), [
    ["a", "forge/pulls", { state: "open", limit: 100 }], ["b", "forge/pulls", { state: "open", limit: 100 }],
    ["c", "forge/pulls", { state: "open", limit: 100 }]]);
});

test("forge/listAll with pages asks only the machines named, each from its cursor", async () => {
  const h = here({ b: { "forge/issues": (p) => ({ items: [], cursor: p.cursor + "+" }) } });
  const got = await answerHere("forge/listAll", { kind: "issues", pages: [{ machine: "b", cursor: "x" }] }, h);
  assert.deepEqual(h.asked.map((a) => [a.host, a.params.cursor]), [["b", "x"]]);
  assert.equal(got.lists[0].list.cursor, "x+");
  await assert.rejects(answerHere("forge/listAll", { kind: "epics" }, h), /issues or pulls/);
});

test("docs/listAll merges the machines' docs newest first, and its cursor goes on from where each machine was", async () => {
  const docs = { a: [{ ref: "a1", updated: 9 }, { ref: "a2", updated: 5 }], b: [{ ref: "b1", updated: 7 }, { ref: "b2", updated: 1 }] };
  const h = here({
    a: { "docs/list": { docs: docs.a, sources: [{ id: "sa" }] } },
    b: { "docs/list": { docs: docs.b, sources: [{ id: "sb" }] } },
  });
  const first = await answerHere("docs/listAll", { limit: 3 }, h);
  assert.deepEqual(first.docs.map((d) => [d.machine, d.doc.ref]), [["a", "a1"], ["b", "b1"], ["a", "a2"]]);
  assert.deepEqual(first.sources.map((s) => [s.machine, s.source.id]), [["a", "sa"], ["b", "sb"]]);
  assert.ok(first.cursor);
  const next = await answerHere("docs/listAll", { limit: 3, cursor: first.cursor }, h);
  assert.deepEqual(next.docs.map((d) => d.doc.ref), ["b2"]);
  assert.equal(next.cursor, undefined);
  await assert.rejects(answerHere("docs/listAll", { cursor: "!!" }, h), /not a cursor/);
});

test("messages/conversationsAll keeps each machine's connector errors, with the machine", async () => {
  const h = here({
    a: { "messages/conversations": { conversations: [{ conversation: { lastActivity: "2026-11-17T10:00:00Z" } }], errors: [{ message: "slack" }] } },
    b: { "messages/conversations": { conversations: [{ conversation: { lastActivity: "2026-11-17T11:00:00Z" } }], errors: [] } },
  });
  const got = await answerHere("messages/conversationsAll", { unread: true, limit: 50 }, h);
  assert.deepEqual(got.conversations.map((c) => c.machine), ["b", "a"]);
  assert.deepEqual(got.errors, [{ machine: "a", machineName: "A", error: { message: "slack" } }]);
  assert.deepEqual(h.asked[0].params, { limit: 50, unread: true });
});

test("a method that is not a call for every machine is not answered here", async () => {
  assert.equal(await answerHere("forge/issues", {}, here({})), undefined);
});
