---
name: agentsd-stacks-on-sigusr1
description: kill -USR1 on agentsd (or its supervisor) logs every goroutine's stack to daemon.log, with no stop; the uplink logs any handler over 2 s
metadata:
  type: project
---

To see what a live agentsd waits on (a stalled uplink, a hung call), send it SIGUSR1:
`kill -USR1 <pid>` (the pid in the root's `daemon.lock`; the `supervise` parent passes it on).
It writes every goroutine's stack to `daemon.log` between `stacks: every goroutine, on SIGUSR1`
and `stacks: end`, and goes on running (#6260). Never SIGQUIT the live host: that stops it.

The uplink (`Go/internal/controllink`) logs `uplink: <method> took <time>` for a channel-0
notification, an ask from the control plane, or one line on its read loop over 2 s.

**Why:** #6223 and #6260: the Mac's link to the control plane dropped because a handler held
the read loop, and there was no way to see which without a restart that ended the stall.

**How to apply:** at the next stall, read the log's slow lines first, then dump the stacks.
Nothing that can wait runs on the uplink's read loop: Told goes through one ordered queue
(`toldqueue.go`), Asked and Accept run in goroutines. See [[agentsd-goroutines-go-through-gojob]].
