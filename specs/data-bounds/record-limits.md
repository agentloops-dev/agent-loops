# The largest record: results by kind (#6908)

Checked at `324a840a2` on 2026-11-24 (epic #6669). A **test** row ran; **read** means the code was
read and not run. A store below its total is no proof that a record was handled.

## Results by kind

| Kind | Where | At the limit and over it | Bulk | Migration | Replay | Result |
| --- | --- | --- | --- | --- | --- | --- |
| Small file (`store.JSONFile`) | machine | test: taken; refused with the limit in the words, the file unchanged | test: one over refuses all, nothing written | test: an old large entry does not stop others; `UpdateKeepingAll` keeps it (#7181) | test | The kind holds. Callers fail: #7186 |
| Other small files | machine | test: catalogue records, settings, `host-user.json`, lock; read: pins, approvals, folders, devices, policy copies, wake | read | read: `moveSetting` copies as it is, the next write checks | read | Holds (#7188, #7189, #7203 fixed) |
| Secret (`store.SecretFile`) | machine | test: refused, no name or value in the words, the earlier value intact | test | test (kind's own): a file over its limit opens and can shrink | test | Holds |
| Secret | control plane | read: checked before the seal; test: a token's use and revoke | read | read | read, each retry | Holds (#7203 fixed) |
| Log (`FolderLog`) | machine | test: refused; a transcript's entry becomes a notice that says so | test | test: an old large line is read | test: the retry is one record; held lines use the same write | #7183 (restore, import, move), #7187 |
| Log (`OpenLog`, `DayLog`) | control plane | test: refused, nothing written | one record a call | read | test | Holds; an audit entry is cut with `…` (its own test) |
| Log (`rolllog`) | machine, control plane | read: a write is split, not lost | read | none | none | Holds. Baseline logs: #7200, #7202 |
| Time series (`OpenTimeSeries`) | control plane | test: refused, nothing written | test: the roll-up | none | test | Holds (#7196 fixed; no caller yet) |
| Time series | machine | test: CI timings | test | read | read | #7200 |
| Graph | machine | test: a ref of 512 bytes, also after a repo's move | read | read | test: read again | Holds; a line is not declared (#7200) |
| Graph | control plane | test | test | test: an old indented object is written again by the sweep | read: the sweep | Holds (#7193 fixed) |
| Document | machine | test: requests | read | read | read | #7185, #7200 |
| Document, key-value | control plane | own tests for settings, policy, sinks, views, links, maturity, people, machines, clients; test here: governance, choices | read: approvals and bus check the whole batch first | read: renamed machine keys are copied as they are | read: checked in each conflict retry | #7201 (#7192, #7203 fixed) |
| Document, key-value | hosted | test | test: the sweep | test: an old invite | read: in each retry | Holds (#7197 fixed) |
| Blob | machine, control plane | read: attachment, dropbox, backup parts | read | read | none | Holds (#7203: a file's newest set-aside copy stays whatever its size, as it now says) |
| Backend | control plane | test: memory, folder; S3 fake, to 128 MiB, and a write over it refused (the one cloud store) | none | test: an object of 16 to 128 MiB is read; a larger one stays, and can be listed, replaced and deleted | none | Holds (#7195 fixed) |
| Cache | Mac, Remote, web | test: a page and a picture kept at the limit, not over it, in bytes; a stale one goes | test: a list's pages | test: the web's old large record is not read, and goes (#7253); read: the Mac's is read until it ages out (#7252) | read: an optimistic change over the limit is not made | Holds; an item over the limit goes, with its earlier copy (#7198) |
| Drafts, settings | clients | test: web drafts, and the web's `localStorage` (`keyValue`, #7253); read: Mac's drafts, inbox, pins, start folders | read | read | read | #7252 (#7199, #7253 fixed) |

A refused write tells its caller: `store.ErrLimit` or `LimitError` with the name, the size and the
limit. The stores that only log it have bugs above.

## The tests

`recordlimit_test.go` in `Go/internal/agentsd/store`, `agentsd/skillcatalog`, `controlplane`,
`controlplane/store`, `controlplane/providers`, `controlplane/s3store`, `hosted`, `lineage`,
`citimings` and `whatsappmcp`; `Go/internal/agentsd/recordlimits_test.go`; `WorkCacheTests`,
`LocalCopyTests`, and the web's `workCache`, `localCopy`, `drafts` and `keyValue` tests. A test that a bug
fails is skipped with the bug's number; the fix takes the skip out.

## The entries left in the baseline

What each line of `scripts/store-bounds-baseline.txt` is. A **store** is owned by the bug shown.

| Class | Entries |
| --- | --- |
| Not a store: temporary, staging or scratch | `ControlSettingsModel`, `ConnectLinearSheet`, `BackgroundPane`, `VoiceTape`; `agentsd/`: `brief`, `bringback`, `receive`, `send`, `backups` and `backups_store` staging (then #7183), `secretcmd`, `trailerhook`; `skillcatalog/`: `github`, `installer`, `preview`; `conflictrisk`, `hostinstall/`, `hosttunnels/session`, `releasefiles`, `linuxrelease` |
| Not a store: an installer, a service or a lock file | `LocalServices`; `cmd/agentsd/`: `install`, `main` (the lock), `supervise`; `agentsd/`: `install`, `selfupdate`, `gitcredentials`, `modelrelay`, `relay`, `runtimes`, `orglaunch`; `hostsetup/install` (its log: #7200), `controlplane/machine`, `mac/signin/certs`, `mac/signin/codex`, `whatsappmcp` lock, `controlplane/store/folder` lock |
| Not a store: the person's file or their project | `HostModel`, `AgentsExportTransfer`; `acp/session`; `agentsd/`: `agentconfigeditor`, `files`, `newapp`, `publicrepo`, `policyfolder`, `folders`, `guarded` (its undo), `dotagents`, `readymade`, `readymadeskills`, `settings` (a persona), `environment` (a folder's), `worktrees`, `host` (an artifact), `skillcatalog/personas`; `codespacessh`, `controlplane/homebackup` |
| Not a store: one fixed key or mark | `HostJoinFile`, `MachineLock`, `ControlMembership`, `keys.ts` (its caches are declared), `signIn.ts`; `cmd/agents-control/main`, `cmd/agentsd/enrol`, `cmd/agentsd/main`; `agentsd/`: `lock` (mark), `localpath` (below), `restarts`, `setupforward`, `setupprojects`, `usersuspend`, `runtimesignin`; `store/roots`, `store/store` (root id), `controllink/` (all), `kms/`, `workspacecommands/digitalocean`, `controlplane/capacity`, `hosted/limits` |
| Not a store: the helper or the backend itself | `store/files`, `execfile`, `store/folder`, `storeopen`, `storecopy`, `records/records` (an old prefix, read only), `homebackup` `NewFolder` |
| Declared at another line | `cmd/agentsd/main` and `supervise` (the daemon log), `hosttunnels/tunnels` and `sweep` (`v1/codes`, itself #7201), `lock` pin, `links` (the links) |
| Store, bounded in code, not declared | `citimings/store`, `store/revisions`, `secretops` (`secret-ops.json`), `providers/kept`, `controlplane/backups`, `joins`, `audit/chain` |
| **Store with no largest record: machine, #7200** | `agentsd/`: `attachments`, `attention`, `backgroundended`, `cost` (about 15 files), `dailychecks`, `environment`, `events`, `githubbotcopy`, `guarded`, `host`, `leases`, `localpath`, `lock`, `mcpcatalog/files`, `mcpevents`, `orgroutes`, `policyyours`, `said`, `turnscache`, `usersettings`, `viewservers`; `store/store` (`agent.json`), `store/summaries`; `skillcatalog/`: `lock`, `marketplaces`, `secrets`; `hostsetup/report`, `lineage`, `mac/signin/relays` |
| **Store with its own bug** | `whatsappmcp/store` #7191, `providers/secrets` #7201 |
| Store, with its largest record (#7248) | `bots` (`v1/runs`), `builds`, `copies`, `deploys`, `lineagegraph`, `mesh` (`v1/leases`, the heartbeats of `v1/copies`), `projects` (`v1/projectFolders`), `records/groups`, `records/projects`, `reports` (`v1/usage`), `sweep` (`v1/mesh/events`) |
| **Store with no largest record: control plane, #7201** | `codes`, `hosttunnels/tunnels`, `links` (requests), `lockjoins`, `locks`, `orgrefresh`, `people_scim`, `policy_ask`, `secretrelease`, `signin_approvals`, `signin_org`, `hosted/billing` |
| **Store with no largest record: clients, #7202** | `HostSettings`, `HangWatchdog`, `RollingLog`, `AttentionSnapshotStore` |
