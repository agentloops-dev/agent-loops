// The sessions that work on Work's items (#5453), and the lead of an epic (#3265), of those this page knows, as the
// window's WorkSessions.
import type { Agent, AgentState, LineageNode } from "../protocol/generated";
import { shortRef } from "./work";

const holdsRuntime = (s: AgentState) => s === "starting" || s === "running" || s === "waitingOnUser";

/** The session that leads an epic, of those this page knows: labelled lead and the epic's short ref
 * (#3265, or ENG-5 on Linear), as Start lead labels it; not archived; a live one first, then the newest. */
export function epicLead(epic: LineageNode, agents: readonly Agent[]): Agent | undefined {
  const ref = shortRef(epic.ref);
  let best: Agent | undefined;
  for (const a of agents) {
    const labels = (a.labels ?? []).map((l) => l.value);
    if (a.state === "archived" || !labels.includes("lead") || !labels.includes(ref)) continue;
    const better = !best || (holdsRuntime(a.state) !== holdsRuntime(best.state) ? holdsRuntime(a.state) : a.createdAt > best.createdAt);
    if (better) best = a;
  }
  return best;
}

/** The label a session that works on an item has (#5453): "#12" for an issue (ENG-5 on Linear), "P#12" for a pull
 * request, as set_session_labels and Start Agent label it. */
export const itemLabel = (node: LineageNode) => (node.kind === "pr" ? "P" : "") + shortRef(node.ref);

/** The session that works on a row's item now (#5453), of those this page knows: labelled with the item's label and
 * starting, running or waiting on a person. One that needs you first, then the newest; undefined with none. */
export function workingSession(node: LineageNode, agents: readonly Agent[]): Agent | undefined {
  const label = itemLabel(node);
  let best: Agent | undefined;
  for (const a of agents) {
    if (!holdsRuntime(a.state) || !(a.labels ?? []).some((l) => l.value === label)) continue;
    const asks = (x: Agent) => x.state === "waitingOnUser";
    const better = !best || (asks(a) !== asks(best) ? asks(a) : a.createdAt > best.createdAt);
    if (better) best = a;
  }
  return best;
}

/** A row's session in words (#5453): "Working", or "Needs you" when it waits on a person. */
export const workingWords = (state: AgentState) => state === "waitingOnUser" ? "Needs you" : "Working";
