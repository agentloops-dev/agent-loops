// The user scope's words (#1760, #2157), as the window and the Remote have them (UserScopeWords
// in Packages/AgentsKit): the secrets and settings the control plane keeps for the person and
// sends to each machine of theirs. Settings ▸ You ▸ Secrets (#3632).
import type { SecretsLeft, UserSecretRow } from "../protocol/generated";
import { namesWords } from "./machineNames";

// Reveal and Copy (#5112, #5115; specs/5112-view-copy-secrets-accounts/wireframes.md), as UserScopeWords.
export type CardPart = "number" | "expiry" | "cvc" | "name" | "zip";
export const cardParts: CardPart[] = ["number", "expiry", "cvc", "name", "zip"];
/** How long a value shows, how long until the clipboard is cleared, and how long Copy says Copied, in seconds. */
export const shownSeconds = 30;
export const clipboardSeconds = 90;
export const copiedSeconds = 2;

/** Under a shown value, with the seconds left. */
export function shownFor(left: number, card: boolean): string {
  return `Shown for ${shownSeconds} s (${Math.max(left, 0)} s left). `
    + (card ? "Each reveal and copy is in the audit log." : "Your reveal is in the audit log.");
}

/** Under a row just copied: the whole value, or one part of a card. */
export function copiedLine(part: CardPart | null = null): string {
  return (part ? `Copied ${partWords[part]}.` : "Copied.") + ` The clipboard clears in ${clipboardSeconds} s.`;
}

export const noCopyYet = "This browser has no copy yet. It gets one when another app of yours is online.";
export const didNotOpen = "This browser's copy did not open. Replace the secret to seal it again.";
export const copyRefused = "This browser did not let the page copy.";
export const revealNeedsNewerPlane = "This control plane cannot show or copy a secret yet. Update it.";

export const partLabels: Record<CardPart, string> = {
  number: "Card number", expiry: "Expiry", cvc: "CVC", name: "Name on card", zip: "Billing ZIP",
};
const partWords: Record<CardPart, string> = {
  number: "the card number", expiry: "the expiry", cvc: "the CVC", name: "the name on card", zip: "the billing ZIP",
};
export const thisMacOnly = "This Mac's";

/** Under thisMacOnly at the top of the ~/.agents editors (#2766, #2704): what goes to the other machines. A
 * machine does not send its ~/.agents files (#2703); the Mac's Settings window sets these parts at the user scope. */
export const agentConfigurationFollows = "Your other machines get this Mac's instructions, personas, "
  + "skills and MCP servers from the Mac's Settings window. Workflows, plugins and a skill's other files stay on this Mac.";

export const nameHint = "Capital letters, digits and _, such as GITHUB_TOKEN.";

// A secret's description (#4167): not secret, and kept beside the name. As UserScopeWords.
export const descriptionLabel = "Description";
export const descriptionPrompt = "What it is for: which account, which scopes, whose";
export const descriptionHint = "Agents read the description to choose the right secret. It is not sealed: never put a value in it.";
export const noDescription = "No description";

/** The description under a secret's name, or No description. */
export function about(description: string | undefined): string {
  const text = (description ?? "").trim();
  return text || noDescription;
}

/** What a sheet sends for its description: nothing when it is empty. */
export function describedAs(typed: string): { description?: string } {
  const text = typed.trim();
  return text ? { description: text } : {};
}

/** A secret's row: where it is. */
export function machines(row: Pick<UserSecretRow, "machines">): string {
  const n = row.machines.length;
  return n === 0 ? "On no machine yet" : n === 1 ? "On 1 machine" : `On ${n} machines`;
}

/** The version a secret is set at (#3397): the kept one plus one; for a new name, the time in ms, above a removed one's. */
export function nextVersion(row: Pick<UserSecretRow, "version"> | null, now: number): number {
  return row?.version !== undefined ? row.version + 1 : now;
}

/** Under a secret that was on a machine since removed (FR-026). */
export function leftOn(row: Pick<UserSecretRow, "leftOn">): string | null {
  const left = row.leftOn ?? [];
  if (left.length === 0) return null;
  return `It was on ${namesWords(left.map((l) => l.name))}, which was removed. Change it where it was issued, then replace it here.`;
}

/** When a machine of yours went with secrets on it (control/secretsLeft). */
export function secretsLeft(left: SecretsLeft): string {
  const one = left.secrets.length === 1;
  return `${left.name} was removed with ${one ? "this secret" : "these secrets"} on it: ${left.secrets.join(", ")}. `
    + `Change ${one ? "it" : "them"} where ${one ? "it was" : "they were"} issued.`;
}

