// The hand-back page (#1343, #1335 S1 Q1c option 4). It holds nothing and keeps nothing: it
// gives the code and the state (or the provider's error) back to the client that opened the
// browser, then takes them out of the address bar.
//
// - The state has no dot (the Mac and the Remote): go to agents://oauth/callback, which their
//   authentication session catches.
// - The state ends in "." and a base64url origin (the web page): postMessage to window.opener,
//   to that origin only. A page at another origin is never sent the code. An origin that is
//   not agentloops.dev's gets it only after the person sees the origin and agrees.
//
// The code alone is of no use: the host that started the sign-in holds the PKCE verifier.
(function () {
  "use strict";
  var words = document.getElementById("words");
  var back = document.getElementById("back");
  var query = new URLSearchParams(location.search);
  var answer = {};
  ["code", "state", "error"].forEach(function (k) {
    var v = query.get(k);
    if (v) answer[k] = v;
  });
  history.replaceState(null, "", location.pathname);

  function say(text) { words.textContent = text; }

  if (!answer.state && answer.error) {
    // A provider that leaves the state out of an error (DigitalOcean's Deny, #6072): an
    // error holds no secret, so it goes to the page that opened this one, or to the app,
    // which ends its one sign-in that is waiting.
    if (window.opener) {
      window.opener.postMessage({ type: "agents-oauth-callback", code: null, state: null, error: answer.error }, "*");
      say("The sign-in did not finish. You can close this window.");
      setTimeout(function () { window.close(); }, 400);
      return;
    }
    // With no opener and no state (the web page's window was cut off, #6074), which page asked
    // is not known: the app is tried, and the words say what to do on the web page.
    var denied = "agents://oauth/callback?" + new URLSearchParams({ error: answer.error }).toString();
    back.href = denied;
    back.hidden = false;
    say("The sign-in did not finish. Close this window, then connect again from Agent Loops.");
    location.replace(denied);
    return;
  }
  if (!answer.state) {
    say("This page is for signing in from Agent Loops. Start again from Agent Loops.");
    return;
  }
  var dot = answer.state.indexOf(".");
  if (dot < 0) {
    var target = "agents://oauth/callback?" + new URLSearchParams(answer).toString();
    back.href = target;
    back.hidden = false;
    say(answer.error ? "The sign-in did not finish. Going back to Agent Loops…" : "Going back to Agent Loops…");
    location.replace(target);
    return;
  }
  var origin = "";
  try {
    var b64 = answer.state.slice(dot + 1).replace(/-/g, "+").replace(/_/g, "/");
    origin = new URL(atob(b64)).origin;
  } catch (e) {
    origin = "";
  }
  if (!/^https?:\/\//.test(origin)) {
    say("The Agent Loops page that asked is not open any more. Start the sign-in again from Agent Loops.");
    return;
  }
  function give() {
    if (!window.opener) {
      // A provider whose pages send Cross-Origin-Opener-Policy (DigitalOcean, #6074) cuts this
      // window off from the page that opened it. So go to that page's own origin, which gives
      // the answer to its other tabs (Web/src/oauthHandBack.ts) and closes this one.
      say("Going back to Agent Loops…");
      location.replace(origin + "/#oauth-callback?" + new URLSearchParams(answer).toString());
      return;
    }
    window.opener.postMessage({ type: "agents-oauth-callback", code: answer.code || null,
      state: answer.state, error: answer.error || null }, origin);
    say("Signed in. You can close this window.");
    setTimeout(function () { window.close(); }, 400);
  }
  // Any page can start a sign-in with our client ID and name itself in the state. So a page
  // that is not ours gets the code only when the person sees its address and agrees.
  var host = new URL(origin).hostname;
  if (origin.indexOf("https://") === 0 && (host === "agentloops.dev" || /\.agentloops\.dev$/.test(host))) {
    give();
    return;
  }
  say("Give this sign-in to " + origin + "? Do this only when it is your own Agent Loops page.");
  back.textContent = "Give it to " + host;
  back.href = "#";
  back.hidden = false;
  back.addEventListener("click", function (e) {
    e.preventDefault();
    back.hidden = true;
    give();
  });
})();
