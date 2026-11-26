// A blocked image on a Work page and Settings ▸ Machines ▸ Image Hosts on the web (#7140, port of
// #7139, epic #7136). The words are ImageHostsWords' in
// Packages/AgentsKit/Sources/AgentsKitCore/Model/ImageHosts.swift; PageImages is the same model as
// that file's, for one open page: an image shown once stays until the page closes, and the next
// page open blocks it again.
import { signal } from "@preact/signals";
import { Failure, type ForgeImage, type ImageBlocked, type ImageHost, type ImageHosts } from "../protocol/generated";
import { CallFailed } from "../wire/link";
import { describe } from "./errors";
import { keepWork, keptWork } from "./localCopy";
import { keepWork as keepAnswer } from "./workCache";
import { bytesOf } from "./pageImage";
import type { Store } from "./store";

/** The longest host that a button and the line under the host say in full. */
const longest = 40;

/** "This Mac" is a name in a list and words in a sentence. */
const inSentence = (machine: string) => machine === "This Mac" ? "this Mac" : machine;

export const imageHostsWords = {
  title: "Image Hosts",
  show: "Show image",
  blockedChip: "Blocked",
  reading: "Reading…",
  tryAgain: "Try Again",
  remove: "Remove",
  none: "No allowed hosts",
  noneDetail: "Open a Work page with a blocked image and press Always allow.",
  firstRead: "Reading the list…",
  notRemoved: "Not removed. Try again.",
  noMachine: "No machine keeps image hosts yet. Update Agent Loops on a machine to see them.",
} as const;

/** What a button says for a host: the host, or the end of a long one, which says who owns it. */
export function shortHost(host: string): string {
  if (host.length <= longest) return host;
  const end = host.split(".").slice(-3).join(".");
  return "…" + end.slice(-longest);
}

export const allowWords = (host: string) => `Always allow ${shortHost(host)}`;
/** The button's whole words, for its tooltip and a screen reader. */
export const allowFull = (host: string) => `Always allow ${host}`;
export const blockedWords = (b: ImageBlocked) =>
  b.from ? `Image blocked: ${b.from} redirects to ${b.host}` : `Image blocked: ${b.host}`;
export const learnsWords = (host: string, machine: string) =>
  `If you show it, ${host.length > longest ? "this host" : host} learns that ${inSentence(machine)} read it.`;
export const shownOnceWords = (host: string) => `Shown once from ${host}`;
export const failedWords = (why: string) => `The image could not be read: ${why.endsWith(".") ? why : why + "."}`;
export const notAllowedWords = (host: string, machine: string, why: string) => `${host} was not allowed on ${inSentence(machine)}. ${why}`;
export const removeLabel = (host: string) => `Remove ${host}`;
export const footerWords = (machine: string) =>
  `Work pages on this machine load images from these hosts. Each host learns that ${inSentence(machine)} read the image. `
  + "Connected forges’ own image hosts always load and are not listed. Add a host with Always allow on a blocked image.";
export const offlineWords = (machine: string) => `${machine} is offline. Connect it to remove a host.`;
export const couldNotRemove = (host: string, machine: string) => `${host} could not be removed on ${inSentence(machine)}.`;
export const couldNotRead = (machine: string) => `The list on ${inSentence(machine)} could not be read.`;

/** "Allowed today", else "Allowed 3 Nov", with the year when it is not this one. */
export function allowedOn(at: number, now: number = Date.now(), locale?: string): string {
  const d = new Date(at), n = new Date(now);
  if (d.toDateString() === n.toDateString()) return "Allowed today";
  const year = d.getFullYear() !== n.getFullYear();
  return `Allowed ${d.toLocaleDateString(locale, { day: "numeric", month: "short", ...(year ? { year: "numeric" } : {}) })}`;
}

