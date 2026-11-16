// The Machines page's list (#5285, epic #5281): a search, filter chips with counts, a sort and a table that loads by
// page from machines/list (#5283), then Start, Stop and Delete on the rows that are ticked. A control plane before
// machines/2 answers the plain array, so the page filters and sorts that array itself and has no counts to say.
import type { ControlMachine, MachineCounts, MachineListQuery } from "../protocol/generated";
import { machineActions } from "./machineRow";

export const pageSize = 50;

export type MachineFilterKey = "state" | "provider" | "os" | "version" | "labels";
export const filterKeys: readonly MachineFilterKey[] = ["state", "provider", "os", "version", "labels"];
export const filterTitle: Record<MachineFilterKey, string> = {
  state: "State", provider: "Provider", os: "OS", version: "Version", labels: "Label",
};

export type MachineSort = NonNullable<MachineListQuery["sort"]>;
export const sorts: readonly MachineSort[] = ["name", "state", "lastSeen", "version"];
export const sortTitle: Record<MachineSort, string> = { name: "Name", state: "State", lastSeen: "Last seen", version: "Version" };

/** What the person chose: the search words, one value for each filter (labels: all of them), and the sort. */
export interface MachineChoice {
  search: string;
  state?: string;
  provider?: string;
  os?: string;
  version?: string;
  labels: readonly string[];
  sort: MachineSort;
  /** Only these machines, from "and N more" (#5289): a link's `?hosts=`. */
  only?: readonly string[];
}

export const noChoice: MachineChoice = { search: "", labels: [], sort: "name" };

/** Whether any search or filter narrows the list. */
export function narrowed(c: MachineChoice): boolean {
  return (c.only?.length ?? 0) > 0 || c.search.trim() !== "" || !!c.state || !!c.provider || !!c.os || !!c.version || c.labels.length > 0;
}

/** The query of one page; `cursor` is the previous page's `next`. */
export function pageQuery(c: MachineChoice, cursor?: string): MachineListQuery {
  const q: MachineListQuery = { sort: c.sort, limit: pageSize };
  if (c.search.trim()) q.query = c.search.trim();
  if (c.state) q.state = c.state;
  if (c.provider) q.provider = c.provider;
  if (c.os) q.os = c.os;
  if (c.version) q.version = c.version;
  if (c.labels.length > 0) q.labels = [...c.labels];
  if (cursor) q.cursor = cursor;
  return q;
}

/** A chip's value picked, or let go when it already is the one. */
export function toggled(c: MachineChoice, key: MachineFilterKey, value: string): MachineChoice {
  if (key === "labels") {
    return { ...c, labels: c.labels.includes(value) ? c.labels.filter((l) => l !== value) : [...c.labels, value] };
  }
  return { ...c, [key]: c[key] === value ? undefined : value };
}

export function chosen(c: MachineChoice, key: MachineFilterKey, value: string): boolean {
  return key === "labels" ? c.labels.includes(value) : c[key] === value;
}

export interface Chip { key: MachineFilterKey; value: string; count: number; on: boolean }

/** The chips of each filter the counts have values for (and the one chosen, even at 0), most machines first. */
export function chips(counts: MachineCounts | null, c: MachineChoice): Chip[] {
  if (!counts) return [];
  return filterKeys.flatMap((key) => {
    const seen = counts[key] ?? {};
    const values = new Set([...Object.keys(seen), ...(key === "labels" ? c.labels : c[key] ? [c[key]!] : [])]);
    return [...values].map((value) => ({ key, value, count: seen[value] ?? 0, on: chosen(c, key, value) }))
      .sort((a, b) => b.count - a.count || a.value.localeCompare(b.value));
  });
}

/** What a chip says: `online 84`. */
export function chipWords(chip: Chip): string {
  return `${chip.value} ${chip.count}`;
}

/** The counts of a plain array, when the control plane has no pages: the same shape machines/list would say. */
export function countsOf(all: readonly ControlMachine[]): MachineCounts {
  const out: MachineCounts = { state: {}, labels: {}, provider: {}, os: {}, version: {} };
  const bump = (m: Record<string, number>, k: string | undefined) => { if (k) m[k] = (m[k] ?? 0) + 1; };
  for (const h of all) {
    bump(out.state, h.state);
    bump(out.provider, h.workspace?.providerName);
    bump(out.os, h.platform);
    bump(out.version, h.version);
  }
  return out;
}

/** A plain array filtered, sorted and cut as machines/list would, for a control plane before machines/2. */
export function localList(all: readonly ControlMachine[], c: MachineChoice): ControlMachine[] {
  const words = c.search.trim().toLowerCase();
  const rows = all.filter((h) => (!c.only?.length || c.only.includes(h.id)) && (!words || h.name.toLowerCase().includes(words) || h.id.toLowerCase().includes(words))
    && (!c.state || h.state === c.state) && (!c.provider || h.workspace?.providerName === c.provider)
    && (!c.os || h.platform === c.os) && (!c.version || h.version === c.version));
  const by = (h: ControlMachine) => c.sort === "state" ? h.state : c.sort === "version" ? h.version : h.name;
  return rows.sort((a, b) => by(a).localeCompare(by(b), undefined, { numeric: true }) || a.name.localeCompare(b.name));
}

/** Which of the ticked machines each action reaches, and the words for the bar over the table. */
export interface BulkActions { start: ControlMachine[]; stop: ControlMachine[]; delete: ControlMachine[] }

export function bulkActions(selected: readonly ControlMachine[], macID: string): BulkActions {
  const of = (pick: "start" | "stop" | "delete") => selected.filter((h) => machineActions(h, macID)[pick]);
  return { start: of("start"), stop: of("stop"), delete: of("delete") };
}

export function selectedWords(n: number): string {
  return `${n} selected`;
}

/** An action's button: `Stop 3` says how many of the ticked ones it reaches; it is off when none. */
export function bulkLabel(verb: string, reached: number, selected: number): string {
  return reached === selected || reached === 0 ? verb : `${verb} ${reached}`;
}

/** Delete's question for many: it always asks, and names the unpushed work of each machine that has some. */
export function bulkDeleteQuestion(names: readonly string[], unpushed: readonly string[]): string {
  const list = names.length <= 5 ? names.join(", ") : `${names.slice(0, 5).join(", ")} and ${names.length - 5} more`;
  const work = unpushed.length === 0 ? "" : ` Work not pushed on ${unpushed.length === 1 ? "it" : "them"}: ${unpushed.join("; ")}.`;
  return `Delete ${names.length === 1 ? list : `${names.length} machines (${list})`}? Their providers delete them and the sessions on them stop.${work}`;
}

export const showMoreWords = (shown: number, total: number): string => `Show more (${shown} of ${total})`;
export const emptyWords = (narrow: boolean): string => narrow ? "No machine matches. Clear the search or a filter." : "No machines yet. Add one under Machines in the sidebar.";
