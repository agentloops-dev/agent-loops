---
diataxis: explanation
devices: [mac, iphone, ipad, web, server]
description: "Why no data store of Agent Loops grows without bound: the rules every store follows, the kinds of store, where each limit is enforced, and how the limits are kept true."
---

# How data stores stay bounded

**The goal: no data store grows without bound.** Every store has a worst case that is
known before it happens, so the disk, the memory and the time to read a store stay
predictable on every machine, client and control plane.

Each store is a file today. It must be possible to change a store to a real database
later, without changing the code that uses it.

This page says the rules, and the design that follows from them. Epic #6669 brings each
store that breaks a rule into line, with one issue for each store. For each store's limits
today, see [Data and retention](compliance/data-and-retention.md).

## The rules

| Rule | What it says |
| --- | --- |
| **Size** | Every record has a largest size, and every store has a limit on its count of records or on its total size. A count is a size limit only when each record has a largest size too. |
| **Time** | A record ages out at 90 days at most. A shorter time is fine. Current state (an approval in force, a feature's rating, configuration) has no age: it stays until it is removed, and only its size is bounded. One exception: the hosted service's accounts, with their sign-in links (`v1/github/`, `v1/apple/`, `v1/google/`), `v1/emails/` and `v1/usage/`, stay until Delete Account, at most 100 accounts, until epic [#7092](https://github.com/agentloops-dev/agent-loops/issues/7092) (Alex, 2026-11-24). |
| **Config** | Configuration is in a `.agents` folder: `<repo>/.agents/` for a folder, or `~/.agents/` for a person. A control plane keeps its configuration in its own store, which is the system of record for it (Alex, 2026-11-24). A device's own pairing is not configuration: it is the identity of one device, so it stays in the app's own folder, out of backups and dotfile syncs (#6800). The pairings of `loops` and `agentsctl` are the same: they hold a private key, so they stay out of `~/.agents`, in the program's own folder with mode 0600 (#6807, #6808). A Mac's pin from its organisation stays in the MDM profile, which macOS delivers: the app only reads it, and a file that the person can edit must not replace it (#6801). The descriptions of a machine's own secrets stay beside the secrets, in `~/.agents-secrets`: people sync and commit `~/.agents`, and the file names the secrets of one machine (#6742). |
| **Roll-up** | Only time series roll up: old detail becomes a summary (usage keeps hours for 7 days, then days for 90 days). The summary is a store too, so the size and time rules apply to it. Nothing else keeps a summary of data that has aged out. |
| **Kind** | Each store has a declared kind, and the code reaches it through an interface of that kind. A database of that kind can then replace the file. |

## Kinds of store

| Kind | What it holds | Today | Library or backend |
| --- | --- | --- | --- |
| **Small file** | A few entries, read and written whole: settings, approvals, pins, small state | About 75 whole JSON files on each machine, and about 7 records on the control plane | Our own typed file, with its limits declared (below) |
| **Document** | One record for each ID | A folder of files, or one object a record on the control plane | The control plane's `store.Store`; `gocloud.dev/docstore` as a later database backend |
| **Log** | Records appended in order, read by time | JSONL files, and objects by day on the control plane | Our own interface; NATS JetStream fits (age, count and size limits on each stream) |
| **Time series** | Totals by period, rolled up | JSON by date | Our own interface; a time-series database later |
| **Graph** | Nodes and links | `lineage.jsonl`, and the control plane's lineage graph | Our own interface |
| **Blob** | Large files, opaque | Backups, attachments, the dropbox, worktrees | `gocloud.dev/blob` (a folder, S3, GCS, Azure), shared with the Artifacts store (#5713) |
| **Cache** | Data that can be deleted and filled again | Local copies, the Work cache, turn summaries | Each client's own (SQLite, IndexedDB, files) |
| **Secret** | Keys, tokens and secret values | `~/.agents-secrets`, the Keychain | `gocloud.dev/secrets`, which the control plane uses already |

A store holds one kind. A store that mixes kinds is split first: `workflows.json` held
states and runs (the runs are in `workflow-runs.json` now), `lineage.jsonl` holds links and changes of state, and a Keychain
connection item holds a token and settings that are not secret.

### The small-file type

Most stores are one small JSON file. One typed file covers them all, with no database and
no library. Each declares, once:

- the most entries, and the largest entry;
- the most age of an entry;
- whether it evicts or refuses (below).

It writes the file whole and atomically, as the stores do today. So most stores become a
declaration of a few lines. In the host daemon it is `store.JSONFile`, which `smallFile` opens
and adds to the hourly sweep (`Go/internal/agentsd/smallstores.go`).
A file of secrets is `store.SecretFile`: it has no age, refuses, and never evicts.

## Where each limit is enforced

**Lean into the backend.** The people who built each backend made its limits work well,
so we use them and do not build them again. Our code does only what the backend does not
do.

| Limit | Enforced by |
| --- | --- |
| **Age** | The backend, where it can: S3, GCS and Azure lifecycle rules; DynamoDB, Firestore and Cosmos DB TTL; MongoDB TTL indexes; NATS JetStream `MaxAge`. Otherwise our sweep: a folder, `fileblob`, `memdocstore`, the small-file type and the clients' stores have no TTL |
| **Count and total size** | The backend, where it can: NATS JetStream `MaxMsgs` and `MaxBytes`, MongoDB capped collections, MinIO bucket quotas, SQLite `max_page_count`. Otherwise our sweep. Most backends have no count or size limit |
| **Largest record** | Our code, when the record is written |
| **Roll-up** | Our code. Only time-series databases roll up by themselves |

A backend applies its limits late: S3 lifecycle rules run about once a day, and a TTL
delete can come hours after a record expires. We accept that delay, as for our own sweep.

### Evict or refuse

Each store declares what happens when it is over its limit:

- **Evict:** the oldest record goes, or the least recently used for a cache. This is for
  history, logs, caches and data made from other data: events, lineage, local copies, the
  Work cache.
- **Refuse:** a write over the limit fails, with a message to the person. This is for data
  a person made: the dropbox, secrets, drafts, kept requests, backups. A person's data is
  never deleted to make room.

### Eviction is in the background

A store may be over its count or size limit for a while. A sweep evicts later, as a
backend's TTL does. So a write does not check the limits of its store. The sweep runs
every hour. A write that takes a store past a high-water mark, such as twice its limit,
asks the sweep to run soon, so that a burst of writes cannot make a store much larger
between sweeps.

Two checks stay at the write, because they are cheap and a sweep cannot correct them
later:

- **The largest record.** A write of a record over its largest size is refused, or cut to
  size. One very large record cannot be evicted down to size.
- **Stores that refuse.** They check their limit before the write, because a person's data
  cannot be evicted after the write.

### Stores that remember what they have seen

Some stores exist only to refuse something again: the agents a machine deleted (so that a
restore does not bring one back), the signed requests already seen (so that a request
cannot be sent again), and a removed person's sign-in. These do not keep each entry for
ever. Each keeps a cutoff time and refuses anything older than it: a backup made before
the cutoff, a request signed before it. Then the entries can age out at 90 days, as every
other store does.

## Keeping the limits true

- **Declare.** Each store declares its kind, its largest record, its count, its size, its
  age, its roll-up, and evict or refuse, in one place. A check fails a store that does not
  (#6683).
- **Test.** One shared test for each kind writes past every limit, and checks that the
  sweep brings the store back under it, and that a store that refuses does refuse.
- **See.** Each store reports its count, its bytes and the age of its oldest record to its
  machine's health, so that a store near its limit shows before it evicts or refuses.

## Related

- [Data and retention](compliance/data-and-retention.md): each store's limits today.
- [Architecture](architecture.md#where-the-data-is-kept): where each component keeps its
  data.
- [Direction](direction.md): where data lives, and its system of record.
