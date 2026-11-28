// Work ▸ Next (#5691): the address, the project's folders, the epic filter and the on hold rows at the end.
import { test } from "node:test";
import assert from "node:assert/strict";
import { load } from "./load.mjs";

const n = await load("src/model/workNext.ts");
const nav = await load("src/model/nav.ts");

const projects = [
  { id: "alexec/agents", name: "Agents", folders: new Set(["mac|/Users/a/Agents", "box|/home/a/Agents"]) },
  { id: "folder:mac|/Users/a/Notes", name: "Notes", folders: new Set(["mac|/Users/a/Notes"]) },
];

function row(number, place, { onHold = false, reasons = ["priority high"], title = `Item ${number}` } = {}) {
  return {
    item: { node: { kind: "issue", ref: `github:issue:alexec/agents#${number}` }, title, state: "open", labels: [], assignees: [], updatedAt: 0, comments: 0 },
    place, reasons, onHold,
  };
}

test("Next is a page of Work, after Issues", () => {
  const ids = nav.navPages.work.map((p) => p.id);
  assert.equal(ids[ids.indexOf("issue") + 1], "next");
  assert.deepEqual(nav.navPages.work.find((p) => p.id === "next").draw, { kind: "next" });
});

test("the address says the project, the epic and the level; the Mac's link names a folder by its path", () => {
  assert.deepEqual(n.parseNextQuery("", projects), { project: "", epic: "", level: "issue" });
  assert.deepEqual(n.parseNextQuery("folder=%2Fhome%2Fa%2FAgents%2F", projects), { project: "alexec/agents", epic: "", level: "issue" });
  assert.deepEqual(n.parseNextQuery("folder=/nowhere", projects), { project: "", epic: "", level: "issue" });
  const c = { project: "alexec/agents", epic: "github:issue:alexec/agents#5687", level: "issue" };
  assert.deepEqual(n.parseNextQuery(n.nextQuery(c), projects), c);
  // The epics in order take no epic.
  assert.equal(n.nextQuery({ project: "", epic: "github:issue:alexec/agents#5687", level: "epic" }), "level=epic");
  assert.deepEqual(n.parseNextQuery("level=epic&epic=x", projects), { project: "", epic: "", level: "epic" });
});

test("forge/next is asked on the project's folders, the machines' order first", () => {
  assert.deepEqual(n.nextFolders(projects[0], ["box", "mac"]), [{ host: "box", folder: "/home/a/Agents" }, { host: "mac", folder: "/Users/a/Agents" }]);
  assert.deepEqual(n.nextFolders(projects[0], ["mac"]), [{ host: "mac", folder: "/Users/a/Agents" }]);
  assert.deepEqual(n.nextFolders(undefined, ["mac"]), []);
});

test("an epic's ref is the node forge/next takes", () => {
  assert.deepEqual(n.epicNode("github:issue:alexec/agents#5687"), { kind: "issue", ref: "github:issue:alexec/agents#5687" });
  assert.equal(n.epicNode(""), undefined);
});

test("on hold rows go under their own heading, after the ready ones", () => {
  const got = n.splitNext({ rows: [row(1, 1), row(2, 2), row(3, 3, { onHold: true, reasons: ["on hold", "no priority"] })], readAt: 0 });
  assert.deepEqual(got.ready.map((r) => r.place), [1, 2]);
  assert.deepEqual(got.onHold.map((r) => r.item.title), ["Item 3"]);
  assert.equal(n.reasonsWords(got.onHold[0]), "on hold · no priority");
  assert.deepEqual(n.splitNext(null), { ready: [], onHold: [] });
});

test("the epic filter lists the epics in order, by number and title", () => {
  assert.deepEqual(n.epicChoices({ rows: [row(5687, 1, { title: "Priority" })], readAt: 0 }),
    [{ value: "github:issue:alexec/agents#5687", words: "#5687 Priority" }]);
  assert.equal(n.itemNumber("linear:issue:ENG-4"), "ENG-4");
});

test("an empty page and an older machine say why", () => {
  assert.equal(n.emptyTitle({ project: "", epic: "", level: "issue" }), "Nothing is ready");
  assert.equal(n.emptyTitle({ project: "", epic: "x", level: "issue" }), "Nothing in this epic is ready");
  assert.equal(n.emptyTitle({ project: "", epic: "", level: "epic" }), "No open epics");
  assert.match(n.nextProblem("Method not found: forge/next"), /older/);
  assert.equal(n.nextProblem("GitHub refused the token"), "GitHub refused the token");
});

test("Autopilot: the strip, the cell and the item that starts next (#5905)", () => {
  const at = (ms) => (ms - Date.UTC(2001, 0, 1)) / 1000;
  const now = Date.parse("2026-11-26T12:00:00Z");
  const status = (mode, rows = [], lastRunAt) => ({ mode, rows, ...lastRunAt ? { lastRunAt: at(lastRunAt) } : {} });
  assert.equal(n.autopilotStrip(null, now), null);
  assert.match(n.autopilotStrip(status("off"), now).text, /^Autopilot: Off/);
  assert.equal(n.autopilotStrip(status("on", [], now - 120_000), now).text, "Autopilot: On · last run 2 min ago");
  const paused = { ref: "#9", kind: "lead", state: "paused", reason: "paused: main red", since: at(now - 72 * 60_000) };
  assert.deepEqual(n.autopilotStrip(status("on", [paused], now), now), { text: "Paused: main red for 1 h", paused: true });

  const rows = [row(1, 1), row(2, 2), row(3, 3), row(9, 4), row(4, 0, { onHold: true })];
  const st = status("on", [
    { ref: "#1", kind: "lane", state: "started", reason: "", since: at(now) },
    { ref: "#2", kind: "lane", state: "waiting", reason: "limit open-prs", since: at(now) }, paused,
  ]);
  assert.equal(n.startsNext(st, rows.filter((r) => !r.onHold)), "github:issue:alexec/agents#3");
  assert.deepEqual(n.autopilotCell(st, rows[0], false), { words: "Started", resume: null });
  assert.deepEqual(n.autopilotCell(st, rows[1], false), { words: "limit open-prs", resume: null });
  assert.deepEqual(n.autopilotCell(st, rows[2], true), { words: "Starts next", resume: null });
  assert.deepEqual(n.autopilotCell(st, rows[3], false), { words: "paused: main red", resume: "#9" });
  assert.deepEqual(n.autopilotCell(st, rows[4], false), { words: "on hold", resume: null });
  assert.equal(n.autopilotCell(status("off"), rows[0], true), null);
});
