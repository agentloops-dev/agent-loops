// Work ▸ Timeline (#4206), as the window's WorkTimeline in AgentsKitCore (#4173): each epic one block in its own row
// on a linear time axis (#5506, #7058): solid from when its first child started to when the work stopped (#6043), then
// for an open epic a ghost (the lighter tint with a dashed edge) to its predicted finish; one that has not started has
// only that ghost, from Now.
// No individual issue is drawn, and an epic on hold only with the On hold toggle on (Alex, 2026-11-24, 2026-11-25; #7335). The range, the blocks, their rows, the marks and the words; the
// view draws them in SVG. Times are milliseconds since 1970.
import type { Agent, ForgeItem, ForgeLabel, LineageNode, LineageSessionOn, WireDate, WorkTimeline, WorkTimelineBacktest } from "../protocol/generated";
import { fromWireDate } from "../protocol/dates";
import { shortAge, shortRef } from "./work";
import { oneEach } from "./workLists";
import { nextRank, onHoldWords } from "./forgeNextOrder";
import { priorityWords } from "./epicPlan";
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

/** How an epic's block ends: joined (the epic closed: it ends where its work stopped), dropped (closed as not planned: grey, then
 * ✕), forecast (a lighter tint with a dashed edge after Now), waits (children wait for the person: a
 * pill after it), open (nothing to forecast: it ends where its work stopped), held (on hold, #7335: dimmed, with "On hold"
 * after it, and no forecast). */
export type TimelineEnd = { how: "joined" } | { how: "dropped" } | { how: "forecast" } | { how: "waits"; count: number } | { how: "open" }
  | { how: "held" };

/** One line: an epic's block (#7058). */
export interface TimelineLine {
  id: string;
  epic: string;
  title: string;
  number: string;
  feature: string | undefined;
  /** The epic's children closed, and all of them. */
  done: number;
  total: number;
  /** The solid span (#6043): from when its first child started to when the work stopped (its last child's close, or Now
   * while a session is in a turn on it); undefined while no child has started. */
  work: Interval | undefined;
  /** An open epic's dashed ghost (#7058): from when its work stopped (Now, while no child has started) to its predicted
   * finish; undefined with no forecast. */
  ghost: Interval | undefined;
  /** Where the block starts and ends: its solid span and its ghost. */
  from: number;
  end: number;
  /** When the epic closed. */
  closedAt: number | undefined;
  how: TimelineEnd;
  band: Interval | undefined;
  /** The epic and its open children that a session is in a turn on now (#4265): the line pulses and a
   * ring turns at Now while it is above 0. */
  working: number;
  /** The epic's priority (ForgeItem.priority; absent for none): it gives its band (#6089, #6093). */
  priority?: ForgeItem["priority"] | undefined;
  /** The row, 0 at the top (#5506). */
  row: number;
}

/** The band of the epics on hold (#7335): below the four priorities. */
export const heldBand = 4;

/** A priority's band (#6089), the top first: High 0, Medium 1, none 2, Low 3; an epic on hold's is `heldBand` (#7335). */
export const priorityBand = (l: Pick<TimelineLine, "priority"> & { how?: TimelineEnd }) =>
  l.how?.how === "held" ? heldBand : nextRank({ priority: l.priority });

/** A block's chip, after its title (#6089): High, Medium or Low; undefined for none. */
export const chipWords = (l: Pick<TimelineLine, "priority">) => priorityWords(l.priority);

/** A band's name at the plot's leading edge (#6089, #7335). */
export const bandWords = (band: number) => ["High", "Medium", "None", "Low", onHoldWords][band] ?? "None";

/** How wide the mark of an epic on hold with no work yet is, in pixels (#7335): a short dimmed mark at Now. */
export const heldMarkWidth = 10;

/** The width in pixels that a block is drawn past its end: an epic on hold with no work yet's mark (#7335), else 0. */
export const markWidth = (l: Pick<TimelineLine, "how" | "work">) => l.how.how === "held" && l.work === undefined ? heldMarkWidth : 0;

