// Reports ▸ Store sizes (#6907, epic #6669): the last measurement of each declared store, the control plane's
// reports/storeSizes beside each machine's stores/list. The page joins them, not the control plane, so a machine that
// does not answer is named and keeps the rows that this browser read from it before. Sizes and counts only.
import type { StoreAtLimit, StoreSize, StoreSizes, StoreSizesQuery, StoreSizeState } from "../protocol/generated";
import { fromWireDate } from "../protocol/dates";
import { CallFailed } from "../wire/link";
import { bytesInWords } from "./artifacts";
import { lastUpdateAge } from "./lastUpdate";

/** The control plane's place among the sources; a machine's is its id. */
export const planeSource = "control-plane";
/** The most machines that one read asks, as the control plane's own fan (mostFanMachines). */
export const mostMachines = 50;
/** How long one place has to answer, with all its pages. */
export const patience = 10_000;
/** The most pages of one place: 1,000 stores. */
export const mostPages = 10;
/** The rows drawn at first, and the rows that Show More adds. */
export const firstRows = 10;
export const moreRows = 25;

const hour = 3_600_000;
/** How often a place measures its stores: each hour on a machine, every 6 h on the control plane (#6906). */
const cadence = { machine: hour, controlPlane: 6 * hour, client: 6 * hour } as const;
/** When a measurement is old: twice its cadence, as the place's own answer says it (`stale` in the schema). */
const staleAfter = { machine: 2 * cadence.machine, controlPlane: 2 * cadence.controlPlane, client: 2 * cadence.client } as const;
const nearPercent = 90;

/** A place's kept answer. */
export interface PlaceStores { stores: StoreSize[]; total: number }

/** A place asked: what it answered last, when this browser read that, and why the last ask failed. */
export interface Source { id: string; name: string; got?: PlaceStores | undefined; at?: number | undefined; problem?: string | undefined }

/** A place to ask, and why it is not asked. */
export interface Place { id: string; name: string; not?: string | undefined }

export type StoreStatus = "over" | "near" | "stale" | "unavailable" | "within";
export interface StoreRow { key: string; where: string; store: StoreSize; status: StoreStatus; old: boolean; measured: number | undefined }

const offlineWords = "It is offline.";
const oldWords = "Update it to show its stores.";
const lateWords = "It did not answer in time.";

/** The places to ask: the control plane, then each machine. One that cannot answer is not asked, and says why. */
export function placesOf(hosts: readonly { id: string; name: string; answers: boolean; has: boolean }[]): Place[] {
  return [{ id: planeSource, name: "Control plane" }, ...hosts.map((h, i): Place => ({ id: h.id, name: h.name,
    ...!h.answers ? { not: offlineWords } : !h.has ? { not: oldWords }
      : i >= mostMachines ? { not: `Only the first ${mostMachines} machines are asked.` } : {} }))];
}

/** Every page of a place. */
export async function readPlace(ask: (q: StoreSizesQuery) => Promise<StoreSizes>): Promise<PlaceStores> {
  const stores: StoreSize[] = [];
  let total = 0;
  let cursor: string | undefined;
  for (let page = 0; page < mostPages; page++) {
    const got = await ask(cursor === undefined ? {} : { cursor });
    stores.push(...got.stores);
    total = got.total;
    cursor = got.cursor;
    if (cursor === undefined) break;
  }
  return { stores, total };
}

/** The answer within `ms`; else it fails as late. */
export async function within<T>(answer: Promise<T>, ms: number = patience): Promise<T> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  const late = new Promise<never>((_, refuse) => { timer = setTimeout(() => refuse(new Late()), ms); });
  try {
    return await Promise.race([answer, late]);
  } finally {
    clearTimeout(timer);
  }
}

class Late extends Error {}

/** Why a place gave no answer, for a person. */
export function problemWords(error: unknown): string {
  if (error instanceof Late) return lateWords;
  if (error instanceof CallFailed) return error.code === -32601 ? oldWords : error.message;
  return "It can’t be reached.";
}

/** The rows of every place, with what each is now: a kept answer's measurement gets old on this page too. */
export function rowsOf(sources: readonly Source[], now: number): StoreRow[] {
  return sources.flatMap((s) => (s.got?.stores ?? []).map((store): StoreRow => {
    const measured = store.measuredAt === undefined ? undefined : fromWireDate(store.measuredAt).getTime();
    const old = store.state === "stale" || (measured !== undefined && now - measured > staleAfter[store.owner]);
    return { key: `${s.id} ${store.id}`, where: store.owner === "client" ? "Apps" : s.name, store, measured, old,
      status: statusOf(store, store.state, old) };
  }));
}

