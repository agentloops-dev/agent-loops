// Work ▸ Timeline (#4206), as the window's WorkTimelinePane (#4173): time across, each epic one block in its own row
// (#7058): solid from when its first child started to when the work stopped (#6043), then for an open epic a ghost (a
// lighter tint with a dashed edge) to its predicted finish, and a thin bar to where it closes 8 times in 10 (#5506,
// Alex, 2026-11-14). Drawn in SVG; the rows
// and the marks come from model/workTimeline. The state lives for the tab, so Back from an issue's page finds the
// Timeline as it was left.
import { signal } from "@preact/signals";
import { useEffect, useLayoutEffect, useRef, useState } from "preact/hooks";
import type { ForgeItem, LineageSessionOn, WorkTimeline as Timeline } from "../protocol/generated";
import { Capability } from "../protocol/capabilities.generated";
import { toWireDate } from "../protocol/dates";
import type { Store } from "../model/store";
import { describe } from "../model/errors";
import { boardChoices, boardName, withBoard, type WorkProject } from "../model/work";
import { boards, forgeFolders, forgeHosts, loadBoards } from "../model/workLists";
import {
  Axis, bandGutter, bandRuleY, bands as bandsOf, blockHeight, blockWords, cardRow, chipWidth, chipWords, priorityBand, colourIndex, epicCount, epicRows, errorWords, forWhiteText, hexOf, hit, lastJoined,
  lastJoinedWords, keepForecasts, layout, markWidth, mergeTimelines, nextInRow, noEpicsWords, noMachine, noProject, noProjectTitle, overlapping, lateAfter, Readers,
  packSpan, rangeSpan, rangeTitle, rangeWindow, reach, rgbOf, rowCount, rowGap, rowTop, sessionNodes, showTheWeek, startedBySessions,
  timelineLines, timelineRanges, timelineShows, titleWords, titleY, workingNow, zoomed, type Interval, type TimelineBand, type TimelineLine, type TimelineRange,
} from "../model/workTimeline";
import { go } from "../route";
import { keepWork, keptWorkRead } from "../model/workCache";
import { Choice, workProjects } from "./Work";
import { choiceFrom, choiceWords, rememberEach, remembered } from "../model/workRemembered";
import { EmptyState, ErrorStrip, FilterMenu, LabelChip, PlaceholderRows, PriorityChip, RefreshButton, StateIcon, WorkFooter } from "./WorkParts";
import { pageCount, pageFooterState } from "../model/workFooter";

/** The middle of Timeline when nothing was read (#5971). */
const couldNotReadTimeline = "Could not read the timeline";
import { onHoldWords, shownLabels } from "../model/forgeNextOrder";

/** The On hold toggle's help (#7335). */
const heldToggleHelp = "Show the epics on hold too";

// The range, the project and the board are what the Timeline had when it was last left (#5451).
const kept = remembered("timeline");
const range = signal<TimelineRange>(choiceFrom(kept, "range", "twoDays", timelineRanges));
/** The project, by id; "" until one is picked, then the first one. */
const project = signal<string>(choiceFrom(kept, "project", ""));
/** The board the epics are narrowed to (#3282), by its url; "" for every board. */
const board = signal<string>(choiceFrom(kept, "board", ""));
/** The On hold toggle (#7335): the epics on hold are drawn too. Off by default; kept with the range. */
const held = signal<boolean>(choiceFrom<"" | "on">(kept, "held", "", ["", "on"]) === "on");
rememberEach("timeline", () => choiceWords({ range: range.value, project: project.value, board: board.value, held: held.value ? "on" : "" },
  { range: "twoDays", project: "", board: "", held: "" }));
/** Where the window is: Now, until the plot is scrolled past an edge. */
const anchor = signal(Date.now());
const timeline = signal<Timeline | null>(null);
const problem = signal<string | null>(null);
const asked = signal(0);
/** Now was clicked: the plot scrolls back to it. */
const nowAsked = signal(0);
/** A read of `work/timeline` runs: Refresh spins (note 30). */
const reading = signal(false);
/** How many of the machines asked have answered the read that runs: "Reading 1 of 3 machines…" in the footer (#5971). */
const answered = signal(0);
const machines = signal(0);
let read: string | undefined;
/** The plot on screen came from this device's copy (#4930), until the machines answer. */
const fromDevice = signal(false);
/** When the plot on screen was read (#5142): this device's copy's time, or the last read that worked. The footer says it
 * (#5971), also after a later read failed. */
const plotAt = signal<number | undefined>(undefined);
/**
 * Every machine's sessions on the epics and their children, from the control plane's merged copy
 * (#4276): a sleeping machine's sessions still start their children.
 */
const sessions = signal<LineageSessionOn[]>([]);
/** The machines' timelines as one, before the sessions start their children. */
let fromMachines: Timeline | undefined;

const axisHeight = 26;
const footHeight = 26;

