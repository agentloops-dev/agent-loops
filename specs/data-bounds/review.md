# Performance and usability review of the bounded stores (#7240)

Stage 8 of epic #6669. Measured on 2026-11-25 at `b16677f88`, on an M-series Mac, on scratch roots
only. The live control plane is an e2-micro (#7052), which is slower: read the CPU times as a
lower bound there. Each fault is a bug, a child of #6669; none is fixed here.

## Performance

`TestStoreReviewAtRealisticCounts` in `Go/internal/controlplane/storereview_test.go` and
`Go/internal/agentsd/storereview_test.go` makes the data and times each step. Run them with
`AGENTS_RUN_PERF=1` (the commands are in each test).

**Control plane:** a folder store of 315,240 files, 205 MB: 120 days × 1,000 audit events (30 days
past the 90 kept), 20,000 bus envelopes, 10,000 mesh events, 5,000 runs, 10 machines with lineage
at its bounds, a 1 MiB host copy each (3,000 sessions in all), and their usage hours and days.

| Step | CPU | Allocation | Lists, keys listed |
| --- | --- | --- | --- |
| First hourly sweep after an update (39,000 events removed, 40 rounds) | 14.8 s | 3,649 MB | 876, 1,562,900 |
| Next hourly sweep (nothing to remove) | 1.2 s | 590 MB | 27, 27,600 |
| Size measurement, 71 stores | 0.13 s | 334 MB | 158, 118,620 |
| `reports/storeSizes` read | 0 | 0 | 0 |

- The steady sweep and the size measurement are not a cause of #7052: about 1 s of CPU an hour,
  and 0.13 s each 6 hours.
- **Fault, #7403:** the first sweep after an update lists the store again in each round. Its cost
  is rounds × keys, and its allocation is far above the live `GOMEMLIMIT` of 200 MiB.
  Fixed: a round skips the steps that have no deletions left, and the rounds of an hour do
  not run a step again once it has found all it could delete. At the same data (seeded as
  plain files, 157,650 with no lock files), the first sweep went from 876 lists, 1,562,900
  keys and 2,749 MB to 67 lists, 231,600 keys and 1,190 MB. Its CPU went from 22 s to 10 to
  18 s (this Mac was under load), most of it the deletion of the 39,000 files and the one
  list of every event.
- A read of Reports measures nothing, as declared.
- The `store-sizes.json` record is 8,078 bytes of its 128 KiB.

**Machine:** 3,000 sessions (300 live, 2,700 archived), 40 entries of about 1 KiB each. Each
tenth session has entries older than 90 days.

| Step | CPU | Allocation |
| --- | --- | --- |
| First hourly sweep | 0.88 s | 314 MB |
| Next hourly sweep | 0.70 s | 293 MB |
| `trimConversations` alone | 0.25 s | 223 MB |
| `measureStores` alone | 0.31 s | 31 MB |
| `stores/list`, a page of 100 | 1 ms | 0 |

- No fault. Each hour the trim reads every transcript (223 MB for 120 MB of transcripts). That
  grows with the bytes kept, which are bounded (4 MB a conversation, 90 days).

## Bounds over their limits

The tests of each kind put a store over its limit and pass at `b16677f88`: small file (evicts the
oldest, ages out, refuses), secret (refuses, opens over its limit), log (a trim keeps its indexes,
an entry over the limit becomes a notice; a day log evicts and asks for a sweep at twice its
limit), time series (a roll-up is kept, one that cannot roll up does not stop the others), blob
(set-asides age out), and the control plane's sweep (stops at its bound, catches up in the hour,
the bus and mesh events). The per-record results are in `record-limits.md` (#6908).

Only time series roll up. The lineage "roll-up" of #6863 merges sent changes into the links of the
same graph, so it keeps no summary of data that aged out.

## The bounds baseline

`scripts/store-bounds-baseline.txt` goes from 93 lines to 31. The other 62 lines were not stores,
and each opening has a `// store-bounds: not a store: <why>` note: installs, temporary files, locks
and launch files, a handoff, the person's own files, a restore that writes a backup's bytes as
they are, and the writes of a store's own interface. The 31 lines left are stores, each in a bug:

| Bug | Stores |
| --- | --- |
| #7348 | `daemon.log`: the detached child's output |
| #7349 | the ready-made catalogue's state files |
| #7350 | machine secret and one-record files, and `suggestions.json` (#7023) |
| #7351 | key and identity files of machines, clients and control planes |
| #7352 | CI timings, WhatsApp messages, release files |
| #7353 | the web page's IndexedDB `key` and `agents-signin` |

## Usability

Read from the code, not walked. What is right: the dropbox, drafts, questions, secrets of a
session, and the approvals of connectors say what happened and what to do. Faults:

| Bug | What |
| --- | --- |
| #7404 | A chat entry of 4 to 5 MB says it was 4 MB, and that 4 MB is the most |
| #7405 | The 90-day or size trim of a chat shows nothing |
| #7406 | A kept request that expires is not told to its sender |
| #7407 | Refusals with raw bytes or no next step (connectors, six control plane refusals, draft attachments, an old backup's restore) |
| #7408 | Reports ▸ Stores: "Over limit" does not say what follows; "Refresh" seems to measure |
