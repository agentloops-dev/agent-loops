// Each Work page opens with the filters, sort and range it had when the person last left it (#5451), kept across
// visits in localStorage, as the window and the Remote keep them in defaults (WorkRemembered.swift). One entry per page,
// in the words of the page's address (`workQuery`), so a change to a filter's words changes both together. Clear on the
// page is the way back to the defaults.
import { fits, putEntry } from "./keyValue";
import { effect } from "@preact/signals";

/** The pages that remember: the lists by kind, then Timeline and Next (#5691). */
export type WorkPage = "issue" | "pr" | "release" | "timeline" | "next";

/** The most bytes of a page's choices (#7253): longer ones are not kept, nor read when an older build kept them. */
export const rememberedBytes = 16 * 1024;

export const rememberedKey = (page: WorkPage) => `agents.work.filters.${page}`;

function storage(): Storage | undefined {
  try {
    return globalThis.localStorage;
  } catch {
    return undefined;
  }
}

/** What the page had when it was last left, in its address's words; undefined when it has nothing kept. */
export function remembered(page: WorkPage, from: Storage | undefined = storage()): string | undefined {
  try {
    const kept = from?.getItem(rememberedKey(page)) ?? undefined;
    return kept !== undefined && fits(kept, rememberedBytes) ? kept : undefined;
  } catch {
    return undefined;
  }
}

/** Keeps what the page has now; the defaults (no words) remove the entry. */
export function remember(page: WorkPage, words: string, to: Storage | undefined = storage()): void {
  if (words) {
    // store-bounds: kind=key-value; count=5; age=until-deleted; action=refuse; record=16KiB; applied=on write
    putEntry(to, rememberedKey(page), words, rememberedBytes);
    return;
  }
  try {
    to?.removeItem(rememberedKey(page));
  } catch {
    // A refused storage only loses the memory, never the page.
  }
}

/** Keeps a page's choices each time one of them changes: `words` reads the signals it is made of. */
export function rememberEach(page: WorkPage, words: () => string): () => void {
  return effect(() => { remember(page, words()); });
}

/** A page's choices as address words, each left out while it is its default. */
export function choiceWords(choices: Readonly<Record<string, string>>, defaults: Readonly<Record<string, string>>): string {
  const q = new URLSearchParams();
  for (const [name, value] of Object.entries(choices)) if (value && value !== defaults[name]) q.set(name, value);
  return q.toString();
}

/** One choice from words made by `choiceWords`; `fallback` when it is not there or not one of `allowed`. */
export function choiceFrom<T extends string>(words: string | undefined, name: string, fallback: T, allowed?: readonly T[]): T {
  const value = new URLSearchParams(words ?? "").get(name);
  if (!value) return fallback;
  return allowed && !allowed.includes(value as T) ? fallback : value as T;
}

