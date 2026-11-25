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
| `agents/<id>/agent.json` | document | Each agent's record | Until the agent is deleted. An archived agent is deleted after **Archived agents** (7, 14, 30 or 90 days; 30 by default). A pinned agent is kept for longer than the setting, but not for longer than 90 days after it was archived (#6677). A record of an agent that is not archived stays, without the lines that aged out. At most 1 MiB: of a larger record, the large parts are cut (commands, options, plans, prompts, labels, then long titles) and the log says so; the session is never dropped and its save never refused (#7244). | At start, then hourly |
| `agents/<id>/transcript.jsonl` | log | The whole conversation. | 10 MB a conversation and 90 days, live and pinned agents too (#6677, #6706). The oldest lines go, with no summary. One entry is 4 MB at most: a larger one is kept as a notice. | At start, then hourly; soon after a write that passes 20 MB |
| `agents/<id>/app-tools.jsonl` | log | Each call to the app's own tools. | 2 MB an agent and 90 days. Each argument and the answer are cut to 2,000 characters; one call is 1 MB at most. | At start, then hourly; soon after a write that passes 4 MB |
| `agents/<id>/said.json` | cache | A search index of what was said. | 32 KiB of messages and a tail of at most 32 KiB, with the agent. Made again after its transcript loses lines. | On write |
| `turns/<id>.jsonl`, in the cache folder | cache | A summary of each turn. | A turn's line is at most 64 KiB: a larger one is cut. Only the turns of the transcript kept, with the agent. Deleted when its transcript loses lines, and made again from it when it is missing. | With the transcript |
| `erasures.json` | document | Runtime copies of deleted agents still to erase | 1,000 entries, 3 tries, 7 days | On write |
| `erased.json` | document | Every agent deleted here | 10,000 IDs of 128 bytes each, for 90 days; the oldest goes first (#6715) | Hourly, and on write: age, and the count when it is twice over |
| `elsewhere.json` | key-value | Sessions started on or from other machines | 800 entries of 64 KiB each, for 90 days from when the two machines last spoke of the session, so one in use stays; the one heard of the longest ago goes first | Hourly, and on write: age, and the count when it is twice over |
| `requests/<id>.json` | document | Open questions and forms | 1,000 records of 256 KiB each, and 90 days from when it was asked, open too (#6718). Ended ones 14 days; the oldest ended one goes first. An open one is never evicted: a new one past 1,000 open is refused. At 90 days an open one is dropped, and the session that asked is told once. An answer is cut to 16 KiB, and 100 waiters are listed. | Hourly and at start; on write when it is twice over the count |
| `archived-summaries.json`, in the cache folder | cache | An index of the archived agents | With the agents. 64 MiB, and 256 KiB for a summary: one over is left out (the newest are kept) and its record is read in full at the next start (#7244). Made again when it is missing. | On write |
| `revisions.json` | log | Which records changed and went | 2,000 removals, 7 days | On write |
| `alive.json`, `background.json` | key-value | When the machine last ran, and the commands that outlive a stop | One of each | On write |

### Events, usage, cost and lineage

| Store | Kind | What it keeps | How long, how large | Applied |
| --- | --- | --- | --- | --- |
| `events.jsonl` | log | The machine's events | 7 days, and 10,000 events (pruned when it reaches 10,500). The consequences of an event have no count limit. A line is at most 64 KiB: a larger one is cut and the log says so (#7244). | At start, hourly and on write |
| `events-state.json` | key-value | Branch tips that `branch.moved` compares, cost crossings, disk levels | The live folders, and today only. At most 256 KiB: past it, the later half of the largest part goes, never the next position (#7244) | On write |
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
| `store-sizes.json` | key-value | The last measurement of each declared store of this machine: its bytes and its count, never a path or what is stored (#6906) | 200 stores of 4 KiB, for 90 days; each measurement replaces the last | Hourly |

### Workflows, assessments, approvals and devices

| Store | Kind | What it keeps | How long, how large | Applied |
| --- | --- | --- | --- | --- |
| `workflow-runs.json` | log | Workflow runs (in `workflows.json` before #6730) | 7 days, unless its agent still works; 90 days at most. 1,000 of 64 KiB each; the oldest goes first. Of a larger run, its trigger is cut, or its earlier form stays (#7184) | At start, when a run ends, and hourly (#3649, #6730) |
| `workflows.json` | key-value | A state for each folder and workflow: its approval, on or off, its standing session, when and how it last ran, the fires it holds | The approval and on or off: while the workflow is there, as they are current state. When and how it last ran, and a fire it holds: 90 days (the last run stays while its cooldown is on). All of it: 30 days after its workflow file goes (#3652). 2,000 states of 64 KiB; past that the one gone longest goes, and with none gone a new one is refused. Who ran a session: until it is archived, 1,000 (#6730) | On write, hourly, and the app's **Remove** |
| `workflow-copies/` | key-value | The approved text of each workflow and its skills | The copies that the states name; an older version goes when one is saved. 1 MiB a file, 50 skills a workflow, 64 MiB in all: an approval past that is refused (#6730) | On write |
| `assessments/` | document | Each runtime assessment's start, score and scope | 90 days from its start, and the newest 50; the oldest goes first. 256 KiB a file: a larger start or score is refused, and a longer scope file is cut (#6724) | Hourly, and when an assessment starts or is scored |
| `mcp-events.json` | key-value | MCP event IDs already seen | 2,000 IDs, 7 days | On write |
| `mcp-views/` | cache | View catalogues | Pruned to the live servers | On write |
| `mcp-approvals.json` | key-value | The answers about MCP servers and their views: digests of the approved and denied ones | 2,000 approved, 2,000 denied and 2,000 views, of 8 KiB each: one more is refused, after the one whose server went the longest ago made room. No age while the server is in its file, on purpose: an approval in force stays. 90 days after the server went, its answers go and are asked again. Those of a folder that is not on disk and not in the app go (#3652). Stays in the data folder (#6749) | On write; on remove; and hourly |
| `plugin-approvals.json` | key-value | The answers about plugins: digests of the approved and denied ones | 2,000 approved and 2,000 denied, of 8 KiB each, with the same rules for a plugin and its folder. Stays in the data folder (#6750) | On write, and hourly |
| `devices.json` | document | Paired devices | 256 of 4 KiB each; a new one past that is refused, with a message, and one in use stays. 90 days after a device was last seen (#3652, #6734) | On write; hourly; and **Forget** |
| `review-dismissals.json` | key-value | PRs dismissed from For Review | 2,000 of 8 KiB each, for 90 days from the PR's last change (or its dismissal, when that is later); the oldest goes first. For Review leaves out a PR that has not changed for 90 days, so one whose dismissal aged out does not come back (#6728). A folder that is not on disk and not in the app goes (#3652) | Hourly, and on write: age, and the count when it is twice over |
| `review-opened.json` | key-value | PRs opened from For Review | 2,000 of 8 KiB each, for 90 days from the PR's last change (or its opening, when that is later); the oldest goes first. A folder that is not on disk and not in the app goes (#3652, #6729) | Hourly, and on write: age, and the count when it is twice over |
| `lock-seen.json` | key-value | IDs of signed requests already seen, and the cutoff | 4,096 IDs of 128 bytes each, for 2 days; the one signed first goes first. A request is refused when it is more than a day old, or signed at the cutoff or before it: the latest time signed of the IDs that the count took out (#6723). | Hourly, and on write |
| `attention.json` | document | What needs you, and what was delivered | The live needs | On write |
| `leases.json`, `resources.json` | document | Resource leases | 240 minutes a lease; dropped when its agent stops | On write |
| `guarded-files.json` | key-value | Guarded file states | The folders that exist | On write |
| `org-routes/`, `runtimes/org/` | key-value; secret | Org key routes and their stand-in homes | 50 routes | On delivery |
| `runtimes/accounts/<runtime>/<id>/` | blob | The home of each runtime account you added, **with that runtime's own transcripts** | The conversations (Claude's and Codex's transcripts, and Claude's files for each session): 90 days after the last write, and 1,200 MB a home; the oldest go first (#6707). Claude's own `cleanupPeriodDays`, in your settings, can remove them sooner. The conversation of a session that is not archived stays, and so does one written in the last 24 hours, so a home can be over its limit for them. The sign-in, the configuration, Claude's memory and all else in the home are the runtime's, and stay until the account is removed. Codex's compressed rollouts stay too, as the app cannot tell whose they are | Hourly; all of it on remove |
| `browser/profile/` and `browser/chrome-profile/` | blob | The agents' browser profiles (Chrome for Testing's and Google Chrome's): every browser sign-in, site storage and caches | A site goes, with its sign-in, 90 days after its last use: a page of it opened, or a cookie of it renewed. Over 200 sites or 256 MB of site data, the sites used longest ago go first, but a site used in the last 30 days stays, so a profile can be over a limit for a time (#6710). Chromium keeps at most 3,300 cookies of 4 KB. A profile whose browser has not run for 90 days goes whole. The caches are emptied before each start, the HTTP cache stays under 64 MB while it runs (#3727), and Chrome's own downloads are emptied before a start when they pass 1 GB. You can clear a site in Settings ▸ Browser. | Hourly while the browser runs, and at each start; the host starts it at most once a day when a site is past 90 days; an idle profile hourly |
| `browser/profile-sites.json`, `browser/chrome-profile-sites.json` | key-value | Each site's last use, its latest cookie expiry and up to 20 of its origins, for the row above | 1,000 sites a profile, at most 7 KB each; an entry goes with its site | With the row above; the count on write |
| `browser/chromium.pid` | key-value | The running browser's process | 1 | On write; removed when it stops |
| `folders.json` | key-value | The folders of this machine: the path of each, when it was added or archived, and its pin | 10,000 folders of 16 KiB each: one past that is refused, with a message. No age, on purpose: a folder is your choice. It stays until you delete it. Not in `~/.agents`, on purpose (Alex, 2026-11-24): each is a path on this machine, and a sync could lose a folder (#6744). | On write; on delete |
| `settings/`, and other small files | key-value | Configuration | A fixed shape | — |
| `<file>.unreadable-<time>`, `<file>.corrupt-<time>` | blob | A store that could not be read, or an old form with an entry too large to carry over (#7182), set aside | The newest 3 of each kind for each file, for 90 days from when it was set aside. 100 MB for each file and kind: past that the older go, and the newest stays (#6737) | On write, and hourly |
| `connector-setup.json` | key-value | Connector set-up for each folder | What a read found (a title, a link): 90 days. Which reads worked, and that the set-up added its connector: no age, on purpose, until the folder leaves the machine, so that a connector's set-up stays done. 2,000 folders of 64 KiB each: past that the one read longest ago goes, and a folder whose record is larger reads again (#6731) | On write, and hourly |
| `catalog-skills.json`, `catalog-mcp.json` | key-value | Installed catalogue items | No age while the item is installed, on purpose, so that it stays known as one from a catalogue; 30 days after an item that is no longer there went. 2,000 in each file, 8 KiB each: one past that is not recorded, and its item shows as added by hand (#6732, #6733) | On write; on remove; and hourly |
| `catalog-pending.json` | document | An install in progress | One; goes when the install ends or at the next start | At start |
| `account-ops.json` | key-value | The last operation of each account, and the last read of each secret | 1,000 entries of 1 KiB each, for 90 days; the oldest goes first | Hourly, and on write: age, and the count when it is twice over |
| `secret-ops.json` | key-value | The last operation of each secret held | Goes with its secret | On write |
| `secret-ops-pending.json` | log | Secret audit entries not yet sent | 1,000 operations of 16 KiB each, for 90 days; the oldest goes first, with a log line, and the audit never has it (#6721) | Hourly, and on write: age, and the count when it is twice over |
| `allowances.json` | key-value | Each credential's allowance, as its runtime said it | 90 days after the last news, and each reading at 90 days; one that is out until a time still to come (90 days ahead at most) stays. 500 credentials of 64 KiB: the oldest goes first. Stays in the data folder: it is derived (#6747) | On write; on remove; and hourly |
| `runtime-accounts.json` | key-value | Runtime accounts you added: the name and the sign-in folder | 100 accounts of 4 KiB: one more is refused, with a message. No age, on purpose: it is yours until you remove it. Stays in the data folder (Alex, 2026-11-24, #6748) | On write; on remove |
| `relay/granted.json` | key-value | A SHA-256 hash of the stand-in token last granted for each runtime whose sign-in this Mac lends (never the token) | 2 entries, one a runtime. No age, on purpose: a machine keeps its stand-in over a restart of the Mac (#7170) | On write, a new grant replaces it |
| `github-bot-copies.json` | key-value | Which machines have the GitHub bot's key (not the key) | The lock's machines; 16 forgotten Apps | On write |
| `control-clients.json` | key-value | A copy of the control plane's list | Replaced by each new list | On write |
| `policy.json` | key-value | The organisation's signed policy | One, of 1 MiB: a larger one applies and is not kept, so no work starts after a restart until it is sent again. No age, on purpose: it is in force until a newer one comes. Stays in the data folder (Alex, 2026-11-24, #6751) | On write |
| `policy-seen.json` | key-value | The highest policy version applied, and the control key before a rotation | One, of 4 KiB. The old key is trusted for 30 days. No age, on purpose: it refuses an older policy. Stays in the data folder (#6751) | On write |
| `project-tools.json` | key-value | The tools of the projects of this machine's folders | 16 MiB: a larger list applies and is not kept. Replaced by each new list. Stays in the data folder (#6752) | On write |
| `host-user.json` | key-value | Whose machine this is | One, of 256 KiB: a larger one is kept without the bots' names. Replaced by each new one. Stays in the data folder (#6753) | On write |
| `lock-pin.json` | key-value | The head of the machine lock that this machine trusts | One, of 4 KiB. No age, on purpose: only a lock that grows from it is taken. Stays in the data folder (#6754) | On write; `agentsd lock reset` removes it |
| `policy-yours-seen.json`, `lock.json`, `suspended.json` | key-value | Small settings and state | A fixed shape | — |
| `setup-reports.jsonl` | log | The steps of this machine's set-up | One run: a new run starts it again | At install |

### Transfers, backups and staging

| Store | Kind | What it keeps | How long, how large | Applied |
| --- | --- | --- | --- | --- |
| `backups/<machine>/` | blob | This machine's backups | The newest **Keep** (1 to 10, 3 by default), for 90 days. This machine's newest good backup always stays, past the count and the age, on purpose: it is the way back. A backup over 10 MB is made, with a line in the log: there is no limit on its size, on purpose, so a machine is never left without one (Alex, 2026-11-24, as for operator backups). A `.tmp` after 24 hours. Another machine's backups in a shared backup folder go at 90 days, its newest too (#3648). | After each backup, when **Keep** changes, and hourly |
| `backups-upload/`, `backups-download/` | blob | A server backup on its way | Removed after use | On use |
| `.restore-*` | blob | A restore in progress | Removed when it ends; one a crash left goes at the next start (#3649) | At end; at start |
| `Outgoing/`, `Incoming/` | cache | Exports, sends and imports | 1 hour untouched | On the next transfer, at start and hourly (#3649) |
| `Seeds/<id>` | blob | The commits that a send's new folder gets from its origin (#7119) | Until the folder is made; 1 hour if it never is; a get ends after 10 minutes, or 1 minute after the sender last asked; 4 at once | On use; 1 hour after the get; at start |
| `Clones/<id>` | blob | A clone in progress | Removed after the clone | After the clone, and at start |

### Caches and logs

| Store | Kind | What it keeps | How long, how large | Applied |
| --- | --- | --- | --- | --- |
| `daemon.log`, in the log folder | log | What the machine did | 7 days and 10 MB (#6735): 4 MB, and one older file. A file holds the lines of 83 hours at most, and there are two files, so a line goes after 83 hours to 7 days, with no summary. The oldest lines go first. | On write, at start and every 30 seconds |
| `mcp-logs/`, in the log folder | log | What each hosted MCP server wrote to stderr | 7 days and 10 MB for the folder (#6736): 256 KB and one older file a server, and the files written longest ago go when the folder passes 7 MB. A larger write is split, so no file passes 256 KB. A line goes after 83 hours to 7 days, with no summary. | On write, when a server starts, and hourly |
| `tools/<runtime>/`, in the cache folder | cache | Pinned runtime toolsets | The current one | At start |
| `browser/`, in the cache folder | cache | The browser that agents drive, and its output | Old browser versions are left to Playwright. Output goes when the session ends; what a crash left goes at the next start (#3649) | On install; at session end; at start |
| The Mac browser pane's website data (WebKit's default store), and the date each site was last opened (`browserPane.sites` in the app's defaults) | blob | The sign-ins and site storage of the sites the pane opened, and its caches | 10 MB and 100 sites; a site goes, with its sign-in, 90 days after a pane last opened it (#6796). Over a limit the caches go first, then the site opened longest ago. A site in an open pane stays, so the store can be over its size until that pane closes. The caches are also emptied when the app starts and once a day. The dates are of 100 sites, and of 1,000 hosts at most between two sweeps. | At launch, hourly, and sooner past 20 MB; Forget This Mac clears all of it |
| `catalog-staging/`, in the cache folder | cache | Skill previews | 8 previews, 30 minutes; folders left by a restart go at the next start (#3649) | On use; at start |
| `option-cache.json`, in the cache folder | cache | Runtime options | 50 | On write |
| Cache and log folders of each other data folder (`roots/<name>-<hash>`) | cache | As above, for a second copy | Removed when that data folder is gone (#3746). Each keeps its data folder's path in `root-path`; a folder made before that is kept, and so is one whose data folder's parent is gone too, as on a disk that is not attached. | Hourly, by a machine whose data folder is not a temporary one |
| Terminal scrollback | log | What a terminal showed | In memory only, 4 MiB a terminal | On write |

### `~/.agents` and `~/.agents-secrets`

| Store | Kind | What it keeps | How long, how large | Applied |
| --- | --- | --- | --- | --- |
| `~/.agents/settings/` | key-value | Settings that follow you (see [The `~/.agents` folder](../../reference/agents-folder.md)) | A fixed shape | — |
| `~/.agents/settings/wake.json` | key-value | Keep awake, and the warm runtimes | One, of 4 KiB. No age, on purpose: a person's choice (#6745). `wake.json.moved` in the data folder is an old one that another machine's stood for | On write |
| `~/.agents/settings/backup-settings.json` | key-value | Each machine's backup target, folder and Keep | 100 machines of 32 KiB each: one more is refused, with a message. No age, on purpose: a person's choice, and a machine off for 90 days keeps its own (#6746). `backup-settings.json.moved` in the data folder is an old one, as above | On write |
| `~/.agents/settings/image-hosts.json` | key-value | Each machine's hosts that the person allowed images from, and when | 100 hosts and 32 KiB a machine, 100 machines: one more is refused, with a message. No age, on purpose: a person's choice, until they remove it (#7138) | On write |
| `~/.agents/summaries/` | document | Daily summaries saved before #5853, and `pages.json` with who wrote each: none is written now. A file that is not one of these is yours and stays | The newest 3 of at most 1 MiB, 90 days after their last change; `pages.json` goes with the last of them | Hourly |
| `~/.agents/checks/suggestions.json` | document | Daily-check findings and dismissals | 50 findings, 200 dismissals; the oldest go first. No 90-day age-out: an exception to the 90-day rule until #7023 removes the daily checks (Alex, 2026-11-24) | On write |
| `~/.agents-secrets` | secret | Secrets, connections and machine provider secrets (see [Security overview](security-overview.md#where-secrets-live)) | Until you remove them | — |
| `~/.agents-secrets/sealed.json` | secret | Every secret of yours on this machine, sealed to it (#4014, #4177), with Ask before use on or off | Until you remove them. 200 from the control plane and 200 of the machine's own; a value of 16 KiB, a record of 64 KiB. One over is refused, and none is evicted (#6738). | — |
| `~/.agents-secrets/sessions/<agent>.json` | secret | Secrets for one session | The values go when the agent is archived; the file goes when it is deleted. 1,000 sessions, each with 200 secrets (a value of 16 KiB, a record of 64 KiB) and the newest 400 expired names; one over is refused (#6739). | On archive and delete; leftovers at start |
| `~/.agents/dismissed-leads.json` | key-value | The epics you dismissed from New Session's **Suggested** | 500 epics of 1 KiB each, for 90 days; the oldest goes first. An epic that gets a lead goes at once (#6713). In the data folder on a machine with no personal home | Hourly, and on write: age, and the count when it is twice over |
| `~/.agents/mcp.removed.json` | key-value | MCP servers you removed, so that you can put one back by hand. The app does not read it to connect anything | 100 connectors of 256 KiB each, for 90 days after you removed them. When it is full, a removal is refused; none goes to make room (#6727). Copies from before these limits are kept as they were, for 90 days (#7181) | Hourly, and on write: age. On write: count and size |
| `~/.agents-secrets/mcp-sign-ins.json` | secret | MCP sign-in tokens | Until you sign out. 200 sign-ins and 200 clients of 64 KiB each; one over is refused. 200 notes that a server needs a sign-in, of 4 KiB each, for 90 days (#6740). | On write |
| `~/.agents-secrets/hints.json` | secret | The last 4 characters of each secret | With its secret. 200 of 256 bytes each; one over is refused (#6741). | On write |
| `~/.agents-secrets/descriptions.json` | key-value | Secret descriptions, which are not secret. Not in `~/.agents`, on purpose: people sync and commit that folder (#6742). | With its secret. 400 of 2 KiB each; one over is refused (#6742). | On write |
| Keychain `agents.connection.<hash>` (Linux: `connections.json`) | secret | Forge connections: each token, with the server, account and site it is for. They stay with the token, because they say where it is sent (#6743) | Until you remove the connection. Linux: 16 connections of 64 KiB each. Mac: one for each provider, and the Keychain command's line of 4,095 bytes (about 2.9 KB of JSON). A connection past that is refused, and what was kept stays | On write |

## In each folder

These are in `<folder>/.agents/`.

| Store | Kind | What it keeps | How long, how large | Applied |
| --- | --- | --- | --- | --- |
| `attachments/` | blob | Files attached to prompts | A file that no kept conversation names goes after 7 days. The folder stays under 500 MB, oldest first. A folder with no agent left is swept too: `attachment-folders.json` lists each folder an attachment was written in, until it holds none (#3649). | At start, hourly and on write |
| `dropbox/` | blob | Files handed to a workflow | 25 MB a file and 500 MB a folder from **dropbox/put**; a put past the total is refused. A cut-off put's `.part` after 24 hours. A file goes 90 days after it was last changed or put there, also one put there by hand (Alex, 2026-11-24, #6711). It stays when git tracks it, or when it is in a repository in the drop box; a link is not followed, and a drop box that is a link is not swept. | On put; hourly |
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
policy, governance and remove requests (#3649), old policy versions and SCIM and read
tokens that stopped working (#6765 to #6767), and once a day the backups of removed
servers. It also forgets clients whose key expired (#6757), and machines with no workspace whose key expired (#6758), deletes old
machine set-ups, updates and failed makes, and those of machines and providers that are gone
(#6968, #6969, #6971), and removes
the approvals of people and groups that are gone, and probes and move marks left by a
copy that stopped (#3748).

The control plane's configuration (its settings, sign-in settings, policy, budgets, org keys
and gateways, audit sinks, report views, and a person's settings and secrets) stays in the
control plane's own store (Alex, 2026-11-24), and has no age: it stays until it is changed or
removed. A write over a limit is refused with a message, and nothing is evicted. A record
that is over its largest already is kept, and can be changed to one no larger (#6771 to
#6781).

A removed client, machine, group, project, route or machine provider leaves a **tombstone** for 7 days, so
the other copies of the control plane learn that it went. A removed person's stays longer (below).

### People, machines and clients

| Records | Kind | What they keep | How long, how large | Applied |
| --- | --- | --- | --- | --- |
| Clients | document | Name, kind, public key, when last seen, push tokens (APNs, up to 16 Live Activity tokens of 12 hours each, Web Push) | Until forgotten, then a tombstone for 7 days. A key lives 90 days at most from when it was paired or last renewed in use; a client whose key expired is forgotten (#6757), unless it is connected. 16 KiB a record and 1 MiB in all (hosted: 5 or 50 clients too): a new client, a longer name or a push token over that is refused; a renewal of a client in use never is. Tombstones keep no push tokens, and may come to 1 MiB more | Hourly; sizes on each write |
| Machines | document | Name, key, platform, version, owner, workspace | Until removed, then a tombstone for 7 days that keeps no labels, links or capabilities. A key lives 90 days at most from when it was joined or last renewed in use; a machine whose key expired is removed (#6758), unless it is connected, or linked to a provider's workspace, which its person could still start or delete: that one stays until a person removes it. 32 KiB a record and 1 MiB in all (hosted: 2 or 20 machines too): a new machine, a label or a lend over that is refused; a hello of a joined machine is refused only over 32 KiB a record, and a renewal never is. Tombstones may come to 1 MiB more | Hourly; sizes on each write |
| People | document | Name, role, sign-ins (email, login), SCIM data, old names for 90 days | A removed person goes after 14 days. One with a sign-in goes after 90 days; until then only a tombstone is kept: the ID, kind, role and each sign-in's provider and account (#3647, #6756). The local owner's tombstone, one record, stays while they are the control plane's owner, so that a removed owner is not an admin again. 16 KiB a record and 1 MiB in all, which is 1,000 people or more: a new person, a longer name, one more sign-in or a larger SCIM user over that is refused, and nobody is removed to make room. The first sign-in of a person SCIM made, a role, a suspension and a removal never are. Removed people may come to 1 MiB more (#7019) | Hourly; sizes on write |
| Join cutoffs | document | A provider's ID and a time: when a removed person's tombstone with a sign-in of that provider first aged out. From then on, an account of it that nobody has needs an invite, so the old sign-in stays refused (#6756) | 1 KiB a record; Google and 5 org providers. Until the provider is removed | On write, and when the provider is removed |
| Groups, projects | document | Name and members; name, owner, slots and boards | 1,000 groups; 500 projects a person | Tombstone, hourly |
| Projects' folders | document | Each machine's joined folders | 1,000 | Hourly |
| Machine locks | log | Each person's lock chain | 4,096 entries a person. Deleted when the person is removed (#3647) | On **Reset** and on remove |
| A person's settings and secrets | key-value; secret | Settings that follow a person; secrets, sealed | 256 KiB of settings, 200 secrets of 16 KiB, each sealed to 200 machines and apps at most: 256 KiB a part (16 KiB for the small ones), 5 MiB of settings as kept and 5 MiB a sealed secret; 100 offers from agents; more is refused. A secret names the newest 200 removed machines it was on. They follow the person to each machine, so they are not in one machine's `~/.agents`, and they are the person's own data, so they have no age. A removed secret's tombstone (its name, version and time) goes after 90 days; at 200 tombstones a new name is refused (#6780) | Limits on write; tombstones hourly; all of it when the person is removed |
| Channel links | document | Which person a channel identity is, and when it last acted | 4 KiB a record and 1 MiB in all: an approval over that is refused, and the request still waits. No link is evicted. A link goes 90 days after its identity last acted; a message from it renews the link (noted once a day). After that, a message asks the person again (#6763) | Size before an approval, age hourly; also on unlink, or when the person is removed |
| Machine set-up | document | Each machine's set-up steps | One a machine, 512 KiB at most; a step's words are cut at 2 KiB. One that ended (ready or failed) goes 90 days later. One that has not ended has no age. Deleted when the machine is removed; that of a machine that is gone, or never joined, 7 days after its last change (#6968) | On write; on remove; hourly |
| Machine set-up times | time series | How long each set-up step took, and when it ended | 90 days, 50 a step (#6760), 32 KiB at most. No roll-up. | On write, hourly |
| Machine versions | document | Each machine's version and its updates | 20 updates a machine, 64 KiB at most; a version over 256 bytes is not kept. An update goes after 90 days; the version now has no age. Deleted when the machine is removed; that of a machine that is gone, a day after it was last seen (#6969) | On write; on remove; hourly |
| Machine names | key-value | A person's machine counter and last choices | One a person, 64 KiB at most: choices past that are not kept. The counter has no age while the person is here, so that a number is never used twice. Deleted when the person is removed (#6759). | On write; on remove; hourly |
| Approvals | document | Each person's signed approvals of workflows and connectors | 2,000 a person of 8 KiB each: one past that is refused, and it still holds on the machine that approved. An approval has no age while the person is here, so that nobody approves the same file again. Deleted when the person is removed (#6970). | On write; on remove; hourly |

### Requests, codes and sign-in

| Records | Kind | What they keep | How long, how large | Applied |
| --- | --- | --- | --- | --- |
| Codes | document | A code's purpose, owner and expiry, and an invite's name and role; never its secret | Deleted 24 hours after it expires. 1,000 codes; a new one over that is refused (#3748). | Hourly |
| Join, lock-join and sign-in requests | document | The asking key and its address | 10 minutes; 50, 200 and 50 open | On read and hourly, 1 hour after expiry |
| Link requests | document | A channel link that waits | 24 hours; 50 open | On read and hourly, 24 hours after expiry |
| Sign-in state | secret | PKCE state and sealed refresh tokens while a sign-in runs | 10 and 2 minutes; 1,000 sign-ins started, each with at most one ticket (#3748) | Hourly, 1 hour after expiry |
| Sign-in settings | key-value; secret | Org OIDC providers, domain claims, one sealed refresh token a person and provider | The sign-in settings are one record of 16 KiB (50 domains, 50 organisations). 5 org providers of 16 KiB, and 20 domain claims of 4 KiB; more is refused. The daily check of a claim is never refused. A removed provider's tombstone keeps its ID from a new provider: it goes after 90 days, or when no person and no SCIM token or name has the ID, whichever is later; at 95 tombstones a new provider is refused (#6773, #6774, #6775) | On write; tombstones hourly; a claim also with its provider |
| SCIM and read tokens | secret | Token hashes, address of last use | SCIM: 2 live of 20 a provider; read: 10 live of 50; a new one over 8 KiB is refused. A live token has no age: until it is revoked or ends. A revoked or ended one goes 90 days after that; a removed provider's go with it (#6766, #6767) | On write; hourly |
| Policy, governance and remove requests | document | Asks for a budget, a catalogue item, or removal of a person | 1,000; a decided one 30 days; a governance one 90 days from the ask at most, and 8 KiB (#6764) | On read and hourly |

### Work data

| Records | Kind | What they keep | How long, how large | Applied |
| --- | --- | --- | --- | --- |
| Kept requests | document | Prompts and agent starts for a stopped workspace, **with their attachments** (up to 40 MiB each) | Waiting requests expire after 90 days at most; failed requests expire after 7 days. At most 20 requests and 800 MiB per machine. The control plane refuses a new request at either limit; it does not evict a person's request (#3647, #6674) | On delivery, on keep, hourly |
| Host copies | cache | Each machine's last folders and agents, up to 500, **with titles** | One a machine | When the machine is forgotten |
| Usage reports | time series | Usage, cost and time by person, project, folder, machine and runtime | 7 days by the hour, 90 days by the day | Hourly |
| Lineage | graph | Cost of each issue, PR and session node | 5,000 nodes, 32 MiB and 90 days a machine: the oldest go. A node over 64 KiB is not kept. A removed machine's a day after its last send (#6768) | On write, hourly |
| Lineage graph | graph; log | Each machine's links and its sessions' changes of state (working, waiting, needs-you, stopped, archived, deleted), as refs, states and times, in `v1/lineage-graph/<machine>/` (#4275); each send in a small file until a merge of several into one (#6863) or a roll-up (#6584) | Links as on the machine: an ended link 90 days after its end, 20,000 a machine. States as on the machine: 90 days, and each session's last state whatever its age, 20,000 a machine. At most 32 sends before a roll-up. A removed machine's a day after its last send | On write, hourly |
| Deploys | document | Each machine's deploys, as refs, names, states and times: the component, the environments and their tiers, the end, whether it is a rollback, the merge times of the PRs it brings and the health signals seen in it, in `v1/deploys/<machine>.json` (#5496). No titles, no logs | As on the machine: 1,000 deploys; each send replaces the last. A removed machine's goes a day after its last send | On write, hourly |
| Build timings | time series | Each machine's CI runs of a repository, with their jobs and steps, as refs, times, attempts, conclusions, runner labels and names, in `v1/build-timings/<machine>/` (#5232). No commit or PR titles, no logs | As on the machine: 90 days, 20,000 runs a repository, a pull request run's steps for 14 days. A machine that stopped sending is bounded once a day; a removed machine's goes a day after its last send | On write, hourly |
| Workflow runs | log | Which machine took each workflow trigger | 7 days | Hourly |
| Report views | key-value | Saved report views | 50 and 64 KiB a person; more is refused (#6779) | On write; when the person is removed |
| Machine providers | document | Each provider | 10, and 64 KiB each: one more, or one larger, is refused. It is configuration, so it has no age: until removed. A removed one's tombstone, with none of its commands, for 7 days, and the newest 100 (#3748, #6762) | On write; hourly |
| Tunnels | document | Servers' reverse tunnels | 15 minutes unjoined | On use |
| Control plane settings | key-value | Its name, address, owner, public keys, home machine, folder detection and audit days | One record of 64 KiB: folders or places to answer past that are refused. What the control plane writes itself (its own address and key) is never refused (#6771) | On write |
| Policy | key-value | The org policy, its signature and its old versions | One policy of 64 KiB, and a signature of 1 KiB; a larger one is refused. The kill switch is never refused for the size (#6772). The policy in force has no age. An old version goes 90 days after it was replaced; the newest 100 (#6765) | On write; hourly |
| Governance | document | Installed servers and skills; approvals | 500 items and 1 MiB a machine; 1,000 approvals of 8 KiB: one more, or a larger record, is refused. An approval in force has no age: it stays until an admin takes it back, or its person or group is gone (#3748, #6764) | Hourly |
| Org keys and gateways | key-value; secret | Routes and their sealed secrets | 50 routes of 256 KiB, and 4 MiB of sealed copies each (200 machines and apps); more is refused (#6776) | On write; tombstone, hourly |
| Warm pools | document | Pools of ready machines, and their claims | 10 a provider, over every person, and 256 KiB a pool: a pool past that is refused. Claims: 7 days and 500 a pool. The pool has no age. Deleted, with the machines nobody claimed, when the person is removed (#6761). | On write; every 30 s; on remove |
| Failed machine makes | log | Why a machine could not be made | 5 a provider, 64 KiB at most; a reason is cut at 4 KiB. 90 days: one kept with no time counts from the first write or sweep. Deleted with the provider (#6971) | On write; on remove; hourly |
| Event bus | log | Events between a person's machines: refs, names and times, and sealed data | 90 days; 100,000 envelopes and 1 GiB of encoded log records a person, the oldest deleted first with their dedupe ids. An envelope is at most 24 MiB encoded: 64 sealed copies of 256 KiB, in base64. Deleted when the person is removed (#6671). | On write; hourly; on remove |
| Budgets | key-value | The org's budgets | One record of 4 KiB, with one budget (#6778) | On write |
| Feature maturity | document | Evidence for each feature's rating | 84 records of 4 KiB, each updated in place: one more, or a larger one, is refused. A rating is current state, so it has no age: an idle feature keeps it (#6905, #6972). | On write |
| Store sizes | key-value | The last measurement of each declared store of the control plane: its bytes and its count, never a key or what is stored (`v1/store-sizes.json`, #6906) | One record of 128 KiB; a larger one is refused | Each measurement, every 6 hours, replaces the last |

### Audit, keys and backups

| Records | Kind | What they keep | How long, how large | Applied |
| --- | --- | --- | --- | --- |
| Audit events | log | Who did what (see [Security overview](security-overview.md#who-did-what)) | 90 days by default; an admin sets 1 to 90 days (hosted: 7 or 90). At most 1,000,000 entries, of at most 16 KiB each: the oldest over that go, even when they are newer than the days set. A longer text in an entry is cut. No roll-up: the long copy is the SIEM sink's (#6755). | Hourly, oldest first; before the hour at 2,000,000 entries |
| Change events between copies | log | What changed, when the control plane has several copies | 1 day, in `v1/mesh/events/` (#3648) | Hourly, by day |
| Audit seals and sinks | log; key-value | Each day's chain head; up to 4 SIEM sinks with sealed tokens | With the day; 4 sinks in one record of 64 KiB, more is refused (#6777) | Hourly; sinks on write |
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
| Account | document | Sign-in provider (GitHub, Apple or Google) and its user ID, login, plan, suspension, certificate time, **Stripe customer and subscription IDs**, the date it signed up | Until **Delete Account** (the exception below). 100 accounts, then a new one is refused; 8 KiB a record. |
| Sign-in links | document | Which account a GitHub, Apple or Google user is | Until **Delete Account** (the exception below). One an account, 128 bytes each. |
| Account usage | key-value | The bytes the account's store holds | Until **Delete Account** (the exception below). One an account, 128 bytes each. |
| The account's control plane store | all of the kinds | All of the section above | Plan limits: 1 or 20 GB, 2 or 20 machines, 5 or 50 clients, audit 7 or 90 days. Until **Delete Account**. |
| Invites | document | Expiry, and **who used it** | 14 days to use it, 90 at most. It goes when that time ends, used or not, and with **Delete Account** of the account that used it. 1,000 of 4 KiB; a new one over that is refused (#6782). Hourly |
| Waitlist | document | Who asked to join | 90 days; removed sooner on sign-up or a block. 10,000 of 4 KiB; a new one over that is refused (#6786). Hourly |
| Stripe events | key-value | Stripe event IDs already seen | 30 days, swept daily |
| Operator backups | blob | Each account's store, on the operator's disk | The newest 7 by default; `--keep N` sets it, 1 to 90 (#3648). An account's copy goes at 90 days. The newest copy of an account that exists always stays, past the count and the age, on purpose: it is the way back. A copy over 10 MB is made, with a warning (Alex, 2026-11-24). Applied after each backup: the service does not know the folder (#6787) |
| Email links | key-value | Which account a sign-in email is | Until **Delete Account** (the exception below). At most one an account, 128 bytes each. |
| Allow list | key-value | GitHub logins that may sign up, and when the operator allowed each | 90 days from the last allow; an account that was made stays. 10,000 of 4 KiB; a new one over that is refused (#6783). Hourly |
| Block lists | key-value | Sign-ins and emails' keyed hashes that may not sign up, with the login and the reason | Until the operator unblocks it. A block has no age, because one that aged out would let the person back in (Alex, 2026-11-24). 10,000 sign-ins and 10,000 emails of 4 KiB; a new one over that is refused (#6784) |

**An exception to the 90-day rule (Alex, 2026-11-24):** an account, its sign-in links, its
email link and its usage have no age. They stay until **Delete Account**, for at most 100
accounts, until epic #7092 deletes
idle accounts safely.

The service keeps nothing for a signed-in browser: a session is a signed cookie for 30 days.

**Delete Account** stops the account's control plane, deletes its store, its usage, the
invites its sign-in used, the account and its sign-in link. It does not delete the
account's machines. It leaves the operator's backups of it, and Stripe's own records.

## Agent Loops on the Mac

| Store | Kind | What it keeps | How long, how large |
| --- | --- | --- | --- |
| `~/Library/Application Support/Agents Control/` | document; secret | The control plane's store, TLS certificate, `secrets-key`, push key, web settings | One of each |
| `Agents Control/store-before-<…>` | blob | A whole old store, set aside when you change the store or move back. Each holds the audit, sealed secrets and server backups. | The newest 2 (#3671), 1 GiB in all, and 90 days (#6769). Over a limit the oldest is deleted, when another is set aside or hourly while the app runs. Count and size never delete the newest, so one folder has no largest size of its own: it is the whole store, moved (#7252); its age does, but only while the control plane runs on this Mac from its own store. While it is moved away, or a move back is not finished, nothing ages out and the store it left here stays. A folder whose name has no time is yours, and is left alone. |
| Keychain `com.alexecollins.agentshost` | secret | The control key, bucket keys, and `control-key-before-<time>` | One of each; the newest 2 `control-key-before-*` items (32 bytes each). An item is deleted at 90 days, on the next write or hourly while the app runs, but not while a `store-before-*` folder is left: that store needs its key to be put back (#3671, #6770). |
| `~/Library/Logs/Agents/control.log` | log | What the control plane did | 7 days and 10 MB (#6901): 4 MB, and one older file. A file holds the lines of 83 hours at most, and there are two files, so a line goes after 83 hours to 7 days, with no summary. Checked on write, at start and every 30 seconds. `/data/control.log` in a container is the same. |
| The Web Push key | secret | The key that signs browser push | One. In `agents-control backup` as `webpush-key.pem` (#3651). |
| `Agents Control/web-off` | key-value | That the web page is off | One empty file, beside the control plane's store, until the page is turned on. It is this control plane's own configuration, so it is not in a person's `~/.agents` (#6781) |

## On each client

What the Mac window and the Remote keep in their defaults, and the web page in
`localStorage`, by folder and by control plane (#3750).

| Store | Kind | What it keeps | How long, how large | Applied |
| --- | --- | --- | --- | --- |
| Sidebar folds (`sidebar.folds`, `sidebar.folded`, each scope; `agents.sidebar.folds`, `agents.sidebar.folded` on the web page) | key-value | Which folders and folds are open | The folders that exist. A folder that its machine no longer lists goes; all of a machine's go when the control plane no longer lists the machine. An offline machine's stay. The Remote lists no archived folders, so their folds go there. Each client keeps 1,000 open and 1,000 folded names for each scope, each 2 KiB: a new one past that holds for the visit only, or while the app runs (#7252, #7253). | When a machine lists its folders, and when it leaves |
| Open sessions (`sidebar.openSessions`; `agents.sidebar.openSessions` on the web page) | key-value | Sessions whose children are shown | 200, the oldest out first; an id is 64 bytes at most | On write |
| `sidebar.folder.<folder>.width`, `.pane` (Mac only) | key-value | A folder window's inspector width and pane | As the folds; 2,000 at most: one past that holds while its window is open (#7252) | As the folds |
| `newSessionProjectFolders` (`agents.newSession.projectFolders` on the web page) | key-value | New Session's last folder for each project | As the folds. Each client keeps 1,000 projects, each 2 KiB: a new one past that is not kept (#7252, #7253). | As the folds |
| `machineLockPin.<key>` (`agents-lock-pin.<key>` on the web page) | key-value | The machine-lock pins, the lock and each person's | This control plane's only: another control plane's go once the client is paired (#3750), and all go on **Forget** (#3670). Each client keeps 5,000 pins and recovery marks, each 1 KiB, and the Mac and the Remote 100 whole locks, each 1 MiB: one past that is not kept, and none goes to make room (#7252, #7253). | When the control plane answers |
| Drafts (`draft.agent.<uuid>`, `draft.new.<folder>`, and the Mac's `draft.start`) | document | Prompts not sent, with their attachments | 40 drafts, 1 MiB each with at most 512 KiB of attachments: a new one over 40, or one too big, is refused, with a message, and stays only while the app runs (#6794). The start form is one entry of 1 MiB: one over that is refused, with a message, and the earlier one stays (#7252). A draft that an older build kept over the limit is read where it is, and is not moved. Deleted after 30 days without an edit, when its agent is archived, on send, and on **Forget** (#3749). | On write, at start, on archive |
| Drafts on the web page (`agents.drafts`) | document | Prompts not sent, with their attachments | 40 drafts, 1 MiB each: a new one over 40, one too big, or one the browser has no room for is refused, with a message, and stays only while the page is open; the others are saved (#7199). Attachments only while they are at most 256 KiB: a message says when they are not saved. Deleted after 30 days without an edit, on send, and on **Forget** (#6793). | On write, at start, hourly |
| `control-client.json` (Mac, Remote), and the Mac's `control-client.here.json`; IndexedDB `agents` (web) | key-value; secret | The pairing, and the web page's key | One of each, until **Forget**. On the Mac and the Remote it is not in backups, and is not in `~/.agents`, on purpose: it identifies one device, and a backup or a dotfile sync would copy it to another (#6800). | On **Forget** |
| Keychain `control-client-key`, `lock-signing-key`, `device-key` (Mac, Remote) | secret | The client's own keys | One of each; kept after **Forget** | — |
| `copy/` in the cache folder (Mac, Remote) | cache | The local copy: machines, folders, records, the ends of 20 chats and Work's pictures | 90 days since it was read from its host. 200 MiB on the Mac, 50 MiB on the Remote, the oldest out first. A record is 16 MiB at most. A machine that the control plane no longer lists goes, with its chats. A larger record that an older build kept is not read, and goes at the next sweep (#7252). | On write, hourly, when the machines are listed, and on **Forget** |
| IndexedDB `agents` › `copy` (web) | cache | The local copy: machines, folders, records, the ends of 20 chats and Work's pictures | 90 days since it was read from its host. 50 MiB, the oldest out first. A record is 16 MiB at most. A machine that the control plane no longer lists goes, with its chats. A larger record that an older build kept is not read (#7253). | On write, at sign-in, hourly, when the machines are listed, and on **Forget** |
| `work-cache.sqlite`, in the cache folder (Mac, Remote); IndexedDB `agents` › `work` (web) | cache | Forge items, bodies, comments and Work page answers | 90 days since it was read from the forge. 200 MiB on the Mac, 50 MiB on the Remote and the web page, least recently shown out first. An item is 4 KiB and any other record 4 MiB at most. A larger record that an older build kept is not read, and goes when the cache opens, or at sign-in on the web page (#7252, #7253). | On write, at start, hourly, and on **Forget** |
| `shared-inbox.json` (Remote) | blob | What was shared to Agent Loops | 100 shares and 10 MB in all: a share over either is refused, with a message in the share sheet, and nothing that waits goes to make room. A share that the Remote did not take in 90 days is deleted, and New session then says so (Alex, 2026-11-24). Deleted when New session takes it, and on **Forget** (#6795). | On write, on take, and hourly while the Remote runs |
| `attention-snapshot.json` (Remote) | cache | The widget's rows | 12 rows and 64 KiB: the oldest rows that do not fit are left out, and the count stays. Stale after 15 minutes; 90 days (#7252) | On write, and on **Forget** |
| `remote.startFolders` (Remote) | key-value | The folders you add to a new session on the Remote, each with its machine | 100 folders, each a path of 1,024 bytes and a machine id of 64: one past that is refused, with a message. No age, on purpose: a folder is your choice. It stays until you remove it, until the control plane no longer lists its machine, or until **Forget**. An offline machine's stays (#6798). | On write, when the machines are listed, and on **Forget** |
| `machinePicker.pinned` (`agents.machinePicker.pinned` on the web page) | key-value | Pinned machines | 100 machine ids of 64 bytes each: one past that is refused, with a message. No age, on purpose: a pin is your choice. It stays until you remove it, or until the control plane no longer lists its machine. An offline machine's stays (#6797). | On write, and when the machines are listed |
| `voiceMode` and `voiceMode.*` (Mac, Remote) | key-value | Voice mode settings | A fixed set: the switch, five choices and one for each sound, each a number or a short name. On each device only, on purpose: the wait is for one microphone and the sounds are the device's. No age (#6802). | On write |
| `catalog.personas.marketplace`, `reviewDiffLayout` (Mac, Remote) | cache | The mirror of two choices in `~/.agents/settings/display.json` | One short value each. The web page keeps no copy. A value set before the move is sent to the host once, when the host has none (#6803, #6806); a `displayMoved:` mark says it was. | When the host says its choices |
| `agents.startForm` (web) | key-value | The folders that a new session reaches beyond its own | 100 folders, each a path of 1,024 bytes: one past that is refused, with a message. In the browser only, on purpose: each is a path on one machine. No age: it stays until you remove it, or until **Forget** (#6805). | On write, and on **Forget** |
| `agents.achievements.seen` (web) | key-value | The achievements whose card was shown | 100 names of 64 bytes (#7253) | On write |
| `agents.firstBuild…` (web) | key-value | Where the first build is: the idea, the starter, the session, if it shows | 5 entries, 64 KiB each: an idea over that is refused, with a message, and its words stay in the box. They stay until the person leaves the page (#7253). | On write, and on leaving |
| `featureFlags` (web) | key-value | The copy of the person's feature flags | One, 64 KiB: one over that is not kept (#7253) | When the host says its choices |
| `agents.work.filters.<page>` (web) | key-value | Each Work page's filters, sort and range | 5 pages, 16 KiB each: one over that is not kept (#7253) | On write |
| `agents.sidebar.newSessionFolder`, `agents.multiplier.shown`, `agents.machinePicker.recent` (web) | key-value | New Session's last folder; the figures shown last; the machines picked last | One of 4 KiB; one of 1 KiB; 5 ids of 64 bytes, the oldest out first (#7253) | On write |
| `agentsFilterMachine`, `agentsFilterState`, `tableSort.<table>` (web, the tab's own) | key-value | The sidebar's filters; each table's order | 2 of 1 KiB; one of 1 KiB for each table, 100 at most. The browser deletes them with the tab (#7253). | On write |
| Other choices on this client | key-value | How this client shows things | A fixed set; on the web page (`pageTextZoom`, `showsThoughts`, `agents.newSession.suggestedHidden`) 16 bytes each | — |
| MDM profile (Mac): `ControlPlaneAddress`, `ControlPlaneKey`, `MustJoin` | key-value | The control plane an org pins a Mac to | Three settings. The app only reads them, and only the values that the profile forces: macOS and the org's MDM set and remove them, so they have no age. They are not in `~/.agents`, on purpose: the person could edit a file there, and so remove the pin (#6801). | When the org removes the profile |
| `crashes/`, `hangs/`, in the log folder (Mac); `remote.log` (Remote) | log | Crash and hang notes; the device log | 20 of each; a hang note is 2 MiB at most, cut past that, for 90 days. The device log is 256 KiB and one older file; a line is cut at 64 KiB, and a file holds 45 days, so a line goes by 90 days (#7252) | At start; on write; hang notes hourly |
| `achievements.seen`, `installOffered` (Mac) | key-value | The achievements whose card was shown; the runtimes that the start-up sheet offered | 100 names of 64 bytes each (#7252) | On write |
| `firstBuild.idea` (Mac) | key-value | The first build's idea | One of 64 KiB: one over that is refused, with a message, and its words stay in the box (#7252) | On write, and on leaving |
| `work.filters.<page>` (Mac, Remote) | key-value | Each Work page's filters, sort and range | 5 pages, 16 KiB each: one over that is not kept (#7252) | On write |
| `liveActivityTurns` (Remote) | key-value | The turns that the Live Activity speaks for | 100, the oldest out first; a place is cut at 256 bytes; 90 days (#7252) | On write and at start |
| `control.thisMacHost` (Mac) | key-value | This Mac's host, as the control plane last listed it | One of 64 KiB: one over that is not kept (#7252) | When the machines are listed, and on **Forget** |
| `newSessionFolder`, `selectedFolderPath`, `startRuntimeID`, `machinePicker.recent`, and the host's choices mirrored here (Mac, Remote) | key-value | New Session's last folder; the open folder; the runtime a share starts with; the machines picked last; the choices in `display.json` | 4 KiB each; 5 machine ids of 64 bytes, the oldest out first. A mirrored choice over that is none on this client, which does not change the host's (#7252) | On write |
| `~/.agents/settings/host.json` (Mac) | document | Each Mac's role | 100 entries of 64 KiB each: one past that is refused, with a message, and nothing goes to make room. A Mac whose earlier entry cannot be set aside keeps it (#7252) | On write |
| `~/.config/loops/hosts.json`, `lock-keys/` | secret | Pairings of `loops`, with their keys, the default control plane and the lock pins | 20 control planes, 4 KiB each: one over that is refused, with a message. A lock key and a lock pin for each. No age, on purpose: a pairing stays until `loops auth logout`. Not in `~/.agents`, on purpose: it identifies one terminal, and a sync of `~/.agents` would copy it (#6807). | Before a pairing, and on `loops auth logout` |
| `~/.agents-go/ctl.json` | secret | The pairing of `agentsctl`, with its key | One, 4 KiB. No age, on purpose: it stays until `agentsctl unpair`. Not in `~/.agents`, on purpose, as for `loops` (#6808). | On pair, and on `agentsctl unpair` |

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
  its Work cache, and the browser pane's website data (it is signed out of each site there,
  #6796).
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

- **A machine** backs up every day by default, and keeps the newest 3 (1 to 10), for 90
  days. Its newest good backup always stays on disk. A backup holds each agent's record, conversation, tool log and search index, and the usage and
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
