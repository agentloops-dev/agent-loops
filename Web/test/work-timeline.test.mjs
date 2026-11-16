// Work ▸ Timeline (#4206), as WorkTimelineTests has it on the window (#4173).
import { test } from "node:test";
import assert from "node:assert/strict";
import { load } from "./load.mjs";

const t = await load("src/model/workTimeline.ts");
const nav = await load("src/model/nav.ts");

const now = 1_800_000_000_000;
const h = (hours) => now + hours * 3_600_000;
// The wire's dates are seconds since 2001 (protocol/dates.ts).
const wire = (hours) => hours === undefined ? undefined : (h(hours) - Date.UTC(2001, 0, 1)) / 1000;
const drop = (o) => Object.fromEntries(Object.entries(o).filter(([, v]) => v !== undefined));

const item = (n, { feature, state = "open" } = {}) => drop({
  node: { kind: "issue", ref: `github:alexec/Agents#${n}` }, title: `Issue ${n}`, state, labels: [], assignees: [], updatedAt: 0, comments: 0, feature,
});
const child = (n, { started, closed, forecast, waits, reason } = {}) => drop({
  item: item(n, { state: closed === undefined ? "open" : "closed" }), stateReason: reason, createdAt: wire(-100),
  startedAt: wire(started), closedAt: wire(closed), waitsForPerson: waits ? true : undefined, forecastAt: wire(forecast),
});
const fc = (low, mid, high, waits) => drop({ low: wire(low), mid: wire(mid), high: wire(high), waits });
const epic = (n, children, segments, { forecast, feature = "work", reason } = {}) => drop({
  item: item(n, { feature }), stateReason: reason, createdAt: wire(-100), children,
  segments: segments.map(([s, c]) => drop({ startedAt: wire(s ?? undefined), closedAt: wire(c ?? undefined) })), forecast,
});
const axis = new t.Axis({ start: h(-24), end: h(24) }, 960);

test("the ranges put Now in the centre, and at three quarters in Week", () => {
  assert.deepEqual(t.timelineRanges.map((r) => t.rangeTitle[r]), ["1 Day", "2 Days", "Week"]);
  assert.deepEqual(t.rangeWindow("twoDays", now), { start: h(-24), end: h(24) });
  assert.deepEqual(t.rangeWindow("day", now), { start: h(-12), end: h(12) });
  // Alex, 2026-11-09: in Week, Now sits at three quarters of the width.
  assert.deepEqual(t.rangeWindow("week", now), { start: h(-126), end: h(42) });
  assert.equal(t.zoomed("twoDays", true), "day");
  assert.equal(t.zoomed("day", true), "day");
  assert.equal(t.zoomed("twoDays", false), "week");
  assert.equal(t.zoomed("week", false), "week");
});

test("the axis is linear time, ticked on the hour", () => {
  assert.equal(axis.x(h(-24)), 0);
  assert.equal(axis.x(now), 480);
  assert.equal(axis.date(960), h(24));
  const ticks = axis.ticks(2 * 86_400_000);
  assert.ok(ticks.length >= 8 && ticks.every((x) => x >= h(-24) && x <= h(24) && new Date(x).getMinutes() === 0 && new Date(x).getHours() % 6 === 0));
  const week = new t.Axis({ start: h(-126), end: h(42) }, 800).ticks(7 * 86_400_000);
  assert.ok(week.length >= 6 && week.every((x) => new Date(x).getHours() === 0));
});

test("an epic with no children is not drawn, and a line is a segment", () => {
  const lines = t.timelineLines({ epics: [
    epic(1, [], []),
    epic(2, [child(10, { started: -20, closed: -18 }), child(11, { started: -6, closed: -5 })], [[-20, -18], [-6, -5]]),
  ], releases: [] }, now);
  assert.deepEqual(lines.map((l) => l.id), ["github:alexec/Agents#2/0", "github:alexec/Agents#2/1"]);
  assert.deepEqual(lines.map((l) => l.how.how), ["joined", "joined"]);
  assert.deepEqual(lines[0].dots.map((d) => d.number), ["#10"]);
  assert.deepEqual(lines[1].dots.map((d) => d.number), ["#11"]);
  assert.equal(t.figures(lines[1]), "#2 · 2 of 2");
});

