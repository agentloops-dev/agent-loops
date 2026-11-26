---
name: autopilot-lead-restart
description: Autopilot replaces a lead stopped/finished 10 min (2 restarts per epic per day, then paused "lead keeps stopping"); a person's stop is EndedReasonCancelled and is never restarted
metadata:
  type: project
---

#5901: autopilotrun.go. A stop by a person and a stop_session both end as `cancelled`, so any cancelled lead is left alone. Restarts are kept in autopilot-status.json (`restarts`). **How to apply:** supersedes the manual "archive + new lane" advice in [[stopped-with-the-daemon-resumes]] for leads when Autopilot is on.
