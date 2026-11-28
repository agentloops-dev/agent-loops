#!/bin/bash
# The parts of freeing disk space on this Mac that are not a worktree or the build cache
# (#1796). The free-disk-space skill runs them; a person can too. Each is safe to run
# again, and what it removes is made again when it is needed.
#
#   free-disk.sh status                free space now, and how far from the target
#   free-disk.sh chromes [--hours N]   end leaked headless Chromes, then their clones
#   free-disk.sh xcode                 older iOS DeviceSupport builds, unavailable simulators
#   free-disk.sh deriveddata [--dry-run]  Xcode DerivedData of worktrees that are gone
#   free-disk.sh worktrees [--dry-run] [--committed-hours N] [--uncommitted-hours N]
#                                      worktrees nobody has changed for a while
#   free-disk.sh coredevice [--dry-run]   the app install deltas CoreDevice keeps
#   free-disk.sh buildoutput [--dry-run] [--hours N]
#                                      build output in worktrees nobody builds in,
#                                      the nightly runtime check's tree too
#   free-disk.sh simulators [--dry-run]   erase the walk simulators that are shut down
#   free-disk.sh colima [--dry-run] [--days N]  Colima data disks no instance has
#   free-disk.sh caches [--dry-run]    uv's and npm's package caches
#   free-disk.sh gocache [--dry-run] [--max-gb N]  Go's default build cache, over N GB
#   free-disk.sh hidden                what du cannot see: fseventsd's logs, local snapshots
#
# hidden: about 120 GB of the Data volume was not visible to du on 2026-11-20 (#5725).
# The main suspect is /System/Volumes/Data/.fseventsd, the file-event logs that only root
# can read: it had about 137,000 log files, and builds add to them. On APFS a folder's
# size is 32 bytes for each entry, so its stat size gives the number of files with no
# sudo. This prints that number, the Time Machine local snapshots, and, when `sudo -n`
# works (#6426), the true size of the root-only folders. It deletes nothing: clearing
# .fseventsd needs root and a restart, so it is Alex's step (the free-disk-space skill).
#
# None of them needs the app or its session list (#5722): each decides by what is on disk
# and what runs, so a person or any agent can run it.
#
# status: the target is 20% of the disk (AGENTS_FREE_TARGET_PCT moves it; #4699), the ok
# line machine.disk_ok fires at: the 10% line machine.disk_low fires at, plus 10 points
# (#6953), so a run ends well past the line and not on it.
# It exits 0 when free space is at the target or over it, and 1 when it is under.
#
# worktrees: the worktrees under <main checkout>/.agents/worktrees that nobody has changed
# for a while (Alex, 2026-11-11, #4699). It needs no session list, so a person can run it.
# The last change is the newest file's mtime, leaving out .git and build output (build,
# .build, DerivedData, node_modules). A worktree goes, with `git worktree remove --force`,
# when one of these is true:
#   - all its work is committed, and nothing changed for N hours (--committed-hours, 4);
#   - it has uncommitted work, and nothing changed for N hours (--uncommitted-hours, 8).
#     That work is lost: --force removes it.
# It keeps a worktree any process has as its working folder, a detached HEAD with commits
# that are on no branch, a locked one, and anything that is not a linked worktree of the
# main checkout. The branch stays, with every commit. --dry-run says what it would remove.
#
# buildoutput: the build output (build, .build, DerivedData, node_modules) of each
# worktree under <main checkout>/.agents/worktrees, and of the nightly runtime check's
# tree ($AGENTS_NIGHTLY_HOME/tree, default ~/Library/Caches/Agents Nightly Runtimes/tree;
# #6461), that the worktrees checks below keep for no other reason, when no process works in it and no file in it, build output too,
# changed for N hours (--hours, 1). A folder goes only when git ignores it and tracks
# nothing in it: the worktree's own .gitignore says it is build output (#5722).
#
# simulators: the run-app skill's walk simulators ("Agents Walk", "Agents Walk (<type>)",
# #1182) are kept (#2400), and their data grows with each walk. Each one that is shut
# down and uses 1 GB or more is erased (`simctl erase`): it keeps its name and id, and
# the next walk installs the Remote again. A booted one is in use and is kept, and none is erased while a walk
# script (remote.sh, walk.py) runs.
#
# colima: `colima delete` keeps the instance's data disk (~/.colima/_lima/_disks/<name>,
# up to 100 GB sparse) for the next `colima start`. When `colima list` names no instance
# and no limactl or colima process runs, a disk that nothing changed for N days
# (--days, 1) is removed. The next `colima start` makes a new one, and its images are
# pulled or built again.
#
# caches: `uv cache prune` (the entries no package uses) and `npm cache clean --force`.
# Both caches are filled again by the next install.
#
# gocache: Go/scripts/go.sh and scripts/lint.sh put Go's build cache in the shared build
# cache, which scripts/build-cache.sh prune bounds. A `go` run without them (by hand, or
# gopls) uses Go's default cache, `go env GOCACHE` (~/Library/Caches/go-build), which
# nothing bounds: 28 GB on 2026-11-20 (#6462). When that cache is not the shared one, is
# over N GB (--max-gb, 2) and no go process runs, `go clean -cache` empties it. The next
# build compiles again.
#
# coredevice: CoreDevice keeps a delta of each app it installs on a device
# (AppInstallationBinaryDeltas, 15 GB here on 2026-11-11), so the next install sends only
# what changed. It removes them when no devicectl runs; the next install sends the whole app.
#
# chromes: a walk's Chrome (Web/test/walk/cdp.mjs) is headless, with a throwaway profile
# under /tmp. One that a walk left keeps running, and a running Chrome keeps the APFS
# clone of the app that macOS made for it (up to 3 GB each) until a restart. This ends
# only a Chrome that is all of these:
#   - the main process (no --type=), started more than N hours ago (default 6);
#   - --headless;
#   - --user-data-dir under /tmp or /private/tmp.
# The person's own Chrome, and the sign-up Chrome (.agents/signups/chrome-profile, not
# headless), are none of these, so they are never ended. Then it removes each clone folder
# no running process has a file open in, and that is more than an hour old.
#
# xcode: Xcode keeps a DeviceSupport folder per device model and OS build (about 6.5 GB
# each). Only the newest build of each model is kept. Simulators whose runtime is gone
# (`simctl delete unavailable`) go too.
#
# deriveddata: Xcode builds each worktree into ~/Library/Developer/Xcode/DerivedData/
# Agents-<hash> (3-4 GB each), not into the worktree, so removing the worktree leaves it
# (#2683). This removes an Agents-* folder only when all of these are true:
#   - its info.plist has a WorkspacePath;
#   - that path is under <main checkout>/.agents/worktrees/;
#   - that path does not exist;
#   - no running xcodebuild names the folder or that worktree.
# An Agents-* folder with no info.plist goes too, when it holds only Logs and SourcePackages
# and no file in it changed in the last hour: an `xcodebuild -list` or a package resolve with
# no -derivedDataPath leaves one (0.9 GB each, 47 of them on 2026-11-20, #6456), and nothing
# builds in it. Any other folder with no info.plist, a folder of another project, and
# Xcode's shared caches (ModuleCache.noindex, SDK*) are kept. --dry-run says what it would
# remove.
set -euo pipefail

