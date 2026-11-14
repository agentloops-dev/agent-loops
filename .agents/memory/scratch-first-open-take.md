---
name: scratch-first-open-take
description: "A scratch first open (the question page) needs the build opened with --walk and AGENTS_ROOT, not launch.sh; what its host does and does not get, and the traps found in the new-app rehearsals (#5514)"
metadata:
  type: project
---

Found 2026-11-14 rehearsing the new-app video (#5514, three runs, no take).

- **The question page needs the app's own first open.** `launch.sh` pairs the window beforehand, so `HostScenes.firstOpen` is nil and "Let's build your idea." never shows (setting `firstBuild.asking` does not help). Open a copy of the build with `--walk run-<slug>` and `AGENTS_ROOT=<root>` (ports off the real app's), as `make.py`'s `FIRST_OPEN` does. The host is then in `<root>/host` (its socket `<root>/host/daemon.sock`).
- **Its daemon job carries only what LocalServices names.** Since #5514 a Debug scratch host also takes `HOST_NAME`, `AGENTS_PERSONAL_HOME`, `GH_CONFIG_DIR` and `GIT_CONFIG_GLOBAL` from the `open --env`. Without `AGENTS_PERSONAL_HOME` its agent reads the person's own `~/.agents` (a stale `start-a-new-app`, #5559, and the person's memories). `person/set Sam` is still needed, or cards say "Alex". The browser approval card still names the real Mac ("Browser on Alex's MacBook Air"): the take's private-words check would fail on it.
- **Its browser is its own:** agentsd keeps the profile at `<root>/host/browser/chrome-profile`, signed in to nothing. Copying the agents' `Default` + `Local State` (13 MB) into it works: same Keychain key.
- **Its GitHub connection outlives the root:** Keychain service `agents.connection.<sha256("<root>/host")[:8 hex]>`. Delete it before each take, or Connect GitHub shows as done.
- **The token sheet's SecureField takes no walk input** (keys and AX set both leave it empty: the window is never key). Connect by `connections/sign-in/token` over the socket.
- **Product blockers found:** the GitHub App cannot create repositories (#5563), the gate needs a paid plan on a private repository (#5564), and Google Cloud's free trial wanted a $30 prepayment on the Privacy.com card (Alex declined, 2026-11-14).

Related: [[screen-recording-catches-the-real-window]], [[scratch-host-dies-on-a-reused-slug]], [[type-into-a-scratch-window-by-pid]].
