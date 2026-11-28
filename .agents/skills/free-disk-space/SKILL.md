---
name: free-disk-space
description: Free disk space on this Mac (#199, #1796) until 20% of the disk is free. Bound the shared build cache (Swift, Go and the Go temp folders that killed runs leave) and Go's default build cache, delete the build output in worktrees nobody builds in, remove the worktrees nobody has changed for hours, end leaked headless Chromes and their clones, remove old Xcode device support, unavailable simulators, CoreDevice's install deltas and the Xcode DerivedData of removed worktrees, erase the walk simulators, and, when the disk is critical, remove Colima data disks no instance has and prune uv's and npm's caches. Nothing else. It needs no app tools. Use when the Free disk space workflow runs on machine.disk_low, or when Alex asks to free disk space.
---

# Free disk space

This is the method of the Free disk space workflow
(`.agents/workflows/free-disk-space.md`). The workflow says what the method is for and
starts it. A person or an agent can also follow it by hand. Where it says that nobody is
at the keyboard, that is true only of a run of the workflow.

**It needs no app tools (#5722).** Each step is a script that decides by what is on disk
and what runs: a lock, a process that works in a folder, the age of the newest file. A
session with no Agent Loops tools (no `list_sessions`, `list_resources` or
`archive_session`) follows it from start to end.

**Work to the target, not to the threshold (#1796, #4699, #6953).** The target is the ok
line, where `machine.disk_ok` fires: the low line plus 10 points of the disk, so 20% free
(about 99 GB on a 494 GB disk) at the default 10% line `machine.disk_low` fires at. A run
ends well past the line and not on it. When the folder moved the low line (Folder Settings ▸
Disk Space), set `AGENTS_FREE_TARGET_PCT` to that percent plus 10. After each of steps 2 to 6, run:

```sh
scripts/free-disk.sh status   # exits 0 at the target, 1 under it
```

When it says `reached`, go to step 7. Otherwise go on to the next step, until the steps
run out. Stopping at the threshold brings the next low event a few hours later.

## Rules for the whole run

- **Run every command from the folder you start in.** It is the shared checkout of
  `main`: never delete, edit, commit, check out, merge, rebase, stash or push anything in
  it. No builds or tests. The shell is zsh: never name a variable `path`, which is `$PATH`
  there and loses every command.
- **Delete only with the scripts named here.** Never `rm` a worktree or anything in one
  yourself. Never run `git worktree remove`, `git clean`, `git reset`, `git checkout`,
  `git branch -d` or `-D` yourself.
- **The scripts keep every worktree that is in use:** one that a process has as its
  working folder, a locked one, a detached HEAD with commits on no branch, and anything
  that is not a linked worktree of this checkout. Uncommitted work goes only with a
  worktree that step 4 removes, after 8 hours with no change.
- **End a process only with `scripts/free-disk.sh chromes`.** It ends only headless
  Chromes with a profile under `/tmp` that started more than 6 hours ago. Never `kill`,
  `pkill` or `killall` anything yourself: not the person's own Chrome, not the sign-up
  Chrome (`--user-data-dir=…/.agents/signups/chrome-profile`), not a build.
- **Never turn workflows on or off**, this one included. Nobody is at the keyboard: do not
  ask; say what you left and why in your ending.

## 1. What happened

The line at the end of this prompt names the event and its details. `level: critical`
means the disk is critical; anything else means low. With no event at all (**Run now**,
or by hand), the disk is critical when less than 5% is free, and low otherwise. Note the
free space now:

```sh
P=$(pwd -P)
df -k "$P" | tail -1
scripts/free-disk.sh status
```

## 2. Bound the build cache

Bound the build cache every worktree shares (#234). It is outside every worktree,
everything in it can be fetched or compiled again, and the script leaves alone each folder
that a running build uses, and Go's build cache while Go runs. The Swift part and Go's part
each have a bound of their own, so a large Go cache never costs the Swift one. It also
removes each folder in Go's temp folder (`go/tmp`) that nothing changed in for 6 hours:
a `go test` or build that is killed leaves them there (12.8 GB on 2026-11-15, #5722).
Since #5726 each `go.sh` run has a folder of its own there, which it removes when it ends,
and the next run removes a SIGKILLed run's: little is left for the prune.

```sh
scripts/build-cache.sh prune                                # low: 15 GB, of it Go 3 GB
scripts/build-cache.sh prune --max-gb 6 --go-max-gb 1       # critical
```

Each build also prunes it to 15 GB behind itself (#2346). The last line says
`after: <GB>; freed <GB>`: put both in the report. If it exits non-zero, say so with its
output in the report, and go on.

Then Go's default build cache (#6462). A `go` that runs without `Go/scripts/go.sh` or
`scripts/lint.sh` (by hand, or gopls) writes to `$(go env GOCACHE)`,
`~/Library/Caches/go-build`, which the prune above does not bound (28 GB on 2026-11-20).
When it is not the shared cache, is over 2 GB and no `go` runs, `gocache` empties it with
`go clean -cache`:

```sh
scripts/free-disk.sh gocache --dry-run
scripts/free-disk.sh gocache                                # --max-gb N moves the 2 GB
```

Put its line in the report.

## 3. Free build output in worktrees nobody builds in

```sh
scripts/free-disk.sh buildoutput --dry-run
scripts/free-disk.sh buildoutput
```

It looks at each worktree under `$P/.agents/worktrees`, and at the Runtime check's tree
(`~/Library/Caches/Agents Nightly Runtimes/tree`, or `$AGENTS_NIGHTLY_HOME/tree`; #6461),
that no process works in and that is not locked, and in which no file, build output too, changed in the last hour. In it, it
deletes each `build`, `.build`, `DerivedData` and `node_modules` folder that git ignores
and tracks nothing in: by the worktree's own `.gitignore`, it is build output, and a build
makes it again. Put each `freed` line in the report.

## 4. Remove worktrees nobody has changed for hours

At both levels (Alex, 2026-11-11, #4699):

```sh
scripts/free-disk.sh worktrees --dry-run   # what it would remove, and why it keeps the rest
scripts/free-disk.sh worktrees
```

It looks at every worktree under `$P/.agents/worktrees`, by the newest file in it (leaving
out `.git` and build output). It removes one with `git worktree remove --force` when all
its work is committed and nothing changed for 4 hours, or when it has uncommitted work and
nothing changed for 8 hours; that work is lost. It keeps the worktrees in use (the rules
above). The branch stays, with every commit. Put each line it prints in the report.

## 5. Leaked Chromes, Xcode, simulators and CoreDevice leftovers

```sh
scripts/free-disk.sh chromes       # ends leaked headless walk Chromes, then removes clones no process uses
scripts/free-disk.sh xcode         # older iOS DeviceSupport builds per device, unavailable simulators
scripts/free-disk.sh deriveddata   # Xcode DerivedData of worktrees that are gone (--dry-run to look first)
scripts/free-disk.sh simulators    # erases the walk simulators that are shut down (--dry-run to look first)
scripts/free-disk.sh coredevice    # the app install deltas CoreDevice keeps (15 GB on 2026-11-11)
```

CoreDevice keeps a delta of each app it installs on a device, so the next install sends
only what changed. `coredevice` removes them when no `devicectl` runs; the next install
sends the whole app.

A walk's headless Chrome that was left running keeps an APFS clone of the Chrome app (up
to 3 GB) until it ends. Walks end theirs now (`Web/test/walk/cdp.mjs`), but one stopped
with `kill -9` still leaks. Xcode keeps a DeviceSupport folder (about 6.5 GB) for every
OS build a device has run; only the newest per device model is kept, and Xcode makes it
again when a device needs it.

Xcode builds each worktree into `~/Library/Developer/Xcode/DerivedData/Agents-<hash>`
(3 to 4 GB), not into the worktree, so steps 3 and 4 do not find it (#2683). `deriveddata`
removes an `Agents-*` folder only when its `info.plist` names a `WorkspacePath` under
`$P/.agents/worktrees/` that is gone, and no running `xcodebuild` names the folder or that
worktree. It also removes an `Agents-*` folder with no `info.plist` that holds only `Logs`
and `SourcePackages`, when no file in it changed in the last hour: a package resolve with no
`-derivedDataPath` leaves one (about 0.9 GB each; 47 on 2026-11-20, #6456).

The run-app skill keeps its walk simulators ("Agents Walk", #2400), and their data grows
with each walk (23.5 GB on 2026-11-15, much of it shared with the runtime). `simulators`
erases each one that is shut down and uses 1 GB or more, when no walk script runs. It
keeps the simulator's name and id; the next walk installs the Remote again.

## 6. Critical only: Colima and package caches

Only at `level: critical`:

```sh
scripts/free-disk.sh colima --dry-run
scripts/free-disk.sh colima
scripts/free-disk.sh caches
```

`colima delete` keeps the instance's data disk for the next `colima start` (15.5 GB on
2026-11-15, #5722). `colima` removes a disk only when `colima list` names no instance, no
`limactl` or `colima` process runs, and nothing changed in the disk for a day; the next
`colima start` makes a new one and pulls or builds its images again. `caches` runs
`uv cache prune` and `npm cache clean --force`; the next install fills them again.

## 7. Report

```sh
df -k "$P" | tail -1
scripts/free-disk.sh status
```

If the target is not reached, say how far short it is, and name the five largest
worktrees left:

```sh
du -sk "$P"/.agents/worktrees/* 2>/dev/null | sort -rn | head -5
```

Then report what `du` cannot see (#5725):

```sh
scripts/free-disk.sh hidden
```

On 2026-11-20, about 120 GB of the Data volume was not visible to `du`. The main suspect
is `/System/Volumes/Data/.fseventsd`, with about 138,000 log files that only root can read.
`hidden` deletes nothing. Clearing `.fseventsd` is Alex's step, because it needs root and
a restart, and Spotlight and Time Machine scan again after it. Do not do it: name the file
count in the report. When the count is high and the target is not reached, ask Alex (with
`ask_question`, once) whether to run `sudo rm -rf /System/Volumes/Data/.fseventsd` and
restart. #6426 tracks a way to measure these folders without Alex.

End with one line per worktree touched (`freed <GB> from <name>` or `removed <name>,
branch <branch> kept`), then the build cache before and after (with the Go temp folders
removed), the Chromes ended and clones removed, the Xcode folders, DerivedData folders,
walk simulators and CoreDevice deltas removed, the Colima disks and caches at the critical
level, then the free space before and after and how far from the target, then what was
kept and why (a process works in it, locked, changed too recently, detached with commits
on no branch, not a linked worktree).

Then, if this session has an `archive_session` tool, call it with no id. Without one, end.
