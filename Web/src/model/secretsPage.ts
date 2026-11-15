// The Secrets page (#5257, specs/5254-secrets-accounts-environment/wireframes.md sections 2 and 5): every secret of the
// person's in one list (theirs, those for one session and those on one machine), with a search, chips, a sort, and the
// Needs you group on top. The rules are SecretsPage's in Packages/AgentsKit/Sources/AgentsKitCore/Model/SecretsPage.swift.
import type { LocalSecret, OutsideAccount, SecretLastOp, SessionSecret, UserSecretRow } from "../protocol/generated";
import { fromWireDate } from "../protocol/dates";
import { age, hint as secretHint, type Part, setPart, usedPart } from "./secretInfo";
import { cardKind } from "./userScope";

export type Place = "yours" | "session" | "machine";
export type Chip = "all" | Place | "needsYou";
export type Sort = "lastUsed" | "name" | "added";

export const places: readonly Place[] = ["yours", "session", "machine"];
export const chips: readonly Chip[] = ["all", "yours", "session", "machine", "needsYou"];
export const sorts: readonly Sort[] = ["lastUsed", "name", "added"];

export const chipTitles: Record<Chip, string> = {
  all: "All", yours: "Yours", session: "For a session", machine: "On a machine", needsYou: "Needs you",
};
export const sortTitles: Record<Sort, string> = { lastUsed: "Last used", name: "Name", added: "Added" };
export const groupTitles: Record<Place, string> = { yours: "Yours", session: "For a session", machine: "On a machine" };

/** One secret, whatever its place. It holds no value. */
export interface Item {
  /** `yours/NAME`, `session/<agent id>/NAME` or `machine/NAME`: a name can be in more than one place. */
  id: string;
  name: string;
  place: Place;
  description?: string | undefined;
  hint?: string | undefined;
  isCard: boolean;
  asksBeforeUse: boolean;
  added: Date | null;
  updated: Date | null;
  lastOp?: SecretLastOp | undefined;
  agentID?: string | undefined;
  sessionTitle?: string | undefined;
  /** The names of the machines it is on. */
  machines: string[];
  /** The services of the accounts that name it (#3967). */
  services: string[];
  /** The projects of those accounts: a secret has none of its own. */
  projects: string[];
  /** Why it needs the person, or null. */
  needs: string | null;
  /** The row it came from, for the sheets: the person's own secrets only. */
  row?: UserSecretRow | undefined;
  session?: SessionSecret | undefined;
  local?: LocalSecret | undefined;
}

export interface Group {
  id: "needsYou" | Place;
  title: string;
  items: Item[];
}

export const unusedDays = 90;

export const w = {
  title: "Secrets",
  searchPrompt: "Search names, descriptions, sessions, machines…",
  add: "Add",
  addSecret: "Add a Secret…",
  addCard: "Add a Card…",
  addForSession: "Add for a Session…",
  sortLabel: "Sort",
  projectAny: "Any project",
  selectOne: "Select a secret to see it.",
  clearSearch: "Clear Search",
  replaceValue: "Replace Value…",
  edit: "Edit",
  remove: "Remove…",
  recentUse: "Recent use",
  usedBy: "Used by",
  noUse: "Not used yet.",
  editNote: "Ask before use and the description are sealed with the value, so type the value again to change them.",
  settingsLine: "Your secrets are in the sidebar, under Status.",
  openSecrets: "Open Secrets",
  none: "No secrets yet. Agents ask you for one when they need it, or add one here.",
} as const;

export function noMatch(search: string): string {
  return `No secret matches “${search}”.`;
}

function date(text: string | undefined): Date | null {
  if (!text) return null;
  const at = new Date(text);
  return Number.isNaN(at.getTime()) ? null : at;
}

/** When it was last read, for the sort and the row's age: a read or a denied use only. */
export function lastUsed(item: Pick<Item, "lastOp">): Date | null {
  const op = item.lastOp;
  return op && (op.what === "read" || op.what === "denied") ? fromWireDate(op.at) : null;
}

/** "Yours, on 3 machines", "For “Fix the sign-in”, on this Mac only", "On devbox only". */
export function whereLine(item: Item): string {
  switch (item.place) {
    case "yours": {
      const n = item.machines.length;
      return `Yours, ${n === 0 ? "on no machine yet" : n === 1 ? "on 1 machine" : `on ${n} machines`}`;
    }
    case "session": return `For “${item.sessionTitle ?? "a session"}”, on ${item.machines[0] ?? "one machine"} only`;
    case "machine": return `On ${item.machines[0] ?? "one machine"} only`;
  }
}

export function kindLabel(item: Item): string {
  return item.isCard ? "Card" : "Secret";
}

