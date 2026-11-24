#!/bin/bash
# The linters (#1224): each one fast, with a fix mode, and only its bug and security rules on.
#
#   scripts/lint.sh [linter…]         lint the whole tree; with none named, every linter (on a
#                                     Mac, those for the branch's files: below)
#   scripts/lint.sh --all             every linter, on a Mac too
#   scripts/lint.sh --fix [linter…]   run each linter's fix mode first, then lint
#   scripts/lint.sh --baseline        write the go, swift and dupes baselines again (below)
#   scripts/lint.sh --tools DIR TOOL…  put the pinned binaries in DIR (CI): shellcheck,
#                                     golangci-lint, govulncheck and deadcode (need Go), biome,
#                                     swiftlint, jscpd;
#                                     Linux x86_64 on Linux, macOS arm64 on a Mac
#
# Linters: shell (shellcheck), zsh (zsh -n), go (golangci-lint), govulncheck, deadcode, web (biome lint), knip (dead web code),
# swift (SwiftLint), dupes (jscpd), checks (our own text checks). Their rules are in
# .shellcheckrc, Go/.golangci.yml, Web/biome.json, Web/knip.config.js and .swiftlint.yml. There is no Python
# linter (#5645).
#
# On a Mac (not CI) with no linter named, only the linters for the files the branch changes
# since its merge base with origin/main, committed or not, run (#4710): go, govulncheck and deadcode for
# Go/ or Packages/Wire/, swift for *.swift or .swiftlint.yml, web and knip for Web/, shell and zsh for
# a shell file or .shellcheckrc; checks and dupes always. A change to this script runs every
# one. It says which it skipped.
#
# Nx runs the linters, one target per language (#5672), as `scripts/nx.sh affected -t lint`
# does and CI does: go:lint (go, govulncheck, deadcode), web:lint (web and knip, beside tsc and its own checks),
# repo-lint:lint (shell, zsh, checks, dupes; lint/project.json) and swift-lint:lint (swift, in
# CI's macOS `swift-lint` job). Each is cached by its inputs, so a lint that passed at the same
# files is not run again.
#
# Every linter fails on a finding (#5672): there is no linter that only reports. A linter
# whose tool cannot be got fails too, rather than pass with nothing run. With LINT_BASE set
# (CI: the pull request's base), a linter also says which of its findings are in the files
# changed since LINT_BASE.
#
# go (golangci-lint, every rule in Go/.golangci.yml), swift (SwiftLint) and dupes (jscpd) fail
# against a baseline (#5645, #5672): the findings that main had when they were made to fail are
# listed in Go/.golangci-baseline.txt, .swiftlint-baseline.json and .jscpd-baseline.txt, and
# only a finding not in its baseline fails. Go's baseline has no line numbers (path, message
# and rule, one line per finding), so a finding that only moves stays in it; SwiftLint's is
# its own --write-baseline; jscpd's is the pair of files of each clone, one line per clone
# (scripts/jscpd-report.py). Fix an old finding, then run `scripts/lint.sh --baseline` to drop
# it from the list; never add a new one to it by hand. go is one run (#2162): every package is
# loaded once. govulncheck fails on a vulnerability that the code calls (its default mode) and
# that Go/.govulncheck-excluded.txt does not name with its reason, deadcode on any function
# that no main package or test reaches (#6334; below, run_deadcode) and that
# Go/.deadcode-allowed.txt does not name with its reason (an allowlist, never a baseline),
# web (Biome) on any finding of the rules in Web/biome.json,
# and knip on any unused file, export, type or dependency of the web app (#6337; below, run_knip) that
# Web/knip.config.js does not allow with its reason (an allowlist too).
#
# Every linter runs on the host, never in a container (#4709). On Linux a linter on the PATH
# is run as it is (CI, after --tools). On a Mac each one is the pinned macOS binary in
# ~/Library/Caches/Agents-build/lint-tools/<tool>-<version>/, which lint.sh gets with
# --tools the first time; govulncheck is built with the Mac's Go, and zsh is the Mac's own.
# Go's caches are Go/scripts/go.sh's, in ~/Library/Caches/Agents-build/go; golangci-lint's is
# go/lint there. On a Mac, golangci-lint and govulncheck read the Go files as CI's go job
# does: GOOS=linux, GOARCH=amd64, cgo off.
set -u

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root" || exit 1

