---
name: data-principles
description: "Alex 2026-11-21 — goal: no store grows unbounded; bounded by size/count, 90-day age-out, config in .agents, roll-ups allowed, each store has a declared kind behind an interface so a file can become a database"
metadata:
  type: project
---

Alex's principles for this app's data (2026-11-21). **The goal: no data store grows without bound.**

1. **Size:** every store has a byte limit, or a count limit on records of known size.
2. **Time:** records age out at 90 days at most.
3. **Config:** configuration is in a `.agents` folder: `<repo>/.agents/` or `~/.agents/`.
4. **Roll-up:** old detail can become a summary (usage: 168 hours, then daily totals for 90 days, `usage/rollup`). The summary is a store too, so rules 1 and 2 apply to it.
5. **Kind:** each store is a file today, but must be swappable for a real database later. So each store declares its kind (document, log, time series, graph, blob, key-value, cache, secret) and is reached through an interface of that kind. The control plane's `store.Store` is a key-object interface already; the host daemon has none (2026-11-21).

**Why:** the worst case of every store stays known, so performance and disk stay predictable ([[bound-data-not-concurrency]]). Config in `.agents` is inspectable and follows the person or the repo.

**How to apply:** a new store names its kind, its interface, its size, count and age bounds and when they are applied (on write or hourly, not only at start or on read). Its config goes in `.agents`. For history, roll up; never keep an unbounded log. The stores that broke a rule on 2026-11-21 are epic #6669, one issue a store; the bus log, Forever conversations and the Mac's Forget were the worst.
