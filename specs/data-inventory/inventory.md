# Data inventory: what Agents stores, where, and how it is bounded

Status: store inventory, updated for #6684. The rows record observed bounds and implementation
locations. Store kinds follow `specs/data-bounds/store-kinds.md`; a kind describes the access
contract, not the file format. A numeric limit that its responsible issue has not set is
marked **not specified**. Do not infer a product limit from an observed value or this inventory.

This inventory is the detailed list of stores. The reader summary is
`docs/explanation/compliance/data-and-retention.md`. `docs/reference/agents-folder.md` lists
the layout of the `.agents` folder.

**Kinds of bound** used in the tables:

- **time**: data older than a limit is deleted.
- **count**: only the newest N are kept.
- **size**: a byte limit.
- **life**: the data goes when its owner (an agent, a person, a machine) goes.
- **none**: nothing removes it.

**Swept** says when the bound is applied: on write, on read, hourly, or at start. A bound that
is applied only on read or only at start is a weak bound.

Paths in the first three sections are relative to `Go/internal/` unless shown otherwise.

## Contents

1. [On each machine (agentsd)](#1-on-each-machine-agentsd)
2. [In each folder (`<folder>/.agents/`)](#2-in-each-folder-folderagents)
3. [Outside our folders (runtimes, git, cloud)](#3-outside-our-folders)
4. [On the control plane](#4-on-the-control-plane)
5. [The hosted service](#5-the-hosted-service)
6. [Agents Host on the Mac](#6-agents-host-on-the-mac)
7. [The clients: Mac window, Remote, web page](#7-the-clients)
8. [In memory](#8-in-memory)
9. [Kind mapping and unresolved bounds](#9-kind-mapping-and-unresolved-bounds)

---

## 1. On each machine (agentsd)

`<root>` is `~/Library/Application Support/Agents` on a Mac, `~/.local/share/Agents` on Linux,
and `~/.agents-server/root` on a joined server. `<cache>` is
`~/Library/Caches/com.alexecollins.agents` on a Mac. `<logs>` is `~/Library/Logs/Agents` on a Mac.
(`agentsd/store/locations*.go`)

Every `*.json` store is written atomically (`store.WriteAtomically`, `store/files.go:16`). A
store that cannot be read is set aside as `.unreadable-<t>` or `.corrupt-<t>`, and only the
newest 3 are kept (`SetAsidesKept`, `store/files.go:45`).

A machine backup holds only `agent.json`, `transcript.jsonl`, `turns.jsonl`, `app-tools.jsonl`
and `said.json` for each agent, and `usage-history.json`, `time-history.json` and `erased.json`
from the root (`agentsd/backups.go:69,72`). It holds nothing else.

### Agents and conversations

| Store | Holds | Bound | Swept | In backup |
|---|---|---|---|---|
| `agents/<ID>/agent.json` | The agent's record | **life.** An archived agent is deleted after 7/14/30/90 days, 30 by default, a pinned one after 90 (`retention.go`, `sweepArchived`). | at start, then hourly (`retention.go:15,96-110`) | yes |
| `agents/<ID>/transcript.jsonl` | The whole conversation | **10 MB, 90 days**, live and pinned too; an entry 4 MB (`store/conversations.go`, #6706) | at start, then hourly; soon after a write past 20 MB | yes |
| `agents/<ID>/app-tools.jsonl` | Each call to an app tool | **2 MB, 90 days**; answers and arguments cut to 2,000 runes (`store/conversations.go`, #6706) | at start, then hourly; soon after a write past 4 MB | yes |
| `agents/<ID>/said.json` | Search index of what was said | 32 KiB (`said.go:22`) | with the agent | yes |
| `<cache>/turns/<ID>.jsonl` | Turn summaries | life. Media base64 is limited (`turns.go:1197`). | with the agent (`store/store.go:217`) | **no** (the backup reads the wrong path) |
| `erasures.json` | Runtime copies still to erase | 1,000 entries, 3 tries, 7 days (`erasure.go:40-45`) | on write | no |
| `erased.json` | Every agent deleted here | 10,000 IDs, 400 days | on write | yes |
| `elsewhere.json` | Sessions started on or from other machines | 800 entries of 64 KiB; 90 days from when last heard | hourly and on save | no |

### Events, usage, cost and lineage

| Store | Holds | Bound | Swept | In backup |
|---|---|---|---|---|
| `events.jsonl` | The machine's events | 7 days and 10,000 events, applied at 10,500 (`events.go:30-35,460`). Consequence lines have no count limit. | at load, hourly, on write | no |
| `events-state.json` | Branch tips, cost crossings, disk levels, wait restarts | Live folders only; today only; pruned | on write | no |
| `usage-history.json` | Usage | 168 hour **keys**, 90 day **keys** (`cost.go:29-30`) | on write | yes |
| `time-history.json` | Time, recaps, records | 168 hours / 90 days / 90 recaps (`timeledger.go:297`) | on write | yes |
| `spend.json` | Daily spend for cost limits | 7 **keys** (`cost.go:28`) | on write | no |
| `report-hours.json` | Hourly rollup for the control plane | 8 days (`reportledger.go:30`) | on save | no |
| `features-history.json` | Feature use counts | 90 day keys (`features.go:20`) | on write | no |
| `lineage.jsonl` | SDLC graph links | Ended links 90 days, 20,000 links, rewritten at +2,000 (`lineage/lineage.go:97-107`) | at open and on append growth; **no timer** | no |
| `lineage-cost.json` | Cost per node | 90 days, 5,000 nodes (`lineagecost.go:35,40`) | on save | no |
| `lineage-sync.json` | Links and session states unsent to the control plane (#4275) | 2,000 links, 5,000 states saved (25,000 held); past that it saves "send all again" instead (`lineagesync.go:31-40`) | on save | no |
| `ci-timings/sync.json` | Days of build timings unsent to the control plane, per repository (#5232) | 90 days of at most 20 repositories (`cisync.go`) | on send | no |
| `lineage-comments.json` | PR cost comments | 500, 30 days (`lineagecomment.go:42-44`) | on save | no |
| `release-tags.json` | Tags already linked | 90 days, 5,000 tags of 1 KiB (`lineagerelease.go`, `openReleaseTags`) | hourly, and on write: age, and the count when twice over | no |
| `achievements.json` | Milestones | fixed set | n/a | no |

### Workflows, assessments, approvals and devices

| Store | Holds | Bound | Swept | In backup |
|---|---|---|---|---|
| `workflows.json`: runs | Workflow runs | 7 days (`workflowruns.go:27`) | **at start only** (`workflowruns.go:1054`), and when a run ends | no |
| `workflows.json`: states | One state per (folder, workflow) ever seen | **none.** `GoneSince` (`workflows.go:59`) is declared and never set. | only the app's Remove | no |
| `workflow-copies/<sha256>` | Approved workflow and skill text | The copies that the states name (`workflows.go:244`) | after each save | no |
| `assessments/*.json` | Runtime assessments | newest 50; 90 days from the start; 256 KiB per file (`assess_store.go`, #6724) | hourly, on start and on score | no |
| `mcp-events.json` | Seen MCP event IDs | 2,000 IDs, 7 days (`mcpevents.go:53-56`) | on write | no |
| `mcp-views/*.json` | View catalogues | pruned (`viewservers.go:369`) | on write | no |
| `mcp-approvals.json`, `plugin-approvals.json` | Approval digests | **none.** Grows with folders × servers. | MCP: only on explicit remove; plugins: never | no |
| `devices.json` | Paired devices | **none** | only Forget (`devices.go:257`) | no |
| `review-dismissals.json` | Dismissed PRs | 2,000 of 8 KiB each, 90 days from the PR's last change or its dismissal (`reviews.go`, #6728) | hourly and on write | no |
| `review-opened.json` | PR URLs per folder | 200 per folder (`reviewOpenedPerFolder`). **Folder keys are never pruned.** | on write | no |
| `lock-seen.json` | Signed request IDs | 4,096, 2 days (`lockcheck.go`) | hourly and on write | no |
| `attention.json` | Needs, deliveries | live needs only | on write | no |
| `leases.json`, `resources.json` | Resource leases | 240-minute lease cap; dropped on agent stop (`leases.go:28,383`) | on write | no |
| `guarded-files.json` | Guarded file states | folders that exist | on save | no |
| `org-routes/<id>.json`, `runtimes/org/…` | Org key routes, stand-in homes | 50 routes (`orgroutes.go:46`) | on delivery | no |
| `runtimes/accounts/<rt>/<id>/` | Homes of added runtime accounts, **with the runtime's own transcripts** | removed with the account (`accounts.go:343`) | on remove | no |
| `browser/profile/` | Chromium profile shared by every browser sign-in | **none** | never | no |
| Small fixed files (`folders.json`, `root-id.json`, `project-tools.json`, `wake.json`, `policy*.json`, `backup-settings.json`, `settings/*.json`, …) | Configuration | fixed shape | n/a | no |

### Transfers, backups and staging

| Store | Holds | Bound | Swept | In backup |
|---|---|---|---|---|
| `backups/<rootID>/*.tar.gz` | Machine backups | Keep 1–10, 3 by default (`backups.go:45-46`); `.tmp` after 24 h | after each backup. **Other machines' folders in a shared backup folder are never pruned.** | n/a |
| `backups-upload/`, `backups-download/` | Server backup staging | removed after use | on use | no |
| `.restore-*` | Restore staging | deferred `RemoveAll`. **A crash leaves it.** | never | no |
| `Outgoing/`, `Incoming/` | Export, send, import | 1 hour untouched (`send.go:25`) | **only on the next transfer, or at start** | no |
| `Clones/<id>` | Clone staging | removed after the clone | and at start (`folders.go:740`) | no |

### Caches and logs

| Store | Holds | Bound | Swept |
|---|---|---|---|
| `<logs>/daemon.log` (+ `.previous.log`) | Daemon log | 4 MiB + 1 older (`rolllog/rolllog.go:13`). Output written straight to the duplicated stderr (a panic) is not checked until the next write. | on write |
| `<logs>/mcp-logs/<name>-<hash>.log` | Hosted MCP stderr | 1 MiB + `.1`, 32 servers (`hostedmcp.go:57,824`). A single write over 1 MiB is written whole. | on open |
| `<cache>/tools/<runtime>/<id>` | Pinned runtime toolsets | only the current one (`install.go:543`) | at host start |
| `<cache>/browser/chromium`, `toolsets` | Playwright Chromium | old revisions are left to Playwright | on install |
| `<cache>/browser/output/<agent>` | Playwright output | removed when the session ends. **A crash leaves it.** | session end |
| `<cache>/catalog-staging/<id>` | Skill previews | 8, 30 minutes (`skillcatalog/preview.go:37-38`). The list is in memory only, so **folders left over at a restart are never swept**. | on Put/Get/Take |
| `<cache>/option-cache.json` | Runtime options | 50 (`options.go:44`) | on write |
| `~/Library/{Caches,Logs}/…/roots/<name>-<hash>` | Cache and logs for each non-default root | **none on count**; never removed when the root is deleted | never |

### `~/.agents` and `~/.agents-secrets`

| Store | Holds | Bound | Swept |
|---|---|---|---|
| `~/.agents/settings/*.json` | Settings that follow the person (see `docs/reference/agents-folder.md`) | fixed shape | n/a |
| `~/.agents/summaries/*` | Daily summaries, none written since #5853 | the newest 3, 90 days after their last change (#6714) | hourly |
| `~/.agents/checks/suggestions.json` | Daily-check findings | 50 suggestions, 200 dismissals (`dailychecks.go:38,48`) | on write |
| `~/.agents-secrets/secrets.env`, `connections.json`, `provider-secrets.json` | Personal secrets, OAuth connections (Linux; the Keychain on a Mac), machine provider secrets | one per entry | the person edits them |
| `~/.agents-secrets/sessions/<agent>.json` | Session-only secrets | Values go at archive; the file goes at delete; orphans are swept at start (`sessionsecrets.go:221-275`) | archive, delete, start |

## 2. In each folder (`<folder>/.agents/`)

| Store | Holds | Bound | Swept |
|---|---|---|---|
| `attachments/` | Files attached to prompts | 500 MB per folder; 7 days if no kept transcript names the file; `.tmp` after 1 day (`attachments.go:27,28,34`) | hourly, at start, on write. **Only in folders that still have an agent** (`attachments.go:82-90`). |
| `dropbox/**` | Files handed to a workflow | 25 MB per file (`host.go:587`). **No total, count or age limit.** | never |
| `worktrees/<name>` + branch `agents/<name>` | Git worktrees agentsd makes | **none, on purpose: user-owned** (#6708, closed as not planned) | when its agent is archived or deleted (#3726, #3877) |
| `reviews/runtimes/<rt>-<date>.md` | Runtime assessment reports | 1 MiB per report; newest 50 per machine; 90 days; not those git tracks (`assess_reports.go`, #6712) | hourly, on start and on score |
| `workflows/assess-<id>.md` | Assessment throwaway workflow | **none** | never |
| `pins.json` | Pinned pages, views, sessions and workflows, and hidden views | 10 pages and views, 10 sessions and 10 workflows, 4 KiB a pin; 50 hidden views, 1 KiB each; 128 KiB in all; a write past that is refused (`pins.go`, #6725). No age, on purpose: current state, until removed | on write, on archive |
| `folder.json`, `policy.json`, `mcp.json`, `personas/`, `skills/`, `plugins/`, `workflows/`, `docs-maps.json`, `marketplaces.json`, `mcp.removed.json`, … | Folder configuration (meant to be committed) | small; `mcp.removed.json` capped (`mcpcatalog/files.go:26`) | edited in place |

## 3. Outside our folders

Agents starts these processes and tools, but it does not own what they store.

| Store | Holds | Bound | Cleared by |
|---|---|---|---|
| `~/.claude/projects/<slug>/<sid>.jsonl` (or `CLAUDE_CONFIG_DIR`, and `<root>/runtimes/accounts/claude/<id>`) | Claude's copy of each conversation | **none, because of a bug.** Erasure looks in `folders/`, not `projects/` (`agentsd/erasure.go:371-372`): **#3625**. 6.0 GB on Alex's Mac. | erasure, after #3625 is fixed |
| `~/.claude/{todos,file-history,session-env,debug}/<sid>*` | Claude's per-session extras | erased (`erasure.go:373-376`) | agent delete |
| `~/.codex/sessions/…/rollout-*-<sid>.jsonl` (or `CODEX_HOME`) | Codex's copy | erased (`erasure.go:381-382`) | agent delete |
| Grok, Copilot, OpenCode, Cursor and Antigravity session stores | Their copies | **none**, unless the adapter supports ACP `session/delete`; the code knows only Claude's and Codex's files (`erasure.go:302-310,367-386`) | — |
| `~/.claude/history.jsonl`, `~/.codex/*.sqlite`, `history.jsonl`, … | Runtime-wide history and indexes | **none** (not ours) | — |
| `~/.npm/_npx` | `npx` cache for the Claude ACP adapter (`agentsd/runtimes.go:45`) | npm's own | — |
| Docker images and named volumes for Docker workspaces | Images pulled, volumes | **none.** `docker rm -f -v` removes containers and anonymous volumes; images and named volumes stay (`workspacecommands/presets.go:89-95`). | — |
| DigitalOcean, Codespaces, EC2 and GCP machines | Cloud machines | **none in count or time**; no idle stop | only `delete_machine` |
| `~/.ssh/config`, one block per codespace | `gh codespace ssh --config` output | **none**; no code removes a block (`codespacessh/codespacessh.go:131-152`) | — |
| `~/Library/…/NativeMessagingHosts` | Chrome native messaging manifest | 1 file | — |
| `$TMPDIR/agents-*` (Go) | Git indexes, screenshots, ssh install scratch | removed with `defer` | function exit |
| `/tmp/agents-tunnels-<uid>-<pid>` | ssh control sockets | removed on clean stop. **A crash leaves one per pid.** | — |
| `$TMPDIR/agents-output-<id>.<ext>` (Mac window) | Background output opened in TextEdit (`App/Sources/Sidebar/BackgroundPane.swift:70-77`) | **none** | macOS temp purge only |
| `~/.Trash/<skill>` | Removed skills | the person's Trash | — |

The MCP servers in `cmd/*-mcp` write nothing to disk. Their state is in memory and capped (for
example, the WhatsApp inbox holds 1,000 items, `whatsappmcp/inbox.go:29`). Tokens come from the
environment.

## 4. On the control plane

The store is a folder (`~/Library/Application Support/Agents Control/store/`) or a bucket
(`s3://<bucket>/control/v1/…`, `Host/Sources/StoreChoice.swift:121,143`).

One hourly sweep (`controlplane/sweep.go:15-30,424`) deletes at most 1,000 objects a round, and
runs rounds until it is done. It sweeps audit and seals, codes, tombstones, orphans, usage, runs
and lineage, and it runs the backups' `pruneOrphans` once a day. **Joins, lockjoins, signins,
linkRequests, and policy, governance and remove requests are pruned only when read.**

`agents-control backup` copies all of `v1/` except `v1/leases/` and `v1/copies/`. A store copy or
handover also leaves out `v1/keys/` (`storecopy.go:21,25`).

### People, machines and clients

| Key | Holds | Bound | Swept |
|---|---|---|---|
| `v1/control.json` | Owner, endpoints, epoch, control key public parts, audit days | 1 | — |
| `v1/clients/<UUID>.json` | Name, kind, public key, last seen, APNs token, ≤16 Live Activity tokens (12 h each), Web Push subscription | 16 KiB a record, 1 MiB in all (#6757; hosted: 5/50 clients too). Key life 90 days (`keylife.go:18`). | Tombstone 7 days. A client whose key expired is forgotten by the hourly sweep (#6757). |
| `v1/machines/<id>.json` | Name, key, platform, version, owner, workspace | 32 KiB a record, 1 MiB in all (#6758; hosted: 2/20 machines too) | Tombstone 7 days. A machine with no workspace whose key expired is removed by the hourly sweep (#6758); a linked one stays until removed. |
| `v1/people/<id>.json` | Name, role, sign-ins (email, login), SCIM part, old names (90 days) | No cap | A removed person goes after 14 days, or 90 when they have a sign-in (#6756) |
| `v1/groups/<id>.json` | Name, members | 1,000 (`groups.go:20`) | tombstone 7 days |
| `v1/projects/<id>.json` | Name, owner, slots, ≤20 boards | 500 per person (`projects.go:36`) | tombstone 7 days |
| `v1/projectFolders/<host>.json` | A machine's joined folders | 1,000 | orphan sweep |
| `v1/locks/<person>/<n>.json` | Machine-lock chain | 4,096 per person (`machinelock/lock.go:40`) | only on Reset. **Not deleted when the person is removed.** |
| `v1/users/<u>/settings.json`, `notices.json`, `secrets/<NAME>.json` | Person settings, notice bucket, sealed secrets | 256 KiB part, 16 KiB secret, 200 secrets (`usersettings/usersettings.go:45-49`) | on person removal |
| `v1/links/<channel>/<sha>.json` | Channel identity → person | **no cap** | on unlink or person removal |

### Requests, codes and sign-in

| Key | Holds | Bound | Swept |
|---|---|---|---|
| `v1/codes/<id>.json`, `.spent` | Purpose, owner, invite name and role, expiry | **No count cap**; only rate limited (2/s, burst 20, `limits.go:46-47`) | 24 h after expiry |
| `v1/joins/`, `v1/lockjoins/`, `v1/signins/` | Waiting join, lock-join and sign-in asks (keys, source IP) | 50 / 200 / 50 open, per-person caps; 10-minute life | 1 h after expiry, **on read only** |
| `v1/linkRequests/<id>.json` | Link asks | 50 open; 24 h | on read only |
| `v1/oidc/state/`, `v1/oidc/tickets/` | PKCE verifier, sealed refresh token | 10 / 2 minutes. **No count cap.** | 1 h, hourly |
| `v1/signin.json`, `v1/signin/providers/`, `v1/signin/domains/` | Sign-in settings, org OIDC providers, domain claims | 5 providers, 20 domains | on remove |
| `v1/signin/refresh/<person>/<prov>.json` | Sealed org refresh token | 1 per person per provider | person removal |
| `v1/scim/<prov>/names/<sha>.json` | userName → person | 1 per SCIM user | rename or delete. **Not on an admin's remove.** |
| `v1/scim/tokens/`, `v1/read/tokens/` | Token hashes, last-use IP | 2 live / 20; 10 live / 50 | trim on write |
| `v1/requests/remove/<user>.json` | SCIM delete → admin ask | 1 per person | removal or read |
| `v1/policy/requests/`, `v1/governance/requests/` | Budget and catalogue asks | 1,000 total; decided ones 30 days | on read |

### Work data

| Key | Holds | Bound | Swept |
|---|---|---|---|
| `v1/kept/<host>/<seq>-<id>.json` | Prompts and agent starts for a stopped workspace, **with attachments** | 20 per host, **waiting ones only** (`providers/kept.go:21,176`); each up to 40 MiB | on delivery or host removal. **Failed ones are never deleted.** |
| `v1/copies/<host>.json` | A host's last folders and agents (≤500, **with titles**) | 1 per host | on host forget |
| `v1/usage/hours/`, `v1/usage/days/` | Usage and cost by person, project, folder, machine, runtime | 7 days / 90 days (`reports.go:30-31`) | hourly |
| `v1/lineage/<host>.json` | Cost per issue, PR and session node | 5,000 nodes, 90 days (`lineagecost.go:25-28`) | on write |
| `v1/lineage-graph/<host>/links.json`, `states.json`, `changes/*.json` | A machine's lineage links and its sessions' state changes (#4275); each send's changes since the last roll-up (#6584) | Links by `lineage.Bound` (ended 90 days, 20,000); states as on the machine (90 days, each session's last, `lineage.MostStates`); at most `mostLineageChanges` (32) changes before a send or the hourly sweep rolls them up; a removed machine's after a day (`lineagegraph.go`) | on write, hourly |
| `v1/deploys/<host>.json` | A machine's deploys, as deploys/delivery's figures need them: refs, names, states, times, tiers, merge times; no titles or logs (#5496) | 1,000 deploys a send, each in place of the last; of each deploy the newest 1,000 merges and 200 health signals, 20 environments; a removed machine's after a day (`deploys.go`) | on write, hourly |
| `v1/build-timings/<host>/<forge>/<path>/<day>.json`, `meta.json` | A machine's CI runs, jobs and steps of a repository: refs, times, conclusions, runner labels, names; no titles or logs (#5232) | As on the machine (`citimings`: 90 days, 20,000 runs a repository, PR steps 14 days), each day bounded on write; a stopped machine's swept once a day; a removed machine's after a day (`builds.go`) | on write, hourly |
| `v1/runs/<day>/<hash>.json` | Workflow trigger claims | 7 days; **no per-day cap** | hourly |
| `v1/reports/views/<user>.json` | Saved report views | 50 | person removal |
| `v1/providers/<id>.json` | Machine providers | **No cap. Tombstones are never swept.** | — |
| `v1/machine-names/<person>.json` | The person's machine-N counter (#3972), the provider New Machine uses and what the person chose on each provider's form last (#3979; only the values that were not the form's defaults, in `chosen`, #6686) | 1 per person: one number, one provider id, one set of choices per provider that is still here; 64 KiB (#6759) | person removal, and hourly for a removed person; no age before that (a number is never used again) |
| `v1/tunnels/<id>.json` | Server reverse tunnels | unjoined 15 minutes | on use |
| `v1/policy.json`, `v1/policy/versions/` | Org policy and old versions | 64 KiB; 100 versions | on write |
| `v1/governance/machines/`, `v1/governance/approvals/` | Installed servers and skills; approvals | 500 items; 1,000 approvals, **no expiry** | — |
| `v1/org/routes/<id>.json` + `.secret.json` | Org keys and gateways | 50 routes, 4 MiB sealed | tombstone swept |

### Audit, keys and backups

| Key | Holds | Bound | Swept |
|---|---|---|---|
| `v1/events/<day>/…` | Audit entries (who, method, host, target) | 90 days by default, 1–3,650 (`audit/audit.go:29-33`); hosted plans 7 / 90 days. **No size cap per day.** | hourly, by day |
| `v1/events/<day>/<ms>-<rand>.json` | **Mesh change events** (several copies only) | **The audit's days, so up to 10 years** (`mesh.go:675`) | same sweep |
| `v1/audit-seals/`, `v1/audit-sinks.json` | Daily chain heads; ≤4 SIEM sinks with sealed tokens | with the day; 4 | — |
| `v1/keys/control.sealed` (+ previous) | KMS-sealed control key | 2; previous after 30 days | — |
| `v1/backups.key` | Sealed key for server backups | 1 | — |
| `v1/backups/<owner>/<host>/<name>/…` | Servers' sealed backups (transcripts) | 8 MiB × 1,024 parts = 8 GiB a backup; Keep 1–10, **so up to ≈80 GiB a host**; a removed host's for 90 days (`backups.go:52-56`) | on commit, daily. Stray parts age by a **name time the host chooses**, so a future time never ages. |
| `v1/leases/`, `v1/copies/<copyID>.json` | Mesh leases and heartbeats | 30 s | orphan sweep |
| `handover/forwarding-*`, `handover/mark-*` | Move forwarding; move check marks | forwarding ≤30 days. **An abandoned mark stays for ever.** | — |
| `probe/<copy>-<hex>` | Start-up probe | removed with `defer`; a folder store sweeps after 1 h. **On S3, never.** | — |

## 5. The hosted service

| Key | Holds | Bound | Swept |
|---|---|---|---|
| `hosted/v1/accounts/<id>.json` | Sign-in provider and subject, login, plan, suspension, certificate time, **Stripe customer and subscription IDs** | 100 accounts (`hosted/signup.go:40`) | Delete Account |
| `hosted/v1/{github,apple,google}/<subject>.json` | Sign-in → account | 1 per account | Delete Account |
| `hosted/v1/invites/<code>.json` | Expiry and **who used it** | 14-day life | **never; survives Delete Account** |
| `hosted/v1/waiting/`, `waitlist/` | Waitlist | 10,000 | only on sign-up |
| `hosted/v1/allow/`, `blocked/` | Operator lists | operator | operator |
| `hosted/v1/stripe/<evt>.json` | Stripe events seen | 30 days, daily (`billing.go:25`) | daily |
| `hosted/v1/usage/<id>.json` | Account byte count | 1 per account | with the account |
| `accounts/<id>/v1/…` | That account's whole control-plane store (section 4) | Plan: storage 1 / 20 GB, machines 2 / 20, clients 5 / 50, events 7 / 90 days (`hosted/plans.go:37-40`) | Delete Account |
| `<dir>/<date>/<account>/…` (operator's disk) | Per-account backups | **none unless `--keep N`** (`cmd/agents-hosted/admin.go:162-191`) | — |

Sessions are signed cookies (30 days); the server keeps nothing for them.

## 6. Agents Host on the Mac

| Store | Holds | Bound |
|---|---|---|
| `~/Library/Application Support/Agents Control/` | `store/`, `tls/`, `secrets-key`, `apns.p8`, `web.json`, `control-key` (scratch) | fixed |
| `…/Agents Control/store-before-<…>` | A whole old store, set aside on a store switch or a move back (`Host/…/HostModel.swift:561,791`) | **none; never pruned.** Each holds the audit, sealed secrets and server backups. |
| Keychain `com.alexecollins.agentshost` | `control-key`, `bucket-keys`, `control-key-before-<unix>` | **`control-key-before-*` is added on each move back and never deleted** |
| `~/Library/Logs/Agents/control.log` | Control plane log | 4 MB + 1 older (`controlplane/log.go:20`) |
| Web Push VAPID key (`AGENTS_WEBPUSH_KEY_FILE`) | Push signing key | 1. **Not in `agents-control backup`**, so a restore breaks browser push. |

## 7. The clients

### Mac window

| Store | Holds | Bound | Cleared |
|---|---|---|---|
| Defaults `draft.agent.<uuid>`, `draft.new.<folder>`, `draft.start` | Unsent prompts and attachments, ≤1 MiB each | 40 drafts, a new one over that refused; 30 days (`DraftStore.swift`); removed on archive (`App/Sources/Chat/DraftKeeper.swift`) | archive, 30 days, send |
| Defaults `newSessionProjectFolders` | Last folder per project | **none** | never |
| Defaults `sidebar.folds` / `folded` / `openSessions` | Sidebar fold state | `openSessions` 200; **fold sets none** | never |
| Defaults `sidebar.<window>.width` / `.pane` | Inspector size per folder window | **none** | never |
| Defaults `machineLockPin.<ctrlkey>` (+ `.lock`, `:<person>`) | Machine-lock pins and the stored lock | **none**; one set per control plane and person | only on a lock reset. **Not on Forget.** |
| Defaults display and UI choices | Fixed key set | fixed | — |
| Defaults domains `<bundle>.walk.*`, `.root.*` | A scratch copy's preferences | walk: when its folder is gone; root: 7 days (`WindowDefaults.swift:38`) | at launch |
| `…/Application Support/Agents/control-client.json` | Pairing (0600, not backed up) | 1 | Forget This Mac |
| Keychain `control-client-key`, `lock-signing-key` (+ `.walk.<name>`) | Client key, lock signing key | **one more per walk name, never removed** | never (Forget keeps the client key by design) |
| `WKWebsiteDataStore.default()` (browser pane) | Cookies, logins, localStorage, caches of every site any agent opened (`App/Sources/Sidebar/BrowserPane.swift:172`) | **none from the app** | never |
| Kokoro voice models (FluidAudio cache) | TTS models, copied or downloaded | **none**; can be hundreds of MB (`Shared/UI/Voice/KokoroVoice.swift:89-107`) | never |
| `~/Library/Logs/Agents/crashes/`, `hangs/` | Crash notes, hang stacks | 20 each | at launch |
| `$TMPDIR/agents-output-*` | See section 3 | none | — |

### Remote (iPhone and iPad)

| Store | Holds | Bound | Cleared |
|---|---|---|---|
| Defaults `draft.agent.<uuid>`, … | Unsent prompts and attachments, ≤1 MiB each | As the Mac: 40 drafts, 30 days, archive (`Remote/Sources/StartAgent/StartDraftKeeper.swift`) | archive, 30 days, send |
| App group `Library/Caches/attention-snapshot.json` | The widget's needs-you rows **with headlines in plain text** | 12 rows; stale after 15 minutes | **not on Forget**: the widget keeps showing the old pairing |
| Defaults `machineLockPin.*`, `newSessionProjectFolders`, fold sets | As on the Mac | **none** | never |
| Defaults `liveActivityTurns` | Turns covered by the Live Activity | the activity's sessions | when the activity ends |
| `Library/Caches/remote.log` (+ `.1`) | Device log | 256 KiB × 2 | rotation |
| `Application Support/Agents/control-client.json` | Pairing | 1 | Forget This iPhone |
| Keychain `device-key` (shared group), `control-client-key`, `lock-signing-key` | Device and client keys | 1 each | never |
| Delivered notifications, categories | Banners | 32 categories | on reconnect; not on Forget |

### Web page

| Store | Holds | Bound | Cleared |
|---|---|---|---|
| IndexedDB `agents` | Non-extractable pairing key, lock signing key | 1 | Forget, re-pair, forgotten by the control plane |
| IndexedDB `agents-signin` | Pending org sign-in key (10 minutes) | 1 | on use; expiry checked on read |
| localStorage `agents.drafts` | Drafts | 40 drafts, 256 KiB attachments, 30 days without an edit (`Web/src/model/drafts.ts`) | send, 30 days, Forget |
| localStorage `agents-lock-pin`, `…:<person>` | Machine-lock pins | **none. Not keyed by control plane**, so a re-pair to another one reads the old pin. | never |
| localStorage folds, `newSession.projectFolders`, start form, UI choices | UI state | fold sets and project map **none** | never |
| sessionStorage filters and sort | Per tab | fixed | tab close |
| Service worker `/sw.js` + push subscription | Web Push (no offline cache) | 1 | **not on Forget** |

## 8. In memory

Only data that grows is listed here. Most in-memory caches have a bound; the ones without one
are in bold.

| Process | What | Bound |
|---|---|---|
| agentsd | **`d.sessionCost`** (`agentsd/cost.go:278`) | **none**: one entry per agent that ever cost, for the life of the process |
| agentsd | **`d.viewCalls`** (`views.go:128`) | 64 calls per agent; **agent keys never removed** |
| agentsd | **`undoNotes.byAgent`** (`turnundo.go:399`) | **none until the agent's next turn** |
| agentsd | **ACP terminal buffer** (`acp/session.go:811`) | 256 KiB default; **the runtime sets the limit, with no upper cap** |
| agentsd | Event consequences (`events.go:248`) | **no count cap** |
| agentsd | Held lines when the disk is full | 1 MiB per agent (`store/held.go:23`) |
| agentsd | Transcript line indexes, turns caches | 32 each |
| agentsd | Terminal scrollback | 4 MiB a terminal (`shells.go:34`) |
| control plane | **`notices.Desk.posted`** (`controlplane/notices/desk.go:29`) | **none**; a need that is never withdrawn (host crash) stays |
| control plane | **Peer `presence`** (`router.go:705`) | **none**: added by client ID, removed by session ID, so never removed |
| control plane | **`storecopy` `written`** (`storecopy.go:68`) | **the whole store in memory** during backup, store copy or handover, server backups included |
| control plane | Socket out-queue | 8,192 messages × 1,024 sockets, **by count, not bytes** (messages up to 40–64 MiB) |
| control plane | `readcache` | 40,000 entries, **by count, not bytes** |
| control plane | `clientRates` | 4,096, **soft** (only idle entries go) |
| control plane | Audit writer queue, sink outboxes, push queues | 10,000; 10,000 × 4; 256 (drop when full) |
| Mac + Remote | **`AgentsModel` entries, turns, artifacts, file changes** | 600 / 48 / 100, **but trimmed only while the chat follows the end** (`AgentsModel.swift:643`). A chat scrolled up or paged back grows without limit, once per popped-out window. |
| Mac + Remote | `recentEvents` | 1,000 on live insert; **paging back is not trimmed** (`AgentsModel.swift:1218`) |
| Mac | `ServerPictures`, `PinnedPage` pictures | 24 per host, **no byte limit** (the phone has 64 MiB) |
| Web | Chat history | 600–800 entries; paging back stops at 1,600 entries / 200 turns (`Web/src/model/store.ts:86-87`) |
| Web | **`events[host]`** (`store.ts:3561-3593`) | **none** in the page; each load merges into the list. The daemon's 10,000 is the real limit. |
| All clients | `chartHints`, `pageRevisions`, `leadAsked`, `startedPages` | **none**, small |

## 9. Kind mapping and unresolved bounds

The old ranked list and the old comparison of the reader page with this inventory are retired.
They describe findings from an earlier code snapshot. Use the current store rows above for
observed bounds. Use the approved kind mapping and retention decisions for the target contract.

The mapping below covers mixed families. A family with more than one kind must split by the
access pattern shown; do not label the whole file with one kind.

| Store family | Kind | Bounds and action |
|---|---|---|
| Whole JSON files and singleton records | Document | Per-store record, collection, age, and refuse or evict limits are **not specified until the responsible store issue sets them**. |
| Transcripts and ordered event histories | Log | Transcript: 10 MB per chat and 90 days (#6706); runtime home: 1,200 MB and 90 days (#6707). Audit detail: configured 1–90 days; byte/count limit **not specified** (#6755). |
| Usage, cost, timing, health, and capacity samples | Time series | 168 hours of hourly detail, then daily roll-up through 90 days where the store is usage data; each store's byte or count bound is **not specified until its issue sets it**. |
| Lineage links and provenance | Graph plus log | Graph projection and ordered change log are separate stores; each collection's numeric bound is **not specified until its responsible issue sets it**. |
| Attachments, artifacts, exports, backups, binary request bodies | Blob | Use #5713. Per-blob and owner quotas are **not specified until the responsible store issue sets them**. Keep and operator backup: 10 MB each, 90 days (#6709, #6787). |
| Current state, indexes, routes, policy, and workflow run state | Key-value | Per-record, collection, and age limits are **not specified until the responsible store issue sets them**. |
| Local copies, work cache, browser data, derived indexes | Cache | Evict by declared byte/count and age limits; individual limits are **not specified until the responsible store issue sets them**. |
| Credentials, signing keys, and sealed connection material | Secret | Never expose plaintext through a list operation. Record lifetime and size limits are **not specified until the responsible store issue sets them**. |

### Stores that break a rule, with their kind

One row for each child issue of #6809, #6810 and #6811: the store, its kind, its bound as read
from the code on 2026-11-21, the evidence, and the issue that brings it into line. A row here is
newer than a row above for the same store.

| Store | Kind | Holds | Bound | Evidence | Issue |
|---|---|---|---|---|---|
| `<root>/agents/<id>/` (agent.json, transcript.jsonl, app-tools.jsonl, said.json) and `<cache>/turns/` | log | Conversations | Archived agents go after 7/14/30/90 days or Forever; a live or pinned agent never ages; no size limit on a transcript | `Go/internal/agentsd/retention.go:89-93; store/locations.go:100-102` | #6706 |
| `<root>/runtimes/accounts/<rt>/<id>/` | blob | Runtime account homes, with the runtimes' own transcripts | None, until the account is removed | `Go/internal/agentsd/accounts.go:274` | #6707 |
| `<folder>/.agents/worktrees/` | blob | Agent worktrees | User-owned: no bound, on purpose | `docs/explanation/compliance/data-and-retention.md (In each folder)` | #6708 |
| `<root>/backups/` | blob | This machine's backups | The newest Keep (1-10); no size limit on a backup; no age | `Go/internal/agentsd/backups.go:44-45` | #6709 |
| `<root>/browser/profile/` | blob | Browser sign-ins and cookies | Caches emptied at start, 64 MB HTTP cache; cookies until the site ends them | `Go/internal/agentsd/browser/browser.go:139,637` | #6710 |
| `<folder>/.agents/dropbox/` | blob | Files handed to workflows | 25 MB a file, 500 MB a folder; no age, on purpose (#3648) | `Go/internal/agentsd/host.go:689,693` | #6711 |
| `<folder>/.agents/reviews/runtimes/` | document | Runtime assessment reports | 1 MiB per report; newest 50 per machine; 90 days | `Go/internal/agentsd/assess_reports.go` | #6712 |
| `~/.agents/dismissed-leads.json` | key-value | Dismissed epics | None: an entry goes only when its epic gets a lead | `Go/internal/agentsd/worksuggest.go:32,55-59,165` | #6713 |
| `~/.agents/summaries/` | document | Daily summaries from before #5853 | 1 MiB per page; newest 3; 90 days | `Go/internal/agentsd/oldsummaries.go` | #6714 |
| `<root>/erased.json` | document | IDs of deleted agents | 10,000 IDs, 400 days | `Go/internal/agentsd/erasure.go:43-44` | #6715 |
| `<root>/repo-aliases.json` | key-value | Repo renames | 1,000 renames of 2 KiB each, 90 days, the oldest first | `Go/internal/agentsd/repoaliases.go` (`openRepoAliases`) | #6716 |
| `<root>/release-tags.json` | log | Linked release tags | 5,000 tags of 1 KiB; 90 days | `Go/internal/agentsd/lineagerelease.go` | #6717 |
| `<root>/requests/<id>.json` | document | Open questions and forms | Ended ones 14 days, 1,000 files; an open one has no age | `Go/internal/agentsd/requests.go:27-28` | #6718 |
| `<root>/lineage-deploys.json` | key-value | Each deploy's last event | 1,000 deploys of 16 KiB each, 90 days from the last event, the oldest first | `Go/internal/agentsd/lineagedeploy.go` (`openDeploys`) | #6719 |
| `<root>/account-ops.json` | key-value | Last operation of each account and secret | 1,000 entries of 1 KiB each, 90 days, the oldest first | `Go/internal/agentsd/accountops.go` (`openAccountOps`) | #6720 |
| `<root>/elsewhere.json` | key-value | Sessions on other machines | 800 entries of 64 KiB each, 90 days from when the machines last spoke of the session, the oldest first | `Go/internal/agentsd/elsewhere.go` (`openElsewhere`) | #6722 |
| `<root>/secret-ops-pending.json` | log | Secret audit entries not yet sent | 1,000 operations of 16 KiB each, 90 days, the oldest first | `Go/internal/agentsd/secretops.go` (`openSecretOpsPending`) | #6721 |
| `<root>/lock-seen.json` | key-value | Replay IDs of signed requests | 4,096 of 128 bytes each, 2 days, the one signed first goes first; a cutoff refuses what went | `Go/internal/agentsd/lockcheck.go` (`openLockSeen`) | #6723 |
| `<root>/assessments/` | document | Runtime assessments | The newest 50; 90 days from the start; 256 KiB per file | `Go/internal/agentsd/assess_store.go` | #6724 |
| `<folder>/.agents/pins.json` | key-value | Pinned pages, views, sessions and workflows, and hidden views | 10 of each and 50; 4 KiB a pin, 1 KiB a hidden view, 128 KiB a folder; until removed (no age, on purpose: a person's choices, committed with the folder) | `Go/internal/agentsd/pins.go` (`pinsFileMost`) | #6725 |
| `~/.agents/checks/suggestions.json` | document | Daily-check findings and dismissals | 50 findings, 200 dismissals, no age | `Go/internal/agentsd/dailychecks.go:39,49` | #6726 |
| `~/.agents/mcp.removed.json` | key-value | Removed MCP servers | 20, no age | `Go/internal/agentsd/mcpcatalog/files.go:27` | #6727 |
| `<root>/review-dismissals.json` | key-value | Dismissed PR links | 2,000 of 8 KiB each, 90 days from the PR's last change or its dismissal, the oldest first; For Review leaves out a PR unchanged for 90 days; goes with its folder | `Go/internal/agentsd/reviews.go` (`openReviewDismissals`) | #6728 |
| `<root>/review-opened.json` | key-value | Opened PR links | 200 a folder; until the folder goes | `Go/internal/agentsd/reviews.go:31` | #6729 |
| `<root>/workflows.json` (states) and `workflow-copies/` | key-value | Workflow states and approved copies | While the workflow file exists, then 30 days | `Go/internal/agentsd/smallstores.go:21` | #6730 |
| `<root>/connector-setup.json` | key-value | Connector set-up for each folder | Until the folder leaves the machine | `Go/internal/agentsd/connectorsetup.go:386-391` | #6731 |
| `<root>/catalog-skills.json` | key-value | Installed catalog skills | Until the item is removed | `Go/internal/agentsd/skills.go:48` | #6732 |
| `<root>/catalog-mcp.json` | key-value | Installed catalog MCP servers | Until the item is removed | `Go/internal/agentsd/mcpcatalog.go:84` | #6733 |
| `<root>/devices.json` | document | Paired devices | 90 days unseen; no count limit | `Go/internal/agentsd/smallstores.go:22` | #6734 |
| `<logs>/daemon.log` | log | What the machine did | 4 MB and one older file; size only, so an idle machine keeps lines older than 90 days | `Go/internal/rolllog/rolllog.go:14` | #6735 |
| `<logs>/mcp-logs/` | log | Hosted MCP servers' stderr | 1 MB a server, 32 servers; size only | `Go/internal/agentsd/hostedmcp.go:58,865` | #6736 |
| `<file>.unreadable-*`, `<file>.corrupt-*` | blob | Stores set aside when unreadable | The newest 3, no age | `Go/internal/agentsd/store/files.go:79` | #6737 |
| `~/.agents-secrets/sealed.json` | secret | Sealed secrets | No count limit | `Go/internal/agentsd/sealedsecrets.go:122` | #6738 |
| `~/.agents-secrets/sessions/` | secret | Secrets for one session | No count limit; values go on archive, the file on delete | `Go/internal/agentsd/sessionsecrets.go:57` | #6739 |
| `~/.agents-secrets/mcp-sign-ins.json` | secret | MCP OAuth tokens | No count limit | `Go/internal/agentsd/mcpcatalog/signins.go:45,134` | #6740 |
| `~/.agents-secrets/hints.json` | secret | Last 4 characters of each secret | No count limit | `Go/internal/agentsd/secrethints.go:19` | #6741 |
| `~/.agents-secrets/descriptions.json` | key-value | Secret descriptions (not secret) | No count limit; config outside .agents | `Go/internal/agentsd/secretlist.go:37` | #6742 |
| Keychain `agents.connection.<hash>` (Linux `~/.agents-secrets/connections.json`) | secret | Forge connections: the token and settings that are not secret | No count limit; settings kept with the secret | `Go/internal/connections/store.go:110-156` | #6743 |
| `<root>/folders.json` | key-value | The folders the person added | Config in the data root | `Go/internal/agentsd/store/locations.go:104` | #6744 |
| `<root>/wake.json` | key-value | Keep awake, grace hours, warm runtimes | Config in the data root | `Go/internal/agentsd/wake_darwin.go:18` | #6745 |
| `<root>/backup-settings.json` | key-value | Backup target and Keep | Config in the data root | `Go/internal/agentsd/backups.go:43,127` | #6746 |
| `<root>/allowances.json` | key-value | Allowance for each credential | Until its account is removed; config in the data root | `Go/internal/agentsd/allowances.go:32; accounts.go:361` | #6747 |
| `<root>/runtime-accounts.json` | key-value | Runtime accounts the person added and named | Until removed; config in the data root | `Go/internal/agentsd/accounts.go:63` | #6748 |
| `<root>/mcp-approvals.json` | key-value | The person's MCP server approvals | Until the folder goes; config in the data root | `Go/internal/agentsd/smallstores.go:28; mcpservers.go:315` | #6749 |
| `<root>/plugin-approvals.json` | key-value | The person's plugin approvals | Until the folder goes; config in the data root | `Go/internal/agentsd/smallstores.go:29; plugins.go:56` | #6750 |
| `<root>/policy.json`, `policy-seen.json` | key-value | Signed copy of the org policy | A copy of admin config, in the data root | `Go/internal/agentsd/policy.go:35-36` | #6751 |
| `<root>/project-tools.json` | key-value | Project tools from the control plane | A copy, in the data root | `Go/internal/agentsd/store/locations.go:113` | #6752 |
| `<root>/host-user.json` | key-value | The machine's user from the control plane | A copy, in the data root | `Go/internal/agentsd/hostuser.go:29` | #6753 |
| `<root>/lock-pin.json` | key-value | The machine lock's trust pin | In the data root | `Go/internal/agentsd/lock.go:37` | #6754 |
| `v1/events/<day>/` (audit) | log | Audit events | 90 days by default; an admin sets 1 to 3,650; no count or size limit a day | `Go/internal/controlplane/audit/audit.go:31,241` | #6755 |
| `v1/people/` tombstones | document | A removed person who had a sign-in | 90 days, swept hourly; then a cutoff for each provider (`v1/signin/cutoffs/`) refuses an account nobody has (#6756) | `Go/internal/controlplane/signin_cutoff.go` | #6756 |
| `v1/clients/` | document | Paired clients | 16 KiB a record, 1 MiB in all; forgotten when its key expires, at most 90 days unused; tombstone 7 days | `Go/internal/controlplane/keylife.go:18,139,145-177` | #6757 |
| `v1/machines/` | document | Machines | 32 KiB a record, 1 MiB in all; removed when its key expires, at most 90 days unused, or by a person when linked to a workspace; tombstone 7 days | `Go/internal/controlplane/keylife.go`, `records/machinebounds.go` | #6758 |
| `v1/machine-names/<person>.json` | key-value | A person's machine-N counter and last provider options | 64 KiB a person; until the person is removed (no age before, on purpose: a number is never used twice) | `Go/internal/controlplane/providers/defaults.go:20-63`, `Go/internal/controlplane/sweep.go` (`sweepMachineNames`) | #6759 |
| `v1/machine-setup-times.json` | time series | Set-up durations | 90 days, 50 a step, 32 KiB; on write and hourly; no roll-up | `Go/internal/controlplane/machinesetuptimes.go` | #6760 |
| `v1/warm-pools/<id>.json` | document | Warm pools and their claims | No count limit on pools; no delete when a person goes; config outside .agents | `Go/internal/controlplane/warmpool.go:33,44-45,124` | #6761 |
| `v1/providers/` | document | Machine providers | No count limit; until removed; config outside .agents | `Go/internal/controlplane/providers/records.go:31` | #6762 |
| `v1/links/` | document | Channel identity links | No count limit; until unlinked or the person goes | `Go/internal/controlplane/links.go:38` | #6763 |
| `v1/governance/` approvals | document | Catalogue approvals | 1,000; until taken back or the person or group goes | `Go/internal/controlplane/governance.go:43` | #6764 |
| `v1/policy/versions/` | document | Old policy versions | 100, no age | `Go/internal/controlplane/policy.go:36-48` | #6765 |
| `v1/scim/tokens/` | secret | SCIM token hashes, revoked ones too | 20, no age, trimmed on write only | `Go/internal/controlplane/scim_tokens.go:30` | #6766 |
| `v1/read/tokens/` | secret | Read token hashes, revoked ones too | 50, no age, trimmed on write only | `Go/internal/controlplane/readapi.go:32-34,330` | #6767 |
| `v1/lineage/<host>.json` | graph | Lineage node cost | 5,000 nodes and 90 days, applied on write only: an idle machine's nodes pass 90 days | `Go/internal/controlplane/lineagecost.go:27-30,107-125` | #6768 |
| `Agents Control/store-before-*` (Mac) | blob | Whole old stores | The newest 2, of any size, no age | `Host/Sources/HostModel.swift:567,584` | #6769 |
| Keychain `control-key-before-*` (Mac) | secret | Old control keys | The newest 2, no age | `Host/Sources/ControlKey.swift:51,63-83` | #6770 |
| `v1/control.json` | key-value | Control plane settings | Org-wide config in the store | `Go/internal/controlplane/records/records.go:34` | #6771 |
| `v1/policy.json`, `v1/policy.sig` | key-value | The org policy | Org-wide config in the store | `Go/internal/controlplane/policy.go:36-48` | #6772 |
| `v1/signin.json` | key-value | Sign-in providers that are on | Org-wide config in the store | `Go/internal/controlplane/signin.go:33` | #6773 |
| `v1/signin/providers/` | key-value | Org OIDC providers | 5, until removed | `Go/internal/controlplane/signin_org.go:39` | #6774 |
| `v1/signin/domains/` | key-value | Domain claims | 20, until removed | `Go/internal/controlplane/signin_domains.go:27` | #6775 |
| `v1/org/routes/` | key-value | Org keys and gateways | 50, until removed | `Go/internal/controlplane/orgroutes.go:42,45` | #6776 |
| `v1/audit-sinks.json` | key-value | SIEM sinks | 4 | `Go/internal/controlplane/auditing_sinks.go:26` | #6777 |
| `v1/reports/policy.json` | key-value | Budgets | Org-wide config in the store | `Go/internal/controlplane/reports_scope.go:28` | #6778 |
| `v1/reports/views/` | key-value | Saved report views | 50 a person | `Go/internal/controlplane/reports_views.go:23` | #6779 |
| `v1/users/` | key-value | Settings and secrets that follow a person | 256 KiB, 200 secrets of 16 KiB | `Go/internal/controlplane/usersettings/usersettings.go:41-49` | #6780 |
| `Agents Control/web-off` (Mac) | key-value | The web page turned off | A marker file | `Go/cmd/agents-control/main.go:707-720` | #6781 |
| hosted `v1/invites/` | document | Invite links | No count limit; an unused or expired invite is kept for ever | `Go/internal/hosted/admin.go:61; accounts.go:320-340` | #6782 |
| hosted `v1/allow/` | key-value | The operator's allow list | None | `Go/internal/hosted/signup.go:66-74` | #6783 |
| hosted `v1/blocked/`, `v1/blocked/email/` | key-value | The operator's block lists | None | `Go/internal/hosted/signup.go:66-74; admin.go:83,191-204` | #6784 |
| hosted `v1/accounts/`, sign-in links, `v1/emails/`, `v1/usage/` | document | Accounts | 100 accounts; until Delete Account | `Go/internal/hosted/signup.go:45; accounts.go:140-146,302-317` | #6785 |
| hosted `v1/waitlist/`, `v1/waiting/` | document | The waitlist | 10,000; removed only on sign-up | `Go/internal/hosted/signup.go:55,360,413-415` | #6786 |
| Operator backups | blob | Each account's store | The newest 7; `--keep 0` keeps all; no age | `Go/internal/hosted/backup.go:58-83; Go/cmd/agents-hosted/admin.go:33-43` | #6787 |
| `control.log` (`~/Library/Logs/Agents/control.log` or `/data/control.log`) | log | Operational log lines | 4 MB and one older file | — | #6901 |
| Mac `<cache>/work-cache.sqlite` | cache | Work cache: forge items, bodies, comments, page answers | 200 MiB (Remote: 50 MiB), least recently drawn out first; 4 MiB a record; 90 days (#6788) | `Packages/AgentsKit/Sources/AgentsKitCore/Client/WorkCache.swift` (`Bounds`) | #6788 |
| Web IndexedDB `agents` › `work` | cache | Work cache: forge items, bodies, comments, page answers | 50 MiB, least recently drawn out first; 4 MiB a record; 90 days (#6976) | `Web/src/model/workCache.ts` (`webBounds`) | #6976 |
| Mac `<cache>/copy/` (local copy) | cache | Machines, folders, records, the ends of 20 chats | 200 MiB, 20 chats, 200 archived records a machine; 16 MiB a record; 90 days; a machine no longer listed is dropped (#6789) | `Packages/AgentsKit/Sources/AgentsKitCore/Client/LocalCopy.swift` | #6789 |
| Remote `Caches/copy/` (local copy) | cache | As the Mac's local copy | 50 MiB, 20 chats; 16 MiB a record; 90 days; a machine no longer listed is dropped (#6789) | `Packages/AgentsKit/Sources/AgentsKitCore/Client/LocalCopy.swift` | #6790 |
| Web IndexedDB `agents` › `copy` | cache | As the Mac's local copy, and Work's pictures | 50 MiB, 20 chats, 2000 pictures; 16 MiB a record; 90 days; a machine no longer listed is dropped (#6792) | `Web/src/model/localCopy.ts` | #6792 |
| Web `localStorage` `agents.drafts` | document | Drafts | 40, a new one over that refused; attachments under 256 KiB; 30 days since the last edit (#6793) | `Web/src/model/drafts.ts` | #6793 |
| Mac and Remote defaults `draft.*` | document | Drafts | 40, a new one over that refused; 1 MiB a draft, 512 KiB of it attachments; 30 days since the last edit (#6794) | `Packages/AgentsKit/Sources/AgentsKitCore/Model/DraftStore.swift` | #6794 |
| Remote app group `shared-inbox.json` | blob | What was shared to Agent Loops | Each attachment capped; no count, total or age | `Packages/AgentsKit/Sources/AgentsKitCore/Widget/SharedInbox.swift:88,105-118` | #6795 |
| Mac browser pane `WKWebsiteDataStore.default()` | blob | Cookies and site storage of every site the pane opened | None: only the caches are emptied, at launch; not on Forget | `App/Sources/Sidebar/BrowserPane.swift:171-195` | #6796 |
| `machinePicker.pinned` (Mac, Remote, web) | key-value | Pinned machines | 100 of 64 bytes each, one over that refused; until removed, or its machine is no longer listed (no age, on purpose: a person's choice) | `Packages/AgentsKit/Sources/AgentsKitCore/Sidebar/MachinesGroup.swift` (`MachinePins`); `Web/src/model/machinePicker.ts` | #6797 |
| Remote defaults `remote.startFolders` | key-value | Start folders | None; not pruned, not cleared on Forget | `Remote/Sources/RemoteModel.swift` | #6798 |
| Mac and Remote `App Support/Agents/control-client.json` | key-value | The pairing with a control plane | Config outside .agents | `App/Sources/Control/ControlConfig.swift:40; Remote/Sources/Link/ControlPlaneLink.swift:20` | #6800 |
| Mac MDM profile `ControlPlaneAddress`, `ControlPlaneKey`, `MustJoin` | key-value | An org's pin to its control plane | Read only, set by the org | `Packages/AgentsKit/Sources/AgentsKitCore/Control/MachinePin.swift:15-17` | #6801 |
| Mac and Remote defaults `voiceMode.*` | key-value | Voice mode settings | Only in the device's defaults | `Shared/UI/Voice (voiceMode keys)` | #6802 |
| `catalog.personas.marketplace` (Mac, Remote, web) | key-value | The personas marketplace | Only in the device's defaults; the skills and MCP marketplaces are mirrored | `DisplayPreference.swift:50-59` | #6803 |
| `workingMarkTilt`, `waitingMarkPeriod` (Mac, Remote, web) | key-value | How the working and waiting marks move | Only in the device's defaults | `WindowDefaults; Web localStorage` | #6804 |
| Web `localStorage` `agents.startForm` | key-value | The start form | Not cleared on Forget, though the Mac clears its start form | `Web/src (agents.startForm)` | #6805 |
| Web `localStorage` `agents.reviewDiffLayout` and `reviewDiffLayout` | key-value | The diff layout, under two keys | Two keys for one choice | `Web/src (reviewDiffLayout)` | #6806 |
| `loops` `~/.config/loops/hosts.json`, `lock-keys/` | key-value | Pairings, the default host, lock pins | One entry a control plane; outside .agents | `Go/cmd/loops/hosts.go:16-45; lockkey.go:65-75` | #6807 |
| `agentsctl` `~/.agents-go/ctl.json` | key-value | One pairing | 1; no unpair, so it stays for ever; outside .agents | `Go/cmd/agentsctl/main.go:29-35,148` | #6808 |

Two more stores have a bug instead of a store issue: the bus log `v1/bus/` (log; 100,000
envelopes and 1 GiB a person, 90 days; #6671) and `v1/release-keys/` (secret; deleted with the machine and
swept hourly; #6672, closed).

### Stores found for #6684

These had no row above. Paths in the evidence are relative to `Go/internal/`.

| Store | Kind | Holds | Bound | Evidence |
|---|---|---|---|---|
| `<root>/ci-timings/<forge>/<path>/<day>.jsonl` | time series | CI runs, jobs and steps | 90 days, 20,000 runs a repository, steps of other branches 14 days | `citimings/store.go:33` |
| `<root>/revisions.json` | log | Revisions and removed records | 2,000 removals, 7 days | `agentsd/store/revisions.go:27` |
| `<cache>/archived-summaries.json` | cache | Index of archived agents | life | `agentsd/store/locations.go:119` |
| `<root>/alive.json`, `background.json` | key-value | Last run; background processes over a stop | 1 of each | `agentsd/restarts.go:77`, `agentsd/backgroundended.go:159` |
| `<root>/catalog-pending.json` | document | An install in progress | 1, removed at recovery | `agentsd/skillcatalog/installer.go:230` |
| `<root>/secret-ops.json` | key-value | Last operation of each secret | life (its secret) | `agentsd/secretops.go:102` |
| `<root>/github-bot-copies.json` | key-value | Who has the bot's key | lock's machines; 16 forgotten Apps | `agentsd/githubbotcopy.go:42` |
| `<root>/control-clients.json` | key-value | Copy of the paired clients | replaced by each list | `agentsd/localpath.go:45` |
| `<root>/policy-yours-seen.json`, `lock.json`, `suspended.json` | key-value | Small state | fixed shape | `agentsd/policyyours.go:33`, `agentsd/lock.go:38`, `agentsd/usersuspend.go:24` |
| `<root>/setup-reports.jsonl` | log | Steps of the machine's set-up | one run; a new run truncates it | `hostsetup/report.go:23` |
| `v1/machine-setup/<host>.json` | document | A machine's set-up steps | 1 per machine; deleted when the machine is removed (best effort, `controlplane/methods.go`); no age, no sweep | `controlplane/machinesetup.go:24` |
| `v1/machine-versions/<host>.json` | log | A machine's versions | 20 deploys; deleted when the machine is removed (best effort, `controlplane/methods.go`); no age, no sweep | `controlplane/machineversions.go:28` |
| `v1/approvals/<person>/` | document | Signed approvals | 2,000 a person; no age, no delete found | `controlplane/approvals.go:22` |
| `v1/failed-makes/<provider>.json` | log | Failed makes | 5 a provider; deleted with the provider; no age | `controlplane/providers/defaults.go:57` |
| `v1/maturity/<id>.json` | document | Feature maturity evidence | 84 records of 4 KiB; no age | `controlplane/maturity/store.go:161` |

Mixed files follow these decisions: `workflows.json` splits into document/configuration, key-value
run state, and log history; `lineage.jsonl` stays an append log with a separate bounded graph
projection; a Keychain connection item keeps credentials in the secret store and non-secret
metadata in a document. Only time-series data rolls up. Audit, conversations, logs, and event
files expire without a summary. The `Forever` setting and a live conversation do not extend the
90-day maximum age.

Numeric product bounds still missing from responsible child issues include
control-plane entity, provider, warm-pool, link, release-key, and invite quotas; browser and
shared-inbox quotas; and store-specific record and collection limits where the rows above say
**not specified**. The epic lead must track these in the existing responsible child issues or
file a follow-up child issue when no owner exists. Do not replace this statement with estimates.