/** The system colours for the features, as the window's palette; the last, grey, for an epic with none. */
const palette = ["#007aff", "#af52de", "#ff2d55", "#ff9500", "#34c759", "#30b0c7", "#5856d6", "#00c7be", "#a2845e", "#32ade6", "#ff3b30", "#8e8e93"];
/** Each colour darkened until white text on it has 4.5:1 (#5506). */
const fills = palette.map((c) => hexOf(forWhiteText(rgbOf(c))));

/** What the plot holds: the window, and a range's width on each side of it to scroll through. */
function content(): Interval {
  const w = rangeWindow(range.value, anchor.value);
  return { start: w.start - rangeSpan[range.value], end: w.end + rangeSpan[range.value] };
}

/** The machines that draw the Timeline (`work.timeline/1`). */
const timelineHosts = (store: Store) => forgeHosts(store).filter((h) => store.has(h.id, Capability.workTimeline1));

/** The projects the Timeline can draw: those with a folder on a machine that draws it. */
function timelineProjects(store: Store): WorkProject[] {
  const hosts = new Set<string>(timelineHosts(store).map((h) => h.id));
  return workProjects(store).filter((p) => [...p.folders].some((f) => hosts.has(f.slice(0, f.indexOf("|")))));
}

/** What each machine read (#7029), and the machines and folders it was read from then. */
interface KeptScopes { scopes: (readonly [string, readonly string[]])[]; source: string }
const scopesPage = "timeline-scopes";

async function loadTimeline(store: Store) {
  const projects = timelineProjects(store);
  const chosen = projects.find((p) => p.id === project.value) ?? projects[0];
  // The widest range's span, whatever the range: the rows are packed over it, so they stay put across ranges (#5506).
  const span = packSpan(anchor.value);
  const key = `${asked.value} ${chosen?.id} ${board.value} ${range.value} ${held.value} ${span.start} ${timelineHosts(store).map((h) => h.id).join(",")}`;
  if (read === key && timeline.value !== null) return;
  read = key;
  if (!chosen) { timeline.value = { epics: [], releases: [] }; return; }
  if (project.value !== chosen.id) project.value = chosen.id;
  const on = new Set<string>([...chosen.folders].map((f) => f.slice(0, f.indexOf("|"))));
  const hosts = timelineHosts(store).filter((h) => on.has(h.id));
  const chosenBoard = board.value;
  const withHeld = held.value;
  reading.value = true;
  answered.value = 0;
  machines.value = 0;
  // Seen before: this device's copy at once, until the machines answer (#4930). Kept by project, board and range.
  const kept = [chosen.id, chosenBoard, range.value, ...withHeld ? ["held"] : []].join("|");
  if (timeline.value === null) {
    const copy = await keptWorkRead<Timeline>("timeline", kept);
    if (copy && read === key && timeline.value === null) { timeline.value = copy.value; plotAt.value = copy.at; fromDevice.value = true; }
  }
  // Each repo is asked of one machine (#7029). What each read last is kept for the next visit.
  const source = `${hosts.map((h) => h.id).join(",")}|${forgeFolders(store, hosts.map((h) => h.id))}`;
  const scopesKept = (await keptWorkRead<KeptScopes>(scopesPage, `timeline|${chosen.id}`))?.value;
  if (read !== key) return;
  const readers = new Readers(hosts.map((h) => h.id), scopesKept?.source === source ? scopesKept.scopes : []);
  // The first machine to answer draws the plot (#5449), and each one after adds its epics: a slow or offline machine does
  // not hold it. A machine that failed says so only when no machine answered.
  // A machine answers with its epics first, and again with the forecast when its history is read (#7027): its first
  // answer is in firsts until then, and stays there when the second fails.
  const got: { t?: Timeline; refs?: string[] | undefined; problem?: string }[] = [];
  const firsts = new Map<string, { t: Timeline; refs: string[] | undefined }>();
  const draw = async (all: boolean) => {
    if (all) reading.value = false;
    const failed = got.flatMap((g) => g.problem ? [g.problem] : []);
    const none = got.length > 0 && failed.length === got.length;
    problem.value = all && none ? failed.join("\n") : null;
    // With every machine failing, the last plot stays at full opacity, and the footer says what failed (#5971); with
    // none, the middle says it.
    if (all && none) return;
    const answers = [...got, ...firsts.values()];
    let merged = mergeTimelines(answers.flatMap((g) => g.t ? [g.t] : []));
    if (firsts.size > 0) merged = keepForecasts(merged, timeline.value);
    const epics = chosenBoard ? new Set(answers.flatMap((g) => g.refs ?? [])) : undefined;
    if (epics) merged.epics = merged.epics.filter((e) => epics.has(e.item.node.ref));
    fromMachines = merged;
    // The plot draws now, with the sessions it had (#5450); the new ones start their children when they come.
    timeline.value = startedBySessions(merged, sessions.value);
    plotAt.value = Date.now();
    fromDevice.value = false;
    const on = await readSessions(store, merged);
    if (read !== key || fromMachines !== merged) return;
    sessions.value = on;
    timeline.value = startedBySessions(merged, on);
    plotAt.value = Date.now();
    fromDevice.value = false;
    // Kept for the window around now, read by every machine: a plot scrolled away is not the one a person opens on.
    if (all && failed.length === 0 && Math.abs(anchor.value - Date.now()) < 3_600_000) keepWork("timeline", kept, timeline.value);
  };
  const askOne = async (h: (typeof hosts)[number]): Promise<void> => {
    let one: (typeof got)[number];
    try {
      const ask = { project: chosen.id, from: toWireDate(new Date(span.start)), to: toWireDate(new Date(span.end)), ...withHeld ? { onHold: true } : {} };
      let t = await store.link.call("work/timeline", { ...ask, first: true }, h.id);
      // A board narrows to its epics: the forge says which they are.
      // With its board not read, it covers no other machine (the next one's board can be).
      let refs: string[] | undefined, boardFailed = false;
      if (chosenBoard) {
        try {
          const list = await store.link.call("forge/issues", withBoard({ state: "all", label: "epic", limit: 100 }, chosenBoard), h.id);
          refs = list.items.map((i: ForgeItem) => i.node.ref);
          boardFailed = list.items.length === 0 && (list.errors?.length ?? 0) > 0;
        } catch { refs = []; boardFailed = true; }
      }
      if (t.partial) {
        readers.first(h.id);
        if (read !== key) return;
        if (t.epics.length > 0) {
          firsts.set(h.id, { t, refs });
          void draw(false);
        }
        t = await store.link.call("work/timeline", ask, h.id);
      }
      // A machine whose forge failed answers with no epic and only the forge's error: a failed read, not an empty plot
      // with "Last update just now" (#5971, as the Mac's #5969).
      one = t.epics.length === 0 && (t.errors?.length ?? 0) > 0
        ? { problem: (t.errors ?? []).map((e) => hosts.length > 1 ? `${h.name}: ${e}` : e).join("\n") }
        : { t, refs };
      if (one.t) firsts.delete(h.id);
      readers.done(h.id, boardFailed ? undefined : one.t?.scopes);
    } catch (e) {
      one = { problem: hosts.length > 1 ? `${h.name}: ${describe(e)}` : describe(e) };
      readers.done(h.id, undefined);
    }
    got.push(one);
    if (read !== key) return;
    answered.value = got.length;
    const more = askNext();
    if (got.length < machines.value && one.t) void draw(false);
    await more;
  };
  const askNext = async () => {
    const ids = new Set(readers.next());
    machines.value += ids.size;
    await Promise.all(hosts.filter((h) => ids.has(h.id)).map(askOne));
  };
  let late = Promise.resolve();
  const timer = setTimeout(() => {
    if (read !== key) return;
    readers.late();
    late = askNext();
  }, lateAfter);
  await askNext();
  clearTimeout(timer);
  await late;
  if (read !== key) return;
  keepWork(scopesPage, `timeline|${chosen.id}`, { scopes: [...readers.scopes], source } satisfies KeptScopes);
  await draw(true);
}

