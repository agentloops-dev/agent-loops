#!/usr/bin/env bash
# The history a CI job needs, after actions/checkout of one commit (fetch-depth 1, #6965):
# the whole history of HEAD and of main (refs/remotes/origin/main), so `nx affected` finds
# the merge base and `git diff` reads every change since it; with --tags, the tags too
# (`git describe` in scripts/version.sh and scripts/skew-test.sh). It fetches no other
# branch: with fetch-depth 0 each job fetched every branch, the screenshots branch's
# 524 MB with them.
#
#   scripts/ci-history.sh           HEAD and main
#   scripts/ci-history.sh --tags    and the tags
set -euo pipefail

refspecs=("$(git rev-parse HEAD)" "+refs/heads/main:refs/remotes/origin/main")
if [[ ${1:-} == --tags ]]; then
	refspecs+=("+refs/tags/*:refs/tags/*")
fi
# A shallow clone becomes a whole one; a whole one (a rerun on the same runner) only fetches.
deepen=
if [[ $(git rev-parse --is-shallow-repository) == true ]]; then
	deepen=--unshallow
fi
git fetch --no-tags --quiet ${deepen:+"$deepen"} origin "${refspecs[@]}"
echo "ci-history: $(git rev-list --count HEAD) commits from HEAD, origin/main at $(git rev-parse --short origin/main), $(git tag | wc -l | tr -d ' ') tags"
