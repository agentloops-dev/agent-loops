// New Session's Auto machine (#5375, specs/5373-auto-machine): Auto is the first row of On and is
// selected at every start; a machine named by hand starts there and is not remembered. The control
// plane makes the pick and its words (sessions/previewStart); this file only keeps what the
// form needs of them.
import { signal } from "@preact/signals";
import type { AutoMachinePick } from "../protocol/generated";

/** The folder (`host|folder`) the person named by hand; Auto is on again for every other. */
export const namedMachine = signal<string | undefined>(undefined);

/** Whether Auto is the choice for the form at `key`: it is, unless this machine was named by hand. */
export function autoIsChosen(key: string, pick: AutoMachinePick | null): boolean {
  return namedMachine.value !== key && autoOffered(pick) && autoUsable(pick);
}

/** Whether the row is drawn: a project with one machine has no Auto row and no choice. */
export function autoOffered(pick: AutoMachinePick | null): boolean {
  return pick !== null && pick.single !== true;
}

/** Whether Auto can be chosen: greyed when no machine fits. */
export function autoUsable(pick: AutoMachinePick | null): boolean {
  return pick !== null && pick.single !== true && pick.none === undefined && pick.machine !== undefined;
}

/** The Auto row's words. */
export function autoRowWords(pick: AutoMachinePick | null): string {
  return pick && autoUsable(pick) && pick.machineName ? `Auto · ${pick.machineName}` : "Auto";
}

/** The line under the form when Auto is greyed: why no machine fits, in the control plane's words. */
export function autoOffWords(pick: AutoMachinePick | null): string | undefined {
  return pick && pick.none !== undefined ? pick.text : undefined;
}
