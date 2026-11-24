---
name: no-changed-home-for-agentsd-on-a-mac
description: agentsd with a changed HOME on a Mac raises "Keychain Not Found" dialogs whose Reset button is dangerous
metadata:
  type: project
---

A walk of #6890 ran `agentsd install` on the Mac with HOME set to a scratch folder, to stand in
for a pool machine. There is no Keychain there, so macOS showed "Keychain Not Found" dialogs
(with **Reset To Defaults**), which stay after the process ends; agentsd also hung before it
joined. A Keychain replacement for the host key alone (`-overlay`) is not enough: other code
reads the Keychain too.

**Why:** the dialog can replace the person's login Keychain.
**How to apply:** never change HOME for agentsd on a Mac. A pool machine's walk needs a Linux
machine; else test both sides in Go. See [[seeded-keychain-items-prompt-the-app]].
