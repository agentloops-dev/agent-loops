import { test } from "node:test";
import assert from "node:assert/strict";
import { load } from "./load.mjs";

const cycletime = await load("src/model/cycletime.ts");

function sample(cycle, coding, pr, level, repo) {
  return { cycle, coding, pr, level, repo };
}

function history() {
  const today = sample(3600, 1200, 2400, "story", "github:alexec/agents");
  const bug = sample(7200, 1800, 5400, "bug", "github:alexec/agents-site");
  const task = sample(14400, 3600, 10800, "task", "github:alexec/agents");
  return {
    now: "2026-10-12T16",
    hours: { "2026-10-12T09": { samples: [today, bug], noStart: 1 } },
    days: {
      "2026-10-12": { samples: [today, bug], noStart: 1 },
      "2026-10-10": { samples: [task], noStart: 0 },
    },
  };
}

test("W is seven days, with medians, P85, the split and the missing starts", () => {
  const chart = cycletime.cycleTimeChart(history(), "W");
  assert.equal(chart.bars.length, 7);
  assert.equal(chart.bars[6].value, 5400);
  assert.equal(chart.bars[6].count, 2);
  assert.equal(chart.bars[6].noStart, 1);
  assert.equal(chart.bars[4].value, 14400);
  assert.equal(chart.count, 3);
  assert.equal(chart.noStart, 1);
  assert.equal(chart.median, 7200);
  assert.equal(chart.p85, 12240);
  assert.equal(chart.coding, 1800);
  assert.equal(chart.pr, 5400);
  assert.equal(cycletime.splitWords(chart), "30m coding · 1.5h PR");
  assert.equal(cycletime.noStartWords(chart.noStart), "1 PR without a recorded start.");
});

test("shares can be by level or by repository", () => {
  const h = history();
  assert.deepEqual(cycletime.cycleTimeChart(h, "W", "level").shares.map((s) => [s.name, s.value]),
    [["Task", 14400], ["Bug", 7200], ["Story", 3600]]);
  assert.deepEqual(cycletime.cycleTimeChart(h, "W", "repo").shares.map((s) => [s.name, s.count]),
    [["alexec/agents", 2], ["alexec/agents-site", 1]]);
});

test("the repository picker filters samples but keeps the no-start line global", () => {
  const agents = "github:alexec/agents";
  const chart = cycletime.cycleTimeChart(history(), "W", "repo", agents);
  assert.equal(chart.repo, agents);
  assert.equal(chart.bars[6].count, 1);
  assert.equal(chart.count, 2);
  assert.equal(chart.median, 9000);
  assert.equal(chart.p85, 12780);
  assert.deepEqual(chart.shares.map((s) => s.name), ["alexec/agents"]);
  assert.deepEqual(chart.shares.map((s) => s.count), [2]);
  assert.equal(chart.noStart, 1);
  assert.equal(cycletime.noStartWords(chart.noStart, chart.repo !== null),
    "1 PR without a recorded start across all repositories.");
  assert.deepEqual(cycletime.cycleTimeRepositories(history()), [
    "github:alexec/agents",
    "github:alexec/agents-site",
  ]);
});

test("D is today by the hour", () => {
  const chart = cycletime.cycleTimeChart(history(), "D");
  assert.equal(chart.bars.length, 24);
  assert.equal(chart.bars[9].value, 5400);
  assert.equal(chart.bars[9].count, 2);
  assert.equal(chart.count, 2);
});

test("an empty range has bars but no figures", () => {
  const chart = cycletime.cycleTimeChart({ hours: {}, days: {}, now: "2026-10-12T16" }, "M");
  assert.equal(chart.bars.length, 30);
  assert.equal(chart.count, 0);
  assert.equal(chart.median, 0);
  assert.equal(cycletime.splitWords(chart), "Coding and PR time unavailable.");
  assert.equal(cycletime.noStartWords(chart.noStart), "No PRs without a recorded start.");
});

test("the words: durations and the headline", () => {
  assert.equal(cycletime.durationWords(0), "0m");
  assert.equal(cycletime.durationWords(1200), "20m");
  assert.equal(cycletime.durationWords(3600), "1h");
  assert.equal(cycletime.durationWords(5400), "1.5h");
  assert.equal(cycletime.durationWords(86400), "1 day");
  assert.equal(cycletime.durationWords(216000), "2.5 days");
  assert.equal(cycletime.cycleTimeHeadline, "Median cycle time");
});

test("a failed cycletime/history read marks the host failed, and the next read that answers clears it", async () => {
  const { Store } = await load("test/kept-entry.ts");
  let answer = null;
  const link = { onNotification: () => {}, onState: () => {},
    call: async () => { if (!answer) throw new Error("no host"); return answer; } };
  const store = new Store(link);
  await store.loadCycleTime("mac");
  assert.equal(store.cycleTimeFailed.value.mac, true);
  assert.equal(store.cycleTime.value.mac, undefined);
  answer = history();
  await store.loadCycleTime("mac");
  assert.equal(store.cycleTimeFailed.value.mac, false);
  assert.ok(store.cycleTime.value.mac);
  answer = null;
  await store.loadCycleTime("mac");
  assert.equal(store.cycleTimeFailed.value.mac, true);
  assert.ok(store.cycleTime.value.mac);
});
