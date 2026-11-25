// The web page's localStorage and sessionStorage keep to their limits (#7253): an entry at its largest size is kept, one
// byte over is refused and changes nothing, and a list keeps the names there first.
import { test } from "node:test";
import assert from "node:assert/strict";
import { load } from "./load.mjs";

const { fits, putEntry, boundedNames, namesBytes } = await load("src/model/keyValue.ts");

class Memory {
  items = new Map();
  get length() { return this.items.size; }
  key(i) { return [...this.items.keys()][i] ?? null; }
  getItem(key) { return this.items.get(key) ?? null; }
  setItem(key, value) { this.items.set(key, String(value)); }
  removeItem(key) { this.items.delete(key); }
}

test("an entry is measured in UTF-8; one over is refused, and what was kept stays", () => {
  const s = new Memory();
  assert.ok(putEntry(s, "k", "é".repeat(5), 10));
  assert.ok(!putEntry(s, "k", "é".repeat(5) + "a", 10));
  assert.ok(!putEntry(s, "new", "a".repeat(11), 10));
  assert.deepEqual([...s.items], [["k", "é".repeat(5)]]);
  assert.ok(fits("", 0));
  assert.ok(!putEntry(undefined, "k", "a", 10), "storage that is off");
  assert.ok(!putEntry({ setItem() { throw new Error("full"); } }, "k", "a", 10), "storage that is full");
});

test("a list keeps the names there first, less those too long, and its largest JSON fits", () => {
  assert.deepEqual(boundedNames(["a", "toolong", "b", "c"], 2, 3), ["a", "b"]);
  const worst = Array.from({ length: 5 }, () => "\u0001".repeat(8));
  assert.ok(fits(JSON.stringify(boundedNames(worst, 5, 8)), namesBytes(5, 8)));
});

test("achievements: the limit of names, and an old list over it is read to it", async () => {
  const { AchievementsSeen, seenMost } = await load("src/model/achievements.ts");
  const s = new Memory();
  s.setItem("agents.achievements.seen", JSON.stringify(Array.from({ length: seenMost + 1 }, (_, i) => `a${i}`)));
  const seen = new AchievementsSeen(s);
  assert.equal(seen.seen.value.size, seenMost);
  seen.dismiss("firstAgent");
  assert.ok(seen.seen.value.has("firstAgent"), "it goes for this visit");
  assert.equal(JSON.parse(s.getItem("agents.achievements.seen")).length, seenMost);
});

test("folds: the last one at the limit is kept, the next holds for the visit only, and none goes", async () => {
  const { Folds, foldsKept, foldBytes, sessionsKept } = await load("src/model/folds.ts");
  const s = new Memory();
  const folds = new Folds(s);
  for (let i = 0; i <= foldsKept; i++) folds.set("mac", `file:///f${i}/`, true);
  assert.ok(folds.isOpen("mac", `file:///f${foldsKept}/`));
  const again = new Folds(s);
  assert.ok(again.isOpen("mac", "file:///f0/"));
  assert.ok(again.isOpen("mac", `file:///f${foldsKept - 1}/`));
  assert.ok(!again.isOpen("mac", `file:///f${foldsKept}/`));
  // Room again: a new one is kept.
  again.set("mac", "file:///f0/", false);
  again.set("mac", "file:///new/", true);
  const long = `file:///${"x".repeat(foldBytes)}/`;
  again.set("mac", "file:///f1/", false);
  again.set("mac", long, true);
  assert.ok(again.isOpen("mac", long));
  assert.ok(new Folds(s).isOpen("mac", "file:///new/"));
  assert.ok(!new Folds(s).isOpen("mac", long));
  for (let i = 0; i <= sessionsKept; i++) folds.setSession(`s${i}`, true);
  const sessions = new Folds(s);
  assert.ok(!sessions.isSessionOpen("s0"), "the oldest goes");
  assert.ok(sessions.isSessionOpen(`s${sessionsKept}`));
});

test("New Session's project folders: a new project over the count is not kept, a kept one changes", async () => {
  const { rememberProjectFolder, rememberedProjectFolders, projectFoldersMost, projectFolderBytes } = await load("src/model/projects.ts");
  const s = new Memory();
  s.setItem("agents.newSession.projectFolders",
    JSON.stringify(Object.fromEntries(Array.from({ length: projectFoldersMost - 1 }, (_, i) => [`p${i}`, "mac|f"]))));
  rememberProjectFolder(s, "last", "mac|f");
  rememberProjectFolder(s, "over", "mac|f");
  rememberProjectFolder(s, "p0", "mac|g");
  rememberProjectFolder(s, "p1", "x".repeat(projectFolderBytes));
  const kept = rememberedProjectFolders(s);
  assert.deepEqual([kept.last, kept.over, kept.p0, kept.p1], ["mac|f", undefined, "mac|g", "mac|f"]);
});