die() { echo "free-disk.sh: $*" >&2; exit 1; }

gb() { awk -v k="$1" 'BEGIN { printf "%.1f", k / 1048576 }'; }

status() {
	local pct=${AGENTS_FREE_TARGET_PCT:-20} line size avail target
	line=$(df -k "$HOME" | tail -1)
	size=$(echo "$line" | awk '{print $2}')
	avail=$(echo "$line" | awk '{print $4}')
	target=$((size * pct / 100))
	if [ "$avail" -ge "$target" ]; then
		echo "free $(gb "$avail") GB, target $(gb "$target") GB ($pct%): reached"
	else
		echo "free $(gb "$avail") GB, target $(gb "$target") GB ($pct%): $(gb $((target - avail))) GB short"
		return 1
	fi
}

hidden() {
	local data=/System/Volumes/Data ev entries snaps
	ev=$data/.fseventsd
	if [ -d "$ev" ]; then
		entries=$(($(stat -f %z "$ev") / 32))
		echo "fseventsd: about $entries files in $ev (root only)"
	fi
	snaps=$(tmutil listlocalsnapshots / 2>/dev/null | grep -c 'com.apple' || true)
	echo "local snapshots: $snaps"
	if sudo -n true 2>/dev/null; then
		sudo -n du -xsk "$ev" "$data/.Spotlight-V100" "$data/.DocumentRevisions-V100" \
			/private/var/db 2>/dev/null | while read -r k path; do
			echo "$(gb "$k") GB $path"
		done
	else
		echo "root-only folders: not measured (needs sudo; #6426)"
	fi
}

