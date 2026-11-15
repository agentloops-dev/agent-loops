// The Accounts page (#5258, specs/5254-secrets-accounts-environment/wireframes.md sections 3 and 5): every outside account
// of the person's in one list, with a search, chips, a Project and a Service menu, a sort, the Needs you group on top and
// the total a month at the foot, in the Secrets page's frame. The rules are AccountsPage's in
// Packages/AgentsKit/Sources/AgentsKitCore/Model/AccountsPage.swift.
import type { OutsideAccount, UserSecretRow } from "../protocol/generated";
import { fromWireDate } from "../protocol/dates";
import { accountAbout, accountAsks, accountNotBackedUp, accountProjects, expiresSoon, money } from "./accounts";
import { onlyMachine } from "./secretInfo";

export type Chip = "all" | "needsYou" | "asks";
export type Sort = "service" | "lastUsed" | "cost";

export const chips: readonly Chip[] = ["all", "needsYou", "asks"];
export const sorts: readonly Sort[] = ["service", "lastUsed", "cost"];
export const chipTitles: Record<Chip, string> = { all: "All", needsYou: "Needs you", asks: "Asks before use" };
export const sortTitles: Record<Sort, string> = { service: "Service", lastUsed: "Last used", cost: "Cost" };

/** One account, and what the page says of it. It holds no value. */
export interface Item {
  /** As the window's `OutsideAccount.id`: the service and the address, in lower case. */
  id: string;
  account: OutsideAccount;
  /** Why it needs the person, or null: its token expires in 30 days or less, or its secret is on one machine only. */
  needs: string | null;
}

export interface Group {
  id: "needsYou" | "accounts";
  /** None for the rest after Needs you. */
  title: string | null;
  items: Item[];
}

export const w = {
  title: "Accounts",
  searchPrompt: "Search service, address, plan, secret…",
  add: "Add Account…",
  sortLabel: "Sort",
  projectAny: "Any project",
  serviceAny: "Any service",
  selectOne: "Select an account to see it.",
  clearSearch: "Clear Search",
  edit: "Edit",
  remove: "Remove…",
  openSecret: "Open in Secrets",
  none: "No accounts yet. Agents add one when they make it, or add one here.",
  removeMessage: "Only the record goes. The account itself, and its secrets, stay.",
} as const;

export function noMatch(search: string): string {
  return `No account matches “${search}”.`;
}

export function accountID(a: Pick<OutsideAccount, "service" | "address">): string {
  return `${a.service.toLowerCase()}#${(a.address ?? "").toLowerCase()}`;
}

function text(s: string | undefined): string | null {
  return s?.trim() || null;
}

/** The row's and the detail's title: "GitHub · alexec", the user name, else the address, after the service. */
export function accountName(a: OutsideAccount): string {
  const who = text(a.username) ?? text(a.address);
  return who ? `${a.service} · ${who}` : a.service;
}

/** The row's cost: "$4/mo", "free" for 0, else null. */
export function shortCost(a: OutsideAccount): string | null {
  if (a.monthlyCostUsd === undefined) return null;
  return a.monthlyCostUsd === 0 ? "free" : `${money(a.monthlyCostUsd)}/mo`;
}

/** The line under the row's name: the warning for one in Needs you, else the description. */
export function subtitle(item: Item): string {
  return item.needs ?? accountAbout(item.account);
}

/** "Team · $4 a month", or null. */
export function planLine(a: OutsideAccount): string | null {
  const parts = [text(a.plan), a.monthlyCostUsd === undefined ? null : `${money(a.monthlyCostUsd)} a month`].filter((p): p is string => !!p);
  return parts.length ? parts.join(" · ") : null;
}

/** The foot of the list: "23 accounts · $61 a month", or "4 of 23 · $16 a month" when a filter hides some. */
export function foot(shown: readonly Item[], all: number): string {
  const total = shown.reduce((sum, i) => sum + (i.account.monthlyCostUsd ?? 0), 0);
  const count = shown.length === all ? `${all} ${all === 1 ? "account" : "accounts"}` : `${shown.length} of ${all}`;
  return `${count} · ${money(total)} a month`;
}

/** The items from the person's accounts and secrets. The not-backed-up line is left out when every secret is on one
 * machine: the page says that once instead. */
