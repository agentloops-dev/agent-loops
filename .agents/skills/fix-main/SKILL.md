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

- **One session for every spell (#5778).** A red spell starts at the first red `push` run
  on `main` and ends at the next green `push` run. The workflow sends each red run on
  `main` (`conclusion` `failure` or `timed_out`, #5775; a rerun and a bisect step too) to
  one standing session, this one (`agent: standing`). A run that comes while you work
  waits for its turn. When you already work on a spell, a new red run is part of it: add
  it to the bug (step 4), and do not start again.
- **Never archive this session.** End the turn instead: the next run starts it again. An
  archived standing session gets no more runs, and the next run starts a new one.
- **Keep no state between spells** except the "Main broken" and "Flaky test" bugs and
  your PRs. Your context can be compacted between runs: read the state again from them
  and from `gh` at the start of each run, not from what you remember.
- **Never rerun a failed job (Alex, 2026-11-15, #5754).** A rerun that passes hides a flaky test,
  and the test stays flaky in the suite. A test that fails with no cause in the batch is a
  flake: run it alone 20 times first; when it fails there, it is a bug, which you fix
  forward; when it passes, quarantine it and file a bug to fix it (step 3, #6129).
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

The event is `run.completed` for `ci.yml` on `main`, with `conclusion` `failure` or
`timed_out`: the workflow starts no agent for a green, cancelled or skipped run (#5775).
Read its `run`: `id`, `attempt`, `event`, `headSha`, `conclusion`, `url`.

Do these checks in order, and stop at the first that ends the turn. Most runs end here,
so use as few tool calls as you can. Each "end" here is: end the turn. When you work a
spell, end it with step 6's wait again, so its events still wake you. Never archive the
session (Rules).

1. **`event` is not `push`** (a `workflow_dispatch` from a bisect): it is not a red
   `main`. End.
2. **You work a spell already** (you have an open "Main broken" bug or a quarantine PR
   from an earlier turn, or your session labels name one):
   - `attempt` is 1: it is part of the spell. Read the failure (step 2.1); when it is
     the same failure, comment on the bug with the run's link and end. Else it is a new
     break or flake in the same spell: do steps 2 to 5 for it.
   - Else (an attempt that a person reran): your wait sees it already. End.
3. **`attempt` is more than 1** and no spell: a person reran a job. Treat it as the first
   red run of a spell: go on with step 2. Do not rerun it again.
4. **Else:** the event is a hint, so read the run again (`gh run view <id> --json
   status,conclusion,attempt,jobs,headSha`). This is a new spell: go on. When a newer
   `push` run on `main` is green already, do step 2 all the same: a flake that passed by
   chance stays a flake (go to step 3), and a break that a later commit fixed ends here.

While you work the spell, the events of `main`'s next runs come to your waits, and each
new red run comes to you as a new run of the workflow (check 2). An open bug titled "Main
broken:" from an earlier spell (this session stopped, or a new standing session started
after the old one was archived) is not closed by a green run: you find it in step 4 and
take it as your own.

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

**First, run the test alone, 20 times (#6129).** A test that fails under load only is a
flake; a test that fails with no load has a real fault, in the product or in the test. Two
of the three Go tests in quarantine on 2026-11-17 were not load (#6098, #6106). Run only that
test, under the "build" lease (a Linux machine is good for Go and web), in the foreground:

```sh
# Go: the package path is from Go/; -run takes the test's whole name ('^TestX$/^sub$' for a subtest).
Go/scripts/go.sh test -race -count=20 -timeout=30m -run '^TestX$' ./internal/agentsd/
# Swift (Swift Testing): it stops at the first failure.
AGENTS_QUARANTINE=0 scripts/build-cache.sh swift test --package-path Packages/AgentsKit \
  --filter '<Suite type>/<test function>' --repeat-until fail --maximum-repetitions 20
# Web (node:test has no -count): a loop that stops at the first failure.
(cd Web && for i in $(seq 20); do node --test --test-force-exit \
  --test-name-pattern='<the test name>' test/<file>.test.mjs || { echo "failed on run $i"; exit 1; }; done)
```

The Go run takes seconds to a few minutes; the Swift run about 3 minutes with a warm cache
(most of it the build); the web run seconds (after `scripts/web.sh build` when the test reads
the build). Swift and web have no `-race`: Swift's thread sanitizer (`--sanitize=thread`)
builds the package and its dependencies again with no cache, which costs more than the check
gives, and Node has no race detector. An XCTest test has no `--repeat-until`: run its
`swift test --filter` in the same loop as the web one.

- **It fails alone, in any of the 20 runs:** it is a bug, not a flake. Do not quarantine it,
  and do not use `flaky.UnderLoad`, `.flakyUnderLoad` or `flakyUnderLoad`. File one `bug`
  (or update the open one) titled by what is wrong, not "flaky" ("`TestX`: <what is wrong>"),
  with the command, how many of the 20 runs failed and the failure lines (in a fence). It is
  a break of `main` that is older than the batch, so a bisect does not help: fix it forward
  with that bug (step 5). When the fix is not clear, ask Alex (step 5.3).
- **It passes 20 of 20 alone:** it is a flake. Quarantine it (the steps below), and write in
  the bug the command and "passed 20 of 20 alone".

1. Search for an open `bug` about the same test (`gh issue list --search "<test>
   in:title,body"`). When there is one, comment with the run's link and the failure lines.
   Else file one `bug`, with no milestone and no parent (the bug lane, #4207, takes it; say so in it), the labels `bug` and `size:XS` and no priority (the quarantine
   unblocks `main`; #5692), titled by what fails, not "flaky" ("`<test>` times out
   waiting for <what>"). The body gives the job, the failure lines from the log (in a
   fence), the run's link, why it is a flake (step 2.5, and the run alone above) and the **probable cause**, with
   what the log shows. The bug stays open: its root-cause lane (step 3.7) removes the
   quarantine.
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
7. **Start a root-cause lane at once (Alex, 2026-11-17, #6037).** Start a session on the
   bug with `start_session` (on a Linux machine when the test is Go or web), or, when you
   cannot start one, ask the bug lane's lead (#4207) with `message_session` to start it.
   The lane finds the cause, fixes it and takes the quarantine out. A quarantine keeps
   `main` green, but the bug it hides stays in the product until that lane lands.
8. Go to step 6.

## 4. File a break

Search for an open bug titled "Main broken:" (`gh issue list --state open --label bug
--search "Main broken in:title"`).

- **One is open:** comment on it with this run's link, the failed jobs and the failing
  tests. Use that bug for the rest of the spell.
- **None is open:** file one, with no milestone (AGENTS.md, "Bugs have no milestone") and no parent (the
  bug lane, #4207, takes it; say so in it),
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
- **Leave it to Alex:** comment on the bug that Alex has it, unassign nothing, and end
  the spell as step 7.3 says (do not archive the session).

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
3. Remove your worktree's build output (`build/`, `.build`, `Packages/*/.build`), and go
   back to the folder's checkout with `move_worktree` and `leave_worktree: remove` (it
   takes the worktree away, and its branch when it is merged). A standing session that
   stays in an old worktree would start the next spell on a stale branch and keep its
   disk. Commit or push everything first: the move refuses uncommitted work.
4. The move starts you again in the folder's checkout: end that turn with no more steps.
   Do not archive the session. The next red run on `main` comes to this session, and its
   step 1 starts a new spell.
