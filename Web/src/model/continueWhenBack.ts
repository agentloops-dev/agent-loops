// A chat paused by a spent allowance that can carry on by itself (#1859): ContinueWhenBackWords
// (Shared/UI/ContinueWhenBackWords.swift), said the same here. Stopped, the line "Its allowance ran
// out" offers "Continue at 07:00" and "Continue in new session" (#5919). Waiting, it says "Continues
// at 07:00" in the chat and on its row, with Cancel, and the line offers nothing. The daemon keeps
// the wait on agent.continueWhenBack.
import type { Agent, RuntimeAllowances } from "../protocol/generated";
import { fromWireDate } from "../protocol/dates";
import { switchTime } from "./switchNote";
import { fields, type ChatTurn } from "./turns";

export const continueCancelLabel = "Cancel";
export const continueHelp = "Sends “continue” in this chat when the allowance is back, or sooner if a turn on this runtime works.";
/** Under the line, beside Continue at (ContinueWhenBackWords.inNewSession, #5919). */
export const continueInNewSession = "Continue in new session";
export const continueInNewSessionHelp = "Starts a new session on Auto that reads this one, then archives this chat.";
/** Under the line, one for each other account of the runtime (AccountWords.continueOn, #7459). */
export function continueOnAccount(title: string): string {
  return `Continue on ${title}`;
}
export const continueOnAccountHelp = "Carries on this chat on that account.";

/** Whether the chat offers the buttons: paused by a spent allowance, and not waiting yet. */
export function offersContinue(agent: Agent): boolean {
  return agent.state === "stopped" && agent.endedReason === "allowanceSpent" && !agent.continueWhenBack;
}

/** The last "Its allowance ran out" line in these turns. A stored turn is read from its outcome. */
export function allowanceOutLine(turns: readonly ChatTurn[]): string | null {
  let found: string | null = null;
  for (const turn of turns) {
    const items = turn.items.length > 0 ? turn.items : turn.storedOutcome ?? [];
    for (const item of items) {
      if (item.kind !== "entry") continue;
      const state = fields(item.entry, "stateChanged");
      if (state?._0 === "stopped" && state.reason === "allowanceSpent") found = item.entry.id;
    }
  }
  return found;
}

/** Whether this turn holds that line, so only it is drawn again when the title changes. */
export function turnHoldsAllowance(turn: ChatTurn, id: string): boolean {
  const items = turn.items.length > 0 ? turn.items : turn.storedOutcome ?? [];
  return items.some((item) => item.kind === "entry" && item.entry.id === id);
}

/** The button: the provider's reset time when the page knows it. */
export function continueButton(resetsAt: Date | null, now = new Date()): string {
  if (!resetsAt || resetsAt <= now) return "Continue when it is back";
  return `Continue at ${switchTime(resetsAt, now)}`;
}

/** "Continues at 07:00", or null for a chat that is not waiting. Also the row's grey line. */
export function continueLine(agent: Agent, now = new Date()): string | null {
  const wait = agent.continueWhenBack;
  if (!wait) return null;
  const time = switchTime(fromWireDate(wait.at), now);
  return wait.resetKnown ? `Continues at ${time}` : `Continues when it is back · tries at ${time}`;
}

/** The workflow page's switch (#1918), writing `continue-when-back:`. */
export const workflowContinueSwitch = "Continue when the allowance is back";

/** What the switch means for this workflow's runs. */
export function workflowContinueSentence(on: boolean): string {
  return on ? "A run whose allowance is spent waits, and carries on when it is back."
    : "A run whose allowance is spent stops until somebody chooses Continue.";
}

/** RuntimeAllowances.resetsAt: when the provider said this runtime's allowance resets. */
export function resetsAt(allowances: RuntimeAllowances | undefined, runtimeID: string): Date | null {
  const row = allowances?.rows.find((r) => r.credentialKey.split(":", 1)[0] === runtimeID && "out" in r.state.status);
  const out = row && "out" in row.state.status ? row.state.status.out : undefined;
  return out?.until !== undefined ? fromWireDate(out.until) : null;
}
