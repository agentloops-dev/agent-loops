// An agent's secret that becomes yours (#5308, specs/5308-agent-secrets-sync): the words of its row in Needs
// you and of the Secrets row it becomes, the same on the Mac window and the Remote (SecretOfferWords.swift).
import type { SecretOffer, SecretSavedBy } from "../protocol/generated";
import { age } from "./secretInfo";

/** Under a secret of yours that an agent saved: "Saved by an agent on devbox, 2 h ago". */
export function savedByLine(by: SecretSavedBy | undefined, now: Date = new Date()): string | null {
  if (!by) return null;
  const at = new Date(by.at);
  return Number.isNaN(at.getTime()) ? `Saved by an agent on ${by.name}` : `Saved by an agent on ${by.name}, ${age(at, now)}`;
}

/** Under an offer's name: where it came from, and what happens next. */
export function offerLine(offer: Pick<SecretOffer, "machineName" | "conflict">): string {
  return offer.conflict
    ? `Saved by an agent on ${offer.machineName}. You have a secret with this name: your other machines keep yours until you choose.`
    : `Saved by an agent on ${offer.machineName}. It goes to your other machines when you next open Agents.`;
}

export const keepYours = "Keep Yours";
export const useTheAgents = "Use the Agent's";
export const offersLabel = "Secrets saved by agents";

/** The offers a person sees in Needs you: every one with a conflict, and one with none that no app took yet. */
export function waitingOffers(offers: readonly SecretOffer[]): SecretOffer[] {
  return [...offers].sort((a, b) => a.savedAt.localeCompare(b.savedAt));
}