/** Every machine's sessions on the timeline's epics and children, from the control plane's merged copy (#4276); none from one before it. */
async function readSessions(store: Store, t: Timeline): Promise<LineageSessionOn[]> {
  const nodes = sessionNodes(t);
  if (nodes.length === 0) return [];
  try {
    return (await store.link.call("lineage/sessionsOn", { nodes })).sessions;
  } catch {
    return [];
  }
}

/** The sessions again, on control/lineageChanged (#4276): a session that starts on a child on any machine starts it, with no read of the forges. */
async function readSessionsAgain(store: Store) {
  const machines = fromMachines;
  if (!machines) return;
  const got = await readSessions(store, machines);
  if (fromMachines !== machines) return;
  sessions.value = got;
  timeline.value = startedBySessions(machines, got);
}

function goToNow() {
  anchor.value = Date.now();
  nowAsked.value++;
}

/** A label's width, as the plot draws it. */
let measurer: CanvasRenderingContext2D | null | undefined;
function measure(text: string): number {
  if (measurer === undefined) measurer = typeof document === "undefined" ? null : document.createElement("canvas").getContext("2d");
  if (!measurer) return text.length * 6.5;
  measurer.font = "11px -apple-system, BlinkMacSystemFont, system-ui, sans-serif";
  return measurer.measureText(text).width;
}