test("no dot is before its fork", () => {
  const [line] = t.timelineLines({ epics: [epic(3, [child(12, { closed: -30 }), child(13, { started: -10, closed: -2 })], [[-10, -2]])], releases: [] }, now);
  assert.ok(line.dots.every((d) => d.at >= line.start && d.at <= line.end));
});

test("an open line is forecast, waits or stops at Now", () => {
  const timeline = { epics: [
    epic(4, [child(20, { started: -8, closed: -3 }), child(21, { started: -1, forecast: 3 }), child(22, { forecast: 5 })], [[-8, null]],
      { forecast: fc(2, 5, 11) }),
    epic(5, [child(23, { started: -4 }), child(24, { waits: true })], [[-4, null]], { forecast: fc(1, 2, 3, true) }),
    epic(6, [child(25, { started: -4 })], [[-4, null]]),
    // Not started, but forecast: it forks at its first child's forecast.
    epic(7, [child(26, { forecast: 6 })], [[null, null]], { forecast: fc(5, 6, 8) }),
  ], releases: [] };
  const lines = t.timelineLines(timeline, now);
  assert.deepEqual(lines.map((l) => l.how), [{ how: "forecast" }, { how: "waits", count: 1 }, { how: "open" }, { how: "forecast" }]);
  assert.equal(lines[0].end, h(5));
  assert.deepEqual(lines[0].band, { start: h(2), end: h(11) });
  assert.deepEqual(lines[0].dots.map((d) => d.closed), [true, false, false]);
  // A start is history: no session in a turn, no line works (#4265).
  assert.ok(lines.every((l) => l.working === 0));
  assert.equal(lines[2].end, now);
  assert.equal(lines[3].start, h(6));
  // Forecast off: the past only.
  const past = t.timelineLines(timeline, now, false);
  assert.deepEqual(past.map((l) => l.how.how), ["open", "waits", "open"]);
  assert.ok(past[0].dots.every((d) => d.closed) && past[0].band === undefined);
});

test("a dropped epic ends in a cross", () => {
  const lines = t.timelineLines({ epics: [epic(8, [child(30, { started: -5, closed: -4 })], [[-5, -4]], { reason: "not_planned" })], releases: [] }, now);
  assert.deepEqual(lines.map((l) => l.how.how), ["dropped"]);
});

test("only the lines that overlap the window are drawn", () => {
  const lines = t.overlapping(t.timelineLines({ epics: [
    epic(9, [child(31, { started: -60, closed: -50 })], [[-60, -50]]),
    epic(10, [child(32, { started: -30, closed: -20 })], [[-30, -20]]),
    epic(11, [child(33, { started: -90 }), child(34, { waits: true })], [[-90, null]], { forecast: fc(-80, -80, -80, true) }),
  ], releases: [] }, now), axis.interval);
  // A line that waits for the person is drawn while it is open.
  assert.deepEqual(lines.map((l) => l.number), ["#10", "#11"]);
});

test("each block takes the first row free at its start, packed in time", () => {
  const timeline = { epics: [
    epic(12, [child(40, { started: -20, closed: -10 })], [[-20, -10]]),
    epic(13, [child(41, { started: -18, closed: -12 })], [[-18, -12]]),
    epic(14, [child(42, { started: -11, closed: -9 })], [[-11, -9]]),
    // Starts 5 h after #12 ends, more than the gap: row 0 is free again.
    epic(15, [child(43, { started: -5, closed: -1 })], [[-5, -1]]),
  ], releases: [] };
  const lines = t.layout(t.timelineLines(timeline, now));
  assert.deepEqual(lines.map((l) => l.row), [0, 1, 2, 0]);
  assert.equal(t.rowCount(lines), 3);
  assert.equal(t.rowCount([]), 0);
  // The gap is 2 % of the widest range: #14 starts 1 h after #12 ends, so it does not take row 0.
  assert.equal(t.packGap, 7 * 86_400_000 / 50);
  // A block that waits for the person holds its row while it is open.
  const waits = t.layout(t.timelineLines({ epics: [
    epic(16, [child(44, { started: -30, waits: true })], [[-30, null]]),
    epic(17, [child(45, { started: -1, closed: -0.5 })], [[-1, -0.5]]),
  ], releases: [] }, now));
  assert.deepEqual(waits.map((l) => l.row), [0, 1]);
});

