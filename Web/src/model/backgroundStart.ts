// New Session in Background (#5798): the words and the folder it starts in, as the window's BackgroundStartDraft.
import { folderKey } from "./groups";

export const backgroundStartTitle = "New Session in Background";

export type StartPlace = { host: string; folder: string };

export const placeKey = (p: StartPlace) => `${p.host}|${folderKey(p.folder)}`;

/** The folder it starts in at first: the one open (or the open session's), else the first live one; null with none. */
export function backgroundStartFolder(live: readonly StartPlace[], open: { host?: string | undefined; folder?: string | undefined }):
  StartPlace | null {
  if (open.host && open.folder) {
    const want = placeKey({ host: open.host, folder: open.folder });
    const here = live.find((p) => placeKey(p) === want);
    if (here) return here;
  }
  return live[0] ?? null;
}

/** Whether Return in the prompt starts: Shift or Option and Return, or Return while composing text, is a new line. */
export function returnStarts(e: { key: string; shiftKey: boolean; altKey: boolean; isComposing: boolean }): boolean {
  return e.key === "Enter" && !e.shiftKey && !e.altKey && !e.isComposing;
}
