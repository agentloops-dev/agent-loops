// Work's client cache on the web page (#6356, epic #6348): the Work records this device read, in
// IndexedDB, so a Work page seen before draws at once, with no call, also when its machine does not
// answer. It is the Mac's WorkCache (Packages/AgentsKit, WorkCache.swift, #6352, #6573), with the
// model of #6358's decision:
// - Items by id: each issue or pull request once, by its node (kind and ref), with the forge's
//   updatedAt. A copy as new or newer replaces it, so a change shows in every answer that holds it.
// - Query answers as ids: each query's answer per machine, as the refs it returned in the server's
//   order, page by page, each page with its cursor and when it was read, with the total, the forges
//   and the errors. A refresh replaces page 1 and keeps the pages read before, marked stale (#6636):
//   the next page is asked by the last fresh page's cursor, and each fresh page takes out of the stale
//   ones what it covers.
// - A page whose answer is of its own shape (a plan, Releases, the boards, Timeline, Deploys, Next,
//   the lineage figures) keeps it whole, by the page and what it asked, with when it was read.
// Nothing here syncs or sorts: the caller asks the forge and gives the answer to keepPage. The store
// is `work` in the database `agents` (wire/keys.ts), so a forget, which deletes it, drops it too.
import type { ForgeCapabilities, ForgeItem, ForgeItemDetail, ForgeList, ForgeListError, LineageNode } from "../protocol/generated";
import { openAgentsDatabase } from "../wire/keys";
import { fromWireDate } from "../protocol/dates";
import { cacheAgeLimit, type CacheStats, type CacheStore } from "./cacheStore";

/** What a list asks the forge (#6358): the parts that change the server's answer. The words in the filter field, the
 * label, the assignee and the project narrow the kept answer on the page. */
export interface WorkQuery {
  kind: "issue" | "pr";
  state: string;
  board?: string | undefined;
  /** The server's order (#6538). */
  sort: "updated" | "priority";
}

/** One kept page of a machine's answer: its items' keys, its cursor to the next page (none at the end), when it was read.
 * A stale page was read before the last refresh (#6636): it draws, but its cursor is never asked. */
export interface WorkPage { refs: string[]; cursor?: string | undefined; read: number; stale?: boolean | undefined }

/** A machine's kept answer to a query, its items in the server's order. */
export interface WorkHostAnswer {
  host: string;
  /** The kept pages' items, in order; an item that two pages hold is drawn once, where it was first. */
  items: ForgeItem[];
  pages: WorkPage[];
  total?: number | undefined;
  forges?: ForgeCapabilities[] | undefined;
  errors?: ForgeListError[] | undefined;
  /** When the machine last failed to answer, after its last page was read. */
  failed?: number | undefined;
  /** When page 1 was read: the list's "Last update" (#5139). */
  read?: number | undefined;
  /** The cursor to the page after the fresh ones; none when they reach the end. A stale page's is never asked. */
  cursor?: string | undefined;
  /** How many pages were read since the last refresh: the next page is kept after them (#6636). */
  fresh: number;
}

export interface WorkAnswer {
  hosts: WorkHostAnswer[];
  /** The oldest page 1 of the machines: what the list says it shows. */
  read?: number | undefined;
}

/** An item's kept page: the machine that read it, its answer, and when. */
export interface WorkDetail { host: string; got: ForgeItemDetail; read: number }

/** The cache's bounds (#6352; the web's 50 MB, epic #6348; the age, #6976): over `bytes`, bodies go first, then the pages'
 * answers and the lists' answers, each the least recently drawn first, with the items no other answer holds. */
export interface WorkBounds {
  itemBytes: number;
  bodyBytes: number;
  comments: number;
  /** The largest record. A page's answer or an item's page over it is not kept, so the page asks again next time; a list's
   * answer loses its last pages. */
  recordBytes: number;
  bytes: number;
  /** A record goes this long (ms) after it was read from the forge, at most 90 days; drawing it does not make it newer. */
  age: number;
}
export const webBounds: WorkBounds = { itemBytes: 4 * 1024, bodyBytes: 64 * 1024, comments: 50, recordBytes: 4 * 1024 * 1024,
  bytes: 50 * 1024 * 1024, age: cacheAgeLimit };
const sweepEvery = 60 * 60 * 1000;

