# Session parents: a lead or director over a session (#7536, epic #7535)

Spike result: the data model, its storage and access, the wire, cycle prevention, the
next-turn notices and the flag. Gate 1 of the `epic-lifecycle` skill. It writes no product
code. #7537 (the wireframes) and #7538 (the daemon and wire) build on it.

The code read for it, on `main` at fa78ee381: the record `wire.Agent`
(`Packages/Wire/schema/sessions.schema.json`), the daemon's store
(`Go/internal/agentsd/store/store.go`), a turn's words and prefaces (`turns.go`,
`takeTurnWords`, `turnPrefaceBlocks`), the restart pick-up (`recovery.go`), the page-edit and
undo notes (`pageedits.go`), the brief (`brief.go`, `namingBrief`), the sidebar tree
(`Web/src/model/tree.ts` and `SidebarTree` in `AgentsKitCore/Sidebar`, #835, #3553) and the
flags (`Packages/Wire/feature-flags.json`, #6254).

## Decisions

- **A session's parent is one optional field on the child's own record, `parent`.** The
  children of a session are not stored: they are an index that the store makes in memory, as
  it makes `inFolder` today. `startedByAgent` (the starter) does not change and stays the
  history of who started whom.
- **The sidebar's tree follows the parent when it is set, else the starter.** Today the tree
  nests each session under its starter (#835). With a parent set, it nests under the parent
  instead. A person can also put a session "at the top", which ignores its starter in the tree.
- **A parent is a live session on the same host, labelled `lead`.** A director is a lead whose
  children include leads: the model has no separate kind. The `lead` label is checked when the
  parent is set, not later.
- **Moving a session moves its subtree.** Each child points to its parent, so a move changes
  one record, and the subtree goes with it by construction (as `rename(2)` moves a directory).
- **The daemon refuses a cycle, a depth over 6 and a parent with 100 live children.** It checks
  under one lock for all parent changes on the host.
- **Each notice is kept on the recipient's record and goes with its next turn.** The daemon
  takes it in the same record write that takes that turn's words off the queue (an outbox),
  and clears it when the runtime answers the turn. A restart before an answer tells it again.
- **The flag is `session-parents` in `feature-flags.json`: off by default, per person, on the
  Mac first.** It hides the controls and the parent nesting. The daemon does not read it.

*Decided, because* each of these is the simplest form that meets the epic and is easy to
change before the build. Alex can overrule; the questions for Alex are at the end.

## Prior art

- **Adjacency list** (a parent id on each row): Joe Celko, *Trees and Hierarchies in SQL for
  Smarties*; PostgreSQL's recursive CTEs (`WITH RECURSIVE`) read it. Moves are one write;
  reads of a subtree walk the index. Nested sets and materialized paths make a move rewrite
  the subtree, so they do not fit a model where moves are the main write.
- **GitHub sub-issues:** one parent per issue, at most 100 sub-issues per parent and 8 levels;
  a cycle is refused. We take the 100 and a smaller depth (6), as the sidebar draws at most 3
  indents (`maxIndent`).
- **Linear sub-issues and Jira's hierarchy:** the parent is a field of the child; moving a
  parent takes its children with it.
- **POSIX `rename(2)`:** moving a directory into its own subtree fails with `EINVAL`. That is
  our cycle rule.
- **Transactional outbox** (microservices.io, "Pattern: Transactional outbox"): the message is
  written in the same transaction as the state change and sent from there, so it is neither
  lost nor sent twice in the normal case.

## The data model

### `Agent.parent` (new, optional)

```jsonc
"parent": {                       // SessionParent; absent: the tree follows the starter, as today
  "id": "<UUID>",                 // the lead; absent: "at the top" (the starter is ignored)
  "setAt": "<WireDate>",
  "setBy": "person" | "agent"     // who made the last change; "agent" is for a later app tool
}
```

Three states, so a person can take a session out from under its starter:

| `parent` | The tree puts the session |
|---|---|
| absent | under its starter, when the list holds it (today's rule) |
| `{id}` | under that lead |
| `{}` (no id) | at the top |

The **effective parent** of a session is `parent.id` when `parent` is present, else
`startedByAgent`. Cycle and depth checks walk effective parents.

### `Agent.parentNotices` (new, optional, daemon-kept)

```jsonc
"parentNotices": {
  "pending": [                    // at most 32; one per counterpart, the latest change wins
    { "id": "<UUID>", "about": "<UUID>", "change": "parent" | "noParent" | "joined" | "left",
      "at": "<WireDate>" }
  ],
  "more": 3,                      // changes dropped past 32, said as "and 3 more"
  "sentWith": "<UUID>",           // the transcript entry of the turn that took them
  "sent": [ /* the notices taken by that turn, until the runtime answers it */ ]
}
```

- `parent`: the recipient (a child) now has the lead `about` as its parent.
  `noParent`: it has no lead now (at the top, or back under its starter).
- `joined`: the session `about` now works under the recipient (a lead).
  `left`: `about` no longer works under the recipient.
- The text is made at delivery, from the titles then, so a renamed session is named right.
- **Coalescing:** a new change about the same `about` replaces the pending one. A `joined`
  then `left` of the same child before the lead's next turn cancels both. So a drag back and
  forth tells nothing.

The clients ignore `parentNotices`; it is on `wire.Agent` because the record is that type
(as `restartPickUps` is).

### Tenancy

A session belongs to its host, and its host to one person (`docs/explanation/direction.md`,
"Tenancy"). The parent must be on the same host, so a parent never crosses a tenant.

## Storage and access

1. **Volume and growth.** One `parent` (about 100 bytes) per session that has one, and at most
   32 pending notices (about 3 KB) per session. A host has hundreds of live sessions and some
   thousands of archived ones; a person makes a few moves a day. Growth is that of the
   sessions.
2. **Life and retention.** The field lives and goes with its session's record (the existing
   archive and age-out of sessions). There is no roll-up: it is not a time series. A notice
   lives until its turn's answer. A child whose parent is deleted gets `parent` cleared in the
   same sweep as the delete. An archived parent keeps its children's pointers; the tree shows
   them at the top while the parent is not in the same list, as it does for starters today,
   and under it again when it is unarchived.
3. **Hot reads.** The sidebar's tree on each client, from its cache: each row's effective
   parent. The picker: the live sessions labelled `lead` on the host, from the cache. The
   drag: the descendants of the dragged row, from the cache, to refuse a drop on them before
   the daemon does. On the daemon: the cycle walk (at most 6 steps) and the children count
   of a parent (the in-memory index).
4. **Partition.** By host, as the sessions are today. Nothing in this model crosses hosts.
5. **A real database later.** A `parent_id` column (nullable), a `parent_top` flag and an
   index on `parent_id` in the sessions table. The cycle walk is a recursive CTE, or the same
   walk in the code, in one transaction. `parentNotices` is an outbox table keyed by
   recipient. The reads stay behind `store.Store` (`Agent`, `Children(id)`, the new
   `SetParent`), the kind **document**, as the agent records are.
6. **Largest record and count.** `parent`: about 100 bytes. `parentNotices`: 32 entries, about
   3 KB; a notice's text is cut to 2 KB (titles cut to 80 characters, 10 lines and "and N
   more"). Children per parent: 100 live, refused past it. Depth: 6. `boundedRecord` drops
   `parentNotices.pending` before `labels` if a record is over `AgentRecordMost`.
7. **Third parties.** None.

## Cycle prevention and moves

`agents/setParent` and `agents/start` with a parent hold one daemon-wide lock, `parentMu`,
for the check and the write, so two moves cannot each pass the check and make a cycle
together (A under B and B under A at the same time). Moves are rare, so one lock costs
nothing. Under it, for a child `c` and a new parent `p`:

1. `p` exists on this host, is not archived, is not queued to be deleted, and has the label
   `lead`. Else `FailureParentNotLead` or `FailureParentNotFound`.
2. `p != c`, and `c` is not an effective ancestor of `p`: walk `p`'s effective parents. Meeting
   `c` is a cycle: `FailureParentCycle`. The walk stops after 6 steps.
3. The effective depth of `p` plus the height of `c`'s subtree (from the children index) is
   at most 6. Else `FailureParentTooDeep`.
4. `p` has fewer than 100 live children, not counting `c`. Else `FailureParentFull`.
5. With `expectedRevision` given, `c`'s revision is still that one. Else `FailureStale`: the
   client's cache is behind, and it reads again.

Then one record change on `c` (its `parent`), and the notices (below), each a change on its
recipient's record. The walk is O(depth) and the height is O(subtree), both bounded.

A starter chain cannot make a cycle (a session's starter is older than it), and the web's
tree already guards one (`nest`). The clients keep that guard.

## The wire

All new fields are optional, so an old client and an old host work with a new one. The
methods carry the capability `agents.parent/1`; a client offers the controls only when the
host says it.

- **`agents/setParent`** (`SetParentRequest` → `Agent`):
  `{agentID, placement: "under" | "top" | "starter", parentID?, expectedRevision?}`.
  `under` needs `parentID`. `starter` clears `parent` (today's rule). It returns the child's
  record. A no-op (the same placement) changes nothing and sends no notice.
- **`agents/start`:** `StartRequest.parentID?` puts the new session under a lead at creation,
  with the same checks. The new session learns its parent from its brief
  (`namingBrief`: "…, started by X, and you work under Y"), not by a notice; the lead gets a
  `joined` notice.
- **Events:** no new event. A move is an `agent/changed` of the child's record, and a notice
  is an `agent/changed` of the recipient's. A client syncs by its `changedSince` revision
  cursor, as for every record change (#4785).
- **Failures:** `FailureParentNotFound`, `FailureParentNotLead`, `FailureParentCycle`,
  `FailureParentTooDeep`, `FailureParentFull`, `FailureStale` (when no such code exists yet),
  each with a message a client shows as the reason of a reverted drag.
- **Later, not in the MVP:** an app tool `set_session_parent`, so a director can move its own
  leads (`setBy: "agent"`).

### Access by client

- **Mac (the MVP, #7539):** reads `parent` from its cache of `agents/list` and `agent/changed`.
  The picker in New Session lists the cached live sessions labelled `lead`. A drag, or the
  keyboard and VoiceOver action **Move Under Lead…**, calls `agents/setParent` with the
  row's revision. The change is optimistic: the row moves at once, and a refusal moves it back
  and says why. `SidebarTree` takes the effective parent instead of the starter.
- **Remote and web (the port, #7540):** the same field, the same method and the same tree
  rule (`SidebarTree` on the Remote, `nest` in `Web/src/model/tree.ts`), with a long-press or
  a menu for touch. The control plane's copy of a host's list keeps the whole record, so a
  host that is offline still shows the tree. A move needs the host online, as a rename does.
- **Sessions on another machine** (`thereAgents`): not a parent and not a child in the MVP.
  The picker lists only this host's leads. A cross-host parent needs the check across hosts,
  and is a follow-up for #7540 if Alex wants it.

## Next-turn notices

**What each one is told.** Each recipient gets one text block before the turn's words, after
the page-edit and undo notes (`turnPrefaceBlocks`):

- to a child, `parent`: "You now work under <lead title> (session <id>), a lead. Report to it
  with message_session; it can read you with read_session. You were started by <starter>;
  that does not change."
- to a child, `noParent`: "You no longer work under <lead title>. You have no lead now."
- to a lead, `joined`: "<child title> (session <id>, in <folder>, labelled <labels>) now works
  under you. Read it with read_session, and message it with message_session."
- to a lead, `left`: "<child title> no longer works under you" (and ", now under <new lead>").

A move from lead A to lead B gives three notices: `parent` to the child, `left` to A, `joined`
to B. A move of a lead with its subtree tells only the lead and its old and new parents: the
subtree's own parent does not change.

**Delivery, exactly once.**

1. **Kept.** A notice is added to `parentNotices.pending` on its recipient's record in the
   same lock as the parent change, and so it is on disk with it (`agent.json`, written by the
   store's safe write). A restart keeps it. This is unlike the page-edit note, which is held in
   memory only.
2. **Taken.** In `takeTurnWords`, the same `d.change` that takes the turn's words off the queue
   moves `pending` to `sent`, with `sentWith` the turn's transcript entry. The text goes in the
   turn's recorded user message, so the person sees it in the chat too.
3. **Cleared.** When the runtime first answers the turn (its first update, or the end of the
   prompt with no error), one change clears `sent` and `sentWith`.
4. **After a restart.** A record with `sent` set had a turn that the host cut off. The pick-up
   (`recovery.go`, `pickUp`) reads the transcript: when no runtime entry follows the
   `sentWith` entry, the runtime never had the prompt, so `sent` goes back to `pending` and
   goes with the pick-up turn. When one follows, the runtime had it, and `sent` is cleared.
5. **The one double.** The runtime took the prompt and the host died before any answer was
   recorded: the notice is told again, with "(Said again after a restart.)". It is never lost.
   This is the least that can be done without an acknowledgement from the runtime itself.

The notice does not start a turn. A lead that waits in `wait_for_event` reads it when it next
wakes. A line in each chat (a runtime note, "Now under <lead>", "<child> now works under
you") shows the person the change at once.

**#7538 checks:** that a cut-off turn's transcript tells a recorded runtime entry after
`sentWith` from none, on Claude, Codex and OpenCode (marked **unverified** here).

## The feature flag

- `session-parents` in `Packages/Wire/feature-flags.json` (#6254): title "Session parents",
  about "Put a session under a lead or director: in New Session, and by a drag in the sidebar.
  On the Mac.", issue 7535. Off by default, kept per person as `flag:session-parents` in the
  host's display settings, so it is the same on each client of the person's.
- **Off:** no picker, no drag, no **Move Under Lead…**, and the tree follows the starter only.
  The daemon still keeps `parent` and still sends the notices, because the agents must agree
  with each other whatever one person's clients show.
- The Remote and the web page do not show the controls until the port; their tree reads
  `parent` from the port on. The flag comes out as the last child of the port epic.

## What this leaves out, and why

- **No parent across hosts** (above): the cycle check needs one lock over every session it
  walks.
- **No wake of the lead.** The epic says "on their next turns". A wake is a workflow the
  person can make (`agent.ended` and the like) if they want one.
- **No change to `startedByAgent` or to the helper limits.** Helper limits still count by
  starter (`helpersIn`); a session moved under a lead does not count in that lead's limits.

## Questions for Alex

1. Is the "at the top" state (`parent: {}`) wanted, so that a drag to the top takes a session
   out from under its starter? Without it, a drag to the top puts it back under its starter.
2. Should a parent on another machine be allowed in the port (#7540), or stay out?
