// Work ▸ Timeline (#4206), as the window's WorkTimeline in AgentsKitCore (#4173): each epic a filled
// block in its own row, from when its first child starts to when its last child closes, on a linear
// time axis (#5506, Alex, 2026-11-14). The range, the blocks, their rows, the marks and the words; the
// view draws them in SVG. Times are milliseconds since 1970.
import type { Agent, ForgeLabel, LineageNode, LineageSessionOn, WireDate, WorkTimeline, WorkTimelineBacktest } from "../protocol/generated";
import { fromWireDate } from "../protocol/dates";
import { shortAge, shortRef } from "./work";
import { sessionIdOf } from "../protocol/ref";

const ms = (d: number | undefined) => d === undefined ? undefined : fromWireDate(d as never).getTime();
const day = 86_400_000;

/** The toolbar's range. 1 Day and 2 Days keep Now in the centre; Week puts it at three quarters, as
 * the forecast reaches only about 1.5 days ahead (Alex, 2026-11-09, #4173). */
export type TimelineRange = "day" | "twoDays" | "week";
export const timelineRanges: readonly TimelineRange[] = ["day", "twoDays", "week"];
export const rangeTitle: Record<TimelineRange, string> = { day: "1 Day", twoDays: "2 Days", week: "Week" };
export const rangeSpan: Record<TimelineRange, number> = { day, twoDays: 2 * day, week: 7 * day };

/** Where Now is, from the left edge, as a share of the width. */
export const nowAt = (r: TimelineRange) => r === "week" ? 0.75 : 0.5;

/** The window seen when the page opens or Now is clicked. */
export function rangeWindow(r: TimelineRange, now: number): Interval {
  const start = now - rangeSpan[r] * nowAt(r);
  return { start, end: start + rangeSpan[r] };
}

/** A pinch, or ctrl and the wheel, steps the range: apart is a shorter one, together a longer one. */
export function zoomed(r: TimelineRange, zoomIn: boolean): TimelineRange {
  const i = timelineRanges.indexOf(r) + (zoomIn ? -1 : 1);
  return timelineRanges[Math.min(Math.max(i, 0), timelineRanges.length - 1)]!;
}

export interface Interval { start: number; end: number }

/** A time axis: `interval` across `width` pixels. */
export class Axis {
  constructor(readonly interval: Interval, readonly width: number) {}

  x(t: number): number {
    const d = this.interval.end - this.interval.start;
    return d <= 0 ? 0 : (t - this.interval.start) / d * this.width;
  }

  date(x: number): number {
    return this.interval.start + (this.width <= 0 ? 0 : x / this.width * (this.interval.end - this.interval.start));
  }

  /** The hour ticks for a range seen across the width: every 3 hours at 1 Day, 6 at 2 Days, 24 at
   * Week, on the hour of the page's clock. */
  ticks(seen: number): number[] {
    const hours = seen <= day * 1.5 ? 3 : seen <= day * 3 ? 6 : 24;
    const hour = new Date(this.interval.start);
    hour.setMinutes(0, 0, 0);
    const out: number[] = [];
    // Each hour on the clock, so a tick stays on the hour across a change of summer time.
    while (hour.getTime() <= this.interval.end) {
      if (hour.getTime() >= this.interval.start && hour.getHours() % hours === 0) out.push(hour.getTime());
      hour.setHours(hour.getHours() + 1);
    }
    return out;
  }
}

/** How an epic's block ends: joined (its last child closed), dropped (closed as not planned: grey, then
 * ✕), forecast (a lighter tint with a dashed edge after Now), waits (children wait for the person: a
 * pill after it), open (nothing to forecast: it stops at Now). */
export type TimelineEnd = { how: "joined" } | { how: "dropped" } | { how: "forecast" } | { how: "waits"; count: number } | { how: "open" };

/** A child's dot on its line: filled when closed, hollow when forecast. */
export interface TimelineDot { ref: string; number: string; title: string; at: number; closed: boolean; notPlanned: boolean }

/** One line: a segment of an epic. */
export interface TimelineLine {
  id: string;
  epic: string;
  segment: number;
  title: string;
  number: string;
  feature: string | undefined;
  /** The epic's children closed, and all of them. */
  done: number;
  total: number;
  start: number;
  /** The join, the forecast join, the pill, or Now. */
  end: number;
  how: TimelineEnd;
  band: Interval | undefined;
  dots: TimelineDot[];
  /** The epic and its open children that a session is in a turn on now (#4265): the line pulses and a
   * ring turns at Now while it is above 0. */
  working: number;
  /** The row, 0 at the top (#5506). */
  row: number;
}

