---
name: never-link-node-modules-into-a-worktree
description: A symlink from a worktree's node_modules to the main checkout's lets the AgentsHost build's install empty the main checkout's packages
metadata:
  type: feedback
---

Do not link `node_modules` or `Web/node_modules` in a worktree to the main checkout's. The `AgentsHost` build runs `scripts/web.sh dist`, which installs the packages in the worktree: an install (then `npm ci`) emptied the link's target, the main checkout's packages, and put a real folder in the worktree (#5137, 2026-11-12).

**Why:** the main checkout lost every web package until an install ran there again.

**How to apply:** in a worktree with no packages, run `scripts/pnpm.sh install --frozen-lockfile` at its root (since #5628 the web app is in the root pnpm workspace; pnpm's store is shared, so it takes seconds), or run `scripts/web.sh test`. If a link was made, repair the main checkout with the same install there.
