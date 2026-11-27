---
name: second-scratch-host-by-a-second-root
description: an online second machine for a walk on this Mac is a second agentsd on its own root, real HOME, joined by a host code; no overlay, no Keychain dialog
metadata:
  type: project
---

For #7558 (2026-11-27) a walk needed an online machine that is not This Mac (a new session's
form on a machine whose set-up failed). It worked with no overlay and no changed HOME: after
`launch.sh`, run the same build's `agentsd` on a second short root, with this session's
`CLAUDE_*`/`AGENTS_*` taken out as `launch.sh` does:
`env -u … AGENTS_ROOT=/tmp/run-<slug>c agentsd --control-code "$(agents-control code --host --home $ROOT/control)" --host-name dev-3`.
It joins under a random id (`uplink: connected … as <id>`), shows as a server in the window,
and needs Compare Digits ▸ Approve. Add a folder with `rpc.py <second root> call folders/add`.
A set-up record for it is a file, `$ROOT/control/store/v1/machine-setup/<id>.json`, read after a
control plane restart (kill its pid, start it with the same command line, write `control.pid`).
`stop.sh` on each root stops both.

`preferences/set` takes the map itself (`{"flag:machine-scripts":true}`), not
`{"preferences":{…}}`; the wrong shape saves a key named `preferences` and no flag shows.
A new session on another machine: `walk.py context "<machine>:<folder>" "New Session"`.

**Why:** [[scratch-server-host-on-the-mac]] and [[no-changed-home-for-agentsd-on-a-mac]] are
for a host that must act as a Linux server; a plain second host needs neither.
**How to apply:** use this before an overlay; never change HOME.