/** Where the cache lives: IndexedDB on the page, memory in the tests. */
export interface WorkStore {
  /** The values of `keys`, in their order; undefined for a missing one. */
  get(keys: readonly string[]): Promise<unknown[]>;
  /** Puts and deletes, and changes the stored index, in one transaction: two tabs do not undo each other's index. */
  write(put: ReadonlyArray<[string, unknown]>, drop: readonly string[], index: (stored: IndexRows) => IndexRows): Promise<void>;
  /** Deletes the records that the stored index does not name: nothing else would age them out. */
  dropUnindexed(): Promise<void>;
}

function request<T>(r: IDBRequest<T>): Promise<T> {
  return new Promise((resolve, reject) => {
    r.onsuccess = () => resolve(r.result);
    r.onerror = () => reject(r.error);
  });
}

/** The store `work` in the database `agents`. */
// store-bounds: kind=cache; size=50MiB; age=90d; action=evict; record=4MiB; applied=on write, on open and hourly
export class IndexedWorkStore implements WorkStore {
  private async run<T>(mode: IDBTransactionMode, act: (store: IDBObjectStore) => Promise<T> | T): Promise<T> {
    const db = await openAgentsDatabase();
    try {
      const tx = db.transaction("work", mode);
      const done = new Promise<void>((resolve, reject) => {
        tx.oncomplete = () => resolve();
        tx.onerror = () => reject(tx.error);
        tx.onabort = () => reject(tx.error);
      });
      const got = await act(tx.objectStore("work"));
      await done;
      return got;
    } finally {
      db.close();
    }
  }

  get(keys: readonly string[]): Promise<unknown[]> {
    return this.run("readonly", (store) => Promise.all(keys.map((k) => request(store.get(k)))));
  }

  write(put: ReadonlyArray<[string, unknown]>, drop: readonly string[], index: (stored: IndexRows) => IndexRows): Promise<void> {
    return this.run("readwrite", async (store) => {
      for (const key of drop) store.delete(key);
      for (const [key, value] of put) store.put(value, key);
      const stored = (await request(store.get(indexKey))) as IndexRows | undefined;
      store.put(index(stored ?? {}), indexKey);
    });
  }

  dropUnindexed(): Promise<void> {
    return this.run("readwrite", async (store) => {
      const stored = ((await request(store.get(indexKey))) as IndexRows | undefined) ?? {};
      for (const key of await request(store.getAllKeys())) {
        if (key !== indexKey && !Object.hasOwn(stored, String(key))) store.delete(key);
      }
    });
  }
}

/** For the tests: the same contract, in memory. */
export class MemoryWorkStore implements WorkStore {
  readonly held = new Map<string, unknown>();
  async get(keys: readonly string[]): Promise<unknown[]> {
    return keys.map((k) => structuredClone(this.held.get(k)));
  }
  async write(put: ReadonlyArray<[string, unknown]>, drop: readonly string[], index: (stored: IndexRows) => IndexRows): Promise<void> {
    for (const key of drop) this.held.delete(key);
    for (const [key, value] of put) this.held.set(key, structuredClone(value));
    this.held.set(indexKey, structuredClone(index(structuredClone((this.held.get(indexKey) as IndexRows | undefined) ?? {}))));
  }
  async dropUnindexed(): Promise<void> {
    const stored = (this.held.get(indexKey) as IndexRows | undefined) ?? {};
    for (const key of [...this.held.keys()]) if (key !== indexKey && !Object.hasOwn(stored, key)) this.held.delete(key);
  }
}

/** A machine's answer as the store keeps it; the items are their own records. */
interface KeptAnswer {
  pages: WorkPage[];
  total?: number | undefined;
  forges?: ForgeCapabilities[] | undefined;
  errors?: ForgeListError[] | undefined;
  failed?: number | undefined;
}

/** What the cache knows of each record without reading it, kept as one record: its size, when it was last drawn, and when it
 * was read from the forge (an answer's oldest page; none in a row from before #6976, which the first sweep sets). */
interface IndexRow { bytes: number; used: number; read?: number | undefined }
type IndexRows = Record<string, IndexRow>;
/** A record to write: its key, its value and when it was read from the forge. */
type Put = readonly [key: string, value: unknown, read: number];

const indexKey = "@index";
const itemKey = (node: LineageNode) => `item ${node.kind} ${node.ref}`;
const detailKey = (node: LineageNode) => `detail ${node.kind} ${node.ref}`;
const pageKey = (page: string, asked: string) => `page ${page} ${asked}`;

