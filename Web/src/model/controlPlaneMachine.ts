// The control plane as a machine in Machines (#6592, the web port of #6591, epic #6593), for an admin: its row is its
// machine record (#6609), roles ["control"], or both roles on a machine that also runs agents. Its level and figures
// come from control/capacity (#6590), from the copy that names that machine record. The same words as AgentsKitCore's
// ControlPlaneMachine on the Mac window. The state is the page's: read when the page connects, as Machines opens, and
// every 15 s while a control plane's page is open; control/capacityChanged moves a level between reads.
import { signal } from "@preact/signals";
import type { CapacityLevel, ControlCapacityCopy, ControlMachine } from "../protocol/generated";
import { fromWireDate } from "../protocol/dates";
import { levelWords } from "./capacity";
import { failedWords } from "./console";
import { CallFailed } from "../wire/link";
import { bytesWords, controlRole, runsControl } from "./controlMove";

/** What the row and the page call a machine that only runs the control plane. */
export const title = "Control plane";

/** How often the control plane looks at itself, and so how often its page reads again. */
export const lookSeconds = 15;

export const noReading = "No capacity reading yet";
export const noReadingDetail = "The first reading will appear after the control plane checks itself.";
/** The line when no reading will come by waiting (#7364): the read failed, or the answers stay empty. */
export const noReadingNow = "No capacity reading";
export const readFailedDetail = "The capacity read failed. There is no reading to show.";
export const emptyDetail = "The control plane gives no capacity reading. It may need an update.";
export const foot = `Reading every ${lookSeconds} seconds. The control plane keeps serving requests at every level.`;
export const refreshFailed = "Capacity could not refresh. Showing the last reading.";
/** The docs site (#1327), as the sidebar's Help and machineProviders.ts: a link opened in a new tab, never fetched. */
const helpSite = "https://docs.agentloops.dev/";
/** What the strip offers: the guide that says how to give the control plane more room (#6589). */
export const guide = `${helpSite}how-to/run-the-control-plane-in-the-cloud/#metrics-and-logs`;

export const stripWords = {
  show: "Show Control Plane",
  showHelp: "Its level and figures, on its page in Machines",
  what: "What to Do",
  whatHelp: "Run the control plane in the cloud: a larger machine, more memory, a faster disk or a store sweep",
};

/** The machine runs the control plane and no agents: nothing dials in with it, so it has no sessions, folders, start or stop. */
export function controlOnly(row: ControlMachine | undefined): boolean {
  const roles = row?.roles;
  return !!roles && roles.includes("control") && !roles.includes("agents");
}

/** The control plane's machines first, then the others in the order they came. */
export function first<T extends ControlMachine>(rows: readonly T[]): T[] {
  return [...rows.filter(controlRole), ...rows.filter((r) => !controlRole(r))];
}

/** What tells one control-only machine from the others (#7362): null when it is the only one; else its name when no
 * other control-only machine has the same name, else its id. */
export function label(row: ControlMachine | undefined, rows: readonly ControlMachine[]): string | null {
  if (!row || !controlOnly(row)) return null;
  const others = rows.filter((r) => controlOnly(r) && r.id !== row.id);
  if (others.length === 0) return null;
  const name = row.name.trim();
  if (name && name !== title && !others.some((r) => r.name.trim() === name)) return name;
  return row.id;
}

/** The row's and the page's title: "Control plane", or "Control plane · <label>" with several (#7362). */
export function titleOf(row: ControlMachine | undefined, rows: readonly ControlMachine[]): string {
  const l = label(row, rows);
  return l === null ? title : `${title} · ${l}`;
}

/** "control.example.dev · Linux · Admin view": where the control plane is, and what it runs on. With several
 * control-only machines the page's address is no one machine's, so it is left out, and so is a name the title says. */
export function metaLine(row: ControlMachine | undefined, rows: readonly ControlMachine[], address: string | null): string {
  const parts: string[] = [];
  const l = label(row, rows);
  if (l === null && address) parts.push(address);
  if (row) {
    if (row.name && row.name !== title && row.name !== l) parts.push(row.name);
    if (row.platform) parts.push(row.platform);
  }
  parts.push("Admin view");
  return parts.join(" · ");
}

const at = (look: ControlCapacityCopy) => fromWireDate(look.at).getTime();

/** The latest look of the copy that runs on this machine. A copy from before #6591 names no machine: when it is the only
 * copy and this is the only control machine, it is this one's. */
