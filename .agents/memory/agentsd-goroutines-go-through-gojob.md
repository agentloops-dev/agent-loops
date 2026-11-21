---
name: agentsd-goroutines-go-through-gojob
description: In agentsd, start every goroutine with d.goJob and wrap every timer callback with d.jobFunc; tests swap a package variable with setFor, never a deferred `v = was`
metadata:
  type: project
---

Since #5653, a daemon's Shutdown waits for every goroutine started with `d.goJob` (or `d.jobFunc` for a `time.AfterFunc` callback, `d.goLoop` for a loop) and ends `d.ctx`. `startWith`'s cleanup fails a test whose daemon still runs one, with the file and line where it started.

**Why:** a bare `safe.Go` or `go` in a daemon method outlived Shutdown, wrote into a test's root while `t.TempDir` removed it ("directory not empty"), and read package variables that the next test changed (races). Eight PRs fixed one goroutine at a time before this.

**How to apply:**
- In a `*Daemon` (or `*live`, via `l.d`) method, use `d.goJob("what", fn)`, not `safe.Go` or `go`. A goroutine that runs until the host stops waits on `d.ctx.Done()` (`d.hostCtx()` when the Daemon may be a hand-made one in a test). Leave alone fan-outs that the caller joins with a WaitGroup.
- Never call Shutdown or `d.Quit` from inside a job: Shutdown waits for it.
- In a test, change a package variable with `setFor(t, &v, x)` (testhooks_test.go): it puts the old value back only after the test's daemons are down. A `defer func() { v = was }()` runs before startWith's Shutdown cleanup.
- A test failure "goroutines the daemon started still run after Shutdown" names the start site: make that goroutine end with `d.ctx`.
- Since #6130, `TestEveryDaemonGoroutineIsCounted` (goroutines_test.go) fails on a new bare `go` or `safe.Go` in agentsd that `goroutinesNotCounted` does not list. List one there only with the reason Shutdown need not wait for it (joined, or ends when a value it alone owns closes). For work that must still be done after Shutdown (closing a screencast), use `d.goJobOrNow`.
- TestMain fails the run with goleak when any goroutine outlives every test, by its "created by" site. In a test, drain a conn with `c.drain()`, never a bare `for range c.out` (nothing closes c.out), and open a channel with `channelTo(t, d)`, which closes it at cleanup.
- A broadcast goes through `c.writeUnasked` (Broadcast, sendEach): once Shutdown has begun it drops the line, also one already waiting for the client's write lock (#6268). The test window conn from `start` is never read, and each line past its 64 waits 5 s; the writes in line behind one went on one at a time and outlived Shutdown.