# ps's etime, [[dd-]hh:]mm:ss, in seconds.
seconds() {
	awk -v t="$1" 'BEGIN {
		d = 0; if (index(t, "-")) { split(t, a, "-"); d = a[1]; t = a[2] }
		n = split(t, p, ":"); s = 0; for (i = 1; i <= n; i++) s = s * 60 + p[i]
		print d * 86400 + s }'
}

chromes() {
	local hours=6
	while [ $# -gt 0 ]; do
		case $1 in
		--hours) hours=$2; shift ;;
		*) die "chromes [--hours N]" ;;
		esac
		shift
	done
	local pid etime cmd ended=0 kept=0
	while read -r pid etime cmd; do
		case $cmd in "/Applications/Google Chrome"*/Contents/MacOS/*) ;; *) continue ;; esac
		case $cmd in *" --type="*) continue ;; esac
		case $cmd in *" --headless"*) ;; *) continue ;; esac
		case $cmd in *" --user-data-dir=/tmp/"* | *" --user-data-dir=/private/tmp/"*) ;; *) continue ;; esac
		if [ "$(seconds "$etime")" -lt $((hours * 3600)) ]; then
			kept=$((kept + 1))
			continue
		fi
		echo "ending Chrome $pid (running $etime): $(echo "$cmd" | grep -o -- '--user-data-dir=[^ ]*')"
		kill "$pid" 2>/dev/null && ended=$((ended + 1)) || true
	done < <(ps -axo pid=,etime=,command=)
	echo "ended $ended headless Chrome(s); $kept younger than $hours hours left running"
	[ "$ended" = 0 ] || sleep 5

	local clones
	clones="$(dirname "$(getconf DARWIN_USER_TEMP_DIR)")/X/com.google.Chrome.code_sign_clone"
	[ -d "$clones" ] || { echo "no Chrome clones"; return 0; }
	local open d k freed=0
	# Every clone any process has a file open in: a running Chrome runs from its own.
	open=$(lsof -nP -Fn 2>/dev/null | sed -n 's#^n.*/com\.google\.Chrome\.code_sign_clone/\(code_sign_clone\.[^/]*\).*#\1#p' | sort -u || true)
	while IFS= read -r d; do
		if grep -qxF "$(basename "$d")" <<<"$open"; then
			echo "kept $(basename "$d"): in use"
			continue
		fi
		k=$(du -sk "$d" | cut -f1)
		echo "removing $(basename "$d") ($(gb "$k") GB apparent)"
		rm -rf -- "$d"
		freed=$((freed + k))
	done < <(find "$clones" -mindepth 1 -maxdepth 1 -type d -name 'code_sign_clone.*' -mmin +60)
	echo "removed clones of $(gb "$freed") GB apparent (clones share blocks, so less is freed)"
}

xcode() {
	local ds="$HOME/Library/Developer/Xcode/iOS DeviceSupport" model d
	if [ -d "$ds" ]; then
		# Names are "<model> <version> (<build>)", such as "iPhone16,2 27.0.1 (24A446)".
		while IFS= read -r model; do
			# All but the newest version of this model, by version order.
			find "$ds" -mindepth 1 -maxdepth 1 -type d -name "$model *" -exec basename {} \; |
				awk '{print $2 "\t" $0}' | sort -t "$(printf '\t')" -k1,1V | cut -f2- | sed '$d' |
				while IFS= read -r d; do
					echo "removing DeviceSupport $d ($(gb "$(du -sk "$ds/$d" | cut -f1)") GB), a newer build is there"
					rm -rf -- "${ds:?}/$d"
				done
		done < <(find "$ds" -mindepth 1 -maxdepth 1 -type d -exec basename {} \; | awk '{print $1}' | sort -u)
	fi
	echo "deleting unavailable simulators"
	xcrun simctl delete unavailable || echo "simctl delete unavailable failed: left as it is"
}

