#!/usr/bin/env bash
# Say whether a cache's folders grew in this run, so an empty or unchanged cache is never saved
# over a full one (#5740). A run whose restore missed and whose Go tasks were all Nx cache hits
# or attested skips compiles nothing: saving then stored an empty cache (main's go-race-macOS
# was 0 MB), and every later run restored it by prefix and started cold.
#
# Usage:
#   scripts/ci-cache-grew.sh mark <name> <path>...   after the restore: record the size
#   scripts/ci-cache-grew.sh check <name> <path>...  before the save: write save=true to
#                                                    $GITHUB_OUTPUT when the size grew
# A path that does not exist counts as 0 bytes.
set -euo pipefail

mode=${1:?mark or check}
name=${2:?cache name required}
shift 2
(( $# > 0 )) || { echo "ci-cache-grew.sh: no paths" >&2; exit 2; }

kib() {
	local total=0 p n
	for p in "$@"; do
		p=${p/#\~/$HOME}
		[[ -e $p ]] || continue
		n=$(du -sk "$p" 2>/dev/null | awk '{print $1}') || n=0
		total=$(( total + ${n:-0} ))
	done
	echo "$total"
}

mark="${RUNNER_TEMP:-/tmp}/cache-size-$name"
now=$(kib "$@")
case $mode in
mark)
	echo "$now" > "$mark"
	echo "$name: ${now} KiB restored."
	;;
check)
	before=$(cat "$mark" 2>/dev/null || echo 0)
	if (( now > before )); then
		echo "$name: ${before} KiB restored, ${now} KiB now; saving."
		echo "save=true" >> "${GITHUB_OUTPUT:-/dev/stdout}"
	else
		echo "$name: ${before} KiB restored, ${now} KiB now; nothing new, not saving."
	fi
	;;
*)
	echo "ci-cache-grew.sh: unknown mode $mode" >&2
	exit 2
	;;
esac
