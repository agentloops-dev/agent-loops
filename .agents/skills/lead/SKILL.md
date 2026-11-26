---
name: lead
description: Lead one epic from start to end (#3265) - read its plan (the sub-issues and their "blocked by" links), start a lane for each child whose blockers are closed, wait on events, file the new children that the lanes find, and close the epic with a summary when all its children are closed. A standing epic (#3659) has a query as its plan and never closes. Writes no code. Use when a session is started on an issue labelled `epic` (Start lead in Work, the Lead persona, or Alex), or when Alex asks for a lead on an epic.
---

# Lead

You lead one epic: a parent issue labelled `epic` whose sub-issues are its plan
(AGENTS.md, "Plans", `specs/3265-work-plans/spec.md`). You start the lanes that do its
children, in the order that its "blocked by" links give. You own the plan: you keep it
true while the work goes on. You write no code and decide no features: Alex and the
Product Owner do.

An epic with the label `standing` as well as `epic` is a **standing epic** (#3659,
`specs/3659-standing-epics/spec.md`): its plan is a query in its body, not sub-issues,
and it never closes. Do steps 1 to 4 as "Standing epics", below, says. Never do step 5.

## Starting a lead

Autopilot (when it is on for the folder, #5897), Start lead in Work, Alex, or the release (for the bug lane) starts the lead with one
session in the epic's folder, with the Lead persona (`.agents/personas/lead.md`). Its
title is `#<epic>: <epic title> lead`, its labels `#<epic>` and `lead`, no worktree, and
its prompt is this one:

> #<epic>: <epic title>
>
> Lead this epic: <epic URL>

A lead that Autopilot starts has the same title, labels and prompt. When Autopilot starts
it again because the epic is open and the lead before it ended, stopped or was archived
(#5901), the prompt names that lead, and this lead reads it first (`read_session`) to
take up where it stopped.

Only one lead per epic: if a session labelled `#<epic>` and `lead` is not archived, do
not start a second one.

## Rules for the whole run

- **Write no code.** Do not make a branch, worktree, commit or push, and do not open,
  rebase or merge a pull request. Each child's lane does that.
- **Change only the plan.** You can file, label and comment on issues, add and remove
  sub-issues, set "blocked by", and file a new epic for follow-ups (step 4). Do not change the epic's goal or a child's scope: when that
  must change, ask Alex.
- **Follow AGENTS.md** for "Questions that already have an answer" and "Decide and
  tell". The order of work is `forge/next` ("The order", below). The lead does not
  count as a lane; its lanes count towards the folder's lane and open-PR limits.
- **Places:** the folder's limits give places, not another agent. Start no new lane
  while more than the folder's open-PR limit of pull requests are open: land first. A PR
  with a review requested from Alex (`gh pr view <n> --json reviewRequests`) does not
  count: it waits for Alex, and its lane holds no running place.
- **Wait with `wait_for_event`, never by polling.** To wait on lanes themselves, wait on
  `agent.ended` alone, with `agent` their ids and `wake_on: any`.
- **Name people in questions:** "Claude (the epic's lead) will…", "Alex, do you want…?".

## 1. Read the plan

Use `forge/plan` when the daemon has it (#3267). Until then, read it with `gh`:

```zsh
epic=<n>
gh issue view $epic --json title,state,labels,milestone,body
gh api repos/agentloops-dev/agent-loops/issues/$epic/sub_issues --paginate \
  -q '.[] | "\(.number) \(.state) \([.labels[].name]|join(","))  \(.title)"'
# for each open child:
gh api repos/agentloops-dev/agent-loops/issues/<child>/dependencies/blocked_by \
  -q '.[] | "\(.number) \(.state)"'
```

- A child is **ready** when all its blockers are closed. The ready children are the
  current step; they can run at the same time.
- A blocker that is not a child of this epic (⊘) is a blocker all the same. Do not start
  its lane: it is another lead's or lane's issue. Say it in your report.
- Look for each child's session and PR: a session labelled `#<child>` (`list_sessions`)
  or an open PR that fixes it (`gh pr list --search "<child> in:body"`). Such a child is
  taken (AGENTS.md, "An issue is taken"): do not start a second lane on it.

## The order: `forge/next`

The order of work is the one order of `forge/next` (#5689, #5692), the same order as Work's
**Next**: not on hold first; then priority `high`, `medium`, none, `low`; then
size `XS`, `S`, `M`, `L`, `XL`, no size last (#7033); then the oldest (then the
lowest number). An epic has no size. Do not work out an order of your own from prose. `forge/next` with `epic`
gives the epic's children that are ready (open, no open blocker, not taken, no assignee),
and those on hold, in that order, with the reasons for each place; a standing epic's
query comes in the same order. Its top row that is not on hold is the next one.

Until the lead can call `forge/next`, read the ready children (step 1) and sort them in
the same order with `gh` and `jq`. This reads the labels only; the daemon also reads
GitHub's **Priority** issue field where the repo has one (#5747):

```zsh
gh issue list --limit 100 --json number,title,labels,createdAt \
  --search "is:open -label:epic parent-issue:agentloops-dev/agent-loops#$epic" | jq -r '
  def rank: ([.labels[].name] as $l
    | if ($l|index("priority:high")) then 0 elif ($l|index("priority:medium")) then 1
      elif ($l|index("priority:low")) then 3 else 2 end);
  def size: [.labels[].name | ascii_downcase | select(startswith("size:")) | .[5:] as $s
    | ["xs","s","m","l","xl"] | index($s) | select(. != null)] | max // 5;
  map(. + {hold: ([.labels[].name|ascii_downcase]|any(. == "on hold" or . == "on-hold"))})
  | sort_by([.hold, rank, size, .createdAt, .number])
  | .[] | "\(.number)\(if .hold then " (on hold)" else "" end) \(.title)"'
```

For a standing epic, put its query in `--search` in place of `parent-issue:…`.

## 2. Start the lanes

For each free place, take the top row of `forge/next` for this epic that is not on hold
(#5692, "The order", above), and do these steps for it. Never start a lane on a row that is
on hold: it sorts below every other row, and waits for Alex.

1. Assign it: `gh issue edit <child> --add-assignee alexec`.
   Before you start a lane on a child whose work is on a branch or an open PR, call
   `conflict_risk_analysis` with `diff` the branch and `depends_on` the blocker PRs (#5473).
   On `wait for #N`, hold the lane until #N lands (wait on its `pr.closed`); on `stack on
   #N`, tell the lane to base on #N's branch (#1777). A child with no branch yet has nothing
   to run: the lane runs it itself. It is advice, never a gate.
2. Start its lane with `start_session`, with the prompt and labels from
   `.agents/personas/software-engineer.md`. The first line of the prompt is
   `#<child>: <title>`, then a blank line and the persona's instructions; put nothing
   before the issue number and title. Title `#<child>: <title>`, worktree
   `fix-github-issue-<child>` (a bug) or `work-github-issue-<child>` (anything else), no
   runtime. Add to the prompt: on a conflict with `main`, `git merge origin/main` and push
   without running the tests again (#5712); lease `ci` only before the PR's first push, not
   before a push that fixes a red check or settles a conflict (Alex, 2026-11-26, #7243).
   A lead runs on any machine; it writes no code and holds no build lease (#5877).
   **A `linux-ok` child** (AGENTS.md, "Plans", #5810) goes to a Linux machine when one is
   free: call `list_machines`, and when a Linux machine is online, pass it as `machine` to
   `start_session`. This keeps the Mac's build lease free for the Swift lanes. When no
   Linux machine is online, or its start fails, start the lane on the Mac, as before. A
   child without `linux-ok` always runs on the Mac.
3. If `start_session` refuses (the folder's limits are full), unassign the child and
   start no more until a lane ends.
4. After the epic's first lane starts, ask `estimate_issue` for the epic once. This keeps
   the epic's estimate on its node. Then, when the epic's spend passes 2× and then 3× that
   estimate, Alex gets a notice on your session, once for each (#4529). Do not post
   anything for it yourself: the notice is enough. When Alex answers in your chat (stop,
   trim or go on), do that.

**A spike** can be yours: when its question is small and needs no code (read the code,
the docs or the forge), settle it yourself. Write the decision in the spike's issue
(`Decided: <X>, because <Y>. Alex can overrule.` for an easy-to-undo choice; ask Alex
with a form for the rest), then close it. Otherwise give it a lane as above.

## 3. Wait

End each pass with one `wait_for_event` call: `events: ["issue.closed", "pr.merged",
"pr.blocked", "issue.parented"]`, `where: {"issue": [<open children>], "fixes": ["#<child>",
…], "parent": <epic>}`, `until_minutes: 60`. Then end your turn. The wait names this epic's children (#3797):
`issue` for `issue.closed`, and `fixes` for the PR events, which match a PR whose body
closes one of them, so you know the child before its lane opens the PR. Each event takes
only the keys it has. A wait that names nothing is refused. When you start again:

- **`issue.parented`** (#5945: an issue became a sub-issue of this epic; `parent` is the
  epic's number): read the plan again (step 1) and start the new child's lane when it is
  ready (step 2). `issue.parented` takes no `issue`, so the list of open children does not hide a new
  child from it. An app older than #5945 refuses `issue.parented` ("No server here offers"): leave it
  out and wait as before, reading the plan at the time limit.
- **`issue.closed` or `pr.merged`** of a child: read the plan again (step 1) and start
  the lanes of the children that are now ready (step 2).
- **`pr.blocked`** on a child's PR (#5321: a conflict, a failed required check, changes
  requested, auto-merge off; its `reasons` say which): its lane fixes it. If the lane
  has ended or stopped, message it about its own PR (`message_session`), never to widen
  its work. Leave a PR with a review requested from Alex alone: it waits for Alex. A failed
  check that main also fails, with the same test, is not the lane's to fix: the lane
  waits for main to go green, then runs its checks again (`fix-failed-checks`, step 2a,
  #5589). Do not message the lane, and do not start a lane to fix main: the Fix main
  lane does it (#5581, §6). Wait again.
- **The wait is refused** with "No server here offers pr.blocked" (#5437: the
  installed app is older than #5321): wait instead on `["issue.closed", "pr.merged",
  "checks.failed", "pr.conflicted"]`, with the same `where`, and use that list for the
  rest of this session. `checks.failed` and `pr.conflicted` on a child's PR are
  `pr.blocked` above.
- **An event that is not about this epic** (a PR that fixes a child and something else):
  wait again.
- **The time limit:** run `scripts/rearm-automerge.sh --stalled`, read the plan again,
  and wait again.

## 4. Keep the plan true

- **New work that a lane finds** (a missing part, a bug in the epic's work): file it as a
  new child. Look for a duplicate first (`gh issue list --search`). Give it
  its level (`story`, `task`, `spike`, or `bug`), its type label, the epic's `feature:`
  label, its `size:`, a priority (AGENTS.md, "Set a priority when you file"; never `on hold`), and the
  epic's milestone (a bug gets none, unless it blocks the milestone's goal). Then add it to the epic and set what it waits for, also in another
  epic. Work that this epic does not need goes to the epic it serves, or, with none, is an
  orphan (AGENTS.md, "Every issue has an epic"): say in it which standing epic takes it.

  ```zsh
  id=$(gh api repos/agentloops-dev/agent-loops/issues/<new> -q .id)
  gh api -X POST repos/agentloops-dev/agent-loops/issues/$epic/sub_issues -F sub_issue_id=$id
  blocker=$(gh api repos/agentloops-dev/agent-loops/issues/<blocker> -q .id)
  gh api -X POST repos/agentloops-dev/agent-loops/issues/<new>/dependencies/blocked_by -F issue_id=$blocker
  ```

- **A follow-up at the end of the epic** (#7037; "come back and validate in a few days"):
  do not hold the epic open for it. One issue goes to the standing epic #4208: file it
  with no parent. (A bug goes to the bug lane, #4207, as any bug with no parent does.)
  Several go to a new epic, with their own children, that is "blocked by" this epic.
  A child that turns out to be a follow-up moves there too: take it out of this epic
  with `gh api -X DELETE repos/agentloops-dev/agent-loops/issues/$epic/sub_issue -F sub_issue_id=$id`.
- **A port to the other clients** is in its own epic, "blocked by" this one, or is one
  issue in #4208 (the `epic-lifecycle` skill, "Which epics"). It is not a child of the
  MVP epic. Do not start a port epic's lanes before the MVP epic is closed.
- **A child that is no longer needed:** close it as not planned, with one line that says
  why, and remove the "blocked by" links that point to it.
- **A child that is stuck** (its lane asks Alex, or its PR waits for approval): it is not
  yours to answer. Say it in your report and go on with the other children.

## 5. Close the epic

When all the children are closed (for an MVP epic, when stage 8 of the `epic-lifecycle`
skill is done too; do not wait for its port or a follow-up):

1. Close the epic: `gh issue close $epic --reason completed`.
2. Ask `estimate_issue` for the epic (#3590). Closed, it says the estimate the epic had
   when its work started against what it took, in one line: "Estimated $X, Zh of work;
   actual $A, Hh of work".
3. Post one comment on the epic, starting with `<!-- agent -->` when you post as `alexec`
   (AGENTS.md, "Who an agent posts as"): a table of each child,
   its PR, and what it cost (from each lane's session, `read_session`), then the total.
   Then that line, as `estimate_issue` says it; when it says no estimate was kept, say
   that. Say what was left out and where it went.
4. Archive the lanes you started that are not archived yet (their work is merged or
   closed), then archive yourself with `archive_session` and no id.

## Standing epics

A standing epic is a work stream, such as the bug lane (#4207) or the lane for every
other issue with no parent (#4208; #1570, #3212, #4335, #7033). Its body has one `standing` block:

````markdown
```standing
labels: bug
parent: none
lanes: 1
```
````

**Read the plan (step 1).** `forge/plan` gives the query's issues as the plan's children,
in order, and the block as `standing` (#3661). Until the lead can call it, read the
query with `gh`. Change the block into a search: `labels: a, b` is `label:a label:b`,
`any: a, b` is `label:a,b`, `not: a` is `-label:a`, `milestone: none` is
`no:milestone`, `milestone: <name>` is `milestone:"<name>"`, `parent: none` is
`no:parent-issue` (#4335).

```zsh
gh issue view $epic --json body -q .body   # the standing block
gh issue list --limit 100 --json number,title,labels,assignees \
  --search "is:open -label:epic label:bug no:parent-issue"
```

- **The order:** the one order of `forge/next` ("The order", above): on hold last, then
  priority, size and the oldest. In the bug lane (#4207), a bug that
  names a quarantine (`flaky.UnderLoad`, `.flakyUnderLoad` or `flakyUnderLoad`) goes
  before the other bugs of its priority (Alex, 2026-11-17, #6037): while it waits, the
  bug it hides stays in `main`.
- **Ready** is an issue with no open blocker (`dependencies/blocked_by`, as in step 1).
- **No block, a second block or an unknown field** (`forge/plan` says it in
  `standing.error`): start no lanes. Say it in a comment on the epic once, and ask the
  Product Owner or Alex to fix the block. Wait again.

**Start the lanes (step 2).** Run at most `lanes` lanes at once (the block's `lanes:`,
else 1), and never more than the folder's lane limit. Count the lanes you started that
have not ended. Take the ready issues from the top of the order that are not taken, as
step 2 says, until that count is `lanes`, in the order of `forge/next`, and never one on
hold. When places run short,
a standing epic's lane waits before another epic's lane (AGENTS.md): start no new
lane while another (not standing) epic's lane is queued, and start a paused one again only when a
lane ends.

**Wait (step 3).** Also wake when new work comes into the query. One `where` serves all
the events of a wait, and each event takes only the keys it has. So name the lanes'
PRs with `fixes`, and the query with `label`: the block's first `labels:` label, else its first `any:` label, else the type label that most of its issues have (#4208 names neither:
`enhancement`). Do not set `issue`: it would narrow `issue.labelled` to the
lanes' own issues. A wait takes one label, so the time limit catches the issues of the
other labels.

```json
{"events": ["pr.merged", "pr.blocked",
            "issue.opened", "issue.labelled", "issue.closed"],
 "where": {"fixes": ["#<issue of a lane>", …], "label": "bug"},
 "until_minutes": 60}
```

With no lane running, `fixes` names nothing: leave it out, and `label` names the wait.

When you start again:

- **`issue.opened` or `issue.labelled`:** read the query again. When the issue is in it
  and a lane place is free, start its lane. Else wait again.
- **`issue.closed` or `pr.merged`:** read the query again and fill a free place. An
  issue closed that no lane had changes nothing more.
- **An empty query:** start nothing and wait again. An empty query is not the end of the
  epic.

**Keep the plan true (step 4).** A new issue that a lane finds is not a sub-issue. File
it with the labels that put it in the query (a bug: `bug`, no parent), or, when an epic
fits, as a sub-issue of that epic. Do not add sub-issues to a standing epic. Do not change its block:
the query is the Product Owner's and Alex's.

**Never close it.** Do not close a standing epic, also when its query is empty and no
lane runs. Do not post a closing summary. Stay until Alex stops you. When you are stopped, archive the lanes you started whose work is merged or closed.

## Reports

When Alex asks, or when you end a turn with something to say, report in one short
table: child, level, state, lane, PR. Then the total of done and running children.
