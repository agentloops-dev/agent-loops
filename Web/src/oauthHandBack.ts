// The hand-back page's answer, when it came to this origin's address (#6074): a provider whose pages
// send Cross-Origin-Opener-Policy (DigitalOcean) cuts the sign-in window off from the page that
// opened it, so deploy/site/www/oauth/callback.js sends it here, as #oauth-callback?code&state.
// This tab gives it to the others of this origin, which wait for it, takes it out of the address,
// and closes. The code alone is of no use: the machine that started the sign-in holds the verifier.

/** The channel the dialogs that wait for a sign-in listen on. */
export const oauthChannel = "agents-oauth-callback";

const prefix = "#oauth-callback?";

/** The answer in this address, or undefined. */
export function handedBack(hash: string): { type: string; code: string | null; state: string | null; error: string | null } | undefined {
  if (!hash.startsWith(prefix)) return undefined;
  const q = new URLSearchParams(hash.slice(prefix.length));
  return { type: "agents-oauth-callback", code: q.get("code"), state: q.get("state"), error: q.get("error") };
}

/** Gives this address's answer to the other tabs and closes this one; false when it has none. */
export function handBack(): boolean {
  const answer = handedBack(location.hash);
  if (!answer) return false;
  history.replaceState(null, "", location.pathname + location.search);
  if (typeof BroadcastChannel === "undefined") return true;
  const channel = new BroadcastChannel(oauthChannel);
  channel.postMessage(answer);
  channel.close();
  setTimeout(() => window.close(), 400);
  return true;
}