/** The figures of a hover card: `#n · 6 of 8`. A block shows its title only (Alex, 2026-11-14). */
export const figures = (l: TimelineLine) => `${l.number} · ${l.done} of ${l.total}`;
/** Whether a line runs on (it has not ended). */
export const isOpen = (l: TimelineLine) => l.how.how === "forecast" || l.how.how === "waits" || l.how.how === "open";

/** The lines of a timeline, past and forecast, as section 7 says: one per segment, an epic with no
 * children not drawn, a line that has not started not drawn unless it is forecast. `forecast` off
 * draws the past only: an open line stops at Now. `working` is the refs a session is in a turn on now
 * (`workingNow`): only the open line of their epic works. */
export function timelineLines(timeline: WorkTimeline, now: number, forecast = true,
  working: ReadonlySet<string> = new Set()): TimelineLine[] {
  return timeline.epics.flatMap((epic) => {
    if (epic.children.length === 0) return [];
    const total = epic.children.length;
    const done = epic.children.filter((c) => c.closedAt !== undefined).length;
    const last = epic.segments.length - 1;
    return epic.segments.flatMap((segment, i): TimelineLine[] => {
      const open = segment.closedAt === undefined && i === last;
      // The children of this line: those that closed in it, and the open ones on the open line.
      const next = i < last ? ms(epic.segments[i + 1]!.startedAt) : undefined;
      const mine = epic.children.filter((child) => {
        const closed = ms(child.closedAt);
        if (closed !== undefined) {
          const from = i === 0 ? -Infinity : ms(segment.startedAt) ?? -Infinity;
          return closed >= from && closed < (next ?? Infinity);
        }
        return open;
      });
      const forecastStarts = mine.flatMap((c) => c.forecastAt === undefined ? [] : [ms(c.forecastAt)!]);
      const start = ms(segment.startedAt) ?? (forecast && forecastStarts.length > 0 ? Math.min(...forecastStarts) : undefined);
      if (start === undefined) return [];
      let how: TimelineEnd = { how: "joined" };
      let end = ms(segment.closedAt) ?? now;
      let band: Interval | undefined;
      const waiting = mine.filter((c) => c.closedAt === undefined && c.waitsForPerson === true).length;
      const f = epic.forecast;
      if (open) {
        if (waiting > 0) {
          how = { how: "waits", count: waiting };
          if (forecast && f) end = Math.max(ms(f.mid)!, now);
        } else if (forecast && f) {
          how = { how: "forecast" };
          end = Math.max(ms(f.mid)!, now);
          const low = ms(f.low)!, high = ms(f.high)!;
          band = { start: Math.min(low, high), end: Math.max(low, high) };
        } else {
          how = { how: "open" };
        }
      } else if (epic.stateReason === "not_planned" || (epic.closedAt !== undefined && epic.item.state === "closed"
                 && mine.every((c) => c.stateReason === "not_planned"))) {
        how = { how: "dropped" };
      }
      end = Math.max(end, start);
      const dots = mine.flatMap((child): TimelineDot[] => {
        const number = shortRef(child.item.node.ref);
        const base = { ref: child.item.node.ref, number, title: child.item.title };
        const closed = ms(child.closedAt);
        // A child closed before its line forked sits at the fork (section 12, item 7).
        if (closed !== undefined) {
          return [{ ...base, at: Math.min(Math.max(closed, start), end), closed: true, notPlanned: child.stateReason === "not_planned" }];
        }
        const at = ms(child.forecastAt);
        if (!forecast || child.waitsForPerson === true || at === undefined) return [];
        return [{ ...base, at: Math.min(Math.max(at, Math.max(start, now)), end), closed: false, notPlanned: false }];
      });
      const worked = open
        ? mine.filter((c) => c.closedAt === undefined && working.has(c.item.node.ref)).length + (working.has(epic.item.node.ref) ? 1 : 0)
        : 0;
      return [{
        id: `${epic.item.node.ref}/${i}`, epic: epic.item.node.ref, segment: i, title: epic.item.title,
        number: shortRef(epic.item.node.ref), feature: epic.item.feature, done, total, start, end, how, band,
        dots: dots.sort((a, b) => a.at - b.at), working: worked, row: 0,
      }];
    });
  });
}