SHELLCHECK=0.11.0
GOLANGCI=2.15.0
GOVULNCHECK=1.9.0
DEADCODE=0.53.0 # golang.org/x/tools/cmd/deadcode
BIOME=2.5.15
SWIFTLINT=0.65.1
JSCPD=5.4.2
ZSH=5.9
CACHE=${AGENTS_BUILD_CACHE:-$HOME/Library/Caches/Agents-build}

ALL="shell zsh go govulncheck deadcode web knip swift dupes checks"
GO_BASELINE=Go/.golangci-baseline.txt
SWIFT_BASELINE=.swiftlint-baseline.json
DUPES_BASELINE=.jscpd-baseline.txt
GOVULN_EXCLUDED=Go/.govulncheck-excluded.txt
DEADCODE_ALLOWED=Go/.deadcode-allowed.txt

if [ "${1:-}" = --tools ]; then
	dir=${2:?usage: scripts/lint.sh --tools DIR TOOL…}
	shift 2
	mkdir -p "$dir" && cd "$dir" || exit 1
	set -e
	gh=https://github.com
	mac=0
	[ "$(uname -s)" = Darwin ] && mac=1
	for t in "$@"; do
		[ -x "$t" ] && continue
		case $t in
		shellcheck)
			a=linux.x86_64; [ $mac = 1 ] && a=darwin.aarch64
			curl -sSfL "$gh/koalaman/shellcheck/releases/download/v$SHELLCHECK/shellcheck-v$SHELLCHECK.$a.tar.xz" |
				tar -xJ --strip-components=1 "shellcheck-v$SHELLCHECK/shellcheck" ;;
		golangci-lint)
			a=linux-amd64; [ $mac = 1 ] && a=darwin-arm64
			curl -sSfL "$gh/golangci/golangci-lint/releases/download/v$GOLANGCI/golangci-lint-$GOLANGCI-$a.tar.gz" |
				tar -xz --strip-components=1 "golangci-lint-$GOLANGCI-$a/golangci-lint" ;;
		govulncheck) GOBIN=$PWD "$root/Go/scripts/go.sh" install "golang.org/x/vuln/cmd/govulncheck@v$GOVULNCHECK" ;;
		deadcode) GOBIN=$PWD "$root/Go/scripts/go.sh" install "golang.org/x/tools/cmd/deadcode@v$DEADCODE" ;;
		biome)
			a=linux-x64; [ $mac = 1 ] && a=darwin-arm64
			curl -sSfLo biome "$gh/biomejs/biome/releases/download/@biomejs/biome@$BIOME/biome-$a" && chmod +x biome ;;
		swiftlint)
			if [ $mac = 1 ]; then
				curl -sSfLo swiftlint.zip "$gh/realm/SwiftLint/releases/download/$SWIFTLINT/portable_swiftlint.zip"
				unzip -qo swiftlint.zip swiftlint && rm swiftlint.zip
			else
				curl -sSfLo swiftlint.zip "$gh/realm/SwiftLint/releases/download/$SWIFTLINT/swiftlint_linux_amd64.zip"
				unzip -qo swiftlint.zip swiftlint-static && mv swiftlint-static swiftlint && rm swiftlint.zip
			fi ;;
		jscpd)
			a=linux-x64-gnu; [ $mac = 1 ] && a=darwin-arm64
			curl -sSfL "$gh/kucherenko/jscpd/releases/download/v$JSCPD/jscpd-$a.tar.gz" |
				tar -xz --strip-components=0 jscpd ;;
		*) echo "lint.sh: no tool $t" >&2; exit 2 ;;
		esac
	done
	exit 0
fi

fix=0
all=0
baseline=0
while [ $# -gt 0 ]; do
	case $1 in
	--fix) fix=1 ;;
	--all) all=1 ;;
	--baseline) baseline=1 ;;
	*) break ;;
	esac
	shift
done

