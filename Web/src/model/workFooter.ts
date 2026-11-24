// The Work footer's load states (#5971, epic #6348; #5967's rule), as AgentsKitCore's WorkLook.FooterState says them
// for the Mac window and the Remote (#5968, #5969, #5970): what a Work page's body draws, and the left of the bar under
// it (Loading…, Updating…, From this device, Reading 2 of 3 machines…, or what failed). The right of the bar is the
// count, then "Last update <time ago>" (#5139) once a read has ended.

/** The footer while a read runs and the page has nothing to draw: placeholders above it. */
export const loadingWords = "Loading…";
/** The footer while a read runs again (Refresh, a filter, the poll): the rows on screen stay. */
export const updatingWords = "Updating…";
/** A page drawn from this device's copy (#4930) while its machine is asked again. */
export const fromDeviceWords = "From this device. Reading it again.";
/** The footer under "Could not read issues" in the middle: a read failed and nothing is shown. */
export const couldNotReadShort = "Could not read.";
/** A release's page with no machine that keeps lineage (#5970). */
export const noLineage = "No machine keeps lineage.";

/** What a Work list's body draws: placeholders while a read runs with nothing to draw (never an empty state then), what
 * failed in the middle when nothing was read, the empty state once every read ended, else the rows. */
export type ListBody = "placeholders" | "failed" | "empty" | "rows";

/** The body for a list: `drawn` is false before its first answer (or a copy) is on screen; `read` the rows kept, `shown`
 * those the filters leave; `failed` whether a forge or a machine said something. */
export function listBody(drawn: boolean, read: number, shown: number, reading: boolean, failed: boolean): ListBody {
  if (shown > 0) return "rows";
  if (!drawn || reading) return "placeholders";
  return read === 0 && failed ? "failed" : "empty";
}

/** The left of the Work footer: what runs or what failed. */
export type FooterState =
  | { kind: "none" }
  | { kind: "loading" }
  | { kind: "updating" }
  | { kind: "fromDevice" }
  /** A page that asks several machines (Timeline): "Reading 2 of 3 machines…" while some have answered. */
  | { kind: "readingMachines"; answered: number; of: number }
  /** A read failed with something on screen: "Could not update: GitHub said 502." with Try again; `help` has every line. */
  | { kind: "couldNotUpdate"; line: string; help: string }
  /** A read failed with nothing on screen. */
  | { kind: "couldNotRead" }
  /** A state the page cannot read past, with the warning glyph and no Try again: "No machine keeps lineage." */
  | { kind: "notice"; words: string };

export function footerWords(s: FooterState): string {
  switch (s.kind) {
    case "none": return "";
    case "loading": return loadingWords;
    case "updating": return updatingWords;
    case "fromDevice": return fromDeviceWords;
    case "readingMachines": return `Reading ${s.answered} of ${s.of} machines…`;
    case "couldNotUpdate": return s.line;
    case "couldNotRead": return couldNotReadShort;
    case "notice": return s.words;
  }
}

/** A spinner beside the words. */
export const footerBusy = (s: FooterState) => s.kind === "loading" || s.kind === "updating" || s.kind === "fromDevice" || s.kind === "readingMachines";
/** The warning glyph, and Try again where there is something on screen. */
export const footerFailure = (s: FooterState) => s.kind === "couldNotUpdate" || s.kind === "couldNotRead" || s.kind === "notice";
/** The right of the bar (the count and the age) shows once something is read. */
export const footerShowsRight = (s: FooterState) => s.kind !== "couldNotRead" && s.kind !== "loading";

/** The footer's state for a list: `lines` are what the forges or machines said, one each. */
export function footerState(drawn: boolean, read: number, reading: boolean, fromDevice: boolean, lines: readonly string[]): FooterState {
  if (lines.length > 0) {
    if (read === 0) return reading ? { kind: "loading" } : { kind: "couldNotRead" };
    const line = lines.length === 1 ? `Could not update: ${lines[0]}` : `${lines.length} forges could not update`;
    return { kind: "couldNotUpdate", line, help: lines.join("\n") };
  }
  if (fromDevice) return { kind: "fromDevice" };
  if (!reading) return { kind: "none" };
  return drawn && read > 0 ? { kind: "updating" } : { kind: "loading" };
}

/** The footer's state for a page that is not a list (Deploys, Timeline, an item's page): `drawn` whether anything is on
 * screen; `answered` of `machines` the machines that answered the read that runs. */
export function pageFooterState(drawn: boolean, reading: boolean, fromDevice: boolean, lines: readonly string[],
  answered = 0, machines = 0): FooterState {
  const state = footerState(drawn, drawn ? 1 : 0, reading, fromDevice, lines);
  if (!reading || lines.length > 0 || machines <= 1 || answered <= 0 || answered >= machines) return state;
  return { kind: "readingMachines", answered, of: machines };
}

/** When a page of several reads was read (#5139): the oldest part's, once every part has been read. */
export function oldestRead(reads: readonly (number | undefined)[]): number | undefined {
  if (reads.some((r) => r === undefined)) return undefined;
  const all = reads as number[];
  return all.length === 0 ? undefined : Math.min(...all);
}

/** "4 targets", "1 epic": a page's count at the right of its footer. */
export const pageCount = (n: number, noun: string) => `${n} ${noun}${n === 1 ? "" : "s"}`;
