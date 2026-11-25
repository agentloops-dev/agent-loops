// What is typed and not sent, kept per session and per new-agent form across a reload, as the
// window keeps its drafts across a relaunch (#254). In localStorage, bounded: a count of drafts, a size
// for each (#7199), an age since the last edit (#6793), and a draft's attachments only while they are small. A draft is a
// person's own, so a full store refuses a new one and says so; it never drops one to make room
// (docs/explanation/data-stores.md, "Evict or refuse"). A refused draft stays while the page is open,
// and changes nothing that is kept: not the others, nor its own last words that fitted.
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
/** The most bytes one draft may take as kept: `DraftStore.draftBytes`. */
export const draftBytes = 1024 * 1024;
export const draftTooBig = "This draft is not saved: it is over 1 MB. It stays until this page closes. Send it, or make it shorter to save it.";
/** Said when the browser's own quota refuses a draft. */
export const draftNoRoom = "This draft is not saved: the browser has no room for it. It stays until this page closes. Send or empty another draft to save it.";
/** Said when a draft's words are kept without its attachments. */
export const attachmentsCut = "This draft's attachments are not saved: they are over 256 KB. They stay until this page closes.";
/** The most bytes a draft's attachments may take, as JSON, to be kept with it. */
export const attachmentsKept = 256 * 1024;

/**
 * The key of New Session's draft (#5376): the project's, so the words stay when On moves the form
 * to the project's folder on another machine. A folder in no project keeps a draft of its own,
 * under the key it had before. The rule of `DraftKey.newSession` in Packages/AgentsKit.
 */
export function newSessionDraftKey(host: string, folder: string, project: string | undefined): string {
  return project ? `new|project:${project}` : `new|${host}|${folder}`;
}

const encoder = new TextEncoder();
/** Whether the words take more than `most` bytes of UTF-8. */
function over(words: string, most: number): boolean {
  return words.length > most || encoder.encode(words).length > most;
}

/** A draft with when it was last edited, as kept. */
interface Kept extends Draft { editedAt: number }

export class Drafts {
  /** Every draft of this page, the one edited last at the end. */
  private readonly held = new Map<string, Kept>();
  /** The drafts that storage keeps, as it keeps them. */
  private readonly saved = new Map<string, Kept>();
  /** The drafts whose last edit storage does not keep. */
  private readonly waiting = new Set<string>();
  /** What was said last of a draft not kept whole. */
  private readonly told = new Map<string, string>();
  /** The length of what was written last. */
  private written = 0;
  private timer: ReturnType<typeof setInterval> | undefined;

  constructor(private readonly storage: Storage | undefined, private readonly refused: (sentence: string) => void = () => undefined,
    private readonly now: () => number = () => Date.now()) {
    const { drafts, changed } = this.stored();
    for (const [key, draft] of drafts) {
      this.held.set(key, draft);
      this.saved.set(key, draft);
    }
    this.written = JSON.stringify(drafts).length;
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
    // There may be room again for a draft refused before.
    if (this.waiting.size > 0) this.retry();
  }

  get(key: string): Draft | undefined {
    const draft = this.held.get(key);
    return draft && this.fresh(draft) ? { text: draft.text, attachments: draft.attachments } : undefined;
  }

  set(key: string, draft: Draft): void {
    this.put(key, { text: draft.text, attachments: draft.attachments, editedAt: this.now() });
  }

  delete(key: string): void {
    if (this.forget(key)) this.release();
  }

  /** The draft under `from` goes to `to`, unless `to` has one already (#5376: a folder's draft, kept before it was a project's). */
  move(from: string, to: string): void {
    if (from === to || this.get(to)) return;
    const draft = this.held.get(from);
    if (!draft) return;
    const saved = this.saved.get(from);
    this.forget(from);
    if (!saved) {
      this.put(to, draft);
      return;
    }
    // Not a new draft, so no limit refuses what is kept already.
    this.held.set(to, draft);
    this.saved.set(to, saved);
    this.writeSaved();
    if (draft !== saved) this.keep(to);
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
    for (const [other, kept] of this.held) if (!this.fresh(kept)) this.forget(other);
    if (draft.text === "" && draft.attachments.length === 0) {
      this.forget(key);
      this.release();
      return;
    }
    this.held.set(key, draft);
    const before = this.written;
    this.keep(key);
    if (this.written < before) this.retry();
  }

  private forget(key: string): boolean {
    this.saved.delete(key);
    this.waiting.delete(key);
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

  /** A draft went: what is kept without it, then those that waited for room. */
  private release(): void {
    this.writeSaved();
    this.retry();
  }

  private retry(): void {
    // One too big changes only by its own edit.
    for (const key of [...this.waiting]) if (this.told.get(key) !== draftTooBig) this.keep(key);
  }

  /** Saves the draft under `key`, or refuses it and says so; what is kept already stays as it is. */
  private keep(key: string): void {
    const draft = this.held.get(key);
    if (!this.storage || !draft) return;
    const cut = over(JSON.stringify(draft.attachments), attachmentsKept);
    const kept = cut ? { ...draft, attachments: [] } : draft;
    const before = this.saved.get(key);
    let problem: string | undefined;
    if (over(JSON.stringify(kept), draftBytes)) problem = draftTooBig;
    else if (!before && this.saved.size >= draftsKept) problem = draftsFull;
    else {
      this.saved.delete(key);
      this.saved.set(key, kept);
      if (!this.writeSaved()) {
        problem = draftNoRoom;
        if (before) this.saved.set(key, before);
        else this.saved.delete(key);
      }
    }
    if (problem) this.waiting.add(key);
    else this.waiting.delete(key);
    this.tell(key, problem ?? (cut ? attachmentsCut : undefined));
  }

  private tell(key: string, sentence: string | undefined): void {
    if (this.told.get(key) === sentence) return;
    if (sentence === undefined) {
      this.told.delete(key);
      return;
    }
    this.told.set(key, sentence);
    this.refused(sentence);
  }

  private writeSaved(): boolean {
    return this.write([...this.saved]);
  }

  /** Whether storage took them. When it did not, it holds what it held before. */
  private write(drafts: Array<[string, Kept]>): boolean {
    const json = JSON.stringify(drafts);
    try { this.storage?.setItem(storageKey, json); } catch { return false; }
    this.written = json.length;
    return true;
  }
}
