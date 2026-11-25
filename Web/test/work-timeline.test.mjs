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
// `created` is when it was filed: by default when it started (or closed), so the ghost adds nothing.
const child = (n, { created, started, closed, waits, reason } = {}) => drop({
  item: item(n, { state: closed === undefined ? "open" : "closed" }), stateReason: reason,
  createdAt: wire(created ?? started ?? closed ?? -100),
  startedAt: wire(started), closedAt: wire(closed), waitsForPerson: waits ? true : undefined,
});
const fc = (low, mid, high, waits) => drop({ low: wire(low), mid: wire(mid), high: wire(high), waits });
// By default created with its first child, and closed with its last once all are.
const epic = (n, children, { forecast, feature = "work", reason, created, closed } = {}) => {
  const closes = children.flatMap((c) => c.closedAt ?? []);
  const closedAt = wire(closed) ?? (closes.length === children.length && closes.length > 0 ? Math.max(...closes) : undefined);
  return drop({ item: item(n, { feature, state: closedAt === undefined ? "open" : "closed" }), stateReason: reason,
    createdAt: wire(created) ?? Math.min(...children.map((c) => c.createdAt), wire(0)), closedAt, children, segments: [], forecast });
};
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

test("an epic with no children is not drawn, and an epic is one block (#7058)", () => {
  const lines = t.timelineLines({ epics: [
    epic(1, []),
    // A pause in the work: one block, solid over the pause.
    epic(2, [child(10, { started: -20, closed: -18 }), child(11, { started: -6, closed: -5 })]),
  ], releases: [] }, now);
  assert.deepEqual(lines.map((l) => l.id), ["github:alexec/Agents#2"]);
  assert.equal(lines[0].how.how, "joined");
  assert.deepEqual(lines[0].work, { start: h(-20), end: h(-5) });
  assert.equal(t.figures(lines[0]), "#2 · 2 of 2");
});

test("only an open epic has a ghost, to its predicted finish; none on hold is shown (#7058)", () => {
  const forecast = fc(2, 5, 11);
  const held = (e) => ({ ...e, item: { ...e.item, onHold: true } });
  const timeline = { epics: [
    // Created before its children and closed after them: its dates draw nothing.
    epic(1, [child(10, { created: -20, started: -18, closed: -10 })], { created: -30, closed: -4 }),
    epic(2, [child(11, { started: -8, closed: -5 }), child(12)], { created: -9, forecast }),
    // Not started: the ghost only, from Now.
    epic(3, [child(13), child(14)], { created: -15, forecast }),
    // Not started, and nothing to forecast: nothing to draw.
    epic(4, [child(15)], { created: -15 }),
    epic(5, [child(16, { started: -12, closed: -7 })], { created: -14, closed: -6, reason: "not_planned" }),
    // On hold: never shown, open or closed.
    held(epic(6, [child(17, { started: -3 })], { forecast })),
    held(epic(7, [child(18, { started: -3, closed: -2 })])),
  ], releases: [] };
  const span = (a, b) => ({ start: h(a), end: h(b) });
  const lines = t.timelineLines(timeline, now);
  assert.deepEqual(lines.map((l) => l.number), ["#1", "#2", "#3", "#5"]);
  assert.deepEqual(lines.map((l) => l.work), [span(-18, -10), span(-8, -5), undefined, span(-12, -7)]);
  assert.deepEqual(lines.map((l) => l.ghost), [undefined, span(-5, 5), span(0, 5), undefined]);
  assert.deepEqual(lines.map((l) => l.from), [-18, -8, 0, -12].map(h));
  assert.deepEqual(lines.map((l) => l.end), [-10, 5, 5, -7].map(h));
  assert.deepEqual(lines.map((l) => l.how.how), ["joined", "forecast", "forecast", "dropped"]);
  // The words say when the epic closed, not when its work stopped.
  assert.equal(t.lineLine(lines[0], now), "#1 · 1 of 1 · closed 4 h ago");
  // Nothing started, so nothing stopped.
  assert.equal(t.stoppedAt(lines[2], now), undefined);
  assert.equal(t.lastJoined(timeline, h(-1)), h(-4));
  // A session in a turn: solid to Now, the ghost from there. One on an epic not started: still a ghost.
  const working = t.timelineLines(timeline, now, true, new Set([3, 5, 12].map((n) => `github:alexec/Agents#${n}`)));
  assert.deepEqual(working.map((l) => l.working), [0, 1, 1, 0]);
  assert.deepEqual([working[1].work, working[1].ghost, working[2].work], [span(-8, 0), span(0, 5), undefined]);
  // Forecast off: the past only.
  assert.deepEqual(t.timelineLines(timeline, now, false).map((l) => l.number), ["#1", "#2", "#5"]);
});

