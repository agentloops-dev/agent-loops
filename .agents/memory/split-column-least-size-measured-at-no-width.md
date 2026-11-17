---
name: split-column-least-size-measured-at-no-width
description: A NavigationSplitView column's least height is measured at almost no width, so fixed-height wrapped text in it (a foot banner) stood the Mac split view 2116 pt tall (#5932)
metadata:
  type: project
---

#5932 (2026-11-17): with this Mac's host not answering, the main window's sidebar foot shows a
`PaneBanner` whose words are `.fixedSize(horizontal: false, vertical: true)`. AppKit asks a
split view column for its least size at almost no width, so the text wrapped a character to
a line and the column reported about 2,100 pt. The `NSSplitView` stood 2116 pt tall at every
window height; `windowFloor` kept the window at its own height, so the split view was centred
in it: the sidebar blank, the page's top and bottom cut off, on every page, and it stayed
after the host came back. Before #5741 the same least height grew the window past the screen
(#5739).

**Why:** a flexible frame at the window's root does not cap a column's own least size: the
split view sizes from the columns.

**How to apply:** every split view column gets `.columnFloor()` (`App/Sources/WindowFloor.swift`).
To reproduce: run-app, then `kill` the scratch daemon (not the control plane), and read
`walk.py <root> views 6 | grep 'NSSplitView '`; it must be the window's height. See
[[sidebar-section-header-press-breaks-layout]] and [[animated-top-inset-loops-appkit-layout]].