function statusOf(store: StoreSize, state: StoreSizeState, old: boolean): StoreStatus {
  if (state === "unavailable" || state === "over") return state;
  if (old) return "stale";
  // A store of one record is full when it has it: that is not near a limit.
  return Math.max(store.percentBytes ?? 0, store.maxRecords === 1 ? 0 : store.percentRecords ?? 0) >= nearPercent ? "near" : "within";
}

export const statusTitle: Record<StoreStatus, string> = {
  over: "Over limit", near: "Near limit", stale: "Stale", unavailable: "Unavailable", within: "Within limit",
};

/** What happens to a store over its limit, by its declaration (#7408). */
export const atLimitWords: Record<StoreAtLimit, string> = {
  refuse: "New writes are refused until it is within its limit.",
  write: "The next write removes what is past its limit.",
  hourly: "The hourly sweep removes what is past its limit.",
  daily: "The daily sweep removes what is past its limit.",
};

/** The words under a row's status: why it is unavailable, or what happens next when it is over its limit. */
export function statusNote(r: StoreRow): string | undefined {
  if (r.status === "over" && r.store.atLimit) return atLimitWords[r.store.atLimit];
  return r.store.why;
}

/**
 * When the next measurement is, of the places drawn: the latest of each place's measurements and its cadence,
 * the soonest of them. A read measures nothing (#7408), so this says when the numbers can next change.
 */
export function nextMeasuredAt(rows: readonly StoreRow[]): number | undefined {
  const last = new Map<string, number>();
  for (const r of rows) {
    if (r.measured === undefined || r.store.owner === "client") continue;
    const place = r.key.slice(0, r.key.indexOf(" "));
    last.set(place, Math.max(last.get(place) ?? 0, r.measured + cadence[r.store.owner]));
  }
  return last.size === 0 ? undefined : Math.min(...last.values());
}

/** "The next measurement is in 25 min." */
export function nextMeasuredWords(at: number | undefined, now: number): string | undefined {
  if (at === undefined) return undefined;
  const minutes = Math.ceil((at - now) / 60_000);
  if (minutes <= 1) return "The next measurement is due now.";
  return minutes < 60 ? `The next measurement is in ${minutes} min.` : `The next measurement is in about ${Math.round(minutes / 60)} h.`;
}

/** A row's status in words: one that is over its limit says too that its measurement is old. */
export function statusWords(r: StoreRow): string {
  return r.status === "over" && r.old ? "Over limit, stale" : statusTitle[r.status];
}

/** "64%", never 100% for one that is over and never 100% for one that is not there yet. */
export function percentWords(p: number): string {
  return `${p > 100 ? Math.ceil(p) : p > 0 && p < 1 ? "<1" : Math.floor(p)}%`;
}

const count = (n: number) => n.toLocaleString();
const records = (n: number) => `${count(n)} ${n === 1 ? "record" : "records"}`;

/** A limit of a store: its words, and how much of it is used when that is known. */
export interface Limit { kind: "bytes" | "records"; words: string; percent?: number | undefined }

/** Both limits, when the store has both. */
export function limitsOf(s: StoreSize): Limit[] {
  const per = s.limitPer ? ` for each ${s.limitPer}` : "";
  return [
    ...s.maxBytes === undefined ? [] : [{ kind: "bytes", words: bytesInWords(s.maxBytes) + per, percent: s.percentBytes } satisfies Limit],
    ...s.maxRecords === undefined ? [] : [{ kind: "records", words: records(s.maxRecords) + per, percent: s.percentRecords } satisfies Limit],
  ];
}

export const limitWords = (l: Limit) => l.percent === undefined ? `Limit ${l.words}` : `${percentWords(l.percent)} · ${l.words}`;
/** What a limit's percent is of, when it is not the whole store. */
export const limitTitle = (s: StoreSize) => s.limitPer ? `The percent is of the largest ${s.limitPer}.` : undefined;
/** What the Limit used column sorts by. */
export const mostUsed = (s: StoreSize) => s.percentBytes === undefined && s.percentRecords === undefined ? undefined
  : Math.max(s.percentBytes ?? 0, s.percentRecords ?? 0);

