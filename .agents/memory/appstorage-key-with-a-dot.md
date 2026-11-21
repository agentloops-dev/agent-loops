---
name: appstorage-key-with-a-dot
description: "@AppStorage never hears an outside change to a key with a dot (KVO reads it as a key path); a mirrored display key must have no dot"
metadata:
  type: project
---

`@AppStorage` hears a key's changes by key-value observing on its `UserDefaults`, and KVO reads a dot as a key path, so a view on a key with a dot never redraws when something else writes it (the `DisplayPreferenceMirror`, after a change on another client). It reads the right value only when the view is made again. Found in #6254 (2026-11-21): the Feature Flags switch kept its old state while the host's `flag.commit-trailers` changed; the Sidebar pane (`sidebarSmartGroupsOff`) redrew. The key became `flag:<name>`, with a capability's dots as colons (`FeatureFlags.key`).

**Why:** the walk showed the data was right and only the open view was stale, which looks like a walk or render fault.

**How to apply:** a display choice that a view reads with `@AppStorage` and another client can change gets a key with no dot. `catalog.skills.marketplace` and `catalog.mcp.marketplace` still have dots; they are read in a sheet, where it does not show. To test a live redraw on a scratch app, set the key with `rpc.py $ROOT call preferences/set` and compare two `walk.py shot`s of the control.
