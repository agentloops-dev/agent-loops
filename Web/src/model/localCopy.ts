// The page's local copy (#4790; spec 4777-fast, "the copy is a cache, never the record"): each
// machine's row, folders and lean session records (the live ones and the archived page last
// shown), and the end of the last 20 transcripts opened, in IndexedDB beside the keys. The page
// draws it at once, marked as from this device, and the host's answer always wins. A forget
// deletes the database, so the copy goes with the keys (direction.md, customer data).
import { effect } from "@preact/signals";
import type { Agent, ControlMachine, FolderSummary, ListRevision, TranscriptPage, TurnsPage } from "../protocol/generated";
import { formatRef } from "../protocol/ref";
import { openAgentsDatabase } from "../wire/keys";
import { cacheAgeLimit, type CacheStats, type CacheStore } from "./cacheStore";
import type { Store } from "./store";

/** The most the copy holds, as the Remote's (spec 4777-fast). */
export const copyCap = 50 * 1024 * 1024;
/** The transcripts kept: the last ones opened. */
export const transcriptsKept = 20;
/** A transcript's entries kept: its end, as one `agents/transcript` page (store.ts `turnPage`). */
export const entriesKept = 200;
/** The largest record, in UTF-8 bytes of its JSON (#6792, as the Mac's). A machine over it loses its archived records and a
 * chat its oldest entries; one still over it, or a picture over it, is not kept, so it is read again next time. */
export const recordCap = 16 * 1024 * 1024;
const sweepEvery = 60 * 60 * 1000;
/** A change is written this long after the last one, so a busy chat writes once a second at most. */
export const writeAfter = 1_000;

/** One machine: its row, folders and session records. */
export interface MachineCopy {
  kind: "machine";
  ref: string;
  host: ControlMachine;
  folders?: FolderSummary[];
  agents: Agent[];
  /** Where `agents` are in the host's order of changes (#4794): a launch asks only for what changed since. */
  point?: ListRevision;
  at: number;
}

/** One transcript's end: its finished turns and its last page of entries. */
export interface TranscriptCopy {
  kind: "transcript";
  ref: string;
  host: string;
  session: string;
  turns: TurnsPage;
  page: TranscriptPage;
  at: number;
}

/** What each Work entry holds (#4930), with how many of it are kept, the newest first; the pictures also to `imageCap`. Since
 * #6356 only the forge's pictures: Work's records and answers are Work's cache (workCache.ts). */
export const workKept = { image: 2000 } as const;
export type WorkGroup = keyof typeof workKept;

/** One of Work's last answers (#4930), kept by what was asked: a cache, never the record. */
export interface WorkCopy {
  kind: "work";
  ref: string;
  group: WorkGroup;
  value: unknown;
  at: number;
}

export type CopyRecord = MachineCopy | TranscriptCopy | WorkCopy;

/** What the copy knows of each record without reading it, kept as one record of its own. */
export interface CopyIndexRow {
  ref: string;
  kind: CopyRecord["kind"];
  /** A Work entry's group (#4930). */
  group?: WorkGroup | undefined;
  /** A transcript's machine (#6792): it goes with it. None in a row from before #6792. */
  host?: string | undefined;
  /** When the record was read from its host; drawing it does not make it newer. */
  at: number;
  bytes: number;
}

/** A machine's ref (#4727). */
export function machineRef(host: string): string {
  return formatRef({ provider: "agents", type: "machine", topic: "", id: host });
}

/**
 * A session's ref (#4727). The topic, the folder's origin, is left out: the session's id is
 * unique without it, and a session of a folder with no origin has none.
 */
export function sessionRef(session: string): string {
  return formatRef({ provider: "agents", type: "session", topic: "", id: session });
}

/** A Work entry's ref (#4727, #4930): its group, and what was asked. */
export function workRef(group: WorkGroup, asked: string): string {
  return formatRef({ provider: "agents", type: "work", topic: group, id: asked });
}

/**
 * The refs to drop so the copy keeps its caps: transcripts past the newest `kept`, then, while the whole is over `cap`,
 * the oldest Work entry, then the oldest transcript, and last the oldest machine.
 */