/** Where a block's drawing ends: its end or its band's, whichever is later. */
export const reach = (l: TimelineLine) => Math.max(l.end, l.band?.end ?? l.end);

/** Only the lines that overlap the window (Alex, 2026-11-08). A line that waits for the person has
 * no end, so it is drawn while it is open. */
export function overlapping(lines: readonly TimelineLine[], window: Interval): TimelineLine[] {
  return lines.filter((l) => l.start <= window.end && (l.how.how === "waits" ? Infinity : reach(l)) >= window.start);
}

/** The widest range: the rows are packed over its span, so they are the same in every range (#5506). */
const widest: TimelineRange = "week";

/** The gap after a block in its row, in time: 2 % of the widest range (#5506). */
export const packGap = rangeSpan[widest] / 50;

/** What a read covers and the rows are packed over: the widest range's window around `anchor`, and its width on
 * each side to scroll through. It holds every narrower range's window around the same anchor, so a change of range
 * packs the same epics the same way (#5506). */
export function packSpan(anchor: number): Interval {
  const w = rangeWindow(widest, anchor);
  return { start: w.start - rangeSpan[widest], end: w.end + rangeSpan[widest] };
}

const byStart = (a: TimelineLine, b: TimelineLine) => a.start - b.start || (a.id < b.id ? -1 : a.id > b.id ? 1 : 0);

/** Put each block in the first row that is free at its start, sorted by start, then by id (#5506). It packs in time,
 * not in pixels: a row is busy until the block's reach and `gap` after it, so the rows do not change with the range. A
 * block that waits for the person has no end: it holds its row while it is open. A label outside a block takes no room. */
export function layout(lines: readonly TimelineLine[], gap = packGap): TimelineLine[] {
  const busy: number[] = [];
  return [...lines].sort(byStart).map((line) => {
    let row = 0;
    while ((busy[row] ?? -Infinity) > line.start) row++;
    busy[row] = line.how.how === "waits" ? Infinity : reach(line) + gap;
    return { ...line, row };
  });
}

/** The rows in use. */
export const rowCount = (lines: readonly TimelineLine[]) => Math.max(0, ...lines.map((l) => l.row + 1));

/** A block's geometry, in pixels (#5506): 28 high, 8 between rows, the first row 12 from the top. */
export const blockHeight = 28;
export const rowGap = 8;
export const plotTop = 12;
/** A row's top. */
export const rowTop = (row: number) => plotTop + row * (blockHeight + rowGap);
/** Where the dots go: in the block's lower half. */
export const dotY = (row: number) => rowTop(row) + blockHeight - 7;
/** Where the title goes: in the block's upper half. */
export const titleY = (row: number) => rowTop(row) + 11;
/** The title's inset from the block's leading edge. */
export const titleInset = 6;

/** The next block's start in the same row, after `line`'s: a label outside `line` stops before it. */
export function nextInRow(lines: readonly TimelineLine[], line: TimelineLine): number | undefined {
  const after = lines.filter((l) => l.row === line.row && l.start > line.start).map((l) => l.start);
  return after.length > 0 ? Math.min(...after) : undefined;
}

/** Where a dot is drawn: at its time, held inside its block. */
export function dotX(dot: TimelineDot, line: TimelineLine, axis: Axis): number {
  const sx = axis.x(line.start), ex = Math.max(axis.x(line.end), sx);
  const from = sx + 4;
  return Math.min(Math.max(axis.x(dot.at), from), Math.max(ex - 4, from));
}

/** `text` whole when it fits `room`, else cut short with "…", else undefined. */
export function cut(text: string, room: number, measure: (s: string) => number): string | undefined {
  if (measure(text) <= room) return text;
  const chars = Array.from(text);
  let best: string | undefined;
  let low = 1;
  let high = chars.length - 1;
  while (low <= high) {
    const mid = Math.floor((low + high) / 2);
    const c = chars.slice(0, mid).join("").trim();
    if (c && measure(`${c}…`) <= room) {
      best = `${c}…`;
      low = mid + 1;
    } else {
      high = mid - 1;
    }
  }
  return best;
}

/** What a closed-as-not-planned block says after it. */
export const droppedWords = "✕ closed as not planned";

