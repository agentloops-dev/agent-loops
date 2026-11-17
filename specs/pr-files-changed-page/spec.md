# Files changed is a full page with coloured diffs

Epic #5474. Stories: tokens #5476, web #5477, Mac #5478, Remote #5479.

The first spec and its wireframes (`files-wide.svg`, `files-narrow.svg`) were approved by Alex
but never committed, and were lost. This spec is written again from the epic's Proposal and the
stories' text (the lead's decision on #5474). Each client's page PR waits for Alex's approval
with screenshots, so the screenshots stand in for the wireframes.

## Problem

On a pull request's page, **Files changed** opened the diff in a small modal sheet
(`AppViewDiffPage` on the Mac and the Remote, `ViewDiff` on the web page). Its lines were marked
only by sign, weight, strike and a faint grey wash (`CodeInk.changedWash`, `.file-diff`, #1472).
People expect what GitHub gives: a full page, and green and red lines.

## The page

1. **Files changed pushes a page** in the pull request's place, on all three clients. No sheet.
   Back goes to the pull request (‹ #4402, as the Checks page does on a narrow page).
   - Mac: the place in the toolbar is *Pull requests › #12 › Files changed*; *#12* opens the
     pull request, and the window's Back does too. Refresh is at the toolbar's end.
   - Web: `#/work/pr/<ref>/files`, with the same trail.
   - Remote: a pushed page with ‹ *#12*.
2. **The line at the top:** *5 files changed +14 −9*, the counts in the forge's green and red,
   then **Unified | Side by Side** when the diff is wide enough for two columns (820 pt), and
   **Open on GitHub**. When the forge cut the diff, one line says so.
3. **A file list on a wide page** (900 pt and over, with more than one file): a column of
   260 pt at the page's leading edge, titled *Files*. Each row: the change's glyph (added,
   removed, renamed, modified), the file's name, its folder under it, and *+5 −2*. A click
   goes to the file (and opens it when folded) and marks the row.
4. **Files ▾ on a narrow page:** a menu at the start of the top line with each file and its
   counts; a choice goes to the file.
5. **A header per file that stays:** the fold chevron, the path (*old → new* for a rename),
   the change when not *modified*, and the counts. It stays at the top while its file's lines
   scroll by. A click on it folds the file to its header, and again opens it. The header and the
   lines are one card.
6. **Coloured lines:** an added line on a green wash with a green bar at its leading edge, a
   removed line on a red wash with a red bar; the words that changed within a line on a
   stronger green or red. The `+` and `−` signs stay, in the bar's colour, so colour is never
   the only mark. Line numbers, folds of unchanged lines and the breaks between hunks are as
   before (#1472).
7. **Colours** come from `design/tokens.json`'s `color.diff` (#5476): `added` and `removed`,
   each a `wash`, a `bar` and a `word`, light and dark (`Paper.diff…` in Swift,
   `--paper-diff-…` in CSS). The counts use `forge.open` and `forge.closed`.
8. **States:** reading (a spinner), the diff, a file with no lines (its note, such as *Binary
   file*), and could not read (*Could not read the files of #12*, the error, **Try again** and
   **Open on GitHub**). The diff is read each time the page opens and is not kept, so the page
   has no "Last update" line (#5139).

## Decisions

- Decided: a page, not a sheet, because people expect a page; it supersedes the "button opens
  today's sheet" line in `specs/4328-work-look/pull-request.md` (#5474).
- Decided: the same coloured diff wherever a diff is drawn (the Review page, the chat's diff
  rows, a view's `agents/open-diff` sheet), because two looks for one diff is worse than one
  (#5474).
- Decided: no line comments in the diff yet (as `pull-request.md`, #4408).
- Decided (Mac, #5478): the Files changed page is a Work place (`WorkPlace.files`), so the
  window's Back and Forward go through it as through any other page.
- Decided (Mac, #5478): strike, dimming and bold no longer mark removed and added lines; the
  sign and the colour do, as on GitHub.

## Clients

Core: Mac, Remote and web. No wire change: `forge/diff` already returns files and hunks. The
page's views are in `Shared/UI` (`DiffFilesView`, `WorkFilesPage`), so the Remote (#5479) draws
the same page; until it does, the Remote keeps the sheet, which already draws `DiffFilesView`.

## The web page (#5477)

`Web/src/views/WorkFilesPage.tsx`, words in `Web/src/model/filesPage.ts`. Where it differs from
the page above, and why:

- **Back:** ‹ *#21* above the header, or Escape, as the web's Checks page. The page is not in the
  address yet (no `#/work/pr/<ref>/files`): it is a state of the pull request's page, as the
  Checks page is.
- **The top line:** *Files changed*, *6 files · +11 −6*, then **Unified | Split**, **Collapse all**
  (**Expand all** when every file is folded) and **On GitHub**.
- **The file list from 700 px**, not 900, with a **filter** at its top (every word in the path, any
  case; *No file matches*). It stays in view as the diff scrolls.
- **Files ▾ under 700 px** opens the same list, with the filter, in a sheet; a file closes the
  sheet and goes to its card.
- **States:** placeholder cards while it reads, *No files changed.* when there are none, and
  *Last update* under the header: the web page says when the diff was read (#5139).
- Decided (web, #5477): the list from 700 px, the width the Work page already changes at, and
  the filter, because a long pull request's list needs one. Alex approved the page's screenshots
  on #5602.