export function overCaps(rows: readonly CopyIndexRow[], cap = copyCap, kept = transcriptsKept, imageCap = cap / 4): string[] {
  const newest = (a: CopyIndexRow, b: CopyIndexRow) => b.at - a.at;
  const transcripts = rows.filter((r) => r.kind === "transcript").sort(newest);
  const drop = transcripts.slice(kept);
  // A Work entry of a group no longer kept here (a list or an item before #6356, now in Work's cache) goes.
  drop.push(...rows.filter((r) => r.kind === "work" && !(r.group && r.group in workKept)));
  // Each Work group to its count, and the pictures to their own cap (#4930).
  const work: CopyIndexRow[] = [];
  for (const group of Object.keys(workKept) as WorkGroup[]) {
    const held = rows.filter((r) => r.kind === "work" && r.group === group).sort(newest);
    let bytes = held.reduce((sum, r) => sum + r.bytes, 0);
    while (held.length > workKept[group] || (group === "image" && held.length > 0 && bytes > imageCap)) {
      const oldest = held.pop()!;
      bytes -= oldest.bytes;
      drop.push(oldest);
    }
    work.push(...held);
  }
  let total = rows.reduce((sum, r) => sum + r.bytes, 0) - drop.reduce((sum, r) => sum + r.bytes, 0);
  // Over the cap, the oldest Work entry goes first, then the oldest transcript.
  work.sort(newest);
  while (total > cap && work.length > 0) {
    const oldest = work.pop()!;
    drop.push(oldest);
    total -= oldest.bytes;
  }
  for (let i = kept - 1; i >= 0 && total > cap; i--) {
    const oldest = transcripts[i];
    if (!oldest || drop.includes(oldest)) continue;
    drop.push(oldest);
    total -= oldest.bytes;
  }
  const machines = rows.filter((r) => r.kind === "machine").sort(newest);
  while (total > cap && machines.length > 0) {
    const oldest = machines.pop()!;
    drop.push(oldest);
    total -= oldest.bytes;
  }
  return drop.map((r) => r.ref);
}

/** Where the copy lives: IndexedDB on the page, memory in the tests. */
export interface CopyStore {
  get(ref: string): Promise<unknown>;
  /** Puts `records` and changes the stored index in one transaction, so two tabs do not undo each other's index. What the
   * new index does not name goes: of what the old one named, or with `all`, of everything held. Answers the new index. */
  write(records: readonly CopyRecord[], change: (stored: readonly CopyIndexRow[]) => CopyIndexRow[], all?: boolean): Promise<CopyIndexRow[]>;
}

const indexKey = "@index";

function request<T>(r: IDBRequest<T>): Promise<T> {
  return new Promise((resolve, reject) => {
    r.onsuccess = () => resolve(r.result);
    r.onerror = () => reject(r.error);
  });
}

/** The keys to delete once the index is `rows`. */
function unnamed(keys: readonly string[], rows: readonly CopyIndexRow[]): string[] {
  const named = new Set(rows.map((r) => r.ref));
  return keys.filter((k) => k !== indexKey && !named.has(k));
}

/** The store `copy` in the database `agents` (wire/keys.ts). */
// store-bounds: kind=cache; size=50MiB; age=90d; action=evict; record=16MiB; applied=on write, on open and hourly
export class IndexedCopyStore implements CopyStore {
  async get(ref: string): Promise<unknown> {
    const db = await openAgentsDatabase();
    try {
      return await new Promise((resolve, reject) => {
        const request = db.transaction("copy", "readonly").objectStore("copy").get(ref);
        request.onsuccess = () => resolve(request.result);
        request.onerror = () => reject(request.error);
      });
    } finally {
      db.close();
    }
  }

  async write(records: readonly CopyRecord[], change: (stored: readonly CopyIndexRow[]) => CopyIndexRow[], all = false): Promise<CopyIndexRow[]> {
    const db = await openAgentsDatabase();
    try {
      const tx = db.transaction("copy", "readwrite");
      const done = new Promise<void>((resolve, reject) => {
        tx.oncomplete = () => resolve();
        tx.onerror = () => reject(tx.error);
        tx.onabort = () => reject(tx.error);
      });
      // A failed request also aborts the transaction, which `done` says.
      done.catch(() => undefined);
      const store = tx.objectStore("copy");
      const stored = ((await request(store.get(indexKey))) as CopyIndexRow[] | undefined) ?? [];
      const rows = change(stored);
      const held = all ? (await request(store.getAllKeys())).map(String) : stored.map((r) => r.ref);
      for (const record of records) store.put(record, record.ref);
      for (const ref of unnamed([...held, ...records.map((r) => r.ref)], rows)) store.delete(ref);
      store.put(rows, indexKey);
      await done;
      return rows;
    } finally {
      db.close();
    }
  }
}