test("Work's choices: one over the size is not kept, and an old one over it is not read", async () => {
  const { remember, remembered, rememberedBytes, rememberedKey } = await load("src/model/workRemembered.ts");
  const s = new Memory();
  remember("issue", "a".repeat(rememberedBytes), s);
  assert.equal(remembered("issue", s).length, rememberedBytes);
  remember("issue", "b".repeat(rememberedBytes + 1), s);
  assert.equal(remembered("issue", s)[0], "a");
  s.setItem(rememberedKey("pr"), "c".repeat(rememberedBytes + 1));
  assert.equal(remembered("pr", s), undefined);
});

test("New Session's last folder: one over the size is not kept", async () => {
  const { rememberFolder, rememberedFolder, rememberedFolderBytes } = await load("src/model/sidebar.ts");
  const s = new Memory();
  rememberFolder(s, { host: "mac", folder: "file:///a/" });
  rememberFolder(s, { host: "mac", folder: "x".repeat(rememberedFolderBytes) });
  assert.equal(rememberedFolder(s).folder, "file:///a/");
});

test("the first build's idea: at the limit it is sent and kept, over it it is refused", async () => {
  const { FirstBuildStore, ideaBytes } = await load("src/model/firstBuild.ts");
  const s = new Memory();
  const build = new FirstBuildStore(s);
  assert.ok(!build.send("x".repeat(ideaBytes + 1)));
  assert.equal(build.sent.value, null);
  assert.ok(build.send("x".repeat(ideaBytes)));
  assert.equal(new FirstBuildStore(s).sent.value.length, ideaBytes);
});

test("lock pins: a new one over the count or the size is not kept, a kept one still changes", async () => {
  const before = globalThis.localStorage;
  const s = globalThis.localStorage = new Memory();
  try {
    const { pinsAt, recoveryDoneAt, pinsKept, pinBytes } = await load("src/model/machineLock.ts");
    const pin = (count, person = "p") => ({ person, count, head: "h" });
    for (let i = 0; i < pinsKept - 1; i++) s.setItem(`agents-lock-pin.x:${i}`, "{}");
    s.setItem("agents.other", "not a pin");
    pinsAt("agents-lock-pin.x:last").save(pin(1));
    pinsAt("agents-lock-pin.x:over").save(pin(1));
    recoveryDoneAt(() => "agents-lock-pin.x").set("over");
    pinsAt("agents-lock-pin.x:last").save(pin(2));
    assert.equal(pinsAt("agents-lock-pin.x:last").load().count, 2);
    assert.equal(pinsAt("agents-lock-pin.x:over").load().count, 0);
    assert.ok(!recoveryDoneAt(() => "agents-lock-pin.x").done("over"));
    assert.equal(s.length, pinsKept + 1);
    pinsAt("agents-lock-pin.x:last").save(pin(3, "p".repeat(pinBytes)));
    assert.equal(pinsAt("agents-lock-pin.x:last").load().count, 2);
  } finally {
    globalThis.localStorage = before;
  }
});

test("the copy of the feature flags, the tab's filters and a table's order keep to their sizes", async () => {
  const [local, session] = [globalThis.localStorage, globalThis.window];
  const s = globalThis.localStorage = new Memory();
  const tab = new Memory();
  globalThis.window = { sessionStorage: tab, localStorage: s };
  try {
    const { setMachineFilter } = await load("src/model/agentFilters.ts");
    setMachineFilter("m".repeat(1024));
    setMachineFilter("n".repeat(1025));
    assert.equal(tab.getItem("agentsFilterMachine"), "m".repeat(1024));
    setMachineFilter(null);
    assert.equal(tab.getItem("agentsFilterMachine"), null);
    const { keepOrder } = await load("src/model/sort.ts");
    const empty = JSON.stringify({ column: "", dir: "asc" }).length;
    keepOrder("t", { column: "c".repeat(1024 - empty), dir: "asc" });
    keepOrder("t", { column: "d".repeat(1025 - empty), dir: "asc" });
    assert.equal(tab.getItem("tableSort.t").length, 1024);
    assert.match(tab.getItem("tableSort.t"), /c/);
    const { flagsBytes, preferencesSaid } = await load("test/flagBounds-entry.ts");
    preferencesSaid({ "flag:a": true });
    assert.equal(s.getItem("featureFlags"), '{"flag:a":true}');
    preferencesSaid({ [`flag:${"x".repeat(flagsBytes)}`]: true });
    assert.equal(s.getItem("featureFlags"), '{"flag:a":true}');
  } finally {
    globalThis.localStorage = local;
    globalThis.window = session;
  }
});
