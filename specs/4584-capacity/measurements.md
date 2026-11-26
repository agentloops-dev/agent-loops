# Capacity gates: before and after (#4762)

Part of epic #4584. It follows #4583, which set the lines (near above 2.0 load per core,
full above 3.0) but could not measure the gates.

## Question

Did the lease gate (#4579) and the start gate (#4580) make this Mac do more work?

## Windows

Both gates merged on 2026-11-12: #4580 at 00:46 UTC, #4579 at 01:20 UTC.

| Window | From (UTC) | To (UTC) |
|---|---|---|
| Before | 2026-11-09 00:46 | 2026-11-12 00:46 |
| After | 2026-11-12 01:20 | 2026-11-15 01:20 |

## Data

All of it is this Mac's own records, read only. No setting changed.

- **Throughput:** the cost ledger, `~/Library/Application Support/Agents/lineage-cost.json`.
  A merge is a PR node with outcome `merged` or `reverted`, an `endedAt` in the window and a
  session, as `lineage.Merges` counts it for the Multiplier. GitHub's merged PRs are a check.
- **Build and test times:** the Claude Code transcripts (`~/.claude/projects/**/*.jsonl`), as
  in #4583: each foreground Bash call that runs `xcodebuild`, `swift test` or `swift build`,
  `go test` (or `go.sh test`), or a web build or test, from its call to its result. Calls under
  1 s or over 4 h, and calls in the background, are left out. Codex transcripts have another
  format and are not in it.
- **Agents at once:** the transcripts again: a session counts in a minute when it wrote an
  assistant message in that minute. Leads and Alex's own sessions count too.
- **Holds:** the tool results in the transcripts that say "near capacity" or "at capacity".
- `events.jsonl` starts on 2026-11-24, and `leases.json` keeps no history, so neither has the
  windows.

The scripts were one-off and are not kept.

## Throughput

| | Before | After | Change |
|---|---|---|---|
| PRs merged (ledger, the Multiplier's count) | 255 | 328 | +29% |
| PRs merged (GitHub) | 269 | 387 | +44% |
| Reverts | 0 | 0 | |
| Agent cost of the merged PRs | $490 | $530 | +8% |
| Cost per merged PR | $1.92 | $1.62 | −16% |
| Median time from a PR's start to its merge | 23 min | 27 min | +4 min |
| Failed checks per merged PR (mean) | 0.41 | 0.57 | |

The Multiplier is merged PRs against a fixed baseline, so it rose by the same 29%.

## Build and test times

Median and p90 run time, with the number of runs. "Hours" is the sum of the run times.

| Kind | Before: median / p90 (n) | After: median / p90 (n) | Hours before → after |
|---|---|---|---|
| xcodebuild | 69 s / 373 s (314) | 70 s / 188 s (349) | 11.7 → 8.5 |
| swift test | 38 s / 223 s (205) | 43 s / 131 s (251) | 4.9 → 4.1 |
| Go tests | 20 s / 224 s (526) | 12 s / 90 s (531) | 11.7 → 6.2 |
| Web | 12 s / 310 s (118) | 10 s / 36 s (169) | 2.5 → 1.4 |
| All | (1,163) | (1,300) | 30.8 → 20.2 |

Share of the time with 0, 1, 2 or 3+ builds running at once:

| | 0 | 1 | 2 | 3+ |
|---|---|---|---|---|
| Before | 68.5% | 22.5% | 7.0% | 2.1% |
| After | 74.8% | 22.5% | 2.7% | under 0.1% |

The median did not change, but the slow tail did: p90 fell by 40% to 90%. Before, 2.1% of
the time had three or more builds at once, and a build that overlapped three others took
5× to 45× its median (xcodebuild 381 s, web 538 s). After, three or more at once almost never
happened. There were 12% more runs, in 34% less build time.

## Agents at once

| | Before | After |
|---|---|---|
| Minutes with any agent in a turn | 71% | 77% |
| Mean agents in a turn | 1.91 | 2.28 (+19%) |
| Median, when any | 2 | 3 |
| p90, when any | 5 | 6 |
| Minutes with more than 5 | 5.1% | 8.1% |
| Most at once | 14 | 13 |

## What the gates held

In the after window, agents saw 97 lease results that held them ("near capacity" or "at
capacity", 1st in line and so on), 7 that said the line was being served again, and 56
`start_session` results that queued a helper for the machine. These are tool results, not
separate waits: one agent that asks again counts again. The before window had none; its 10
lines were issue text about the gates. How long each hold waited is not in any record.

## Confounders

The after window is not the before window with only the gates added:

- **#4697** (2026-11-11 23:39 UTC, in the before window's last hour): a waiting session holds
  no running place, so more helpers could run.
- **#5129** (2026-11-13 04:45 UTC): `helpers.mostNotArchived` 7 → 10.
- **#5502** (2026-11-14 19:10 UTC): `helpers.mostRunning` 5 → 10, for the last 6 hours.
  Without those 6 hours the after window still had 2.32 agents at once.
- The work was not the same: different issues, a different mix of Swift and Go.

So the throughput gain is not all the gates'. What the gates clearly did is the build tail:
more agents ran at once, but fewer builds overlapped, and the slow builds became fast.

## Decision: helpers above the running limit while the machine is ok

The epic decided that the start queue may start helpers above the fixed running limit while
the machine is `ok` (`startQueuesAfterLook`: one helper a look, up to the total), and asked
this spike to measure it.

Decided: the start queue keeps starting helpers above the fixed running limit while the
machine is `ok`, because in the after window more agents ran at once (+19%, more than 5 for
8.1% of the time against 5.1%), PRs merged went up 29% at 16% less cost each, and the build
tail got shorter, not longer: the load gate kept builds from piling up. #4377 can make it a
policy rule. Alex can overrule.

No default changes, so there is no PR for one.

## Linux and PSI (step 4)

This Mac has no Docker or Colima any more (Alex, 2026-11-12), so the `agents-devbox`
container is gone, and no Linux machine was loaded.

What the code and its tests show (PR #7297, #6613, merged 2026-11-25):

- **A host** on Linux reads `/proc/pressure/memory` (`psiPressure`, #4578): `full avg10` of
  10 or more is `critical`, which gives full; `some avg10` of 10 or more is `warn`, which gives
  no level, as on the Mac. Its level is otherwise the load, as on the Mac (2.0 and 3.0 per core).
- **A machine with the control plane on it** (`AGENTS_CONTROL_HERE`) keeps one core's load free
  and takes the higher of the host's level and the control plane's. The control plane reads
  PSI `full avg60`: disk above 10 is near and above 25 is full; memory above 5 is near and above
  15 is full (`capacity.ControlTarget`).
- **Tests:** `TestReadPSIFullAvg60` and `TestControlTarget` (`Go/internal/capacity`) and
  `TestPSIPressure` (`Go/internal/agentsd`) give PSI text and check the level at each line.
  They prove the parse and the lines, not that real load gives those numbers.

What was not checked: whether PSI on a real Linux host under builds gives the level that its
load gives, and whether its lines (5, 10, 15, 25) are right. No record has PSI of a loaded
machine. PR #7297 says its units were not run on a Linux machine either. The answer to step 4
is therefore open: #7457 measures it on a Linux machine that runs builds.
