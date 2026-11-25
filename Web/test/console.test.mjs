// What was the Console and Reports (#1804), each page in its section since #2141: their words and figures.
import { test } from "node:test";
import assert from "node:assert/strict";
import { load } from "./load.mjs";

const c = await load("src/model/console.ts");

test("Reports' pages are listed in Reports, in their order", async () => {
  const n = await load("src/model/nav.ts");
  // Builds sits under Delivery (#5178, #5280), a page of its own, not a report section.
  const pages = n.navPages.reports.filter((p) => p.draw.kind === "report").map((p) => p.id);
  assert.deepEqual(pages, c.reportSections.map((s) => s.id));
  const ids = n.navPages.reports.map((p) => p.id);
  assert.deepEqual(ids.slice(ids.indexOf("delivery") + 1, ids.indexOf("delivery") + 3), ["builds", "environments"]);
});

test("Reports' pages are grouped by the question each answers (#5280)", async () => {
  const n = await load("src/model/nav.ts");
  const groups = n.groupedPages(n.navPages.reports).map(([g, list]) => [g, list.map((p) => p.title)]);
  assert.deepEqual(groups, [
    ["Cost", ["Cost", "Tokens", "Time"]],
    ["Output", ["Outcomes", "Delivery", "Builds", "Environments"]], // no Summary since #5853
    ["Plan", ["Forecast", "Estimates"]],
    ["Use", ["Adoption", "Runtimes", "Workflows"]],
    ["Storage", ["Store sizes"]], // behind its flag (#6907)
    ["Saved", ["Saved Views"]],
    ["Records", ["Audit"]],
  ]);
  // Features is about the product and the machines: it is Status's now.
  assert.ok(n.navPages.status.some((p) => p.id === "features" && p.draw.kind === "report"));
  assert.ok(!n.navPages.reports.some((p) => ["value", "usage", "multiplier", "features"].includes(p.id)));
  // Each of Usage's panes is in the Me lens of the page it repeats.
  assert.equal(n.usagePaneTitle.spending, "Spend"); // #6884, was Cost
  assert.deepEqual(n.usagePanesOf("spend"), ["spending"]);
  assert.deepEqual(n.usagePanesOf("tokens"), ["tokens"]);
  assert.deepEqual(n.costTabs.map(([id]) => id), ["spend", "tokens", "time"]);
  assert.deepEqual(n.navPages.reports.filter((p) => p.tabOf).map((p) => p.id), ["tokens", "time"]);
  assert.deepEqual(n.usagePanesOf("time"), ["time"]);
  assert.deepEqual(n.usagePanesOf("outcomes"), ["velocity", "cycletime"]);
  assert.deepEqual(n.usagePanesOf("adoption"), []);
});

test("an old Reports address opens its new place (#5280)", async () => {
  const r = await load("src/route.ts");
  const moved = {
    "#/reports/value": { nav: "reports", section: "delivery" },
    "#/reports/value?range=week": { nav: "reports", section: "delivery", query: "range=week" },
    "#/reports/value/builds?repo=github:alexec/agents": { nav: "reports", section: "delivery", item: "builds", query: "repo=github:alexec/agents" },
    "#/reports/value/delivery": { nav: "reports", section: "delivery", item: "environments" },
    "#/reports/usage": { nav: "reports", section: "spend", query: "lens=me" },
    "#/reports/multiplier": { nav: "reports", section: "outcomes", query: "lens=me" },
    "#/reports/features": { nav: "status", section: "features" },
  };
  for (const [old, now] of Object.entries(moved)) {
    assert.deepEqual(r.parseRoute(old), now, old);
    assert.equal(r.isOldAddress(old), true, old);
  }
  // The Mac's Cost ▸ Open Reports (#4938) still opens Spend, in the Me lens.
  assert.deepEqual(r.parseRoute("#/reports/spend?range=week"), { nav: "reports", section: "spend", query: "range=week" });
  assert.equal(r.isOldAddress("#/reports/spend?range=week"), false);
  assert.equal(r.isOldAddress("#/reports/delivery"), false);
});

