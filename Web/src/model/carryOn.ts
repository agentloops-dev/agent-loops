// "Carry on" in the prompt bar of a chat that an error stopped: the bar gets the words, not a send,
// one time for each stop. The window's and the Remote's copy is CarryOn.swift in AgentsKit.
import type { Agent, EndedReason } from "../protocol/generated";

/** The words the bar gets. */
export const carryOnWords = "Carry on";

/** The errors that another turn can get past; not a chosen stop, a limit, or an ending with its own way on. */
const errors: ReadonlySet<EndedReason> = new Set<EndedReason>([
  "maxTokens", "maxTurnRequests", "processDied", "unrecognised", "runtimeError", "rateLimited",
]);

/** Whether this ending is an error that "Carry on" can get past. */
export function carryOnFollows(reason: EndedReason | undefined): boolean {
  return reason !== undefined && errors.has(reason);
}

/** The name of the agent's stop when it is such an error, else undefined: the same agent stopped again is a new stop. */
export function carryOnStop(agent: Pick<Agent, "id" | "state" | "endedReason" | "lastActivityAt">): string | undefined {
  return agent.state === "stopped" && carryOnFollows(agent.endedReason) ? `${agent.id}|${agent.lastActivityAt}` : undefined;
}

/** The stops whose bar has had the words, for as long as the page is open. */
export class CarryOnOffers {
  private readonly given = new Set<string>();

  /** "Carry on" for a bar that holds `typed` on this stop, when its bar has not had it; else null. Never over what is typed. */
  words(stop: string | undefined, typed: string): string | null {
    if (!stop || typed.trim() !== "" || this.given.has(stop)) return null;
    this.given.add(stop);
    return carryOnWords;
  }
}

export const carryOnOffers = new CarryOnOffers();
