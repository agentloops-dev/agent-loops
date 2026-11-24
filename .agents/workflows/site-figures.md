---
name: Site figures
on:
  - schedule:
      at: [":00"]
      between: "05:00-05:00"
      days: [mon]
cooldown: 144h
agent: new
labels: [site, figures]
when-done: archive
permission-mode: approve-for-me
---

You keep the figures on agentloops.dev up to date (#6830; Alex chose a weekly pull request,
2026-11-22). Each Monday, with nobody watching, read three counts from this repository and,
when any has changed, land them on one small pull request. Nothing else.

1. Read the counts, each with one call (the search API's `total_count`):
   - merged pull requests: `gh api -X GET search/issues -f q='repo:agentloops-dev/agent-loops is:pr is:merged' -f per_page=1 --jq .total_count`
   - pull requests merged in the last 7 days: the same, with `merged:>=<today − 7 days, YYYY-MM-DD>` added to `q`
   - issues done: `q='repo:agentloops-dev/agent-loops is:issue is:closed reason:completed'`

   When a call fails or gives 0, stop: say so in your last message, and change nothing.
2. In `www/index.html`, the section `<section class="page proof" id="built">` has three
   `<p class="figure" data-to="N">N</p>`, in that order. Set each `data-to` to the count and
   the text to the count with a comma for thousands (`3,212`), and set the date in the
   comment above the section (`Figures from the repo's records on YYYY-MM-DD`) to today.
   Change no other line. When all three counts are the same as on the page, stop: no branch,
   no pull request.
3. Land it as the Software Engineer does (`.agents/personas/software-engineer.md`, steps 3, 6
   and 7), with no issue of its own: move into a worktree of your own with `move_worktree`,
   commit `agentloops.dev: the figures on <date> (#6830)`, ending with the line
   `mac: by design (agentloops.dev is not a client), remote: by design (agentloops.dev is not a client), web: by design (agentloops.dev is not a client)`,
   lease `ci`, push, release it, and open the pull request with
   `Approval: not needed (no new screen, control or layout: the figures only)` in its body.
   Turn on squash auto-merge with `commit_trailers` and `scripts/automerge.sh`, request no
   review, and wait for it to merge. On a conflict, `git merge origin/main` and push again.
