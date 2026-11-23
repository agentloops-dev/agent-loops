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

/** The cache's bounds (#6352; the web's 50 MB, epic #6348): over `bytes`, bodies go first, then the pages' answers and the
 * lists' answers, each the least recently drawn first, with the items no other answer holds. */
export interface WorkBounds { itemBytes: number; bodyBytes: number; comments: number; pageBytes: number; bytes: number }
export const webBounds: WorkBounds = { itemBytes: 4 * 1024, bodyBytes: 64 * 1024, comments: 50, pageBytes: 4 * 1024 * 1024, bytes: 50 * 1024 * 1024 };

/** Where the cache lives: IndexedDB on the page, memory in the tests. */
export interface WorkStore {
  /** The values of `keys`, in their order; undefined for a missing one. */
  get(keys: readonly string[]): Promise<unknown[]>;
  /** Puts and deletes in one transaction. */
  write(put: ReadonlyArray<[string, unknown]>, drop: readonly string[]): Promise<void>;
}

function request<T>(r: IDBRequest<T>): Promise<T> {
  return new Promise((resolve, reject) => {
    r.onsuccess = () => resolve(r.result);
    r.onerror = () => reject(r.error);
  });
}

/** The store `work` in the database `agents`. */
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

  write(put: ReadonlyArray<[string, unknown]>, drop: readonly string[]): Promise<void> {
    return this.run("readwrite", (store) => {
      for (const key of drop) store.delete(key);
      for (const [key, value] of put) store.put(value, key);
    });
  }
}

/** For the tests: the same contract, in memory. */
export class MemoryWorkStore implements WorkStore {
  readonly held = new Map<string, unknown>();
  async get(keys: readonly string[]): Promise<unknown[]> {
    return keys.map((k) => structuredClone(this.held.get(k)));
  }
  async write(put: ReadonlyArray<[string, unknown]>, drop: readonly string[]): Promise<void> {
    for (const key of drop) this.held.delete(key);
    for (const [key, value] of put) this.held.set(key, structuredClone(value));
  }}

/** A machine's answer as the store keeps it; the items are their own records. */
interface KeptAnswer {
  pages: WorkPage[];
  total?: number | undefined;
  forges?: ForgeCapabilities[] | undefined;
  errors?: ForgeListError[] | undefined;
  failed?: number | undefined;
}

/** What the cache knows of each record without reading it: its size and when it was last drawn, kept as one record. */
interface IndexRow { bytes: number; used: number }

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
export class WorkCache {
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

  constructor(readonly store: WorkStore = new IndexedWorkStore(), readonly bounds: WorkBounds = webBounds,
    private readonly now: () => number = () => Date.now()) {}

  private serial<T>(act: () => Promise<T>): Promise<T> {
    const run = this.turn.then(act, act);
    this.turn = run.catch(() => undefined);
    return run;
  }

  private ready(): Promise<void> {
    this.loaded ??= this.store.get([indexKey]).then(([rows]) => {
      this.index = new Map(Object.entries((rows as Record<string, IndexRow> | undefined) ?? {}));
    }, () => undefined);
    return this.loaded;
  }

  private async read<T>(keys: readonly string[]): Promise<(T | undefined)[]> {
    await this.ready();
    await this.queue;
    try {
      return (await this.store.get(keys)) as (T | undefined)[];
    } catch {
      return keys.map(() => undefined);
    }
  }