export function WorkTimeline({ store }: { store: Store }) {
  const hosts = timelineHosts(store);
  const projects = timelineProjects(store);
  const hostKey = hosts.map((h) => h.id).join(",");
  const projectKey = projects.map((p) => p.id).join(",");
  useEffect(() => { void loadTimeline(store); }, [asked.value, project.value, board.value, range.value, held.value, anchor.value, hostKey, projectKey]);
  useEffect(() => { void loadBoards(store); }, [hostKey]);
  // A machine sent new lineage to the control plane (#4276): the sessions again, no polling.
  const lineageChanges = store.lineageChanges.value;
  useEffect(() => {
    if (lineageChanges === 0) return;
    const t = setTimeout(() => { void readSessionsAgain(store); }, 500);
    return () => clearTimeout(t);
  }, [lineageChanges]);
  // Read again every five minutes, so closes land while the page is open.
  useEffect(() => {
    const t = setInterval(() => { asked.value++; }, 300_000);
    return () => clearInterval(t);
  }, [project.value]);
  const t = timeline.value;
  const problems = [...problem.value ? problem.value.split("\n") : [], ...t?.errors ?? []];
  const shownCount = t && projects.length > 0 ? epicCount(timelineLines(t, Date.now(), true, new Set(), held.value), rangeWindow(range.value, anchor.value)) : undefined;
  // The read's state, the count and Last update are in the footer, as on the lists (#5971, the Mac's #5969); no strip.
  const footer = hosts.length === 0 || projects.length === 0 ? { kind: "none" as const }
    : pageFooterState(t !== null, reading.value, fromDevice.value, problems, answered.value, machines.value);
  const refresh = () => { asked.value++; void loadBoards(store, true); };
  return (
    <div class="work-list timeline-page work-footed">
      {/* The filter bar of parts.md section 4 (note 31 and 32): Project and Board at the leading edge, the title line, then
          the range and Now at the trailing edge, where Sort is on a list, and Refresh (note 30). */}
      <div class="work-filters timeline-bar" role="group" aria-label="Timeline">
        <div class="work-menus">
          {projects.length > 1 && (
            <FilterMenu label="Project" value={project.value} choices={projects.map((p) => ({ value: p.id, words: p.name }))}
              onPick={(id) => { project.value = id; }} />
          )}
          <Choice label="Board" any="All boards" value={board.value} options={boardChoices(boards.value, board.value)}
            words={(url) => boardName(boards.value, url)} onChange={(url) => { board.value = url; }} />
          {/* The forecast is always drawn (Alex, 2026-11-10): how past forecasts did is the title line's help. */}
          {t && projects.length > 0 && (
            <span class="quiet timeline-title" title={t.backtest ? `The forecast after now. ${errorWords(t.backtest)}.` : "The forecast after now"}>
              {titleWords(projects.length > 1 ? "" : projects[0]!.name,
                epicCount(timelineLines(t, Date.now(), true, new Set(), held.value), rangeWindow(range.value, anchor.value)), range.value)}
            </span>
          )}
        </div>
        <span class="timeline-trailing">
          {/* The epics on hold too (#7335), dimmed, below the rest. */}
          <button type="button" class="timeline-now-button timeline-held-toggle" aria-pressed={held.value} title={heldToggleHelp}
            onClick={() => { held.value = !held.value; }}>{onHoldWords}</button>
          <div class="diff-choice" role="group" aria-label="Range">
            {timelineRanges.map((r) => (
              <button key={r} type="button" aria-pressed={range.value === r} onClick={() => { range.value = r; goToNow(); }}>{rangeTitle[r]}</button>
            ))}
          </div>
          <button type="button" class="timeline-now-button" onClick={goToNow} title="Back to now">Now</button>
        </span>
        <RefreshButton busy={reading.value} onClick={refresh} />
      </div>
      {hosts.length === 0 ? <EmptyState kind="timeline" title={noMachine} />
        : projects.length === 0 ? <EmptyState kind="timeline" title={noProjectTitle} line={noProject} />
        : (
          <>
            {t === null && problems.length > 0 && !reading.value
              ? <ErrorStrip lines={problems} retry={refresh} middle title={couldNotReadTimeline} />
              : <Plot timeline={t ?? { epics: [], releases: [] }} drawn={t !== null} reading={reading.value}
                  working={t ? workingNow(t, sessions.value, Object.values(store.agents.value).flat()) : new Set()} />}
          </>
        )}
      <WorkFooter state={footer} retry={refresh} count={shownCount === undefined ? undefined : pageCount(shownCount, "epic")}
        at={t !== null ? plotAt.value : undefined} />
    </div>
  );
}

/** The minute, so Now moves while the page is open. */
function useMinute(): number {
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 60_000);
    return () => clearInterval(t);
  }, []);
  return now;
}

interface Hover { line: TimelineLine; x: number; y: number }

/** The plot; under 700 px the list of epics in its place (note 36). `drawn`: a read or a device's copy drew it;
 * `reading`: a read of the window runs. Until one draws, or while one runs and no epic is in the window, the axis and
 * Now with grey blocks that shimmer in rows (S10, #5925), never S11. A later read that failed leaves it at full opacity, and the
 * footer says what failed (#5971). `working`: the epics and
 * children a session is in a turn on now (#4265); their blocks glow. */
