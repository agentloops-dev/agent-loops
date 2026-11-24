// The words of a warm pool's card and of New Machine's Start from (#5354, epic #5353, boards 1 and 5), as the window's
// WarmPoolWords: the same on every client that draws them.
import type { WarmPool, WarmPoolClaims, WarmPoolMachine } from "../protocol/generated";
import { fromWireDate } from "../protocol/dates";
import { duration } from "./machineSetup";

/** The chip on a pool's card: *Healthy* when it holds what it aims at, *Filling* while it makes up to it, else its state. */
export function poolChip(pool: WarmPool): string {
  switch (pool.state) {
    case "stopped": return "Stopped";
    case "off-hours": return "Off hours";
    case "empty": return "Min 0";
    case "keeping": return (pool.ready ?? 0) >= pool.target ? "Healthy" : "Filling";
  }
}

/** A project's name from the path a pool keeps. */
export function projectName(path: string): string {
  return path.replace(/\/+$/, "").split("/").pop() ?? path;
}

/** The line under its name: *s-8vcpu-16gb · fra1 · claude, codex · projects Agents, website · …*. */
export function poolSummary(pool: WarmPool): string {
  const parts: string[] = [];
  if (pool.size) parts.push(pool.size);
  if (pool.region) parts.push(pool.region);
  if (pool.runtimes?.length) parts.push(pool.runtimes.join(", "));
  const projects = (pool.projects ?? []).map(projectName);
  parts.push(projects.length ? `projects ${projects.join(", ")}` : "no projects");
  parts.push("Agents kept at the latest release");
  return parts.join(" · ");
}

/** *ready (min 2, max 4)*, beside the number ready. */
export function readyLine(pool: WarmPool): string {
  return `ready (min ${pool.minReady}, max ${pool.maxSize})`;
}

/** Why a pool makes nothing now, or null when it can make. */
export function poolStateLine(pool: WarmPool): string | null {
  switch (pool.state) {
    case "stopped": return `Stopped: ${pool.stoppedBecause ?? "three makes failed in a row"}. Save the pool to try again.`;
    case "off-hours": return "Outside its hours: it holds no ready machine and makes none.";
    case "empty": return "Min ready is 0: it makes nothing.";
    case "keeping": return pool.waitingBecause ? `Waiting to make a machine: ${pool.waitingBecause}.` : null;
  }
}

/** The last seven days: *41 claims · median 22 s · 3 found it empty*. */
export function claimsLine(claims: WarmPoolClaims | undefined): string {
  if (!claims || claims.count <= 0) return "No claims yet";
  const parts = [claims.count === 1 ? "1 claim" : `${claims.count} claims`];
  if (claims.medianSeconds !== undefined) parts.push(`median ${duration(claims.medianSeconds)}`);
  if (claims.empty > 0) parts.push(`${claims.empty} found it empty`);
  return parts.join(" · ");
}

/** One machine's line: *pool-fra1-a · ready 2 h 38 min*, *pool-fra1-c · installing Claude · 41 s*, *… · failed: quota*. */
export function poolMachineLine(machine: WarmPoolMachine, now = new Date()): string {
  const age = duration((now.getTime() - fromWireDate(machine.madeAt).getTime()) / 1000);
  switch (machine.state) {
    case "ready": return `${machine.name} · ready ${age}`;
    case "setting-up": return `${machine.name} · ${machine.detail || "setting up"} · ${age}`;
    case "failed": return `${machine.name} · failed${machine.detail !== undefined ? `: ${machine.detail}` : ""}`;
  }
}

/** The hourly price in a provider's `x-costs` words: *$0.05/hour* is 0.05. Null when it names none. */
export function hourly(cost: string | undefined): number | null {
  const at = cost?.indexOf("$") ?? -1;
  if (!cost || at < 0) return null;
  const digits = /^[\d.]+/.exec(cost.slice(at + 1))?.[0];
  const value = digits ? Number(digits) : NaN;
  return Number.isFinite(value) ? value : null;
}

/** *$0.38*, or *$4.6* for a larger sum. */
export function dollars(amount: number): string {
  return amount < 1 ? `$${amount.toFixed(2)}` : `$${amount.toFixed(1)}`;
}

/** What the idle machines cost now: *Idle ones cost about $0.38 an hour*; with no price, *Cost not known*. */
export function idleCost(pool: WarmPool, price: number | null): string {
  if (price === null) return "Cost not known";
  return `Idle ones cost about ${dollars(((pool.ready ?? 0) + (pool.settingUp ?? 0)) * price)} an hour`;
}