/** For the tests: the same contract, in memory. */
export class MemoryCopyStore implements CopyStore {
  readonly held = new Map<string, unknown>();
  async get(ref: string): Promise<unknown> {
    return structuredClone(this.held.get(ref));
  }
  async write(records: readonly CopyRecord[], change: (stored: readonly CopyIndexRow[]) => CopyIndexRow[], all = false): Promise<CopyIndexRow[]> {
    const stored = (structuredClone(this.held.get(indexKey)) as CopyIndexRow[] | undefined) ?? [];
    const rows = change(stored);
    const held = all ? [...this.held.keys()] : stored.map((r) => r.ref);
    for (const record of records) this.held.set(record.ref, structuredClone(record));
    for (const ref of unnamed([...held, ...records.map((r) => r.ref)], rows)) this.held.delete(ref);
    this.held.set(indexKey, structuredClone(rows));
    return rows;
  }
}

const encoder = new TextEncoder();
const utf8 = (text: string) => encoder.encode(text).length;

/** The record cut to `cap`, with its size; undefined when it cannot be. */
function fitted(record: CopyRecord, cap: number): { record: CopyRecord; bytes: number } | undefined {
  let fit = record;
  for (;;) {
    const bytes = utf8(JSON.stringify(fit));
    if (bytes <= cap) return { record: fit, bytes };
    if (fit.kind === "machine") {
      const live = fit.agents.filter((a) => a.state !== "archived");
      if (live.length === fit.agents.length) return undefined;
      // The archived ones left out are not in the point's list.
      const { point: _, ...rest } = fit;
      fit = { ...rest, agents: live };
    } else if (fit.kind === "transcript" && fit.page.entries.length > 0) {
      const less = Math.ceil(fit.page.entries.length / 2);
      fit = { ...fit, page: { ...fit.page, firstIndex: fit.page.firstIndex + less, entries: fit.page.entries.slice(less) } };
    } else return undefined;
  }
}

/**
 * The copy: its index in memory, its records in the store. Every failure is swallowed: a copy
 * that cannot be read or written is no copy, and the page fetches as it did before.
 */
export class LocalCopy implements CacheStore {
  private index: CopyIndexRow[] = [];
  private loaded = false;
  private queue: Promise<void> = Promise.resolve();
  private timer: ReturnType<typeof setInterval> | undefined;
  /** Off while the page is not signed in: a write then would make the database again. */
  writing = false;

  constructor(readonly store: CopyStore = new IndexedCopyStore(), readonly cap = copyCap, readonly recordBytes = recordCap,
    private readonly now: () => number = () => Date.now()) {}

  /** The page is signed in: it writes from now, and sweeps at once and each hour. */
  open(): void {
    if (this.writing) return;
    this.writing = true;
    void this.sweep();
    this.timer ??= setInterval(() => void this.sweep(), sweepEvery);
  }

  private fresh(at: number): boolean {
    return at >= this.now() - cacheAgeLimit;
  }

  /** The rows less what has aged out (#6792), then less what is over the caps. */
  private bounded(rows: readonly CopyIndexRow[]): CopyIndexRow[] {
    const fresh = rows.filter((r) => this.fresh(r.at));
    const over = new Set(overCaps(fresh, this.cap));
    return fresh.filter((r) => !over.has(r.ref));
  }

  /** One write after another; each failure is swallowed. */
  private serial(act: () => Promise<void>): Promise<void> {
    this.queue = this.queue.then(() => (this.writing ? act() : undefined)).catch(() => undefined);
    return this.queue;
  }

  private async ready(): Promise<void> {
    if (this.loaded) return;
    this.index = ((await this.store.get(indexKey)) as CopyIndexRow[] | undefined) ?? [];
    this.loaded = true;
  }

  /** The index, then every machine it names. */
  async machines(): Promise<MachineCopy[]> {
    try {
      const rows = ((await this.store.get(indexKey)) as CopyIndexRow[] | undefined) ?? [];
      const machines = (await Promise.all(rows.filter((r) => r.kind === "machine").map((r) => this.store.get(r.ref)))) as Array<MachineCopy | undefined>;
      return machines.filter((m): m is MachineCopy => m?.kind === "machine" && this.fresh(m.at));
    } catch {
      return [];
    }
  }

  async transcript(session: string): Promise<TranscriptCopy | null> {
    try {
      const got = (await this.store.get(sessionRef(session))) as TranscriptCopy | undefined;
      return got?.kind === "transcript" && this.fresh(got.at) ? got : null;
    } catch {
      return null;
    }
  }

