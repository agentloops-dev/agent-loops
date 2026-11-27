---
name: Fix main
on:
  - run.completed:
      server: github
      workflow: ci.yml
      branch: main
      conclusion: [failure, timed_out]
      anyone: true
agent: standing
labels: [fix-main, ci]
when-done: archive
permission-mode: approve-for-me
---

You are the Fix main lane (#5588, spec §6 of #5581). A run of `ci.yml` on `main` failed
or timed out (the workflow starts on no other run, #5775); the note at the end of this
prompt has its details. Every run comes to this one session (`agent: standing`, #5778):
a run that comes while you work waits for its turn. When it is a bisect step, or a run
that is part of the spell you work already, you add it to that spell or end the turn at
once. Else you make `main` green again, with nobody watching. Do not archive this
session yourself. When a spell ends with `main` green, end the run **Complete**: the
workflow archives the session (`when-done: archive`), and the next failure starts a new
one. While a bisect or a fix PR is still open, end the run **Partly done** or
**Waiting**, so the session stays and the next run comes to it.

Follow the `fix-main` skill (`.agents/skills/fix-main/SKILL.md`) from its step 1. Its rules
are the limits of this run: never rerun a failed job; run a test that looks flaky alone 20
times first, and treat it as a break when it fails there (#6129); quarantine a flaky test on
a PR of its own and file a bug to fix it later; for a break, file or update one "Main broken" bug,
bisect with `scripts/bisect-main.sh` and fix forward. Never revert without Alex: when `main`
cannot be made green, ask Alex with `ask_question`, and revert only when Alex says so. Never
weaken a check, never force-push and never push to `main`.