/** A read of one image, as the machine answered it. */
export type PageImageAnswer =
  | { kind: "image"; bytes: Uint8Array; type: string }
  | { kind: "blocked"; blocked: ImageBlocked }
  /** Why, for a person, and whether reading it again can help. */
  | { kind: "failed"; why: string; again: boolean };

/** What a failed forge/image says. */
export function answerOf(error: unknown): PageImageAnswer {
  if (error instanceof CallFailed && error.code === Failure.imageBlocked) {
    const data = error.data as Partial<ImageBlocked> | undefined;
    if (data && typeof data.host === "string") {
      return { kind: "blocked", blocked: { host: data.host, ...(typeof data.from === "string" ? { from: data.from } : {}) } };
    }
  }
  const again = !(error instanceof CallFailed && error.code === Failure.imageNotPublic);
  return { kind: "failed", why: describe(error), again };
}

export type ImagePhase =
  | { kind: "loading" }
  | { kind: "image"; bytes: Uint8Array; type: string }
  /** problem: why Always allow did not work. */
  | { kind: "blocked"; blocked: ImageBlocked; problem: string | null }
  /** Being read since the person pressed a button. */
  | { kind: "reading"; blocked: ImageBlocked }
  /** It failed after the person chose to read it. */
  | { kind: "failed"; why: string; again: boolean }
  /** It failed as it loaded: the alternative text. */
  | { kind: "none" };

interface Entry {
  machine: string;
  phase: ImagePhase;
  /** The hosts the person showed for this image, in order. */
  show: string[];
  /** What the person last chose to read past. */
  pressed: ImageBlocked | null;
  /** Why Always allow under a shown image did not work. */
  problem: string | null;
  /** The latest read, so an older one's answer is not drawn. */
  serial: number;
}

export type ReadImage = (url: string, show: string[], machine: string) => Promise<PageImageAnswer>;
export type AllowHost = (host: string, machine: string) => Promise<void>;

/** A Work page's images with their controls: the page's model, and the machine that read the page. */
export interface ImageControls {
  images: PageImages;
  machine: string;
  machineName: string;
}

/** The remote images of one open Work page, and the person's choices for them. */
export class PageImages {
  /** Each image's entry, by url; a new object on each change, so a page that reads it draws again. */
  readonly entries = signal<ReadonlyMap<string, Entry>>(new Map());
  /** The hosts allowed from this page, by machine: only for the caption of a shown image. */
  private readonly allowed = new Map<string, Set<string>>();
  /** The hosts being allowed now, by machine, so a second press waits for the first. */
  private readonly allowing = new Set<string>();

  constructor(private readonly read: ReadImage, private readonly allowHost: AllowHost) {}

  phase(url: string): ImagePhase {
    return this.entries.value.get(url)?.phase ?? { kind: "loading" };
  }

  /** The host to say under an image that is here for one press: the last one shown and not allowed since. */
  shownOnce(url: string): string | null {
    const entry = this.entries.value.get(url);
    if (!entry || entry.phase.kind !== "image") return null;
    const allowed = this.allowed.get(entry.machine);
    for (let i = entry.show.length - 1; i >= 0; i--) if (!allowed?.has(entry.show[i]!)) return entry.show[i]!;
    return null;
  }

  /** Why Always allow under a shown image did not work. */
  problem(url: string): string | null {
    return this.entries.value.get(url)?.problem ?? null;
  }

  /** The image is on screen: read once from the machine that read the page. */
  load(url: string, machine: string): void {
    const had = this.entries.peek().get(url);
    if (had && (had.phase.kind === "image" || had.phase.kind === "reading" || had.machine === machine)) return;
    this.set(url, { machine, phase: { kind: "loading" }, show: [], pressed: null, problem: null, serial: had?.serial ?? 0 });
    this.start(url);
  }

