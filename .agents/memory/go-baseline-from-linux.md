---
name: go-baseline-from-linux
description: golangci-lint finds different things on the Mac and on Linux; a Go baseline written on a Mac drops Linux-only entries and breaks CI's go job
metadata:
  type: project
---

`scripts/lint.sh --baseline go` on a Mac drops entries that only Linux reports. For example, gosec's G702 in `Go/internal/agentsd/git.go` shows on Linux and not on the Mac. #5667 wrote the baseline on a Mac, and main's Linux `go` job then failed on that old entry (#5682).

**Why:** CI's `go` job, which gates main, runs golangci-lint on Linux. gosec's taint analysis does not give the same result on each OS.

**How to apply:** do not write the whole baseline again on a Mac. Add or remove only the lines you mean to, by hand. Check the result against the Linux `go` job's log, not only a local run. See [[go-tests-on-the-mac-host]].
