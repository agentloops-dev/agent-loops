// The guided DigitalOcean Connect (#6074, wireframes W1, W2, W2b and W6 in epic #6070): the
// web page's words, the same as the window's DigitalOceanConnectWords.swift, and the hand-back
// page's message for this sign-in.
import type { ProviderOAuth, ProviderSummary } from "../protocol/generated";
import { oauthCallbackOrigin } from "./connectorSetup";

/** The preset an OAuth connection makes (#6072). */
export const digitalOceanPreset = "digitalocean-api";

export const doWords = {
  // W1
  guidedChip: "Guided",
  rowSummary: "Connect your account; Agent Loops makes the droplet.",
  // W2
  intro: "Agent Loops makes droplets for your agents, and starts and stops them.",
  howItWorks: "How it works",
  connectLine: "Connect your DigitalOcean account. Your browser opens DigitalOcean: sign in and choose Authorize.",
  connect: "Connect DigitalOcean…",
  noAccount: "No account yet?",
  signUp: "Sign up on DigitalOcean",
  signUpURL: "https://cloud.digitalocean.com/registrations/new",
  keeps: "Keeps the connection",
  keepsHelp: "The token stays on this machine.",
  noMachine: "Add a machine first. The connection stays on one of your machines.",
  tokenInstead: "Use an API token instead",
  tokenField: "API token",
  tokenPage: "Make a token on DigitalOcean",
  tokenPageURL: "https://cloud.digitalocean.com/account/api/tokens",
  tokenScopes: "Custom scopes: droplet create, read, update and delete; ssh_key create and read; regions read; sizes read; tag create; account read.",
  connectWithToken: "Connect with Token",
  // W2b
  waiting: "Waiting for DigitalOcean…",
  waitingHelp: "Finish in your browser, then come back here.",
  openAgain: "Open the Page Again",
  addPayment: "Add a Payment Method",
  billingURL: "https://cloud.digitalocean.com/account/billing",
  checkAgain: "Check Again",
  popupBlocked: "The browser did not open DigitalOcean’s page. Let this page open windows, then choose Open the Page Again.",
  // W6
  connectedChip: "Connected",
  disconnectedChip: "Disconnected",
  reconnect: "Reconnect…",
  disconnected: "DigitalOcean disconnected Agent Loops. Reconnect to start and stop its droplets.",
  removeNote: "Agent Loops’s connection is revoked at DigitalOcean. Its droplets stay: delete them on DigitalOcean to stop paying for them.",
};

/** What a refusal offers: Connect again, or the billing page and Check Again. */
export type Fix = "connect" | "payment";

/** A refusal's words and its one fix, by the control plane's data.reason (#6072). */
export function refusal(reason: string | undefined, words: string): { text: string; fix: Fix } {
  switch (reason) {
    case "denied": return { text: "DigitalOcean did not connect: you chose Deny.", fix: "connect" };
    case "account_inactive": return { text: "Your DigitalOcean account cannot make droplets yet, such as when it has no payment method.", fix: "payment" };
    case "scope_missing": return { text: "DigitalOcean gave Agent Loops less than it needs. Connect again, and keep every permission it asks for.", fix: "connect" };
    case "expired": return { text: "That sign-in took too long, or was started somewhere else. Connect again.", fix: "connect" };
    case "disconnected": return { text: doWords.disconnected, fix: "connect" };
    default: return { text: words, fix: "connect" };
  }
}

/** "alex@example.com · team “Personal”", or undefined when the provider said neither. */
export function accountLine(oauth: ProviderOAuth): string | undefined {
  const parts = [oauth.account, oauth.team ? `team “${oauth.team}”` : undefined].filter((p): p is string => !!p);
  return parts.length ? parts.join(" · ") : undefined;
}

/** The card's chip and Reconnect… are for a provider connected with OAuth. */
export function isOAuth(provider: ProviderSummary): boolean {
  return !!provider.oauth && provider.preset === digitalOceanPreset;
}

/**
 * The hand-back page's message for the sign-in with `state`: its code and state, or the error of
 * a Deny, which DigitalOcean sends with no state. Undefined for any other message.
 */
export function doCallback(event: { origin: string; data: unknown }, state: string): { state?: string; code?: string; error?: string } | undefined {
  if (event.origin !== oauthCallbackOrigin) return undefined;
  const d = event.data as { type?: unknown; code?: unknown; state?: unknown; error?: unknown } | null;
  if (!d || typeof d !== "object" || d.type !== "agents-oauth-callback") return undefined;
  const error = typeof d.error === "string" && d.error ? d.error : undefined;
  if (d.state === state) {
    const answer: { state?: string; code?: string; error?: string } = { state };
    if (typeof d.code === "string" && d.code) answer.code = d.code;
    if (error) answer.error = error;
    return answer;
  }
  return !d.state && error ? { error } : undefined;
}
