// A session's priority on its row and in its menu (#5849), in the words of SessionPriorityWords.swift,
// which the window and the Remote hold. The row has a chip for high and low only, with the look of the
// Work rows' (#5690): medium and none are the usual, and a chip on every row would say nothing.
import type { Agent, SessionPriority } from "../protocol/generated";
import { labelKind } from "./labels";

export const priorityMenu = "Priority";

const levelWords: Record<SessionPriority, string> = { high: "High", medium: "Medium", low: "Low" };

type Prioritised = Pick<Agent, "priority" | "prioritySet" | "prioritySource" | "labels">;

/** High or Low for the row's chip; null for medium and none. */
export function priorityChip(agent: Prioritised): string | null {
  return agent.priority === "high" || agent.priority === "low" ? levelWords[agent.priority] : null;
}

/** The first label that links the session to an issue, else to a pull request: #5849, ENG-4, P#5895. */
export function issueLabel(agent: Prioritised): string | null {
  const labels = agent.labels ?? [];
  return labels.find((l) => labelKind(l.value) === "issue")?.value
    ?? labels.find((l) => labelKind(l.value) === "pullRequest")?.value ?? null;
}

/** The chip's tooltip and spoken label: "High, from #5849", "Low, from the session that started it". */
export function priorityHelp(agent: Prioritised): string | null {
  const words = priorityChip(agent);
  if (!words) return null;
  switch (agent.prioritySource) {
    case "issue": return `${words}, from ${issueLabel(agent) ?? "its issue"}`;
    case "starter": return `${words}, from the session that started it`;
    case "set": return `${words}, set on this session`;
    default: return words;
  }
}

/** Whether the session has an issue to follow: a label that links one, or a priority read from one. */
export function hasIssue(agent: Prioritised): boolean {
  return agent.prioritySource === "issue" || issueLabel(agent) !== null;
}

/** One item of Priority ▸: a level, or `follow`, which clears the set priority. */
export type PriorityChoice = SessionPriority | "follow";

export const priorityChoices: readonly PriorityChoice[] = ["high", "medium", "low", "follow"];

/** The item's title: High, Medium, Low, then "From issue (High)", "From issue", or "None" with no issue. */
export function priorityTitle(choice: PriorityChoice, agent: Prioritised): string {
  if (choice !== "follow") return levelWords[choice];
  if (!hasIssue(agent)) return "None";
  if (!agent.prioritySet && agent.prioritySource === "issue" && agent.priority) return `From issue (${levelWords[agent.priority]})`;
  return "From issue";
}

/** The item with the check: the priority set on the session, else the one that follows its issue. */
export function priorityChecked(agent: Prioritised): PriorityChoice {
  return agent.prioritySet ?? "follow";
}
