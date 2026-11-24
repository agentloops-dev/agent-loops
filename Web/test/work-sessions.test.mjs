// The sessions on Work's items and an epic's lead (#5453, #3265), as WorkSessionsTests has it on the window.
import { test } from "node:test";
import assert from "node:assert/strict";
import { load } from "./load.mjs";

const s = await load("src/model/workSessions.ts");
const nav = await load("test/nav-entry.ts");
nav.preferencesSaid({ "flag:work": true, "flag:docs": true });

const node = (n) => ({ kind: "issue", ref: `github:alexec/Agents#${n}` });
const agent = (id, labels, state, at = 0) => ({ id, state, createdAt: at, labels: labels.map((value) => ({ value, owner: "agent", addedAt: 0 })) });

test("the lead is the live session labelled lead and the epic", () => {
  const ended = agent("ended", ["#3265", "lead"], "stopped", 100);
  const live = agent("live", ["#3265", "lead"], "running");
  const lane = agent("lane", ["#3265"], "running", 200);
  const other = agent("other", ["#3100", "lead"], "running", 300);
  const archived = agent("archived", ["#3265", "lead"], "archived", 400);
  assert.equal(s.epicLead(node(3265), [ended, live, lane, other, archived])?.id, "live");
  assert.equal(s.epicLead(node(3265), [ended, lane])?.id, "ended");
  assert.equal(s.epicLead(node(3265), [lane, other]), undefined);
  assert.equal(s.epicLead({ kind: "issue", ref: "linear:ENG-5" }, [agent("l", ["ENG-5", "lead"], "finished")])?.id, "l");
});

// #5453: a row in Work ▸ Issues shows the session that works on its item now.
test("a row's session is a live one labelled with the item, one that needs you first", () => {
  const stopped = agent("stopped", ["#5453"], "stopped", 900);
  const old = agent("old", ["#5453"], "running", 100);
  const newer = agent("newer", ["#5453", "lane"], "starting", 200);
  const asks = agent("asks", ["#5453"], "waitingOnUser", 50);
  const other = agent("other", ["#5454"], "running", 300);
  const pr = agent("pr", ["P#5453"], "running", 400);
  assert.equal(s.workingSession(node(5453), [stopped, old, newer, other, pr])?.id, "newer");
  assert.equal(s.workingSession(node(5453), [old, asks, newer])?.id, "asks");
  assert.equal(s.workingSession(node(5453), [stopped, other, pr]), undefined);
  assert.equal(s.workingSession({ kind: "pr", ref: "github:alexec/Agents#5453" }, [old, pr])?.id, "pr");
  assert.equal(s.workingSession({ kind: "issue", ref: "linear:ENG-5" }, [agent("l", ["ENG-5"], "running")])?.id, "l");
  assert.equal(s.itemLabel({ kind: "pr", ref: "gitlab:a/b!7" }), "P!7");
  assert.equal(s.workingWords("running"), "Working");
  assert.equal(s.workingWords("waitingOnUser"), "Needs you");
});

test("Work lists Releases after Pull requests", () => {
  // Docs and Messages are in Work since #5267; Next after Issues since #5691.
  assert.deepEqual(nav.navPages.work.map((p) => p.title), ["Docs", "Messages", "Issues", "Next", "Pull requests", "Releases", "Deploys", "Timeline"]);
  // Work has no Milestones list now (#3878): an old #/work/milestone opens the Work default.
  assert.equal(nav.openPage("work", "milestone", true).id, "issue");
});