test("an open line is forecast, waits or stops at Now", () => {
  const timeline = { epics: [
    epic(4, [child(20, { started: -8, closed: -3 }), child(21, { started: -1 }), child(22)],
      { forecast: fc(2, 5, 11) }),
    epic(5, [child(23, { started: -4 }), child(24, { waits: true })], { forecast: fc(1, 2, 3, true) }),
    epic(6, [child(25, { started: -4 })]),
    // Not started, but forecast: a ghost from Now to the forecast.
    epic(7, [child(26)], { created: -3, forecast: fc(5, 6, 8) }),
  ], releases: [] };
  const lines = t.timelineLines(timeline, now);
  assert.deepEqual(lines.map((l) => l.how), [{ how: "forecast" }, { how: "waits", count: 1 }, { how: "open" }, { how: "forecast" }]);
  assert.equal(lines[0].end, h(5));
  assert.deepEqual(lines[0].band, { start: h(2), end: h(11) });
  // A start is history: no session in a turn, no line works (#4265).
  assert.ok(lines.every((l) => l.working === 0));
  assert.equal(lines[2].end, h(-4));
  assert.deepEqual([lines[3].from, lines[3].end, lines[3].work], [now, h(6), undefined]);
  // Forecast off: the past only.
  const past = t.timelineLines(timeline, now, false);
  assert.deepEqual(past.map((l) => l.how.how), ["open", "waits", "open"]);
  assert.ok(past.every((l) => l.band === undefined && l.ghost === undefined && l.end === l.work.end));
});

test("a block is solid from started to stopped (#6043)", () => {
  const timeline = { epics: [
    // Open, nobody on it, its last close 3 h before Now: solid to there.
    epic(1, [child(10, { created: -12, started: -10, closed: -3 }), child(11, { created: -9 })]),
    epic(2, [child(12, { created: -30, started: -20 }), child(13, { started: -6 })]),
  ], releases: [] };
  const [stopped, filed] = t.timelineLines(timeline, now);
  assert.deepEqual([stopped.from, stopped.end, stopped.ghost], [h(-10), h(-3), undefined]);
  assert.equal(t.stoppedAt(stopped, now), h(-3));
  assert.equal(t.lineLine(stopped, now), "#1 · 1 of 2 · stopped 3 h ago · open");
  assert.deepEqual(t.lineWords(stopped, undefined, now).slice(2), ["Open", "Stopped 3 h ago"]);
  assert.deepEqual(filed.work, { start: h(-20), end: h(-6) });
  // A session in a turn on it: the work did not stop, solid to Now.
  const working = t.timelineLines(timeline, now, true, new Set(["github:alexec/Agents#11"]))[0];
  assert.equal(working.work.end, now);
  assert.equal(t.lineLine(working, now), "#1 · 1 of 2 · open");
  // The block, its words and its hit start where the work started.
  assert.equal(t.blockWords(filed, axis, (s) => s.length * 6).insideX, axis.x(h(-20)) + 6);
  assert.equal(t.hit({ x: axis.x(h(-10)), y: t.rowTop(filed.row) + 4 }, [filed], axis)?.number, "#2");
});

test("a dropped epic ends in a cross", () => {
  const lines = t.timelineLines({ epics: [epic(8, [child(30, { started: -5, closed: -4 })], { reason: "not_planned" })], releases: [] }, now);
  assert.deepEqual(lines.map((l) => l.how.how), ["dropped"]);
});

