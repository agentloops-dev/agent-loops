---
name: fix-failed-checks
description: Find why a pull request's checks or a merge request's pipeline failed, fix it on its branch and push, or run the failed jobs again when the failure is not caused by the change. Use when the Fix failed checks workflow starts on checks.failed, when a lane's pr.blocked names required_check_failed, or when someone asks you to fix failed checks.
---

# Fix failed checks

A pull request's checks (GitHub, Bitbucket, Forgejo) or a merge request's pipeline (GitLab) failed.
You find why, and fix it on its own branch. The event's server says which forge: use the
`github`, `gitlab`, `bitbucket` or `forgejo` server's tools, not `gh` or `glab`.
On GitLab a merge request is `pr` in the event, and its number is its iid (`!12`).

| Step | GitHub (`github` server) | GitLab (`gitlab` server) | Bitbucket (`bitbucket` server) | Forgejo or Gitea (`forgejo` server) |
|---|---|---|---|---|
| Read the failure | `get_job_logs` with `run_id` (the event's `run.id`) and `failed_only: true` | `failed_log` with `pipelineId` (the event's `run.id`) | `failed_log` with `pipelineId` (the event's `run.id`) | `failed_log` with `runId` (the event's `run.id`) |
| See the change | `pull_request_read` (`method: get_diff`) | `git diff` of the branch against its target, after step 3 | `git diff` of the branch against its target, after step 3 | `git diff` of the branch against its target, after step 3 |
| Run the failed jobs again | `actions_run_trigger` (`method: rerun_failed_jobs`, `run_id`) | `retry_pipeline` with `pipelineId` | `retry_pipeline` with `pipelineId` (a new run: every step runs again) | `retry_pipeline` with `runId` |
| Comment | `add_issue_comment` on the pull request | `comment_on_merge_request` | `comment_on_pull_request` | `comment_on_pull_request` |

On Bitbucket, a `run.id` of 0 is a check from another CI than Pipelines: `failed_log` and
`retry_pipeline` cannot read or run it. Read its page (`get_checks` gives the address), and
comment when you cannot get to the log. On Forgejo the same is true of a check from another
CI than Forgejo Actions (Woodpecker, say). `failed_log` needs Forgejo 16 or Gitea 1.25, and
`retry_pipeline` Forgejo 17 or Gitea 1.26; on an older server each says so and gives the
run's address.

A lane that waits on `pr.blocked` (#5321) comes here for the reason
`required_check_failed`. That event has no `run.id`: take it from each of its
`failedChecks`' `url` (on GitHub, `…/actions/runs/<run.id>/job/<job id>`), and use the
event's `pr` and your own branch.

0. **Stop at once when the pull request is labelled `needs-approval`** (#2811). It is a UI
   enhancement that waits for Alex's approval, and its checks are fixed only after Alex
   approves it. Do not comment.
1. **Read the failure.** Use the tools in the table.
2. **Decide.** When the failure is not caused by the change (a timeout, a runner or
   network problem), run the failed jobs again, comment to say why, and stop. When the
   default branch fails the same test, do step 2a, not a rerun.

   **2a. The default branch is red with the same test (#5589).** Merges go on while main
   is red, and the Fix main lane makes main green again (#5581, §6). Do not fix it on
   your branch, and do not rerun at once: the rerun fails again.
   1. **Compare.** Read the newest finished run on the default branch of the same
      workflow (`actions_list`, `method: list_workflow_runs`, `branch: main`, or `gh run
      list --branch main --workflow <workflow> --limit 3 --json
      databaseId,headSha,conclusion,status`). It is the same failure only when that run
      failed the same job with the same test (or the same build error), in its log. When
      it did not, the failure is yours: go to step 3.
   2. **Tell the pull request.** Comment once: "Main fails `<job>` with the same test
      (`<test>`, run <link>). Waiting for main to go green, then running the failed
      jobs again (#5589)."
   3. **Wait.** Use `wait_for_event` on the forge server's `run.completed`, `where:
      {"branch": "main"}`, `until_minutes: 60`, and end your turn. A lane in step 7 of
      its persona waits on this beside its own `pr.merged`, `pr.closed` and
      `pr.blocked` (with `pr` only in their `where`: `branch` on a PR event means the
      PR's own branch). When it wakes, read the default branch's newest finished run of
      the workflow again: the event is a hint. Still red with the same test: wait again.
      Red with another failure, or not finished: wait again.
   4. **Run the checks again once main is green.** When main's run went green at the
      same commit as the red one (a flake), run the failed jobs again. When it went green
      at a newer commit (a revert or a fix), a rerun tests the old merge commit and
      fails again: rebase the branch onto the default branch and push (never a
      force-push of someone else's work; `gh pr update-branch <n>` when the branch is
      not yours), then run `scripts/rearm-automerge.sh <pr>`.
   5. **When it fails again** after main is green, the failure is the change's: go to
      step 3. When main is still red 60 minutes after you began to wait, wait again;
      the Fix main lane tells Alex (#5581, §6 step 7).
3. **Get the branch.** Use the app's `move_worktree` tool to move into a worktree on the
   branch (the event's `pr.branch`). When you do not have that tool, use
   `git worktree add` beside the folder's folder. Pull it first, so that you have its
   newest commit.
4. **Fix it.** Change only what makes the check pass. Never turn off, skip or weaken a
   check or a test to make it pass. Run the check that failed, on your computer, when you
   can.
5. **Push.** Commit with a message that says which check you fixed, and push to the same
   branch. Never force-push. Before the push, take the lease `ci`, push, and release it (AGENTS.md, #5712;
   the lease waits for an empty CI queue, #5709). On a conflict with `main`, `git merge origin/main` and push
   without running the tests again.
6. **Tell the pull request or merge request.** Comment to say what failed and what you
   changed.

When the fix is not clear, or it needs a decision from a person, do not push. Comment to
say what you found, and stop. Never merge, approve or close the pull request or merge
request.
