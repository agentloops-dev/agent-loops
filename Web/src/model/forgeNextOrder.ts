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

/** The Priority section's words. */
export const priorityTitle = "Priority";
export const priorityHelp = "Set the priority";
export const holdToggleWords = "On hold";
export const holdToggleHelp = "An item on hold is not picked up next: it waits for a person";
export const holdLine = "Not picked up next";
