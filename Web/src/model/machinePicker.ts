// The one machine picker (#5287, epic #5281): a search, then the pinned and the recently used machines, then the rest grouped
// by label, each with a state dot. It asks machines/list (#5283) with the typed words, so it holds only a page, never every
// machine. Pinned and recent are this browser's, kept in localStorage as the window keeps them in its defaults.
import type { ControlMachine, MachineListQuery } from "../protocol/generated";

export const pickerLimit = 30;
export const pinnedKey = "agents.machinePicker.pinned";
export const recentKey = "agents.machinePicker.recent";
const recentCap = 5;
/** The most pins, and the longest id of one, as `MachinePins` (#6797): one past that is refused. A pin has no age, on purpose:
 * it stays until it is let go, or until the control plane no longer lists its machine. */
export const pinnedMost = 100;
const idMost = 64;

export type PickerDot = "online" | "stopped" | "offline" | "attention";

/** The state dot of a row: green online, amber needing an update, grey stopped, red anything else that does not answer. */
export function pickerDot(h: ControlMachine): PickerDot {
  if (h.state === "needsUpdate") return "attention";
  if (h.state === "stopped" || h.state === "starting" || h.state === "stopping") return "stopped";
  return h.state === "online" ? "online" : "offline";
}

export interface PickerSection { title: string; machines: ControlMachine[] }

/** The query for the typed words: a page of machines whose name, id or label matches, by name. */
export function pickerQuery(search: string): MachineListQuery {
  const q: MachineListQuery = { sort: "name", limit: pickerLimit };
  if (search.trim()) q.query = search.trim();
  return q;
}

/** A label's group: `key` for `key=value`, the label for a plain one; the provider's own (os=, arch=) after the person's. */
export function groupOf(label: string): string {
  const at = label.indexOf("=");
  return at < 0 ? label : label.slice(0, at);
}

const byName = (a: ControlMachine, b: ControlMachine) => a.name.localeCompare(b.name, undefined, { numeric: true });

/**
 * The sections of a page: Pinned, Recent (in the order last used), then one section for each label group by name, each
 * machine under the first of its person-set labels (the provider's own ones group nothing), and last the ones with no label.
 * A machine appears once, in the first section that takes it. `skip` leaves out machines (the one the session is on).
 */
export function sections(page: readonly ControlMachine[], pinned: readonly string[], recent: readonly string[], skip: (h: ControlMachine) => boolean = () => false): PickerSection[] {
  const rows = page.filter((h) => !skip(h));
  const taken = new Set<string>();
  const take = (ids: readonly string[], title: string): PickerSection[] => {
    const found = ids.map((id) => rows.find((h) => h.id === id)).filter((h): h is ControlMachine => !!h && !taken.has(h.id));
    for (const h of found) taken.add(h.id);
    return found.length ? [{ title, machines: found }] : [];
  };
  const out = [...take(pinned, "Pinned"), ...take(recent, "Recent")];
  const groups = new Map<string, ControlMachine[]>();
  const none: ControlMachine[] = [];
  for (const h of rows.filter((r) => !taken.has(r.id)).sort(byName)) {
    const auto = new Set(h.autoLabels ?? []);
    const own = (h.labels ?? []).find((l) => !auto.has(l));
    if (!own) { none.push(h); continue; }
    const key = groupOf(own);
    groups.set(key, [...(groups.get(key) ?? []), h]);
  }
  for (const key of [...groups.keys()].sort((a, b) => a.localeCompare(b))) out.push({ title: key, machines: groups.get(key)! });
  if (none.length) out.push({ title: out.length ? "Other machines" : "Machines", machines: none });
  return out;
}

function read(storage: Storage | undefined, key: string): string[] {
  try {
    const v = JSON.parse(storage?.getItem(key) ?? "null");
    return Array.isArray(v) ? v.filter((x): x is string => typeof x === "string") : [];
  } catch {
    return [];
  }
}

export const pinnedMachines = (s: Storage | undefined): string[] => read(s, pinnedKey);
export const recentMachines = (s: Storage | undefined): string[] => read(s, recentKey);

/** A machine pinned, or let go when it is. Returns the new list; null when the pin is refused. */
export function togglePin(s: Storage | undefined, id: string): string[] | null {
  const now = pinnedMachines(s);
  if (!now.includes(id) && (now.length >= pinnedMost || id.length > idMost)) return null;
  const next = now.includes(id) ? now.filter((x) => x !== id) : [...now, id];
  try { s?.setItem(pinnedKey, JSON.stringify(next)); } catch { /* storage full or off: the pin lasts this page only */ }
  return next;
}

/** Keeps only the pins that `there` says still have a machine. Returns the new list. */
export function keepPins(s: Storage | undefined, there: (id: string) => boolean): string[] {
  const now = pinnedMachines(s);
  const next = now.filter(there);
  if (next.length < now.length) {
    try { s?.setItem(pinnedKey, JSON.stringify(next)); } catch { /* as above */ }
  }
  return next;
}

/** A machine picked goes first in Recent; the list keeps the last five. */
export function rememberMachine(s: Storage | undefined, id: string): string[] {
  const next = [id, ...recentMachines(s).filter((x) => x !== id)].slice(0, recentCap);
  try { s?.setItem(recentKey, JSON.stringify(next)); } catch { /* as above */ }
  return next;
}

export const pickerWords = {
  search: "Search machines by name or label",
  full: `At most ${pinnedMost} machines can be pinned. Unpin one first.`,
  none: "No machine matches. Clear the search.",
  pin: (name: string) => `Pin ${name}`,
  unpin: (name: string) => `Unpin ${name}`,
  more: (shown: number, total: number) => `Showing ${shown} of ${total}: type to narrow`,
};
