---
name: dead-code-review
description: Review changed web code for unreachable code since its last run with Knip (Go's and Swift's are gated at PR time, by deadcode in go:lint, #6334, and Periphery in swift-lint:lint, #6336), search Go and Swift too for compatibility code, record false positives, and remove only code proven dead. Use when the Dead code review workflow runs on its schedule (#2161, #3715), or when Alex asks for a dead-code review.
---

# Dead-code review

This is the method of the Dead-code review workflow
(`.agents/workflows/dead-code-review.md`). The workflow says what the method is for and
starts it. A person or agent can also follow it by hand. Where it says that nobody is at
the keyboard, that is true only of a workflow run.

## Rules for the whole run

- Work only in the review worktree and helper worktrees. Do not change the shared checkout.
- Never push to `main`, merge, ship, restart the app or call the live app's `daemon.sock`.
- Do not delete code on the review branch. Each language/client deletion is a separate
  helper branch and pull request. A helper must explain why the finding cannot be reached
  through reflection, generated code, a runtime entry point, a test hook or an external
  consumer. Leave uncertain findings in the report.
- Do not run Periphery: Swift's dead declarations fail `swift-lint:lint` on every PR
  (`scripts/lint.sh periphery`, #6336), so `main` has none. A Swift declaration kept on
  purpose is a line in `.periphery-allowed.txt`, with its reason, on a PR. The compatibility
  search in step 1.3 still reads Swift, as it reads Go.
- Do not run `deadcode`: Go's unreachable functions fail `go:lint` on every PR
  (`scripts/lint.sh deadcode`, #6334), so `main` has none. The compatibility search in
  step 1.3 still reads Go, because deadcode cannot see a live function that only an old
  peer's wire message reaches (#6312).
- Run Knip 6.42.1 with `npx` and the Node of `Web/.node-version` on the Mac (no container,
  #4709), with `KNIP_DISABLE_RAW_TRANSFER=1` and the npm cache under
  `~/Library/Caches/Agents-build/npm`; run it from `Web/`. Keep the
  explicit build entry points in `Web/knip.json`. Use `--include files,exports
  --include-entry-exports`; dependencies are outside this review's scope.
- Nobody is at the keyboard during a workflow run. Do not ask questions. Record any
  choice that needs Alex as `needs a decision`, with the options and evidence.

## 1. Set up and scope

1. Move to `.agents/worktrees/reviews-dead-code` through `move_worktree`, on the local
   branch `agents/reviews-dead-code`. If the branch exists, call `move_worktree` with
   worktree `agents/reviews-dead-code`: the app moves you into its worktree, or makes one
   on it. Never run `git worktree add` (#3174). Otherwise, ask the app for a new worktree
   named `reviews-dead-code`. End the turn after the move. Merge `main` in that worktree.
2. Read every file in `.agents/reviews/dead-code/`. The newest run file has
   `reviewed-through:` with the last reviewed `main` commit. If no run file exists,
   start at the last release tag.
3. Review `git diff <previous reviewed-through>..main` for web code, and Go and Swift for the compatibility search only. On the
   first run, use `git diff <last release tag>..main`. Also search current source for
   compatibility code using `before 0NN`, `older build`, `legacy` and `no longer`.
   Review only matches that are still in source; ignore docs, release notes, tests that
   describe old behavior and generated files. v0.x has no migration code.
4. Read `.agents/reviews/dead-code/baseline.md` before interpreting scanner output.
   Keep a finding only if it is new or the code or reachability has changed since the
   recorded baseline.

## 2. Review

Run both analyzers and the compatibility search in step 1.3. Do not treat a tool
result as proof by itself. Check call sites, target membership, protocol and serialization
use, App Intents, Objective-C selectors, reflection, test-only hooks and entry points.
Generated code is excluded unless a handwritten reference is dead.

For every false positive, add a durable baseline entry with the exact declaration or
path, the analyzer, and the reason it is live. Use Knip's
`ignore` only for a path with a documented reason. A Go function kept on purpose is a line
in `Go/.deadcode-allowed.txt`, and a Swift declaration a line in `.periphery-allowed.txt`,
with its reason, on a PR, not in `baseline.md`.
Include the baseline update in the report branch.

For every likely-dead finding, give severity (critical, high, medium or low), the exact
`file:line` or declaration, the analyzer output, the reachability check, and the proposed
removal. Mark a finding `sure` only when the full target set was indexed and no runtime,
generated, reflective or external entry point can use it. Otherwise mark it `uncertain`
or `needs a decision`.

## 3. Write findings and baseline

Write `.agents/reviews/dead-code/<YYYY-MM-DD>.md` with front matter:

```yaml
reviewed-through: <git rev-parse main>
base: <last release tag>
previous: <previous reviewed-through or none>
```

Record the commands, tool versions, failed or incomplete scans, compatibility search
terms, findings and baseline changes. A run with no findings still records what it read
and confirms that all targets were scanned. Append each new false positive to
`.agents/reviews/dead-code/baseline.md` with its reason. Commit the report and baseline
on `agents/reviews-dead-code`.

## 4. Remove code that is surely dead

Group `sure` findings by client or language: Mac/Host, Remote, each independent Swift
package (compatibility code only), Go (compatibility code only) and Web. Start no more than one helper for a group at a time, and stay within
the folder's session limits. Give each helper a new worktree off `main` and one group.
The helper removes only that group's code, updates any necessary tests, runs the unit
tests for touched paths, and opens one pull request with `Fixes #<finding issue>`.
The PR explains the reachability proof and includes the client parity lines if a deletion
changes what a client draws. Do not combine groups into one PR. If no helper slot is
available, record the finding for the next run. Leave low-confidence findings in the
report; do not create a removal branch for them.

## 5. Finish

Wait for each helper with `wait_for_event` naming its session. Read the helper's last
message and check its pull request and checks. Record the PR and status in the report.
Archive helpers after their PR merges or is abandoned. Commit the final report changes on
`agents/reviews-dead-code`. End with a line per finding and the path to the report.
