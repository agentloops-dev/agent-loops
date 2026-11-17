#!/usr/bin/env bash
# After a failed Go step in CI (#6008): print the shard summary that Go/scripts/go-test.sh wrote
# to $GO_TEST_LOG_DIR/summary.txt, and an error annotation for each shard that failed.
#
# Nx can stop reading a task's output before its end: in #6008 the last shard's log was cut
# after Nx's "Failed tasks" line, with its FAIL line in the lost part. This step runs outside
# Nx, so it always shows how each shard ended. The logs themselves go up as an artifact.
set -euo pipefail

dir=${GO_TEST_LOG_DIR:?GO_TEST_LOG_DIR is not set}
summary="$dir/summary.txt"
if [[ ! -f $summary ]]; then
	echo "No $summary: go:test did not run here, or it stopped before its summary."
	ls -l "$dir" 2>/dev/null || true
	# A shard's log can be there without a summary (the step's time limit stopped the script).
	for log in "$dir"/*.log; do
		[[ -f $log ]] || continue
		echo "== $(basename "$log"): its last 40 lines"
		tail -n 40 "$log"
	done
	exit 0
fi
cat "$summary"
awk '/^go-test.sh: shard .* exit code [1-9]/ { sub(/^go-test.sh: /, ""); print "::error::go:test " $0 }' "$summary"