/** A block's words (#5506). `inside`: its title in it, from `insideX` (held at the visible left edge, with ←, when the
 * block starts before it). `outside`: what goes after the block, from `outsideX`: the title when the block is too
 * narrow for it, and "✕ closed as not planned"; cut with "…" before the next block in the row. `trailing` is the
 * width of what is drawn after the block first (the wait pill). */
export interface BlockWords { inside: string | undefined; insideX: number; arrow: boolean; outside: string | undefined; outsideX: number }

export function blockWords(line: TimelineLine, axis: Axis, measure: (s: string) => number,
                           { next, left = -Infinity, trailing = 0 }: { next?: number | undefined; left?: number; trailing?: number } = {}): BlockWords {
  const sx = axis.x(line.start), ex = Math.max(axis.x(line.end), sx);
  const fits = measure(line.title) + 2 * titleInset <= ex - sx;
  const arrow = fits && sx < left;
  const insideX = (arrow ? left : sx) + titleInset;
  const inside = fits ? cut(arrow ? `← ${line.title}` : line.title, ex - insideX - titleInset, measure) : undefined;
  const outsideX = axis.x(reach(line)) + 6 + (trailing > 0 ? trailing + 6 : 0);
  const words = [fits ? "" : line.title, line.how.how === "dropped" ? droppedWords : ""].filter((w) => w).join("  ");
  const room = (next === undefined ? Infinity : axis.x(next) - 6) - outsideX;
  return { inside, insideX, arrow, outside: words ? cut(words, room, measure) : undefined, outsideX };
}

// Colours

/** A feature's colour, as an index into the page's palette, the same every time for the same name
 * (FNV-1a of its lower-cased UTF-8, as the window has it). No feature takes the last one (grey). */
export function colourIndex(feature: string | undefined, count: number): number {
  if (!feature || count <= 1) return Math.max(count - 1, 0);
  let hash = 2_166_136_261;
  for (const byte of new TextEncoder().encode(feature.toLowerCase())) hash = Math.imul(hash ^ byte, 16_777_619) >>> 0;
  return hash % (count - 1);
}

/** The WCAG contrast of two sRGB colours, each 0–255. */
export function contrast(a: readonly number[], b: readonly number[]): number {
  const lum = (c: readonly number[]) => {
    const [r, g, bl] = c.map((v) => { const x = v / 255; return x <= 0.04045 ? x / 12.92 : ((x + 0.055) / 1.055) ** 2.4; });
    return 0.2126 * r! + 0.7152 * g! + 0.0722 * bl!;
  };
  const [hi, lo] = [lum(a), lum(b)].sort((x, y) => y - x);
  return (hi! + 0.05) / (lo! + 0.05);
}

/** A feature's colour darkened, a step towards black at a time, until white text on it has 4.5:1 (#5506): orange
 * becomes a burnt orange, teal a deep teal; a colour that has it already stays. */
export function forWhiteText(rgb: readonly number[]): number[] {
  const white = [255, 255, 255];
  let c = [...rgb];
  for (let k = 0; k < 40 && contrast(c, white) < 4.5; k++) c = c.map((v) => v * 0.97);
  return c.map((v) => Math.round(v));
}

/** `#rrggbb` as [r, g, b], and back. */
export const rgbOf = (hex: string) => [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16));
export const hexOf = (rgb: readonly number[]) => `#${rgb.map((v) => Math.round(v).toString(16).padStart(2, "0")).join("")}`;

// Hit testing

/** What the pointer is on: a dot within 6 px, else a block (with its band and pill, a few pixels around it). */
export type TimelineHit =
  | { kind: "dot"; line: TimelineLine; dot: TimelineDot }
  | { kind: "line"; line: TimelineLine };

export function hit(p: { x: number; y: number }, lines: readonly TimelineLine[], axis: Axis): TimelineHit | undefined {
  let best: [number, TimelineHit] | undefined;
  for (const line of lines) {
    for (const dot of line.dots) {
      const d = Math.hypot(dotX(dot, line, axis) - p.x, dotY(line.row) - p.y);
      if (d <= 6 && d < (best?.[0] ?? Infinity)) best = [d, { kind: "dot", line, dot }];
    }
  }
  if (best) return best[1];
  const line = lines.find((l) => {
    const top = rowTop(l.row);
    return p.y >= top && p.y <= top + blockHeight && p.x >= axis.x(l.start) - 2 && p.x <= axis.x(reach(l)) + 2;
  });
  return line ? { kind: "line", line } : undefined;
}

// Words