/** The figures of a hover card: `#n · 6 of 8`. A block shows its title only (Alex, 2026-11-14). */
export const figures = (l: TimelineLine) => `${l.number} · ${l.done} of ${l.total}`;
/** Whether a line runs on (it has not ended). */
export const isOpen = (l: TimelineLine) => l.how.how === "forecast" || l.how.how === "waits" || l.how.how === "open" || l.how.how === "held";
/** When the work stopped, on an open block where that was before Now (a minute's slack): the hover card says so (#6043). */
export const stoppedAt = (l: TimelineLine, now: number) => isOpen(l) && l.work && l.work.end < now - 60_000 ? l.work.end : undefined;

/** The lines of a timeline, past and forecast: one per epic (#7058). Not drawn: an epic with no children, one on hold
 * unless `held` (the On hold toggle, #7335), and one that has not started and has no forecast. `forecast` off draws the
 * past only. `working` is the refs a session is in a turn on now (`workingNow`): only an open epic works. An open epic
 * on hold has its solid span and no forecast; with no work yet, it is a mark at Now. */
export function timelineLines(timeline: WorkTimeline, now: number, forecast = true,
  working: ReadonlySet<string> = new Set(), held = false): TimelineLine[] {
  return timeline.epics.flatMap((epic): TimelineLine[] => {
    const children = epic.children;
    const open = epic.closedAt === undefined;
    const onHold = epic.item.onHold === true;
    if (onHold && !(held && open)) return [];
    if (children.length === 0 && !onHold) return [];
    const worked = !open ? 0
      : children.filter((c) => c.closedAt === undefined && working.has(c.item.node.ref)).length + (working.has(epic.item.node.ref) ? 1 : 0);
    const times = (dates: (WireDate | undefined)[]) => dates.map(ms).filter((t): t is number => t !== undefined);
    const starts = times(children.map((c) => c.startedAt));
    // The work stopped at its last child's close (or the last start after it); while a session is in a turn on it, it
    // did not stop (#6043).
    let work: Interval | undefined;
    if (starts.length > 0) {
      const start = Math.min(...starts, now);
      const last = worked > 0 ? now : Math.max(...starts, ...times(children.map((c) => c.closedAt)));
      work = { start, end: Math.max(Math.min(last, now), start) };
    }
    const f = open && forecast && !onHold ? epic.forecast : undefined;
    const ghost = f ? { start: work?.end ?? now, end: Math.max(ms(f.mid)!, now) } : undefined;
    const from = work?.start ?? ghost?.start ?? (onHold ? now : undefined), end = ghost?.end ?? work?.end ?? (onHold ? now : undefined);
    if (from === undefined || end === undefined) return [];
    let how: TimelineEnd = { how: "open" };
    let band: Interval | undefined;
    const waiting = children.filter((c) => c.closedAt === undefined && c.waitsForPerson === true).length;
    if (onHold) {
      how = { how: "held" };
    } else if (!open) {
      how = epic.stateReason === "not_planned" || children.every((c) => c.stateReason === "not_planned") ? { how: "dropped" } : { how: "joined" };
    } else if (waiting > 0) {
      how = { how: "waits", count: waiting };
    } else if (f) {
      how = { how: "forecast" };
      const low = ms(f.low)!, high = ms(f.high)!;
      band = { start: Math.min(low, high), end: Math.max(low, high) };
    }
    return [{
      id: epic.item.node.ref, epic: epic.item.node.ref, title: epic.item.title, number: shortRef(epic.item.node.ref),
      feature: epic.item.feature, done: children.filter((c) => c.closedAt !== undefined).length, total: children.length,
      work, ghost, from, end, closedAt: ms(epic.closedAt), how, band, working: worked, priority: epic.item.priority, row: 0,
    }];
  });
}

/** Where a block's drawing ends: its end or its band's, whichever is later. */
export const reach = (l: TimelineLine) => Math.max(l.end, l.band?.end ?? l.end);

/** Only the lines that overlap the window (Alex, 2026-11-08). A line that waits for the person has
 * no end, so it is drawn while it is open. */
