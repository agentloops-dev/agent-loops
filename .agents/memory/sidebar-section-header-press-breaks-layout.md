---
name: sidebar-section-header-press-breaks-layout
description: An accessibility press on a sidebar Section header stood the Mac window 41,378 points tall; open a page from a row, not the header
metadata:
  type: project
---

On the Mac window (#5567, 2026-11-15), a `walk.py press` on the sidebar's Machines section
header (a Button or an `.onTapGesture` with the button trait) laid the whole
NavigationSplitView out 41,378 points tall, the same number every time, even when the
press's action did nothing. The page it opened was not the cause: opened from a menu or a
row, the same page laid out at the window's size. `walk.py click` on a header or a sidebar
row does nothing in a window that is not key, so it proves nothing either way.

**Why:** a header is the outline's group row; pressing it goes through AppKit's own group
row handling. A real click was not tried.

**How to apply:** give a sidebar page a row with a `SidebarItem` tag (as **All Machines**
is), never a button in a `Section` header. When a walk shows a frame of tens of thousands
of points, measure `walk.py views 6 | grep NSSplitView` after each step to find the step
that does it. See [[walk-socket-in-process-walks]].