# changed_linters: the linters for the files this branch changes since its merge base with
# origin/main, committed or not; every one when that cannot be told or this script changed.
changed_linters() {
	local base f line want=" checks dupes " l out=
	base=$(git merge-base origin/main HEAD 2>/dev/null) || { echo "$ALL"; return; }
	while IFS= read -r f; do
		[ -n "$f" ] || continue
		case $f in
		scripts/lint.sh) echo "$ALL"; return ;;
		Go/* | Packages/Wire/*) want="$want go govulncheck deadcode " ;;
		esac
		case $f in
		*.swift | .swiftlint.yml) want="$want swift " ;;
		Web/*) want="$want web knip " ;;
		*.sh | *.bash | *.zsh | .shellcheckrc) want="$want shell zsh " ;;
		*)
			# An executable script with no extension: its first line says sh or zsh.
			if [ -f "$f" ] && [ -x "$f" ]; then
				IFS= read -r line <"$f" || line=
				case $line in '#!'*sh*) want="$want shell zsh " ;; esac
			fi ;;
		esac
	done < <({ git diff --name-only "$base"; git ls-files --others --exclude-standard; } | sort -u)
	for l in $ALL; do
		case "$want" in *" $l "*) out="$out $l" ;; esac
	done
	echo "${out# }"
}

if [ "$baseline" = 1 ]; then
	linters=${*:-go swift dupes}
elif [ $# -gt 0 ]; then
	linters=$*
elif [ "$all" = 0 ] && [ "$(uname -s)" = Darwin ] && [ -z "${CI:-}" ]; then
	linters=$(changed_linters)
	skipped=
	for l in $ALL; do
		case " $linters " in *" $l "*) ;; *) skipped="$skipped $l" ;; esac
	done
	[ -n "$skipped" ] && echo "lint.sh: only the linters for this branch's files ($linters); skipped:$skipped. --all runs every one."
else
	linters=$ALL
fi

# tool LINTER: the program it runs; zsh and checks need none of ours.
tool() {
	case $1 in
	shell) echo shellcheck ;; go) echo golangci-lint ;;
	web) echo biome ;; swift) echo swiftlint ;; dupes) echo jscpd ;; govulncheck) echo govulncheck ;;
	deadcode) echo deadcode ;;
	*) ;;
	esac
}

# version TOOL: the version this script pins.
version() {
	case $1 in
	shellcheck) echo "$SHELLCHECK" ;; golangci-lint) echo "$GOLANGCI" ;;
	govulncheck) echo "$GOVULNCHECK" ;; deadcode) echo "$DEADCODE" ;; biome) echo "$BIOME" ;; swiftlint) echo "$SWIFTLINT" ;;
	jscpd) echo "$JSCPD" ;;
	esac
}

# have TOOL: make TOOL runnable. On Linux it is on the PATH (CI). On a Mac it is the pinned
# binary in the cache, got the first time, its folder put first on the PATH.
have() {
	[ -n "$1" ] || return 0
	if [ "$(uname -s)" != Darwin ]; then
		command -v "$1" >/dev/null 2>&1
		return
	fi
	local dir tmp
	dir="$CACHE/lint-tools/$1-$(version "$1")"
	if [ ! -x "$dir/$1" ]; then
		echo "== getting $1 $(version "$1") into $dir"
		# Got beside it, then renamed: two lanes may get it at once.
		mkdir -p "$dir" && tmp=$(mktemp -d "$CACHE/lint-tools/.get.XXXXXX") || return 1
		"$root/scripts/lint.sh" --tools "$tmp" "$1" >&2 && mv -f "$tmp"/* "$dir/"
		rm -rf "$tmp"
		[ -x "$dir/$1" ] || return 1
	fi
	touch "$dir" # scripts/build-cache.sh prune takes a version not used in a week
	case ":$PATH:" in *":$dir:"*) ;; *) PATH="$dir:$PATH" ;; esac
}

# Go's caches on a Mac: Go/scripts/go.sh's, and golangci-lint's beside them.
if [ "$(uname -s)" = Darwin ] && [ -z "${CI:-}" ]; then
	mkdir -p "$CACHE/go/gopath" "$CACHE/go/gocache" "$CACHE/go/lint"
	export GOPATH="${GOPATH:-$CACHE/go/gopath}" GOMODCACHE="${GOMODCACHE:-$CACHE/go/gopath/pkg/mod}" \
		GOCACHE="${GOCACHE:-$CACHE/go/gocache}" GOLANGCI_LINT_CACHE="${GOLANGCI_LINT_CACHE:-$CACHE/go/lint}" \
		GOTOOLCHAIN="${GOTOOLCHAIN:-go$(awk '$1 == "go" { print $2; exit }' Go/go.mod)}" # as Go/scripts/go.sh
	PATH="$PATH:/opt/homebrew/bin:/usr/local/bin:/usr/local/go/bin"
	# The files CI's go job reads, on Linux: not the _darwin.go ones in their place.
	golinux=(env GOOS=linux GOARCH=amd64 CGO_ENABLED=0)
else
	golinux=()
fi

# tracked FILTER: tracked files whose first line (or name) says FILTER: sh or zsh.
tracked() {
	git ls-files -z | while IFS= read -r -d '' f; do
		[ -f "$f" ] || continue
		case "$f" in
		.specify/*) continue ;;                  # spec-kit's own scripts, vendored
		*/Tests/*/Samples/*) continue ;;          # CodeText's highlighting samples
		*.sh | *.bash | *.zsh) ;;
		*) [ -x "$f" ] || continue ;;
		esac
		IFS= read -r line <"$f" || line=
		case "$line" in
		*zsh*) kind="zsh" ;;
		*bash* | */sh*) kind="sh" ;;
		'#!'*) continue ;;
		*) case "$f" in *.sh) kind="sh" ;; *) continue ;; esac ;;
		esac
		[ "$kind" = "$1" ] && printf '%s\n' "$f"
	done
}