export function overlapping(lines: readonly TimelineLine[], window: Interval): TimelineLine[] {
  return lines.filter((l) => l.from <= window.end && (l.how.how === "waits" ? Infinity : reach(l)) >= window.start);
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

const byStart = (a: TimelineLine, b: TimelineLine) => a.from - b.from || (a.id < b.id ? -1 : a.id > b.id ? 1 : 0);

/** Each priority is a band of rows, High at the top, then Medium, none and Low (#6089, #6093). In a band,
 * each block goes in the first row that is free at its start, sorted by it, then by id (#5506),
 * so a block never moves into a higher band's row. It packs in time, not in pixels: a row is busy until the block's
 * reach and `gap` after it, so the rows do not change with the range. A block that waits for the person has no end: it
 * holds its row while it is open. A label outside a block takes no room. A band with no block takes no row. */
export function layout(lines: readonly TimelineLine[], gap = packGap): TimelineLine[] {
  const out: TimelineLine[] = [];
  let first = 0;
  for (let band = 0; band <= 3; band++) {
    const busy: number[] = [];
    for (const line of lines.filter((l) => priorityBand(l) === band).sort(byStart)) {
      let row = 0;
      while ((busy[row] ?? -Infinity) > line.from) row++;
      busy[row] = line.how.how === "waits" ? Infinity : reach(line) + gap;
      out.push({ ...line, row: first + row });
    }
    first += busy.length;
  }
  // The epics on hold (#7335): below the rest, a row each, in the order of forge/next (the read's order breaks a tie).
  const held = lines.filter((l) => priorityBand(l) === heldBand)
    .map((l, i) => ({ l, i })).sort((a, b) => nextRank({ priority: a.l.priority }) - nextRank({ priority: b.l.priority }) || a.i - b.i);
  return [...out, ...held.map(({ l }, i) => ({ ...l, row: first + i }))];
}

/** A band in use (#6089): its name, its first row and how many rows it has. */
export interface TimelineBand { band: number; words: string; row: number; rows: number }

/** The bands in use, the top first. */
export function bands(lines: readonly TimelineLine[]): TimelineBand[] {
  const by = new Map<number, number[]>();
  for (const l of lines) by.set(priorityBand(l), [...by.get(priorityBand(l)) ?? [], l.row]);
  return [...by].map(([band, rows]) => ({ band, words: bandWords(band), row: Math.min(...rows), rows: Math.max(...rows) - Math.min(...rows) + 1 }))
    .sort((a, b) => a.row - b.row);
}

/** The band names' gutter at the plot's leading edge (#6089), in pixels. */
export const bandGutter = 64;

/** Where the rule above a band's first row goes: in the gap between the rows. */
export const bandRuleY = (row: number) => rowTop(row) - rowGap / 2;

/** The rows in use. */
export const rowCount = (lines: readonly TimelineLine[]) => Math.max(0, ...lines.map((l) => l.row + 1));

/** A block's geometry, in pixels (#5506): 28 high, 8 between rows, the first row 12 from the top. */
export const blockHeight = 28;
export const rowGap = 8;
export const plotTop = 12;
/** A row's top. */
export const rowTop = (row: number) => plotTop + row * (blockHeight + rowGap);
/** Where the title goes: in the middle of the block. */
export const titleY = (row: number) => rowTop(row) + blockHeight / 2;
/** The title's inset from the block's leading edge. */
export const titleInset = 6;

/** The next block's start in the same row, after `line`'s: a label outside `line` stops before it. */
export function nextInRow(lines: readonly TimelineLine[], line: TimelineLine): number | undefined {
  const after = lines.filter((l) => l.row === line.row && l.from > line.from).map((l) => l.from);
  return after.length > 0 ? Math.min(...after) : undefined;
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
export interface BlockWords {
  inside: string | undefined; insideX: number; arrow: boolean; outside: string | undefined; outsideX: number;
  /** The priority chip (#6089): after the title in the block when both fit, else after the words outside it, where
   * there is room before the next block; undefined when there is none. */
  chip?: string | undefined; chipX?: number | undefined; chipInside?: boolean | undefined;
}

/** A chip's padding on each side of its words, and the gap before it (#6089). */
export const chipPadding = 5;
export const chipGap = 6;
/** A chip's width, in pixels. */
export const chipWidth = (words: string, measure: (s: string) => number) => measure(words) + 2 * chipPadding;

export function blockWords(line: TimelineLine, axis: Axis, measure: (s: string) => number,
                           { next, left = -Infinity, trailing = 0 }: { next?: number | undefined; left?: number; trailing?: number } = {}): BlockWords {
  const sx = axis.x(line.from), ex = Math.max(axis.x(line.end), sx) + markWidth(line);
  const fits = measure(line.title) + 2 * titleInset <= ex - sx;
  const arrow = fits && sx < left;
  const insideX = (arrow ? left : sx) + titleInset;
  const inside = fits ? cut(arrow ? `← ${line.title}` : line.title, ex - insideX - titleInset, measure) : undefined;
  const outsideX = axis.x(reach(line)) + markWidth(line) + 6 + (trailing > 0 ? trailing + 6 : 0);
  const words = [fits ? "" : line.title, line.how.how === "dropped" ? droppedWords : line.how.how === "held" ? onHoldWords : ""]
    .filter((w) => w).join("  ");
  const room = (next === undefined ? Infinity : axis.x(next) - 6) - outsideX;
  const out: BlockWords = { inside, insideX, arrow, outside: words ? cut(words, room, measure) : undefined, outsideX };
  const chip = chipWords(line);
  if (!chip) return out;
  const width = chipWidth(chip, measure);
  // In the block, after the whole title.
  if (inside && !inside.endsWith("…")) {
    const x = insideX + measure(inside) + chipGap;
    if (x + width <= ex - titleInset) return { ...out, chip, chipX: x, chipInside: true };
  }
  // After the block's words, with the words cut to leave it room.
  const wordsRoom = room - width - (words ? chipGap : 0);
  if (wordsRoom < 0) return out;
  if (!words) return { ...out, chip, chipX: outsideX };
  const shown = cut(words, wordsRoom, measure);
  return shown === undefined ? out : { ...out, outside: shown, chip, chipX: outsideX + measure(shown) + chipGap };
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

/** The block the pointer is on (with its band and pill, a few pixels around it). */
export function hit(p: { x: number; y: number }, lines: readonly TimelineLine[], axis: Axis): TimelineLine | undefined {
  return lines.find((l) => {
    const top = rowTop(l.row);
    return p.y >= top && p.y <= top + blockHeight && p.x >= axis.x(l.from) - 2 && p.x <= axis.x(reach(l)) + markWidth(l) + 2;
  });
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

/** The hover card on a line. */
export function lineWords(line: TimelineLine, backtest: WorkTimelineBacktest | undefined, now: number): string[] {
  const out = [`${line.number} ${line.title}`, `${line.done} of ${line.total} done`];
  switch (line.how.how) {
    case "joined": out.push(`Closed ${timeWords(line.closedAt ?? line.end, now)}`); break;
    case "dropped": out.push("Closed as not planned"); break;
    case "forecast": out.push(...joinWords(line, backtest, now)); break;
    case "waits": { const w = waitWords(line.how.count); out.push(w.charAt(0).toUpperCase() + w.slice(1)); break; }
    case "open": out.push("Open"); break;
    case "held": out.push(onHoldWords); break;
  }
  const stopped = stoppedAt(line, now);
  if (stopped !== undefined) out.push(`Stopped ${agoWords(stopped, now)}`);
  return out;
}

/** A row of the hover card, on three lines (Alex, 2026-11-10): the state glyph and the title; the label
 * chips, where the wire has them; then the prediction, in the window's words. No cost (Alex, 2026-11-14). */
export interface CardRow {
  state: "open" | "closed"; title: string; labels: ForgeLabel[]; line: string;
  /** The item's priority chip (#6089), before its labels. */
  priority?: ForgeItem["priority"] | undefined;
}

/** "stopped 3 h ago": an open block whose work stopped before Now (#6043). */
export const stoppedWords = (stopped: number, now: number) => `stopped ${agoWords(stopped, now)}`;

/** "2 h ago", or "just now". */
function agoWords(t: number, now: number): string {
  const age = shortAge((now - t) / 1000);
  return age === "now" ? "just now" : `${age} ago`;
}

/** On an epic's block: "#4250 · 11 of 16 · closes about 5:00 PM · 3:00 PM–9:00 PM, 8 in 10". */
export function lineLine(line: TimelineLine, now: number): string {
  const out = [figures(line)];
  const stopped = stoppedAt(line, now);
  if (stopped !== undefined) out.push(stoppedWords(stopped, now));
  switch (line.how.how) {
    case "joined": out.push(`closed ${agoWords(line.closedAt ?? line.end, now)}`); break;
    case "dropped": out.push("closed as not planned"); break;
    case "forecast":
      out.push(`closes about ${timeWords(line.end, now)}`);
      if (line.band) out.push(`${timeWords(line.band.start, now)}–${timeWords(line.band.end, now)}, 8 in 10`);
      break;
    case "waits": out.push(waitWords(line.how.count)); break;
    case "open": out.push("open"); break;
    case "held": out.push(onHoldWords); break;
  }
  return out.join(" · ");
}

/** The hover card's row, for an epic's block. The backtest is not in it: it is the title line's help. */
export function cardRow(l: TimelineLine, timeline: WorkTimeline, now: number): CardRow {
  const epic = timeline.epics.find((e) => e.item.node.ref === l.epic);
  return { state: (epic?.item.state ?? (isOpen(l) ? "open" : "closed")) === "open" ? "open" : "closed", title: l.title,
    labels: epic?.item.labels ?? [], line: lineLine(l, now), priority: epic?.item.priority };
}

/** The epics in `window`. */
export const epicCount = (lines: readonly TimelineLine[], window: Interval) => overlapping(lines, window).length;

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

/** How long a machine may take to its first answer before the machines it covers are asked too, in ms: a read of one repo is
 * about 4 s. */
export const lateAfter = 8000;

/** Which machines a read of the Timeline asks (#7029): each repo of one machine (`oneEach`), then the next with that repo when
 * the first fails, did not read it all, or is late, so a slow machine does not hold the plot (#5449). One whose scopes are not
 * known is always asked. As the window's `WorkTimeline.Readers`. */
export class Readers {
  readonly scopes: Map<string, readonly string[]>;
  private readonly asked = new Set<string>();
  private readonly heard = new Set<string>();

  constructor(private readonly machines: readonly string[], scopes: Iterable<readonly [string, readonly string[]]>) {
    this.scopes = new Map([...scopes].filter(([m, s]) => machines.includes(m) && s.length > 0));
  }

  /** The machines to ask now, each once a read. */
  next(): string[] {
    const ask = oneEach(this.machines, this.scopes).ask.filter((m) => !this.asked.has(m));
    for (const m of ask) this.asked.add(m);
    return ask;
  }

  /** A machine gave its first answer: it is not late. */
  first(machine: string) { this.heard.add(machine); }

  /** A machine gave its last answer, with what it read; undefined when it failed. */
  done(machine: string, read: readonly string[] | undefined) {
    this.heard.add(machine);
    if (read && read.length > 0) this.scopes.set(machine, read);
    else this.scopes.delete(machine);
  }

  /** `lateAfter` is over: a machine not heard from covers no other. */
  late() {
    for (const m of this.asked) if (!this.heard.has(m)) this.scopes.delete(m);
  }
}

/** A first answer has no forecast (#7027): each open epic keeps the one on screen until its own comes. */
export function keepForecasts(timeline: WorkTimeline, shown: WorkTimeline | null): WorkTimeline {
  const kept = new Map((shown?.epics ?? []).flatMap((e) => e.forecast ? [[e.item.node.ref, e.forecast] as const] : []));
  if (kept.size === 0) return timeline;
  return {
    ...timeline,
    epics: timeline.epics.map((e) => {
      const forecast = e.forecast ?? (e.closedAt === undefined ? kept.get(e.item.node.ref) : undefined);
      return forecast ? { ...e, forecast } : e;
    }),
  };
}

/**
 * The children's starts with every machine's sessions on them (#4276, `lineage/sessionsOn`), so a
 * child a sleeping machine's session started still starts then: a child starts at the earliest of
 * its own start, its close and its first session's link. As the window's
 * `WorkTimeline.started`.
 */
export function startedBySessions(timeline: WorkTimeline, sessions: readonly LineageSessionOn[]): WorkTimeline {
  const first = new Map<string, WireDate>();
  for (const s of sessions) for (const n of s.on) first.set(n.ref, Math.min(first.get(n.ref) ?? s.at, s.at) as WireDate);
  if (first.size === 0) return timeline;
  return {
    ...timeline,
    epics: timeline.epics.map((epic) => ({
      ...epic,
      children: epic.children.map((child) => {
        const at = first.get(child.item.node.ref);
        return at === undefined ? child : { ...child, startedAt: Math.min(child.startedAt ?? at, child.closedAt ?? at, at) as WireDate };
      }),
    })),
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

/** A row of the list of epics (note 36): the Timeline under 700 px, and what a screen reader reads over it. */
export interface EpicRow { line: TimelineLine; line2: string }

/** The list of epics in the window (note 36), in priority order (#6089, #6093: High first), then in the
 * order they close: the forecast first (the soonest first), then
 * those that wait for the person, then those still open, then those that joined (the latest first). Line 2: "closes
 * about 5:00 PM · 3:00 PM–9:00 PM", "3 wait for you", "joined 9:40 AM". */
export function epicRows(lines: readonly TimelineLine[], window: Interval, now: number): EpicRow[] {
  const closes = (l: TimelineLine) => l.closedAt ?? l.end;
  const rank = (l: TimelineLine) => ({ forecast: 0, waits: 1, open: 2, held: 2, joined: 3, dropped: 3 })[l.how.how];
  const line2 = (l: TimelineLine): string => {
    switch (l.how.how) {
      case "forecast": return [`closes about ${timeWords(l.end, now)}`,
        ...l.band ? [`${timeWords(l.band.start, now)}–${timeWords(l.band.end, now)}`] : []].join(" · ");
      case "waits": return waitWords(l.how.count);
      case "open": return "open";
      case "held": return onHoldWords;
      case "joined": return `joined ${timeWords(l.closedAt ?? l.end, now)}`;
      case "dropped": return "closed as not planned";
    }
  };
  return overlapping(lines, window)
    .sort((a, b) => priorityBand(a) - priorityBand(b) || rank(a) - rank(b) || (rank(a) === 3 ? closes(b) - closes(a) : closes(a) - closes(b)) || a.title.localeCompare(b.title))
    .map((line) => {
      const stopped = stoppedAt(line, now);
      return { line, line2: stopped === undefined ? line2(line) : `${line2(line)} · ${stoppedWords(stopped, now)}` };
    });
}

/** What the plot shows (#5925): `loading` (S10) while no read has drawn yet, or while any read of the window runs and no
 * epic is in it; `empty` (S11) only when every read ended and the window has none; else the epics. A device's copy with
 * no epic is not "no epic" while the machines are read. */
export function timelineShows(drawn: boolean, reading: boolean, rows: number): "loading" | "empty" | "epics" {
  if (rows > 0) return "epics";
  return !drawn || reading ? "loading" : "empty";
}

/** When the last epic before `at` closed, for the empty card (S11); undefined when the read has none. */
export function lastJoined(timeline: WorkTimeline, at: number): number | undefined {
  const joined = timeline.epics.flatMap((e) => { const c = ms(e.closedAt); return e.children.length > 0 && e.item.onHold !== true && c !== undefined && c < at ? [c] : []; });
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