test("only the lines that overlap the window are drawn", () => {
  const lines = t.overlapping(t.timelineLines({ epics: [
    epic(9, [child(31, { started: -60, closed: -50 })]),
    epic(10, [child(32, { started: -30, closed: -20 })]),
    epic(11, [child(33, { started: -90 }), child(34, { waits: true })], { forecast: fc(-80, -80, -80, true) }),
  ], releases: [] }, now), axis.interval);
  // A line that waits for the person is drawn while it is open.
  assert.deepEqual(lines.map((l) => l.number), ["#10", "#11"]);
});

test("each block takes the first row free at its start, packed in time", () => {
  const timeline = { epics: [
    epic(12, [child(40, { started: -20, closed: -10 })]),
    epic(13, [child(41, { started: -18, closed: -12 })]),
    epic(14, [child(42, { started: -11, closed: -9 })]),
    // Starts 5 h after #12 ends, more than the gap: row 0 is free again.
    epic(15, [child(43, { started: -5, closed: -1 })]),
  ], releases: [] };
  const lines = t.layout(t.timelineLines(timeline, now));
  assert.deepEqual(lines.map((l) => l.row), [0, 1, 2, 0]);
  assert.equal(t.rowCount(lines), 3);
  assert.equal(t.rowCount([]), 0);
  // The gap is 2 % of the widest range: #14 starts 1 h after #12 ends, so it does not take row 0.
  assert.equal(t.packGap, 7 * 86_400_000 / 50);
  // A block that waits for the person holds its row while it is open.
  const waits = t.layout(t.timelineLines({ epics: [
    epic(16, [child(44, { started: -30, waits: true })]),
    epic(17, [child(45, { started: -1, closed: -0.5 })]),
  ], releases: [] }, now));
  assert.deepEqual(waits.map((l) => l.row), [0, 1]);
});

test("each priority is a band of rows, High at the top, and none on hold (#6093, #7058)", () => {
  const with_ = (e, priority, onHold) => ({ ...e, item: { ...e.item, ...priority ? { priority } : {}, ...onHold ? { onHold } : {} } });
  const timeline = { epics: [
    with_(epic(30, [child(50, { started: -20, closed: -10 })])),
    // A High epic that starts later goes above a none one that starts first.
    with_(epic(31, [child(51, { started: -5, closed: -1 })]), "high"),
    with_(epic(32, [child(52, { started: -18, closed: -12 })]), "high"),
    with_(epic(33, [child(53, { started: -15, closed: -2 })]), "low"),
    with_(epic(34, [child(54, { started: -15 }), child(55)], { forecast: fc(4, 6, 10) }), "high", true),
  ], releases: [] };
  const lines = t.layout(t.timelineLines(timeline, now));
  const rowOf = Object.fromEntries(lines.map((l) => [l.number, l.row]));
  // High packs in its own rows (#32 then #31 in row 0), none next, then Low.
  assert.deepEqual(rowOf, { "#31": 0, "#32": 0, "#30": 1, "#33": 2 });
  assert.deepEqual(t.bands(lines).map((b) => [b.words, b.row, b.rows]), [["High", 0, 1], ["None", 1, 1], ["Low", 2, 1]]);
  assert.equal(t.bandRuleY(1), t.rowTop(1) - t.rowGap / 2);
  assert.deepEqual(lines.map((l) => t.chipWords(l)), lines.map((l) => ({ "#31": "High", "#32": "High", "#33": "Low" })[l.number]));
  // The list: High first.
  const listed = t.epicRows(lines, { start: h(-48), end: h(48) }, now).map((r) => r.line.number);
  assert.deepEqual(listed.slice(0, 2).sort(), ["#31", "#32"]);
  assert.deepEqual(listed.slice(2), ["#30", "#33"]);
});