run_shell() {
	local files
	files=$(tracked sh)
	# shellcheck disable=SC2086 # one path per line, none with spaces
	[ "$fix" = 1 ] && shellcheck -S warning -f diff $files | git apply
	# shellcheck disable=SC2086
	shellcheck -S warning -f gcc $files
}

run_zsh() {
	local f status=0
	if command -v zsh >/dev/null 2>&1; then
		for f in $(tracked zsh); do zsh -n "$f" 2>&1 || status=1; done
		return $status
	fi
	# CI's Linux runner has no zsh: the pinned zsh container there. A Mac has zsh.
	[ "$(uname -s)" = Linux ] && command -v docker >/dev/null 2>&1 ||
		{ echo "lint.sh: no zsh on the PATH" >&2; return 1; }
	# shellcheck disable=SC2016,SC2046 # $f is the container's; one path per line, none with spaces
	docker run --rm -v "$root":/repo -w /repo "zshusers/zsh:$ZSH" \
		zsh -c 'rc=0; for f in "$@"; do zsh -n "$f" || rc=1; done; exit $rc' zsh $(tracked zsh)
}

# Every finding, then only those not in Go/.golangci-baseline.txt: a finding's key is its line
# with the line and column taken out, and each key in the baseline covers one finding. A
# gocognit finding's key has no number either: a function in the baseline stays there when an
# edit makes it a little harder or easier to read, until its task splits it (#6182).
# golangci-lint exits 1 for findings and something else when it could not finish.
# --allow-serial-runners: a run waits for the lock that every worktree's run shares (in the temp
# folder), not fail with "parallel golangci-lint is running" when another lane lints (#6039).
run_go() {
	local args=(run --allow-serial-runners --max-issues-per-linter 0 --max-same-issues 0 --show-stats=false
		--output.text.print-issued-lines=false ./...) all status
	[ "$fix" = 1 ] && args=("${args[@]}" --fix)
	all=$(mktemp)
	(cd Go && ${golinux[@]+"${golinux[@]}"} golangci-lint "${args[@]}") | sed 's|^|Go/|' >"$all"
	status=${PIPESTATUS[0]}
	if [ "$status" != 0 ] && [ "$status" != 1 ]; then
		cat "$all"
		rm -f "$all"
		return "$status"
	fi
	if [ "$baseline" = 1 ]; then
		go_key <"$all" | LC_ALL=C sort >"$GO_BASELINE"
		echo "lint.sh: wrote $(wc -l <"$GO_BASELINE" | tr -d ' ') finding(s) to $GO_BASELINE" >&2
		rm -f "$all"
		return 0
	fi
	# Each key in the baseline covers one finding; lines that are not findings print as they are.
	awk 'NR == FNR { n[$0]++; next }
		!/^[^:[:space:]]+:[0-9]+(:[0-9]+)?: / { print; next }
		{ k = $0; sub(/:[0-9]+(:[0-9]+)?: /, ": ", k); sub(/cognitive complexity [0-9]+ of /, "cognitive complexity of ", k); if (n[k] > 0) { n[k]--; old++ } else { print; new++ } }
		END { if (old) printf "lint.sh: %d old finding(s) in the baseline not shown\n", old > "/dev/stderr"; exit new > 0 }' \
		"$GO_BASELINE" "$all"
	status=$?
	rm -f "$all"
	return "$status"
}

