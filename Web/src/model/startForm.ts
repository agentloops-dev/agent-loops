// The new session's form as it was left (#257): its runtime, the choices made on it and the
// folders it may also reach, kept across starts and a reload, as the window's DraftKeeper keeps
// its start form. The runtime and choices follow the person to every client (#1033). One form, as the window has one; a runtime that is gone takes its choices
// with it, since they were that runtime's.
import type { JSONValue, Levels, ModelLevel, PermissionLevel } from "../protocol/generated";
import { preferenceKeys, preferences, setPreferences } from "./preferences";

export interface StartForm {
  runtimeID?: string;
  chosen: Record<string, JSONValue>;
  folders: string[];
}

interface Storage { getItem(key: string): string | null; setItem(key: string, value: string): void }

/** The most folders a form keeps, and the longest path, in bytes (#6805), as the Remote's StartFolders. */
export const foldersMost = 100;
export const folderPathMost = 1024;

/** What to say when a folder is refused; null when it can be added. A folder is the person's choice, so none goes
 *  to make room. */
export function folderRefusal(folders: readonly string[], url: string): string | null {
  if (!fits(url)) return `This path is too long. A path has at most ${folderPathMost} bytes.`;
  return folders.length < foldersMost ? null : `At most ${foldersMost} folders can be added. Remove one first.`;
}

/** A folder URL whose path is short enough. One that is not a URL this page made is measured as it is. */
function fits(url: string): boolean {
  let path = url;
  try { path = decodeURI(url.replace(/^file:\/\//, "")); } catch { /* as it is */ }
  return new TextEncoder().encode(path).length <= folderPathMost + 1; // The URL ends with a slash.
}

/** The folders are this browser's (they are paths on one host), with no age, on purpose, until
 *  each is removed or Forget (#6805); the runtime and its choices are
 *  the person's, kept on their Mac with the other display choices (#1033). */
const storageKey = "agents.startForm";

function sharedChoices(): Record<string, JSONValue> {
  const all = preferences.peek()[preferenceKeys.startChoices];
  return all && typeof all === "object" && !Array.isArray(all) ? all : {};
}

export function keptForm(storage: Storage | undefined): StartForm {
  let folders: string[] = [];
  try {
    const kept = JSON.parse(storage?.getItem(storageKey) ?? "null") as Partial<StartForm> | null;
    folders = Array.isArray(kept?.folders) ? kept.folders.filter((f): f is string => typeof f === "string" && fits(f)).slice(0, foldersMost) : [];
  } catch {
    // Unreadable: no folders.
  }
  const runtimeID = preferences.peek()[preferenceKeys.startRuntimeID];
  if (typeof runtimeID !== "string") return { chosen: {}, folders };
  const chosen = sharedChoices()[runtimeID];
  return { runtimeID, chosen: chosen && typeof chosen === "object" && !Array.isArray(chosen) ? chosen : {}, folders };
}

export function keepForm(storage: Storage | undefined, form: StartForm): void {
  try { storage?.setItem(storageKey, JSON.stringify({ folders: form.folders })); } catch { /* full: not kept */ }
  if (!form.runtimeID) return;
  const said = preferences.peek();
  const changes: Record<string, JSONValue> = {};
  if (said[preferenceKeys.startRuntimeID] !== form.runtimeID) changes[preferenceKeys.startRuntimeID] = form.runtimeID;
  const choices = sharedChoices();
  if (Object.keys(form.chosen).length > 0 && JSON.stringify(choices[form.runtimeID]) !== JSON.stringify(form.chosen)) {
    changes[preferenceKeys.startChoices] = { ...choices, [form.runtimeID]: form.chosen };
  }
  if (Object.keys(changes).length > 0) setPreferences(changes);
}

/** Forget: the folders were that control plane's machines' (#6805). */
export function forgetForm(storage: { removeItem(key: string): void } | undefined): void {
  try { storage?.removeItem(storageKey); } catch { /* storage off: nothing kept */ }
}

/** The form's runtime when it can still start, the choices made on it only then. */
export function formFor(form: StartForm, startable: readonly string[]): StartForm {
  if (form.runtimeID && startable.includes(form.runtimeID)) return form;
  const { runtimeID: _gone, ...rest } = form;
  return { ...rest, chosen: {} };
}

const modelLevels: readonly string[] = ["fast", "balanced", "smart"] satisfies ModelLevel[];
const permissionLevels: readonly string[] = ["plan", "ask", "approveForMe", "bypassPermissions"] satisfies PermissionLevel[];

/** The model and permission levels last chosen on any client's start form (#3798), so a new
 *  session does not fall back to Ask; a level not kept, or not known, is Balanced or Ask. */
export function keptLevels(): Levels {
  const kept = preferences.peek()[preferenceKeys.startLevels];
  const said = kept && typeof kept === "object" && !Array.isArray(kept) ? kept : {};
  const model = typeof said.model === "string" && modelLevels.includes(said.model) ? said.model as ModelLevel : "balanced";
  const permission = typeof said.permission === "string" && permissionLevels.includes(said.permission)
    ? said.permission as PermissionLevel : "ask";
  return { model, permission };
}

/** Keep levels' model and permission for the next session, on every client (#3798). */
export function keepLevels(levels: Levels): void {
  const kept: Record<string, JSONValue> = {};
  if (levels.model) kept.model = levels.model;
  if (levels.permission) kept.permission = levels.permission;
  if (JSON.stringify(preferences.peek()[preferenceKeys.startLevels]) === JSON.stringify(kept)) return;
  setPreferences({ [preferenceKeys.startLevels]: kept });
}
