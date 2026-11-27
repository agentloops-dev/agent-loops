# Runtime sign-in on a new machine (spike #5360, epic #5353)

Answers the "Sign-in" section of the epic's spec. Read on 2026-11-14 from each runtime's
own documentation and from what the daemon already does (`Go/internal/agentsd/runtimesignin.go`).
"Not in docs" means the vendor's page does not say; the step must measure it (slice 6).

## Table

| Runtime | Key accepted? | Where it goes, as the machine's user | Device or browser route | What the code shows, and how long | Copy a login from the Mac? |
|---|---|---|---|---|---|
| **Claude** | Yes. `ANTHROPIC_API_KEY` (Console key; in `-p` mode always used). | Environment of the run (the daemon already clears and sets it, `agentsd`). Nothing is written to disk; `/login` would write `~/.claude/.credentials.json` (0600). | No device code. `claude setup-token` opens a browser link; the page gives a code typed back into the terminal. It prints a **one-year** `CLAUDE_CODE_OAUTH_TOKEN` (model requests only; needs a paid plan). | A link and a paste-back code. Lifetime of the code: not in docs. The daemon waits 15 min. | No. Docs describe one config dir per login and no several-machine login. |
| **Codex** | Yes. `printenv OPENAI_API_KEY \| codex login --with-api-key`. | `~/.codex/auth.json` (plaintext by default) or the OS store; `cli_auth_credentials_store` picks. | `codex login --device-auth` (beta, the documented choice for headless). Needs device-code login switched on in the ChatGPT account or workspace. | A link and a one-time code. Lifetime: not in docs. The daemon waits 15 min. | The docs offer copying `auth.json` as a fallback, with "treat it like a password". Not a several-machine login. A ChatGPT Enterprise **access token** (`codex login --with-access-token`) is documented for trusted runners: a later option for orgs. |
| **Copilot** | Yes, a token, not an API key: `COPILOT_GITHUB_TOKEN`, then `GH_TOKEN`, `GITHUB_TOKEN`. A fine-grained PAT (`github_pat_`) with the **Copilot Requests** permission, an OAuth `gho_` or an app `ghu_` token. Classic `ghp_` is refused. | Environment (overrides a stored login). Stored login: OS keychain `copilot-cli`; with no keychain, `~/.copilot/config.json` in plaintext. | `/login` ▸ device code at github.com/login/device. | One-time code. GitHub's device codes last 15 min (GitHub's OAuth docs; not on the Copilot page). | No. |
| **Cursor** | Yes. `CURSOR_API_KEY` or `--api-key`, from the Cursor Dashboard. | Environment or flag. Stored login: "locally", path not documented. | `agent login` opens a browser; `NO_OPEN_BROWSER=1` prints the URL. **No device code.** | A URL only. Not in docs. | No. |
| **Grok** | Yes. `XAI_API_KEY` (order: `model.api_key`, `model.env_key`, session token, `XAI_API_KEY`). | Environment. Config is `~/.grok/config.toml` (or `$GROK_HOME`). | `grok login` (browser) and `grok login --device-auth` for ssh and headless hosts. | One-time code. Lifetime: not in docs. | No. Where the session token is kept: not in docs. |
| **OpenCode** | Yes, per provider. Provider env vars (`OPENROUTER_API_KEY`, `AWS_BEARER_TOKEN_BEDROCK`, …), or `opencode auth login` for a key typed into its store. It works signed out on Zen's free models. | Its own `auth.json` (path not on the providers page) or the provider's variable. | Only for some providers: GitHub Copilot uses GitHub's device code (15 min). The rest take a key. | As Copilot for Copilot. | No. |

## Result

- **Every runtime takes a key** (Copilot a token). A key is the only route that needs no person, so step 7 can be automatic for a runtime when the person has put its key in Secrets.
- **Only Codex, Grok and Copilot have a true device code** (Copilot and OpenCode-Copilot through GitHub). **Claude** has a browser link with a paste-back code. **Cursor** has a browser URL only. These are the sign-ins where step 7 says **waiting for you**.
- **No runtime documents one login on several machines**, so nothing is copied from the Mac. Codex's `auth.json` copy and the Enterprise access token are noted, not built.
- **Where to write.** Prefer the run's environment (no file on the machine) for Claude, Cursor, Grok, Copilot and OpenCode; Codex's own `codex login --with-api-key` for Codex, which only reads `auth.json`. All as the machine's user, never root.

## Secret names step 7 looks for

`ANTHROPIC_API_KEY` or `CLAUDE_CODE_OAUTH_TOKEN` (Claude), `OPENAI_API_KEY` (Codex), `COPILOT_GITHUB_TOKEN` (Copilot), `CURSOR_API_KEY` (Cursor), `XAI_API_KEY` (Grok), and the provider's own variable (OpenCode).

## Not settled by reading (slice 6 measures it)

Device-code lifetimes for Codex, Grok and Cursor's URL; where Cursor and Grok keep a stored login; whether Grok's ACP mode accepts `XAI_API_KEY` set in the environment alone on Linux.

## Can an agent do the person's step? (spike #5493)