/** The query's key: its JSON with sorted keys and no empty parts, so equal queries have one key. */
export function queryKey(q: WorkQuery): string {
  const parts: Record<string, string> = { kind: q.kind, sort: q.sort, state: q.state };
  if (q.board) parts.board = q.board;
  return JSON.stringify(Object.fromEntries(Object.entries(parts).sort(([a], [b]) => a.localeCompare(b))));
}
const answerKey = (q: WorkQuery, host: string) => `answer ${host} ${queryKey(q)}`;

/** The pages before the first stale one: read since the last refresh. */
const freshCount = (pages: readonly WorkPage[]) => {
  const i = pages.findIndex((p) => p.stale);
  return i < 0 ? pages.length : i;
};
const freshCursor = (pages: readonly WorkPage[]) => {
  const fresh = freshCount(pages);
  return fresh > 0 ? pages[fresh - 1]!.cursor : undefined;
};

/** When an answer's oldest page was read; with no page, when the machine failed to answer. */
const oldestRead = (kept: KeptAnswer) => kept.pages.length > 0 ? Math.min(...kept.pages.map((p) => p.read)) : kept.failed ?? 0;

/** The answer less what was read before `cutoff` (#6976); undefined when nothing is left. The pages read since a refresh
 * follow one another, so those after one that goes go too. */
function since(kept: KeptAnswer, cutoff: number): KeptAnswer | undefined {
  if (oldestRead(kept) >= cutoff && (kept.failed === undefined || kept.failed >= cutoff)) return kept;
  const fresh = kept.pages.slice(0, freshCount(kept.pages));
  const old = fresh.findIndex((p) => p.read < cutoff);
  const pages = [...(old < 0 ? fresh : fresh.slice(0, old)), ...kept.pages.slice(fresh.length).filter((p) => p.read >= cutoff)];
  const failed = kept.failed !== undefined && kept.failed >= cutoff ? { failed: kept.failed, errors: kept.errors } : {};
  if (pages.length > 0) return { pages, total: kept.total, forges: kept.forges, ...(kept.failed === undefined ? { errors: kept.errors } : failed) };
  return failed.failed === undefined ? undefined : { pages, ...failed };
}

const rank = (item: ForgeItem) => ({ high: 0, medium: 1, low: 2 } as Record<string, number>)[item.priority?.toLowerCase() ?? ""] ?? 3;

/** `a` comes strictly before `b` in the server's order: by priority first for "priority" (high, medium, low, none), then the
 * last updated first, as the daemon merges the forges' pages (#6538). A tie is not before: it may be on the next page. */
export function before(a: ForgeItem, b: ForgeItem, sort: WorkQuery["sort"]): boolean {
  if (sort === "priority" && rank(a) !== rank(b)) return rank(a) < rank(b);
  return a.updatedAt > b.updatedAt;
}

const encoder = new TextEncoder();
const utf8 = (text: string) => encoder.encode(text).length;

/** The longest prefix of `text` of at most `bytes` in UTF-8, cut at a character. */
export function prefixBytes(text: string, bytes: number): string {
  let used = 0;
  let end = 0;
  for (const char of text) {
    used += utf8(char);
    if (used > bytes) break;
    end += char.length;
  }
  return text.slice(0, end);
}

/**
 * The cache: an index and the items drawn in memory, the records in the store. Every failure is swallowed: a cache
 * that cannot be read or written is no cache, and the page asks the forge as it would with none.
 */
export class WorkCache implements CacheStore {
  private index = new Map<string, IndexRow>();
  private loaded: Promise<void> | undefined;
  /** Items read once, by key: a draw after the first reads nothing. The store stays the record. */
  private memo = new Map<string, ForgeItem>();
  /** The items an optimistic write changed, until it ends: a copy as old as theirs does not replace them. */
  private edited = new Set<string>();
  private queue: Promise<void> = Promise.resolve();
  /** The changes, one after another, as the Mac's actor runs them: two pages of one answer do not undo each other. */
  private turn: Promise<unknown> = Promise.resolve();
  /** Off while the page is not signed in: a write then would make the database again. */
  writing = false;
  private timer: ReturnType<typeof setInterval> | undefined;
  readonly bounds: WorkBounds;

  constructor(readonly store: WorkStore = new IndexedWorkStore(), bounds: WorkBounds = webBounds,
    private readonly now: () => number = () => Date.now()) {
    this.bounds = { ...bounds, age: Math.min(bounds.age, cacheAgeLimit) };
  }

