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
#   GO_TEST_LOG_DIR=dir Go/scripts/go-test.sh -race  # keep each shard's log and summary.txt in dir
#
# After each shard's log, and again at the end, it prints a summary of each shard: its exit code
# and its result lines (`--- FAIL`, `FAIL`, `ok`, `panic:`, `fatal error:`, `test timed out`,
# `DATA RACE`, `race: limit`, `signal:`), and the last lines of a shard that has no result line
# or that a signal stopped (#6008). Nx can stop reading a task's output before its end, so CI
# also prints summary.txt and uploads the logs in a step of its own.
#
# Through Go/scripts/go.sh, so a Mac gets its clean environment and caches. Lease "build" first.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
go_sh="$here/go.sh"
shards=${GO_TEST_SHARDS:-3}
# The packages to split, slowest first (add one when it sets the step's time).
slow_pkgs=(./internal/agentsd)

if [[ -n ${GO_TEST_LOG_DIR:-} ]]; then
	out=$GO_TEST_LOG_DIR
	mkdir -p "$out"
	rm -f "$out"/*.log "$out/summary.txt"
else
	out=$(mktemp -d "${TMPDIR:-/tmp}/go-test.XXXXXX")
	trap 'rm -rf "$out"' EXIT
fi

slow_paths=$("$go_sh" list "${slow_pkgs[@]}")
rest=()
while IFS= read -r pkg; do
	grep -qxF "$pkg" <<<"$slow_paths" || rest+=("$pkg")
done < <("$go_sh" list ./...)

# With a log folder (CI), the machine's load, memory and swap every 30 s in load.log, so a slow
# run shows whether it waited on the cores, on memory or on neither (#7480).
sampler=
if [[ -n ${GO_TEST_LOG_DIR:-} ]]; then
	sample() {
		while :; do
			echo "== $(date -u +%H:%M:%S) load $(uptime | sed 's/.*load averages*: //')"
			if [[ $(uname) == Darwin ]]; then
				sysctl -n vm.swapusage
				vm_stat | grep -E '^(Pages (free|active|wired down|occupied by compressor)|Pageouts|Swapouts)' || true
			else
				grep -E '^(MemAvailable|SwapTotal|SwapFree):' /proc/meminfo || true
			fi
			sleep 30
		done
	}
	sample >"$out/load.log" 2>&1 &
	sampler=$!
	trap 'kill "$sampler" 2>/dev/null || true' EXIT
fi

pids=() logs=() shard_names=() codes=()
start() { # name, then the go test arguments
	local log="$out/$1.log"
	shard_names+=("$1")
	shift
	"$go_sh" test "$@" >"$log" 2>&1 &
	pids+=("$!")
	logs+=("$log")
}

# The lines that say how a shard ended. `go test` prints them at the start of a line, except
# the race detector's and a timeout's, which can follow other output.
result_lines='^(--- FAIL|FAIL|ok |panic:|fatal error:|signal: )|test timed out|WARNING: DATA RACE|race: limit'

# One shard's summary: its exit code, its result lines, and when it has no `ok` or `FAIL`
# result line, or a signal stopped it (exit code over 128), its last 40 lines.
shard_summary() { # index
	local i=$1 log=${logs[$1]} code=${codes[$1]}
	echo "go-test.sh: shard ${shard_names[$i]} exit code $code"
	grep -E "$result_lines" "$log" | sed 's/^/  /' || true
	if ((code > 128)) || { ((code != 0)) && ! grep -qE '^(FAIL|ok )' "$log"; }; then
		echo "  the shard has no result line; its last 40 lines:"
		tail -n 40 "$log" | sed 's/^/  | /'
	fi
}

# The rest first, so its build and tests run while the slow packages' test binaries build for
# `-list` (on CI's macOS runner that build took 1.5 to 4 minutes before a shard could start).
start rest "$@" "${rest[@]}"

n=0
for pkg in "${slow_pkgs[@]}"; do
	# The test binary is built here, with the same flags, so the shards find it in the cache.
	if ! list=$("$go_sh" test "$@" -list '.*' "$pkg"); then
		wait "${pids[@]}" || true
		cat "${logs[@]}"
		exit 1
	fi
	names=$(grep -E '^(Test|Example|Fuzz)' <<<"$list" || true)
	if [[ -z $names ]]; then
		echo "go-test.sh: $pkg lists no tests" >&2
		wait "${pids[@]}" || true
		exit 1
	fi
	listed=$(wc -l <<<"$names" | tr -d ' ')
	dealt=0
	for ((s = 0; s < shards; s++)); do
		part=$(awk -v s="$s" -v k="$shards" 'NR % k == s' <<<"$names" | paste -sd '|' -)
		[[ -z $part ]] && continue
		dealt=$((dealt + $(tr '|' '\n' <<<"$part" | wc -l)))
		n=$((n + 1))
		start "$(basename "$pkg")-$n" "$@" -run "^($part)\$" "$pkg"
	done
	if ((dealt != listed)); then
		echo "go-test.sh: $pkg listed $listed tests but dealt $dealt" >&2
		wait "${pids[@]}" || true
		exit 1
	fi
done

failed=0
for i in "${!pids[@]}"; do
	code=0
	wait "${pids[$i]}" || code=$?
	codes[i]=$code
	((code == 0)) || failed=1
	cat "${logs[$i]}"
	shard_summary "$i"
done

if [[ -n $sampler ]]; then
	kill "$sampler" 2>/dev/null || true
	wait "$sampler" 2>/dev/null || true
fi

# Again, all together at the end, and in summary.txt for CI's own step.
{
	echo "go-test.sh: summary of ${#pids[@]} shards"
	for i in "${!pids[@]}"; do
		shard_summary "$i"
	done
} | tee "$out/summary.txt"
exit "$failed"
