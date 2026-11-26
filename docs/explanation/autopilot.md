---
diataxis: explanation
description: What Autopilot starts on its own, the limits it obeys and when it pauses.
---

# Autopilot

Ready work gets a lead with no press from a person. The person says what they want, the
Product Owner files it, and Autopilot starts it (#5897).

## The setting

Autopilot is a setting on the folder, beside the helper limits: `off`, `high` (only
`priority:high` work) or `on`. Absent is `off`, because it starts paid work with no press.
It is set with `autopilot/set` and read with `autopilot/status` (`docs/reference/events.md`,
"Autopilot"). The daemon of the folder runs the loop, so it works with no control plane.

## What it starts

While the mode is `on` or `high`, the daemon reads `forge/next` with `level: epic` and starts a
lead for the top open epic with no lead, as **Start lead** does: the lead prompt, the labels
`#<n>` and `lead`, no worktree. It starts at most one lead for each run. A run happens when
the forge changes, a session ends, a machine comes back or a PR merges, and at least every
ten minutes. In `high` mode an epic that is not `priority:high` waits.

After the leads, it reads `forge/next` for issues and starts a lane (#5900) for the top row that
has no parent and that no standing epic's query takes (their leads start those, so two starters
never race): the Software Engineer persona, the labels `#<n>` and `lane`, the issue's own
prompt. At most one lane starts for each run, in the same limits; in `high` mode only a
`priority:high` issue. A `linux-ok` issue goes to an online Linux machine of the project (the
machine list says each one's `platform`); else, when the project is on several machines, to
the one Auto picks; else, or when that start fails, to this machine.

Starting a lead again when it ends while its epic is open (#5901) and pausing an epic whose
work goes bad (#5902) are the next slices of the epic.

## Its limits

It starts nothing past the folder's helper limits (running sessions and open PRs) or the
machine's capacity level. An epic `on hold` is never started. Because the order comes from
the priority, the Product Owner sets the priority with care.

## What it says

Each epic that waits has a row in `autopilot/status` with the reason: `limit running`,
`limit open-prs`, `capacity near`, `capacity full`, `on hold`, `blocked by #n` or `mode high`.
Each start, pause and resume is an event: `autopilot.started`, `autopilot.paused` and
`autopilot.resumed`.
