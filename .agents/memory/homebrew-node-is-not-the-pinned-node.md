---
name: homebrew-node-is-not-the-pinned-node
description: A Homebrew Node links the system's zlib, so it gzips to other sizes than CI's official Node; checks.sh attest does not attest web tasks with it (#5774)
metadata:
  type: project
---

Homebrew's Node (`/opt/homebrew/Cellar/node/<v>`) links zlib, brotli, OpenSSL and ICU from the system. CI uses the official build (actions/setup-node), which has its own copies. At the same version, the two gzip the web bundle to different sizes: 637216 bytes here, 640413 on CI, for the same main (2026-11-15). So the budget test in `Web/test/perfBudgets.test.mjs` passed on the Mac and failed on CI. PR #5767 merged over the budget on such an attestation.

**Why:** an attestation must stand for CI's run. Since #5774, `scripts/checks-attest.mjs` (`nodeBuild`) treats a Node with system libraries as "not the pinned Node". With that Node, a task that reads `Web/.node-version` (web:build, web:lint, web:test) is not attested, and CI runs it.

**How to apply:** if a lane's attestation says `Web/.node-version says 26.x, this machine has 26.x with the system's zlib, …`, that is this rule and not a bug. To attest web tasks on this Mac, put an official Node build of the pinned version first on PATH. `scripts/node.sh` uses nvm's when nvm is installed. Installing nvm is Alex's choice. Never measure a gzip budget by hand with a Homebrew Node.