  /** The page is signed in: it writes from now, and sweeps at once and each hour. */
  open(): void {
    if (this.writing) return;
    this.writing = true;
    void this.sweep();
    this.timer ??= setInterval(() => void this.sweep(), sweepEvery);
  }

  private cutoff(): number {
    return this.now() - this.bounds.age;
  }

  private serial<T>(act: () => Promise<T>): Promise<T> {
    const run = this.turn.then(act, act);
    this.turn = run.catch(() => undefined);
    return run;
  }

  private ready(): Promise<void> {
    this.loaded ??= this.store.get([indexKey]).then(([rows]) => {
      this.index = new Map(Object.entries((rows as IndexRows | undefined) ?? {}));
    }, () => undefined);
    return this.loaded;
  }

  private async read<T>(keys: readonly string[]): Promise<(T | undefined)[]> {
    await this.ready();
    await this.queue;
    try {
      const got = (await this.store.get(keys)) as (T | undefined)[];
      return got.map((value, i) => (value !== undefined && this.within(keys[i]!, value) ? value : undefined));
    } catch {
      return keys.map(() => undefined);
    }
  }

  private most(key: string): number {
    return key.startsWith("item ") ? this.bounds.itemBytes : this.bounds.recordBytes;
  }

  /** Whether the index alone says that a record is within its largest size: a row from before #6976 counts UTF-16 units, of
   * which UTF-8 takes at most three bytes each. */
  private surelyWithin(key: string): boolean {
    const row = this.index.get(key);
    return row !== undefined && row.bytes * 3 <= this.most(key);
  }

  /** Whether a kept record is within its largest size: an older build kept larger ones (#7253). */
  private within(key: string, value: unknown): boolean {
    return this.surelyWithin(key) || utf8(JSON.stringify(value)) <= this.most(key);
  }

  /** Removes what an older build kept over the largest record. */
  private async dropOver(): Promise<void> {
    // An item's size was in UTF-8 in every build, so its row says when it is over.
    const items = [...this.index].filter(([key, row]) => key.startsWith("item ") && row.bytes > this.bounds.itemBytes).map(([key]) => key);
    const look = [...this.index.keys()].filter((key) => !key.startsWith("item ") && !this.surelyWithin(key));
    const records = look.length > 0 ? await this.read(look) : [];
    const drop = [...items, ...look.filter((_, i) => records[i] === undefined)];
    if (drop.length === 0) return;
    for (const key of drop) {
      this.memo.delete(key);
      this.edited.delete(key);
    }
    await this.save([], drop);
  }

  /** Writes records and their index rows, one write after another; `touch` only changes the rows of kept records. */
  private save(put: readonly Put[], drop: readonly string[] = [], touch: ReadonlyMap<string, Partial<IndexRow>> = new Map()): Promise<void> {
    if (!this.writing) return this.queue;
    const at = this.now();
    const rows = put.map(([key, value, read]): [string, IndexRow] => [key, { bytes: utf8(JSON.stringify(value)), used: at, read }]);
    const apply = (index: Map<string, IndexRow>) => {
      for (const key of drop) index.delete(key);
      for (const [key, row] of rows) index.set(key, row);
      for (const [key, change] of touch) {
        const row = index.get(key);
        if (row) index.set(key, { ...row, ...change });
      }
      return index;
    };
    apply(this.index);
    const records = put.map(([key, value]): [string, unknown] => [key, value]);
    this.queue = this.queue.then(() => (this.writing
      ? this.store.write(records, drop, (stored) => Object.fromEntries(apply(new Map(Object.entries(stored)))))
      : undefined)).catch(() => undefined);
    return this.queue;
  }

  /** The records were drawn now. */
  private drawn(keys: readonly string[]): void {
    const used = this.now();
    void this.save([], [], new Map(keys.map((k) => [k, { used }])));
  }

  /** Reads the keys' items that are not in memory yet. */
  private async load(keys: readonly string[]): Promise<void> {
    const missing = [...new Set(keys.filter((k) => !this.memo.has(k)))];
    if (missing.length === 0) return;
    const got = await this.read<ForgeItem>(missing);
    missing.forEach((key, i) => {
      const item = got[i];
      if (item && !this.memo.has(key)) this.memo.set(key, item);
    });
  }

  // Answers