const sameDay = (a: number, b: number) => new Date(a).toDateString() === new Date(b).toDateString();

/** A time on the page's clock: "6:38 PM" today, "Sun 6:38 PM" on another day. */
export function timeWords(t: number, now: number): string {
  return new Date(t).toLocaleString(undefined, sameDay(t, now)
    ? { hour: "numeric", minute: "2-digit" } : { weekday: "short", hour: "numeric", minute: "2-digit" });
}

/** "3 wait for you": the hover card and the list, where a block waits for the person; its pill says 👤 and the
 * number only (Alex, 2026-11-14). */
export const waitWords = (count: number) => `${count} wait${count === 1 ? "s" : ""} for you`;

/** "Past forecasts were off by ×2.2". */
export const errorWords = (b: WorkTimelineBacktest) => `Past forecasts were off by ×${b.error.toFixed(1)}`;

/** The words for a forecast end (section 12, item 8). */
export function joinWords(line: TimelineLine, backtest: WorkTimelineBacktest | undefined, now: number): string[] {
  const out = [`Closes about ${timeWords(line.end, now)}`];
  if (line.band) out.push(`8 times in 10 between ${timeWords(line.band.start, now)} and ${timeWords(line.band.end, now)}`);
  if (backtest) out.push(errorWords(backtest));
  return out;
}

/** The hover card on a dot. */
export function dotWords(dot: TimelineDot, now: number): string[] {
  const when = timeWords(dot.at, now);
  return [`${dot.number} ${dot.title}`, dot.closed ? (dot.notPlanned ? `Closed as not planned ${when}` : `Closed ${when}`) : `Closes about ${when}`];
}

/** The hover card on a line. */
export function lineWords(line: TimelineLine, backtest: WorkTimelineBacktest | undefined, now: number): string[] {
  const out = [`${line.number} ${line.title}`, `${line.done} of ${line.total} done`];
  switch (line.how.how) {
    case "joined": out.push(`Closed ${timeWords(line.end, now)}`); break;
    case "dropped": out.push("Closed as not planned"); break;
    case "forecast": out.push(...joinWords(line, backtest, now)); break;
    case "waits": { const w = waitWords(line.how.count); out.push(w.charAt(0).toUpperCase() + w.slice(1)); break; }
    case "open": out.push("Open"); break;
  }
  return out;
}

/** A row of the hover card, on three lines (Alex, 2026-11-10): the state glyph and the title; the label
 * chips, where the wire has them; then the prediction, in the window's words. No cost (Alex, 2026-11-14). */
export interface CardRow { state: "open" | "closed"; title: string; labels: ForgeLabel[]; line: string }

/** "2 h ago", or "just now". */
function agoWords(t: number, now: number): string {
  const age = shortAge((now - t) / 1000);
  return age === "now" ? "just now" : `${age} ago`;
}

/** On a dot: "#4250 · closed 2 h ago" or "#4250 · closes about 5:00 PM". */
export function dotLine(dot: TimelineDot, now: number): string {
  const state = dot.closed ? `${dot.notPlanned ? "closed as not planned" : "closed"} ${agoWords(dot.at, now)}` : `closes about ${timeWords(dot.at, now)}`;
  return `${dot.number} · ${state}`;
}

/** On an epic's block: "#4250 · 11 of 16 · closes about 5:00 PM · 3:00 PM–9:00 PM, 8 in 10". */
export function lineLine(line: TimelineLine, now: number): string {
  const out = [figures(line)];
  switch (line.how.how) {
    case "joined": out.push(`closed ${agoWords(line.end, now)}`); break;
    case "dropped": out.push("closed as not planned"); break;
    case "forecast":
      out.push(`closes about ${timeWords(line.end, now)}`);
      if (line.band) out.push(`${timeWords(line.band.start, now)}–${timeWords(line.band.end, now)}, 8 in 10`);
      break;
    case "waits": out.push(waitWords(line.how.count)); break;
    case "open": out.push("open"); break;
  }
  return out.join(" · ");
}