/** The form's cost line: *Min 2: about $0.38 an hour · at the max of 4: $0.76 an hour*. */
export function formCost(minReady: number, maxSize: number, price: number | null): string {
  if (price === null) return "Cost not known: the provider names no price for this size.";
  return `Min ${minReady}: about ${dollars(minReady * price)} an hour · at the max of ${maxSize}: ${dollars(maxSize * price)} an hour`;
}

/** What the form refuses before Save: a name, and min ready no more than max size. */
export function formProblem(name: string, minReady: number, maxSize: number): string | null {
  if (!name.trim()) return "A warm pool needs a name.";
  if (maxSize < 1) return "Max size is at least 1.";
  if (minReady > maxSize) return `Min ready (${minReady}) is above max size (${maxSize}).`;
  return null;
}

/** *Replace a ready machine after*'s choices, in seconds. */
export const replaceChoices: readonly { label: string; seconds: number }[] = [
  { label: "6 hours", seconds: 6 * 3600 }, { label: "12 hours", seconds: 12 * 3600 }, { label: "24 hours", seconds: 24 * 3600 },
  { label: "2 days", seconds: 48 * 3600 }, { label: "7 days", seconds: 7 * 24 * 3600 },
];

/** The choice nearest a pool's age, 24 hours when it has none. */
export function nearestReplace(replaceAfter: number | undefined): number {
  const want = replaceAfter ?? 86400;
  return replaceChoices.reduce((a, b) => (Math.abs(b.seconds - want) < Math.abs(a.seconds - want) ? b : a)).seconds;
}

/** When a pool runs: all the time, or working hours every day or on weekdays. */
export type PoolRuns = "always" | "everyDay" | "weekdays";
export const runsWords: Record<PoolRuns, string> = { always: "All the time", everyDay: "Every day", weekdays: "Weekdays" };

/** A pool's hours as the form's choice. */
export function runsOf(days: readonly number[] | undefined): PoolRuns {
  if (!days) return "always";
  return days.length === 5 && [1, 2, 3, 4, 5].every((d) => days.includes(d)) ? "weekdays" : "everyDay";
}

/** The form's choice as the days the wire takes; null for all the time. */
export function daysOf(runs: PoolRuns): number[] | null {
  return runs === "always" ? null : runs === "weekdays" ? [1, 2, 3, 4, 5] : [0, 1, 2, 3, 4, 5, 6];
}

/** The pools Start from offers for a provider and size: those of the provider and size that are making machines, the
 * ones with a ready machine first, as a claim takes them. */
export function matchingPools(pools: readonly WarmPool[], provider: string | undefined, size: string | undefined): WarmPool[] {
  const same = pools.filter((p) => p.provider === provider && p.state !== "stopped" && (p.size ?? "") === (size ?? ""));
  return [...same.filter((p) => (p.ready ?? 0) > 0), ...same.filter((p) => (p.ready ?? 0) === 0)];
}

/** Start from's line for a pool: *2 ready · about 22 s*; *none ready now · a new machine instead*. */
export function startFromLine(pool: WarmPool): string {
  const ready = pool.ready ?? 0;
  if (ready <= 0) return "none ready now · a new machine instead";
  const time = pool.claims?.medianSeconds !== undefined ? `about ${duration(pool.claims.medianSeconds)}` : "in seconds";
  return `${ready} ready · ${time}`;
}

/** Start from's line for a new machine. */
export const newMachineLine = "a few minutes";

/** A pool's name as its machines' names begin, as the control plane's `machineName` makes it. */
function slug(pool: string): string {
  let out = "";
  let dash = false;
  for (const c of pool.toLowerCase()) {
    if (/^[a-z0-9]$/.test(c)) {
      if (dash && out) out += "-";
      dash = false;
      out += c;
    } else {
      dash = true;
    }
  }
  if (!/^[a-z]/.test(out)) out = `pool-${out}`;
  if (out.length > 24) out = out.slice(0, 24).replace(/-+$/, "");
  return out;
}

/** Whether a machine's name is one a pool of these names gives its machines: the pool's slug, a dash and six hex
 * digits. The sidebar leaves them out: nobody owns a pool's machine until a claim. */
export function isPoolMachine(name: string, pools: readonly string[]): boolean {
  const dash = name.lastIndexOf("-");
  if (dash < 0 || !/^[0-9a-fA-F]{6}$/.test(name.slice(dash + 1))) return false;
  const head = name.slice(0, dash);
  return pools.some((p) => slug(p) === head);
}

/** The note on a machine's page once a claim gave it: the pool makes another. */
export function replacedNote(pool: string): string {
  return `The pool replaced this machine the moment it was claimed: “${pool}” makes a new one now (Settings ▸ Machine providers).`;
}