# go_key: each finding without its line and column (and gocognit's number), as
# Go/.golangci-baseline.txt holds it.
go_key() {
	sed -nE -e 's/cognitive complexity [0-9]+ of /cognitive complexity of /' -e 's/^([^:[:space:]]+):[0-9]+(:[0-9]+)?: /\1: /p'
}

# govulncheck exits 3 for a vulnerability the code calls. One whose id is in
# Go/.govulncheck-excluded.txt (the id, then why it stays and what removes it) does not fail.
run_govulncheck() {
	local out status id new=
	out=$(cd Go && ${golinux[@]+"${golinux[@]}"} govulncheck ./... 2>&1)
	status=$?
	printf '%s\n' "$out"
	[ "$status" = 3 ] || return "$status"
	for id in $(printf '%s\n' "$out" | sed -nE 's/^Vulnerability #[0-9]+: (GO-[0-9]+-[0-9]+)$/\1/p'); do
		grep -q "^$id " "$GOVULN_EXCLUDED" 2>/dev/null || new="$new $id"
	done
	if [ -n "$new" ]; then
		echo "lint.sh: not in $GOVULN_EXCLUDED:$new"
		return 3
	fi
	echo "lint.sh: each vulnerability above is in $GOVULN_EXCLUDED, with why"
}

# deadcode (whole program, #6334): every function that no main package and no test reaches. It
# runs twice, as GOOS=linux GOARCH=amd64 and as GOOS=darwin GOARCH=arm64, each with the tests
# (-test) and the live tests (-tags live), cgo off; a function is dead only when both runs say
# so, as one used only by a _darwin.go or a _linux.go file is live. A function kept on purpose
# (reflection, go:linkname, a method that only something outside the program calls) is a line
# in Go/.deadcode-allowed.txt: its path from the root, a colon, its name as deadcode gives it,
# then why it stays. There is no baseline: every other finding fails. deadcode runs the `go`
# on the PATH, which must be the Go of go.mod (a newer one fails to read the older toolchain's
# sources): its GOROOT's bin goes first.
run_deadcode() {
	local goroot onlinux ondarwin status=0 line key
	goroot=$(cd Go && go env GOROOT) || return 1
	onlinux=$(mktemp) ondarwin=$(mktemp)
	(cd Go && PATH="$goroot/bin:$PATH" GOOS=linux GOARCH=amd64 CGO_ENABLED=0 deadcode -test -tags live ./...) >"$onlinux" 2>&1 || status=$?
	(cd Go && PATH="$goroot/bin:$PATH" GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 deadcode -test -tags live ./...) >"$ondarwin" 2>&1 || status=$?
	if [ "$status" != 0 ]; then
		cat "$onlinux" "$ondarwin"
		rm -f "$onlinux" "$ondarwin"
		return "$status"
	fi
	status=0
	while IFS= read -r line; do
		# path:line:col: unreachable func: Name, keyed as path: Name.
		key=$(printf 'Go/%s\n' "$line" | sed -nE 's/^([^:]+):[0-9]+:[0-9]+: unreachable func: (.*)$/\1: \2/p')
		if [ -z "$key" ]; then
			printf '%s\n' "$line"
			status=1
		elif ! grep -qF -- "$key " "$DEADCODE_ALLOWED" 2>/dev/null; then
			printf 'Go/%s\n' "$line"
			status=1
		fi
	done < <(LC_ALL=C comm -12 <(LC_ALL=C sort "$onlinux") <(LC_ALL=C sort "$ondarwin"))
	rm -f "$onlinux" "$ondarwin"
	[ "$status" = 0 ] || echo "lint.sh: remove each function above, or name it in $DEADCODE_ALLOWED with why it stays"
	return "$status"
}

