// A chat waiting to continue when its allowance is back (#1859): under its head, when it continues
// and a Cancel, as the window's ContinueWhenBackStrip has them. While it is only stopped, the two
// buttons sit under the line in the conversation (#5919), and this strip is not shown.
import type { Agent } from "../protocol/generated";
import type { Store } from "../model/store";
import { Capability } from "../protocol/capabilities.generated";
import { continueCancelLabel, continueLine } from "../model/continueWhenBack";
import { Strip } from "./Strip";

export function ContinueStrip({ store, host, agent, disabled }: { store: Store; host: string; agent: Agent | undefined; disabled: boolean }) {
  if (!agent || !continueStripApplies(store, host, agent)) return null;
  const line = continueLine(agent);
  if (!line) return null;
  // The allowance's strip (#5034, fourth): grey, as the chat carries on by itself.
  return <Strip tone="neutral" name="continue-strip" icon="history" line={line}
    second={{ label: continueCancelLabel, disabled, run: () => void store.continueWhenBack(host, agent.id, false) }} />;
}

/** Whether the session has an allowance strip: it was asked to continue, and it is not archived. */
export function continueStripApplies(store: Store, host: string, agent: Agent | undefined): boolean {
  return !!agent && agent.state !== "archived" && !!agent.continueWhenBack
    && store.has(host, Capability.chatContinueWhenBack1);
}