/** The row's end: `••••ab12`, or `Card`. */
export function rowHint(item: Item): string {
  return item.isCard ? "Card" : secretHint(item.hint);
}

/** The row's age: of the last use, else of the last change. "2 h", "12 d", "20 min". */
export function shortAge(item: Item, now = new Date()): string | null {
  const at = lastUsed(item) ?? item.updated ?? item.added;
  if (!at) return null;
  const seconds = Math.max(0, (now.getTime() - at.getTime()) / 1000);
  if (seconds < 60) return "now";
  if (seconds < 3600) return `${Math.floor(seconds / 60)} min`;
  if (seconds < 86_400) return `${Math.floor(seconds / 3600)} h`;
  return `${Math.floor(seconds / 86_400)} d`;
}

/** The line under the row's name: the warning for one in Needs you, else the description, else its session and machine. */
export function subtitle(item: Item): string {
  if (item.needs) return item.needs;
  const text = item.description?.trim();
  if (text) return text;
  switch (item.place) {
    case "session": return [item.sessionTitle, item.machines[0]].filter((s): s is string => !!s).join(" · ");
    case "machine": return `Saved by an agent on ${item.machines[0] ?? "one machine"}`;
    case "yours": return "No description";
  }
}

/** The age line of the detail: "Added 27 d ago · Used 2 h ago". */
export function ageLine(item: Item, now = new Date(), timeZone?: string): Part[] {
  const set = setPart(item.added, item.updated, now, timeZone);
  return [...(set ? [set] : []), usedPart(item.lastOp, now, timeZone)];
}

/** Recent use: the newest operation the machines keep (#4158). "2 h ago: read by “Fix the shop sign-in”". */
export function recentLine(item: Item, now = new Date(), agentTitle: (id: string) => string | undefined = () => undefined): string | null {
  const last = item.lastOp;
  if (!last) return null;
  const verbs: Record<string, string> = { read: "read", denied: "use denied", added: "added", replaced: "replaced", removed: "removed" };
  const title = last.agent ? agentTitle(last.agent) : undefined;
  const who = title ? ` by “${title}”` : !last.agent && last.what !== "read" ? " by you" : "";
  return `${age(fromWireDate(last.at), now)}: ${verbs[last.what] ?? last.what}${who}`;
}

export interface Sources {
  yours: readonly UserSecretRow[];
  sessions?: readonly SessionSecret[];
  local?: readonly LocalSecret[];
  accounts?: readonly OutsideAccount[];
  machineNames?: Record<string, string>;
  /** The machine whose session and local secrets these are. */
  hostName?: string;
  /** The person's secrets this page has no copy of yet (#5115). */
  noCopy?: ReadonlySet<string>;
  didNotOpen?: ReadonlySet<string>;
  clientName?: string;
}

function unused(item: Item, now: Date): string | null {
  const since = lastUsed(item) ?? item.updated ?? item.added;
  return since && now.getTime() - since.getTime() > unusedDays * 86_400_000 ? `Not used in ${unusedDays} days.` : null;
}

function onlyOneMachine(rows: readonly UserSecretRow[]): boolean {
  if (!rows.some((r) => r.backedUp === false)) return false;
  return new Set(rows.flatMap((r) => [...r.machines, ...(r.waiting ?? [])])).size === 1;
}

