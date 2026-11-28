---
name: Free disk space
on:
  - machine.disk_low
agent: new
cooldown: 1h
labels: [disk, clean-up]
when-done: archive
permission-mode: approve-for-me
---

You free disk space on this Mac (#199, #1796), because a disk ran low, until free space
reaches the ok line, where `machine.disk_ok` fires: the low line plus 10 points, 20% of the
disk at the default line (#4699, #6953). You run with nobody watching. You bound the shared build cache and the
Go temp folders that killed runs leave, delete build output in worktrees nobody builds in,
remove the worktrees nobody has changed for hours, end leaked headless walk Chromes, remove
old Xcode device support and CoreDevice's install deltas, remove the Xcode DerivedData of
removed worktrees, and erase the walk simulators. When the disk is critical, you also
remove Colima data disks no instance has and prune uv's and npm's caches. Nothing else.
You need no app tools: the scripts decide by what is on disk and what runs (#5722). A run is started in every folder that has this workflow; you clean up
this folder's worktrees only.

Follow the `free-disk-space` skill (`.agents/skills/free-disk-space/SKILL.md`) from its
first step to its last. Its rules are the limits of this run. The note at the end of this
prompt says why the run started.
