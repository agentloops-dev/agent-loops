// A refresh makes a fixed count of calls, the same at 1 and at many machines (#5592, the budget of #5282).
import { test } from "node:test";
import assert from "node:assert/strict";
import { load } from "./load.mjs";

const { Store, loadBoards, loadList, loadMore, workList, hostListsOf, notAnswered, oneEach, MemoryWorkStore, WorkCache, setWorkCache } =
  await load("test/manyMachines.ts");

const machines = (n) => Array.from({ length: n }, (_, i) => ({ id: `m${i}`, name: `Machine ${i}`, state: "online" }));

function counting(n) {
  const hosts = machines(n);
  const asked = [];
  const each = (field, of) => ({ [field]: hosts.map((h) => ({ machine: h.id, machineName: h.name, ...of(h) })), unreached: [], notAsked: 0 });
  const link = {
    asked, notify: () => {}, onNotification: () => {}, onState: () => {},
    call: async (method, params, host) => {
      asked.push(host ? `${host} ${method}` : method);
      switch (method) {
        case "machines/list": return hosts;
        case "agents/list": return [];
        case "reports/nodes": return { nodes: [] };
        case "forge/listAll": return each("lists", (h) => ({ list: { items: [{ node: { kind: "issue", ref: `github:o/r#${h.id}` }, title: h.id }], cursor: params.pages ? undefined : "2" } }));
        case "forge/boardsAll": return each("boards", () => ({ boards: { boards: [{ url: "https://b", name: "B" }] } }));
        case "events/listAll": return each("pages", () => ({ page: { events: [], waiting: [], hasMore: false } }));
        case "connections/listAll": return each("lists", () => ({ connections: { connections: [] } }));
        default: if (method.endsWith("/pending")) return []; throw new Error(`no ${method}`);
      }
    },
  };
  const store = new Store(link);
  store.takeHosts(hosts);
  return { store, asked, hosts };
}

async function refresh(n) {
  const { store, asked, hosts } = counting(n);
  asked.length = 0;
  const l = workList("issue");
  l.read = undefined;
  await loadList(store, l);
  await loadMore(store, l);
  await loadBoards(store, true);
  await store.loadEventsAll(hosts.map((h) => h.id));
  await store.refreshEndedConnections(hosts.map((h) => ({ host: h.id, folder: "file:///w/" })));
  assert.equal(l.lists.value.length, n, "a list for each machine");
  assert.equal(Object.keys(store.events.value).length, n, "events of each machine");
  return asked.filter((line) => line !== "machines/list" && !line.endsWith("agents/list") && !line.endsWith("/pending"));
}

test("a refresh of Work, Events and connections makes the same calls at 1 and at 40 machines (#5592)", async () => {
  const one = await refresh(1);
  const many = await refresh(40);
  assert.deepEqual(many, one);
  assert.deepEqual(one, ["reports/nodes", "forge/listAll", "forge/listAll", "forge/boardsAll", "events/listAll", "connections/listAll"]);
});

test("forge/listAll's answer is a list for each machine asked, and an unreached one says it did not answer (#5592)", () => {
  const got = {
    lists: [{ machine: "a", machineName: "A", list: { items: [], cursor: "c" } }, { machine: "off", machineName: "Off", list: { items: [] } }],
    unreached: [{ machine: "b", machineName: "B" }], notAsked: 0,
  };
  const lists = hostListsOf(got, ["a", "b"], []);
  assert.deepEqual(lists.map((l) => l.host), ["a", "b"], "only the machines asked, in their order");
  assert.equal(lists[0].cursor, "c");
  assert.deepEqual(lists[1].errors, [{ forge: "", message: notAnswered }]);
});

test("oneEach leaves out a machine that reads only what another asked reads (#6823)", () => {
  const scopes = new Map([["a", ["github:o/r"]], ["b", ["github:o/r", "github:o/s"]], ["c", ["github:o/t"]]]);
  assert.deepEqual(oneEach(["a", "b", "c", "d"], scopes), { ask: ["b", "c", "d"], covered: ["a"] }, "the one that reads the most first; d is not known");
  assert.deepEqual(oneEach(["a", "b"], new Map([["a", ["github:o/r"]], ["b", ["github:o/r"]]])), { ask: ["a"], covered: ["b"] });
});

test("two machines on the same repo: page 1 is asked of one of them, and the other's kept rows go (#6823)", async () => {
  const cache = new WorkCache(new MemoryWorkStore());
  cache.writing = true;
  setWorkCache(cache);
  try {
    const hosts = [{ id: "a", name: "A", state: "online" }, { id: "b", name: "B", state: "online" }];
    const pages = [];
    let fail = false;
    const link = {
      notify: () => {}, onNotification: () => {}, onState: () => {},
      call: async (method, params) => {
        switch (method) {
          case "machines/list": return hosts;
          case "reports/nodes": return { nodes: [] };
          case "forge/listAll": {
            pages.push(params.pages?.map((p) => `${p.machine}${p.cursor}`) ?? "all");
            const asked = params.pages ? hosts.filter((h) => params.pages.some((p) => p.machine === h.id)) : hosts;
            return {
              lists: asked.map((h) => ({ machine: h.id, machineName: h.name, list: fail && h.id === "a"
                ? { items: [], errors: [{ forge: "github", message: "down" }] }
                : { items: [{ node: { kind: "issue", ref: "github:o/r#1" }, title: "one" }], scopes: ["github:o/r"] } })),
              unreached: [], notAsked: 0,
            };
          }
          default: return [];
        }
      },
    };
    const store = new Store(link);
    store.takeHosts(hosts);
    const l = workList("issue");
    l.read = undefined;
    await loadList(store, l);
    assert.deepEqual(l.lists.value.map((h) => h.host), ["a"], "b reads what a reads");
    l.asked.value++;
    await loadList(store, l);
    assert.deepEqual(pages, ["all", ["a"]], "the second read names only a, at cursor \"\"");
    assert.equal((await cache.answer({ kind: "issue", state: "open", sort: "updated" }, ["b"])), undefined, "b's kept answer went");
    fail = true;
    l.asked.value++;
    await loadList(store, l);
    assert.deepEqual(pages.at(-2), ["a"]);
    assert.deepEqual(pages.at(-1), ["b"], "a failed, so b is asked too");
    assert.deepEqual(l.lists.value.map((h) => h.host), ["a", "b"]);
  } finally {
    setWorkCache(undefined);
  }
});