  /** The kept answer to a query, with its items; undefined when it was never answered on this device. */
  async answer(q: WorkQuery, hosts: readonly string[]): Promise<WorkAnswer | undefined> {
    const keys = hosts.map((h) => answerKey(q, h));
    const kept = await this.read<KeptAnswer>(keys);
    const cutoff = this.cutoff();
    const found = hosts.flatMap((host, i) => {
      const fresh = kept[i] && since(kept[i]!, cutoff);
      return fresh ? [{ host, kept: fresh }] : [];
    });
    if (found.length === 0) return undefined;
    await this.load(found.flatMap((f) => f.kept.pages.flatMap((p) => p.refs)));
    this.drawn(found.map((f) => answerKey(q, f.host)));
    const answers = found.map(({ host, kept }): WorkHostAnswer => {
      const seen = new Set<string>();
      const items = kept.pages.flatMap((p) => p.refs).flatMap((ref) => {
        if (seen.has(ref)) return [];
        seen.add(ref);
        const item = this.memo.get(ref);
        return item ? [item] : [];
      });
      return { host, items, pages: kept.pages, total: kept.total, forges: kept.forges, errors: kept.errors, failed: kept.failed,
        read: kept.pages[0]?.read, cursor: freshCursor(kept.pages), fresh: freshCount(kept.pages) };
    });
    const reads = answers.flatMap((a) => (a.read === undefined ? [] : [a.read]));
    return { hosts: answers, read: reads.length > 0 ? Math.min(...reads) : undefined };
  }

  /** Keeps one page of a machine's answer: page 0 replaces the first page and marks the pages read before stale; the page
   * after the last fresh one is added after it; any other fresh page is replaced. The stale pages lose what the fresh ones
   * cover (#6636). Its items replace older copies. */
  keepPage(q: WorkQuery, host: string, page: number, list: ForgeList): Promise<void> {
    return this.serial(async () => {
      const key = answerKey(q, host);
      const [kept] = await this.read<KeptAnswer>([key]);
      const had = kept?.pages ?? [];
      const fresh = freshCount(had);
      if (page < 0 || page > fresh) return;
      const at = this.now();
      // The forges' read time, not this call's (#6642): the daemon can answer from a copy up to a minute old.
      const readAt = list.readAt === undefined ? at : Math.min(at, fromWireDate(list.readAt).getTime());
      const { put, drop } = await this.itemsToPut(list.items, readAt);
      const read: WorkPage = { refs: list.items.map((i) => itemKey(i.node)).filter((k) => this.memo.has(k)), cursor: list.cursor, read: readAt };
      let reads = had.slice(0, fresh);
      let stale = had.slice(fresh);
      if (page === 0) {
        // A refresh: every page read before is stale now, the old page 1 too (an item of it may be on page 2 now).
        stale = [...reads.map((p) => ({ ...p, stale: true })), ...stale];
        reads = [read];
      } else if (page === reads.length) reads.push(read);
      else reads[page] = read;
      const pages = [...reads, ...await this.covered(stale, reads, q.sort)];
      const answer: KeptAnswer = { pages, total: list.total ?? kept?.total, forges: list.forges ?? kept?.forges, errors: list.errors };
      await this.saveAnswer(key, answer, put, drop);
      await this.trim();
    });
  }

  /** Writes an answer, less its last pages when it is over `bounds.recordBytes`; one with nothing left goes. */
  private saveAnswer(key: string, answer: KeptAnswer, put: readonly Put[] = [], drop: readonly string[] = []): Promise<void> {
    let fit = answer;
    const over = () => utf8(JSON.stringify(fit)) > this.bounds.recordBytes;
    while (fit.pages.length > 0 && over()) fit = { ...fit, pages: fit.pages.slice(0, -1) };
    if (over() || (fit.pages.length === 0 && fit.failed === undefined)) return this.save(put, [...drop, key]);
    return this.save([...put, [key, fit, oldestRead(fit)]], drop);
  }