export const none = "—";
export const sizeWords = (n: number | undefined) => n === undefined ? none : bytesInWords(n);
export const recordsWords = (n: number | undefined) => n === undefined ? none : count(n);
export const measuredWords = (r: StoreRow, now: number) => r.measured === undefined ? "Not measured" : lastUpdateAge(r.measured, now);

/** The three cards. A store with a size or a count is measured, also when its limit cannot be compared with it. */
export function cardsOf(sources: readonly Source[], rows: readonly StoreRow[]) {
  const total = sources.reduce((n, s) => n + (s.got?.total ?? 0), 0);
  const measured = rows.filter((r) => isMeasured(r.store));
  const of = (status: StoreStatus) => rows.filter((r) => r.status === status).length;
  const over = rows.filter((r) => r.status === "over");
  const bytesOver = over.filter((r) => (r.store.percentBytes ?? 0) > 100).length;
  const recordsOver = over.filter((r) => (r.store.percentRecords ?? 0) > 100).length;
  return {
    measured: `${count(measured.length)} of ${count(total)}`,
    unmeasured: [`${count(of("unavailable"))} unavailable`, `${count(rows.filter((r) => r.old).length)} stale`].join(" · "),
    bytes: bytesInWords(measured.reduce((n, r) => n + (r.store.bytes ?? 0), 0)),
    over: over.length,
    overWords: over.length === 1 ? "1 store" : `${count(over.length)} stores`,
    overKinds: over.length === 0 ? "No store is over a limit" : `${bytesOver} byte ${bytesOver === 1 ? "limit" : "limits"} · ${recordsOver} record ${recordsOver === 1 ? "limit" : "limits"}`,
    any: measured.length > 0,
  };
}

const isMeasured = (s: StoreSize) => s.bytes !== undefined || s.records !== undefined;

/** The filter: every store, or those of one status, or of one place. */
export type StoreFilter = string;
export const everyStore: StoreFilter = "all";
const statuses: readonly StoreStatus[] = ["over", "near", "stale", "unavailable"];

/** A filter's rows: a stale one is each whose measurement is old, also when it is over its limit. */
export function filtered(rows: readonly StoreRow[], filter: StoreFilter): StoreRow[] {
  if (filter.startsWith("in:")) return rows.filter((r) => filter === `in:${r.where}`);
  if (filter === "stale") return rows.filter((r) => r.old);
  return filter === everyStore ? [...rows] : rows.filter((r) => r.status === filter);
}

/** The filters, each with how many stores it keeps. */
export function filtersOf(rows: readonly StoreRow[]): (readonly [StoreFilter, string])[] {
  const wheres = [...new Set(rows.map((r) => r.where))];
  const option = (f: StoreFilter, title: string) => [f, `${title} (${count(filtered(rows, f).length)})`] as const;
  return [option(everyStore, "All"), ...statuses.map((s) => option(s, statusTitle[s])), ...wheres.map((w) => option(`in:${w}`, w))];
}

/** The oldest read of the places drawn: the page's "Last update". */
export function readAtOf(sources: readonly Source[]): number | undefined {
  const times = sources.flatMap((s) => s.got && s.at !== undefined ? [s.at] : []);
  return times.length > 0 ? Math.min(...times) : undefined;
}

/** A line for each place that did not answer: what is drawn of it, and its age. */
export function problemLines(sources: readonly Source[], now: number): string[] {
  return sources.flatMap((s) => !s.problem ? [] : [`${s.name}: ${s.problem} ${s.got && s.at !== undefined
    ? `Its measurements are from the last read, ${lastUpdateAge(s.at, now)}.` : "Its stores are not shown."}`]);
}

export const failedWords = "Some measurements could not be read.";
export const refreshingWords = "Reloading the last measurements… Showing cached results.";
export const staleWords = "Some measurements are stale or unavailable.";
export const emptyTitle = "No store measurements yet";
export const emptyWords = "When the first measurements are available, each store’s size, limit, and last checked time will appear here.";
export const averageNote = "Average record size uses the last measured byte and record counts.";
export const readOnlyNote = "Measurements are read-only. Stored content and secrets are never shown.";
export const memberNote = "Only an admin sees the control plane’s stores.";
export const showingWords = (shown: number, all: number) => `Showing ${count(shown)} of ${count(all)} ${all === 1 ? "store" : "stores"}`;