  /** Show image: this one, this time. One press accepts one host. */
  show(url: string): void {
    const entry = this.entries.peek().get(url);
    if (!entry || entry.phase.kind !== "blocked") return;
    const blocked = entry.phase.blocked;
    this.set(url, { ...entry, show: entry.show.includes(blocked.host) ? entry.show : [...entry.show, blocked.host] });
    this.press(url, blocked);
    this.start(url);
  }

  /** Try Again, after a failure. */
  again(url: string): void {
    const entry = this.entries.peek().get(url);
    if (!entry || entry.phase.kind !== "failed" || !entry.phase.again || !entry.pressed) return;
    this.press(url, entry.pressed);
    this.start(url);
  }

  /**
   * Always allow: the host, and then each of the page's images that names it. At once; changed
   * back, with why, when the machine refuses. It asks the machine every time, also for a host this
   * page allowed before, as the list may have lost it since (#7273).
   */
  async allow(host: string, from: string, machineName: string): Promise<void> {
    const machine = this.entries.peek().get(from)?.machine;
    if (machine === undefined) return;
    const key = `${machine}\n${host}`;
    if (this.allowing.has(key)) return;
    this.allowing.add(key);
    const waiting = [...this.entries.peek()].filter(([, e]) => e.machine === machine && e.phase.kind === "blocked" && e.phase.blocked.host === host)
      .map(([url]) => url);
    for (const url of waiting) {
      const phase = this.entries.peek().get(url)!.phase;
      if (phase.kind === "blocked") this.press(url, phase.blocked);
    }
    const fromEntry = this.entries.peek().get(from);
    if (fromEntry) this.set(from, { ...fromEntry, problem: null });
    const had = this.allowed.get(machine) ?? new Set<string>();
    const added = !had.has(host);
    had.add(host);
    this.allowed.set(machine, had);
    try {
      await this.allowHost(host, machine);
      for (const url of waiting) if (this.entries.peek().get(url)?.machine === machine) this.start(url);
      this.touch();
    } catch (error) {
      if (added) had.delete(host);
      const why = notAllowedWords(host, machineName, describe(error));
      for (const url of waiting) {
        const entry = this.entries.peek().get(url);
        if (!entry || entry.machine !== machine || entry.phase.kind !== "reading") continue;
        this.set(url, { ...entry, phase: { kind: "blocked", blocked: entry.phase.blocked, problem: url === from ? why : null } });
      }
      const shown = this.entries.peek().get(from);
      if (shown && shown.phase.kind === "image") this.set(from, { ...shown, problem: why });
    } finally {
      this.allowing.delete(key);
    }
  }

  private press(url: string, blocked: ImageBlocked): void {
    const entry = this.entries.peek().get(url);
    if (entry) this.set(url, { ...entry, pressed: blocked, phase: { kind: "reading", blocked } });
  }

  /** Not a component's own effect: a picture that goes off screen does not stop a read that the person asked for. */
  private start(url: string): void {
    const entry = this.entries.peek().get(url);
    if (!entry) return;
    const serial = entry.serial + 1;
    this.set(url, { ...entry, serial });
    const { show, machine } = entry;
    void this.read(url, show, machine).then((answer) => {
      const now = this.entries.peek().get(url);
      if (!now || now.serial !== serial || now.machine !== machine) return;
      this.set(url, { ...now, phase: phaseOf(answer, now.pressed !== null) });
    });
  }

  private set(url: string, entry: Entry): void {
    const next = new Map(this.entries.peek());
    next.set(url, entry);
    this.entries.value = next;
  }

  /** The allowed set changed: the captions draw again. */
  private touch(): void {
    this.entries.value = new Map(this.entries.peek());
  }
}

function phaseOf(answer: PageImageAnswer, pressed: boolean): ImagePhase {
  switch (answer.kind) {
    case "image": return { kind: "image", bytes: answer.bytes, type: answer.type };
    case "blocked": return { kind: "blocked", blocked: answer.blocked, problem: null };
    case "failed": return pressed ? { kind: "failed", why: answer.why, again: answer.again } : { kind: "none" };
  }
}

