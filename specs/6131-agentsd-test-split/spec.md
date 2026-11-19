# Spike: split the agentsd tests so that most do not start a full daemon

**Issue**: #6131 (epic #6126)

**Created**: 2026-11-17

**Status**: Decided

## Decision

Do not split the agentsd tests broadly. A full daemon is not the main cost of the tests:
its start, its stop and its background loops are about 15% of the tests' CPU. Two causes
cost more, and each is one task with no new test layer:

1. `processTable` reads each process on the machine with three `sysctl` calls. It is 21% of
   the test process's CPU, and its cost grows with the number of processes on the machine,
   so it grows with load. It is product code: the live daemon pays it too.
2. Each fake runtime is a new copy of the 100 MB `-race` test binary. 233 tests start one
   or more; they are 38% of the summed test time, and each start waits about 1.1 s on the
   Mac before the runtime answers.

Then measure `main` for 7 days with steps 1 to 3 of #6126 in, and split more only when
the figures say so.

## How it was measured

On this Mac (10 cores, 889 processes), at `42823072c`, in a scratch copy of
the repository with a log added to `newWith` and `Shutdown` (the stack gives the test's
name). Each run is `Go/scripts/go.sh test -race -count=1 -json ./internal/agentsd/`, one
package, no shards:

| Run | What changed | Wall (s) | User + sys CPU (s) | Result |
|---|---|--:|--:|---|
| 1 | nothing | 354 | 235 + 615 = 850 | pass |
| 2 | the log | 341 | 208 + 672 = 880 | pass |
| 3 | the log, and the daemon's 14 background loops off | 467 | 215 + 626 = 841 | 6 tests fail |
| 4 | the log and a CPU profile | 360 | 216 + 578 = 794 | pass |

Other lanes used the Mac at the same time (load up to 52), so compare CPU, not wall
time. The build, vet and link with no test run take 6 s of CPU, so almost all of the CPU is
the tests and the processes they start.

## 1. Which tests need a full daemon

The package has 1,387 tests in 340 files. 1,093 of them call `t.Parallel()`. In run 2,
**1,002 tests started 1,070 daemons** (42 tests start more than one). The classes come from
the log (does the test start a daemon?) and from the test's source (does it call the
daemon over its connection, and with how many method groups?):

| Class | Tests | Summed test time (s) | Share |
|---|--:|--:|--:|
| A: no daemon | 373 | 27 | 2% |
| B: a daemon, but it calls only Go methods on `*Daemon` (no RPC) | 487 | 467 | 32% |
| C: a daemon, RPC to one method group | 325 | 569 | 40% |
| D: a daemon, RPC to two or more method groups | 187 | 377 | 26% |

Only 29 of the 330 files have no test that starts a daemon. Every area mixes the classes.
These are the files with the most test time:

| File | Tests | A | B | C | D | Time (s) |
|---|--:|--:|--:|--:|--:|--:|
| `leadanswers_test.go` | 4 | 1 | 3 | 0 | 0 | 41 |
| `forge_test.go` | 14 | 4 | 0 | 8 | 2 | 33 |
| `brief_test.go` | 19 | 10 | 4 | 4 | 1 | 29 |
| `recovery_test.go` | 7 | 0 | 4 | 3 | 0 | 28 |
| `receive_test.go` | 4 | 0 | 0 | 4 | 0 | 26 |
| `forgestanding_test.go` | 6 | 2 | 2 | 0 | 2 | 25 |
| `forgeforgejo_test.go` | 10 | 1 | 2 | 7 | 0 | 25 |
| `turns_test.go` | 14 | 2 | 4 | 5 | 3 | 24 |
| `forgeazure_test.go` | 8 | 0 | 1 | 7 | 0 | 23 |
| `elicitations_test.go` | 6 | 1 | 1 | 2 | 2 | 21 |
| `policy_test.go` | 19 | 2 | 5 | 6 | 6 | 21 |
| `workspacetools_test.go` | 6 | 1 | 0 | 0 | 5 | 21 |
| `agents_test.go` | 10 | 0 | 0 | 9 | 1 | 21 |
| `turnundo_test.go` | 5 | 1 | 1 | 3 | 0 | 21 |
| `autoruntime_test.go` | 11 | 3 | 0 | 4 | 4 | 21 |

What the daemon costs a test:

