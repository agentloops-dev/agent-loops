# Store kinds and bounds — gate 1 proposal

Status: decision for [#6670](https://github.com/agentloops-dev/agent-loops/issues/6670), part of [#6669](https://github.com/agentloops-dev/agent-loops/issues/6669). Gate 1 and gate 2 are **not approved**.

## Decisions and scope

Decided: sweep local folder, `fileblob`, and `memdocstore` backends that have no native TTL, because Alex chose a small local sweep on 2026-11-23. Keep the default plain-file store. A hosted backend uses its own TTL or lifecycle mechanism; reads accept its expiry delay. Do not add a second expiry layer over S3 lifecycle, DynamoDB, Firestore, Cosmos, or MongoDB TTL.

Alex's [#6670 comments](https://github.com/agentloops-dev/agent-loops/issues/6670) also decide: only time series roll up; audit, conversations, logs, and event files expire without a summary. A record that only says “seen” retains a cutoff and rejects events older than it. Evict history, logs, caches, and derived data asynchronously; refuse an over-limit write to a person's data. Check every record's byte limit at write time. The hourly sweep and a high-water trigger enforce evicting collection limits. A refusing collection checks its limits before writing. Each store reports count, bytes, and oldest age to machine health.

This model applies to the host daemon and control plane. It keeps data on the customer's systems where possible, isolates each organisation, and puts configuration under a project or home `.agents` directory, in line with [Direction](../../docs/explanation/direction.md). The control plane must not acquire customer credentials through a store migration.

## Declaration at construction

Every store constructor takes one immutable `Spec` beside its path, bucket prefix, or collection name. A call site cannot obtain a writable store without a complete spec. The spec is the input to the lint check in #6683 and to one shared conformance test per kind. The numbers below are a *shape*, not invented limits for individual stores; each migration must set its own values from the current access pattern and product limit.

```go
type Bounds struct {
    MaxRecordBytes int64         // required, also with MaxCount
    MaxCount       int64         // zero only when MaxBytes is set
    MaxBytes       int64         // zero only when MaxCount is set
    MaxAge         time.Duration // at most 90 days for retained records
    Action         Action        // EvictOldest, EvictLRU, or Refuse
    Rollup         *Rollup       // time series only; its output has bounds too
}
type Spec struct {
    Name, Kind, Scope string // stable inventory id, kind, owner or organisation
    Bounds            Bounds
}
```

`OpenDocument(spec, backend)`, `OpenLog(spec, backend)`, and the other constructors validate the kind, all limits, action, and backend capability. A durable configuration store has its source of truth in `.agents`; its small local index still gets bounds. For a refusal, return a typed limit error with the current use, limit, and action a person can take. For eviction, writes may exceed collection limits until the hourly sweep; crossing a declared high-water mark (initially 2× the bound) requests an early sweep. Per-record limits apply before writing. A sweep measures actual bytes and count, deletes oldest complete records until both limits hold, and never deletes a record under a retention or legal hold. Backend TTL handles age where available; the local sweep handles age otherwise. The age clock is the record's creation or event time, not last read. Keep an expiry cutoff so an old “seen” event cannot re-enter after its tombstone expires.

## Kind interfaces

The interface is the library's interface where it fits. A thin bound wrapper checks the spec and reports health. Do not reimplement the library's protocol. Types below state required operations and semantics; exact Go signatures are settled in the implementation issue. All list and scan calls are paged and have an explicit maximum result size. Every write is scoped to one organisation or local owner.

| Kind | Contract and backend | Typical access |
|---|---|---|
| Document | `gocloud.dev/docstore` collection (`Get`, conditional `Put`, `Delete`, paged query) plus a bound wrapper; one typed whole JSON file uses the small-file adapter below. | Read or change a record by id; query a bounded set by indexed fields. |
| Log | `Append(eventID, time, bytes)`, `Read(afterCursor, limit)`, `Trim(before)`; append id is idempotent. The local adapter uses ordered segments; a native stream may enforce its own size and age limits. | Audit and event history in append order. No roll-up. |
| Time series | `Append(series, eventID, time, value)`, `Range(series, from, to, granularity, limit)`, `Rollup(before, interval)`. A point has an id for retry safety. | Hourly detail, then daily aggregates until expiry (usage: 168 hours of hourly detail, days to 90 days). |
| Graph | `PutNode`, `PutEdge`, `Edges(node, direction, cursor, limit)`, `DeleteNode`; edges name endpoints and a relation, with conditional version for changes. | Lineage and provenance traversal. Bound node and edge collections separately. |
| Blob | Use the **one** content-addressed blob contract from [#5713](https://github.com/agentloops-dev/agent-loops/issues/5713): `sha256` id; put, get, head, list, delete; folder first and S3-compatible bucket second. `gocloud.dev/blob` supplies backend I/O below that contract. Blob metadata and artifact versions stay separate. | Bounded binary content, attachments, exports, backups. Respect retention and legal hold before delete. |
| Key-value | `Get(key)`, conditional `Put(key, value, version)`, `Delete(key, version)`, paged `Keys(prefix)`; use the current `store.Store` conditions where shared copies need them. | Current state and small indexes. |
| Cache | `Get`, `Put(key, value, expiry)`, `Delete`, `Clear`; a miss is valid. LRU and TTL enforce declared bytes and age; rebuild from a named source. | Derived local copy and work cache. |
| Secret | `gocloud.dev/secrets` keeper for encrypt/decrypt, with platform Keychain or KMS as key owner; the bound wrapper limits ciphertext records and lifetime. Do not expose a generic list of plaintext. | Keys, credentials, and sealed connection material. |

### The first host type: one small JSON file

About 75 host stores and seven control-plane records are whole JSON files. Start with `JSONFile[T]`: `Read(ctx) (T, version, error)` and `Update(ctx, expectedVersion, func(*T) error) (newVersion, error)`. The constructor declares `MaxRecordBytes`, `MaxCount` for contained entries, `MaxBytes`, `MaxAge` for entries with time, and evict/refuse. It uses the existing `diskjson` codec and atomic file replacement, preserves the current on-disk schema, and checks the version when writers can race. Its compacting sweep works on typed entries, not an unbounded raw JSON scan. This is the **document** kind for a singleton; an individual collection can later move to `docstore` without changing its caller's domain operations. A file that contains several kinds is split before it uses this type.

### Control-plane key-object migration

`Go/internal/controlplane/store.Store` is currently `Get`, conditional `Put`, `Delete`, and `List` of objects with ETags. Preserve it as the low-level adapter while a family moves. A log adapter stores immutable entries under an ordered, scoped prefix with an event id, timestamp, and cursor; conditional `Put(Absent)` makes retries idempotent. A time-series adapter stores points under a scoped series/time prefix and separate daily aggregate objects. `Range` pages within a time window; roll-up writes the aggregate conditionally before it removes expired detail. A replacement stream or time-series database implements the *kind interface*, not the key layout. Neither caller parses object keys or reads the full prefix to answer one page. Keep conditional mutation and the multi-copy start-up probe for any backend that still uses `store.Store`. A backend switch must test replay, concurrent append, roll-up retry, pagination, and backup/restore without a dual-write window.

## Store-family mapping and mixed files

This maps the families named by #6669 and its machine, control-plane, hosted, and client child issues. The owner-specific migration sets each numeric bound; no family is exempt from max record bytes plus count or bytes and an age of at most 90 days for retained records.

| Family | Kind and split | Bound action and main access |
|---|---|---|
| Host whole JSON records (about 75), control-plane singleton JSON (about seven) | Document / `JSONFile[T]`; configuration moves into `.agents`. | Refuse for person-owned settings and drafts; evict old derived/history entries. Read or update one file. |
| Sessions, conversations, transcripts, runtime homes, requests and kept requests | Document metadata + log transcript + blob payloads; runtime homes and worktrees need explicit per-owner byte budgets. | Refuse person-owned payloads; expire old history by policy, with no summary. Page by session/time. |
| Audit, bus envelopes, workflow events, notices and other event-like files | Log; keep a separate current-state document or key-value view where required. | Evict oldest after durable delivery/retention; audit copy goes to customer SIEM. Append then page by cursor. |
| Usage, build and machine timing, health and capacity samples | Time series; aggregates are separate bounded records. | Evict old detail after safe roll-up. Query one series and time range. |
| Lineage links, nodes and state changes | Graph for current nodes/edges; log for ordered changes. | Bound nodes and edges separately; page adjacency and change history. |
| Attachments, artifacts, machine backups, exports, binary request bodies | #5713 blob; metadata is document/key-value. | Refuse writes to person-owned blobs at quota; retention governs delete. Get by digest or page metadata. |
| Control-plane entities, people, providers, warm pools, links, invites, routes, policy, release keys and tombstones | Document or key-value by record access; `.agents` for configuration; secret for key material. | Refuse active person-owned state; expire spent links/invites and old tombstones. Get by scoped id, not global list. |
| Hosted account, sign-in and billing state | Document/key-value; secret for cryptographic material; log for events. | Refuse durable account state; expire transient codes and events. Scope every read by organisation. |
| Local copies, work cache, browser data, derived indexes | Cache. | LRU eviction and expiry, rebuild from source. |
| Keychain items, agent secrets, control key history | Secret; metadata is document/key-value. | Refuse active secrets at limit; age out superseded keys after their safe-use window. |

**`workflows.json`:** split workflow definitions and user settings (document/config in `.agents`), run state (key-value), and execution history (log). Keep stable ids and migrate atomically with a version marker; read the old file until the new parts verify, then retire it.

**`lineage.jsonl`:** keep the ordered append history as log; build bounded graph nodes, edges, and current state from it. Record a graph checkpoint cursor so replay is idempotent. Expire old log entries only after the graph checkpoint is durable; the graph has its own bounds and age. No log summary.

**Keychain connection item:** not split (Alex, 2026-11-24, #6743). The endpoint, account and site say where the token is sent and as whom, so they stay with it in the secret store; an older build reads them there too.

## Host daemon migration order

1. Inventory the 110 files with owner, writer, reader, path, record size, count, age, access query, and present retention. Set limits and evict/refuse at each constructor. Move configuration sources into `.agents` as their family migrates.
2. Add the bounded `JSONFile[T]` document type, codec compatibility, atomic writes, health measures, and local sweep. Move the roughly 75 single-kind whole JSON files in small groups, preserving their bytes and version behavior. Split `workflows.json` before moving its parts.
3. Move key-value current state and small indexes. Keep conditional writes where agents or copies race. Move secrets and the Keychain connection split next; validate no plaintext crosses the boundary.
4. Move caches and derived indexes; define their rebuild source, LRU/TTL, and byte budgets. Then move append logs, including audit, transcripts, bus, and the log half of `lineage.jsonl`; give each a cursor and retention rule.
5. Move time series after logs so append and sweep are proven; add the only roll-ups. Build the graph projection from lineage log with checkpointed replay, then move other graph stores.
6. Adopt #5713's blob store for binary families as that epic supplies it. Inventory references before moving bytes; enforce digest, per-blob size, owner quota, retention, and backup/restore. Do not make a second blob interface while #5713 is on hold.
7. For each family, switch one caller at a time, compare reads with existing data, exercise its shared limit test and health output, then remove its old direct file access. A kind is complete only when no caller bypasses its bound constructor.

## Gate 1 review inputs still needed

Before gate 1 approval, the implementation issues must give each store's numeric `MaxRecordBytes`, `MaxCount` or `MaxBytes`, `MaxAge`, action, high-water threshold, owner/scope, indexed access patterns, and backup/restore source. The unresolved product limits named in #6670's comments need Alex's values: transcript and runtime-home budgets; worktree budget; control-plane entity, provider, warm-pool, link, release-key and invite quotas; and browser/shared-inbox quotas. The issue's family inventory must confirm every one of the 110 host files and the mixed-file schema so no store falls through this mapping. This document defines the model and order; it does not assert either gate's approval.