Read on 2026-11-26 from the vendors' public docs (no account was signed in to), from
`Go/internal/agentsd/secretsignin.go` (step 7's key route, built), from spec
`specs/7469-server-own-sign-in/` (each server's own Claude and Codex sign-in, with a card for
the link and code: #7470, #7471), and from the `sign-up` skill. The issue's three ways:

1. **Make the key once.** An agent, in the agents' browser signed in to the person's vendor
   account, makes a key or a long-lived token on the vendor's page and saves it with
   `capture_secret` under the name step 7 looks for. Each later machine then takes the key
   route with no person.
2. **Answer the code page.** For each machine, an agent opens the link that step 7 (or the
   #7470 / #7471 card) shows, in the agents' browser signed in to the vendor, and approves it,
   with 2FA by the TOTP secret.
3. **Codex Enterprise access token.** `codex login --with-access-token`.

### Per runtime

| Runtime | 1. Make the key once | 2. Answer the code page | 3. Access token | What stays a person's step |
|---|---|---|---|---|
| **Claude** | **Yes.** Two kinds. (a) `claude setup-token`: a **one-year** `CLAUDE_CODE_OAUTH_TOKEN` on the person's plan (Pro, Max, Team, Enterprise); the agent opens the link, approves it on claude.ai, types back the code, and captures the token the command prints. (b) A Console key (`ANTHROPIC_API_KEY`) at platform.claude.com/settings/keys, with an expiry (up to "Never", or the org's maximum), billed per token. The Admin API cannot make a key (it only reads, renames and disables keys). | Yes, the same claude.ai page and paste-back code as (a), once per machine. | n/a | The first sign-in to claude.ai in the agents' browser (email code, or a passkey/Google sign-in that needs the person). Renewal after a year. |
| **Codex** | **Only with API billing.** An `OPENAI_API_KEY` from platform.openai.com, or by the Admin API (`POST /organization/projects/{id}/service_accounts` returns a key; expiry up to 365 days) with an admin key, which is itself a credential the person must make. A ChatGPT plan has **no key**: its sign-in is per machine. | **Yes**, for a ChatGPT plan: `codex login --device-auth` gives a link and code at auth.openai.com. Device-code login must be on (the person's security settings, or the workspace admin's permission). | Only for a ChatGPT Business/Enterprise workspace whose admin gives the access-token permission; a member makes the token. Not for a personal plan. | The first sign-in to ChatGPT in the agents' browser; switching device-code login on; for way 3, the workspace admin's permission. |
| **Copilot** | **Yes.** A fine-grained PAT with the **Copilot Requests** permission, user-owned, saved as `COPILOT_GITHUB_TOKEN`. GitHub has no API to make one, but a prefilled URL (`github.com/settings/personal-access-tokens/new?…&expires_in=366`) fills the form, so the agent only names it and presses Generate. Expiry 1 to 366 days, or none where policy allows. | Yes: github.com/login/device, 15-minute code. | n/a | The first GitHub sign-in in the agents' browser (password, TOTP, or a passkey that needs the person). Renewal at expiry. |
| **Cursor** | **Yes.** A key at cursor.com/dashboard/api (Dashboard ▸ API Keys), saved as `CURSOR_API_KEY`. An API to make one: not in docs. | Possible but poor: `agent login` gives a browser URL only (no code), so the agent must open it on the same machine or pass the URL. | n/a | The first Cursor sign-in in the agents' browser. |
| **Grok** | **Yes.** A key at console.x.ai, saved as `XAI_API_KEY`; or by xAI's Management API (`management-api.x.ai`) with a management key that has Management Keys read and write. | Yes: `grok login --device-auth`, a link and a code. | n/a | The first xAI sign-in in the agents' browser, or the management key. |

### Result

- **Way 1 is the best for every runtime that has a key or a long-lived token**: Claude
  (`setup-token`), Copilot, Cursor and Grok. It is done once per person (once a year for
  Claude and Copilot), and step 7's key route, already built, does the rest on every machine.
  It needs the agents' browser to hold the person's vendor session, which is a credential: the
  person chooses it, per vendor.
- **Way 2 is only for Codex on a ChatGPT plan**, which has no key. It runs on each new machine
  and each "Sign in again", so it needs a vendor session that stays signed in. It answers the
  #7470 / #7471 card in the person's place, and is built after them.
- **Way 3 is not built now.** It serves only Business/Enterprise workspaces with an admin's
  permission. It becomes a third Codex secret name (`CODEX_ACCESS_TOKEN`) when an org asks.
- **A person's step stays** in each way: the first sign-in to the vendor in the agents'
  browser when it needs a passkey, an SMS code or a CAPTCHA, and the person's choice to let an
  agent make or store that credential. The agent never signs in to a vendor with a password it
  does not have; it uses `fill_secret`, `generate_secret` and `scripts/totp.sh`, never a
  typed value.
- **Direction.** A key made this way lives in the person's Secrets, on the person's machines,
  and goes to a server sealed to its key (Principle 6); the control plane holds none of it.

### Build steps

Filed as orphans for the standing epic #4208, linked in #5493's decision comment.
