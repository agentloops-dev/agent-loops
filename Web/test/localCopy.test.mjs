// The page's local copy (#4790; spec 4777-fast, "the copy is a cache"): drawn at once, marked as
// from this device, the host's answer always wins, the caps hold, and nothing is written signed out.
import { test } from "node:test";
import assert from "node:assert/strict";
import { load } from "./load.mjs";

const { Store, LocalCopy, MemoryCopyStore, overCaps, startLocalCopy, machineRef, sessionRef, transcriptsKept, entriesKept, writeAfter } =
  await load("test/localCopy-entry.ts");

const host = (id, state = "online") => ({ id, name: id, state });
const agent = (id, at = 1) => ({ id, runtimeID: "claude", cwd: "file:///w/", state: "idle", lastActivityAt: at });
const entry = (id) => ({ id, at: 0, kind: { agentMessage: { text: id } } });
const settle = () => new Promise((r) => setTimeout(r, writeAfter + 50));

/** A link that answers nothing until `open`, then lists `hosts` and `agents`. */
function slowLink(hosts, agents) {
  const link = {
    open: false,
    onNotification: () => {},
    onState: () => {},
    call: async (method) => {
      if (!link.open) throw new Error("down");
      if (method === "machines/list") return hosts;
      if (method === "agents/list") return agents;
      if (method === "agents/turns") return { turns: [], firstTurn: 0, openStart: 0 };
      if (method === "agents/transcript") return { firstIndex: 0, total: 1, entries: [entry("host")] };
      if (method === "folders/list") return [];
      throw new Error("no");
    },
  };
  return link;
}

test("refs: a machine and a session", () => {
  assert.equal(machineRef("mac"), "agents:machine:#mac");
  assert.equal(sessionRef("7F3C"), "agents:session:#7F3C");
});

test("caps: transcripts past the newest 20 go, then the oldest while over the cap; then machines", () => {
  const rows = [{ ref: "m", kind: "machine", at: 0, bytes: 10 }];
  for (let i = 0; i < transcriptsKept + 2; i++) rows.push({ ref: `t${i}`, kind: "transcript", at: i, bytes: 10 });
  assert.deepEqual(overCaps(rows, 1_000).sort(), ["t0", "t1"]);
  assert.deepEqual(overCaps(rows, 10 + 10 * 18).sort(), ["t0", "t1", "t2", "t3"]);
  assert.deepEqual(overCaps([{ ref: "m", kind: "machine", at: 0, bytes: 99 }], 10), ["m"]);
});

test("the copy is drawn at once, marked from this device, and the host's answer wins", async () => {
  const held = new MemoryCopyStore();
  const seed = new LocalCopy(held);
  seed.writing = true;
  await seed.put([{ kind: "machine", ref: machineRef("mac"), host: host("mac"), folders: [], agents: [agent("old")], at: Date.now() },
    { kind: "machine", ref: machineRef("gone"), host: host("gone"), agents: [agent("g")], at: Date.now() }]);

  const link = slowLink([host("mac")], [agent("new", 2)]);
  const store = new Store(link);
  const copy = new LocalCopy(held);
  const stop = startLocalCopy(store, copy);
  await new Promise((r) => setTimeout(r, 10));
  assert.deepEqual(store.hosts.value.map((h) => h.id).sort(), ["gone", "mac"]);
  assert.deepEqual(store.agents.value.mac.map((a) => a.id), ["old"]);
  assert.deepEqual([...store.fromDevice.value].sort(), ["gone", "mac"]);

  link.open = true;
  copy.writing = true;
  await store.load();
  assert.deepEqual(store.hosts.value.map((h) => h.id), ["mac"]);
  assert.deepEqual(store.agents.value.mac.map((a) => a.id), ["new"]);
  assert.equal(store.agents.value.gone, undefined);
  assert.equal(store.fromDevice.value.size, 0);

  await settle();
  assert.deepEqual(held.held.get(machineRef("mac")).agents.map((a) => a.id), ["new"]);
  assert.equal(held.held.has(machineRef("gone")), false);
  stop();
});