test("the priority chip goes after the title in the block, else after the block, else nowhere (#6093)", () => {
  const measure = (s) => s.length * 6;
  const high = (e) => ({ ...e, item: { ...e.item, priority: "high" } });
  const [wide] = t.layout(t.timelineLines({ epics: [high(epic(35, [child(56, { started: -20, closed: -2 })]))], releases: [] }, now));
  const inside = t.blockWords(wide, axis, measure);
  assert.deepEqual([inside.chip, inside.chipInside, inside.chipX], ["High", true, inside.insideX + measure(inside.inside) + t.chipGap]);
  const [narrow] = t.layout(t.timelineLines({ epics: [high(epic(36, [child(57, { started: -2, closed: -1.5 })]))], releases: [] }, now));
  const after = t.blockWords(narrow, axis, measure);
  assert.deepEqual([after.chip, after.chipInside, after.outside], ["High", undefined, "Issue 36"]);
  assert.equal(after.chipX, after.outsideX + measure("Issue 36") + t.chipGap);
  // No room before the next block: no chip.
  const cut = t.blockWords(narrow, axis, measure, { next: narrow.end + 60_000 });
  assert.equal(cut.chip, undefined);
  // None has no chip.
  const [plain] = t.layout(t.timelineLines({ epics: [epic(37, [child(58, { started: -20, closed: -2 })])], releases: [] }, now));
  assert.equal(t.blockWords(plain, axis, measure).chip, undefined);
});

