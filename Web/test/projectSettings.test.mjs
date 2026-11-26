// Project Settings and Folder Settings (#3280): a default is kept as no value, as the window keeps it.
import { test } from "node:test";
import assert from "node:assert/strict";
import { load } from "./load.mjs";

const p = await load("src/model/projectSettings.ts");

test("project settings: a limit at its default leaves .agents/folder.json", () => {
  assert.deepEqual(p.withLimit({ queued: 8, agentsMayArchive: false }, "queued", 5, 5), { agentsMayArchive: false });
  assert.deepEqual(p.withLimit(undefined, "queued", 8, 5), { queued: 8 });
  assert.deepEqual(p.withLimit({ continueWhenBack: true }, "continueWhenBack", undefined, undefined), {});
});

test("project settings: the Helpers line says running, queued and total from policy (#4377)", () => {
  assert.equal(p.helpersNow(undefined), "3 running, 5 queued, 5 in total");
  assert.equal(p.helpersNow({ running: 4, queued: 2, notArchived: 9 }), "4 running, 2 queued, 9 in total");
});

test("project settings: the disk menus offer a number set by hand", () => {
  assert.deepEqual(p.diskChoices([5, 10, 20], 15), [5, 10, 15, 20]);
  assert.deepEqual(p.withDiskLine({ lowPercent: 50 }, "lowPercent", 20, 20), {});
  assert.deepEqual(p.withDiskLine(undefined, "criticalPercent", 5, 1), { criticalPercent: 5 });
});

test("project settings: the folder that keeps them is named", () => {
  assert.equal(p.folderPathOf("file:///Users/a/My%20Repo/"), "/Users/a/My Repo");
  assert.match(p.writtenIn("/Users/a/Agents", "This Mac"), /^Written in \/Users\/a\/Agents on This Mac\. Commit \.agents/);
});

test("project settings: Folder Settings has Not Committed, and Remove from Project… says what happens", () => {
  assert.deepEqual(p.folderPanes.map((x) => x.id), ["general", "diskSpace", "notCommitted"]);
  assert.match(p.removeQuestion("/Users/a/Agents"), /^Remove \/Users\/a\/Agents from this project\?\n\nIt gets a project of its own, named after it\./);
});

test("project settings: the folder's own project takes a free name", () => {
  assert.equal(p.freeProjectName("shop", [{ name: "Agents" }]), "shop");
  assert.equal(p.freeProjectName("shop", [{ name: "Shop" }, { name: "shop 2" }]), "shop 3");
  assert.equal(p.freeProjectName("  ", []), "Project");
});

test("project settings: Autopilot is a pane, and turning it on asks once (#5904)", async () => {
  assert.ok(p.projectPanes.some((x) => x.id === "autopilot"));
  const a = await load("src/model/autopilotSettings.ts");
  assert.deepEqual(a.autopilotModes.map((m) => m.mode), ["off", "high", "on"]);
  assert.equal(a.needsConfirm("off", "on"), true);
  assert.equal(a.needsConfirm("off", "high"), true);
  assert.equal(a.needsConfirm("high", "on"), false);
  assert.equal(a.needsConfirm("on", "off"), false);
  const row = (state) => ({ ref: "#1", kind: "lane", state, reason: "", since: 0 });
  const now = (100 + 978_307_200) * 1000 + 120_000;
  assert.equal(a.lastRunLine({ mode: "on", lastRunAt: 100, rows: [row("started"), row("waiting"), row("waiting")] }, now),
    "Last run 2 min ago · 1 started, 2 waiting");
  assert.equal(a.lastRunLine({ mode: "on", rows: [] }, now), a.autopilotWords.never);
});
