---
name: fix-main
description: Make a red main green again (#5588, spec §6 of #5581) - never rerun a failed job; quarantine a flaky test on a pull request of its own and file a bug to fix it later; for a real break, file or update one "Main broken" bug, bisect the batch with scripts/bisect-main.sh and fix forward; never revert without Alex, and ask Alex with ask_question when main cannot be made green. Use when the Fix main workflow starts on a ci run on main that failed, or when Alex asks to fix main.
---

# Fix main

A `ci` run on `main` failed. You make `main` green again, as fast as you can. You do not
gate merges: other PRs land while `main` is red (#5581, goal 2), and their lanes wait for
`main` to go green (`fix-failed-checks`, step 2a, #5589).

Use the `github` server's tools where they fit (`get_job_logs`, `actions_list`), and `gh`
for the rest. Write every comment that you post as `alexec` with `<!-- agent -->` first
(AGENTS.md, "Who an agent posts as").

## Rules

- **One red spell, one lane.** A red spell starts at the first red `push` run on `main`
  and ends at the next green `push` run. The workflow starts a new agent for each run on
  `main` (every run, green or red, and each bisect step). Only the first one for a spell
  does the work; the others end at once (step 1). When you already work on a spell, a new
  red run is part of it: add it to the bug (step 4), and do not start again.
- **Never rerun a failed job (Alex, 2026-11-15, #5754).** A rerun that passes hides a flaky test,
  and the test stays flaky in the suite. A test that fails with no cause in the batch is a
  flake: quarantine it and file a bug to fix it (step 3).
- **Never revert without Alex (Alex, 2026-11-15).** Fix forward. When you cannot make
  `main` green, ask Alex with `ask_question` (step 6). Revert only when Alex says so.
- **Never weaken a check.** Never skip, turn off or loosen a test or a job to make
  `main` green. A quarantine of a flaky test, with a bug to fix it, is the only exception
  (step 3).
- **Never force-push, and never push to `main`.** Every change goes on a PR with squash
  auto-merge.
- **Lease "build"** for each local build, test or lint, and release it when the command
  ends. The bisect runs in CI and needs no lease.

## 1. Read the event

The event is `run.completed` for `ci.yml` on `main`. Read its `run`: `id`, `attempt`,
`event`, `headSha`, `conclusion`, `url`.

Do these checks in order, and stop at the first that ends the turn. Most runs end here,
so use as few tool calls as you can. Each "end" here is: archive the session with
`archive_session`, so it does not look like a lane in check 3.

1. **`event` is not `push`** (a `workflow_dispatch` from a bisect): it is not a red
   `main`. End.
2. **`conclusion` is `cancelled` or `skipped`:** end.
3. **Another lane has the spell.** Call `list_sessions` and look for a session that is
   not archived, is not you, and has the label `fix-main` (the workflow gives it to each
   of its sessions; a lane archives itself when its spell ends). When there is one:
   - `conclusion` is `failure` or `timed_out`, with `attempt` 1: send that lane this
     run's link with `message_session` (it is part of its spell, or a new break), and
     end.
   - Else (a green run, or an attempt that a person reran): that lane waits for it
     already. End.
4. **`conclusion` is `success`:** when an open bug titled "Main broken:" is left with no
   lane (none in check 3), comment on it with this run's link and close it (step 7, part
   1). End.
5. **`conclusion` is `failure` or `timed_out`, with `attempt` more than 1** and no lane:
   a person reran a job. Treat it as the first red run of a spell: go on with step 2. Do
   not rerun it again.
6. **`conclusion` is `failure` or `timed_out`:** the event is a hint, so read the run
   again (`gh run view <id> --json status,conclusion,attempt,jobs,headSha`). This spell
   is yours: go on. When a newer `push` run on `main` is green already, do step 2 all the
   same: a flake that passed by chance stays a flake (go to step 3), and a break that a
   later commit fixed ends here.

While you work the spell, the events of `main`'s next runs come to your waits; new agents
of the workflow see you in check 3 and end.

## 2. Flake or break?

Do not rerun the failed jobs to find out (Rules). Read the failure and the batch.

1. List the failed jobs (`go`, `apps`, `test`) from the run's `jobs`, and read their logs
   (`get_job_logs`, `failed_only: true`). Write down each failed test or build error.
2. Read the batch from the failed run's summary (the `### Batch` section, written by
   `scripts/ci-batch.sh`, or `gh run view <id> --log | grep bisect-main.sh`). It gives the
   last green sha and this run's sha. List what the batch changed: `git diff --stat
   <green sha> <red sha>`.
3. **A build error** (a compile error, a missing file, a lint finding, a stale generated
   file) is a break: go to step 4.
4. **A failed test is a break** when the batch changed the test, the code that it tests,
   or a file that it reads (its project's `project.json` names the inputs), or when the
   failure names something that the batch changed. Go to step 4.
5. **Else the failed test is a flake:** nothing in the batch explains it. Signs that agree
   are a timeout or a wait that ran out, a test that failed on other commits too
   (`gh issue list --search "<test> in:title,body"`, or the newest runs on `main`), and a
   PR whose own checks passed the same test at the same inputs. Go to step 3.
6. **Both** (one break and one flake in the same run): do step 3 for the flake and step 4
   for the break.

## 3. Quarantine a flake

1. Search for an open `bug` about the same test (`gh issue list --search "<test>
   in:title,body"`). When there is one, comment with the run's link and the failure lines.
   Else file one `bug`, with no milestone, the labels `bug` and `size:XS`, titled "Flaky
   test: `<test>` fails under load on main". The body gives the job, the failure lines
   from the log (in a fence), the run's link and why it is a flake (step 2.5). The bug
   stays open: the bug lane (#4207) fixes the test later and removes the quarantine.
2. Label the session `#<bug>` with `set_session_labels`.
3. Move to a worktree of your own with `move_worktree` on a new branch
   `fix-main/quarantine-<bug>`. Run `git fetch origin` and `git rebase origin/main`.
4. Quarantine the test, and only that test:
   - **Swift:** mark it `.flakyUnderLoad` on its `@Test` line, and add it to the list in
     `Packages/AgentsKit/Tests/AgentsKitTests/Support/FlakyUnderLoad.swift` with the bug.
   - **Go:** `flaky.UnderLoad(t, "#<bug>")` as the test's first line
     (`Go/internal/flaky`).
   - **Web:** `{ skip: flakyUnderLoad("#<bug>") }` as the test's options
     (`Web/test/flaky.mjs`).

   Each one skips the test in CI and runs it with `AGENTS_RUN_FLAKY=1`.
5. Run the test's task on this Mac under the "build" lease (`scripts/nx.sh affected -t
   gen build vet lint test`).
6. Commit with the subject "Quarantine `<test>`: flaky on main (#<bug>)" and the line
   `mac: docs only, remote: docs only, web: docs only`. Call `conflict_risk_analysis`,
   push, and open the PR with `gh pr create`: the body says `Refs #<bug>` (not `Fixes`:
   the bug is for the fix), the failing run and step 2.5's reason. Turn on squash
   auto-merge at once. Label the session `P#<quarantine pr>`.
7. Go to step 6.

## 4. File a break

Search for an open bug titled "Main broken:" (`gh issue list --state open --label bug
--search "Main broken in:title"`).

- **One is open:** comment on it with this run's link, the failed jobs and the failing
  tests. Use that bug for the rest of the spell.
- **None is open:** file one, with no milestone (AGENTS.md, "Bugs have no milestone"),
  labels `bug`, `priority:high` and `size:S`, titled "Main broken: `<job>` since
  `<short sha>`". The body gives the run's link, the failed jobs, the failing tests or the
  build error (the lines from the log, in a fence), and the batch. Write down the time of
  the first red run: step 6 counts from it.

Label the session `#<bug>` with `set_session_labels`.

### Bisect the batch

1. **The batch is one commit, or the log names the commit that caused it:** that commit is
   the bad one. Go to step 5.
2. Else run, in your worktree (no lease: each step runs in CI):

   ```sh
   scripts/bisect-main.sh <green sha> <red sha> <failed jobs…>
   ```

   It runs about log2(n) `workflow_dispatch` runs of only the failed jobs, each at a
   different commit, and prints the first bad commit and its pull request. Run it in the
   foreground with a timeout of 10 minutes; when it needs more, run it with
   `run_in_background` and wait for it to end.
3. **The bisect cannot decide** (a step ends `cancelled`, or the first bad commit is the
   red sha and that commit's own PR checks show no failure like it): the break may come
   from two PRs together, from outside the repo (a runner image, a tool's new release), or
   it is a flake after all. Comment on the bug with what the bisect found. A failed test
   with no cause found is a flake: go to step 3. Else go to step 5.

Comment on the bug with the first bad commit, its PR and the bisect's runs.

## 5. Fix forward

1. Move to a worktree of your own with `move_worktree` on a new branch `fix-main/<bug>`.
   Run `git fetch origin` and `git rebase origin/main`.
2. Fix the cause, and only the cause. Run the failed tasks on this Mac when they can run
   here, under the "build" lease. A failure that only Linux has (the `go` job) shows only
   in CI.
3. **When the fix is not clear, or needs a decision from a person:** do not push. Comment
   on the bug with what you found, and ask Alex at once (step 6, "Ask Alex").
4. Commit with the subject "Fix main: <what> (#<bug>)" and these lines: `mac: same,
   remote: same, web: same` when the fix changes a client, else `mac: docs only, remote:
   docs only, web: docs only` (AGENTS.md, "Keeping the three clients in step").
5. Call `conflict_risk_analysis`, push, open the PR with `Fixes #<bug>`, the bad commit's
   PR, the failing run and the bisect, and turn on squash auto-merge. Label the session
   `P#<fix pr>`.
6. **Reopen the bad PR's issue** when your fix does not keep what that PR meant to do
   (`gh issue reopen`, with a comment that gives your PR and the failure lines).
7. Go to step 6.

## 6. Wait for green, and ask Alex when you cannot get there

Wait with one `wait_for_event`: `run.completed`, `where: {"workflow": "ci.yml", "branch":
"main", "anyone": true}`, together with `pr.merged`, `pr.closed` and `pr.blocked`,
`where: {"pr": <your pr>}`, with `until_minutes: 30`, and end the turn. Each time you wake:

- **Your PR is blocked:** do what its reasons say, as in the Software Engineer persona's
  step 7 (`.agents/personas/software-engineer.md`). A required check that fails because
  of the break on `main` is not yours: wait.
- **Your PR merged:** wait again for the `push` run on `main` that contains it.
- **A `push` run on `main` that contains your commit is green:** do step 7.
- **A run on `main` is red with a different failure:** it is a new break or flake in the
  same spell. Comment on the bug, and do steps 2 to 5 for it.
- **You cannot get `main` green:** ask Alex (below) when one of these is true:
  - the fix is not clear, or needs a decision (step 5.3);
  - your fix PR merged and `main` still fails the same way;
  - your fix PR fails its own checks with the same failure, and you have no other fix;
  - 60 minutes after the first red run, `main` is still red.

Run `scripts/rearm-automerge.sh --stalled` each time you wake.

### Ask Alex

Ask once in a spell, with `ask_question`. Do not `notify` as well. The question stands on
its own: "Main is red since <time> (<bug link>): `<job>` fails `<test or error>`. The
bisect found <bad PR link>. Claude (this agent) tried <your PR link, or: no fix, because
…>. Alex, what does Claude do?" The options:

- **Revert #<bad pr> (Recommended)** when the bisect found a bad PR: do "The revert",
  below.
- **Keep fixing forward:** say in the description what you will try next.
- **Leave it to Alex:** comment on the bug that Alex has it, unassign nothing, and
  archive the session.

Wait for the answer with the same `wait_for_event` as above, and do what Alex says. Never
revert before Alex answers.

### The revert (only when Alex says so)

1. Move to a worktree of your own with `move_worktree` on a new branch
   `fix-main/revert-<pr>`. Run `git fetch origin` and `git rebase origin/main`.
2. Run `git revert --no-edit <bad sha>`. **When it conflicts:** run `git revert --abort`,
   comment on the bug, and ask Alex again (this is a new question, not the same one).
3. Set the commit's subject to "Revert #<pr>: it broke `<job>` on main (#<bug>)". The
   body says what failed, the run's link, Alex's answer, and the client lines as in
   step 5.4.
4. Verify the failed jobs' tasks on this Mac when they can run here, under the "build"
   lease. Call `conflict_risk_analysis`; say in the PR body what it said.
5. Push, open the PR with `Fixes #<bug>`, the reverted PR and the failing run, and turn
   on squash auto-merge. Label the session `P#<revert pr>`.
6. **Reopen the reverted PR's issue** (the issue its body `Fixes`) with `gh issue
   reopen`, and comment with the revert PR and the failure lines, so the next lane lands
   the change again with a fix. When the PR fixed no issue, comment on the PR itself.
7. Wait as above.

## 7. Close it

When a `push` run on `main` is green after the break:

1. **A break:** comment on the "Main broken" bug with the green run's link, and close it
   (`gh issue close <bug> --reason completed`) when your PR's `Fixes` did not close it
   already. **A flake:** comment on the flaky test's bug with the green run's link, and
   leave it open for the fix.
2. Label the session `merged` when your PR merged.
3. Remove your worktree's build output (`build/`, `.build`, `Packages/*/.build`), and
   leave the worktree with `move_worktree` and `leave_worktree: remove`.
4. Archive the session with `archive_session`. The workflow starts a new lane on the
   next red run on `main`.