test("the rows do not change between ranges for the same read", () => {
  const timeline = { epics: [
    epic(1, [child(10, { started: -150, closed: -100 })], [[-150, -100]]),
    epic(2, [child(11, { started: -120, closed: -2 })], [[-120, -2]]),
    epic(3, [child(12, { started: -90, closed: -60 })], [[-90, -60]]),
    epic(4, [child(13, { started: -20 }), child(14, { forecast: 6 })], [[-20, null]], { forecast: fc(4, 6, 10) }),
    epic(5, [child(15, { started: -5, closed: -1 })], [[-5, -1]]),
  ], releases: [] };
  const lines = t.timelineLines(timeline, now);
  // Every range's window, and its scroll on each side, is inside the span the rows are packed over.
  const span = t.packSpan(now);
  for (const r of t.timelineRanges) {
    const w = t.rangeWindow(r, now);
    assert.ok(w.start - t.rangeSpan[r] >= span.start && w.end + t.rangeSpan[r] <= span.end, r);
  }
  const rows = () => Object.fromEntries(t.layout(t.overlapping(lines, t.packSpan(now))).map((l) => [l.number, l.row]));
  assert.deepEqual(rows(), { "#1": 0, "#2": 1, "#3": 0, "#4": 0, "#5": 2 });
  // The same in 1 Day, 2 Days and Week: the packing has no range and no pixels in it.
  for (const _ of t.timelineRanges) assert.deepEqual(rows(), { "#1": 0, "#2": 1, "#3": 0, "#4": 0, "#5": 2 });
});

test("a block's title is in it, or after it when it is too narrow, cut before the next block", () => {
  const measure = (s) => Array.from(s).length * 6;
  const timeline = { epics: [
    epic(19, [child(60, { started: -9, closed: -1 })], [[-9, -1]]),
    epic(20, [child(61, { started: -24, closed: -23.5 })], [[-24, -23.5]]),
    epic(21, [child(62, { started: -20, closed: -19 })], [[-20, -19]], { reason: "not_planned" }),
  ], releases: [] };
  const [wide, narrow, dropped] = t.timelineLines(timeline, now);
  // 8 h is 160 px: "Issue 19" fits, 6 px in.
  const inside = t.blockWords(wide, axis, measure);
  assert.deepEqual([inside.inside, inside.insideX, inside.outside], ["Issue 19", axis.x(wide.start) + 6, undefined]);
  // Half an hour is 10 px: the title goes after the block, cut before the next block in the row.
  const after = t.blockWords(narrow, axis, measure);
  assert.deepEqual([after.inside, after.outside, after.outsideX], [undefined, "Issue 20", axis.x(narrow.end) + 6]);
  assert.equal(t.blockWords(narrow, axis, measure, { next: h(-21.6) }).outside, "Iss…");
  assert.equal(t.blockWords(narrow, axis, measure, { next: h(-23.4) }).outside, undefined);
  // Closed as not planned: "✕ closed as not planned" after the block.
  assert.equal(t.blockWords(dropped, axis, measure).outside, "Issue 21  ✕ closed as not planned");
  // A block that starts before the left edge keeps its title at the edge, after ←, cut to what is left of the block.
  const edge = t.blockWords(wide, axis, measure, { left: axis.x(h(-4)) });
  assert.deepEqual([edge.arrow, edge.insideX, edge.inside], [true, axis.x(h(-4)) + 6, "← Issue…"]);
  // The wait pill goes first: the words after it.
  assert.equal(t.blockWords(narrow, axis, measure, { trailing: 30 }).outsideX, axis.x(narrow.end) + 42);
  assert.equal(t.cut("Issue 19", 0, measure), undefined);
});