  /** The stale pages less what the fresh pages cover (#6636): the items they hold, and the items the server's order puts
   * before the last fresh item, which left the list. None when the fresh pages reach the end. An empty page goes. */
  private async covered(stale: readonly WorkPage[], fresh: readonly WorkPage[], sort: WorkQuery["sort"]): Promise<WorkPage[]> {
    const end = fresh.at(-1);
    if (stale.length === 0 || !end?.cursor) return [];
    const held = new Set(fresh.flatMap((p) => p.refs));
    await this.load(stale.flatMap((p) => p.refs));
    const lastRef = [...fresh].reverse().flatMap((p) => p.refs.slice(-1)).find((r) => this.memo.has(r));
    const last = lastRef ? this.memo.get(lastRef) : undefined;
    return stale.flatMap((page) => {
      const refs = page.refs.filter((ref) => {
        const item = this.memo.get(ref);
        return !held.has(ref) && item !== undefined && (!last || !before(item, last, sort));
      });
      return refs.length > 0 ? [{ ...page, refs }] : [];
    });
  }

  /** The machine did not answer the query: its kept pages stay, with the errors and when it failed. */
  keepFailure(q: WorkQuery, host: string, errors: ForgeListError[]): Promise<void> {
    return this.serial(async () => {
      const key = answerKey(q, host);
      const [kept] = await this.read<KeptAnswer>([key]);
      await this.saveAnswer(key, { ...(kept ?? { pages: [] }), errors, failed: this.now() });
      await this.trim();
    });
  }

  /** The machine is not asked the query, as another machine reads what it reads (#6641): its kept answer goes, so its
   * rows do not draw as old ones. */
  forgetAnswer(q: WorkQuery, host: string): Promise<void> {
    return this.serial(async () => {
      await this.ready();
      await this.save([], [answerKey(q, host)]);
    });
  }

  // Items

  /** Items read or written outside a list (a write's answer, #6351, or an item's page): each replaces an older copy. */
  keepItems(items: readonly ForgeItem[]): Promise<void> {
    return this.serial(async () => {
      const { put, drop } = await this.itemsToPut(items, this.now());
      await this.save(put, drop);
      await this.trim();
    });
  }

  /** The items to write: each replaces a kept one only when as new or newer, so a late answer does not undo a newer one;
   * one over `bounds.itemBytes` after trimming is not kept, and its earlier copy is in `drop`, so no answer read now draws
   * the item as it was (#7198). */
  private async itemsToPut(items: readonly ForgeItem[], read: number): Promise<{ put: Put[]; drop: string[] }> {
    await this.load(items.map((i) => itemKey(i.node)));
    const put = new Map<string, Put>();
    const drop = new Set<string>();
    for (const item of items) {
      const key = itemKey(item.node);
      const had = this.memo.get(key);
      if (had && (had.updatedAt > item.updatedAt || (this.edited.has(key) && had.updatedAt === item.updatedAt))) continue;
      const fit = this.fitted(item);
      this.edited.delete(key);
      if (!fit) {
        this.memo.delete(key);
        put.delete(key);
        drop.add(key);
        continue;
      }
      drop.delete(key);
      this.memo.set(key, fit);
      // An answer from the daemon's copy can be older than the kept item's own read.
      put.set(key, [key, fit, Math.max(read, this.readOf(key) ?? 0)]);
    }
    return { put: [...put.values()], drop: [...drop] };
  }

  /** The item cut to `bounds.itemBytes`: a long title, then labels and assignees beyond the first. */
  private fitted(item: ForgeItem): ForgeItem | undefined {
    let fit = item;
    for (let step = 0; step < 4; step++) {
      if (utf8(JSON.stringify(fit)) <= this.bounds.itemBytes) return fit;
      if (step === 0) fit = { ...fit, title: fit.title.slice(0, 256) };
      else if (step === 1) fit = { ...fit, labels: fit.labels.slice(0, 10) };
      else if (step === 2) fit = { ...fit, assignees: fit.assignees.slice(0, 5) };
    }
    return undefined;
  }

  /** Changes one kept item at once (an optimistic write, #6354). Its updatedAt stays, so the forge's newer copy replaces
   * it; a copy as old (a list read while the write runs) does not, until `settle`. */
  edit(node: LineageNode, change: (item: ForgeItem) => ForgeItem): Promise<void> {
    return this.serial(async () => {
      const key = itemKey(node);
      await this.load([key]);
      const had = this.memo.get(key);
      if (!had) return;
      const fit = this.fitted({ ...change(had), updatedAt: had.updatedAt });
      if (!fit) return;
      this.memo.set(key, fit);
      this.edited.add(key);
      // Not a read from the forge: the item ages from its last one.
      await this.save([[key, fit, this.readOf(key) ?? this.now()]]);
    });
  }

  /** The write of an edited item ended: a copy as old as the kept one replaces it again. */
  settle(node: LineageNode): void {
    this.edited.delete(itemKey(node));
  }