  /**
   * Puts records and keeps the bounds, one write after another. With `machines`, the hosts the control plane lists now,
   * every other machine in the copy goes, with its chats.
   */
  put(records: readonly CopyRecord[], machines?: readonly string[]): Promise<void> {
    if (!this.writing || (records.length === 0 && !machines)) return this.queue;
    return this.serial(async () => {
      await this.ready();
      const fits = records.flatMap((r) => fitted(r, this.recordBytes) ?? []);
      const listed = machines && new Set(machines);
      const listedRefs = machines && new Set(machines.map(machineRef));
      // A chat of a row from before #6792 does not say its machine: it goes too, and is read again when it opens.
      const gone = (r: CopyIndexRow) => !!listed && !!listedRefs && (r.kind === "machine" ? !listedRefs.has(r.ref)
        : r.kind === "transcript" && !(r.host !== undefined && listed.has(r.host)));
      const replaced = new Set(records.map((r) => r.ref));
      const rows = fits.map(({ record, bytes }): CopyIndexRow => ({ ref: record.ref, kind: record.kind, at: record.at,
        ...(record.kind === "work" ? { group: record.group } : record.kind === "transcript" ? { host: record.host } : {}),
        // With its row in the index: its ref and its numbers.
        bytes: bytes + utf8(record.ref) + 64 }));
      const change = (stored: readonly CopyIndexRow[]) =>
        this.bounded([...stored.filter((r) => !replaced.has(r.ref)), ...rows].filter((r) => !gone(r)));
      if (records.length === 0 && change(this.index).length === this.index.length) return;
      if (!this.writing) return;
      this.index = await this.store.write(fits.map((f) => f.record), change);
    });
  }

  /** At sign-in and each hour: what another tab wrote is counted, what the index does not name goes, then the bounds. */
  sweep(): Promise<void> {
    return this.serial(async () => {
      this.index = await this.store.write([], (stored) => this.bounded(stored), true);
      this.loaded = true;
    });
  }

  async stats(): Promise<CacheStats> {
    await this.ready().catch(() => undefined);
    return { count: this.index.length, bytes: this.index.reduce((sum, r) => sum + r.bytes, 0),
      oldest: this.index.length === 0 ? undefined : Math.min(...this.index.map((r) => r.at)) };
  }

  /** Work's last answer for what was asked (#4930); undefined with none. */
  async work<T>(group: WorkGroup, asked: string): Promise<T | undefined> {
    return (await this.workRead<T>(group, asked))?.value;
  }

  /** Work's last answer for what was asked, with when this page read it from its machine (#5142); undefined with none. */
  async workRead<T>(group: WorkGroup, asked: string): Promise<{ value: T; at: number } | undefined> {
    try {
      const got = (await this.store.get(workRef(group, asked))) as WorkCopy | undefined;
      return got?.kind === "work" && got.group === group && this.fresh(got.at) ? { value: got.value as T, at: got.at } : undefined;
    } catch {
      return undefined;
    }
  }

  /** A host's answer, kept for the next open (#4930). Only a host's answers are kept, never what came from the copy. */
  keepWork(group: WorkGroup, asked: string, value: unknown, at = this.now()): Promise<void> {
    return this.put([{ kind: "work", ref: workRef(group, asked), group, value, at }]);
  }

  /** At sign-out: nothing more is written, and what is in memory goes. The database itself goes with the keys. */
  forget(): void {
    this.writing = false;
    clearInterval(this.timer);
    this.timer = undefined;
    this.index = [];
    this.loaded = false;
  }
}

/** The end of the open transcript, as a page: the last `entriesKept` entries held. */
export function transcriptEnd(store: Store, host: string, session: string, at: number): TranscriptCopy {
  const entries = store.entries.peek();
  const cut = Math.max(0, entries.length - entriesKept);
  const kept = entries.slice(cut);
  const firstIndex = store.firstEntryIndex.peek() + cut;
  return {
    kind: "transcript", ref: sessionRef(session), host, session, at,
    turns: { turns: store.turns.peek(), firstTurn: store.firstTurn.peek(), openStart: store.openTurnStart.peek(),
      ...(store.firstPrompt.peek() === null ? {} : { firstPrompt: store.firstPrompt.peek()! }) },
    // The host's count, not this page's (#4794): a reconnect resumes from it, and an entry heard but
    // never written would count past it.
    page: { firstIndex, total: store.chatEnd() ?? firstIndex + kept.length, entries: kept },
  };
}

