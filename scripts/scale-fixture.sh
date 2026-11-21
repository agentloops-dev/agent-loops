#!/bin/sh
# A scratch control plane's store with fake machines (#5282, epic #5281), for run-app and
# test-servers: writes N fake machines (default 1,000) into a store folder, before the
# scratch agents-control starts on it. They list as offline (no host is behind them), with
# mixed platforms, versions and implementations.
#
#   scripts/scale-fixture.sh STORE_DIR [N]
#
# The budgets it proves are in scripts/perf-budgets.json ("scale") and
# docs/how-to/keep-the-performance-budgets.md; Go/internal/controlplane/scalefixture_test.go
# holds the list's.
set -eu
[ $# -ge 1 ] || { echo "usage: scale-fixture.sh STORE_DIR [N]" >&2; exit 2; }
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
# go.sh runs in Go/: a relative STORE_DIR is from the caller's folder.
case $1 in /*) store=$1 ;; *) store=$PWD/$1 ;; esac
# Through go.sh, so a Mac run uses the shared, bounded caches and not Go's default one (#6462).
exec "$root/Go/scripts/go.sh" run ./cmd/scale-seed -store "$store" -n "${2:-1000}"