# resolve_only DIR: DIR holds nothing but Logs and SourcePackages (and Finder's .DS_Store).
resolve_only() {
	local e n=0
	for e in "$1"/* "$1"/.[!.]*; do
		[ -e "$e" ] || continue
		case ${e##*/} in
		Logs | SourcePackages) n=$((n + 1)) ;;
		.DS_Store) ;;
		*) return 1 ;;
		esac
	done
	[ "$n" -gt 0 ]
}

deriveddata() {
	local dry=0
	while [ $# -gt 0 ]; do
		case $1 in
		--dry-run) dry=1 ;;
		*) die "deriveddata [--dry-run]" ;;
		esac
		shift
	done
	local dd="$HOME/Library/Developer/Xcode/DerivedData" main
	[ -d "$dd" ] || { echo "no DerivedData"; return 0; }
	main=$(dirname "$(git -C "$(dirname "$0")" rev-parse --path-format=absolute --git-common-dir)")
	local builds d name ws k freed=0 removed=0 verb=removing
	[ "$dry" = 0 ] || verb="would remove"
	builds=$(pgrep -fl xcodebuild || true)
	while IFS= read -r d; do
		name=$(basename "$d")
		if [ ! -e "$d/info.plist" ]; then
			if ! resolve_only "$d"; then
				echo "kept $name: no info.plist, and it holds more than Logs and SourcePackages"
			elif [ -n "$(find "$d" -mmin -60 -print -quit 2>/dev/null)" ]; then
				echo "kept $name: no info.plist, and a file in it changed in the last hour"
			elif [ -n "$builds" ] && grep -qF "$name" <<<"$builds"; then
				echo "kept $name: an xcodebuild uses it"
			else
				k=$(du -sk "$d" | cut -f1)
				echo "$verb $name ($(gb "$k") GB): no info.plist, only a package resolve's Logs and SourcePackages"
				[ "$dry" = 1 ] || rm -rf -- "$d"
				freed=$((freed + k))
				removed=$((removed + 1))
			fi
			continue
		fi
		if ! ws=$(plutil -extract WorkspacePath raw "$d/info.plist" 2>/dev/null) || [ -z "$ws" ]; then
			echo "kept $name: no WorkspacePath"
			continue
		fi
		case $ws in
		"$main"/.agents/worktrees/*) ;;
		*) echo "kept $name: $ws is not a worktree of $main"; continue ;;
		esac
		if [ -e "$ws" ]; then
			echo "kept $name: $ws is there"
			continue
		fi
		if [ -n "$builds" ] && grep -qF -e "$name" -e "$(dirname "$ws")" <<<"$builds"; then
			echo "kept $name: an xcodebuild uses it"
			continue
		fi
		k=$(du -sk "$d" | cut -f1)
		echo "$verb $name ($(gb "$k") GB): $ws is gone"
		[ "$dry" = 1 ] || rm -rf -- "$d"
		freed=$((freed + k))
		removed=$((removed + 1))
	done < <(find "$dd" -mindepth 1 -maxdepth 1 -type d -name 'Agents-*')
	if [ "$dry" = 1 ]; then
		echo "would remove $removed folder(s), $(gb "$freed") GB"
	else
		echo "removed $removed folder(s), $(gb "$freed") GB"
	fi
}

# The newest mtime, in seconds, of a file in $1, leaving out .git and build output.
last_change() {
	find "$1" \( -name .git -o -name build -o -name .build -o -name DerivedData -o -name node_modules \) -prune \
		-o -type f -exec stat -f '%m' {} + 2>/dev/null | awk '$1 > m { m = $1 } END { if (m) print m }'
}

main_checkout() {
	local m
	m=$(dirname "$(git -C "$(dirname "$0")" rev-parse --path-format=absolute --git-common-dir)")
	cd "$m" && pwd -P
}

# Every process's working folder: a session or a build in a worktree keeps it.
working_folders() { lsof -a -d cwd -Fn 2>/dev/null | sed -n 's/^n//p' || true; }

# The checks a worktree must pass before anything in it goes. It sets W (the path with
# its links resolved) and NAME; when the worktree is kept, it says why and fails.
# $1 is the folder, $2 the main checkout, $3 `git worktree list --porcelain` and $4 every
# process's working folder.
movable() {
	W=$(cd "$1" && pwd -P) || return 1
	NAME=$(basename "$W")
	if [ "$W" = "$2" ] || ! grep -qxF "worktree $W" <<<"$3"; then
		echo "kept $NAME: not a linked worktree of $2"
		return 1
	fi
	if [ "$(git -C "$W" rev-parse --path-format=absolute --git-dir)" = \
		"$(git -C "$W" rev-parse --path-format=absolute --git-common-dir)" ]; then
		echo "kept $NAME: a main checkout"
		return 1
	fi
	if awk -v w="worktree $W" '$0 == w { f = 1; next } /^worktree / { f = 0 } f && /^locked/ { found = 1 } END { exit !found }' <<<"$3"; then
		echo "kept $NAME: locked"
		return 1
	fi
	if awk -v w="$W" '$0 == w || index($0, w "/") == 1 { found = 1 } END { exit !found }' <<<"$4"; then
		echo "kept $NAME: a process works in it"
		return 1
	fi
}

worktrees() {
	local dry=0 committed=4 uncommitted=8
	while [ $# -gt 0 ]; do
		case $1 in
		--dry-run) dry=1 ;;
		--committed-hours) committed=$2; shift ;;
		--uncommitted-hours) uncommitted=$2; shift ;;
		*) die "worktrees [--dry-run] [--committed-hours N] [--uncommitted-hours N]" ;;
		esac
		shift
	done
	local main
	main=$(main_checkout)
	[ -d "$main/.agents/worktrees" ] || { echo "no worktrees"; return 0; }
	local listed cwds now w last hours dirty limit k freed=0 removed=0 verb=removing
	[ "$dry" = 0 ] || verb="would remove"
	listed=$(git -C "$main" worktree list --porcelain)
	cwds=$(working_folders)
	now=$(date +%s)
	for w in "$main"/.agents/worktrees/*/; do
		movable "$w" "$main" "$listed" "$cwds" || continue
		w=$W
		if ! git -C "$w" symbolic-ref -q HEAD >/dev/null &&
			[ -n "$(git -C "$w" rev-list -n 1 HEAD --not --branches --remotes)" ]; then
			echo "kept $NAME: detached, with commits on no branch"
			continue
		fi
		last=$(last_change "$w")
		hours=$(((now - ${last:-$now}) / 3600))
		dirty=$(git -C "$w" status --porcelain --untracked-files=all | wc -l | tr -d ' ')
		if [ "$dirty" -gt 0 ]; then limit=$uncommitted; else limit=$committed; fi
		if [ "$hours" -lt "$limit" ]; then
			echo "kept $NAME: changed ${hours}h ago, under ${limit}h ($dirty uncommitted)"
			continue
		fi
		k=$(du -sk "$w" | cut -f1)
		echo "$verb $NAME ($(gb "$k") GB): unchanged for ${hours}h, $dirty uncommitted, branch $(git -C "$w" symbolic-ref -q --short HEAD || echo detached) kept"
		if [ "$dry" = 0 ] && ! git -C "$main" worktree remove --force "$w"; then
			echo "kept $NAME: git worktree remove failed"
			continue
		fi
		freed=$((freed + k))
		removed=$((removed + 1))
	done
	[ "$dry" = 1 ] || git -C "$main" worktree prune
	if [ "$dry" = 1 ]; then
		echo "would remove $removed worktree(s), $(gb "$freed") GB"
	else
		echo "removed $removed worktree(s), $(gb "$freed") GB"
	fi
}