/** The hover card's row, for a dot or a block. The backtest is not in it: it is the title line's help. */
export function cardRow(h: TimelineHit, timeline: WorkTimeline, now: number): CardRow {
  const lineRow = (l: TimelineLine): CardRow => {
    const epic = timeline.epics.find((e) => e.item.node.ref === l.epic);
    return { state: (epic?.item.state ?? (isOpen(l) ? "open" : "closed")) === "open" ? "open" : "closed", title: l.title,
      labels: epic?.item.labels ?? [], line: lineLine(l, now) };
  };
  switch (h.kind) {
    case "dot": {
      const child = timeline.epics.flatMap((e) => e.children).find((c) => c.item.node.ref === h.dot.ref);
      return { state: h.dot.closed ? "closed" : "open", title: h.dot.title, labels: child?.item.labels ?? [], line: dotLine(h.dot, now) };
    }
    case "line": return lineRow(h.line);
  }
}

/** The epics with a line in `window`. */
export const epicCount = (lines: readonly TimelineLine[], window: Interval) => new Set(overlapping(lines, window).map((l) => l.epic)).size;

/** The window in words: "these 2 days". */
const these: Record<TimelineRange, string> = { day: "these 24 hours", twoDays: "these 2 days", week: "these 7 days" };

/** The title line above the plot (note 32): "Agents · 6 epics in these 2 days"; no project when a
 * menu names it. Its help is the forecast's backtest (Alex, 2026-11-10). */
export function titleWords(project: string, epics: number, r: TimelineRange): string {
  const count = `${epics} epic${epics === 1 ? "" : "s"} in ${these[r]}`;
  return project ? `${project} · ${count}` : count;
}

/** Several machines' timelines of one project as one: each epic and release once. */
export function mergeTimelines(timelines: readonly WorkTimeline[]): WorkTimeline {
  const out: WorkTimeline = { epics: [], releases: [] };
  const epics = new Set<string>(), releases = new Set<string>();
  for (const t of timelines) {
    out.epics.push(...t.epics.filter((e) => !epics.has(e.item.node.ref) && epics.add(e.item.node.ref)));
    out.releases.push(...t.releases.filter((r) => !releases.has(`${r.repo}@${r.tag}`) && releases.add(`${r.repo}@${r.tag}`)));
    if (out.timeZone === undefined && t.timeZone !== undefined) out.timeZone = t.timeZone;
    if (out.backtest === undefined && t.backtest !== undefined) out.backtest = t.backtest;
    for (const e of t.errors ?? []) if (!(out.errors ?? []).includes(e)) out.errors = [...out.errors ?? [], e];
  }
  out.releases.sort((a, b) => a.at - b.at);
  return out;
}

/**
 * The children's starts with every machine's sessions on them (#4276, `lineage/sessionsOn`), so a
 * child a sleeping machine's session started still starts then: a child starts at the earliest of
 * its own start, its close and its first session's link, and its line no later. As the window's
 * `WorkTimeline.started`.
 */
export function startedBySessions(timeline: WorkTimeline, sessions: readonly LineageSessionOn[]): WorkTimeline {
  const first = new Map<string, WireDate>();
  for (const s of sessions) for (const n of s.on) first.set(n.ref, Math.min(first.get(n.ref) ?? s.at, s.at) as WireDate);
  if (first.size === 0) return timeline;
  return {
    ...timeline,
    epics: timeline.epics.map((epic) => {
      const children = epic.children.map((c) => ({ ...c }));
      const segments = epic.segments.map((s) => ({ ...s }));
      const last = segments.length - 1;
      for (const child of children) {
        const at = first.get(child.item.node.ref);
        if (at === undefined) continue;
        const start = Math.min(child.startedAt ?? at, child.closedAt ?? at, at) as WireDate;
        if (start === child.startedAt) continue;
        child.startedAt = start;
        if (last < 0) continue;
        // Its line, as timelineLines finds it: an open child is on the last; a closed one on the line it closed in.
        let i = last;
        const closed = child.closedAt;
        if (closed !== undefined) i = segments.findIndex((_, k) => k === last || closed < (segments[k + 1]!.startedAt ?? Infinity));
        const segment = segments[i]!;
        if (segment.startedAt === undefined || start < segment.startedAt) segment.startedAt = start;
      }
      return { ...epic, children, segments };
    }),
  };
}

/**
 * The epics and children a session is in a turn on now (#4265), by ref: every machine's sessions from
 * the control plane's copy whose state is working, and the page's own sessions that are running with a
 * `#<n>` label of one. A session that waits (on an event, on CI, on the person) is not in a turn, so it
 * does not count. A session the page knows goes by its own state, which is newer than the copy's. As
 * the window's `WorkTimeline.workingNow`.
 */