export interface ImageHostsCalls {
  kept: () => Promise<{ value: ImageHosts; at: number } | undefined>;
  list: () => Promise<ImageHosts>;
  remove: (host: string) => Promise<ImageHosts>;
  keep: (list: ImageHosts) => void;
}

/** Settings ▸ Image Hosts' list for one machine, as ImageHostsList in ImageHosts.swift: the kept list first, then the
 * machine's; Remove at once, and back where it was, with why, when the machine refuses. */
export class ImageHostsList {
  /** Null until something was read. */
  readonly hosts = signal<ImageHost[] | null>(null);
  readonly readAt = signal<number | undefined>(undefined);
  readonly reading = signal(false);
  /** The machine did not answer the list. */
  readonly problem = signal<string | null>(null);
  /** The last host that was not removed, and what the machine said. */
  readonly notRemoved = signal<{ host: string; why: string } | null>(null);
  /** Counts the reads and the changes: a list that was asked before a later one is not drawn. */
  private serial = 0;
  private readonly removing = new Set<string>();

  constructor(private readonly calls: ImageHostsCalls) {}

  /** `live` false for a machine that is offline: the kept list only. */
  async load(live: boolean): Promise<void> {
    this.notRemoved.value = null;
    if (this.hosts.peek() === null) {
      const kept = await this.calls.kept();
      if (kept && this.hosts.peek() === null) { this.hosts.value = kept.value.hosts; this.readAt.value = kept.at; }
    }
    if (!live) return;
    const mine = ++this.serial;
    this.reading.value = true;
    try {
      const list = await this.calls.list();
      if (mine === this.serial) this.took(list);
    } catch (error) {
      if (mine === this.serial) this.problem.value = describe(error);
    } finally {
      if (mine === this.serial) this.reading.value = false;
    }
  }

  async remove(host: string): Promise<void> {
    const had = this.hosts.peek();
    const index = had?.findIndex((h) => h.host === host) ?? -1;
    if (!had || index < 0 || this.removing.has(host)) return;
    this.removing.add(host);
    const mine = ++this.serial;
    this.reading.value = false;
    const row = had[index]!;
    this.hosts.value = had.filter((h) => h.host !== host);
    this.notRemoved.value = null;
    try {
      const list = await this.calls.remove(host);
      if (mine === this.serial) this.took(list);
    } catch (error) {
      const back = [...(this.hosts.peek() ?? [])];
      if (!back.some((h) => h.host === host)) back.splice(Math.min(index, back.length), 0, row);
      this.hosts.value = back;
      this.notRemoved.value = { host, why: describe(error) };
    } finally {
      this.removing.delete(host);
    }
  }

  private took(list: ImageHosts): void {
    this.hosts.value = list.hosts.filter((h) => !this.removing.has(h.host));
    this.readAt.value = Date.now();
    this.problem.value = null;
    this.calls.keep(list);
  }
}

/** A Work page's images (#7140): read by the machine that read the page, from this device's copy when it holds one, and
 * kept only when the read went through a forge's own hosts (`keep` absent): Remove must leave no copy here. */
export function newPageImages(store: Store): PageImages {
  return new PageImages(async (url, show, machine) => {
    const kept = await keptWork<ForgeImage>("image", url);
    if (kept) return { kind: "image", bytes: bytesOf(kept.data), type: kept.mimeType };
    try {
      const got = await store.link.call("forge/image", show.length > 0 ? { url, show } : { url }, machine);
      if (got.keep !== false) keepWork("image", url, got);
      return { kind: "image", bytes: bytesOf(got.data), type: got.mimeType };
    } catch (error) {
      return answerOf(error);
    }
  }, async (host, machine) => {
    const list = await store.link.call("imageHosts/allow", { host }, machine);
    keepAnswer("imageHosts", machine, list);
  });
}
