# A server's own Claude and Codex sign-in (#7469, epic #7468)

Spike result. It answers the six questions of #7469 and says which 056 decisions it
replaces. Facts are from the vendors' docs, read 2026-11-27; what no doc says is marked
**unverified** and is checked by the child named next to it.

## Decisions

- **Each server signs in to each runtime itself, on the server.** The sign-in is made there and
  stays there. For Claude the daemon runs `claude setup-token` in a pseudo-terminal on the
  server (not Agents Host on the Mac); for Codex it runs `codex login --device-auth`. Either
  way the daemon shows the URL and code as a question card and takes the pasted code back from
  the card. The token is printed on the server, so **it is never on the Mac, the control plane
  or the wire, and nothing needs sealing.** *Decided, because* it meets Principle 6 with no
  ciphertext to carry, and it keeps the sign-in inside Anthropic's own flow (Q4). It changes
  the epic's text ("Agents Host runs `setup-token`, sealed to the server's key"): #7470 is
  updated to match. Alex can overrule.
- **Fallback only:** when a server cannot run the flow (no usable pty, `setup-token` needs a
  browser on the same machine), Agents Host runs `claude setup-token` on the Mac and the token
  goes by the sealed-secret path (Q5). #7470 tries the server-side flow first on a scratch
  server and builds the fallback only if it fails.
- **The relay is removed one release after a server's own sign-in ships** (Q6), as the epic says.

## Q1. `claude setup-token`: life, scope, renewal, revocation

