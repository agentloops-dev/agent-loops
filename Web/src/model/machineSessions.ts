// One count of a machine's sessions (#5488, specs/machine-sessions), as `MachineSessions` in
// AgentsKitCore: the sidebar line ("N running") and the machine's page ("N sessions · M running",
// the groups) both read it, so the two never disagree. A stopped or offline machine's list is the
// last one it sent, so it counts as last known.
import type { Agent } from "../protocol/generated";
import { groupOf } from "./groups";

export type MachineSessionGroup = "working" | "waiting" | "done";

export interface MachineSessions {
  /** Every non-archived session of the machine, newest activity first, by group. */
  groups: Record<MachineSessionGroup, Agent[]>;
  /** Sessions that are not archived. */
  total: number;
  /** Sessions with status Working. */
  running: number;
}

export const machineSessionGroups: readonly MachineSessionGroup[] = ["working", "waiting", "done"];
export const machineSessionTitles: Record<MachineSessionGroup, string> = { working: "Working", waiting: "Waiting", done: "Done" };

/** Working is running; Waiting is what needs the person or waits for a free place or an event; Done is finished or paused. */
export function machineSessionGroup(agent: Agent): MachineSessionGroup | null {
  switch (groupOf(agent)) {
    case "running": return "working";
    case "needsAttention": case "blocked": case "waiting": return "waiting";
    case "finished": case "stopped": return "done";
    default: return null;
  }
}

export function machineSessions(agents: readonly Agent[]): MachineSessions {
  const groups: Record<MachineSessionGroup, Agent[]> = { working: [], waiting: [], done: [] };
  for (const agent of agents) {
    const group = machineSessionGroup(agent);
    if (group) groups[group].push(agent);
  }
  for (const list of Object.values(groups)) list.sort((a, b) => (a.lastActivityAt < b.lastActivityAt ? 1 : a.lastActivityAt > b.lastActivityAt ? -1 : 0));
  return { groups, total: groups.working.length + groups.waiting.length + groups.done.length, running: groups.working.length };
}

/**
 * A machine's working and waiting counts (#6158), as `MachineSessionCounts` in AgentsKitCore (#6157):
 * what the sidebar and the Machines page's Working column read, with no group or sort. The store
 * keeps one for each busy machine and sets it only when a count moves.
 */
export interface MachineSessionCounts {
  working: number;
  waiting: number;
}

/** The counts of one machine's sessions: one pass, no sort. */
export function machineSessionCounts(agents: readonly Agent[]): MachineSessionCounts {
  let working = 0;
  let waiting = 0;
  for (const agent of agents) {
    const group = machineSessionGroup(agent);
    if (group === "working") working++;
    else if (group === "waiting") waiting++;
  }
  return { working, waiting };
}

/** Sessions work or wait there. */
export const isBusy = (counts: MachineSessionCounts | undefined): boolean => !!counts && counts.working + counts.waiting > 0;

/** The sidebar's line under an online machine: "3 running", or "Idle". */
export function runningLineOf(counts: { running: number }): string {
  return counts.running > 0 ? `${counts.running} running` : "Idle";
}

/** The page's header: "7 sessions · 2 running". */
export function sessionsHeader(counts: MachineSessions): string {
  return `${counts.total} ${counts.total === 1 ? "session" : "sessions"} · ${counts.running} running`;
}