test("a chat draws its copy until its host's page comes, which replaces it, and its end is kept", async () => {
  const held = new MemoryCopyStore();
  const seed = new LocalCopy(held);
  seed.writing = true;
  await seed.put([{ kind: "transcript", ref: sessionRef("s"), host: "mac", session: "s", at: Date.now(),
    turns: { turns: [], firstTurn: 0, openStart: 0 }, page: { firstIndex: 0, total: 1, entries: [entry("copy")] } }]);

  const link = slowLink([host("mac")], []);
  const store = new Store(link);
  const copy = new LocalCopy(held);
  const stop = startLocalCopy(store, copy);
  store.watch("mac", "s");
  // Its host's load fails before the copy is read: the copy still draws, and the failure goes.
  store.transcriptLoadFailure.value = { nothingLoaded: true, reason: "down" };
  await new Promise((r) => setTimeout(r, 10));
  assert.equal(store.transcriptSource.value, "device");
  assert.equal(store.transcriptLoadFailure.value, null);
  assert.deepEqual(store.entries.value.map((e) => e.id), ["copy"]);

  // Its host does not answer yet: the copy stays, with no failure said.
  await store.openSession("mac", "s");
  assert.deepEqual(store.entries.value.map((e) => e.id), ["copy"]);
  assert.equal(store.transcriptLoadFailure.value, null);

  link.open = true;
  copy.writing = true;
  await store.reloadTranscript();
  assert.equal(store.transcriptSource.value, "host");
  assert.deepEqual(store.entries.value.map((e) => e.id), ["host"]);
  await settle();
  assert.deepEqual(held.held.get(sessionRef("s")).page.entries.map((e) => e.id), ["host"]);
  stop();
});

test("a transcript's end is its last 200 entries, with where they start", async () => {
  const held = new MemoryCopyStore();
  const store = new Store(slowLink([], []));
  const copy = new LocalCopy(held);
  copy.writing = true;
  const stop = startLocalCopy(store, copy);
  store.watch("mac", "s");
  store.replaceTurns({ turns: [], firstTurn: 0, openStart: 0 });
  store.replaceTranscript({ firstIndex: 10, total: 260, entries: Array.from({ length: 250 }, (_, i) => entry(`e${i}`)) });
  await settle();
  const kept = held.held.get(sessionRef("s")).page;
  assert.equal(kept.entries.length, entriesKept);
  assert.equal(kept.entries[0].id, "e50");
  assert.equal(kept.firstIndex, 60);
  stop();
});

test("signed out, nothing is written", async () => {
  const held = new MemoryCopyStore();
  const copy = new LocalCopy(held);
  copy.writing = true;
  copy.forget();
  await copy.put([{ kind: "machine", ref: machineRef("mac"), host: host("mac"), agents: [], at: Date.now() }]);
  assert.equal(held.held.size, 0);
});

// Work's part of the copy (#4930): since #6356 only the forge's pictures, kept by URL to their count and their own cap,
// the oldest Work entry first when over the cap. Work's records are Work's cache (workCache.test.mjs).
const { workRef, workKept } = await load("test/localCopy-entry.ts");

test("work: a picture is read back for its URL only, with when it was read (#5142)", async () => {
  const copy = new LocalCopy(new MemoryCopyStore());
  copy.writing = true;
  await copy.keepWork("image", "https://x/a.png", { bytes: "AA" }, 1_800_000_000_000);
  assert.deepEqual(await copy.workRead("image", "https://x/a.png"), { value: { bytes: "AA" }, at: 1_800_000_000_000 });
  assert.equal(await copy.work("image", "https://x/b.png"), undefined);
  assert.equal(workRef("image", "https://x/a.png"), "agents:work:image#https://x/a.png");
});

test("work: nothing is kept signed out", async () => {
  const store = new MemoryCopyStore();
  const copy = new LocalCopy(store);
  await copy.keepWork("image", "x", { a: 1 });
  assert.equal(store.held.size, 0);
});

test("caps: pictures to their count and cap, then the oldest Work entry before a transcript; a list kept before #6356 goes", () => {
  const many = Array.from({ length: workKept.image + 2 }, (_, i) => ({ ref: `i${i}`, kind: "work", group: "image", at: i, bytes: 1 }));
  assert.deepEqual(overCaps(many).sort(), ["i0", "i1"]);
  const images = [1, 2, 3].map((i) => ({ ref: `p${i}`, kind: "work", group: "image", at: i, bytes: 40 }));
  assert.deepEqual(overCaps(images, 400), ["p1"]);
  const mixed = [
    { ref: "t", kind: "transcript", at: 0, bytes: 50 },
    { ref: "old", kind: "work", group: "image", at: 1, bytes: 50 },
    { ref: "new", kind: "work", group: "image", at: 2, bytes: 50 },
  ];
  assert.deepEqual(overCaps(mixed, 120, 20, 1_000), ["old"]);
  assert.deepEqual(overCaps([{ ref: "l", kind: "work", group: "list", at: 1, bytes: 1 }]), ["l"]);
});