- Life: one year ([Authentication, "Generate a long-lived token"](https://code.claude.com/docs/en/authentication)).
- Scope: the token "can only make model requests": no Remote Control, no claude.ai
  connectors; locally configured MCP servers still work (same page). It needs a Pro, Max,
  Ultra, Team or Enterprise plan.
- Renewal: none is documented. The command prints the token and saves nothing; it is made
  again when it ends. We treat it as non-refreshing. The daemon notes the made-at date and shows
  "Sign in again" 30 days before the year ends and when a turn fails with a sign-in error.
- Revocation: **unverified.** No doc names a per-token revoke; a feature request asks for
  `--list` and `--revoke` (anthropics/claude-code#48373). Until one exists, "remove a server's
  sign-in" means the daemon deletes its copy (the token stops being used, not being valid);
  the person can end it at claude.ai (Settings) if that lists it. #7470 checks this and the
  UI says so plainly. Reports of `401 Invalid bearer token` with fresh tokens exist (e.g.
  anthropics/claude-code#65320): #7470 starts with a real turn on a scratch server.

## Q2. Same limits as the Mac? How is it sent?

- It "authenticates with your Claude subscription": the same plan and the same limits as the
  Mac's sign-in. Servers and Mac share one pool, so many agents at once spend it faster
  (Anthropic's limits "assume ordinary, individual usage").
- Sent as `CLAUDE_CODE_OAUTH_TOKEN` in the environment of the `claude` process. Precedence:
  it ranks below `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_API_KEY` and `apiKeyHelper`, and above a
  `/login` sign-in, so the daemon sets it only on its own `claude` runs, never in the server's
  shell. `--bare` does not read it.
- No `ANTHROPIC_BASE_URL` gate any more: the relay's gate closes for that server.

## Q3. Codex on a server with no browser

- `codex login --device-auth` prints a URL and a one-time code; the person enters it in any
  browser ([Codex auth](https://learn.chatgpt.com/docs/auth)). It is **beta**, and must first
  be enabled in the person's ChatGPT security settings or the workspace's permissions: the
  card says so when the command reports it is off.
- Code life: 15 minutes per third-party guides; OpenAI's page gives none. **Unverified**;
  #7471 reads it from the command's output and shows a new card when it ends.
- `auth.json`: `~/.codex/auth.json` or `CODEX_HOME`, plaintext, or the OS credential store.
  Codex "refreshes tokens automatically during use", so the server needs no help from the Mac.
  The daemon sets `CODEX_HOME` to a folder it owns, mode 0700, so one agent account's sign-in
  is not another's.

## Q4. Terms

- **Anthropic** ([Legal and compliance](https://code.claude.com/docs/en/legal-and-compliance)):
  OAuth "is intended exclusively for purchasers of … subscription plans" and for "ordinary use
  of Claude Code". Developers "may not collect, store, or intermediate Claude.ai credentials
  or session tokens — sign-in to a Claude account must complete through Anthropic's own
  flow", and may not "route requests through" plan credentials for their users. It does not
  "prevent an end user from signing in to the unmodified Claude Code binary with their own
  Claude subscription". Nothing says one person may not sign in on several machines.
- **OpenAI** ([Terms of use](https://openai.com/policies/row-terms-of-use/)): "You may not
  share your account credentials or make your account available to anyone else." Nothing on
  devices. OpenAI's own Codex page tells a person to copy `auth.json` to a headless machine
  and calls device-code the preferred headless sign-in.
- **Meaning for this epic:** the design must be the person signing in, with the unmodified
  `claude` or `codex`, on their own server, through the vendor's own flow. That is why the
  decision above has the server run the command and the control plane see nothing: we do not
  collect or store a credential, and a Mac-side capture-and-seal (the fallback) is the nearer
  case to "collect … intermediate", so it is the fallback and the reason to try the server
  first. The product never lends one person's sign-in to another person, never to a Team's
  other members: a server of a person has that person's sign-in. A Team or Enterprise
  plan is under the Commercial Terms, not the consumer ones; same design. This is a reading,
  not legal advice. It stays under review before 1.0.

## Q5. How a sealed token would get to one server (fallback only)

Reuse what exists; add no new path:

- Agents Host captures the token from the Mac's `claude setup-token` and saves it as an
  agent-style secret (`saveAgentSecret`, `Go/internal/agentsd/agentsecrets.go`).
- A client of the person's seals it to the **one** server's key (`machine/secretClients`,
  the sealing of #3397); the control plane keeps only the envelope (Principle 6).
- The server's daemon opens it with its own key, keeps it with its other sealed secrets, and
  gives it to `claude` as `CLAUDE_CODE_OAUTH_TOKEN` for the run only, through a grant that
  ends with the run (the grant of #7156, `secretcmd.go`). A value with **Ask before use** is
  not used: this token is made for the server on purpose.
- Gaps to close in #7470 if the fallback is built: sealing to exactly one machine (today an
  agent secret is sealed to the person's clients), and keeping the token out of the Mac's
  logs and out of the agent's reach on the Mac.

## Q6. Moving servers off the relay

1. A server's own sign-in is **never switched automatically**. The first Claude (or Codex)
   turn on a server with no own sign-in still uses the relay, and the app shows a card
   "Sign in on this server so it works when the Mac is away".
2. When the own sign-in is made, the daemon runs one cheap check turn; if it passes, that
   server's relay gate closes (no `ANTHROPIC_BASE_URL`, no Codex stand-in) and its own sign-in
   wins. If it fails, the relay stays and the card says why.
3. A server marked "Use this server's own sign-in only" (D6) never had a relay; it gets the card
   at once.
4. One release after the own sign-in ships, the relay (T091's host side, the stand-ins, the
   gate) is removed in one PR. A server that still has no own sign-in then shows "Sign in
   again". Removal is a child of the epic (new, below).

## 056 decisions

| 056 | Now |
|---|---|
| D1 (the Mac's sign-in, relayed) | **Replaced**: each server's own sign-in. The Mac's is untouched by a server. |
| D2 (no pasted token, no token at all) | **Replaced in part**: a token exists, made on the server by `claude setup-token`; still no pasted token and no Settings row for one. |
| D3 (only a Claude account sign-in is relayed) | Moot with the relay; the server's own sign-in is a claude.ai plan sign-in by construction. |
| D4 (the Mac's `claude` renews it) | Moot: nothing to renew from the Mac. |
| D5 (only the person's own agents on the server) | **Stays** as the rule for who can read the token: `claude` runs get it, the agent's shell does not. |
| D6 ("Use this server's own sign-in only") | **Stays.** |
| D7 (the Mac's sign-in beats a server's own) | **Replaced**: the server's own wins once it exists. |

## Children

- #7470 (Claude): run `setup-token` on the server, card for the URL and code, sealed fallback.
- #7471 (Codex): device-auth card. It shares the card and "Sign in again" path with #7470; the
  first of the two to land makes it.
- New: remove the relay one release after (blocked by #7470 and #7471).