export function lookFor(machine: string, copies: readonly ControlCapacityCopy[], controlMachines: number): ControlCapacityCopy | null {
  const named = copies.filter((c) => c.machine === machine);
  if (named.length) return named.reduce((a, b) => (at(b) > at(a) ? b : a));
  return controlMachines === 1 && copies.length === 1 && copies[0]!.machine === undefined ? copies[0]! : null;
}

const rank = (level: CapacityLevel) => (level === "full" ? 2 : level === "near" ? 1 : 0);

/** The highest level of the copies, the newest of those. */
export function worst(copies: readonly ControlCapacityCopy[]): ControlCapacityCopy | null {
  return copies.reduce<ControlCapacityCopy | null>((best, c) =>
    best === null || rank(c.level) > rank(best.level) || (rank(c.level) === rank(best.level) && at(c) > at(best)) ? c : best, null);
}

/** The copy the strip names: the highest level, near or full only. None while the page cannot reach the control plane
 * (#7363): the level is then the last one known, not now. */
export function warning(copies: readonly ControlCapacityCopy[], away: boolean): ControlCapacityCopy | null {
  if (away) return null;
  const w = worst(copies);
  return w && w.level !== "ok" ? w : null;
}

// Words

/** "16.7%": a kernel pressure figure, the share of the last 60 s that work waited. */
export const percent = (value: number) => `${value.toFixed(1)}%`;
/** "1.42": the load per core over a minute. */
export const load = (value: number) => value.toFixed(2);
/** "1,284". */
export const count = (value: number) => value.toLocaleString("en-US");
/** "184 MB"; "0 bytes". */
export const bytes = (value: number) => bytesWords(Math.max(0, value));

/** The level in a row or a summary: "Near capacity", "At capacity", "OK", or "No reading" before the first look. */
export function level(look: ControlCapacityCopy | null): string {
  if (!look) return "No reading";
  return levelWords(look.level) ?? "OK";
}

/** The level in one word, as the Capacity figure says it. */
export const short = (l: CapacityLevel) => (l === "full" ? "Full" : l === "near" ? "Near" : "OK");

/** The figures after the level, the main one first: at OK the load; near or full, the pressures that are there (0.0%
 * says nothing), then the load. */
function figures(look: ControlCapacityCopy, brief = false): string[] {
  const pressure = brief ? "" : " pressure";
  const parts: string[] = [];
  if (look.level !== "ok") {
    if (look.pressureIOFullAvg60 >= 0.05) parts.push(`I/O${pressure} ${percent(look.pressureIOFullAvg60)}`);
    if (look.pressureMemoryFullAvg60 >= 0.05) parts.push(`Memory${pressure} ${percent(look.pressureMemoryFullAvg60)}`);
  }
  parts.push(brief ? `Load ${look.loadPerCore.toFixed(1)}` : `Load ${look.loadPerCore.toFixed(1)} per core`);
  return parts;
}

/** The row's line: "Near capacity · I/O pressure 16.7% · Load 1.4 per core"; before the first look, the no-reading line. */
export function rowLine(look: ControlCapacityCopy | null, none = noReading): string {
  return look ? [level(look), ...figures(look)].join(" · ") : none;
}

/** The row's line when the whole line does not fit (#7366): "Near capacity · I/O 16.7%" or "OK · Load 1.4". */
export function shortRowLine(look: ControlCapacityCopy | null, none = noReading): string {
  return look ? [level(look), ...figures(look, true).slice(0, 1)].join(" · ") : none;
}

/** The row's help text (#7366): the whole line, then the reason as a sentence when the level is near or full. */
export function rowHelp(look: ControlCapacityCopy | null, none = noReadingDetail): string {
  if (!look) return none;
  return look.level === "ok" ? rowLine(look) : `${rowLine(look)}\n${sentence(look)}`;
}

/** Under a machine that also runs agents, while its control plane is near or full: "Control plane near capacity". */
export function agentsRowLine(look: ControlCapacityCopy | null): string | null {
  const words = levelWords(look?.level);
  return words ? `Control plane ${words.toLowerCase()}` : null;
}

/** The page's and the strip's sentence: "The control plane is near capacity: its disk is busy." The control plane's own
 * reason, with a capital and a full stop. With a label (#7362) it names the machine: "The control plane on box-2 is …". */
export function sentence(look: ControlCapacityCopy, on: string | null = null): string {
  const reason = look.reason.trim();
  const words = reason
    ? `${reason[0]!.toUpperCase()}${reason.slice(1)}${reason.endsWith(".") ? "" : "."}`
    : look.level === "ok" ? "The control plane is within capacity." : `The control plane is ${level(look).toLowerCase()}.`;
  if (!on) return words;
  const lead = "The control plane";
  return words.startsWith(lead) ? `${lead} on ${on}${words.slice(lead.length)}` : `${on}: ${words}`;
}

