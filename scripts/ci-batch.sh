#!/usr/bin/env bash
# Write this run's batch on main to the job summary (#5587, spec §4 of #5581): the commits from
# the last green ci run on main (not included) to this one. main batches through its concurrency
# group, so one run can check several merges; when it fails, scripts/bisect-main.sh takes the two
# shas below and finds the first bad commit.
#
# Usage, in a job on a push to main after a checkout with fetch-depth 0:
#   env: GH_TOKEN (actions: read), GITHUB_REPOSITORY, GITHUB_STEP_SUMMARY (stdout without it)
#   run: bash scripts/ci-batch.sh
set -euo pipefail

head=$(git rev-parse HEAD)
out=${GITHUB_STEP_SUMMARY:-/dev/stdout}
green=$(gh run list --repo "${GITHUB_REPOSITORY:?}" --workflow ci.yml --branch main --event push \
	--status success --limit 1 --json headSha --jq '.[0].headSha // empty' 2>/dev/null) || green=

{
	echo "### Batch"
	echo
	if [[ -z $green ]] || ! git merge-base --is-ancestor "$green" "$head" 2>/dev/null; then
		echo "No green run on main that is an ancestor of \`${head:0:12}\`: the batch is this commit only."
		echo
		echo "- \`${head:0:12}\` $(git log -1 --format=%s "$head")"
	else
		count=$(git rev-list --first-parent --count "$green..$head")
		echo "$count commit(s) since the last green run on main (\`${green:0:12}\`). When this run fails:"
		echo
		echo "    scripts/bisect-main.sh $green $head <failed jobs…>"
		echo
		git log --first-parent --format='- `%h` %s' "$green..$head"
	fi
} >> "$out"