test("the rows do not change between ranges for the same read", () => {
  const timeline = { epics: [
    epic(1, [child(10, { started: -150, closed: -100 })]),
    epic(2, [child(11, { started: -120, closed: -2 })]),
    epic(3, [child(12, { started: -90, closed: -60 })]),
    epic(4, [child(13, { started: -20 }), child(14)], { forecast: fc(4, 6, 10) }),
    epic(5, [child(15, { started: -5, closed: -1 })]),
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
    epic(19, [child(60, { started: -9, closed: -1 })]),
    epic(20, [child(61, { started: -24, closed: -23.5 })]),
    epic(21, [child(62, { started: -20, closed: -19 })], { reason: "not_planned" }),
  ], releases: [] };
  const [wide, narrow, dropped] = t.timelineLines(timeline, now);
  // 8 h is 160 px: "Issue 19" fits, 6 px in.
  const inside = t.blockWords(wide, axis, measure);
  assert.deepEqual([inside.inside, inside.insideX, inside.outside], ["Issue 19", axis.x(wide.from) + 6, undefined]);
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

test("the hit is the block", () => {
  const lines = t.layout(t.timelineLines({ epics: [epic(21, [child(80, { started: -10, closed: -2 })])], releases: [] }, now));
  const at = (x, yy) => t.hit({ x, y: yy }, lines, axis)?.number;
  assert.equal(at(axis.x(h(-4)), t.rowTop(0) + 4), "#21");
  assert.equal(at(axis.x(h(-4)), t.rowTop(1) + 4), undefined);
  assert.equal(at(axis.x(h(0)), t.rowTop(0) + 4), undefined);
  // The title is in the middle of the block (#7058: no dots under it).
  assert.equal(t.titleY(0), t.rowTop(0) + t.blockHeight / 2);
});

test("the hover card is a row of three lines: the title, the labels, the prediction", () => {
  const labelled = epic(24, [child(94, { started: -3, closed: -1 }), child(95)], { forecast: fc(1, 2, 4) });
  labelled.item.labels = [{ name: "enhancement", color: "a2eeef" }];
  const timeline = { epics: [labelled], releases: [], backtest: { error: 2.16, width: 1.3, cuts: 69, holds: 55, epics: 21, at: wire(0) } };
  const lines = t.timelineLines(timeline, now);
  const row = t.cardRow(lines[0], timeline, now);
  assert.deepEqual([row.state, row.title], ["open", "Issue 24"]);
  assert.deepEqual(row.labels.map((l) => l.name), ["enhancement"]);
  assert.match(row.line, /^#24 · 1 of 2 · stopped 1 h ago · closes about .*–.*, 8 in 10$/);
  // How past forecasts did is the title line's help, not the card's.
  assert.doesNotMatch(row.line, /Past forecasts/);
  assert.equal(t.titleWords("Agents", t.epicCount(lines, t.rangeWindow("twoDays", now)), "twoDays"), "Agents · 1 epic in these 2 days");
  assert.equal(t.titleWords("", 6, "week"), "6 epics in these 7 days");
});

test("the words say about", () => {
  const backtest = { error: 2.16, width: 1.3, cuts: 69, holds: 55, epics: 21, at: wire(0) };
  const [line] = t.timelineLines({ epics: [
    epic(22, [child(90, { started: -3, closed: -1 }), child(91)], { forecast: fc(1, 2, 4) }),
  ], releases: [] }, now);
  const words = t.joinWords(line, backtest, now);
  assert.equal(words.length, 3);
  assert.match(words[0], /^Closes about /);
  assert.match(words[1], /^8 times in 10 between /);
  assert.equal(words[2], "Past forecasts were off by ×2.2");
  assert.equal(t.waitWords(3), "3 wait for you");
  assert.equal(t.waitWords(1), "1 waits for you");
  assert.deepEqual(t.lineWords(line, undefined, now).slice(0, 2), ["#22 Issue 22", "1 of 2 done"]);
});

test("under 700 px the epics are a list, in the order they close", () => {
  const timeline = { epics: [
    epic(30, [child(1, { started: -5, closed: -2 })]),
    epic(31, [child(2, { started: -3, closed: -1 }), child(3)], { forecast: fc(2, 3, 5) }),
    epic(32, [child(4, { started: -2, waits: true }), child(5, { started: -2, waits: true })]),
    epic(33, [child(6, { started: -4, closed: -3 })]),
    epic(34, [child(7, { started: -1, closed: -0.5 }), child(8)], { forecast: fc(0.5, 1, 2) }),
  ], releases: [] };
  const rows = t.epicRows(t.timelineLines(timeline, now), t.rangeWindow("twoDays", now), now);
  assert.deepEqual(rows.map((r) => r.line.number), ["#34", "#31", "#32", "#30", "#33"]);
  assert.match(rows[0].line2, /^closes about .+ · .+–.+$/);
  // Nothing closed since they started 2 h ago, and no session is in a turn on them: the work stopped (#6043).
  assert.equal(rows[2].line2, "2 wait for you · stopped 2 h ago");
  assert.match(rows[3].line2, /^joined /);
  assert.equal(rows[1].line.done, 1);
  assert.equal(rows[1].line.total, 2);
});

test("an empty window says when the last epic joined, and the week is a click away", () => {
  const timeline = { epics: [
    epic(40, [child(1, { started: -100, closed: -96 })]),
    epic(41, [child(2, { started: -80, closed: -50 })]),
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

test("the window is loading, not empty, until every read ended (#5925)", () => {
  const win = t.rangeWindow("twoDays", now);
  const rows = (timeline) => t.epicRows(t.timelineLines(timeline, now), win, now).length;
  // Two reads: the first machine answers with no epic, the second with one.
  const first = { epics: [], releases: [] };
  const both = t.mergeTimelines([first, { epics: [epic(42, [child(1, { started: -2 })])], releases: [] }]);
  assert.equal(t.timelineShows(false, true, 0), "loading");
  assert.equal(t.timelineShows(true, true, rows(first)), "loading");
  assert.equal(t.timelineShows(true, true, rows(both)), "epics");
  assert.equal(t.timelineShows(true, false, rows(both)), "epics");
  // Only once every read ended does an empty window say so (S11).
  assert.equal(t.timelineShows(true, false, rows(first)), "empty");
});

test("a first answer keeps the forecasts on screen until its own come (#7027)", () => {
  const forecast = { low: wire(1), mid: wire(2), high: wire(3) };
  const first = { epics: [epic(1, [child(2, { started: -2 })]), epic(3, [child(4, { started: -2 })]), epic(5, [child(6, { closed: -1 })])], releases: [], partial: true };
  const shown = { epics: [epic(1, [child(2, { started: -2 })], { forecast }), epic(5, [child(6, { started: -2 })], { forecast })], releases: [] };
  assert.deepEqual(t.keepForecasts(first, shown).epics.map((e) => e.forecast), [forecast, undefined, undefined]);
  assert.equal(t.keepForecasts(first, null), first);
});

test("machines merge each epic and release once", () => {
  const a = { epics: [epic(23, [child(1, { closed: -1 })])], releases: [{ tag: "v2", at: wire(-1), repo: "a/b" }], errors: ["x"] };
  const b = { epics: [epic(23, [child(1, { closed: -1 })])],
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
    epic(1, [child(2, { started: -10, closed: -6 }), child(3, { started: -2 })]),
    epic(4, [child(5, { started: -2 })]),
    epic(6, [child(7, { started: -2 })]),
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
  assert.deepEqual(t.timelineLines(timeline, now, true, working).map((l) => l.working), [1, 0, 1]);
  // The epic's own session counts too: a lead in a turn.
  assert.deepEqual(t.timelineLines(timeline, now, true, new Set([ref(4)])).map((l) => l.working), [0, 1, 0]);
  assert.equal(t.workingNow(timeline, [], []).size, 0);
});

test("sessions on every machine start the children", () => {
  const timeline = { epics: [epic(1, [child(3, { started: -10, closed: -5 }), child(2), child(4, { started: -3 })])], releases: [] };
  const on = (n, hours) => ({ node: { kind: "session", ref: `session:${n}-${hours}` }, host: "BEAS-MAC",
    on: [{ kind: "issue", ref: `github:alexec/Agents#${n}` }], at: wire(hours) });
  const got = t.startedBySessions(timeline, [on(2, -4), on(3, -12), on(4, -1), on(9, -50)]);
  // #3 started earlier on Bea's machine; #2 started there; #4 started here first.
  assert.deepEqual(got.epics[0].children.map((c) => c.startedAt), [wire(-12), wire(-4), wire(-3)]);
  assert.equal(t.timelineLines(got, now)[0].work.start, h(-12));
  assert.equal(t.startedBySessions(timeline, []), timeline);
  assert.deepEqual(t.sessionNodes(got).map((n) => n.ref), [1, 3, 2, 4].map((n) => `github:alexec/Agents#${n}`));
  // The machines' read is left as it was.
  assert.equal(timeline.epics[0].children[1].startedAt, undefined);
});

test("Timeline is a row after Releases and Deploys in Work", () => {
  const ids = nav.navPages.work.map((p) => p.id);
  assert.deepEqual(ids.slice(-3), ["release", "deploys", "timeline"]);
  assert.deepEqual(nav.navPages.work.at(-1).draw, { kind: "timeline" });
});

// #7029: each repo is asked of one machine, and of the next when it fails or is late; as the window's test.
test("a read asks one machine per repo, and the next when one fails", () => {
  const shop = ["github:a/shop"], web = ["github:a/web"];
  const all = [["a", shop], ["b", shop], ["c", shop], ["d", web]];
  let r = new t.Readers(["a", "b", "c", "d"], all);
  assert.deepEqual(r.next(), ["a", "d"]);
  r.done("a", shop);
  r.done("d", web);
  r.late();
  assert.deepEqual(r.next(), []);
  assert.deepEqual([...r.scopes.keys()].sort(), ["a", "b", "c", "d"]);
  // One fails: the next only, and then the one after it.
  r = new t.Readers(["a", "b", "c", "d"], all);
  r.next();
  r.done("a", undefined);
  assert.deepEqual(r.next(), ["b"]);
  r.done("b", undefined);
  assert.deepEqual(r.next(), ["c"]);
  // Late with no first answer; one that gave its first is not late.
  r = new t.Readers(["a", "b", "d"], all);
  r.next();
  r.first("d");
  r.late();
  assert.deepEqual(r.next(), ["b"]);
  // Not known, or empty: always asked. One that no longer reads a repo no longer covers it.
  assert.deepEqual(new t.Readers(["a", "b"], []).next(), ["a", "b"]);
  assert.deepEqual(new t.Readers(["a", "b"], [["a", []], ["b", shop]]).next(), ["a", "b"]);
  r = new t.Readers(["a", "b"], [["a", [...shop, ...web]], ["b", web]]);
  assert.deepEqual(r.next(), ["a"]);
  r.done("a", shop);
  assert.deepEqual(r.next(), ["b"]);
});
