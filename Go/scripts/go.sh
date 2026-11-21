#!/bin/sh
# Runs the Go toolchain for the module at Go/ (#1135). It is the Go on the PATH, on a Mac
# too (#4709): there is no container, so a run gets all of the Mac's cores. With no Go, it
# fails and says `brew install go`. On a Mac the module and build caches are in
# ~/Library/Caches/Agents-build/go (AGENTS_BUILD_CACHE moves the parent), shared by every
# worktree, as scripts/build-cache.sh shares the Swift ones; Go keys its build cache by
# GOOS, GOARCH and toolchain, so darwin and linux outputs do not mix. On CI and on Linux, Go's
# own caches. Lease "build" first.
#
#   Go/scripts/go.sh test ./...
#   Go/scripts/go.sh vet ./...
#   GOOS=linux GOARCH=amd64 Go/scripts/go.sh build -o out/agentsd ./cmd/agentsd
#
# Paths are inside Go/, from any folder (wiregen reads Packages/Wire and writes Web/ as
# `..`). On a Mac (not CI) Go runs with a clean environment, as the container gave it: only
# GOOS, GOARCH, GOFLAGS, GOBIN, CGO_ENABLED, DEVELOPER_DIR and AGENTS_RUN_PERF pass through, with HOME, USER, the
# caches and a PATH of Go and the system. A session's own PATH (a real codex before a test's
# fake one) and variables then do not reach the tests. TMPDIR is go/tmp/k<sum>-<n> in the cache: a
# folder with no symlink in its path and not under /private, as CI's macOS `test` job has
# ($RUNNER_TEMP); agentsd's tests fail under /var/folders, /tmp or /private/tmp. Cgo is off for `build` and `install`, as the binaries ship, except with
# -race, which needs it (#1505); for the rest it is Go's default, as in CI's macOS `test` job.
# CGO_ENABLED set by the caller wins.
set -e
mod=$(cd "$(dirname "$0")/.." && pwd)
# An Xcode script phase has a bare PATH: look where Go is usually installed too.
PATH="$PATH:/opt/homebrew/bin:/usr/local/bin:/usr/local/go/bin"
if ! command -v go >/dev/null 2>&1; then
	echo "go.sh: no Go on the PATH: brew install go" >&2
	exit 127
fi
cd "$mod"
if [ "$(uname -s)" != Darwin ] || [ -n "${CI:-}" ]; then
	exec go "$@"
fi
cache="${AGENTS_BUILD_CACHE:-$HOME/Library/Caches/Agents-build}/go"
mkdir -p "$cache/gopath" "$cache/gocache" "$cache/tmp"
# The TMPDIR (#6397): the same arguments get the same folder, go/tmp/k<their sum>-<n>, so
# Go's test cache hits when the same go test runs again. Go keys a test's result on each
# variable the test reads, and os.TempDir and t.TempDir read TMPDIR: with a folder for each
# pid (#5726), every go:test on the Mac ran every package again. A run holds its folder with
# lockf(1) while Go runs, so two runs at once never share one (the second takes <n> + 1),
# and the kernel frees the lock of a killed run, also after a SIGKILL. The folder is emptied
# before Go starts and removed after it ends, also on a SIGINT, SIGTERM or SIGHUP:
# t.TempDir, go-build and the tests' own folders go with it, as before.
# scripts/build-cache.sh prune removes what a killed run left after 6 hours.
key=$(printf '%s\n' "$@" | cksum | cut -d' ' -f1)
token="$$.$(date +%s)"
tmp=
clean() {
	# Only the folder this run holds. Go and some tests make files read-only.
	[ -n "$tmp" ] && [ "$(cat "$tmp.owner" 2>/dev/null)" = "$token" ] || return 0
	chmod -R u+w "$tmp" 2>/dev/null || true
	rm -rf "$tmp" 2>/dev/null || true
}
trap 'clean; exit 130' INT
trap 'clean; exit 143' TERM
trap 'clean; exit 129' HUP
cgo=${CGO_ENABLED:-}
if [ -z "$cgo" ]; then
	case ${1:-} in build | install) cgo=0 ;; esac
	for arg in "$@"; do
		[ "$arg" = -race ] && cgo=1
	done
fi
# GOROOT/bin, not /opt/homebrew/bin, where a real codex or claude may be too.
gobin=$(go env GOROOT)/bin
# The Go that go.mod names, as CI's setup-go takes it: Go fetches it once into the module
# cache, checked against its checksum database. The Mac's newer Go differs (#4709: one
# allocation bound of agentsd's fails under -race with go1.27).
toolchain=${GOTOOLCHAIN:-go$(awk '$1 == "go" { print $2; exit }' go.mod)}
set -- HOME="$HOME" USER="${USER:-}" LOGNAME="${LOGNAME:-}" \
	PATH="$gobin:/usr/bin:/bin:/usr/sbin:/sbin" GOPATH="${GOPATH:-$cache/gopath}" \
	GOMODCACHE="${GOMODCACHE:-$cache/gopath/pkg/mod}" GOCACHE="${GOCACHE:-$cache/gocache}" GOTOOLCHAIN="$toolchain" \
	${GOLANGCI_LINT_CACHE:+GOLANGCI_LINT_CACHE="$GOLANGCI_LINT_CACHE"} ${cgo:+CGO_ENABLED="$cgo"} \
	${AGENTS_RUN_PERF:+AGENTS_RUN_PERF="$AGENTS_RUN_PERF"} ${GOOS:+GOOS="$GOOS"} ${GOARCH:+GOARCH="$GOARCH"} ${GOFLAGS:+GOFLAGS="$GOFLAGS"} ${GOBIN:+GOBIN="$GOBIN"} \
	${DEVELOPER_DIR:+DEVELOPER_DIR="$DEVELOPER_DIR"} go "$@"
# Not exec: scripts/build-cache.sh prune sees Go running by this script's name, and the
# run's TMPDIR is removed after Go ends. Under the lock, the inner sh writes this run's token
# to <folder>.owner first, so an exit code of 75 (lockf's "held by another run") from Go
# itself is not taken for a held folder.
n=0
while :; do
	tmp="$cache/tmp/k$key-$n"
	status=0
	/usr/bin/lockf -k -s -t 0 "$tmp.lock" /bin/sh -c '
		d=$1 token=$2
		shift 2
		echo "$token" >"$d.owner"
		chmod -R u+w "$d" 2>/dev/null || true
		rm -rf "$d" 2>/dev/null || true
		mkdir -p "$d"
		status=0
		env -i TMPDIR="$d" "$@" || status=$?
		chmod -R u+w "$d" 2>/dev/null || true
		rm -rf "$d" 2>/dev/null || true
		exit "$status"
	' go.sh "$tmp" "$token" "$@" || status=$?
	[ "$status" = 75 ] && [ "$(cat "$tmp.owner" 2>/dev/null)" != "$token" ] || break
	n=$((n + 1))
	if [ "$n" -ge 64 ]; then
		echo "go.sh: 64 runs with these arguments hold a TMPDIR in $cache/tmp" >&2
		exit 75
	fi
done
exit "$status"
