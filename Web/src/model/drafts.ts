// What is typed and not sent, kept per session and per new-agent form across a reload, as the
// window keeps its drafts across a relaunch (#254). In localStorage, bounded: a count of drafts, an
// age since the last edit (#6793), and a draft's attachments only while they are small. A draft is a
// person's own, so a full store refuses a new one and says so; it never drops one to make room
// (docs/explanation/data-stores.md, "Evict or refuse"). A refused draft stays while the page is open.
import type { Attachment } from "../protocol/generated";

export interface Draft { text: string; attachments: Attachment[] }

interface Storage { getItem(key: string): string | null; setItem(key: string, value: string): void; removeItem(key: string): void }

const storageKey = "agents.drafts";
/** The most drafts kept. */
export const draftsKept = 40;
/** A draft with no edit for this long goes, as on the Mac and the Remote (`DraftStore.horizon`). */
export const draftAge = 30 * 24 * 60 * 60 * 1000;
const sweepEvery = 60 * 60 * 1000;
/** Said when a draft is refused, once for each. */
export const draftsFull = `This draft is not saved: ${draftsKept} drafts are kept already. It stays until this page closes. Send or empty another draft to save it.`;
/** The most a draft's attachments may take, as JSON, to be kept with it. */
export const attachmentsKept = 256 * 1024;

/**
 * The key of New Session's draft (#5376): the project's, so the words stay when On moves the form
 * to the project's folder on another machine. A folder in no project keeps a draft of its own,
 * under the key it had before. The rule of `DraftKey.newSession` in Packages/AgentsKit.
 */
export function newSessionDraftKey(host: string, folder: string, project: string | undefined): string {
  return project ? `new|project:${project}` : `new|${host}|${folder}`;
}

/** A draft with when it was last edited, as kept. */
interface Kept extends Draft { editedAt: number }

export class Drafts {
  /** Every draft of this page, the one edited last at the end. */
  private readonly held = new Map<string, Kept>();
  /** The drafts that storage keeps; the others were refused. */
  private readonly saved = new Set<string>();
  private readonly told = new Set<string>();
  private timer: ReturnType<typeof setInterval> | undefined;

  constructor(private readonly storage: Storage | undefined, private readonly refused: (sentence: string) => void = () => undefined,
    private readonly now: () => number = () => Date.now()) {
    const { drafts, changed } = this.stored();
    for (const [key, draft] of drafts) {
      this.held.set(key, draft);
      this.saved.add(key);
    }
    if (changed) this.write(drafts);
  }

  /** Sweeps each hour from now, so a page left open lets an old draft go too. */
  open(): void {
    this.timer ??= setInterval(() => this.sweep(), sweepEvery);
  }

  /**
   * Lets go of the drafts with no edit in `draftAge`. From what storage holds now, not from this
   * page's drafts: another tab may have saved one since.
   */
  sweep(): void {
    for (const [key, draft] of this.held) if (!this.fresh(draft)) this.forget(key);
    const { drafts, changed } = this.stored();
    if (changed) this.write(drafts);
    // There is room again for a draft refused before.
    if ([...this.held.keys()].some((key) => !this.saved.has(key)) && this.saved.size < draftsKept) this.save();
  }

  get(key: string): Draft | undefined {
    const draft = this.held.get(key);
    return draft && this.fresh(draft) ? { text: draft.text, attachments: draft.attachments } : undefined;
  }

  set(key: string, draft: Draft): void {
    this.put(key, { text: draft.text, attachments: draft.attachments, editedAt: this.now() });
  }

  delete(key: string): void {
    if (this.forget(key)) this.save();
  }

  /** The draft under `from` goes to `to`, unless `to` has one already (#5376: a folder's draft, kept before it was a project's). */
  move(from: string, to: string): void {
    if (from === to || this.get(to)) return;
    const draft = this.held.get(from);
    if (!draft) return;
    this.forget(from);
    this.put(to, draft);
  }

  /** Every draft goes: the control plane they were typed to forgot this browser (#3670). */
  clear(): void {
    for (const key of [...this.held.keys()]) this.forget(key);
    try { this.storage?.removeItem(storageKey); } catch { /* off: nothing kept */ }
  }

  private fresh(draft: Kept): boolean {
    return this.now() - draft.editedAt < draftAge;
  }

  private put(key: string, draft: Kept): void {
    this.held.delete(key);
    if (draft.text === "" && draft.attachments.length === 0) this.forget(key);
    else this.held.set(key, draft);
    this.save();
  }

  private forget(key: string): boolean {
    this.saved.delete(key);
    this.told.delete(key);
    return this.held.delete(key);
  }

  /** What storage holds less what has aged out, and whether that differs from what it holds. */
  private stored(): { drafts: Array<[string, Kept]>; changed: boolean } {
    const drafts: Array<[string, Kept]> = [];
    let changed = false;
    try {
      const now = this.now();
      for (const [key, draft] of JSON.parse(this.storage?.getItem(storageKey) ?? "[]") as Array<[string, Partial<Kept> | null]>) {
        if (typeof draft?.text !== "string") { changed = true; continue; }
        // Kept before drafts had a time, or by a clock that was ahead: its age counts from now.
        const dated = typeof draft.editedAt === "number" && draft.editedAt <= now ? draft.editedAt : now;
        const kept: Kept = { text: draft.text, attachments: draft.attachments ?? [], editedAt: dated };
        if (dated !== draft.editedAt) changed = true;
        if (this.fresh(kept)) drafts.push([key, kept]);
        else changed = true;
      }
    } catch { return { drafts: [], changed: false }; /* unreadable: start empty */ }
    return { drafts, changed };
  }

  private save(): void {
    if (!this.storage) return;
    for (const [key, draft] of this.held) if (!this.fresh(draft)) this.forget(key);
    for (const key of this.held.keys()) {
      if (this.saved.has(key)) continue;
      if (this.saved.size < draftsKept) {
        this.saved.add(key);
        this.told.delete(key);
      } else if (!this.told.has(key)) {
        this.told.add(key);
        this.refused(draftsFull);
      }
    }
    this.write([...this.held].filter(([key]) => this.saved.has(key)).map(([key, draft]): [string, Kept] => {
      const small = JSON.stringify(draft.attachments).length <= attachmentsKept;
      return [key, small ? draft : { ...draft, attachments: [] }];
    }));
  }

  private write(drafts: Array<[string, Kept]>): void {
    try { this.storage?.setItem(storageKey, JSON.stringify(drafts)); } catch { /* full: kept in memory */ }
  }
}