# Biome's github reporter, turned into path:line:col: lines with the path from the root.
run_web() {
	local args=(lint --max-diagnostics=none --error-on-warnings --reporter=github)
	[ "$fix" = 1 ] && args=("${args[@]}" --write)
	cd Web || return 1
	biome "${args[@]}" . 2>&1 | sed -nE 's#^::(error|warning) title=([^,]*),file=([^,]*),line=([0-9]+)[^:]*,col=([0-9]+)[^:]*::(.*)#Web/\3:\4:\5: \1: \6 (\2)#p' |
		sed -E 's#^Web/(/repo/Web/|'"$root"'/Web/|\./)?#Web/#'
	local status=${PIPESTATUS[0]}
	cd "$root" || return 1
	return "$status"
}

# knip (#6337): every file, export, type and dependency of the web app that no bundle of
# Web/build.mjs, test or walk reaches, from Knip at the version Web/package.json pins (it is
# in the workspace's packages: scripts/pnpm.sh install --frozen-lockfile, as scripts/nx.sh
# does). Web/knip.config.js names the entry points, reads a test's load("src/…") as an import,
# and allows each finding kept on purpose, with why; there is no baseline. Its GitHub reporter,
# turned into path:line:col: lines with the path from the root.
run_knip() {
	local knip=Web/node_modules/.bin/knip
	[ -x "$knip" ] || { echo "lint.sh: no $knip: run scripts/pnpm.sh install --frozen-lockfile first"; return 1; }
	cd Web || return 1
	./node_modules/.bin/knip --no-progress --no-config-hints --reporter github-actions 2>&1 |
		sed -E 's#^::(error|warning) file=([^,]*),line=([0-9]+),[^:]*,col=([0-9]+),[^:]*,title=[^:]*/ ([^:]*)::(.*)#Web/\2:\3:\4: \1: \6 (\5)#'
	local status=${PIPESTATUS[0]}
	cd "$root" || return 1
	[ "$status" = 0 ] || echo "lint.sh: remove each one above, or allow it in Web/knip.config.js with why it stays"
	return "$status"
}

# Only the findings not in .swiftlint-baseline.json fail (SwiftLint's own baseline).
run_swift() {
	[ "$fix" = 1 ] && swiftlint --fix --quiet
	if [ "$baseline" = 1 ]; then
		swiftlint lint --quiet --write-baseline "$SWIFT_BASELINE" >/dev/null
		echo "lint.sh: wrote $SWIFT_BASELINE" >&2
		return 0
	fi
	swiftlint lint --quiet --strict --baseline "$SWIFT_BASELINE" | sed -E "s#^(/repo/|$root/)##"
	return "${PIPESTATUS[0]}"
}

run_dupes() {
	local out log status
	out=$(mktemp -d "$root/.agents/reviews/.jscpd-lint.XXXXXX") || return 1
	log="$out/run.log"
	jscpd --reporters json --output "$out" >"$log" 2>&1
	status=$?
	if [ -f "$out/jscpd-report.json" ]; then
		if [ "$baseline" = 1 ]; then
			python3 scripts/jscpd-report.py --write-baseline "$DUPES_BASELINE" "$out/jscpd-report.json"
		else
			python3 scripts/jscpd-report.py --baseline "$DUPES_BASELINE" "$out/jscpd-report.json"
		fi
		status=$?
	else
		cat "$log"
		[ "$status" = 0 ] && status=1
	fi
	rm -rf "$out"
	return "$status"
}

