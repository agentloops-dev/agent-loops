---
name: fix-main
description: Make a red main green again (#5588, spec §6 of #5581) - rerun the failed jobs once to find a flake, file or update one "Main broken" bug, bisect the batch with scripts/bisect-main.sh, revert the squash commit that broke main on a pull request of its own (fix forward only when the revert conflicts), reopen that PR's issue, and tell Alex only when main is still red after 60 minutes. Use when the Fix main workflow starts on a ci run on main that failed, or when Alex asks to fix main.
---

# Fix main

A `ci` run on `main` failed. You make `main` green again, as fast as you can. You do not
gate merges: other PRs land while `main` is red (#5581, goal 2), and their lanes wait for
`main` to go green (`fix-failed-checks`, step 2a, #5589).

Use the `github` server's tools where they fit (`get_job_logs`, `actions_run_trigger`,
`actions_list`), and `gh` for the rest. Write every comment that you post as `alexec` with
`<!-- agent -->` first (AGENTS.md, "Who an agent posts as").

## Rules

- **One red spell, one lane.** A red spell starts at the first red `push` run on `main`
  and ends at the next green `push` run. The workflow starts a new agent for each red
  run on `main` (`conclusion` `failure` or `timed_out`, #5775; a rerun and a bisect step
  too). Only the first one for a spell does the work; the others end at once (step 1). When you already work on a
  spell, a new red run is part of it: add it to the bug (step 3), and do not start again.
- **Revert first** (Decided in #5581: it is the fastest way back to green, and easy to
  undo). Fix forward only when the revert conflicts.
- **Never weaken a check.** Never skip, turn off or loosen a test or a job to make
  `main` green. A quarantine with `.flakyUnderLoad` is the only exception, and only for a
  flake (step 2).
- **Never force-push, and never push to `main`.** Every change goes on a PR with squash
  auto-merge.
- **Lease "build"** for each local build, test or lint, and release it when the command
  ends. The bisect runs in CI and needs no lease.

## 1. Read the event

The event is `run.completed` for `ci.yml` on `main`, with `conclusion` `failure` or
`timed_out`: the workflow starts no agent for a green, cancelled or skipped run (#5775).
Read its `run`: `id`, `attempt`, `event`, `headSha`, `conclusion`, `url`.

Do these checks in order, and stop at the first that ends the turn. Most runs end here,
so use as few tool calls as you can. Each "end" here is: archive the session with
`archive_session`, so it does not look like a lane in check 2.

1. **`event` is not `push`** (a `workflow_dispatch` from a bisect): it is not a red
   `main`. End.
2. **Another lane has the spell.** Call `list_sessions` and look for a session that is
   not archived, is not you, and has the label `fix-main` (the workflow gives it to each
   of its sessions; a lane archives itself when its spell ends). When there is one:
   - `attempt` is 1: send that lane this run's link with `message_session` (it is part
     of its spell, or a new break), and end.
   - Else (a rerun's attempt): that lane waits for it already. End.
3. **`attempt` is more than 1** and no lane: a person reran a job. Treat it as the first
   red run of a spell: go on with step 2.
4. **Else:** the event is a hint, so read the run again (`gh run view <id> --json
   status,conclusion,attempt,jobs,headSha`). When a newer `push` run on `main` is green
   already, end. Else this spell is yours: go on.

While you work the spell, the event of your own rerun (step 2) and of `main`'s next runs
come to your waits; new agents of the workflow see you in check 2 and end. An open bug
titled "Main broken:" that no lane has (its lane stopped) is not closed by a green run:
the next red run's lane finds it in step 3 and takes it as its own.

## 2. Is it a flake?

1. List the failed jobs (`go`, `apps`, `test`) from the run's `jobs`, and read their logs
   (`get_job_logs`, `failed_only: true`). Write down each failed test or build error.
2. **A rerun proves nothing for a build error** (a compile error, a missing file, a lint
   finding): go to step 3.
3. Run only the failed jobs again, once, at the same sha (`actions_run_trigger`,
   `method: rerun_failed_jobs`). Wait for it with `wait_for_event` on `run.completed`,
   `where: {"run": <id>}`, `until_minutes: 60`, and end the turn.
4. **The rerun passes:** it is a flake. `main` is green again, so do not file a "Main
   broken" bug.
   - Search for an open `bug` about the same test (`gh issue list --search "<test> in:title,body"`).
     When there is one, comment with the run's link. Else file one `bug`, with no
     milestone, the labels `bug` and `size:XS`, titled "Flaky test: `<test>` fails under
     load on main", with the job, the failure lines from the log and the run's link.
   - **A Swift test:** mark it `.flakyUnderLoad` (`Packages/AgentsKit/Tests/AgentsKitTests/Support/FlakyUnderLoad.swift`
     says how) on a PR of its own, `Fixes` nothing and `Refs #<bug>`, with squash
     auto-merge on (AGENTS.md, "Questions that already have an answer").
   - **A Go test or a web test:** there is no quarantine for these. Leave the bug for
     the bug lane (#4207).
   - Do not bisect. Archive the session with `archive_session` when the quarantine PR has
     auto-merge on (or at once, with no PR), so the next red run finds no lane.
5. **The rerun fails with the same failure:** it is a real break. Go to step 3. A rerun
   that fails with a different failure is a break and a flake together: go to step 3
   with the failure that both runs have.

## 3. File it

Search for an open bug titled "Main broken:" (`gh issue list --state open --label bug
--search "Main broken in:title"`).

- **One is open:** comment on it with this run's link, the failed jobs and the failing
  tests. Use that bug for the rest of the spell.
- **None is open:** file one, with no milestone (AGENTS.md, "Bugs have no milestone"),
  labels `bug`, `priority:high` and `size:S`, titled "Main broken: `<job>` since
  `<short sha>`". The body gives the run's link, the failed jobs, the failing tests or the
  build error (the lines from the log, in a fence), and the batch from the run's summary
  ("### Batch", written by `scripts/ci-batch.sh`). Write down the time of the first red
  run: step 6 counts from it.

Label the session `#<bug>` with `set_session_labels`.

## 4. Bisect the batch

1. Read the batch from the failed run's summary (the `### Batch` section, or `gh run view
   <id> --log | grep bisect-main.sh`). It gives the last green sha and this run's sha.
2. **The batch is one commit:** that commit is the bad one. Skip to step 5.
3. Else run, in your worktree (no lease: each step runs in CI):

   ```sh
   scripts/bisect-main.sh <green sha> <red sha> <failed jobs…>
   ```

   It runs about log2(n) `workflow_dispatch` runs of only the failed jobs, and prints the
   first bad commit and its pull request. Run it in the foreground with a timeout of 10
   minutes; when it needs more, run it with `run_in_background` and wait for it to end.
4. **The bisect cannot decide** (a step ends `cancelled`, or the first bad commit is the
   red sha and that commit's own PR checks show no failure like it): the break may come
   from two PRs together, or from outside the repo (a runner image, a tool's new release).
   Comment on the bug with what the bisect found, and fix forward (step 5b).

Comment on the bug with the first bad commit, its PR and the bisect's runs.

## 5. Revert first

### 5a. The revert

1. Move to a worktree of your own with `move_worktree` on a new branch
   `fix-main/revert-<pr>`. Run `git fetch origin` and `git rebase origin/main`.
2. Run `git revert --no-edit <bad sha>`. **When it conflicts:** run `git revert --abort`
   and go to 5b.
3. Set the commit's subject to "Revert #<pr>: it broke `<job>` on main (#<bug>)". The
   body says what failed (the test or the error), the run's link, and these lines:
   `mac: same, remote: same, web: same` when the reverted PR changed a client, else
   `mac: docs only, remote: docs only, web: docs only` (AGENTS.md, "Keeping the three
   clients in step").
4. Verify the failed jobs' tasks on this Mac when they can run here (`scripts/nx.sh
   affected -t gen build vet lint test`, under the "build" lease). A failure that only
   Linux has (the `go` job) shows only in CI: push anyway.
5. Call `conflict_risk_analysis`; say in the PR body what it said.
6. Push, and open the PR with `gh pr create`: the title is the commit's subject, and the
   body says `Fixes #<bug>`, the reverted PR, the failing run and the bisect. Turn on
   squash auto-merge at once (`gh pr merge --auto --squash`). Label the session
   `P#<revert pr>`.
7. **Reopen the reverted PR's issue** (the issue its body `Fixes`): `gh issue reopen`, and
   comment with the revert PR, the failing job, the run's link and the failure lines from
   the log, so the next lane lands the change again with a fix. When the PR fixed no
   issue, comment on the PR itself.
8. Go to step 6.

### 5b. Fix forward

Only when the revert conflicts (a later PR builds on the bad one), or the bisect cannot
decide.

1. Move to a worktree on a new branch `fix-main/<bug>`. Run `git fetch origin` and
   `git rebase origin/main`.
2. Fix the cause, and only the cause. Run the failed tasks on this Mac when they can run
   here, under the "build" lease.
3. Commit with the subject "Fix main: <what> (#<bug>)" and the client lines as in 5a.
4. Call `conflict_risk_analysis`, push, open the PR with `Fixes #<bug>`, and turn on
   squash auto-merge.
5. When the fix is not clear or needs a decision from a person: do not push. Comment on
   the bug with what you found, and do step 6 at once.

## 6. Wait for green, and tell Alex only when it takes long

Wait with one `wait_for_event`: `run.completed`, `where: {"workflow": "ci.yml", "branch":
"main", "anyone": true}`, together with `pr.merged`, `pr.closed` and `pr.blocked`,
`where: {"pr": <your pr>}`, with `until_minutes: 30`, and end the turn. Each time you wake:

- **Your PR is blocked:** do what its reasons say, as in the Software Engineer persona's
  step 7 (`.agents/personas/software-engineer.md`).
- **Your PR merged:** wait again for the `push` run on `main` that contains it.
- **A `push` run on `main` that contains your commit is green:** do step 7.
- **A run on `main` is red with a different failure:** it is a new break in the same
  spell. Comment on the bug, and do steps 2 to 5 for it.
- **60 minutes after the first red run, `main` is still red:** tell Alex once with
  `notify`: the title "Main still red after 60 min", the body the bug's link, the failing
  job and what you did (the revert or fix PR and its state). Do not notify again in the
  same spell. Then wait again.

Run `scripts/rearm-automerge.sh --stalled` each time you wake.

## 7. Close it

When a `push` run on `main` is green after the break:

1. Comment on the bug with the green run's link, and close it (`gh issue close <bug>
   --reason completed`) when your PR's `Fixes` did not close it already.
2. Label the session `merged` when your PR merged.
3. Remove your worktree's build output (`build/`, `.build`, `Packages/*/.build`), and
   leave the worktree with `move_worktree` and `leave_worktree: remove`.
4. Archive the session with `archive_session`. The workflow starts a new lane on the
   next red run on `main`.
