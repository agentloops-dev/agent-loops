---
name: toolbar-reading-layout-loops-appkit
description: "The Mac window quit on the Update Constraints loop through its toolbar (#5947): a toolbar must not read a width measured in layout, and a focused value must be a class or Equatable"
metadata:
  type: project
---

#5947 (2026-11-17): the second route into the loop of #5189, at 1470x834. The exception's own
backtrace (the window's crash note, `~/Library/Logs/Agents/crashes/`, symbolicated with `atos -o
<framework> -l <base from the .ips usedImages> <addr>`) ran `NSHostingView.layout()` →
`ToolbarBridge.preferencesDidChange` → `Toolbar.LocationStorage.updatedVendedItems(newFocusedValues:)`
→ `NSHostingView.requestUpdate` → `setNeedsUpdateConstraints`. So the toolbar changed in each
layout pass. The .ips's crashed thread shows only AppKit; the exception backtrace in the crash
note is the one that names the culprit.

**Why:** the main window's and the folder window's toolbars built `SidebarToggle(windowWidth:
frame.windowWidth)`, a width set by `onGeometryChange` during layout, so each new width built the
whole toolbar again. `PageZoomAction`, a focused value holding a `Binding`, was not `Equatable`.

**How to apply:** a toolbar item that needs a measured size reads it in its own view
(`SidebarToggle` reads `frame.windowWidth` itself). Every `FocusedValues` entry is a class or
`Equatable`. `ToolbarLayoutLoopTests` (AgentsKitRepoTests) scans for both. Not reproduced on a
scratch window. See [[animated-top-inset-loops-appkit-layout]].