# Our own checks that read only text, and so run anywhere: the rest need a build.
run_checks() {
	local status=0
	python3 scripts/check-test-indexing.py --selftest || status=1
	python3 scripts/check-test-indexing.py || status=1
	python3 scripts/check-window-defaults.py || status=1
	python3 scripts/check-store-profiles.py --selftest || status=1
	python3 scripts/listing-watch.py --selftest || status=1
	python3 scripts/check-stacked-labels.py --selftest || status=1
	python3 scripts/check-stacked-labels.py || status=1
	python3 scripts/check-task-tuple-returns.py --selftest || status=1
	python3 scripts/check-task-tuple-returns.py || status=1
	python3 scripts/check-approval-words.py --selftest || status=1
	python3 scripts/check-approval-words.py || status=1
	python3 scripts/check-workflow-github-reads.py --selftest || status=1
	python3 scripts/check-workflow-github-reads.py || status=1
	python3 scripts/check-deploy-bounds.py --selftest || status=1
	python3 scripts/check-deploy-bounds.py || status=1
	python3 scripts/check-store-bounds.py --selftest || status=1
	python3 scripts/check-store-bounds.py || status=1
	python3 scripts/wire-compat.py --selftest || status=1
	python3 scripts/jscpd-report.py --selftest || status=1
	python3 scripts/docs-parts.py --selftest || status=1
	python3 scripts/docs-parts.py check || status=1
	python3 scripts/parity-counts.py --selftest || status=1
	python3 scripts/parity-counts.py >/dev/null || status=1
	sh scripts/design.sh selftest || status=1
	sh scripts/design.sh check || status=1
	python3 scripts/check-colour-tokens.py --selftest || status=1
	python3 scripts/check-colour-tokens.py || status=1
	python3 -m unittest discover -s .agents/skills/ship-app/scripts -p 'test_*.py' -q || status=1
	python3 .agents/skills/run-app/scripts/place.py --selftest || status=1
	sh scripts/version.sh --selftest || status=1
	bash scripts/build-cache-test.sh || status=1
	python3 .agents/skills/release/scripts/capability-changes.py --selftest || status=1
	python3 .agents/skills/release/scripts/release-plan.py --selftest || status=1
	python3 .agents/skills/release/scripts/advisory.py --selftest || status=1
	return $status
}

summary=${GITHUB_STEP_SUMMARY:-/dev/null}
changed=$(mktemp)
trap 'rm -f "$changed"' EXIT
[ -n "${LINT_BASE:-}" ] && git diff --name-only "$LINT_BASE"...HEAD >"$changed" 2>/dev/null
failed=
printf '| Linter | Findings | In changed files | Seconds |\n|---|---|---|---|\n' >>"$summary"
for linter in $linters; do
	case " $ALL " in *" $linter "*) ;; *)
		echo "lint.sh: no linter $linter (one of: $ALL)" >&2
		exit 2
		;;
	esac
	if ! have "$(tool "$linter")"; then
		if [ "$(uname -s)" = Darwin ]; then
			echo "== $linter: failed, as $(tool "$linter") is not on the PATH and could not be got"
		else
			# On Linux lint.sh gets nothing itself: the job puts the pinned linters on the PATH (#5890).
			echo "== $linter: failed, as $(tool "$linter") is not on the PATH; on Linux, run scripts/lint.sh --tools DIR $(tool "$linter") first and put DIR on the PATH"
		fi
		failed="$failed $linter"
		continue
	fi
	echo "== $linter"
	out=$(mktemp)
	start=$(date +%s)
	"run_$linter" >"$out" 2>&1
	status=$?
	took=$(($(date +%s) - start))
	cat "$out"
	if [ "$linter" = govulncheck ]; then
		count=$(grep -c '^Vulnerability #' "$out")
	else
		count=$(grep -cE '^[^:[:space:]]+:[0-9]+(:[0-9]+)?: ' "$out")
	fi
	[ "$status" != 0 ] && [ "$count" = 0 ] && count="failed ($status)"
	mine=-
	if [ -s "$changed" ] && [ "$linter" != govulncheck ]; then
		# The findings whose path, before its first colon, this branch changed.
		mine=$(awk -F: 'NR == FNR { c[$0] = 1; next } /^[^:[:space:]]+:[0-9]+/ && ($1 in c)' "$changed" "$out")
		if [ -n "$mine" ]; then
			echo "-- $linter: in the files this branch changes:"
			printf '%s\n' "$mine"
			mine=$(printf '%s\n' "$mine" | wc -l | tr -d ' ')
		else
			mine=0
		fi
	fi
	rm -f "$out"
	printf '| %s | %s | %s | %s |\n' "$linter" "$count" "$mine" "$took" >>"$summary"
	echo "-- $linter: $count finding(s), ${took}s, exit $status"
	[ "$status" != 0 ] && failed="$failed $linter"
done

if [ -n "$failed" ]; then
	echo "lint.sh: linters failed:$failed (scripts/lint.sh --fix fixes what it can)" >&2
	exit 1
fi
