// New Session's Suggested section (#5457, #5461): the words of a row, and whether the person hid the section.
// The rows come from work/suggest-leads (#5458); a tap fills the form as Start lead does and starts nothing.
import type { WorkLeadSuggestion, WorkSuggestLeads } from "../protocol/generated";

/** The key under which the person's Hide is kept on this device. */
export const suggestedHiddenKey = "agents.newSession.suggestedHidden";

/** Whether the section is hidden; a store that cannot be read is a section shown. */
export function suggestedHidden(storage: Pick<Storage, "getItem"> | undefined): boolean {
  try { return storage?.getItem(suggestedHiddenKey) === "1"; } catch { return false; }
}

export function keepSuggestedHidden(storage: Pick<Storage, "setItem" | "removeItem"> | undefined, hidden: boolean): void {
  try { if (hidden) storage?.setItem(suggestedHiddenKey, "1"); else storage?.removeItem(suggestedHiddenKey); } catch { /* the choice is for this visit only */ }
}

/** "Due 2026-11-20", the milestone's name before it when there is one. */
const dueWords = (s: WorkLeadSuggestion) => s.milestone && s.due ? `${s.milestone}, due ${s.due}` : s.milestone ?? (s.due ? `due ${s.due}` : undefined);

/** A row's second line: the epic's number, its folder, its milestone and its progress. A standing epic has no count. */
export function suggestionDetail(s: WorkLeadSuggestion): string {
  const key = s.node.ref.includes("#") ? `#${s.node.ref.split("#").pop()}` : (s.node.ref.split(":").pop() ?? s.node.ref);
  const parts = [key];
  if (s.folder) parts.push(decodeURIComponent(s.folder.replace(/\/+$/, "").split("/").pop() ?? s.folder));
  const due = dueWords(s);
  if (due) parts.push(due);
  parts.push(s.standing ? "standing" : `${s.done} of ${s.total} done`);
  return parts.join(" · ");
}

/** "3 more in Work", or none when there are no more. */
export const moreWords = (more: number): string | undefined => more > 0 ? `${more} more in Work` : undefined;

/** The one line for a list with no rows because a forge could not be read (#5790), or none. */
export function unreadWords(list: WorkSuggestLeads): string | undefined {
  const first = list.suggestions.length === 0 ? list.errors?.[0] : undefined;
  return first ? `Could not read: ${first.message}` : undefined;
}
