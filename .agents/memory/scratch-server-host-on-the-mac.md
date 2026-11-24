---
name: scratch-server-host-on-the-mac
description: a second agentsd as a "server" on this Mac (runtime sign-in, set-up cards) with no Docker, by a Go build overlay; a fake HOME alone raises a Keychain dialog on the screen
metadata:
  type: project
---

A Mac's agentsd refuses `runtimes/signInStart`. For #5866 (2026-11-24) a scratch "server" was made
with `go build -overlay` (no source changes): `internal/agentsd/install.go` with `mac = ""` at the top of
`setToolsHomes`, `internal/mac/keychain_darwin.go` replaced by a file store, and
`"/usr/bin/security"` replaced by `/usr/bin/false`. It runs with `env -i HOME=<scratch>
SHELL=<scratch shell> PATH=…` and the fake `claude` and `codex` of `runtimesignin_test.go` in
`$HOME/.local/bin`, joined with a `code --host`. Approve it in the window (Compare Digits, Approve).
Its `setup-reports.jsonl` takes step 1 to 8 reports in order, for the set-up cards.

**Why:** with the real Keychain code, a HOME with no login keychain makes macOS show a SecurityAgent
dialog on the person's screen, and agentsd hangs.

**How to apply:** use the overlay, never only another HOME. A window that is quit and opened again
by hand loses its lock key: launch a new slug. See [[seeded-keychain-items-prompt-the-app]] and
[[fake-runtime-needs-a-scratch-shell]].
