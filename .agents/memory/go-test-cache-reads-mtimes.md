---
name: go-test-cache-reads-mtimes
description: Go's test cache keeps a pass only while each file a test opened has the same size and mtime; a fresh checkout misses it, so CI sets content times
metadata:
  type: project
---

Go's test cache (cmd/go `hashOpen`, `hashWriteStat`) keys a package's pass on the size, mode and mtime (to the nanosecond) of every file and folder its tests opened, not on their content. A fresh checkout gives each file a new mtime, so with the Go cache restored, about 20 of the module's 70 tested packages still ran again on every CI run (measured on a Mac, 2026-11-15, #5677). The test binary also embeds its absolute path (no `-trimpath`), so a clone at a new path misses for every package; CI's path is fixed.

**Why:** `scripts/ci-mtimes.py` (#5677) sets each tracked file's and folder's time from its git object hash, so the same content has the same time in every checkout. A new CI job that runs `go test` with a restored Go cache needs it after its checkout.

**How to apply:** when Go tests on CI "run everything although the cache restored", check the step runs after the checkout and that nothing rewrites the files after it. See [[go-tests-on-the-mac-host]].