test("white text has 4.5:1 on every feature's colour", () => {
  const white = [255, 255, 255];
  for (const hex of ["#007aff", "#ff9500", "#34c759", "#30b0c7", "#00c7be", "#32ade6", "#8e8e93"]) {
    const dark = t.forWhiteText(t.rgbOf(hex));
    assert.ok(t.contrast(dark, white) >= 4.5, hex);
    assert.ok(dark.every((v, i) => v <= t.rgbOf(hex)[i]), hex);
  }
  // A colour that has it already stays as it is.
  assert.deepEqual(t.forWhiteText([88, 86, 214]), [88, 86, 214]);
  assert.equal(t.hexOf(t.rgbOf("#30b0c7")), "#30b0c7");
});

test("a feature keeps its colour, the window's", () => {
  assert.equal(t.colourIndex("work", 12), t.colourIndex("Work", 12));
  // FNV-1a of "work", as the window's WorkTimeline.colour has it.
  assert.equal(t.colourIndex("work", 12), 8);
  assert.equal(t.colourIndex(undefined, 12), 11);
  assert.ok(t.colourIndex("autonomy", 12) >= 0 && t.colourIndex("autonomy", 12) < 11);
});

test("the hit is the nearest dot, then the block", () => {
  const timeline = { epics: [epic(21, [child(80, { started: -10, closed: -6 })], [[-10, -2]])], releases: [] };
  const lines = t.layout(t.timelineLines(timeline, now));
  const at = (x, yy) => t.hit({ x, y: yy }, lines, axis);
  assert.equal(at(axis.x(h(-6)) + 3, t.dotY(0))?.dot?.number, "#80");
  assert.equal(at(axis.x(h(-4)), t.rowTop(0) + 4)?.line?.number, "#21");
  assert.equal(at(axis.x(h(-4)), t.rowTop(0) + 4)?.kind, "line");
  assert.equal(at(axis.x(h(-4)), t.rowTop(1) + 4), undefined);
  assert.equal(at(axis.x(h(0)), t.rowTop(0) + 4), undefined);
});

test("a dot is held inside its block", () => {
  const timeline = { epics: [epic(23, [child(92, { started: -10, closed: -10 }), child(93, { started: -9, closed: -2 })], [[-10, -2]])], releases: [] };
  const [line] = t.layout(t.timelineLines(timeline, now));
  const [first, last] = line.dots;
  assert.equal(t.dotX(first, line, axis), axis.x(line.start) + 4);
  assert.equal(t.dotX(last, line, axis), axis.x(line.end) - 4);
  assert.equal(t.hit({ x: axis.x(line.start) + 4, y: t.dotY(line.row) }, [line], axis)?.dot?.number, "#92");
});