const machine = (over = {}, host = {}) => ({
  host: { id: "H1", name: "Bea's Mac", platform: "macOS arm64", version: "0.9.0", state: "online", reach: "dialOut", ...host },
  ownerName: "Bea", bot: false, ...over,
});

test("Machines: out of date is missing, needing an update or older than the control plane", () => {
  assert.equal(c.compareVersions("0.8.2", "0.9.0"), -1);
  assert.equal(c.compareVersions("0.10.0", "0.9.0"), 1);
  assert.equal(c.compareVersions("0.9", "0.9.0"), 0);
  assert.equal(c.compareVersions("dev", "0.9.0"), 0);
  assert.equal(c.outOfDate(machine({}, { version: "0.8.0" }), "0.9.0"), true);
  assert.equal(c.outOfDate(machine({}, { state: "needsUpdate" }), "0.9.0"), true);
  assert.equal(c.outOfDate(machine({}, { missing: ["host.wire/2"] }), undefined), true);
  assert.equal(c.outOfDate(machine(), "0.9.0"), false);
});

test("Machines: filters, the summary and each row's words", () => {
  const list = [machine(), machine({ ownerName: "Nightly", bot: true }, { id: "H2", state: "offline", version: "0.8.0" }),
    machine({ ownerName: "", running: 3 }, { id: "H3", workspace: { providerName: "DigitalOcean" } })];
  assert.deepEqual(c.fleetShown(list, "offline", "0.9.0").map((m) => m.host.id), ["H2"]);
  assert.deepEqual(c.fleetShown(list, "outOfDate", "0.9.0").map((m) => m.host.id), ["H2"]);
  assert.equal(c.fleetShown(list, "all", "0.9.0").length, 3);
  assert.equal(c.fleetSummary(list, "0.9.0"), "3 machines · 2 online · 1 out of date");
  assert.deepEqual(list.map(c.ownerWords), ["Bea", "Nightly (bot)", "Nobody"]);
  assert.deepEqual(list.map(c.kindWords), ["macOS arm64", "macOS arm64", "DigitalOcean"]);
  assert.deepEqual(list.map(c.runningWords), ["—", "—", "3"]);
});

test("Report Policy: budgets as typed", () => {
  assert.equal(c.budgetValue("250"), 250);
  assert.equal(c.budgetValue(" $1,200.50 "), 1200.5);
  assert.equal(c.budgetValue(""), 0);
  assert.equal(c.budgetValue("ten"), null);
  assert.equal(c.budgetValue("1.234"), null);
  assert.equal(c.budgetInput(250), "250");
  assert.equal(c.budgetInput(99.5), "99.50");
});

test("Requests: a channel identity's words", () => {
  assert.equal(c.linkWords({ identity: "U1", channel: "Slack", name: "Sam", email: "sam@acme.dev", forName: "Sam" }),
    "Sam (sam@acme.dev) on Slack wants to be linked to Sam");
  assert.equal(c.linkWords({ identity: "U1", channel: "Discord" }), "U1 on Discord wants to be linked");
});

