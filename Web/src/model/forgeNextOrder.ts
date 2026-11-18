// The one order of forge/next on the Work pages (#5689, epic #6086), as ForgeNextOrder.swift, and the On hold chip's
// words (#6090), as WorkPlan.onHoldWords: the same on every client.
import type { ForgeItem, ForgeLabel } from "../protocol/generated";

/** Its place in the order of forge/next, the lowest first: not on hold before on hold, then High, Medium, none, Low.
 * A list breaks a tie by its own order. */
export function nextRank(item: { priority?: ForgeItem["priority"] | undefined; onHold?: boolean | undefined }): number {
  const priority = item.priority === "high" ? 0 : item.priority === "medium" ? 1 : item.priority === "low" ? 3 : 2;
  return (item.onHold ? 4 : 0) + priority;
}

/** The On hold chip's words and its tooltip (#6090). */
export const onHoldWords = "On hold";
export const onHoldHelp = "On hold: not picked up next";

/** The label on hold, as the forges spell it: on hold, on-hold, on_hold, in any case. */
export function isHoldLabel(name: string): boolean {
  return name.toLowerCase().replace(/[-_]/g, " ").trim() === "on hold";
}

/** The label chips of a row: one with the On hold chip does not draw the on hold label again (#6090). */
export function shownLabels(labels: readonly ForgeLabel[], onHold: boolean | undefined): readonly ForgeLabel[] {
  return onHold ? labels.filter((l) => !isHoldLabel(l.name)) : labels;
}

// The item page's Priority section (#6092, #6093), as WorkList's on the window.

/** The Priority menu's choices, in its order: forge/priority's values, undefined for none. */
export const priorityChoices: readonly (ForgeItem["priority"] | undefined)[] = ["high", "medium", undefined, "low"];

/** A priority in the menu's words: High, Medium, Low, or None. */
export function priorityChoiceWords(priority: ForgeItem["priority"] | undefined): string {
  return priority === "high" ? "High" : priority === "medium" ? "Medium" : priority === "low" ? "Low" : "None";
}

/** The Labels section's labels: not priority:high, priority:medium, priority:low or on hold (Jira's on-hold), in any
 * case, which the Priority section shows instead. */
export function sideLabels(labels: readonly ForgeLabel[]): ForgeLabel[] {
  return labels.filter((l) => {
    const name = l.name.toLowerCase();
    return !["priority:high", "priority:medium", "priority:low", "on hold", "on-hold"].includes(name);
  });
}

/** What the page offers on an item's priority and on hold: where its actions say the forge sets it (#6091) and a
 * machine of the person's writes. */
export function setsPriority(actions: readonly string[] | undefined, writes: boolean): { priority: boolean; hold: boolean } {
  return { priority: writes && (actions ?? []).includes("priority"), hold: writes && (actions ?? []).includes("hold") };
}

/** What a forge says it sets, from a list's ForgeCapabilities (#6091). */
export interface PriorityCaps { forge: string; priority?: boolean | undefined; hold?: boolean | undefined }

/** What a Work ▸ Issues row's right-click menu offers (epic #6086), as WorkList.rowSetsPriority on the window: an issue's
 * priority and on hold where the forge its ref names sets them (its list's capabilities, the same rule as the item page's
 * actions), and its machine writes. */
export function rowSetsPriority(row: { node: { kind: string; ref: string } }, forges: readonly PriorityCaps[] | undefined,
  writes: boolean): { priority: boolean; hold: boolean } {
  const forge = row.node.ref.slice(0, Math.max(0, row.node.ref.indexOf(":")));
  const caps = writes && row.node.kind === "issue" ? forges?.find((f) => f.forge === forge) : undefined;
  return { priority: caps?.priority === true, hold: caps?.hold === true };
}

/** Each forge's capabilities of a list and its next page, once each (the earlier page's first): a page can name a forge the
 * first did not; undefined when neither says any (an earlier daemon). */
export function joinForges<T extends PriorityCaps>(had: readonly T[] | undefined, page: readonly T[] | undefined): T[] | undefined {
  if (!had && !page) return undefined;
  const out = [...had ?? []];
  for (const caps of page ?? []) if (!out.some((f) => f.forge === caps.forge)) out.push(caps);
  return out;
}

/** The title of the alert when a row menu's Priority or On hold write failed (epic #6086). */
export const couldNotSetPriority = "Could not change the priority";

/** The Priority section's words. */
export const priorityTitle = "Priority";
export const priorityHelp = "Set the priority";
export const holdToggleWords = "On hold";
export const holdToggleHelp = "An item on hold is not picked up next: it waits for a person";
export const holdLine = "Not picked up next";