buildoutput() {
	local dry=0 hours=1
	while [ $# -gt 0 ]; do
		case $1 in
		--dry-run) dry=1 ;;
		--hours) hours=$2; shift ;;
		*) die "buildoutput [--dry-run] [--hours N]" ;;
		esac
		shift
	done
	local main nightly
	main=$(main_checkout)
	nightly=${AGENTS_NIGHTLY_HOME:-"$HOME/Library/Caches/Agents Nightly Runtimes"}/tree
	local trees=()
	[ ! -d "$main/.agents/worktrees" ] || trees+=("$main"/.agents/worktrees/*/)
	[ ! -d "$nightly" ] || trees+=("$nightly/")
	[ ${#trees[@]} -gt 0 ] || { echo "no worktrees"; return 0; }
	local listed cwds w d r k freed=0 verb=freed
	[ "$dry" = 0 ] || verb="would free"
	listed=$(git -C "$main" worktree list --porcelain)
	cwds=$(working_folders)
	for w in "${trees[@]}"; do
		[ -d "$w" ] || continue
		movable "$w" "$main" "$listed" "$cwds" || continue
		w=$W
		# Build output counts here: a build started from another folder writes in it.
		if [ -n "$(find "$w" -name .git -prune -o -mmin -$((hours * 60)) -print -quit 2>/dev/null)" ]; then
			echo "kept $NAME: a file changed in the last ${hours}h"
			continue
		fi
		while IFS= read -r d; do
			r=${d#"$w"/}
			if git -C "$w" check-ignore -q -- "$r/" && [ -z "$(git -C "$w" ls-files -- "$r" | head -n 1)" ]; then
				k=$(du -sk "$d" | cut -f1)
				echo "$verb $(gb "$k") GB: $NAME/$r"
				[ "$dry" = 1 ] || rm -rf -- "$d"
				freed=$((freed + k))
			else
				echo "kept $NAME/$r: tracked or not ignored"
			fi
		done < <(find "$w" -name .git -prune -o -type d \
			\( -name build -o -name .build -o -name DerivedData -o -name node_modules \) -print -prune)
	done
	echo "$verb $(gb "$freed") GB of build output"
}

simulators() {
	local dry=0
	while [ $# -gt 0 ]; do
		case $1 in
		--dry-run) dry=1 ;;
		*) die "simulators [--dry-run]" ;;
		esac
		shift
	done
	if pgrep -qf 'run-app/scripts/(remote\.sh|walk\.py)'; then
		echo "kept the walk simulators: a walk runs"
		return 0
	fi
	local dev="$HOME/Library/Developer/CoreSimulator/Devices" udid state name k n=0
	while IFS=$'\t' read -r udid state name; do
		if [ "$state" != Shutdown ]; then
			echo "kept $name: $state"
			continue
		fi
		k=$(du -sk "$dev/$udid" 2>/dev/null | cut -f1)
		if [ "${k:-0}" -lt 1048576 ]; then
			echo "kept $name: under 1 GB"
			continue
		fi
		if [ "$dry" = 1 ]; then
			echo "would erase $name ($(gb "${k:-0}") GB apparent)"
		elif xcrun simctl erase "$udid"; then
			echo "erased $name ($(gb "${k:-0}") GB apparent; much of it shares blocks with the runtime)"
			n=$((n + 1))
		else
			echo "kept $name: simctl erase failed"
		fi
	done < <(xcrun simctl list devices -j | python3 -c '
import json, sys
for devices in json.load(sys.stdin)["devices"].values():
    for d in devices:
        if d["name"] == "Agents Walk" or d["name"].startswith("Agents Walk ("):
            print(d["udid"], d["state"], d["name"], sep="\t")')
	[ "$dry" = 1 ] || echo "erased $n walk simulator(s)"
}

colima() {
	local dry=0 days=1
	while [ $# -gt 0 ]; do
		case $1 in
		--dry-run) dry=1 ;;
		--days) days=$2; shift ;;
		*) die "colima [--dry-run] [--days N]" ;;
		esac
		shift
	done
	local disks="${COLIMA_HOME:-$HOME/.colima}/_lima/_disks" d k
	[ -d "$disks" ] || { echo "no Colima data disks"; return 0; }
	if ! command -v colima >/dev/null 2>&1; then
		echo "kept the Colima data disks: no colima command to ask which are in use"
		return 0
	fi
	if [ -n "$(command colima list --json 2>/dev/null)" ]; then
		echo "kept the Colima data disks: colima lists an instance"
		return 0
	fi
	if pgrep -qx limactl || pgrep -qf '(^|/)colima( |$)'; then
		echo "kept the Colima data disks: limactl or colima runs"
		return 0
	fi
	while IFS= read -r d; do
		if [ -n "$(find "$d" -mtime -"$days" -print -quit)" ]; then
			echo "kept $d: changed in the last $days day(s)"
			continue
		fi
		k=$(du -sk "$d" | cut -f1)
		if [ "$dry" = 1 ]; then
			echo "would remove $d ($(gb "$k") GB): no Colima instance uses it"
		else
			rm -rf -- "$d"
			echo "removed $d ($(gb "$k") GB): no Colima instance used it"
		fi
	done < <(find "$disks" -mindepth 1 -maxdepth 1 -type d)
}

caches() {
	local dry=0 k
	while [ $# -gt 0 ]; do
		case $1 in
		--dry-run) dry=1 ;;
		*) die "caches [--dry-run]" ;;
		esac
		shift
	done
	if command -v uv >/dev/null 2>&1; then
		if pgrep -qx uv; then
			echo "kept uv's cache: uv runs"
		elif [ "$dry" = 1 ]; then
			echo "would prune uv's cache ($(uv cache dir 2>/dev/null))"
		else
			uv cache prune 2>&1 | tail -n 1
		fi
	fi
	if command -v npm >/dev/null 2>&1 && [ -d "$HOME/.npm/_cacache" ]; then
		k=$(du -sk "$HOME/.npm/_cacache" | cut -f1)
		if pgrep -qx npm || pgrep -qx npx; then
			echo "kept npm's cache: npm runs"
		elif [ "$dry" = 1 ]; then
			echo "would clean npm's cache ($(gb "$k") GB)"
		else
			npm cache clean --force 2>/dev/null
			echo "cleaned npm's cache ($(gb "$k") GB)"
		fi
	fi
}

gocache() {
	local dry=0 max=2
	while [ $# -gt 0 ]; do
		case $1 in
		--dry-run) dry=1 ;;
		--max-gb) max=$2; shift ;;
		*) die "gocache [--dry-run] [--max-gb N]" ;;
		esac
		shift
	done
	command -v go >/dev/null 2>&1 || { echo "no go command: no Go build cache"; return 0; }
	local dir shared k
	dir=$(go env GOCACHE 2>/dev/null) || dir=
	[ -n "$dir" ] && [ -d "$dir" ] || { echo "no Go build cache"; return 0; }
	dir=$(cd "$dir" && pwd -P)
	shared="${AGENTS_BUILD_CACHE:-$HOME/Library/Caches/Agents-build}/go/gocache"
	if [ -d "$shared" ] && [ "$dir" = "$(cd "$shared" && pwd -P)" ]; then
		echo "kept $dir: the shared build cache, which build-cache.sh prune bounds"
		return 0
	fi
	k=$(du -sk "$dir" | cut -f1)
	if [ "$k" -le $((max * 1048576)) ]; then
		echo "kept $dir ($(gb "$k") GB): under $max GB"
		return 0
	fi
	if pgrep -qx go || pgrep -qf 'Go/scripts/go.sh|golangci'; then
		echo "kept $dir ($(gb "$k") GB): go runs"
		return 0
	fi
	if [ "$dry" = 1 ]; then
		echo "would clean Go's build cache $dir ($(gb "$k") GB)"
	else
		go clean -cache
		echo "cleaned Go's build cache $dir ($(gb "$k") GB)"
	fi
}

coredevice() {
	local dry=0
	while [ $# -gt 0 ]; do
		case $1 in
		--dry-run) dry=1 ;;
		*) die "coredevice [--dry-run]" ;;
		esac
		shift
	done
	local d="$HOME/Library/Containers/com.apple.CoreDevice.CoreDeviceService/Data/Library/Caches/AppInstallationBinaryDeltas" k
	[ -d "$d" ] || { echo "no CoreDevice install deltas"; return 0; }
	if pgrep -q devicectl; then
		echo "kept CoreDevice install deltas: a devicectl runs"
		return 0
	fi
	k=$(du -sk "$d" | cut -f1)
	if [ "$dry" = 1 ]; then
		echo "would remove CoreDevice install deltas ($(gb "$k") GB)"
	else
		find "$d" -mindepth 1 -maxdepth 1 -exec rm -rf -- {} +
		echo "removed CoreDevice install deltas ($(gb "$k") GB)"
	fi
}

case ${1:-} in
status) status ;;
hidden) hidden ;;
chromes) shift; chromes "$@" ;;
xcode) xcode ;;
deriveddata) shift; deriveddata "$@" ;;
worktrees) shift; worktrees "$@" ;;
coredevice) shift; coredevice "$@" ;;
buildoutput) shift; buildoutput "$@" ;;
simulators) shift; simulators "$@" ;;
colima) shift; colima "$@" ;;
caches) shift; caches "$@" ;;
gocache) shift; gocache "$@" ;;
*) awk 'NR > 1 && /^set -euo/ { exit } NR > 1' "$0" | sed 's/^# \{0,1\}//'; exit 2 ;;
esac