export function workingNow(timeline: WorkTimeline, sessions: readonly LineageSessionOn[], agents: readonly Agent[]): Set<string> {
  // By the session's id: its ref's topic is its project's, which the page does not know (#4800).
  const known = new Set(agents.map((a) => a.id.toUpperCase()));
  const out = new Set(sessions.filter((s) => s.state?.state === "working" && !known.has((sessionIdOf(s.node.ref) ?? s.node.ref).toUpperCase()))
    .flatMap((s) => s.on.map((n) => n.ref)));
  const labels = new Set(agents.filter((a) => a.state === "running" || a.state === "starting")
    .flatMap((a) => (a.labels ?? []).map((l) => l.value)));
  if (labels.size === 0) return out;
  for (const epic of timeline.epics) {
    for (const node of [epic.item.node, ...epic.children.map((c) => c.item.node)]) if (labels.has(shortRef(node.ref))) out.add(node.ref);
  }
  return out;
}

/** The nodes a timeline asks `lineage/sessionsOn` for: its epics and their children, each once, at most 500. */
export function sessionNodes(timeline: WorkTimeline): LineageNode[] {
  const seen = new Set<string>();
  return timeline.epics.flatMap((e) => [e.item.node, ...e.children.map((c) => c.item.node)])
    .filter((n) => !seen.has(n.ref) && seen.add(n.ref)).slice(0, 500);
}

/** The children that closed since the last read of the same epics: their dots pulse once. */
export function landed(before: WorkTimeline, after: WorkTimeline): string[] {
  const was = new Set(before.epics.flatMap((e) => e.children).filter((c) => c.closedAt !== undefined).map((c) => c.item.node.ref));
  const known = new Set(before.epics.flatMap((e) => e.children).map((c) => c.item.node.ref));
  return after.epics.flatMap((e) => e.children)
    .filter((c) => c.closedAt !== undefined && !was.has(c.item.node.ref) && known.has(c.item.node.ref)).map((c) => c.item.node.ref);
}

/** A row of the list of epics (note 36): the Timeline under 700 px, and what a screen reader reads over it. */
export interface EpicRow { line: TimelineLine; line2: string }

/** The list of epics in the window (note 36), in the order they close: the forecast first (the soonest first), then
 * those that wait for the person, then those still open, then those that joined (the latest first). Line 2: "closes
 * about 5:00 PM · 3:00 PM–9:00 PM", "3 wait for you", "joined 9:40 AM". */
export function epicRows(lines: readonly TimelineLine[], window: Interval, now: number): EpicRow[] {
  const rank = (l: TimelineLine) => ({ forecast: 0, waits: 1, open: 2, joined: 3, dropped: 3 })[l.how.how];
  const line2 = (l: TimelineLine): string => {
    switch (l.how.how) {
      case "forecast": return [`closes about ${timeWords(l.end, now)}`,
        ...l.band ? [`${timeWords(l.band.start, now)}–${timeWords(l.band.end, now)}`] : []].join(" · ");
      case "waits": return waitWords(l.how.count);
      case "open": return "open";
      case "joined": return `joined ${timeWords(l.end, now)}`;
      case "dropped": return "closed as not planned";
    }
  };
  return overlapping(lines, window)
    .sort((a, b) => rank(a) - rank(b) || (rank(a) === 3 ? b.end - a.end : a.end - b.end) || a.title.localeCompare(b.title))
    .map((line) => ({ line, line2: line2(line) }));
}

/** When the last line before `at` joined, for the empty card (S11); undefined when the read has none. */
export function lastJoined(timeline: WorkTimeline, at: number): number | undefined {
  const joined = timeline.epics.flatMap((e) => e.segments).flatMap((s) => { const c = ms(s.closedAt); return c !== undefined && c < at ? [c] : []; });
  return joined.length > 0 ? Math.max(...joined) : undefined;
}

/** The empty card (S11): "No epic in these 2 days", and "The last one joined 4 d ago." when known. */
export const noEpicsWords = (r: TimelineRange) => `No epic in ${these[r]}`;
export const lastJoinedWords = (t: number, now: number) => `The last one joined ${agoWords(t, now)}.`;
export const showTheWeek = "Show the week";
/** S12: no project to draw. */
export const noProjectTitle = "No project to draw";
export const noProject = "Timeline shows a project’s epics. Add a folder to a project to see them.";
export const noMachine = "No machine draws the timeline yet. Update Agent Loops on a machine to see it.";