  /** One kept item; undefined when this device never read it. */
  async item(node: LineageNode): Promise<ForgeItem | undefined> {
    const key = itemKey(node);
    await this.load([key]);
    return (this.readOf(key) ?? Infinity) < this.cutoff() ? undefined : this.memo.get(key);
  }

  /** When a record was read from the forge, as far as the index says; an item's row from before #6976 has when it was written. */
  private readOf(key: string): number | undefined {
    const row = this.index.get(key);
    return row?.read ?? (key.startsWith("item ") ? row?.used : undefined);
  }

  // Details

  /** An item's kept page, with the newest kept copy of the item; undefined when it was never read here. */
  async detail(node: LineageNode): Promise<WorkDetail | undefined> {
    const [found] = await this.read<WorkDetail>([detailKey(node)]);
    if (!found || found.read < this.cutoff()) return undefined;
    this.drawn([detailKey(node)]);
    const item = await this.item(node);
    // The kept item is never older than the page's: an optimistic change keeps its updatedAt.
    return item && item.updatedAt >= found.got.item.updatedAt ? { ...found, got: { ...found.got, item } } : found;
  }

  keepDetail(host: string, got: ForgeItemDetail): Promise<void> {
    return this.serial(async () => {
      const at = this.now();
      const { put, drop } = await this.itemsToPut([got.item], at);
      const body = utf8(got.body) > this.bounds.bodyBytes ? prefixBytes(got.body, this.bounds.bodyBytes) : got.body;
      const kept: WorkDetail = { host, got: { ...got, body, comments: got.comments.slice(0, this.bounds.comments) }, read: at };
      const key = detailKey(got.item.node);
      if (utf8(JSON.stringify(kept)) > this.bounds.recordBytes) await this.save(put, [...drop, key]);
      else await this.save([...put, [key, kept, at]], drop);
      await this.trim();
    });
  }

  // Pages

  /** A page's kept answer of its own shape (#6573), with when it was read; undefined when it was never answered here. */
  async kept<T>(page: string, asked: string): Promise<{ value: T; at: number } | undefined> {
    const key = pageKey(page, asked);
    const [found] = await this.read<{ value: T; at: number }>([key]);
    if (!found || found.at < this.cutoff()) return undefined;
    this.drawn([key]);
    return found;
  }

  /** Keeps a page's answer for what it asked, read now, in place of the last one; one over `bounds.recordBytes` is not kept,
   * so the page asks again next time. */
  keep(page: string, asked: string, value: unknown): Promise<void> {
    return this.serial(async () => {
      const key = pageKey(page, asked);
      const record = { value, at: this.now() };
      if (utf8(JSON.stringify(record)) > this.bounds.recordBytes) return this.save([], [key]);
      await this.save([[key, record, record.at]]);
      await this.trim();
    });
  }

  // Bounds

  /** A kept record's size, with its row in the index: its key and its numbers. */
  private size(key: string): number {
    const row = this.index.get(key);
    return row ? row.bytes + utf8(key) + 64 : 0;
  }

  /** The kept records' size, with the index: what `bounds.bytes` bounds. */
  bytes(): number {
    let total = 0;
    for (const key of this.index.keys()) total += this.size(key);
    return total;
  }

  async stats(): Promise<CacheStats> {
    await this.ready();
    let oldest: number | undefined;
    for (const [key, row] of this.index) oldest = Math.min(oldest ?? Infinity, this.readOf(key) ?? row.used);
    return { count: this.index.size, bytes: this.bytes(), oldest };
  }

  /** At sign-in and each hour: what another tab wrote is read, what the index does not name goes, then the bounds. */
  sweep(): Promise<void> {
    return this.serial(async () => {
      await this.ready();
      await this.queue;
      if (!this.writing) return;
      try {
        await this.store.dropUnindexed();
        const [rows] = await this.store.get([indexKey]);
        if (!this.writing) return;
        this.index = new Map(Object.entries((rows as IndexRows | undefined) ?? {}));
      } catch {
        // The next sweep tries again.
      }
      for (const key of this.memo.keys()) if (!this.index.has(key)) this.memo.delete(key);
      await this.dropOver();
      await this.trim();
    });
  }