  /** Writes records and the index, one write after another; `used` only moves the records' times. */
  private save(put: ReadonlyArray<[string, unknown]>, drop: readonly string[] = [], used: readonly string[] = []): Promise<void> {
    if (!this.writing) return this.queue;
    const at = this.now();
    for (const key of drop) this.index.delete(key);
    for (const [key, value] of put) this.index.set(key, { bytes: JSON.stringify(value).length, used: at });
    for (const key of used) {
      const row = this.index.get(key);
      if (row) row.used = at;
    }
    const all: Array<[string, unknown]> = [...put, [indexKey, Object.fromEntries(this.index)]];
    this.queue = this.queue.then(() => (this.writing ? this.store.write(all, drop) : undefined)).catch(() => undefined);
    return this.queue;
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
    const found = hosts.flatMap((host, i) => (kept[i] ? [{ host, kept: kept[i]! }] : []));
    if (found.length === 0) return undefined;
    await this.load(found.flatMap((f) => f.kept.pages.flatMap((p) => p.refs)));
    void this.save([], [], found.map((f) => answerKey(q, f.host)));
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
      const put = await this.itemsToPut(list.items);
      // The forges' read time, not this call's (#6642): the daemon can answer from a copy up to a minute old.
      const readAt = list.readAt === undefined ? at : Math.min(at, fromWireDate(list.readAt).getTime());
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
      await this.save([...put, [key, answer]]);
      await this.trim();
    });
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
      await this.save([[key, { ...(kept ?? { pages: [] }), errors, failed: this.now() } satisfies KeptAnswer]]);
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
      await this.save(await this.itemsToPut(items));
    });
  }

  /** The items to write: each replaces a kept one only when as new or newer, so a late answer does not undo a newer one;
   * one over `bounds.itemBytes` after trimming is not kept. */
  private async itemsToPut(items: readonly ForgeItem[]): Promise<Array<[string, unknown]>> {
    await this.load(items.map((i) => itemKey(i.node)));
    const put: Array<[string, unknown]> = [];
    for (const item of items) {
      const key = itemKey(item.node);
      const had = this.memo.get(key);
      if (had && (had.updatedAt > item.updatedAt || (this.edited.has(key) && had.updatedAt === item.updatedAt))) continue;
      const fit = this.fitted(item);
      if (!fit) continue;
      this.edited.delete(key);
      this.memo.set(key, fit);
      put.push([key, fit]);
    }
    return put;
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
      await this.save([[key, fit]]);
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
    return this.memo.get(key);
  }

  // Details

  /** An item's kept page, with the newest kept copy of the item; undefined when it was never read here. */
  async detail(node: LineageNode): Promise<WorkDetail | undefined> {
    const [found] = await this.read<WorkDetail>([detailKey(node)]);
    if (!found) return undefined;
    void this.save([], [], [detailKey(node)]);
    const item = await this.item(node);
    // The kept item is never older than the page's: an optimistic change keeps its updatedAt.
    return item && item.updatedAt >= found.got.item.updatedAt ? { ...found, got: { ...found.got, item } } : found;
  }

  keepDetail(host: string, got: ForgeItemDetail): Promise<void> {
    return this.serial(async () => {
      const put = await this.itemsToPut([got.item]);
      const body = utf8(got.body) > this.bounds.bodyBytes ? prefixBytes(got.body, this.bounds.bodyBytes) : got.body;
      const kept: WorkDetail = { host, got: { ...got, body, comments: got.comments.slice(0, this.bounds.comments) }, read: this.now() };
      await this.save([...put, [detailKey(got.item.node), kept]]);
      await this.trim();
    });
  }

  // Pages

  /** A page's kept answer of its own shape (#6573), with when it was read; undefined when it was never answered here. */
  async kept<T>(page: string, asked: string): Promise<{ value: T; at: number } | undefined> {
    const key = pageKey(page, asked);
    const [found] = await this.read<{ value: T; at: number }>([key]);
    if (!found) return undefined;
    void this.save([], [], [key]);
    return found;
  }

  /** Keeps a page's answer for what it asked, read now, in place of the last one; one over `bounds.pageBytes` is not kept,
   * so the page asks again next time. */
  keep(page: string, asked: string, value: unknown): Promise<void> {
    return this.serial(async () => {
      const key = pageKey(page, asked);
      const record = { value, at: this.now() };
      if (JSON.stringify(record).length > this.bounds.pageBytes) return this.save([], [key]);
      await this.save([[key, record]]);
      await this.trim();
    });
  }

  // Bounds

  /** The kept records' size: what `bounds.bytes` bounds. */
  bytes(): number {
    let total = 0;
    for (const row of this.index.values()) total += row.bytes;
    return total;
  }

  /** Brings the cache under `bounds.bytes`, to nine tenths of it so the next page does not trim again: bodies first, the
   * least recently opened first; then the pages' answers; then the lists' answers, with the items no kept answer holds. */
  private async trim(): Promise<void> {
    let total = this.bytes();
    if (total <= this.bounds.bytes) return;
    const target = this.bounds.bytes / 10 * 9;
    const oldest = (prefix: string) => [...this.index].filter(([k]) => k.startsWith(prefix)).sort(([, a], [, b]) => a.used - b.used);
    const drop: string[] = [];
    for (const prefix of ["detail ", "page "]) {
      for (const [key, row] of oldest(prefix)) {
        if (total <= target) break;
        drop.push(key);
        total -= row.bytes;
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
    const size = (keys: Iterable<string>) => [...keys].reduce((sum, k) => sum + (this.index.get(k)?.bytes ?? 0), 0);
    let freed = unheld();
    total -= size(freed);
    for (const key of answers) {
      if (total <= target) break;
      drop.push(key);
      refsOf.delete(key);
      const now = unheld();
      total -= (this.index.get(key)?.bytes ?? 0) + size(now.filter((k) => !freed.includes(k)));
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