/** Why there is no reading (#7364): before the first read and after an empty answer, that one will come; after a first
 * read that failed, that it failed; when the answers stay empty for one look or more, that the control plane gives none. */
export function noReadingWords(failed: boolean, emptyForMs: number | null): { line: string; detail: string } {
  if (failed) return { line: noReadingNow, detail: readFailedDetail };
  if (emptyForMs !== null && emptyForMs >= lookSeconds * 1000) return { line: noReadingNow, detail: emptyDetail };
  return { line: noReading, detail: noReadingDetail };
}

export interface Figure { label: string; value: string; detail?: string }

/** The four figures across the top: the level, load per core, memory and I/O pressure. */
export function summary(look: ControlCapacityCopy): Figure[] {
  return [
    { label: "Capacity", value: short(look.level) },
    { label: "Load per core", value: load(look.loadPerCore), detail: `${load(look.loadPerCore5)} over 5 min` },
    { label: "Memory pressure", value: percent(look.pressureMemoryFullAvg60), detail: "Full · avg 60 s" },
    { label: "I/O pressure", value: percent(look.pressureIOFullAvg60), detail: "Full · avg 60 s" },
  ];
}

/** The sections under them: Memory, Process and Store, each a line of figures. */
export function sections(look: ControlCapacityCopy): { title: string; figures: Figure[] }[] {
  return [
    { title: "Memory", figures: [{ label: "Available", value: bytes(look.memoryAvailableBytes) },
      { label: "Swap used", value: `${Math.round(look.swapUsedPercent)}%` }, { label: "Go heap", value: bytes(look.heapInuseBytes) }] },
    { title: "Process", figures: [{ label: "Goroutines", value: count(look.goroutines) }, { label: "Open files", value: count(look.openFiles) }] },
    { title: "Store", figures: [{ label: "Files", value: count(look.storeFiles) }, { label: "Size", value: bytes(look.storeBytes) }] },
  ];
}

// The page's state, as the window's ControlCapacityState.

/** The latest look of each copy, by copy id. */
export const copies = signal<Readonly<Record<string, ControlCapacityCopy>>>({});
/** When the last read worked (#5139). */
export const readAt = signal<number | null>(null);
/** Why the last read did not work; the page keeps the last looks under it. */
export const failure = signal<string | null>(null);
/** A read is under way. */
export const refreshing = signal(false);
/** When the reads that worked began to answer with no copy, while they still do (#7364). */
export const emptySince = signal<number | null>(null);

export const looks = (): ControlCapacityCopy[] => Object.values(copies.value);

/** The copies once a read has worked; null before (#7393): which machine runs the control plane falls back to the role. */
export const readLooks = (): ControlCapacityCopy[] | null => (readAt.value === null ? null : looks());

/** Why the page has no reading now. */
export function noReadingNowWords(): { line: string; detail: string } {
  const read = readAt.value;
  const since = emptySince.value;
  return noReadingWords(read === null && failure.value !== null, read !== null && since !== null ? read - since : null);
}

/** control/capacity's answer. */
export function takeBoard(board: readonly ControlCapacityCopy[], now = Date.now()): void {
  const kept: Record<string, ControlCapacityCopy> = {};
  for (const c of board) {
    const before = kept[c.copy];
    if (!before || at(c) > at(before)) kept[c.copy] = c;
  }
  copies.value = kept;
  readAt.value = now;
  failure.value = null;
  emptySince.value = board.length === 0 ? (emptySince.value ?? now) : null;
}

/** A read that failed: the last looks stay under the words. */
export function takeFailure(error: unknown): void {
  failure.value = error instanceof CallFailed ? failedWords(error) : refreshFailed;
}

/** control/capacityChanged: one copy's level moved. */
export function takeLook(look: ControlCapacityCopy): void {
  copies.value = { ...copies.value, [look.copy]: look };
  emptySince.value = null;
}

/** Whether the machine runs a copy of the control plane now: a copy names it (#7393); before the first read, its role. */
export function runsCopy(machine: ControlMachine | undefined, rows: readonly ControlMachine[]): boolean {
  return !!machine && runsControl(machine, readLooks(), rows.filter(controlRole).length);
}

/** The latest look of the control plane on that machine; null when it runs none, or before the first. */
export function lookOn(machine: string, rows: readonly ControlMachine[]): ControlCapacityCopy | null {
  return lookFor(machine, looks(), rows.filter(controlRole).length);
}
