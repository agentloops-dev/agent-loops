---
name: held-calls-and-mac-sleep
description: An MCP call held open across a Mac sleep ends in the client's timeout; the client counts the sleep, Go's timers do not (#6310)
metadata:
  type: project
---

On 2026-11-18 the Mac (on battery) slept 191 times, mostly short Maintenance Sleeps between
DarkWakes. All 46 `wait_for_event` calls that ended in "The operation timed out" or "sent no
response or progress for N s" were open over a sleep or a wake. The "N s" was the sleep's
length. Go's `time.AfterFunc` does not count a sleep, so the 45 s hold had not ended when the
client gave up on waking. Since #6310, agentsd answers every held call on agents-watch's
`sleep` and again on `wake` (`eventholds.go`).

**Why:** clusters of client timeouts in the same minute looked like a blocked MCP handler, but
the daemon log showed only an uplink drop on wake, and the daemon was not blocked.

**How to apply:** for a run of client timeouts, first match each call against
`pmset -g log` (Sleep, Wake, DarkWake) before you look for a blocked handler. A new call that
the server holds open must also be answered on `sleep`. See [[agentsd-stacks-on-sigusr1]].
