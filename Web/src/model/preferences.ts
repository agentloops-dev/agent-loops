// The person's display choices (#1033), kept once on their Mac's host in
// ~/.agents/settings/display.json, so the window, the Remote and this page show the same. The
// page keeps no copy: the store fills `preferences` from the Mac's `preferences/get` and each
// `preferences/changed`, and a choice made here is sent with `preferences/set`. The keys are
// DisplayPreference's.
import { signal } from "@preact/signals";
import type { JSONValue } from "../protocol/generated";

export const preferenceKeys = {
  smartGroupsOff: "sidebarSmartGroupsOff",
  showsArchivedFolders: "showsArchivedFolders",
  showsActivity: "showsActivity",
  showsUsage: "showsUsage",
  personasMarketplace: "catalog.personas.marketplace",
  reviewDiffLayout: "reviewDiffLayout",
  startRuntimeID: "startRuntimeID",
  startChoices: "startChoices",
  startLevels: "startLevels",
  agentsMayArchiveMyChats: "agentsMayArchiveMyChats",
  noticeBurst: "noticeBurst",
  noticeCooldownMinutes: "noticeCooldownMinutes",
} as const;

/** The Mac's map, as last heard, with this page's changes still on their way drawn over it. */
export const preferences = signal<Record<string, JSONValue>>({});

/** Answers the Mac's whole map; one that answers nothing (a test's) leaves the change as made. */
type Sender = (changes: Record<string, JSONValue | null>) => Promise<Record<string, JSONValue>> | unknown;

/** Whether the Mac has said its map since the page opened. */
export const heardOnce = signal(false);

let sender: Sender | null = null;
let failed: ((error: unknown) => void) | null = null;
/** The Mac's own map, as last heard: what goes back when a change did not go (#4814). */
let heard: Record<string, JSONValue> = {};
/** Each key changed here and not yet answered, with the change that set it last (#4814). */
const pending = new Map<string, { value: JSONValue | null; change: number }>();
let changes = 0;

/**
 * The store says where a change goes, the Mac's host once connected, and who says a change that
 * did not go.
 */
export function sendPreferencesWith(send: Sender | null, onFailure?: (error: unknown) => void): void {
  sender = send;
  failed = onFailure ?? null;
}

function redraw(): void {
  const next = { ...heard };
  for (const [key, { value }] of pending) {
    if (value === null) delete next[key];
    else next[key] = value;
  }
  preferences.value = next;
}

/** What the Mac said. A change of this page's still on its way stays drawn over it. */
export function preferencesSaid(said: Record<string, JSONValue>): void {
  heard = said;
  heardOnce.value = true;
  redraw();
  moveOnce(globalThis.window?.localStorage);
}

/** The choices that were this browser's own until #6803 and #6806, and where each was kept, the first one first. */
const movedFrom: [string, string[]][] = [
  [preferenceKeys.personasMarketplace, ["catalog.personas.marketplace"]],
  [preferenceKeys.reviewDiffLayout, ["reviewDiffLayout", "agents.reviewDiffLayout"]],
];

/**
 * Each of them goes to the Mac once, where the Mac has none. The old value goes when the Mac has the
 * choice, so one the Mac did not take is sent again the next time it is heard.
 */
export function moveOnce(storage: Pick<Storage, "getItem" | "removeItem"> | undefined): void {
  if (!storage) return;
  try {
    for (const [key, old] of movedFrom) {
      if (key in heard) { for (const name of old) storage.removeItem(name); continue; }
      const kept = old.map((name) => storage.getItem(name)).find((value) => value !== null);
      if (typeof kept === "string" && !pending.has(key)) setPreferences({ [key]: kept });
    }
  } catch { /* storage off: nothing kept */ }
}

/**
 * Set some of them: drawn at once, and sent. A null removes the key, so its default shows. Kept
 * drawn until the Mac answers; put back, and said, when it did not go (#4814).
 */
export function setPreferences(set: Record<string, JSONValue | null>): void {
  if (!sender) {
    const next = { ...heard };
    for (const [key, value] of Object.entries(set)) {
      if (value === null) delete next[key];
      else next[key] = value;
    }
    heard = next;
    redraw();
    return;
  }
  const change = ++changes;
  for (const [key, value] of Object.entries(set)) pending.set(key, { value, change });
  redraw();
  // Only this change's keys: a later change of the same key is still on its way.
  const settle = () => {
    for (const key of Object.keys(set)) if (pending.get(key)?.change === change) pending.delete(key);
  };
  // Sent now, in this turn, so the Mac hears changes in the order they were made.
  let going: unknown;
  try {
    going = sender(set);
  } catch (error) {
    going = Promise.reject(error);
  }
  // A sender with no promise (a test's) has answered: the change is the Mac's at once.
  if (!(going instanceof Promise)) {
    settle();
    preferencesSaid(applied(heard, set));
    return;
  }
  (going as Promise<Record<string, JSONValue> | undefined>).then(
    (said) => {
      settle();
      if (said && typeof said === "object") preferencesSaid(said);
      else preferencesSaid(applied(heard, set));
    },
    (error: unknown) => { settle(); redraw(); failed?.(error); },
  );
}

export function stringPreference(key: string): string | undefined {
  const value = preferences.value[key];
  return typeof value === "string" ? value : undefined;
}

export function numberPreference(key: string): number | undefined {
  const value = preferences.value[key];
  return typeof value === "number" ? value : undefined;
}

export function boolPreference(key: string): boolean | undefined {
  const value = preferences.value[key];
  return typeof value === "boolean" ? value : undefined;
}

/** A map with some changes made: a null removes the key. */
function applied(map: Record<string, JSONValue>, set: Record<string, JSONValue | null>): Record<string, JSONValue> {
  const next = { ...map };
  for (const [key, value] of Object.entries(set)) {
    if (value === null) delete next[key];
    else next[key] = value;
  }
  return next;
}