/** The items from what the page read. */
export function items(s: Sources, now = new Date()): Item[] {
  const accounts = s.accounts ?? [];
  const names = s.machineNames ?? {};
  const host = s.hostName ?? "this Mac";
  const client = s.clientName ?? "this browser";
  const uses = (name: string) => accounts.filter((a) => a.secret === name || a.card === name);
  const projects = (name: string) => [...new Set(uses(name).flatMap((a) => a.projects ?? []))].sort();
  const oneMachine = onlyOneMachine(s.yours);
  const out: Item[] = s.yours.map((row) => {
    const item: Item = {
      id: `yours/${row.name}`, name: row.name, place: "yours", description: row.description, hint: row.hint,
      isCard: row.kind === cardKind, asksBeforeUse: row.needsApproval === true, added: date(row.created), updated: date(row.updated),
      lastOp: row.lastOp, machines: row.machines.map((m) => names[m] ?? m), services: uses(row.name).map((a) => a.service),
      projects: projects(row.name), needs: null, row,
    };
    const left = row.leftOn ?? [];
    const firstMachine = row.machines[0];
    if (left.length > 0) item.needs = `It was on ${left.map((l) => l.name).join(", ")}, which was removed.`;
    else if (s.didNotOpen?.has(row.name)) item.needs = `This ${client === "this Mac" ? "Mac" : "browser"}’s copy did not open.`;
    else if (s.noCopy?.has(row.name)) item.needs = `No copy for ${client} yet.`;
    else if (!oneMachine && row.backedUp === false && (row.waiting?.length ?? 0) === 0 && firstMachine !== undefined) {
      item.needs = `Only on ${names[firstMachine] ?? "one machine"}. Not backed up.`;
    } else if ((row.waiting?.length ?? 0) > 0) {
      const n = row.waiting?.length ?? 0;
      item.needs = n === 1 ? "1 machine waits for a copy." : `${n} machines wait for a copy.`;
    } else item.needs = unused(item, now);
    return item;
  });
  for (const secret of s.sessions ?? []) {
    const setAt = fromWireDate(secret.setAt);
    const item: Item = {
      id: `session/${secret.agentID}/${secret.name}`, name: secret.name, place: "session", description: secret.description,
      hint: secret.hint, isCard: false, asksBeforeUse: secret.needsApproval === true,
      added: secret.created !== undefined ? fromWireDate(secret.created) : setAt, updated: setAt, lastOp: secret.lastOp,
      agentID: secret.agentID, sessionTitle: secret.title, machines: [host], services: uses(secret.name).map((a) => a.service),
      projects: projects(secret.name), needs: null, session: secret,
    };
    item.needs = unused(item, now);
    out.push(item);
  }
  for (const secret of s.local ?? []) {
    const setAt = fromWireDate(secret.setAt);
    const item: Item = {
      id: `machine/${secret.name}`, name: secret.name, place: "machine", description: secret.description, isCard: false,
      asksBeforeUse: secret.needsApproval === true, added: setAt, updated: setAt, lastOp: secret.lastOp, machines: [host],
      services: uses(secret.name).map((a) => a.service), projects: projects(secret.name), needs: null, local: secret,
    };
    item.needs = unused(item, now);
    out.push(item);
  }
  return out;
}

/** Whether the search matches: the name, the description, the session, the machines, the projects and the services of
 * the accounts that use it. Never a value: the item has none. Every word must match (AND), in any field. */
export function matches(item: Item, search: string): boolean {
  const words = search.toLowerCase().split(/\s+/).filter(Boolean);
  if (words.length === 0) return true;
  const fields = [item.name, item.description ?? "", item.sessionTitle ?? "", ...item.machines, ...item.projects, ...item.services]
    .map((f) => f.toLowerCase());
  return words.every((word) => fields.some((f) => f.includes(word)));
}

export function inChip(item: Item, chip: Chip): boolean {
  return chip === "all" ? true : chip === "needsYou" ? item.needs !== null : item.place === chip;
}

export function inProject(item: Item, project: string | null): boolean {
  return project === null || item.projects.includes(project);
}

export function projectsOf(list: readonly Item[]): string[] {
  return [...new Set(list.flatMap((i) => i.projects))].sort((a, b) => a.localeCompare(b, undefined, { sensitivity: "base" }));
}

/** Each chip's count, after the search and the project (a chip and the search add together). */
export function counts(list: readonly Item[], search: string, project: string | null = null): Record<Chip, number> {
  const found = list.filter((i) => matches(i, search) && inProject(i, project));
  const out = {} as Record<Chip, number>;
  for (const chip of chips) out[chip] = found.filter((i) => inChip(i, chip)).length;
  return out;
}

export function sorted(list: readonly Item[], sort: Sort): Item[] {
  const byName = (a: Item, b: Item) => a.name.localeCompare(b.name, undefined, { numeric: true }) || (a.id < b.id ? -1 : a.id > b.id ? 1 : 0);
  const time = (d: Date | null) => d?.getTime() ?? -Infinity;
  return [...list].sort((a, b) => {
    if (sort === "name") return byName(a, b);
    const x = sort === "lastUsed" ? time(lastUsed(a)) : time(a.added ?? a.updated);
    const y = sort === "lastUsed" ? time(lastUsed(b)) : time(b.added ?? b.updated);
    return x === y ? byName(a, b) : y > x ? 1 : -1;
  });
}

/** The list: Needs you on top whatever the sort, while it has items, then a group per place. */
export function groups(list: readonly Item[], search: string, chip: Chip, project: string | null, sort: Sort): Group[] {
  const found = sorted(list.filter((i) => matches(i, search) && inChip(i, chip) && inProject(i, project)), sort);
  const out: Group[] = [];
  const needs = found.filter((i) => i.needs !== null);
  if (needs.length > 0) out.push({ id: "needsYou", title: "Needs you", items: needs });
  for (const place of places) {
    const these = found.filter((i) => i.place === place && i.needs === null);
    if (these.length > 0) out.push({ id: place, title: groupTitles[place], items: these });
  }
  return out;
}