export function items(accounts: readonly OutsideAccount[], secrets: readonly UserSecretRow[], names: Record<string, string> = {},
  now = Date.now()): Item[] {
  const oneMachine = onlyMachine(secrets) !== null;
  return accounts.map((account) => ({
    id: accountID(account), account,
    needs: expiresSoon(account, now) ?? (oneMachine ? null : accountNotBackedUp(account, secrets, names)),
  }));
}

/** Whether the search matches: the service, the address, the user name, the plan, the description, the projects and the
 * names of the secret and the card. Never a value: an account holds none. Every word must match (AND). */
export function matches(item: Item, search: string): boolean {
  const words = search.toLowerCase().split(/\s+/).filter(Boolean);
  if (words.length === 0) return true;
  const a = item.account;
  const fields = [a.service, a.address, a.username, a.plan, a.description, a.secret, a.card, ...(a.projects ?? [])]
    .filter((f): f is string => f !== undefined).map((f) => f.toLowerCase());
  return words.every((word) => fields.some((f) => f.includes(word)));
}

export function inChip(item: Item, chip: Chip): boolean {
  return chip === "all" ? true : chip === "needsYou" ? item.needs !== null : accountAsks(item.account);
}

/** An account that names no project is for every project, as agents read it. */
export function inProject(item: Item, project: string | null): boolean {
  if (project === null) return true;
  const named = item.account.projects ?? [];
  return named.length === 0 || named.some((n) => n.toLowerCase() === project.toLowerCase());
}

export function inService(item: Item, service: string | null): boolean {
  return service === null || item.account.service.toLowerCase() === service.toLowerCase();
}

export function projectsOf(list: readonly Item[]): string[] {
  return accountProjects(list.map((i) => i.account));
}

export function servicesOf(list: readonly Item[]): string[] {
  const seen = new Map<string, string>();
  for (const i of list) if (!seen.has(i.account.service.toLowerCase())) seen.set(i.account.service.toLowerCase(), i.account.service);
  return [...seen.values()].sort((a, b) => a.localeCompare(b, undefined, { sensitivity: "base" }));
}

/** The items the search and the menus let through, before a chip. */
export function found(list: readonly Item[], search: string, project: string | null, service: string | null): Item[] {
  return list.filter((i) => matches(i, search) && inProject(i, project) && inService(i, service));
}

/** Each chip's count, after the search and the menus (they add together). */
export function counts(list: readonly Item[], search: string, project: string | null = null, service: string | null = null): Record<Chip, number> {
  const these = found(list, search, project, service);
  const out = {} as Record<Chip, number>;
  for (const chip of chips) out[chip] = these.filter((i) => inChip(i, chip)).length;
  return out;
}

export function sorted(list: readonly Item[], sort: Sort): Item[] {
  const byService = (a: Item, b: Item) => a.account.service.localeCompare(b.account.service, undefined, { numeric: true, sensitivity: "base" })
    || accountName(a.account).localeCompare(accountName(b.account), undefined, { numeric: true }) || (a.id < b.id ? -1 : a.id > b.id ? 1 : 0);
  const used = (i: Item) => (i.account.lastOp ? fromWireDate(i.account.lastOp.at).getTime() : -Infinity);
  return [...list].sort((a, b) => {
    if (sort === "service") return byService(a, b);
    const x = sort === "lastUsed" ? used(a) : a.account.monthlyCostUsd ?? -1;
    const y = sort === "lastUsed" ? used(b) : b.account.monthlyCostUsd ?? -1;
    return x === y ? byService(a, b) : y > x ? 1 : -1;
  });
}

/** The list: Needs you on top whatever the sort, while it has items, then the rest. */
export function groups(list: readonly Item[], search: string, chip: Chip, project: string | null, service: string | null, sort: Sort): Group[] {
  const shown = sorted(found(list, search, project, service).filter((i) => inChip(i, chip)), sort);
  const out: Group[] = [];
  const needs = shown.filter((i) => i.needs !== null);
  if (needs.length > 0) out.push({ id: "needsYou", title: "Needs you", items: needs });
  const rest = shown.filter((i) => i.needs === null);
  if (rest.length > 0) out.push({ id: "accounts", title: null, items: rest });
  return out;
}
