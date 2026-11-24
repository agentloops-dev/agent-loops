---
name: periphery-removed
description: Alex removed Periphery from CI and the repo on 2026-11-21 (#6526); do not add a Swift dead-code gate back without Alex
metadata:
  type: project
---

Periphery (Swift dead code, #6336) ran in `swift-lint:lint` from 2026-11-19 to 2026-11-21. Alex
removed it (#6526): its four index builds (Mac app, Remote, AgentsKit, CodeText) had no
compilation cache and took about 8.5 min of each warm `swift-lint` run, 20 to 30 min cold, plus a
2.6 GB Actions cache of the repo's 10 GB. `swift-lint:lint` is SwiftLint only.

**Why:** the dead-code gates were for less code so that builds run faster; Periphery made CI slower.
**How to apply:** do not propose Periphery or another index-build Swift gate again without Alex; the dead-code review reads Swift by hand.
