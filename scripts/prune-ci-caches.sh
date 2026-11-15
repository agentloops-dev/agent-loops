#!/usr/bin/env bash
# Keep only the cache just saved for this family and ref. Cache keys are immutable,
# so source changes otherwise leave every old build graph consuming repository quota.
# A part (a key with -part-, saved by a run that ran out of time, #5704) never deletes a full
# one (-full-): a partial cache never replaces a full one (#5740), so a family keeps at most a
# full and a part until the next full save.
set -euo pipefail

prefix=${1:?cache prefix required}
keep=${2:?current cache key required}
ref=${3:?Git ref required}

ids=$(gh cache list --key "$prefix" --ref "$ref" --limit 1000 --json id,key \
	| jq -r --arg keep "$keep" '.[] | select(.key != $keep)
		| select(($keep | contains("-part-") | not) or (.key | contains("-full-") | not)) | .id') || {
	echo "::warning::Could not list caches to prune for $ref; leaving old entries in place."
	exit 0
}

while read -r id; do
	[[ -n $id ]] && gh cache delete "$id" || true
done <<< "$ids"