/**
 * Draws the copy into the store at once, and keeps it as the hosts answer: a machine when its
 * row, folders or sessions change, the open transcript when its entries or turns do, each a
 * second after the last change. Only what a host said is written, never what came from the copy.
 */
export function startLocalCopy(store: Store, copy: LocalCopy, now: () => number = Date.now): () => void {
  void copy.machines().then((machines) => store.drawCopy(machines));
  // A chat with nothing held reads its copy before asking its host, so it can resume from it (#4794).
  store.readCopy = (session) => copy.transcript(session);
  const written = new Map<string, { host: ControlMachine; folders?: FolderSummary[] | undefined; agents?: Agent[] | undefined;
    point?: ListRevision | undefined }>();
  let timer: ReturnType<typeof setTimeout> | undefined;
  const later = (write: () => void) => {
    if (timer) clearTimeout(timer);
    timer = setTimeout(() => { timer = undefined; write(); }, writeAfter);
  };
  const writeMachines = () => {
    const records: MachineCopy[] = [];
    const hosts = store.hosts.peek();
    const fromDevice = store.fromDevice.peek();
    for (const host of hosts) {
      if (fromDevice.has(host.id)) continue;
      const folders = store.folders.peek()[host.id];
      const agents = store.agents.peek()[host.id];
      const point = store.listPoint(host.id);
      const was = written.get(host.id);
      if (was && was.host === host && was.folders === folders && was.agents === agents && was.point === point) continue;
      written.set(host.id, { host, folders, agents, point });
      records.push({ kind: "machine", ref: machineRef(host.id), host, ...(folders ? { folders } : {}), agents: agents ?? [],
        ...(point ? { point } : {}), at: now() });
    }
    for (const id of [...written.keys()]) if (!hosts.some((h) => h.id === id)) written.delete(id);
    // Until the control plane lists them, the hosts are the copy's own (#6792).
    void copy.put(records, store.hostsListed ? hosts.map((h) => h.id) : undefined);
  };
  const stopMachines = effect(() => {
    void store.hosts.value;
    void store.folders.value;
    void store.agents.value;
    void store.fromDevice.value;
    if (copy.writing) later(writeMachines);
  });
  let transcriptTimer: ReturnType<typeof setTimeout> | undefined;
  const stopTranscript = effect(() => {
    const watching = store.watching.value;
    void store.entries.value;
    void store.turns.value;
    const source = store.transcriptSource.value;
    if (transcriptTimer) clearTimeout(transcriptTimer);
    if (!watching || source !== "host" || !copy.writing) return;
    transcriptTimer = setTimeout(() => {
      const still = store.watching.peek();
      if (still?.host !== watching.host || still.session !== watching.session || store.transcriptSource.peek() !== "host") return;
      void copy.put([transcriptEnd(store, watching.host, watching.session, now())]);
    }, writeAfter);
  });
  // A chat opened draws its copy until its host's page comes.
  const stopDraw = effect(() => {
    const watching = store.watching.value;
    if (!watching) return;
    void copy.transcript(watching.session).then((got) => { if (got) store.drawTranscriptCopy(got); });
  });
  return () => {
    store.readCopy = undefined;
    stopMachines();
    stopTranscript();
    stopDraw();
    if (timer) clearTimeout(timer);
    if (transcriptTimer) clearTimeout(transcriptTimer);
  };
}

/** The page's copy, for Work's pages (#4930): set at start, none in the tests that do not set it. */
let pageCopy: LocalCopy | undefined;
export function setWorkCopy(copy: LocalCopy | undefined): void { pageCopy = copy; }

/** Work's last answer this device kept for what was asked (#4930). */
export function keptWork<T>(group: WorkGroup, asked: string): Promise<T | undefined> {
  return pageCopy ? pageCopy.work<T>(group, asked) : Promise.resolve(undefined);
}

/** A host's answer to a Work page, kept (#4930). */
export function keepWork(group: WorkGroup, asked: string, value: unknown): void {
  void pageCopy?.keepWork(group, asked, value);
}

/** A forge's picture (#4930): from this device's copy when it holds it, as a picture does not change at its URL; else
 * read, and kept. */
export async function keptPicture<T>(url: string, read: (url: string) => Promise<T>): Promise<T> {
  const kept = await keptWork<T>("image", url);
  if (kept !== undefined) return kept;
  const got = await read(url);
  keepWork("image", url, got);
  return got;
}
