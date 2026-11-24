---
diataxis: explanation
devices: [mac, iphone, ipad, web, server]
description: "What each store of Agent Loops keeps, how long and how large, when that bound is applied, how it is backed up and restored, and what is not backed up today."
---

# Data and retention

Agent Loops keeps plain files, not a database. Most stores have a bound: a time, a count or a
size. Some have none. A conversation is kept for 90 days and 10 MB at most. This
page lists the stores, then says what a backup holds and what it does not.

**Kind** is how the code reads and writes a store: document, log, time series, graph, blob,
key-value, cache or secret. [How data stores stay bounded](../data-stores.md) says what each
kind is. The kind is not the file format. A store that mixes kinds names each one; it is to be
split. The bounds here are what the code applies today. Where a store has no bound, its row
says **None**, and the issue that will set one; this page does not guess a limit.

Engineers: the data inventory, `specs/data-inventory/inventory.md` (#3653), names the code
behind each bound and lists the clients and the memory as well.

**When a bound is applied** matters as much as the bound. A store pruned **on write** or
**hourly** stays near its bound. A store pruned only **at start**, **on read** or **on the
next use** can stay over it for as long as nothing starts, reads or uses it. Some bounds
count **keys**, not days: "90 days" means the newest 90 days that have data, so an idle
machine keeps older data.

## On each machine

A machine's data folder is `~/Library/Application Support/Agents` on a Mac and
`~/.local/share/Agents` on Linux (`~/.agents-server/root` on a server installed by its join
command). Only its user can read it (mode 0700). Its cache folder is
`~/Library/Caches/com.alexecollins.agents` and its log folder `~/Library/Logs/Agents` on a
Mac.

### Agents and conversations

| Store | Kind | What it keeps | How long, how large | Applied |
| --- | --- | --- | --- | --- |
| `agents/<id>/agent.json` | document | Each agent's record | Until the agent is deleted. An archived agent is deleted after **Archived agents** (7, 14, 30 or 90 days; 30 by default). A pinned agent is kept for longer than the setting, but not for longer than 90 days after it was archived (#6677). A record of an agent that is not archived stays, without the lines that aged out. | At start, then hourly |
| `agents/<id>/transcript.jsonl` | log | The whole conversation. | 10 MB a conversation and 90 days, live and pinned agents too (#6677, #6706). The oldest lines go, with no summary. One entry is 4 MB at most: a larger one is kept as a notice. | At start, then hourly; soon after a write that passes 20 MB |
| `agents/<id>/app-tools.jsonl` | log | Each call to the app's own tools. | 2 MB an agent and 90 days. Each argument and the answer are cut to 2,000 characters; one call is 1 MB at most. | At start, then hourly; soon after a write that passes 4 MB |
| `agents/<id>/said.json` | cache | A search index of what was said. | 32 KiB, with the agent. Made again after its transcript loses lines. | On write |
| `turns/<id>.jsonl`, in the cache folder | cache | A summary of each turn. | Only the turns of the transcript kept, with the agent. Deleted when its transcript loses lines, and made again from it when it is missing. | With the transcript |
| `erasures.json` | document | Runtime copies of deleted agents still to erase | 1,000 entries, 3 tries, 7 days | On write |
| `erased.json` | document | Every agent deleted here | 10,000 IDs of 128 bytes each, for 90 days; the oldest goes first (#6715) | Hourly, and on write: age, and the count when it is twice over |
| `elsewhere.json` | key-value | Sessions started on or from other machines | 800 entries of 64 KiB each, for 90 days from when the two machines last spoke of the session, so one in use stays; the one heard of the longest ago goes first | Hourly, and on write: age, and the count when it is twice over |
| `requests/<id>.json` | document | Open questions and forms | 1,000 records of 256 KiB each, and 90 days from when it was asked, open too (#6718). Ended ones 14 days; the oldest ended one goes first. An open one is never evicted: a new one past 1,000 open is refused. At 90 days an open one is dropped, and the session that asked is told once. An answer is cut to 16 KiB, and 100 waiters are listed. | Hourly and at start; on write when it is twice over the count |
| `archived-summaries.json`, in the cache folder | cache | An index of the archived agents | With the agents. Made again when it is missing. | On write |
| `revisions.json` | log | Which records changed and went | 2,000 removals, 7 days | On write |
| `alive.json`, `background.json` | key-value | When the machine last ran, and the commands that outlive a stop | One of each | On write |

### Events, usage, cost and lineage

| Store | Kind | What it keeps | How long, how large | Applied |
| --- | --- | --- | --- | --- |
| `events.jsonl` | log | The machine's events | 7 days, and 10,000 events (pruned when it reaches 10,500). The consequences of an event have no count limit. | At start, hourly and on write |
| `events-state.json` | key-value | Branch tips that `branch.moved` compares, cost crossings, disk levels | The live folders, and today only | On write |
| `usage-history.json` | time series | Usage and cost | The last 168 hours and 90 days, by date | On write and hourly (#3686) |
| `time-history.json` | time series | Time, recaps and records | The last 168 hours, 90 days and 90 recaps, by date | On write and hourly (#3686) |
| `spend.json` | time series | Spend for the cost limits | The last 7 days, by date | On write and hourly (#3686) |
| `report-hours.json` | time series | Hourly totals sent to the control plane | 8 days | On write |
| `features-history.json` | time series | How often each feature is used | The last 90 days, by date | On write and hourly (#3686) |
| `lineage.jsonl` | log; graph | Links between issues, PRs, sessions and deploys, and each session's changes of state (#4274) | Ended links 90 days, 20,000 links; changes of state 90 days (each session's latest kept), 20,000 | When it opens, when it grows by 2,000, and hourly (#3649) |
| `lineage-cost.json` | graph | Cost of each lineage node | 90 days, 5,000 nodes | On write |
| `lineage-sync.json` | key-value | Lineage links and session states not yet sent to the control plane (#4275) | 2,000 links and 5,000 state changes; past that, it keeps only a note to send everything again from `lineage.jsonl` | On write |
| `ci-timings/sync.json` | key-value | The days of build timings not yet sent to the control plane, for each repository (#5232) | 90 days of at most 20 repositories | On send |
| `lineage-comments.json` | log | Cost comments on PRs | 500, 30 days | On write |
| `release-tags.json` | log | Release tags already linked, for each repo | 5,000 tags of 1 KiB each, for 90 days; the oldest goes first. Only the newest 50 tags of a repo (#3652), and releases of the last 90 days, are linked, so a tag that goes is not linked again (#6717) | Hourly, and on write: age, and the count when it is twice over |
| `achievements.json` | document | Milestones | A fixed set | — |
| `ci-timings/<forge>/<path>/<day>.jsonl` | time series | CI runs, their jobs and steps | 90 days, 20,000 runs a repository; a pull request run's steps 14 days | On the sweep |
| `lineage-deploys.json` | key-value | Each deploy's last event | 1,000 deploys of 16 KiB each, for 90 days from the last event; the oldest goes first (#6719) | Hourly, and on write: age and count |
| `repo-aliases.json` | key-value | Repository renames | 1,000 renames of 2 KiB each, for 90 days; the oldest goes first. The daily check learns again a rename that a folder or the lineage still names (#6716) | Hourly, and on write: age and count |

### Workflows, assessments, approvals and devices

| Store | Kind | What it keeps | How long, how large | Applied |
| --- | --- | --- | --- | --- |
| `workflows.json`: runs | log | Workflow runs | 7 days, unless its agent still works | At start, when a run ends, and hourly (#3649) |
| `workflows.json`: states | key-value | A state for each folder and workflow ever seen | 30 days after its workflow file goes (#3652) | Hourly, and the app's **Remove** |
| `workflow-copies/` | key-value | The approved text of each workflow and its skills | The copies that the states name; an older version goes when one is saved | On write |
| `assessments/` | document | Each runtime assessment's start, score and scope | 90 days from its start, and the newest 50; the oldest goes first. 256 KiB a file: a larger start or score is refused, and a longer scope file is cut (#6724) | Hourly, and when an assessment starts or is scored |
| `mcp-events.json` | key-value | MCP event IDs already seen | 2,000 IDs, 7 days | On write |
| `mcp-views/` | cache | View catalogues | Pruned to the live servers | On write |
| `mcp-approvals.json`, `plugin-approvals.json` | key-value | Digests of approved MCP servers and plugins | The folders that exist: a folder that is not on disk and not in the app goes (#3652) | Hourly; MCP also on remove |
| `devices.json` | document | Paired devices | 90 days after a device was last seen (#3652) | Hourly, and **Forget** |
| `review-dismissals.json`, `review-opened.json` | key-value | PR links for each folder | 200 a folder; a folder that is not on disk and not in the app goes (#3652) | On write, and hourly |
| `lock-seen.json` | key-value | IDs of signed requests already seen, and the cutoff | 4,096 IDs of 128 bytes each, for 2 days; the one signed first goes first. A request is refused when it is more than a day old, or signed at the cutoff or before it: the latest time signed of the IDs that the count took out (#6723). | Hourly, and on write |
| `attention.json` | document | What needs you, and what was delivered | The live needs | On write |
| `leases.json`, `resources.json` | document | Resource leases | 240 minutes a lease; dropped when its agent stops | On write |
| `guarded-files.json` | key-value | Guarded file states | The folders that exist | On write |
| `org-routes/`, `runtimes/org/` | key-value; secret | Org key routes and their stand-in homes | 50 routes | On delivery |
| `runtimes/accounts/<runtime>/<id>/` | blob | The home of each runtime account you added, **with that runtime's own transcripts** | Until the account is removed | On remove |
| `browser/profile/` | blob | One browser profile for every browser sign-in, and its caches | Sign-ins until the site ends them or you clear the site in Settings ▸ Browser. The caches are emptied before each start, and the HTTP cache stays under 64 MB while it runs (#3727). | At each browser start |
| `folders.json`, `policy*.json`, `backup-settings.json`, `settings/`, and other small files | key-value | Configuration | A fixed shape | — |
| `<file>.unreadable-<time>`, `<file>.corrupt-<time>` | blob | A store that could not be read, set aside | The newest 3 of each kind for each file | On write |
| `connector-setup.json` | key-value | Connector set-up for each folder | Until the folder leaves the machine (#6731) | On remove |
| `catalog-skills.json`, `catalog-mcp.json` | key-value | Installed catalogue items | Until the item is removed (#6732, #6733) | On remove |
| `catalog-pending.json` | document | An install in progress | One; goes when the install ends or at the next start | At start |
| `account-ops.json` | key-value | The last operation of each account, and the last read of each secret | 1,000 entries of 1 KiB each, for 90 days; the oldest goes first | Hourly, and on write: age, and the count when it is twice over |
| `secret-ops.json` | key-value | The last operation of each secret held | Goes with its secret | On write |
| `secret-ops-pending.json` | log | Secret audit entries not yet sent | 1,000 operations of 16 KiB each, for 90 days; the oldest goes first, with a log line, and the audit never has it (#6721) | Hourly, and on write: age, and the count when it is twice over |
| `allowances.json`, `runtime-accounts.json` | key-value | Runtime accounts you added, and each credential's allowance | Until the account is removed (#6747, #6748) | On remove |
| `github-bot-copies.json` | key-value | Which machines have the GitHub bot's key (not the key) | The lock's machines; 16 forgotten Apps | On write |
| `control-clients.json`, `project-tools.json`, `host-user.json` | key-value | Copies of the control plane's lists | Replaced by each new list (#6752, #6753) | On write |
| `wake.json`, `policy-seen.json`, `policy-yours-seen.json`, `lock-pin.json`, `lock.json`, `suspended.json` | key-value | Small settings and state | A fixed shape (#6745, #6751, #6754) | — |
| `setup-reports.jsonl` | log | The steps of this machine's set-up | One run: a new run starts it again | At install |

### Transfers, backups and staging

| Store | Kind | What it keeps | How long, how large | Applied |
| --- | --- | --- | --- | --- |
| `backups/<machine>/` | blob | This machine's backups | The newest **Keep** (1 to 10, 3 by default); a `.tmp` after 24 hours. Another machine's backups in a shared backup folder go 90 days after its newest (#3648). | After each backup |
| `backups-upload/`, `backups-download/` | blob | A server backup on its way | Removed after use | On use |
| `.restore-*` | blob | A restore in progress | Removed when it ends; one a crash left goes at the next start (#3649) | At end; at start |
| `Outgoing/`, `Incoming/` | cache | Exports, sends and imports | 1 hour untouched | On the next transfer, at start and hourly (#3649) |
| `Clones/<id>` | blob | A clone in progress | Removed after the clone | After the clone, and at start |

### Caches and logs

| Store | Kind | What it keeps | How long, how large | Applied |
| --- | --- | --- | --- | --- |
| `daemon.log`, in the log folder | log | What the machine did | 4 MB, and one older file. Output from a crash is checked against the limit within 30 seconds. | On write |
| `mcp-logs/`, in the log folder | log | What each hosted MCP server wrote to stderr | 1 MB and one older file a server, and the newest 32 servers. A larger write is split, so no file passes 1 MB. | When a server starts |
| `tools/<runtime>/`, in the cache folder | cache | Pinned runtime toolsets | The current one | At start |
| `browser/`, in the cache folder | cache | The browser that agents drive, and its output | Old browser versions are left to Playwright. Output goes when the session ends; what a crash left goes at the next start (#3649) | On install; at session end; at start |
| The Mac browser pane's website data (WebKit's default store) | blob | The sign-ins of every site the pane opened, and its caches | Sign-ins until the site ends them. The caches are emptied once each time the app starts (#3727). | At the first pane of each launch |
| `catalog-staging/`, in the cache folder | cache | Skill previews | 8 previews, 30 minutes; folders left by a restart go at the next start (#3649) | On use; at start |
| `option-cache.json`, in the cache folder | cache | Runtime options | 50 | On write |
| Cache and log folders of each other data folder (`roots/<name>-<hash>`) | cache | As above, for a second copy | Removed when that data folder is gone (#3746). Each keeps its data folder's path in `root-path`; a folder made before that is kept, and so is one whose data folder's parent is gone too, as on a disk that is not attached. | Hourly, by a machine whose data folder is not a temporary one |
| Terminal scrollback | log | What a terminal showed | In memory only, 4 MiB a terminal | On write |

### `~/.agents` and `~/.agents-secrets`

| Store | Kind | What it keeps | How long, how large | Applied |
| --- | --- | --- | --- | --- |
| `~/.agents/settings/` | key-value | Settings that follow you (see [The `~/.agents` folder](../../reference/agents-folder.md)) | A fixed shape | — |
| `~/.agents/summaries/` | document | Daily summaries saved before #5853, and `pages.json` with who wrote each: none is written now. A file that is not one of these is yours and stays | The newest 3 of at most 1 MiB, 90 days after their last change; `pages.json` goes with the last of them | Hourly |
| `~/.agents/checks/suggestions.json` | document | Daily-check findings | 50 findings, 200 dismissals | On write |
| `~/.agents-secrets` | secret | Secrets, connections and machine provider secrets (see [Security overview](security-overview.md#where-secrets-live)) | Until you remove them | — |
| `~/.agents-secrets/sealed.json` | secret | Every secret of yours on this machine, sealed to it (#4014, #4177), with Ask before use on or off | Until you remove them | — |
| `~/.agents-secrets/sessions/<agent>.json` | secret | Secrets for one session | The values go when the agent is archived; the file goes when it is deleted | On archive and delete; leftovers at start |
| `~/.agents/dismissed-leads.json` | key-value | The epics you dismissed from New Session's **Suggested** | 500 epics of 1 KiB each, for 90 days; the oldest goes first. An epic that gets a lead goes at once (#6713). In the data folder on a machine with no personal home | Hourly, and on write: age, and the count when it is twice over |
| `~/.agents/mcp.removed.json` | key-value | MCP servers you removed | 20. No age (#6727). | On write |
| `~/.agents-secrets/mcp-sign-ins.json` | secret | MCP sign-in tokens | Until you sign out. No count limit (#6740). | — |
| `~/.agents-secrets/hints.json` | secret | The last 4 characters of each secret | With its secret. No count limit (#6741). | On write |
| `~/.agents-secrets/descriptions.json` | key-value | Secret descriptions, which are not secret | With its secret. No count limit (#6742). | On write |
| Keychain `agents.connection.<hash>` (Linux: `connections.json`) | secret; key-value | Forge connections: the token and its settings | Until you remove the connection. No count limit (#6743). | On remove |

## In each folder

These are in `<folder>/.agents/`.

| Store | Kind | What it keeps | How long, how large | Applied |
| --- | --- | --- | --- | --- |
| `attachments/` | blob | Files attached to prompts | A file that no kept conversation names goes after 7 days. The folder stays under 500 MB, oldest first. A folder with no agent left is swept too: `attachment-folders.json` lists each folder an attachment was written in, until it holds none (#3649). | At start, hourly and on write |
| `dropbox/` | blob | Files handed to a workflow | 25 MB a file and 500 MB a folder from **dropbox/put**; a put past the total is refused. A cut-off put's `.part` after 24 hours. **A file put there is the person's: no age limit** (#3648). | On put; hourly |
| `worktrees/<name>`, and branch `agents/<name>` | blob | Git worktrees that agents made | **No bound, on purpose: they belong to the user** (#6708). The worktree goes when its agent is archived or deleted, merged or not, with its uncommitted and untracked changes (#3726, #3877). The branch goes only when it is merged. A worktree that an agent not archived works in or names, one on no branch, and one the app did not make are always kept. | On archive and delete; when removed |
| `reviews/runtimes/` | document | Runtime assessment reports | 1 MiB a report (a longer one is cut), 90 days, and the newest 50 on the machine, in all its folders; the oldest goes first. A report that git tracks is the repository's, and is never changed or deleted. A file with another name is yours (#6712). | Hourly, and when an assessment starts or is scored |
| `workflows/assess-<id>.md` | document | A runtime assessment's throwaway workflow | **None**; the assessing agent removes it | Never |
| `pins.json` | key-value | Pinned pages, views, sessions and workflows, and hidden views | 10 pages and views, 10 sessions and 10 workflows, 4 KiB a pin; 50 hidden views, 1 KiB each; 128 KiB in all; a write past that is refused. No age, on purpose: they are your choices, committed with the folder. Each stays until you remove it, or its session or workflow is archived (#6725). | On write; on archive |
| `folder.json`, `policy.json`, `mcp.json`, `personas/`, `skills/`, `workflows/` and the like | document | The folder's configuration, meant to be committed | Small | — |

## Outside our folders

Agent Loops starts these tools. It does not own what they keep, so
these have no kind.

| Store | What it keeps | How long, how large |
| --- | --- | --- |
| `~/.claude/projects/` (or `CLAUDE_CONFIG_DIR`) | Claude's copy of each conversation | **Not erased today:** erasure looks in the wrong folder (#3625) |
| `~/.claude/todos`, `file-history`, `session-env`, `debug` | Claude's files for each session | Erased when the agent is deleted |
| `~/.codex/sessions/` (or `CODEX_HOME`) | Codex's copy of each conversation | Erased when the agent is deleted |
| `~/.grok/sessions/`, `~/.copilot/session-state/`, `~/.cursor/acp-sessions/`, and Antigravity's `conversations/` and `brain/` in its own home | Grok's, Copilot's, Cursor's and Antigravity's copy of each conversation | Erased when the agent is deleted (#3647). Copilot's index, `session-store.db`, keeps the session's row |
| OpenCode's `opencode.db` | OpenCode's copies, all in one SQLite file | **Not erased**, unless OpenCode can delete a conversation (ACP `session/delete`). When it cannot, the Delete confirmation says so on every client (#3669) |
| `~/.claude/history.jsonl`, Codex's history and indexes | Each runtime's own history | **Not erased** |
| Docker images and named volumes | What Docker workspaces pulled and kept | **Delete Machine** removes the workspace's container, its volumes and its image, when no other container uses them (#3728). Docker keeps an image with a second tag or a child image. |
| DigitalOcean, Codespaces, EC2 and GCP machines | Cloud machines | **No limit in count.** A linked machine stops after **2 hours** idle by default: no turn running, no answer waited on, no terminal open (#3729). Each machine's **Stop when idle** sets the time, or **Never**, on the Mac and the web. A stopped machine keeps its disk; only **Delete Machine** removes one. |
| `~/.ssh/config` (or `AGENTS_SSH_CONFIG`) | A block for each codespace | A codespace's block goes when **Delete Machine** deletes it (#3746). A codespace deleted outside Agent Loops keeps its block. The one block that agentsd keeps for all codespaces is written again each time it prepares ssh. |
| `/tmp/agents-tunnels-<uid>-<pid>` | ssh control sockets | Removed at a clean stop; one whose process is gone goes when a control plane's tunnels start (#3649) |
| `$TMPDIR/agents-output-*` | Output that the Mac window opened in TextEdit | 1 day: the Mac window removes older copies when it starts (#3747) |

## On the control plane

The control plane's store is a folder or an S3-compatible bucket. **It holds conversation
data:** servers' backups (their transcripts, sealed), each machine's last list of folders
and agents (with agent titles), prompts waiting for a stopped workspace (with their
attachments), lineage nodes (each issue, PR and session it links, with its cost), and a
copy of each machine's lineage links and its sessions' changes of state (refs, states and
times only: no titles or text; #4275).

One sweep runs every hour. It removes old audit days, codes, tombstones, records with no
owner, usage, workflow runs and lineage, join, lock-join, sign-in and link requests, and
policy, governance and remove requests (#3649), and once a day the backups of removed
servers. It also forgets clients whose key expired (#6757), and machines with no workspace whose key expired (#6758), and removes
the approvals of people and groups that are gone, and probes and move marks left by a
copy that stopped (#3748).

A removed client, machine, group, project, route or machine provider leaves a **tombstone** for 7 days, so
the other copies of the control plane learn that it went. A removed person's stays longer (below).

### People, machines and clients

| Records | Kind | What they keep | How long, how large | Applied |
| --- | --- | --- | --- | --- |
| Clients | document | Name, kind, public key, when last seen, push tokens (APNs, up to 16 Live Activity tokens of 12 hours each, Web Push) | Until forgotten, then a tombstone for 7 days. A key lives 90 days at most from when it was paired or last renewed in use; a client whose key expired is forgotten (#6757), unless it is connected. 16 KiB a record and 1 MiB in all (hosted: 5 or 50 clients too): a new client, a longer name or a push token over that is refused; a renewal of a client in use never is. Tombstones keep no push tokens, and may come to 1 MiB more | Hourly; sizes on each write |
| Machines | document | Name, key, platform, version, owner, workspace | Until removed, then a tombstone for 7 days that keeps no labels, links or capabilities. A key lives 90 days at most from when it was joined or last renewed in use; a machine whose key expired is removed (#6758), unless it is connected, or linked to a provider's workspace, which its person could still start or delete: that one stays until a person removes it. 32 KiB a record and 1 MiB in all (hosted: 2 or 20 machines too): a new machine, a label or a lend over that is refused; a hello of a joined machine is refused only over 32 KiB a record, and a renewal never is. Tombstones may come to 1 MiB more | Hourly; sizes on each write |
| People | document | Name, role, sign-ins (email, login), SCIM data, old names for 90 days | A removed person goes after 14 days. One with a sign-in goes after 90 days; until then only a tombstone is kept: the ID, kind, role and each sign-in's provider and account (#3647, #6756). The local owner's tombstone, one record, stays while they are the control plane's owner, so that a removed owner is not an admin again | Hourly |
| Join cutoffs | document | A provider's ID and a time: when a removed person's tombstone with a sign-in of that provider first aged out. From then on, an account of it that nobody has needs an invite, so the old sign-in stays refused (#6756) | 1 KiB a record; Google and 5 org providers. Until the provider is removed | On write, and when the provider is removed |
| Groups, projects | document | Name and members; name, owner, slots and boards | 1,000 groups; 500 projects a person | Tombstone, hourly |
| Projects' folders | document | Each machine's joined folders | 1,000 | Hourly |
| Machine locks | log | Each person's lock chain | 4,096 entries a person. Deleted when the person is removed (#3647) | On **Reset** and on remove |
| A person's settings and secrets | key-value; secret | Settings that follow a person; secrets, sealed | 256 KiB of settings, 200 secrets of 16 KiB | When the person is removed |
| Channel links | document | Which person a channel identity is | No count limit | On unlink or when the person is removed |
| Machine set-up | document | Each machine's set-up steps | One a machine, deleted when the machine is removed. No age. | On remove |
| Machine set-up times | time series | How long each set-up step took, and when it ended | 90 days, 50 a step (#6760), 32 KiB at most. No roll-up. | On write, hourly |
| Machine versions | log | Each machine's version and its updates | 20 updates a machine, deleted when the machine is removed. No age. | On write; on remove |
| Machine names | key-value | A person's machine counter and last choices | One a person, 64 KiB at most: choices past that are not kept. The counter has no age while the person is here, so that a number is never used twice. Deleted when the person is removed (#6759). | On write; on remove; hourly |
| Approvals | document | Each person's signed approvals of workflows and connectors | 2,000 a person; a new one past that is dropped. No age, and not deleted with the person. | On write |

### Requests, codes and sign-in

| Records | Kind | What they keep | How long, how large | Applied |
| --- | --- | --- | --- | --- |
| Codes | document | A code's purpose, owner and expiry, and an invite's name and role; never its secret | Deleted 24 hours after it expires. 1,000 codes; a new one over that is refused (#3748). | Hourly |
| Join, lock-join and sign-in requests | document | The asking key and its address | 10 minutes; 50, 200 and 50 open | On read and hourly, 1 hour after expiry |
| Link requests | document | A channel link that waits | 24 hours; 50 open | On read and hourly, 24 hours after expiry |
| Sign-in state | secret | PKCE state and sealed refresh tokens while a sign-in runs | 10 and 2 minutes; 1,000 sign-ins started, each with at most one ticket (#3748) | Hourly, 1 hour after expiry |
| Sign-in settings | key-value; secret | Org OIDC providers, domain claims, one sealed refresh token a person and provider | 5 providers, 20 domains | On remove |
| SCIM and read tokens | secret | Token hashes, address of last use | 2 live of 20; 10 live of 50 | On write |
| Policy, governance and remove requests | document | Asks for a budget, a catalogue item, or removal of a person | 1,000; a decided one 30 days | On read and hourly |

### Work data

| Records | Kind | What they keep | How long, how large | Applied |
| --- | --- | --- | --- | --- |
| Kept requests | document | Prompts and agent starts for a stopped workspace, **with their attachments** (up to 40 MiB each) | Waiting requests expire after 90 days at most; failed requests expire after 7 days. At most 20 requests and 800 MiB per machine. The control plane refuses a new request at either limit; it does not evict a person's request (#3647, #6674) | On delivery, on keep, hourly |
| Host copies | cache | Each machine's last folders and agents, up to 500, **with titles** | One a machine | When the machine is forgotten |
| Usage reports | time series | Usage, cost and time by person, project, folder, machine and runtime | 7 days by the hour, 90 days by the day | Hourly |
| Lineage | graph | Cost of each issue, PR and session node | 5,000 nodes, 90 days | On write |
| Lineage graph | graph; log | Each machine's links and its sessions' changes of state (working, waiting, needs-you, stopped, archived, deleted), as refs, states and times, in `v1/lineage-graph/<machine>/` (#4275); each send in a small file until a roll-up (#6584) | Links as on the machine: an ended link 90 days after its end, 20,000 a machine. States as on the machine: 90 days, and each session's last state whatever its age, 20,000 a machine. At most 32 sends before a roll-up. A removed machine's a day after its last send | On write, hourly |
| Deploys | document | Each machine's deploys, as refs, names, states and times: the component, the environments and their tiers, the end, whether it is a rollback, the merge times of the PRs it brings and the health signals seen in it, in `v1/deploys/<machine>.json` (#5496). No titles, no logs | As on the machine: 1,000 deploys; each send replaces the last. A removed machine's goes a day after its last send | On write, hourly |
| Build timings | time series | Each machine's CI runs of a repository, with their jobs and steps, as refs, times, attempts, conclusions, runner labels and names, in `v1/build-timings/<machine>/` (#5232). No commit or PR titles, no logs | As on the machine: 90 days, 20,000 runs a repository, a pull request run's steps for 14 days. A machine that stopped sending is bounded once a day; a removed machine's goes a day after its last send | On write, hourly |
| Workflow runs | log | Which machine took each workflow trigger | 7 days | Hourly |
| Report views | key-value | Saved report views | 50 a person | When the person is removed |
| Machine providers | document | Each provider | No count limit. A removed one's tombstone for 7 days (#3748) | Hourly |
| Tunnels | document | Servers' reverse tunnels | 15 minutes unjoined | On use |
| Policy | key-value | The org policy and its old versions | 64 KiB; 100 versions | On write |
| Governance | document | Installed servers and skills; approvals | 500 items; 1,000 approvals. An approval stays until an admin takes it back, or its person or group is gone (#3748) | Hourly |
| Org keys and gateways | key-value; secret | Routes and their sealed secrets | 50 routes | Tombstone, hourly |
| Warm pools | document | Pools of ready machines, and their claims | No count limit (#6761) | On remove |
| Failed machine makes | log | Why a machine could not be made | 5 a provider, deleted with the provider. No age. | On write; on remove |
| Event bus | log | Events between a person's machines: refs, names and times, and sealed data | 90 days; 100,000 envelopes and 1 GiB of encoded log records a person, the oldest deleted first with their dedupe ids. An envelope is at most 24 MiB encoded: 64 sealed copies of 256 KiB, in base64. Deleted when the person is removed (#6671). | On write; hourly; on remove |
| Budgets | key-value | The org's budgets | One (#6778) | On write |
| Feature maturity | document | Evidence for each feature's rating | 84 records of 4 KiB. No age. | On write |

### Audit, keys and backups

| Records | Kind | What they keep | How long, how large | Applied |
| --- | --- | --- | --- | --- |
| Audit events | log | Who did what (see [Security overview](security-overview.md#who-did-what)) | 90 days by default; an admin sets 1 to 90 days (hosted: 7 or 90). At most 1,000,000 entries, of at most 16 KiB each: the oldest over that go, even when they are newer than the days set. A longer text in an entry is cut. No roll-up: the long copy is the SIEM sink's (#6755). | Hourly, oldest first; before the hour at 2,000,000 entries |
| Change events between copies | log | What changed, when the control plane has several copies | 1 day, in `v1/mesh/events/` (#3648) | Hourly, by day |
| Audit seals and sinks | log; key-value | Each day's chain head; up to 4 SIEM sinks with sealed tokens | With the day; 4 sinks | Hourly |
| Keys | secret | The sealed control key and the previous one; the sealed key for server backups | 2; the previous one 30 days | — |
| Server backups | blob | Servers' sealed backups (their transcripts) | The newest **Keep** of each server (1 to 10). At most 20 GiB a server, the newest always kept; a backup over 20 GiB is refused (#3648). A removed server's for 90 days after its newest. A cut-off backup's parts after a day; a name more than a day ahead is refused, and its parts deleted. | After each backup, and daily |
| Leases and heartbeats | document | Which copy holds what | 30 seconds | Hourly |
| Move forwarding and marks | document | Where a moved control plane went | Forwarding 30 days. A mark, and a start-up probe, left by a copy that stopped: 1 to 2 hours (#3748) | Hourly |
| Control settings | key-value | The control plane's owner, addresses and settings | One (#6771) | On write |
| Release keys | secret | Each Linux machine's release key, sealed, and the approvals taken | Deleted with the machine; an approval taken goes after twice its life (#6672) | Hourly |

## The hosted service

On the hosted service, each account has a control plane store of its own (above), and the
service keeps these records.

| Records | Kind | What they keep | How long, how large |
| --- | --- | --- | --- |
| Account | document | Sign-in provider (GitHub, Apple or Google) and its user ID, login, plan, suspension, certificate time, **Stripe customer and subscription IDs**, the date it signed up | Until **Delete Account**. 100 accounts. |
| Sign-in links | document | Which account a GitHub, Apple or Google user is | Until **Delete Account** |
| Account usage | key-value | The bytes the account's store holds | Until **Delete Account** |
| The account's control plane store | all of the kinds | All of the section above | Plan limits: 1 or 20 GB, 2 or 20 machines, 5 or 50 clients, audit 7 or 90 days. Until **Delete Account**. |
| Invites | document | Expiry, and **who used it** | 14 days to use it. Kept after use, until **Delete Account**. |
| Waitlist | document | Who asked to join | 10,000; removed on sign-up |
| Stripe events | key-value | Stripe event IDs already seen | 30 days, swept daily |
| Operator backups | blob | Each account's store, on the operator's disk | The newest 7 by default; `--keep N` sets it, `--keep 0` keeps all (#3648) |
| Email links | key-value | Which account a sign-in email is | Until **Delete Account** (#6785) |
| Allow and block lists | key-value | The operator's lists | **None** (#6783, #6784) |

The service keeps nothing for a signed-in browser: a session is a signed cookie for 30 days.

**Delete Account** stops the account's control plane, deletes its store, its usage, the
invites its sign-in used, the account and its sign-in link. It does not delete the
account's machines. It leaves the operator's backups of it, and Stripe's own records.

## Agent Loops on the Mac

| Store | Kind | What it keeps | How long, how large |
| --- | --- | --- | --- |
| `~/Library/Application Support/Agents Control/` | document; secret | The control plane's store, TLS certificate, `secrets-key`, push key, web settings | One of each |
| `Agents Control/store-before-<…>` | blob | A whole old store, set aside when you change the store or move back. Each holds the audit, sealed secrets and server backups. | The newest 2; an older one is deleted when another is set aside (#3671) |
| Keychain `com.alexecollins.agentshost` | secret | The control key, bucket keys, and `control-key-before-<time>` | One of each; the newest 2 `control-key-before-*` items, deleted with the old stores (#3671) |
| `~/Library/Logs/Agents/control.log` | log | What the control plane did | 4 MB, and one older file |
| The Web Push key | secret | The key that signs browser push | One. In `agents-control backup` as `webpush-key.pem` (#3651). |
| `Agents Control/web-off` | key-value | That the web page is off | One marker file (#6781) |

## On each client

What the Mac window and the Remote keep in their defaults, and the web page in
`localStorage`, by folder and by control plane (#3750).

| Store | Kind | What it keeps | How long, how large | Applied |
| --- | --- | --- | --- | --- |
| Sidebar folds (`sidebar.folds`, `sidebar.folded`, each scope; `agents.sidebar.folds`, `agents.sidebar.folded` on the web page) | key-value | Which folders and folds are open | The folders that exist. A folder that its machine no longer lists goes; all of a machine's go when the control plane no longer lists the machine. An offline machine's stay. The Remote lists no archived folders, so their folds go there. | When a machine lists its folders, and when it leaves |
| Open sessions (`sidebar.openSessions`) | key-value | Sessions whose children are shown | 200 | On write |
| `sidebar.folder.<folder>.width`, `.pane` (Mac only) | key-value | A folder window's inspector width and pane | As the folds | As the folds |
| `newSessionProjectFolders` (`agents.newSession.projectFolders` on the web page) | key-value | New Session's last folder for each project | As the folds | As the folds |
| `machineLockPin.<key>` (`agents-lock-pin.<key>` on the web page) | key-value | The machine-lock pins, the lock and each person's | This control plane's only: another control plane's go once the client is paired (#3750), and all go on **Forget** (#3670) | When the control plane answers |
| Drafts (`draft.agent.<uuid>`, `draft.new.<folder>`, and the Mac's `draft.start`) | document | Prompts not sent, with their attachments | 40 drafts, 1 MiB each with at most 512 KiB of attachments: a new one over 40, or one too big, is refused, with a message, and stays only while the app runs (#6794). Deleted after 30 days without an edit, when its agent is archived, on send, and on **Forget** (#3749). | On write, at start, on archive |
| Drafts on the web page (`agents.drafts`) | document | Prompts not sent, with their attachments | 40 drafts: a new one over that is refused, with a message, and stays only while the page is open. Attachments only while they are under 256 KiB. Deleted after 30 days without an edit, on send, and on **Forget** (#6793). | On write, at start, hourly |
| `control-client.json` (Mac, Remote); IndexedDB `agents` (web) | key-value; secret | The pairing, and the web page's key | One, until **Forget** (#6800) | On **Forget** |
| Keychain `control-client-key`, `lock-signing-key`, `device-key` (Mac, Remote) | secret | The client's own keys | One of each; kept after **Forget** | — |
| `copy/` in the cache folder (Mac, Remote) | cache | The local copy: machines, folders, records, the ends of 20 chats and Work's pictures | 90 days since it was read from its host. 200 MiB on the Mac, 50 MiB on the Remote, the oldest out first. A record is 16 MiB at most. A machine that the control plane no longer lists goes, with its chats. | On write, hourly, when the machines are listed, and on **Forget** |
| IndexedDB `agents` › `copy` (web) | cache | The local copy: machines, folders, records, the ends of 20 chats and Work's pictures | 90 days since it was read from its host. 50 MiB, the oldest out first. A record is 16 MiB at most. A machine that the control plane no longer lists goes, with its chats. | On write, at sign-in, hourly, when the machines are listed, and on **Forget** |
| `work-cache.sqlite`, in the cache folder (Mac, Remote); IndexedDB `agents` › `work` (web) | cache | Forge items, bodies, comments and Work page answers | 90 days since it was read from the forge. 200 MiB on the Mac, 50 MiB on the Remote and the web page, least recently shown out first. An item is 4 KiB and any other record 4 MiB at most. | On write, at start, hourly, and on **Forget** |
| `shared-inbox.json` (Remote) | blob | What was shared to Agent Loops | Each attachment is capped. No count, total or age (#6795). | On use |
| `attention-snapshot.json` (Remote) | cache | The widget's rows | 12 rows; stale after 15 minutes | On write, and on **Forget** |
| `remote.startFolders` (Remote) | key-value | Start folders | **None** (#6798) | Never |
| `machinePicker.pinned` (`agents.machinePicker.pinned` on the web page) | key-value | Pinned machines | 100 machine ids of 64 bytes each: one past that is refused, with a message. No age, on purpose: a pin is your choice. It stays until you remove it, or until the control plane no longer lists its machine. An offline machine's stays (#6797). | On write, and when the machines are listed |
| `voiceMode.*`, marketplaces, working marks, `reviewDiffLayout`, `agents.startForm` and other choices | key-value | Choices on this client | A fixed set (#6802 to #6806) | — |
| MDM profile (Mac) | key-value | The control plane an org pins a Mac to | Read only; set by the org (#6801) | — |
| `crashes/`, `hangs/`, in the log folder (Mac); `remote.log` (Remote) | log | Crash and hang notes; the device log | 20 of each; 256 KiB and one older file | At start; on write |
| `~/.config/loops/hosts.json`, `lock-keys/` | key-value | Pairings of `loops` | One a control plane (#6807) | On unpair |
| `~/.agents-go/ctl.json` | key-value | The pairing of `agentsctl` | One. No unpair (#6808). | Never |

## In memory

What a process holds while it runs, and goes when it stops. Each has a bound (#3650).

| Process | What it holds | How large |
| --- | --- | --- |
| Machine | A session's running cost, its views' calls, its undo notes | Until the agent is archived or deleted; 64 view calls and 21 undo notes an agent |
| Machine | A terminal a runtime asks for | The limit the runtime asks for, 4 MiB at most |
| Control plane | A store copy, backup or handover | One record at a time, and a hash of each record to check the copy |
| Control plane | A socket's lines waiting to go | 8,192 lines and 64 MiB; a slower socket is closed |
| Control plane | Records read for reports and the Console | 40,000 records and 256 MiB |
| Control plane | Notifications posted to devices | A week, and 10,000 at most |
| Control plane | Presence reported at another copy | Until no copy links the client |
| Mac, Remote, web | A chat's history | 600 to 800 entries while it follows the end; paging back stops at 1,600 entries and 200 turns |
| Mac, Remote, web | Events | 1,000 as new ones come; paging back stops at 5,000 |
| Mac, Remote | Pictures in a page | 64 MiB |

## Erasure

**Delete** works on an archived agent. It stops the agent's runtime, takes back its tokens,
and deletes its folder (record, conversation, tool log, search index) and its turn
summaries. Then (T8, #1001):

- **The runtime's own copy goes.** The machine starts the runtime as the agent ran (with
  its account) and asks it to delete the conversation (ACP `session/delete`). For Claude
  and Codex it also removes the session's files in the runtime's folder (`~/.claude` or
  `CLAUDE_CONFIG_DIR`; `~/.codex` or `CODEX_HOME`). Today Claude's conversation file stays
  (#3625). This waits in `erasures.json` in the data folder: 1,000 at most, done one at a
  time, and tried again at the next start when the runtime does not answer, 3 times at
  most within 7 days.
- **A restore never brings the agent back.** `erased.json` in the data folder names every
  agent the machine deleted: 10,000 at most, each for 90 days. A backup carries it, and a
  restore skips each agent in it, also on a new machine. When an ID ages out, the backups
  that can hold its agent are too old to restore: a machine refuses a backup or an export
  made 90 days ago or more, or, when the list is full, one made no later than the oldest
  deletion that the list keeps (#6715).

These stay behind:

- the copy of a runtime whose files are not known and that cannot delete a conversation
  (the Delete confirmation says so, #3669);
- attachments, until the next sweep finds no conversation that names them;
- the agent's events, until they are 7 days old;
- the agent's title in its machine's host copy on the control plane, until a window next
  lists that machine's agents;
- its lineage nodes and cost, until they age out (90 days), and its links and states in the
  control plane's lineage copy (its last state says `deleted`), until they age out (90 days);
- the backups that hold it, on the machine and on the control plane, until they age out (a
  restore skips it).

**Forget** removes a client's pairing: **Forget This Mac**, **Forget This iPhone**,
**Forget This Browser**, or the control plane when it forgets the client. The client then
also deletes what it kept of that control plane (#3670):

- **The Mac window:** its drafts and the start form, its machine-lock pins, its pairing
  files (including the saved pairing with this Mac's own control plane), its local copy,
  and its Work cache.
- **The Remote:** its drafts, its machine-lock pins, the widget's file
  (`attention-snapshot.json`, with its headlines) and the badge, the Live Activity and its
  turns, and every banner it showed.
- **The web page:** its drafts and machine-lock pins in `localStorage`, and its Web Push
  subscription.

Each client keeps its machine-lock pins by control plane (`machineLockPin.<key>` on the Mac
and the Remote, `agents-lock-pin.<key>` on the web page, #3651), so a pairing with another
control plane does not read the old one's pins.

The Mac and the Remote keep their own key in the keychain, so that a new pairing can use
it. The web page deletes its key.

A walk's Mac window (run-app, `--walk <name>`) keeps its own keys,
`control-client-key.walk.<name>` and `lock-signing-key.walk.<name>`. When its folder has
gone, the next Mac window to start deletes them with the walk's defaults (#3747).

Voice mode's Kokoro models are in the app. The Mac window and the Remote link them into
FluidAudio's cache when voice mode loads, and remove there what the app no longer carries,
such as an older model (#3747). Only the current version stays.

## Export and import

**Export…** in an agent's menu, and **Export Agents…** in a folder's, on the Mac, the
Remote and the web page, give a `.tar.gz` of the agent, or of every agent of the folder
(T8, #1001). It is in the backup format: each agent's record, conversation, tool log and
search index. It holds no turn summaries (the machine makes them again from the
conversation), no secret, no usage history and no other agent.

**Import Agents…** in a folder's menu sends the file to that folder's machine. Its agents
come back archived, to read and search. They are never run there, and bring no MCP servers.
An agent that the machine has already, or that it deleted (`erased.json`), is skipped. An
export made 90 days ago or more is refused, as a backup is.

The machine makes an export in its `Outgoing` folder and an import arrives in `Incoming`.
Each goes once it is read or imported. One left untouched for an hour goes at the next
export, send or import, at the next start, or at the hourly sweep.

## Backups

See [Back up and restore a machine](../../how-to/back-up-and-restore.md).

- **A machine** backs up every day by default, and keeps the newest 3 (1 to 10). A backup
  holds each agent's record, conversation, tool log and search index, and the usage and
  time history and the list of deleted agents (`erased.json`). It holds no sign-in, key or
  secret, and no turn summaries: the machine makes them again from the conversation.
- **A Mac** backs up to a folder, `backups` in its own data folder by default. That is the
  same disk: it undoes a mistake, but does not survive the loss of the Mac. Choose a second
  disk or a synced folder. Local backups are not encrypted.
- **A server** backs up to the control plane's store, sealed. The copy on the server is
  deleted once it is sent.
- **Restore** brings agents back archived, to read and search. An agent from another machine
  is shown, never run.

## What is not backed up today

The first HADR review (2026-10-21, #1557) found these. Each one is an open issue.

- **The control plane in the cloud is one VM with one disk**, with no snapshot and no copy
  (#2136). Losing that disk makes every device and machine pair again, and loses every
  server backup in its store.
- **A copy of the store does not restore a control plane on its own** (#2137). A restore
  also needs the control key, the `secrets-key` and the TLS certificate from the old home.
  With all of them, a machine joined again in 26 seconds in the review's drill.
  `agents-control backup` also holds the Web Push key (#3651), so browser push goes on
  after a restore.
- **Machine providers' secrets are on the machine that carries each provider** (#3398), not
  on the control plane. A copy of the store does not carry them, and nor does a machine's
  backup: when that machine is lost, add the provider again on another one.
- **A machine's backups are on its own disk by default** (#2139).
- **Not in a machine's backup:** turn summaries (made again), settings, workflows, secrets,
  attachments, events, lineage, approvals and the runtimes' own session files. A folder's
  `.agents/` folder is in the folder, and so in its git repository if you commit it.
- **Writes that are not synced.** Appends to a machine's events, its records' logs and its turn summaries
  are not synced to disk one by one, so a power cut can lose the last lines.

## Related

- [Data flow](data-flow.md)
- [Security overview](security-overview.md)
- [Back up and restore a machine](../../how-to/back-up-and-restore.md)
- [The `~/.agents` folder](../../reference/agents-folder.md)
