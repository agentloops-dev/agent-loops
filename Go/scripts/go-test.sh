#!/usr/bin/env bash
# go:test (#5719): `go test <flags> ./...` over the module, with each slow package's tests split
# into shards that run beside the rest, each its own `go test -run '^(A|B|…)$'` process.
#
# `go test` runs packages side by side but a package's tests one after another (only one of
# internal/agentsd's 1,300 tests calls t.Parallel). On CI's macOS runner internal/agentsd alone
# took 7.3 of the step's 9.8 minutes, the other packages were done in about 2, and the cores
# stood idle for the rest. Every test still runs once: the shards are the package's own
# `go test -list` at this commit, dealt out in turn, and a name that no shard ran is an error.
#
#   Go/scripts/go-test.sh -race            # what go:test runs (Go/project.json)
#   GO_TEST_SHARDS=1 Go/scripts/go-test.sh # no split
#
# Through Go/scripts/go.sh, so a Mac gets its clean environment and caches. Lease "build" first.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
go_sh="$here/go.sh"
shards=${GO_TEST_SHARDS:-3}
# The packages to split, slowest first (add one when it sets the step's time).
slow_pkgs=(./internal/agentsd)

out=$(mktemp -d "${TMPDIR:-/tmp}/go-test.XXXXXX")
trap 'rm -rf "$out"' EXIT

slow_paths=$("$go_sh" list "${slow_pkgs[@]}")
rest=()
while IFS= read -r pkg; do
	grep -qxF "$pkg" <<<"$slow_paths" || rest+=("$pkg")
done < <("$go_sh" list ./...)

pids=() logs=()
start() { # log, then the go test arguments
	local log=$1
	shift
	"$go_sh" test "$@" >"$log" 2>&1 &
	pids+=("$!")
	logs+=("$log")
}

# The rest first, so its build and tests run while the slow packages' test binaries build for
# `-list` (on CI's macOS runner that build took 1.5 to 4 minutes before a shard could start).
start "$out/rest.log" "$@" "${rest[@]}"

n=0
for pkg in "${slow_pkgs[@]}"; do
	# The test binary is built here, with the same flags, so the shards find it in the cache.
	if ! list=$("$go_sh" test "$@" -list '.*' "$pkg"); then
		wait
		cat "${logs[@]}"
		exit 1
	fi
	names=$(grep -E '^(Test|Example|Fuzz)' <<<"$list" || true)
	if [[ -z $names ]]; then
		echo "go-test.sh: $pkg lists no tests" >&2
		wait
		exit 1
	fi
	listed=$(wc -l <<<"$names" | tr -d ' ')
	dealt=0
	for ((s = 0; s < shards; s++)); do
		part=$(awk -v s="$s" -v k="$shards" 'NR % k == s' <<<"$names" | paste -sd '|' -)
		[[ -z $part ]] && continue
		dealt=$((dealt + $(tr '|' '\n' <<<"$part" | wc -l)))
		n=$((n + 1))
		start "$out/$n.log" "$@" -run "^($part)\$" "$pkg"
	done
	if ((dealt != listed)); then
		echo "go-test.sh: $pkg listed $listed tests but dealt $dealt" >&2
		wait
		exit 1
	fi
done

failed=0
for i in "${!pids[@]}"; do
	wait "${pids[$i]}" || failed=1
	cat "${logs[$i]}"
done
exit "$failed"
