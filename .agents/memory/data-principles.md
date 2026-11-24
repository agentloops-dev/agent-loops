---
name: data-principles
description: "Alex 2026-11-21/22 — no store grows unbounded: size and count limits, 90-day age-out, config in .agents, roll-up only for time series, a declared kind behind an interface; lean into backends; evict or refuse; eviction async"
metadata:
  type: project
---

Alex's rules for this app's data (2026-11-21, design 2026-11-22). The full text is `docs/explanation/data-stores.md`; epic #6669 has one issue for each store that breaks a rule.

**The goal: no data store grows without bound.** Each store is a file today, but must be swappable for a real database later.

1. **Size:** a largest record, and a count or total-size limit on the store.
2. **Time:** records age out within 90 days.
3. **Config:** in `<repo>/.agents/` or `~/.agents/`. A control plane's config stays in its own store (Alex, 2026-11-24, #6761; also for #6762 and #6771 to #6781). A machine's copies from the control plane and its trust records stay in its data folder (Alex, 2026-11-24, #6751 to #6754); its keep-awake settings are shared in `~/.agents/settings`, its backup settings are there with one entry a machine (#6745, #6746).
4. **Roll-up:** only time series roll up (usage: hours for 7 days, days for 90). The audit, conversations and logs just age out.
5. **Kind:** each store declares its kind (small file, document, log, time series, graph, blob, cache, secret) and is reached through that kind's interface.

**Why:** the worst case of every store is known ([[bound-data-not-concurrency]]).

**How to apply:**
- Lean into the backend's own limits (S3 lifecycle, TTLs, JetStream and capped-collection limits, quotas); our code does only the rest (Alex: "the people who built them are smart").
- Most stores are one small JSON file: use one typed small-file type with declared limits.
- Each store declares evict (history, logs, caches) or refuse (a person's own data, never evicted).
- Eviction is asynchronous: a hourly sweep, sooner past a high-water mark. Only the largest-record check and refusing stores check at write.
- Stores that remember "seen" keep a cutoff time instead of every entry.