const day = 24 * 60 * 60 * 1000;
const machine = (id, at, agents = []) => ({ kind: "machine", ref: machineRef(id), host: host(id), agents, at });
const chat = (session, on, at, entries = [entry("e")]) => ({ kind: "transcript", ref: sessionRef(session), host: on, session, at,
  turns: { turns: [], firstTurn: 0, openStart: 0 }, page: { firstIndex: 0, total: entries.length, entries } });

/** A copy with a clock of its own. */
function aged(held = new MemoryCopyStore(), recordBytes = undefined) {
  let now = 1_000;
  const copy = new LocalCopy(held, undefined, recordBytes, () => now);
  copy.writing = true;
  return { copy, held, tick: (ms) => { now += ms; } };
}

test("age: each kind of record goes 90 days after it was read, and drawing it does not keep it (#6792)", async () => {
  const { copy, held, tick } = aged();
  await copy.put([machine("mac", 1_000), chat("s", "mac", 1_000)]);
  await copy.keepWork("image", "u", "picture");
  tick(90 * day);
  await copy.sweep();
  assert.equal((await copy.machines()).length, 1, "90 days is kept");
  assert.ok(await copy.transcript("s"));
  assert.equal(await copy.work("image", "u"), "picture");
  const { count, oldest } = await copy.stats();
  assert.deepEqual([count, oldest], [3, 1_000]);
  tick(1);
  // Before a sweep too, nothing old draws.
  assert.deepEqual(await copy.machines(), []);
  assert.equal(await copy.transcript("s"), null);
  assert.equal(await copy.work("image", "u"), undefined);
  await copy.sweep();
  assert.deepEqual([...held.held.keys()], ["@index"]);
  assert.deepEqual(await copy.stats(), { count: 0, bytes: 0, oldest: undefined });
});

test("age: a write ages old records out", async () => {
  const { copy, held, tick } = aged();
  await copy.put([machine("mac", 1_000)]);
  tick(91 * day);
  await copy.keepWork("image", "u", "picture");
  assert.deepEqual([...held.held.keys()].sort(), ["@index", workRef("image", "u")]);
});

test("a machine no longer listed goes with its chats (#6792)", async () => {
  const { copy, held } = aged();
  await copy.put([machine("mac", 1_000), machine("gone", 1_000), chat("s", "mac", 1_000), chat("g", "gone", 1_000)]);
  await copy.put([]);
  assert.equal(held.held.size, 5);
  await copy.put([], ["mac"]);
  assert.deepEqual([...held.held.keys()].sort(), ["@index", machineRef("mac"), sessionRef("s")].sort());
});

test("until the control plane lists its machines, none goes (#6792)", async () => {
  const { copy, held } = aged();
  await copy.put([machine("mac", 1_000)]);
  const store = new Store(slowLink([], []));
  const stop = startLocalCopy(store, copy);
  await settle();
  // Another tab keeps a machine that this page does not hold.
  await aged(held).copy.put([machine("other", 1_000)]);
  store.hosts.value = [...store.hosts.value];
  await settle();
  assert.ok(held.held.has(machineRef("other")));
  stop();
});

test("tabs: one does not take the other's records out of the index; a record no index names goes", async () => {
  const { copy, held } = aged();
  const other = aged(held).copy;
  await other.stats();
  await copy.put([machine("a", 1_000)]);
  await other.put([machine("b", 1_000)]);
  held.held.set("lost", { at: 1_000 });
  await copy.sweep();
  assert.equal((await copy.stats()).count, 2);
  assert.deepEqual([...held.held.keys()].sort(), ["@index", machineRef("a"), machineRef("b")].sort());
});

test("largest record: a machine loses its archived records, a chat its oldest entries; a picture is not kept (#6792)", async () => {
  const { copy, held } = aged(undefined, 1_024);
  const archived = Array.from({ length: 50 }, (_, i) => ({ ...agent(`a${i}`), state: "archived" }));
  await copy.put([{ ...machine("mac", 1_000, [agent("live"), ...archived]), point: { epoch: "e", revision: 1 } },
    chat("s", "mac", 1_000, Array.from({ length: 50 }, (_, i) => entry(`e${i}`)))]);
  const kept = held.held.get(machineRef("mac"));
  assert.deepEqual(kept.agents.map((a) => a.id), ["live"]);
  assert.equal(kept.point, undefined);
  const page = held.held.get(sessionRef("s")).page;
  assert.equal(page.entries.at(-1).id, "e49");
  assert.equal(page.entries[0].id, `e${page.firstIndex}`);
  await copy.keepWork("image", "u", "small");
  await copy.keepWork("image", "u", "x".repeat(2_048));
  assert.equal(await copy.work("image", "u"), undefined);
  assert.equal(held.held.has(workRef("image", "u")), false, "the stale one goes too");
});