test("the hover card is a row of three lines: the title, the labels, the prediction", () => {
  const labelled = epic(24, [child(94, { started: -3, closed: -1 }), child(95, { forecast: 2 })], [[-3, null]], { forecast: fc(1, 2, 4) });
  labelled.item.labels = [{ name: "enhancement", color: "a2eeef" }];
  labelled.children[0].item.labels = [{ name: "bug" }];
  const timeline = { epics: [labelled], releases: [], backtest: { error: 2.16, width: 1.3, cuts: 69, holds: 55, epics: 21, at: wire(0) } };
  const lines = t.timelineLines(timeline, now);
  const row = t.cardRow({ kind: "line", line: lines[0] }, timeline, now);
  assert.deepEqual([row.state, row.title], ["open", "Issue 24"]);
  assert.deepEqual(row.labels.map((l) => l.name), ["enhancement"]);
  assert.match(row.line, /^#24 · 1 of 2 · closes about .*–.*, 8 in 10$/);
  // How past forecasts did is the title line's help, not the card's.
  assert.doesNotMatch(row.line, /Past forecasts/);
  const dot = t.cardRow({ kind: "dot", line: lines[0], dot: lines[0].dots[0] }, timeline, now);
  assert.deepEqual([dot.state, dot.title, dot.labels.map((l) => l.name), dot.line], ["closed", "Issue 94", ["bug"], "#94 · closed 1 h ago"]);
  assert.equal(t.titleWords("Agents", t.epicCount(lines, t.rangeWindow("twoDays", now)), "twoDays"), "Agents · 1 epic in these 2 days");
  assert.equal(t.titleWords("", 6, "week"), "6 epics in these 7 days");
});

test("the words say about", () => {
  const backtest = { error: 2.16, width: 1.3, cuts: 69, holds: 55, epics: 21, at: wire(0) };
  const [line] = t.timelineLines({ epics: [
    epic(22, [child(90, { started: -3, closed: -1 }), child(91, { forecast: 2 })], [[-3, null]], { forecast: fc(1, 2, 4) }),
  ], releases: [] }, now);
  const words = t.joinWords(line, backtest, now);
  assert.equal(words.length, 3);
  assert.match(words[0], /^Closes about /);
  assert.match(words[1], /^8 times in 10 between /);
  assert.equal(words[2], "Past forecasts were off by ×2.2");
  assert.equal(t.waitWords(3), "3 wait for you");
  assert.equal(t.waitWords(1), "1 waits for you");
  assert.deepEqual(t.lineWords(line, undefined, now).slice(0, 2), ["#22 Issue 22", "1 of 2 done"]);
  assert.equal(t.dotWords(line.dots[0], now)[0], "#90 Issue 90");
});

test("under 700 px the epics are a list, in the order they close", () => {
  const timeline = { epics: [
    epic(30, [child(1, { started: -5, closed: -2 })], [[-5, -2]]),
    epic(31, [child(2, { started: -3, closed: -1 }), child(3, { forecast: 3 })], [[-3, null]], { forecast: fc(2, 3, 5) }),
    epic(32, [child(4, { started: -2, waits: true }), child(5, { started: -2, waits: true })], [[-2, null]]),
    epic(33, [child(6, { started: -4, closed: -3 })], [[-4, -3]]),
    epic(34, [child(7, { started: -1, closed: -0.5 }), child(8, { forecast: 1 })], [[-1, null]], { forecast: fc(0.5, 1, 2) }),
  ], releases: [] };
  const rows = t.epicRows(t.timelineLines(timeline, now), t.rangeWindow("twoDays", now), now);
  assert.deepEqual(rows.map((r) => r.line.number), ["#34", "#31", "#32", "#30", "#33"]);
  assert.match(rows[0].line2, /^closes about .+ · .+–.+$/);
  assert.equal(rows[2].line2, "2 wait for you");
  assert.match(rows[3].line2, /^joined /);
  assert.equal(rows[1].line.done, 1);
  assert.equal(rows[1].line.total, 2);
});

test("an empty window says when the last epic joined, and the week is a click away", () => {
  const timeline = { epics: [
    epic(40, [child(1, { started: -100, closed: -96 })], [[-100, -96]]),
    epic(41, [child(2, { started: -80, closed: -50 })], [[-80, -50]]),
  ], releases: [] };
  assert.equal(t.epicRows(t.timelineLines(timeline, now), t.rangeWindow("twoDays", now), now).length, 0);
  assert.equal(t.lastJoined(timeline, h(-24)), h(-50));
  assert.equal(t.lastJoined(timeline, h(-200)), undefined);
  assert.equal(t.noEpicsWords("twoDays"), "No epic in these 2 days");
  assert.equal(t.noEpicsWords("day"), "No epic in these 24 hours");
  assert.match(t.lastJoinedWords(h(-96), now), /^The last one joined 4 ?d ago\.$/);
  assert.equal(t.showTheWeek, "Show the week");
  assert.equal(t.noProjectTitle, "No project to draw");
});

test("machines merge each epic and release once", () => {
  const a = { epics: [epic(23, [child(1, { closed: -1 })], [[-2, -1]])], releases: [{ tag: "v2", at: wire(-1), repo: "a/b" }], errors: ["x"] };
  const b = { epics: [epic(23, [child(1, { closed: -1 })], [[-2, -1]])],
    releases: [{ tag: "v1", at: wire(-3), repo: "a/b" }, { tag: "v2", at: wire(-1), repo: "a/b" }], errors: ["x", "y"] };
  const merged = t.mergeTimelines([a, b]);
  assert.equal(merged.epics.length, 1);
  assert.deepEqual(merged.releases.map((r) => r.tag), ["v1", "v2"]);
  assert.deepEqual(merged.errors, ["x", "y"]);
});

// #4276: a session on another machine, from the control plane's copy, starts a child this machine
// had no start for, and an earlier one moves a start and its line back; as the window's test.
test("a line works only while a session is in a turn on it", () => {
  const timeline = { epics: [
    epic(1, [child(2, { started: -10, closed: -6 }), child(3, { started: -2 })], [[-10, -6], [-3, null]]),
    epic(4, [child(5, { started: -2 })], [[-2, null]]),
    epic(6, [child(7, { started: -2 })], [[-2, null]]),
  ], releases: [] };
  const ref = (n) => `github:alexec/Agents#${n}`;
  const on = (n, state, id = crypto.randomUUID()) => ({ node: { kind: "session", ref: `session:${id}` }, host: "BEAS-MAC",
    on: [{ kind: "issue", ref: ref(n) }], at: wire(-1), state: { state, at: wire(-1) } });
  const agent = (label, state) => ({ id: crypto.randomUUID().toUpperCase(), runtimeID: "claude", cwd: "file:///tmp/work", state,
    labels: [{ value: label, owner: "agent", addedAt: 0 }] });
  // Here: a lead on #4 between turns, and a lane on #7 in one. There: a lane on #3 in a turn, one on #5
  // waiting, and one that is #4's lead, whose old copy still says working.
  const lead = agent("#4", "finished");
  const working = t.workingNow(timeline, [on(3, "working"), on(5, "waiting"), on(4, "working", lead.id.toLowerCase())],
    [lead, agent("#7", "running"), agent("#9", "running")]);
  assert.deepEqual([...working].sort(), [ref(3), ref(7)]);
  // #1's closed segment does not work; its open one does.
  assert.deepEqual(t.timelineLines(timeline, now, true, working).map((l) => l.working), [0, 1, 0, 1]);
  // The epic's own session counts too: a lead in a turn.
  assert.deepEqual(t.timelineLines(timeline, now, true, new Set([ref(4)])).map((l) => l.working), [0, 0, 1, 0]);
  assert.equal(t.workingNow(timeline, [], []).size, 0);
});

test("sessions on every machine start the children", () => {
  const timeline = { epics: [epic(1, [child(3, { started: -10, closed: -5 }), child(2), child(4, { started: -3 })], [[-10, -5], [-3, null]])], releases: [] };
  const on = (n, hours) => ({ node: { kind: "session", ref: `session:${n}-${hours}` }, host: "BEAS-MAC",
    on: [{ kind: "issue", ref: `github:alexec/Agents#${n}` }], at: wire(hours) });
  const got = t.startedBySessions(timeline, [on(2, -4), on(3, -12), on(4, -1), on(9, -50)]);
  // #3 started earlier on Bea's machine; #2 started there; #4 started here first.
  assert.deepEqual(got.epics[0].children.map((c) => c.startedAt), [wire(-12), wire(-4), wire(-3)]);
  assert.deepEqual(got.epics[0].segments.map((s) => s.startedAt), [wire(-12), wire(-4)]);
  assert.equal(t.startedBySessions(timeline, []), timeline);
  assert.deepEqual(t.sessionNodes(got).map((n) => n.ref), [1, 3, 2, 4].map((n) => `github:alexec/Agents#${n}`));
  // The machines' read is left as it was.
  assert.equal(timeline.epics[0].children[1].startedAt, undefined);
});

test("a child that closed since the last read lands", () => {
  const before = { epics: [epic(24, [child(2, { started: -2 }), child(3, { started: -1 })], [[-2, null]])], releases: [] };
  const after = { epics: [epic(24, [child(2, { started: -2, closed: 0 }), child(3, { started: -1 })], [[-2, null]]),
    epic(25, [child(4, { started: -2, closed: -1 })], [[-2, -1]])], releases: [] };
  // A child the page had not seen yet does not pulse.
  assert.deepEqual(t.landed(before, after), ["github:alexec/Agents#2"]);
});

test("Timeline is a row after Releases and Deploys in Work", () => {
  const ids = nav.navPages.work.map((p) => p.id);
  assert.deepEqual(ids.slice(-3), ["release", "deploys", "timeline"]);
  assert.deepEqual(nav.navPages.work.at(-1).draw, { kind: "timeline" });
});
