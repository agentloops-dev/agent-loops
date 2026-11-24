---
diataxis: reference
devices: [mac, iphone, ipad]
description: The organisation's policy, its rules, its signature and its methods.
---

# Policy

The organisation's policy (#998): one document on the control plane, signed by its key and
sent to every machine. Each rule is optional. A rule that is not set sets no ceiling. Why
and how it is sent: [Policy](../explanation/control-plane.md#policy).

The types are in `Packages/Wire/schema` (`OrgPolicy`, `SignedPolicy`,
`PolicySignature`, `PolicyRefusal`, `PolicySetBy`), and the methods in
`Packages/Wire/control.openrpc.json`.

The organisation's policy is the top layer of three: Organisation, You and Folder (#2957).
Each layer can only make a rule tighter. See [Policy layers](../explanation/policy-layers.md).
`policy/effective` (on a machine, `host.policy-layers/1`) gives each rule's effective value,
every layer's value, the layer that decides, and, for the layer a client edits, its `cap`
and `capLine` (such as `Your organisation: at most Approve for me`). With no folder, it
also lists the folders that set rules of their own (`foldersWithRules`). `policy/yours`
gives your layer and, on the machine that keeps it, its signed copy (`signed`), which the
Mac window passes to a server with `policy/applyYours`.

## The rules

| Field | Values | What it does |
|---|---|---|
| `modeCeiling` | `ask`, `default`, `acceptEdits`, `auto`, `bypassPermissions` | The loosest mode an agent can have. A mode that a machine does not know counts as the loosest. |
| `sandbox` | `required` | Refuses an agent with no command sandbox, or one that the app cannot vouch for. |
| `runtimes` | `{<runtime id>: {models?: [id], reach?: [way]}}` | Only these runtimes. `models` names the models allowed; `*` at the end matches more than one (`claude-sonnet-*`, which also takes the family `claude-sonnet`). A pattern is matched with the runtime's own value, that value without its context suffix (`opus[1m]` is `opus`) and, for Claude, whose values are aliases, the model the option's name says (`Sonnet 5` is `claude-sonnet-5`) and its family (#2274). Claude's `default` is matched as the model its description names. `reach` (#999) names the ways the runtime may reach the model: `signIn` (the person's own sign-in), `orgKey`, `gateway`. Absent: every way. `[orgKey, gateway]` is the org path only. Console ▸ Policy shows it as **Any way**, **Org key or gateway only** and **Own sign-in only**; to set it with no runtime list, the Console names every runtime. |
| `marketplaces` | `{allowed: [id], customServers: allow \| refuse}` | Only these marketplaces, and whether an MCP server added by hand is allowed. |
| `budgets` | `{daily?, dailyTokens?, perAgent?, perAgentTokens?, allowUnmetered: [runtime id], raises: [...]}` | Each person's budget, in US dollars and tokens, per UTC day and per agent. A ceiling over the person's own cost limits. |
| `helpers` | `{mostRunning?, mostNotArchived?}` | The ceiling over each folder's helper limits. |
| `permissionRequests` | `alwaysApprove`, `default` | The loosest answer to a permission request (#2957). `default` makes each request a card. Console ▸ Policy names it **Ask me**. |
| `workflows` | `{mostOn?}` | The most workflows turned on at once in each folder (#2957). |
| `noticeLimits` | `{burst?, waitMinutes?}` | How many notices an agent may send in a burst, and the minutes it waits after one (#2957). With no layer that sets them: 3 and 10. |
| `archiveMyChats` | `false` | Agents may not archive a person's finished chats (#2957). Absent or `true` allows it. |
| `environment` | `{passThrough: [name]}` | The variable names that a person's own pass-through list can name; `*` at the end allowed. A runtime now gets agentsd's whole environment, so the list no longer cuts what a launch receives; an org account still takes the runtime's own variables out. |
| `notices` | `{antigravityTerms?: show \| accepted \| refuse}` | Google's terms notice for Antigravity (#999): `show` (as absent), `accepted` (your organisation reviewed the terms; members see no notice, and the Policy pane says so), `refuse` (Antigravity is refused). Console ▸ Policy names them **Show the notice**, **Reviewed by your organisation** and **Do not allow**. |
| `stopAll` | `{on, by, at, note?}` | The kill switch. Only `policy/stopAll` changes it. |
| `connectorTools` | `{<connector>: {writes?, tools?: {<tool>: allow \| ask \| off}}}` | Only in your layer and a folder's (#4099). **Allow**, **Ask** or **Off** for each tool of a connector, by the connector's name; `writes` is one level for each tool that writes and that `tools` does not name. A tool no layer names is **Ask** when it writes and **Allow** when it only reads. A tool writes unless its annotations say `readOnlyHint: true`, or when they say `destructiveHint: true`. Tighter is later: Allow, Ask, Off. See [Connector tools](#connector-tools). |
| `commitTrailers` | `{<trailer>: on \| off}` | In each layer (#6365). Which trailers an agent's commits and squash messages carry: `Made-by`, `Cost`, `Tokens`, `Duration`, `Session`, `Fixes` (also `Refs`), `Epic`, `Requested-by`, `Approved-by`, `Co-authored-by`. A key that no layer sets has its default: every trailer **On**, `Cost` **Off**. For a record trailer **On** is tighter; for a spend trailer (`Cost`, `Tokens`, `Duration`) **Off** is tighter. A key a machine does not know is kept and has no effect, such as `Attested`, a key until #6545 removed it. Each key is a rule of `policy/effective`, `commitTrailers.<key>`. See [Commit trailers](#commit-trailers). |
| `requireFollowing` | `true` | The control plane refuses to start work on a machine that has not kept this version. |
| `groups` | `{}` | Empty. A place for later layers, which can only make a rule tighter. |

The control plane sets these fields itself:

| Field | What it is |
|---|---|
| `version` | One more at each change. A machine refuses a lower version. |
| `issued` | When the change was saved. |
| `changedBy` | The admin and their client, as the audit names them. |
| `requires` | The capabilities a machine needs to follow the policy: `host.policy/1`, and `host.org-routes/1` when `reach` or `notices.antigravityTerms` is set. |

**Bounds.** The document is under 64 KiB. A list has at most 200 items. A budget is from $0 to
$1,000,000 a day. A helper limit is from 0 to 1,000. A value out of bounds is refused, and
nothing is kept.

**Raises.** `policy/raise` gives one person a higher budget for today (UTC). A person has at
most one raise each day; the newest one is kept. Raises of past days are removed at the next
change. A person sees only their own raises.

## Connector tools

The `connectorTools` rule (#4099) is set on a connector's **Tools** list (Settings ▸ Agent
Configuration ▸ Connectors, or the folder's), not in Agents ▸ Policy, which shows it read
only. `mcp/tools` gives a connector's tools as its last health check's `tools/list` gave them,
each with the level in force, the destination layer's own level and the cap of the layers
above; `mcp/setToolLevel` sets one tool's level, or with no tool the `writes` switch, in your
layer (a connector of yours) or in the folder's `.agents/policy.json` (one of the folder's).
Only the person's own clients can call it: never an agent.

How a level is kept:

| Runtime | Off | Allow and Ask |
|---|---|---|
| Claude | `disallowedTools` on `session/new`: `mcp__<connector>__<tool>` | A permission request for an Allow tool is allowed with no card; an Ask tool shows the card. |
| Copilot | `--deny-tool <connector>(<tool>)` | The same |
| OpenCode | `tools: {<connector>_<tool>: false}` in `OPENCODE_CONFIG_CONTENT` | The same |
| The others | A permission request for an Off tool is refused, and the chat shows **Stopped by a policy**, the rule and where to change it. | The same |

A runtime that uses a tool without asking (a mode such as Bypass Permissions) and has no deny
list of its own is not stopped by an Off level. A runtime starts again at its next turn when
its Off tools change.

## Commit trailers

`git/trailers {session, pr?}` (capability `host.commit-trailers/1`, #6371) gives the trailer
lines of a commit by the session's agent, or of its pull request's squash message, by the
effective `commitTrailers` rule of the session's folder. The worktree's `commit-msg` hook and
the agents' tool `commit_trailers` ask it; an agent types no trailer by hand. A trailer with
no value is left out. The lines come in this order:

| Trailer | Value |
|---|---|
| `Made-by` | `<runtime>:<model>`, as `claude-code:claude-opus-5-5` (the shape of the kernel's `Assisted-by`). |
| `Session` | The session's id. Not its web address, which would put the Mac's hardware id and the person's home folder in public history. |
| `Fixes` | One line for each issue the session is labelled with (`#n`) and each issue the PR fixes. With no `pr`, `Refs`: a commit on a branch closes nothing. |
| `Epic` | The first issue's parent: lineage's part-of link, else one read of the issue from the forge cache. |
| `Requested-by` | The login of the person's forge connection. |
| `Approved-by` | Each login whose newest review of the PR approves it: one read of the PR from the forge cache. Only with `pr`. |
| `Cost`, `Tokens`, `Duration` | The totals of the sessions labelled with the PR (`P#<n>`) or its issues, from the cost on the link (`lineage-cost.json`): the PR, the issues it fixes, and the session's own `#n`. With no `pr`, the session's issues; with none, the session itself. As `$3.00`, `in=412.0k out=38.0k` and `47m`. |
| `Co-authored-by` | The runtime's own line for GitHub's commit page, where it is known: Claude's, `Claude Opus 5.5 <noreply@anthropic.com>`. |

At most two forge calls a PR, each read from the forge cache when it has the answer. A
trailer the rule turns off is not worked out, so it costs no forge call.

**The runtime's own attribution is off.** Each Claude session starts with
`--settings {"attribution":{"commit":""},"includeCoAuthoredBy":false}`, so Claude Code adds no
`Co-authored-by` that the rule turns off, and no second copy of the one `git/trailers` writes.
The daemon knows no such setting for the other runtimes; the worktree's `commit-msg` hook
(#6372) takes out a trailer that the rule turns off, whoever wrote it.

### Commit trailers on main

The app's tool `commit_trailers {pr?}` (#6362) is `git/trailers` for the session that calls
it: a script on the socket is a stranger and cannot call `git/trailers`, so an agent asks with
its own token. It gives the lines, one a line, to write as they come as the message's last
paragraph. A person or a script then reads them with no API call:
`git log --format='%(trailers:key=Cost,valueonly)'`.

GitHub's squash runs no hook, so in this repo a lane writes the squash message itself:
`scripts/automerge.sh <pr> <file>`, with the lines `commit_trailers` gave for the PR in
`<file>`, turns on squash auto-merge with each commit's message (its own trailers taken out,
merges of the base left out) and one block at the end. To that block it adds each commit's
`Co-authored-by` and each commit author but the PR's own, as GitHub's own squash message names
them. It fails on a key that is not a `commitTrailers.<key>` of the wire schema, or `Refs`, so
the keys do not drift. `scripts/rearm-automerge.sh` keeps the message when it turns auto-merge
off and on again; a lane that did more work runs `commit_trailers` and `scripts/automerge.sh`
again, so the totals count it. A PR that a person makes with no agent gets no block.

This repo's `.agents/policy.json` sets `"commitTrailers": {"Cost": "on"}` (Alex, 2026-11-19):
`Cost` shows on its commits unless a person's or the organisation's layer sets it off, which
is tighter.

### The worktree's commit-msg hook

Each worktree that the app makes (new, on a branch, made again, or an older one that an agent
starts in) gets a hooks folder of its own, `.git/worktrees/<name>/agents-hooks/`. The daemon
turns on `extensions.worktreeConfig` and sets `core.hooksPath` with `--worktree`, so the main
checkout and the folder's other worktrees do not change. A repository that keeps `core.bare`
or `core.worktree` in its shared config gets no hook, as git asks. It is a git hook, not a
runtime hook: it never holds an agent's tool use.

- **The folder's own hooks still run.** The folder links `commit-msg`, and each hook's name
  that the folder's own hooks folder has as a program: the last `core.hooksPath` of a scope
  other than the worktree's (as Husky sets it), else the repository's `hooks` folder (where
  lefthook and pre-commit write theirs). Each of those runs the folder's hook of the same name.
  The daemon reads that folder when it puts the hook in, when a session starts in the
  worktree and when it starts (for each worktree that an agent not archived works in), so a
  hook the folder adds later runs from the next session. A name the folder does not have gets
  no link (#6529): git runs `reference-transaction` three times for each ref that a fetch or a
  rebase changes, and with a link each run started four processes, so a fetch of 300 branches
  took 28 s, not 0.15 s.
- **`commit-msg`** runs the folder's own `commit-msg` first; when that refuses the commit, it
  stays refused. Then, in a session, `agents-trailers MSGFILE` (`agentsd trailers`, beside
  `agents-secret` on the session's PATH, with the same grant) asks agentsd for the session's
  `git/trailers` and writes the block as the message's last paragraph: it takes out a trailer
  of a key the rule turns off (`Fixes` also takes out `Refs`), puts the block's `Made-by`,
  `Session`, `Epic`, `Cost`, `Tokens` and `Duration` in place of one already
  there, and adds each other line that is not there yet. Other trailers, such as a person's
  `Signed-off-by`, stay. An editor's comments and `commit -v`'s scissors stay after it.
- **A person's own commit** (no session's grant in the environment) gets no block.
- **When agentsd does not answer**, the message stays as it is and the hook prints one line.
  It never refuses a commit of its own.
- **`git commit --no-verify`** skips it. The squash message (#6362) still applies the rule.

## The signature

`v1/policy.sig` is `{version, keyID, alg: "es256", sig}`:

- `sig` is ECDSA P-256 with SHA-256 over the canonical JSON of the policy (keys sorted, no
  spaces, numbers as written), as 64 bytes: r, then s.
- `keyID` is the first 8 bytes of the SHA-256 of the control key's public key, base64url.
- A machine checks it with the control key it trusts. `controllink.VerifyPolicy` does this
  for `agentsd`.

## Methods

| Method | Who | What |
|---|---|---|
| `policy/mine` | Any person | The policy in force, with only their own raises. |
| `policy/get` | An admin | The policy, its `rev`, and the version each machine keeps. |
| `policy/set` | An admin | Saves the rules with the `rev` that was read. Fails with `changedElsewhere` when another admin saved first. |
| `policy/stopAll` | An admin | Turns the kill switch on or off. |
| `policy/raise` | An admin | Raises a person's budget for today. |
| `control/policyChanged` | To every client | The policy changed. |
| `control/stopAll` | To every client | The kill switch was turned on or off. |
| `machine/policy` | To a machine | The signed policy, or `{none: true}`. |
| `machine/policyApplied` | From a machine | The version it keeps. |

## On a machine

A machine says `host.policy/1` when it follows the policy (slice 2a). How it follows it:
[Organisation policy on a machine](../explanation/organisation-policy.md).

| File or method | What it is |
|---|---|
| `<root>/policy.json` | The newest signed policy, as `{policy, signature}`, 0600. |
| `<root>/policy-seen.json` | The highest version applied, and the control key before a rotation, trusted 30 days. |
| `<settings>/runtime-environment.json` | The person's own pass-through names: `{names: [name], runtimes: {<id>: [name]}}`, `*` at the end for a prefix. |
| `daemon/policyState` | The policy as the machine follows it: `version`, the rules with only this person's raises, `stopAll`, `noPolicy`, `update` (the capabilities it lacks) and `refusal`. `daemon/policyStateChanged` says each change. |
| `settings/passThrough` | The person's pass-through names, each marked with `setBy` when the policy refuses it. |

The lists marked with `setBy` (`{by: "organisation", rule, words}`): `runtimes/list` on each
runtime, each mode and model value of an agent's options and of `agents/options`,
`sandbox/state`, `cost/state` (with `organisation`, the budgets in force today), and
`helperLimitsSetBy` on a folder. Each roll-up row of `usage/rollup` has `limitTokens`.

A refusal is the error `policyRefused` (-32076). Its data is a `PolicyRefusal`: the reason
(`policy`, `budget`, `stopAll`, `noPolicy`, `mustJoin` or `update`), the rule, and the
words for the client.