/** A secret's name as the person types it. */
export function isSecretName(name: string): boolean {
  return /^[A-Z_][A-Z0-9_]{0,63}$/.test(name);
}

// Card secrets (#3531, #3923), as CardSecret in Packages/AgentsKit: a payment card that has a spend limit
// (a merchant or category lock is recommended), kept as one secret whose value is a JSON object of its parts. agentsd's fill_secret
// reads it a part at a time.
export const cardKind = "card";
export const cardRule = "Add only a card that has a spend limit, such as a Privacy.com card. A card locked to one merchant or category is safer. "
  + "An agent fills it into a page with fill_secret and never sees it. It stays sealed on your own machines.";
export const cardConfirm = "This card has a spend limit";

/** The billing address's parts (#5569): each optional, and left out of the value when empty. */
export const addressParts = ["line1", "line2", "city", "state", "country"] as const;
export type AddressPart = (typeof addressParts)[number];
export type Card = { number: string; expiry: string; cvc: string; name: string; zip: string } & Partial<Record<AddressPart, string>>;

/** The card sheet's optional billing address (#5569), the window's words. */
export const cardAddressHeader = "Billing address";
export const cardAddressHint = "Optional. For a sign-up that asks for a billing address. Agents fill each line and never see it.";

/** The address parts that are set, trimmed. */
function address(typed: Partial<Record<AddressPart, unknown>>): Partial<Record<AddressPart, string>> {
  const out: Partial<Record<AddressPart, string>> = {};
  for (const part of addressParts) {
    const text = typeof typed[part] === "string" ? (typed[part] as string).trim() : "";
    if (text) out[part] = text;
  }
  return out;
}

/** The expiry as MM/YY, from 9/2031, 09/31 or 0931. */
export function tidyExpiry(typed: string): string {
  const parts = typed.split(/\D+/).filter(Boolean);
  if (parts.length === 2) {
    const [month, year] = parts as [string, string];
    return `${month.length === 1 ? `0${month}` : month}/${year.length === 4 ? year.slice(2) : year}`;
  }
  const digits = typed.replace(/\D/g, "");
  return digits.length === 4 ? `${digits.slice(0, 2)}/${digits.slice(2)}` : typed.trim();
}

/** The card as the person typed it, tidied: the number's digits only, the expiry as MM/YY. */
export function tidyCard(typed: Card): Card {
  return { number: typed.number.replace(/\D/g, ""), expiry: tidyExpiry(typed.expiry), cvc: typed.cvc.trim(),
    name: typed.name.trim(), zip: typed.zip.trim(), ...address(typed) };
}

function luhn(digits: string): boolean {
  let sum = 0;
  [...digits].reverse().forEach((c, i) => {
    let d = Number(c);
    if (i % 2 === 1) { d *= 2; if (d > 9) d -= 9; }
    sum += d;
  });
  return sum % 10 === 0;
}

/** What is wrong with a tidied card, or null when it can be added. */
export function cardProblem(card: Card): string | null {
  if (card.number.length < 12 || card.number.length > 19 || !luhn(card.number)) return "The card number is not right.";
  const month = Number(card.expiry.slice(0, 2));
  if (!/^\d{2}\/\d{2}$/.test(card.expiry) || month < 1 || month > 12) return "Write the expiry as MM/YY.";
  if (!/^\d{3,4}$/.test(card.cvc)) return "The CVC is 3 or 4 digits.";
  if (!card.name) return "Write the name on the card.";
  if (!card.zip || card.zip.length > 10) return "Write the billing ZIP or postal code.";
  return null;
}

/** The secret's value: one line of JSON with its keys in order, the same bytes as the window's. */
/** A card's value as it was sealed (#5115), or null when it is not a card's. */
export function parseCard(value: string): Card | null {
  try {
    const card = JSON.parse(value) as Partial<Card>;
    if (typeof card !== "object" || card === null || typeof card.number !== "string") return null;
    return { number: card.number, expiry: card.expiry ?? "", cvc: card.cvc ?? "", name: card.name ?? "", zip: card.zip ?? "", ...address(card) };
  } catch {
    return null;
  }
}

/** The card number in groups of four, as a reveal shows it. */
export function spacedNumber(number: string): string {
  return number.replace(/(.{4})(?=.)/g, "$1 ");
}

export function cardValue(card: Card): string {
  const parts: Record<string, string> = { cvc: card.cvc, expiry: card.expiry, name: card.name, number: card.number, zip: card.zip,
    ...address(card) };
  // The keys in order, as the window's JSONEncoder sorts them; an address part only when set.
  return JSON.stringify(Object.fromEntries(Object.entries(parts).sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0))));
}