- **Start** (`newWith`): 1,070 starts, 149 s wall in total, 0.14 s each; 25 s of CPU in the
  profile (3% of the test process's 509 s).
- **Stop** (`Shutdown`): 378 s wall in total, median 5 ms, 90th percentile 0.9 s; 68 s of
  CPU. Of that, 65 s is `handBackgroundOver` reading the process table (below).
- **Background loops**: with the 14 loops off (the workflow clock, folder watches, the hourly
  and minute timers, disk and capacity watches, view counts, reviews, forge requests,
  deploys, erasures), CPU fell by 39 s (5%), and 6 tests failed because they test a loop
  (`erasure_test.go` 3, `restarts_test.go`, `events_test.go`, `readymade_test.go`).

A test that needs no daemon would save its start and its loops, at most about 8% of the
CPU. The B and C tests still need most of what the daemon holds.

## 2. What a test can start without the daemon

**Now, with no seam:**

- The packages under `internal/agentsd/` (`store`, `acp`, `diskjson`, `redact`, `mcpcatalog`,
  `skillcatalog`, `yamlnode`, `browsertoolset`) have their own tests, and a test can open a
  `store.Store` on a `t.TempDir()`.
- A bare `&Daemon{}` with the fields the test sets: 15 tests in 5 files do this now. It works
  only for a method that reads no map, lock or loop that `newWith` makes.
- Pure functions (parsers, formats, tables): the 373 class A tests, 27 s in total. They are
  already cheap; moving them to their own package saves nothing measurable.

**Only with a seam first:**

- **A handler (class C) without the daemon.** The 2,517 methods on `*Daemon` share its 132
  fields and its locks, and `register` puts every handler on one `Daemon`. To test one
  method group alone, its state must move to a type of its own with a constructor (as
  `store` did). That is a refactor of each area, not a test change.
- **A daemon with no background loops** (a `startOptions` switch). It is easy (run 3 is
  the prototype), but it saves 5% of the CPU and must leave the loops on for the tests of a
  loop. Not worth a test layer.
- **A fake runtime in the test process.** Today `fakeRuntimes` points the runtime at the test
  binary itself (`AGENTS_FAKE_ACP=1`, `TestMain`). The seam is a start option that gives
  the daemon the runtime's stdin and stdout from a pipe to an in-process `fakeRuntime`,
  for a test that only talks ACP. The tests of processes (the warm pool, background
  processes, a stop by signal) keep the real process.

## 3. The plan

| Order | Task | Measured now | Expected saving | Size |
|---|---|---|---|---|
| 1 | #6142: `processTable` reads the whole table in one call (`kern.proc.all` on a Mac, `/proc` on Linux), not three calls for each process | 109 s of the test process's 509 s CPU (21%): `handBackgroundOver` 65 s, `descendants` 28 s, `warmPass` 16 s; 889 processes here | about 100 s of CPU under `-race`; less on a CI runner with fewer processes; the live daemon too | XS |
| 2 | #6143: the fake runtime runs in the test process for the tests that only talk ACP | 233 tests start a fake runtime, 547 s of 1,443 s summed test time (38%); each start of the 100 MB binary waits 1.1 s (0.08 s CPU) before it answers | most of the 1.1 s of each start, on the Mac first; fewer processes for the scheduler under load | XS |
| 3 | #6144: count the agentsd failures on `main` for 7 days with steps 1 to 3 in, and decide whether a wider split (seams per area) is needed | see 4 | a decision | XS |

The three can run at the same time. The sizes come from the rubric in
`.agents/personas/product-owner.md`: a daemon task with no client and no outside account
is 0 points.

Task 1 is not `linux-ok`: the Mac path (`sysctl`) is the one that costs, and only a Mac runs
its tests. Task 2 is `linux-ok` (Go only), but the 1.1 s start is measured on the Mac.

## 4. Is it worth it, after steps 1 to 3?

**Before steps 1 to 3** (2026-11-14 to 2026-11-17, CI on `main`): 197 runs ended (not
cancelled); 60 failed. A Go job (`go`, `test-go` or `go-darwin`) failed in 30 of them, and
an agentsd test failed in 20. 24 different agentsd tests failed, each only once: there is
no small set of bad tests. 23 of the 24 start a daemon; the other is
`TestTheBundlesNodeToolsetsPinOneVersion` (class A).

**After steps 1 to 3:** no figures yet. The three PRs merged at 23:16, 23:40 and 23:55 UTC
on 2026-11-17, and no CI run on `main` had ended after the last of them when this was
written. Task 3 gets these figures.

**So:** a broad split (move the B and C tests off the daemon) is not worth it now. It needs
a seam in each of about ten areas of a 196k-line package, and it saves at most about 8% of
the CPU. The failures are in daemon tests because 72% of the tests are daemon tests, and
because they wait on timers under load; steps 1 to 3 attack the waits, and tasks 1 and 2
cut the load (about a fifth of the CPU, and a 1 s wait for each fake runtime) for far less
work. When task 3 shows that agentsd tests still fail on `main` more than once a week, the
next step is a seam for the area that fails most, not a split of every area.
