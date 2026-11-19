# Recorded daemon streams (#6187)

A file here is a scripted session: the notifications a daemon sent for it, one after another, and
what the daemon held after each. Two runners play it into a client model with drops, duplicates and
reconnects, and the client must end at the daemon's state:

- `Web/test/streamHarness.test.mjs` (the web model; `web:test`), with `Web/test/streamPlayer.mjs`;
- `Packages/AgentsKit/Tests/AgentsKitTests/Unit/StreamHarnessTests.swift` (AgentsKit, which the Mac
  window and the Remote share; `agentskit:test`).

The same file is the input of both, so a difference between the clients is a failed test.

## Format (`format: 1`)

| Key | Meaning |
|---|---|
| `session`, `host` | The chat the client has open, and the host its notifications come from. |
| `start` | What the daemon held before step 0: `agents` (a list read) and `transcript` (a page read, from the first entry). |
| `steps[i]` | `method`, `params`: one notification, as the daemon sent it. `after`: what a read gives right after it (same shape as `start`). |
| `transcript.startEntries` (optional, in an `after`) | The read begins with the first n entries of `start`'s transcript, and `entries` lists only those after them. A long chat (`session-long.json`) writes its 2,000 turns once, not once a step. |
| `scenarios[]` | `name`, `ops`, `expect`: the step whose `after` the client must hold when the ops end (-1: `start`). `fault` (optional): the issue of a bug the scenario finds; both runners skip it, and the fix removes the field. `tail` (optional): the client may let go of a long chat's oldest entries, as both do by design (`entriesKept`), but holds at least the last `tail` of them, and what it holds is the end of the daemon's transcript, each entry once and in order. |

Ops:

- `{"op": "deliver", "step": i}`: the client hears step `i`'s notification.
- `{"op": "drop", "steps": [...]}`: those notifications are lost. It does nothing; it says what the scenario means.
- `{"op": "reconnect", "read": k, "during": [...]}`: the client catches up: it reads the daemon's state after step `k`
  (-1: `start`), and hears the notifications of `during` (steps, in that order) while the read is on its way. A step
  at or before `k` in `during` is a duplicate of what the read holds; one after it is news.

A step number may be delivered more than once (a duplicate). Within one connection the daemon's notifications
arrive in order, so reorder is a notification arriving after the read that already holds it, which `during` says.

## Adding a stream or a scenario

A scenario is a few lines in `record.mjs`; a new session is a new function in it. Run
`node Packages/Wire/streams/record.mjs` to write the files again, and commit them: no runner changes. A runner needs
only the small surface of a client (`notify`, `catchUp`, `view`), so a later check (#6188) plays the same files
into another part of the client model by giving it that surface.

`record.mjs` holds a stand-in for the daemon. When a type changes in `Packages/Wire/schema`, change the records there:
both runners decode them with the clients' own types, so a record the type no longer accepts fails the test.
