#!/usr/bin/env bash
# Trim Go's build and module caches before CI saves them, so they stop growing on each save
# (#5837). A run restores main's newest Go cache by prefix and saves it again with what it
# compiled, and nothing ever removed an entry: on 2026-11-16 the Linux cache was 18.3 GB on
# disk (3.5 GB saved) and the two macOS ones 11 to 12 GB (1.9 and 2.2 GB), where one run adds
# only 0.4 to 0.7 GB. With them the repo's caches went over GitHub's 10 GB limit, and GitHub
# removed the Xcode DerivedData cache, so every apps job built the Mac app cold.
#
# Go sets an entry's time when a build uses it (when it is more than an hour old), and the
# cache's tar keeps those times, so an entry's time is the last time a run used it. This keeps
# the entries that a run used in the last $GO_CACHE_HOURS hours (6), this run's among them, and
# then, oldest first, deletes entries until the build cache is under $GO_CACHE_MAX_MB on disk.
# It also deletes the module zips: a build reads the extracted module and its .ziphash, not the
# zip, and the go command downloads a zip again when it needs one.
#
# Usage: scripts/ci-go-cache-trim.sh <go-build cache dir> [module cache dir]
# Run it after scripts/ci-cache-grew.sh check, which must see the size before the trim.
set -euo pipefail

build=${1:?go-build cache dir required}
mod=${2:-}
hours=${GO_CACHE_HOURS:-6}
max_mb=${GO_CACHE_MAX_MB:-6144}

mb() { du -sk "$1" 2>/dev/null | awk '{print int($1 / 1024)}'; }

if [[ -d $build ]]; then
	before=$(mb "$build")
	# Only the entries in the 00..ff folders: README, trim.txt and testexpire.txt stay.
	find "$build" -mindepth 2 -type f -mmin "+$(( hours * 60 ))" -delete
	aged=$(mb "$build")
	if (( aged > max_mb )); then
		# Oldest first, in whole files, until the folder is under the cap. stat's format
		# differs: BSD (macOS) -f, GNU (Linux) -c.
		if stat -f '%m' "$build" >/dev/null 2>&1; then fmt=(-f '%m %z %N'); else fmt=(-c '%Y %s %n'); fi
		over=$(( (aged - max_mb) * 1024 * 1024 ))
		find "$build" -mindepth 2 -type f -print0 | xargs -0 stat "${fmt[@]}" | sort -n \
			| awk -v over="$over" '{ if (freed >= over) exit; freed += $2; sub(/^[0-9]+ [0-9]+ /, ""); print }' \
			| tr '\n' '\0' | xargs -0 rm -f
	fi
	echo "go-build: ${before} MB, ${aged} MB after removing entries unused for ${hours} h, $(mb "$build") MB saved (cap ${max_mb} MB)."
fi

if [[ -n $mod && -d $mod/cache/download ]]; then
	before=$(mb "$mod")
	find "$mod/cache/download" -type f -name '*.zip' -delete
	echo "go module cache: ${before} MB, $(mb "$mod") MB without the module zips."
fi