  /** Removes what was read longer than `bounds.age` ago (#6976). It reads only the answers that lose something, and once
   * each record of a row from before #6976. */
  private async ageOut(): Promise<void> {
    const cutoff = this.cutoff();
    const due = [...this.index.keys()].filter((key) => (this.readOf(key) ?? -Infinity) < cutoff);
    if (due.length === 0) return;
    const look = due.filter((key) => key.startsWith("answer ") || this.readOf(key) === undefined);
    const looked = new Set(look);
    const drop = due.filter((key) => !looked.has(key));
    const put: Put[] = [];
    const touch = new Map<string, Partial<IndexRow>>();
    const records = await this.read<KeptAnswer & { read?: number; at?: number }>(look);
    look.forEach((key, i) => {
      const record = records[i];
      if (record && key.startsWith("answer ")) {
        const kept = since(record, cutoff);
        if (!kept) drop.push(key);
        else if (kept === record) touch.set(key, { read: oldestRead(kept) });
        else {
          put.push([key, kept, oldestRead(kept)]);
          touch.set(key, { used: this.index.get(key)?.used ?? 0 });
        }
      } else {
        const read = record?.read ?? record?.at ?? 0;
        if (read < cutoff) drop.push(key);
        else touch.set(key, { read });
      }
    });
    for (const key of drop) {
      this.memo.delete(key);
      this.edited.delete(key);
    }
    await this.save(put, drop, touch);
  }

  /** Ages records out, then brings the cache under `bounds.bytes`, to nine tenths of it so the next page does not trim again:
   * bodies first, the least recently opened first; then the pages' answers; then the lists' answers, with the items no kept
   * answer holds. */
  private async trim(): Promise<void> {
    if (!this.writing) return;
    await this.ageOut();
    let total = this.bytes();
    if (total <= this.bounds.bytes) return;
    const target = this.bounds.bytes / 10 * 9;
    const oldest = (prefix: string) => [...this.index].filter(([k]) => k.startsWith(prefix)).sort(([, a], [, b]) => a.used - b.used);
    const drop: string[] = [];
    for (const prefix of ["detail ", "page "]) {
      for (const [key] of oldest(prefix)) {
        if (total <= target) break;
        drop.push(key);
        total -= this.size(key);
      }
    }
    // Then the answers, the least recently drawn first, each with the items that no kept answer or page holds after it.
    const answers = oldest("answer ").map(([k]) => k);
    const pages = await this.read<KeptAnswer>(answers);
    const refsOf = new Map(answers.map((k, i) => [k, pages[i]?.pages.flatMap((p) => p.refs) ?? []]));
    const unheld = () => {
      const held = new Set([...refsOf.values()].flat());
      for (const key of this.index.keys()) {
        if (key.startsWith("detail ") && !drop.includes(key)) held.add(`item ${key.slice("detail ".length)}`);
      }
      return [...this.index.keys()].filter((k) => k.startsWith("item ") && !held.has(k));
    };
    const size = (keys: Iterable<string>) => [...keys].reduce((sum, k) => sum + this.size(k), 0);
    let freed = unheld();
    total -= size(freed);
    for (const key of answers) {
      if (total <= target) break;
      drop.push(key);
      refsOf.delete(key);
      const now = unheld();
      total -= this.size(key) + size(now.filter((k) => !freed.includes(k)));
      freed = now;
    }
    for (const key of freed) {
      drop.push(key);
      this.memo.delete(key);
    }
    await this.save([], drop);
  }

  /** At sign-out: nothing more is written, and what is in memory goes. The database itself goes with the keys
   * (wire/keys.ts, deleteAgentsDatabase), so the next sign-in starts with an empty cache. */
  forget(): void {
    this.writing = false;
    this.index.clear();
    this.memo.clear();
    this.edited.clear();
    this.loaded = undefined;
  }
}

/** The page's cache, set at start; none in the tests that do not set it. */
let pageCache: WorkCache | undefined;
export function setWorkCache(cache: WorkCache | undefined): void { pageCache = cache; }
export function workCache(): WorkCache | undefined { return pageCache; }

/** A page's kept answer of its own shape (#6573), with when it was read (#5142), for its "Last update" line. */
export function keptWorkRead<T>(page: string, asked: string): Promise<{ value: T; at: number } | undefined> {
  return pageCache ? pageCache.kept<T>(page, asked) : Promise.resolve(undefined);
}

/** A machine's answer to a page, kept for the next open. */
export function keepWork(page: string, asked: string, value: unknown): void {
  void pageCache?.keep(page, asked, value);
}
