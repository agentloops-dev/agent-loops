---
name: fix-main-asks-the-bug-lane-for-root-cause-lanes
description: The bug lane's lead (#4207) starts a lane on a new flaky-test bug by itself; Fix main asks it, and starts no root-cause lane of its own
metadata:
  type: project
---

On 2026-11-21, Fix main filed #6603 and #6619 and started a root-cause lane on each. The bug lane's lead (#4207) started its own lane on the same bugs within minutes, because a new `bug` with no milestone is its work. That made two lanes and two fix PRs for #6603 (#6620, #6621). For #6619, both stopped their own lane at once, so for a time nobody worked on it.

**Why:** the bug lane's lead takes every new bug with no milestone. A root-cause lane that Fix main starts is a duplicate, and two agents that clean up at the same time can each stop "the other" one.

**How to apply:** after Fix main files a flaky-test bug, it messages the bug lane's lead with `message_session` (the bug, the quarantine PR, the probable cause), and does not `start_session` itself. AGENTS.md (#6037) allows either; the lead asked for this one. When two lanes overlap, the lead says which one stays, and the other side stops only its own.
