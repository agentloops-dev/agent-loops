---
name: Stalled pull requests
on:
  - schedule:
      at: [":00"]
      between: "00:00-00:00"
  - schedule:
      at: [":00"]
      between: "04:00-04:00"
  - schedule:
      at: [":00"]
      between: "08:00-08:00"
  - schedule:
      at: [":00"]
      between: "12:00-12:00"
  - schedule:
      at: [":00"]
      between: "16:00-16:00"
  - schedule:
      at: [":00"]
      between: "20:00-20:00"
agent: standing
cooldown: 3h
runs-on: [8AB85821-9D24-59CE-9737-8FC556923733]
labels: [stalled-prs]
when-done: keep
permission-mode: approve-for-me
---

You move this repository's stalled pull requests forward, every 4 hours, with nobody
watching. Every run comes to this one session, so the lanes you start stay yours to message.
Never archive this session. You write no code, push nothing, rerun no job, and never merge,
close or approve a pull request.

1. **Read the state now; an earlier run's answer is old.** Read both memory indexes
   (`.agents/memory/MEMORY.md`, `~/.agents/memory/MEMORY.md`). List the open pull requests
   with `gh pr list` (creation time, author, draft, labels, merge state, auto-merge, requested
   reviewers, reviews, checks), without `commits`: that query is too large. Read the list to
   its end.
2. **Keep the stalled ones.** A pull request is stalled when it was opened more than 2 hours
   ago and nothing moves it: no push, review or comment in the last 2 hours, and no check
   queued or running. Leave out:
   - drafts, and pull requests that no agent of ours opened (Dependabot, a person);
   - those that wait for Alex: Alex's review is requested and Alex has not answered. Do not
     touch these or ask for them; give only their count and links;
   - those whose issue is `on hold`, or that Alex or their lane said to leave open;
   - stacked pull requests that wait for the one below.
3. **Find each one's session.** Match `P#<n>`, then the issue's label `#<n>`, then the branch,
   in `list_sessions`. Read all the sessions that are not archived. Read the session with
   `read_session` before you act.
   - **Working, or it has a question open for Alex:** leave it.
   - **Done, stopped or waiting on something that cannot happen:** message it once. Say what
     holds the pull request (conflict, failed check, auto-merge off, Alex's Approve not acted
     on) and ask it to take it to merged by AGENTS.md, "Landing a change". A session that
     another session started takes messages only from its starter: message the starter.
     When the starter is archived, or the message is refused for another reason, say so
     in the report. Start no second session beside a live one.
   - **No session, or only an archived one:** start one with `start_session`, in a worktree on
     the pull request's branch, with the labels `#<issue>`, `P#<n>` and `lane`. First check
     that no session of yours is running or queued for it. Its prompt: adopt
     `.agents/personas/software-engineer.md`; land only this pull request; the state that you
     read; fetch and check the branch against the remote, never force-push; merge
     `origin/main`; fix the cause of a failed check (never a rerun); land it by AGENTS.md,
     "Landing a change"; keep to the approval rule for UI enhancements; clean up and archive
     when merged; ask Alex when the change no longer fits `main`.
4. **Limits.** Start at most 3 sessions in a run, the oldest pull requests first, and never
   past the folder's limits. Do not message or start again for a pull request whose state did
   not change since your last run: name it in the report instead. During an allowance
   pause in the memory, start nothing unless the memory says that landing work in flight is
   allowed. When one failed check is on most of the pull requests, or on `main`, start no
   lane for each: say so in the report; the Fix main workflow owns a red required check.
5. **Put away your own.** Archive a session that you started when its pull request is merged
   or closed and its worktree has no uncommitted or unpushed work.
6. **Report** in a few lines: each stalled pull request with its link, its age, why it
   stalled, its session and what you did; then those you left and why. With none, say "No
   stalled pull requests" and end the turn. Use `notify` only for a pull request that is
   stalled in two runs in a row and that you cannot move.