test("Team Workflows: a bot's machines, spend and runs in words", () => {
  const bot = (over = {}) => ({ user: "b", name: "review-bot", spentTodayUSD: 4.2, capped: false, machines: 3, online: 2, workflows: [], live: [], ...over });
  assert.equal(c.machineWords(bot()), "2 of 3 machines online");
  assert.equal(c.machineWords(bot({ machines: 1, online: 1 })), "1 of 1 machine online");
  assert.equal(c.machineWords(bot({ machines: 0 })), "No machines");
  assert.equal(c.spendWords(bot({ caps: { dailyUSD: 50 } })), "$4.20 today of $50 a day");
  assert.equal(c.spendWords(bot()), "$4.20 today");
  assert.equal(c.spendWords(bot({ capped: true })), "At its cap until midnight UTC");
  assert.equal(c.runsWords(1), "1 run");
  assert.equal(c.runsWords(3), "3 runs");
  const run = { host: "h", hostName: "Bot Mac", agent: "a" };
  assert.equal(c.teamStopQuestion(bot(), { ...run, workflow: "review" }),
    "Stop review-bot's review on Bot Mac? It stops now; the workflow runs again at its next trigger.");
  assert.match(c.teamStopQuestion(bot(), run), /^Stop review-bot's agent on Bot Mac\? /);
});

test("Live Now (#2019, M9) says each agent's state and cost, and adds up the board", () => {
  const a = (agent, host, state, cost, extra = {}) => ({ host, hostName: host, owner: "p", ownerName: "Bea", bot: false, agent, runtime: "claude", state,
    costToDate: cost, ...extra });
  assert.equal(c.liveStateWords(a("A", "H", "waitingOnUser")), "Waiting for its person");
  assert.equal(c.liveStateWords(a("A", "H", "running", undefined, { waitsOnOthers: true })), "Waiting on others");
  assert.equal(c.liveStateWords(a("A", "H", "starting")), "Starting");
  assert.equal(c.liveStateWords(a("A", "H", "running")), "Working");
  assert.equal(c.liveCostWords(undefined), "Nothing yet");
  assert.match(c.liveCostWords({ USD: 4.2 }), /4\.20/);
  const board = { agents: [a("A", "H1", "running", { USD: 5 }), a("B", "H1", "running", { USD: 2.5 }), a("C", "H2", "running")] };
  assert.match(c.liveSummary(board), /^3 agents on 2 machines · .*7\.50 so far$/);
  assert.equal(c.liveSummary({ agents: [a("C", "H2", "running")] }), "1 agent on 1 machine");
  assert.deepEqual(c.liveMachines(board).map((m) => [m.host, m.agents.length]), [["H1", 2], ["H2", 1]]);
});

test("the Requests count has the people SCIM removed (#2436)", () => {
  assert.equal(c.waitingCount({ joins: 0, links: 0, capped: 0, governance: 0, budgets: 0, removals: 2 }), 2);
  assert.equal(c.waitingCount({ joins: 1, links: 1, capped: 1, governance: 1, budgets: 1, removals: 1 }), 6);
});

test("Live Now tells two agents apart by a short id, and Stop… names the one it stops (#2435)", () => {
  const a = { host: "H1", hostName: "Bea's Mac", owner: "p", ownerName: "Bea", bot: false, agent: "0f3c9a21-7d4e-4b8a-9c1d-2e5f6a7b8c9d",
    runtime: "claude", state: "running" };
  assert.equal(c.liveShortID(a), "0f3c9a21");
  assert.equal(c.liveStopLabel(a, "Claude"), "Stop Claude 0f3c9a21 on Bea's Mac");
  assert.match(c.liveStopQuestion(a, "Claude"), /^Stop Claude 0f3c9a21 on Bea's Mac \(Bea\)\? .*Bea reads that an admin stopped it\.$/);
});

// #3262: an Ask for More on Console ▸ Requests, its line, the raise it starts from, and the list after an answer.
test("an Ask for More waits on Requests until it is answered", () => {
  const sam = { id: "a", user: "u", userName: "Sam", day: "2026-11-03", at: 2, state: "waiting" };
  assert.equal(c.budgetRequestWords(sam), "Sam asks for more than today’s budget");
  assert.equal(c.budgetRequestWords({ ...sam, why: "the release" }), "Sam asks for more than today’s budget: the release");
  assert.equal(c.raiseDefault(40), "80");
  assert.equal(c.raiseDefault(undefined), "");
  const bea = { ...sam, id: "b", userName: "Bea", at: 1 };
  assert.deepEqual(c.withBudgetRequest([], sam).map((r) => r.id), ["a"]);
  assert.deepEqual(c.withBudgetRequest([sam], bea).map((r) => r.id), ["b", "a"]);
  assert.deepEqual(c.withBudgetRequest([sam, bea], { ...sam, state: "raised" }).map((r) => r.id), ["b"]);
});