function Plot({ timeline: t, drawn, reading, working }: {
  timeline: Timeline; drawn: boolean; reading: boolean; working: ReadonlySet<string>;
}) {
  const now = useMinute();
  const scroller = useRef<HTMLDivElement>(null);
  const [size, setSize] = useState({ width: 0, height: 0 });
  const [offset, setOffset] = useState(0);
  const [hover, setHover] = useState<Hover | null>(null);
  const settle = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const pinch = useRef(0);

  useLayoutEffect(() => {
    const el = scroller.current;
    if (!el) return;
    const ro = new ResizeObserver(() => setSize({ width: el.clientWidth, height: el.clientHeight }));
    ro.observe(el);
    setSize({ width: el.clientWidth, height: el.clientHeight });
    return () => ro.disconnect();
  }, []);

  // A card is for what was under the pointer: a new range or window draws other things.
  useEffect(() => setHover(null), [range.value, anchor.value, nowAsked.value]);

  // The band names' gutter at the leading edge takes its width from the plot (#6089, #6093).
  const visible = Math.max(size.width - bandGutter, 1);
  const span = content();
  const axis = new Axis(span, visible * (span.end - span.start) / rangeSpan[range.value]);
  // Packed over the widest range's span, so a block keeps its row when the range changes or the plot scrolls (#5506).
  const lines = layout(overlapping(timelineLines(t, now, true, working, held.value), packSpan(anchor.value)));
  const height = Math.max(rowTop(Math.max(rowCount(lines), 3)) + rowGap, size.height - axisHeight - footHeight);

  // The window starts a screen in: a range's width is before it.
  const scrollTo = (x: number) => {
    const el = scroller.current;
    if (!el) return;
    el.scrollLeft = x;
    setOffset(x);
  };
  useLayoutEffect(() => {
    const el = scroller.current;
    if (!el || size.width === 0) return;
    scrollTo(visible);
    el.scrollTop = 0;
  }, [nowAsked.value, size.width === 0]);

  // Past an edge of what the plot holds, the window moves by a range, and the plot with it.
  const onScroll = () => {
    const el = scroller.current;
    if (!el) return;
    setOffset(el.scrollLeft);
    clearTimeout(settle.current);
    settle.current = setTimeout(() => {
      const x = el.scrollLeft;
      if (x < visible * 0.05) {
        anchor.value -= rangeSpan[range.value];
        scrollTo(x + visible);
      } else if (x > axis.width - visible * 1.05 && axis.width > visible * 2) {
        anchor.value += rangeSpan[range.value];
        scrollTo(x - visible);
      }
    }, 150);
  };

  // A pinch (ctrl and the wheel, as browsers send it) steps the range: apart is closer in, together further out.
  const onWheel = (e: WheelEvent) => {
    if (!e.ctrlKey) return;
    e.preventDefault();
    pinch.current += e.deltaY;
    if (Math.abs(pinch.current) > 40) {
      range.value = zoomed(range.value, pinch.current < 0);
      pinch.current = 0;
      goToNow();
    }
  };
  useEffect(() => {
    const el = scroller.current;
    el?.addEventListener("wheel", onWheel, { passive: false });
    return () => el?.removeEventListener("wheel", onWheel);
  });

  const point = (e: MouseEvent) => {
    const r = (e.currentTarget as SVGElement).getBoundingClientRect();
    return { x: e.clientX - r.left, y: e.clientY - r.top };
  };
  const hitAt = (p: { x: number; y: number }) => hit(p, lines, axis);
  const open = (l: TimelineLine) => go({ nav: "work", section: "issue", item: l.epic });

  const win = rangeWindow(range.value, anchor.value);
  const rows = epicRows(timelineLines(t, now, true, new Set(), held.value), win, now);
  const shows = timelineShows(drawn, reading, rows.length);
  const loading = shows === "loading";
  const d = new Drawing(lines, axis, now, height, offset, visible, loading ? [] : bandsOf(lines));
  const last = shows === "empty" ? lastJoined(t, win.start) : undefined;
  return (
    <div class="timeline">
      <div class="timeline-scroll" ref={scroller} onScroll={onScroll}>
        <div class="timeline-content" style={{ width: `${axis.width + bandGutter}px` }}>
          {/* Each band's name on its first row (#6089, #6093): it stays at the leading edge while the plot scrolls in
              time, and moves with the rows. */}
          <div class="timeline-gutter" style={{ width: `${bandGutter}px` }} aria-hidden="true">
            <div class="timeline-gutter-corner" style={{ height: `${axisHeight}px` }} />
            <svg width={bandGutter} height={height}>{d.gutter()}</svg>
          </div>
          <div class="timeline-plot-column">
          <svg class="timeline-axis" width={axis.width} height={axisHeight} aria-hidden="true">{d.axisMarks()}</svg>
          <svg class={`timeline-plot${hover ? " pointing" : ""}`} width={axis.width} height={height} aria-hidden="true"
            onMouseMove={(e) => { const p = point(e); const h = hitAt(p); setHover(h ? { line: h, ...p } : null); }}
            onMouseLeave={() => setHover(null)}
            onClick={(e) => { const h = hitAt(point(e)); if (h) open(h); }}>
            {d.plot()}
            {loading && d.placeholders()}
          </svg>
          {hover && <Card hover={hover} timeline={t} now={now} right={offset + visible} />}
          <svg class="timeline-foot" width={axis.width} height={footHeight} aria-hidden="true">{d.nowPill()}</svg>
          </div>
        </div>
      </div>
      {/* S11: no epic in the window, and the way to a wider one. */}
      {shows === "empty" && (
        <div class="timeline-empty" role="status">
          <span class="strong">{noEpicsWords(range.value)}</span>
          {last !== undefined && <span class="quiet">{lastJoinedWords(last, now)}</span>}
          {range.value !== "week" && <button type="button" onClick={() => { range.value = "week"; goToNow(); }}>{showTheWeek}</button>}
        </div>
      )}
      {/* The list of epics (note 36): what a screen reader reads over the plot, and the Timeline itself under 700 px, where
          a plot of 24 h is not readable. In the order they close; a tap opens the epic's page. */}
      <div class="timeline-list">
        <p class="timeline-list-now small"><span class="strong">Now</span> · {new Date(now).toLocaleTimeString(undefined, { hour: "numeric", minute: "2-digit" })}</p>
        {loading ? <PlaceholderRows /> : rows.length > 0 && (
          <ul class="work-rows" aria-label="Epics">
            {rows.map(({ line, line2 }) => (
              <li key={line.id}>
                <button type="button" class="timeline-list-row" style={{ "--c": d.colour(line) }}
                  aria-label={`${line.title}${spokenChip(line)}, ${line.done} of ${line.total} done, ${line2}`}
                  onClick={() => go({ nav: "work", section: "issue", item: line.epic })}>
                  <span class="edge" aria-hidden="true" />
                  <span class="work-row-main">
                    <span class="timeline-list-head">
                      <span class="title strong">{line.title}</span>
                      <PriorityChip priority={line.priority} />
                    </span>
                    <span class={`quiet small${line.how.how === "waits" ? " waits" : ""}`}>{line2}</span>
                  </span>
                  <span class="timeline-list-progress small quiet" aria-hidden="true">
                    <span class="track"><span class="fill" style={{ width: `${line.total ? Math.round(100 * line.done / line.total) : 0}%` }} /></span>
                    {line.done} of {line.total}
                  </span>
                </button>
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}

/** The card beside the pointer, on the left of it near the plot's right edge, so it stays in sight. A row on three
 * lines (Alex, 2026-11-10): the title; the label chips, where the wire has them; the prediction. */
function Card({ hover, timeline: t, now, right }: { hover: Hover; timeline: Timeline; now: number; right: number }) {
  const row = cardRow(hover.line, t, now);
  const labels = shownLabels(row.labels, false);
  return (
    <div class="timeline-card" style={hover.x + 14 + 370 > right
      ? { left: `${hover.x - 14}px`, transform: "translateX(-100%)", top: `${hover.y + axisHeight + 14}px` }
      : { left: `${hover.x + 14}px`, top: `${hover.y + axisHeight + 14}px` }}>
      <div class="timeline-card-row">
        <div class="timeline-card-title">
          <StateIcon kind="issue" state={row.state} size={14} /><span class="strong">{row.title}</span>
        </div>
        {(labels.length > 0 || row.priority) && (
          <div class="work-chips">
            {/* The priority chip first (#6089). */}
            <PriorityChip priority={row.priority} />
            {labels.slice(0, 3).map((l) => <LabelChip key={l.name} label={l} />)}
            {labels.length > 3 && <span class="work-chips-more quiet small">+{labels.length - 3}</span>}
          </div>
        )}
        <div class="quiet small timeline-card-line">{row.line}</div>
      </div>
    </div>
  );
}

/** A band's class for its tint (#6089): High the failure's red, Medium the warning's orange, the rest plain. */
const bandClass = (band: number) => ["high", "medium", "none", "low"][band] ?? "none";

/** What a screen reader says after an epic's title (#6089): ", priority high" or nothing. */
function spokenChip(l: TimelineLine): string {
  const chip = chipWords(l);
  return chip ? `, priority ${chip.toLowerCase()}` : "";
}

/** What one frame of the plot draws: the window, and a screen on each side of it. */
class Drawing {
  readonly nowX: number;
  readonly shown: TimelineLine[];

  constructor(readonly lines: TimelineLine[], readonly axis: Axis, readonly now: number, readonly height: number,
              readonly offset: number, readonly visible: number, readonly bands: readonly TimelineBand[] = []) {
    this.nowX = axis.x(now);
    this.shown = lines.filter((l) => axis.x(l.from) <= this.seenTo && axis.x(reach(l)) >= this.seenFrom);
  }

  get seenFrom() { return this.offset - this.visible; }
  get seenTo() { return this.offset + 2 * this.visible; }
  seen(x: number) { return x >= this.seenFrom && x <= this.seenTo; }

  /** A block's fill: its feature's colour, darkened until white has 4.5:1; grey when closed as not planned. */
  colour(l: TimelineLine) { return fills[l.how.how === "dropped" ? fills.length - 1 : colourIndex(l.feature, fills.length)]!; }

  /** The epic or a child of it is worked on now, and the block is open at Now. */
  isWorking(l: TimelineLine) { return l.working > 0 && l.from <= this.now && l.end >= this.now; }

  /** The wait pill's width: 👤 and the number, no words (Alex, 2026-11-14). */
  pillWidth(count: number) { return measure(`👤 ${count}`) + 12; }

  plot() {
    const { axis, nowX, height, shown } = this;
    const left = this.offset, right = this.offset + this.visible;
    const out = [
      <rect key="plot" class="timeline-ground" x={0} y={0} width={axis.width} height={height} />,
      // A rule above each priority band but the first (#6089, #6093).
      ...this.bands.filter((b) => b.row > 0).map((b) => (
        <line key={`band ${b.band}`} class="timeline-band-rule" x1={0} y1={bandRuleY(b.row)} x2={axis.width} y2={bandRuleY(b.row)} />
      )),
    ];
    shown.forEach((l, i) => {
      const c = this.colour(l), top = rowTop(l.row), mid = top + blockHeight / 2;
      // An epic can be months old: no further than just past the plot's edges.
      const sx = Math.max(axis.x(l.from), -20), ex = Math.max(Math.min(axis.x(l.end), axis.width + 20), sx + Math.max(2, markWidth(l))), w = ex - sx;
      // Solid from started to stopped (#6043), then an open epic's dashed ghost to its predicted finish (#7058). An epic on
      // hold with no work yet is all mark (#7335).
      const wx = axis.x(l.work?.start ?? l.from), wex = markWidth(l) > 0 ? ex : Math.max(axis.x(l.work?.end ?? l.from), wx);
      const onSolid = (x: number) => wex > wx && x >= wx && x <= wex + 0.5;
      const gx = axis.x(l.ghost?.start ?? l.end);
      const solid = `timeline-solid-${i}`, idle = `timeline-idle-${i}`, ahead = `timeline-ahead-${i}`;
      const block = (cls: string, clip?: string) => (
        <rect class={cls} x={sx} y={top} width={w} height={blockHeight} clip-path={clip ? `url(#${clip})` : undefined} />
      );
      const pill = l.how.how === "waits" ? this.pillWidth(l.how.count) : 0;
      const words = blockWords(l, axis, measure, { next: nextInRow(this.lines, l), left: left + 2, trailing: pill });
      out.push(
        <g key={`block ${l.id}`} class={`timeline-block${this.isWorking(l) ? " working" : ""}${l.how.how === "dropped" ? " dropped" : ""}${l.how.how === "held" ? " held" : ""}`}
          style={{ "--c": c }}>
          {/* An epic a session is in a turn on now (#4265): a soft glow that breathes; with Reduce Motion, still. */}
          <defs>
            <clipPath id={solid}><rect x={wx} y={-100} width={wex - wx} height={height + 200} /></clipPath>
            <clipPath id={ahead}><rect x={gx} y={-100} width={Math.max(ex - gx + 2, 0)} height={height + 200} /></clipPath>
            <clipPath id={idle}>
              <rect x={-100} y={-100} width={Math.max(wx + 100, 0)} height={height + 200} />
              <rect x={wex} y={-100} width={Math.max(axis.width - wex + 100, 0)} height={height + 200} />
            </clipPath>
          </defs>
          {this.isWorking(l) && <rect class="breathing" x={sx} y={top} width={w} height={blockHeight} />}
          {block("past", solid)}
          {l.ghost && <>{block("ghost", ahead)}{block("ahead", ahead)}</>}
          {/* 8 times in 10: a thin bar after the forecast. */}
          {l.band && axis.x(l.band.end) > ex && (
            <rect class="band" x={ex} y={mid - 2} width={axis.x(l.band.end) - ex} height={4} rx={2} />
          )}
          {words.inside && (
            <>
              <text class="title past" clip-path={`url(#${solid})`} x={words.insideX} y={titleY(l.row)} dy="0.35em">{words.inside}</text>
              <text class="title later" clip-path={`url(#${idle})`} x={words.insideX} y={titleY(l.row)} dy="0.35em">{words.inside}</text>
            </>
          )}
          {words.outside && <text class="outside" x={words.outsideX} y={mid} dy="0.35em">{words.outside}</text>}
          {words.chip && words.chipX !== undefined && this.chip(words.chip, l, words.chipX, words.chipInside ? titleY(l.row) : mid)}
          {/* Where the forecast stops for the person: 👤 and the number. */}
          {l.how.how === "waits" && this.pill(`👤 ${l.how.count}`, axis.x(reach(l)) + 6 + pill / 2, mid, "timeline-pill waits")}
          {this.isWorking(l) && (
            // ◐: a ring at Now, its left half filled; the half turns about the ring's centre.
            <g class="timeline-working">
              <circle cx={nowX} cy={mid} r={5} />
              <path style={{ transformOrigin: `${nowX}px ${mid}px` }} d={`M${nowX},${mid - 5} A5,5 0 0 0 ${nowX},${mid + 5} Z`} />
            </g>
          )}
          {/* → for a block that goes on past the right edge. */}
          {axis.x(reach(l)) + markWidth(l) > right && (
            <text class={`timeline-more${onSolid(right - 10) ? "" : " later"}`} x={right - 10} y={mid} dy="0.35em" text-anchor="middle">→</text>
          )}
        </g>,
      );
    });
    // Now: a grey line.
    out.push(<line key="now" class="timeline-now" x1={nowX} y1={0} x2={nowX} y2={height} />);
    return out;
  }

  /** Each band's name, on its first row, in the gutter, with a rule above every band but the first (#6089, #6093). */
  gutter() {
    return this.bands.map((b) => (
      <g key={b.band} class={`timeline-band-name is-${bandClass(b.band)}`}>
        {b.row > 0 && <line x1={0} y1={bandRuleY(b.row)} x2={bandGutter} y2={bandRuleY(b.row)} />}
        <text x={12} y={rowTop(b.row) + blockHeight / 2} dy="0.35em">{b.words}</text>
      </g>
    ));
  }

  /** The priority chip after a block's title (#6089, #6093): the priority chip's look, on the plot's own ground so it
   * reads on any block. */
  chip(words: string, l: TimelineLine, x: number, y: number) {
    const w = chipWidth(words, measure);
    return (
      <g class={`timeline-chip is-${bandClass(priorityBand(l))}`}>
        <rect x={x} y={y - 8} width={w} height={16} rx={8} />
        <text x={x + w / 2} y={y} dy="0.35em" text-anchor="middle">{words}</text>
      </g>
    );
  }

  /** S10: grey blocks that shimmer, in rows, on the window. */
  placeholders() {
    const x = this.offset + this.visible * 0.04;
    return (
      <g key="placeholders" class="timeline-placeholder">
        {[[0, 0, 0.4], [1, 0.12, 0.45], [2, 0.03, 0.2], [3, 0.2, 0.36]].map(([row, from, w]) => (
          <rect key={row} x={x + this.visible * from!} y={rowTop(row!)} width={this.visible * w!} height={blockHeight} />
        ))}
      </g>
    );
  }

  pill(words: string, x: number, y: number, cls: string) {
    const w = measure(words) + 12;
    return (
      <g class={cls}>
        <rect x={x - w / 2} y={y - 9} width={w} height={18} rx={9} />
        <text x={x} y={y} dy="0.35em" text-anchor="middle">{words}</text>
      </g>
    );
  }

  /** The hour axis at the top (#5506). */
  axisMarks() {
    const { axis } = this;
    const out = [<rect key="ground" class="timeline-ground" x={0} y={0} width={axis.width} height={axisHeight} />];
    const seenSpan = this.visible * (axis.interval.end - axis.interval.start) / Math.max(axis.width, 1);
    for (const tick of axis.ticks(seenSpan)) {
      const x = axis.x(tick);
      if (!this.seen(x)) continue;
      const midnight = new Date(tick).getHours() === 0;
      // "Sun 8", as the window's ticks say it.
      const words = midnight ? `${new Date(tick).toLocaleDateString(undefined, { weekday: "short" })} ${new Date(tick).getDate()}`
        : new Date(tick).toLocaleTimeString(undefined, { hour: "numeric" });
      out.push(
        <g key={`tick ${tick}`} class={`timeline-tick${midnight ? " midnight" : ""}`}>
          <line x1={x} y1={axisHeight - (midnight ? 7 : 4)} x2={x} y2={axisHeight} />
          <text x={x} y={11} dy="0.35em" text-anchor="middle">{words}</text>
        </g>,
      );
    }
    return out;
  }

  /** Now's time in a grey pill, under the plot where the Now line ends. */
  nowPill() {
    return [
      <rect key="ground" class="timeline-ground" x={0} y={0} width={this.axis.width} height={footHeight} />,
      <g key="now">{this.pill(new Date(this.now).toLocaleTimeString(undefined, { hour: "numeric", minute: "2-digit" }), this.nowX, 12, "timeline-pill now")}</g>,
    ];
  }
}
