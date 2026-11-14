#!/usr/bin/env bash
# Vets and tests, with -race as CI's `go` job does, only the Go packages this branch
# changes, so a lane finds a Go failure before it pushes and not in CI (#2994).
#
# The testset map, .github/testsets.json (#5583), chooses the packages: a package is changed
# when a file in its folder under Go/ changed since the merge base with origin/main,
# committed or not (a testdata file counts for the package above it). A change to Go/go.mod,
# Go/go.sum or the wire (Packages/Wire, scripts/wire.sh, the web types made from it) runs
# ./... . `scripts/checks.sh plan --worktree` says why. No Go change: nothing runs. On a Mac it is the Mac's own Go (#4709), so a failure only Linux
# has (CI's `go` job) still shows only in CI.
#
# When Go/internal/agentsd changes, its hot-path benchmarks (#4505) then run once each,
# without -race, so a change to that path shows its cost before a push; compare it with
# the baseline in Go/internal/agentsd/hotpath_bench_test.go.
#
# Lease "build" first. Usage: scripts/go-changed.sh [base]   (default origin/main)
set -euo pipefail

repo=$(cd "$(dirname "$0")/.." && pwd)
cd "$repo"
base=$(git merge-base "${1:-origin/main}" HEAD)

changed=$( { git diff --name-only "$base"; git ls-files --others --exclude-standard; } | sort -u)
# The packages come from the testset map's go-changed testsets (#5583); a package path has
# no space in it.
out=$(python3 scripts/checks.py go-packages "$base")
pkgs=()
for pkg in $out; do pkgs+=("$pkg"); done

if [ ${#pkgs[@]} -eq 0 ]; then
	echo "go-changed: no Go package changed since $(git rev-parse --short "$base")"
	exit 0
fi

echo "go-changed: ${pkgs[*]}"
Go/scripts/go.sh vet "${pkgs[@]}"
Go/scripts/go.sh test -race "${pkgs[@]}"
if grep -qE '^Go/internal/agentsd/[^/]*$' <<<"$changed" || [ "${pkgs[*]}" = ./... ]; then
	Go/scripts/go.sh test -run '^$' -bench '^Benchmark(ChangeAgent|Folders)$' -benchtime=1x -benchmem ./internal/agentsd
fi
