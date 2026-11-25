---
diataxis: reference
description: Every file in ~/.agents, what writes it, and what stays on each machine.
---

# The `~/.agents` folder

Your settings are files in `~/.agents` in your home folder, so they can go with you to
another Mac: copy the folder, sync it, or commit it. A folder's settings are the same
idea, in the folder's own `.agents` folder.

## What is in it

| Path | What it holds | Written by |
| --- | --- | --- |
| `AGENTS.md` | Instructions every agent gets | You |
| `skills/` | Skills every agent gets | You, or **Settings ▸ Agent Configuration** |
| `personas/` | Personas you can start an agent as | You |
| `plugins/` | Plugins every agent gets | **Settings ▸ Agent Configuration** |
| `mcp.json` | Connectors (MCP servers) every agent gets. Secrets are named as `${NAME}`, never held. | You, or **Settings ▸ Agent Configuration** |
| `env` | Environment variables every session gets, `NAME=value` ([below](#env)). Plain text: never a secret. | You, or the person's settings on every machine |
| `marketplaces.json` | Which marketplaces are on | **Manage Marketplaces…** |
| `settings/` | The settings below, one file each | The app |

The top of the folder is configuration that other tools read too. `settings/` is what the
Agent Loops app writes. **Agent configuration, MCP servers, runtime choices and cost limits**
have one fixed scope: they belong to you and sync to every machine of yours. Runtime
sign-ins stay on each machine. Resources are stored in each machine's `resources.json` and
do not sync (#2410).

## `env`

Environment variables for sessions (#5259). There are two files of the same format:

- **`~/.agents/env`**, yours. It is part of your settings, so it follows you: the control
  plane keeps it and writes it to every machine of yours. When you edit it by hand on one
  machine, that machine uses the edited file, and the Environment page offers **Keep for All
  Machines** or **Discard**.
- **`<folder>/.agents/env`**, the repo's. Commit it with the folder's code, beside
  `.agents/mcp.json`; a branch can change it. It is read from the folder or worktree that
  the session runs in. Many repos are public, so never put a secret in it.

The format is one `NAME=value` per line. A `# comment` line just above a variable is its
description. Blank lines are allowed. `export ` and one pair of quotes around a value are
accepted and removed. There is no `${OTHER}` expansion. When a name is on two lines, the
later line wins.

**What a session gets.** A runtime's process starts with the login environment, then the
repo's file, then yours, then Agents' own variables: yours wins over the repo's. The
runtime's tools, the MCP servers it starts and its terminals inherit them, and so does your
own terminal in that folder. Nothing else that agentsd runs gets them (git, `gh`, the
daemon's own MCP servers). A process keeps its variables until its agent starts again.

**Names that are refused** stay in the file, are not given, and `env/list` says why:

- in either file: `PATH`, `HOME`, `USER`, `SHELL`, `TMPDIR`, `AGENTS_*`, `CLAUDE_*`,
  `CODEX_*`, `GEMINI_*`, `ANTHROPIC_*`, `OPENAI_*`, and the names that point a runtime at
  its model, account or sandbox (`AWS_*`, `GOOGLE_*`, `GH_TOKEN` and the like);
- in a repo's file also: `NODE_OPTIONS`, `LD_*`, `DYLD_*`, `BASH_ENV`, `ENV`, `ZDOTDIR`,
  `PYTHONSTARTUP`, `PYTHONPATH`, `PERL5OPT`, `PERL5LIB`, `RUBYOPT` and `GIT_*`, because a
  cloned repo is not trusted;
- a name that the organisation's environment rule (`environment.passThrough`) does not allow.

**A value that looks like a secret** (a secret's name, such as `…_TOKEN`, or a token's
shape) is refused when a client writes it. In a file edited by hand it is still given, and
the Environment page shows it in Needs you: revoke it when it is in a repo's file, and move
it to a secret when it is in yours.

**Bounds:** a file of at most 64 KB and 500 variables, and a value of at most 4 KB. A larger
file is not read, and the page says why.

**The wire** (`Packages/Wire`, capability `env/1`), the person's own clients only:
`env/list` (each file, with every variable's name, value, description, line and state),
`env/write` (one variable of a repo's file, the other lines kept, left not committed; the
audit keeps the name), `env/resolve` (for a folder or a running session, the value and the
layer that wins each name), `env/discard` (write `~/.agents/env` again from your settings)
and the notice `env/changed`. Yours is changed with `user/setSettings`, part `environment`,
which the audit keeps by name too.

## `settings/`

| File | What it holds | Set in |
| --- | --- | --- |
| `policy-yours.json` | Your own rules (#2957): the spending limits, how many workflows may be on, the notice limits and the variables passed through to runtimes. It is signed with your person key, which stays in this Mac's keychain. | **Settings ▸ Cost**, and the other settings that set your rules |
| `policy-yours-seen.json` | The highest revision of `policy-yours.json` kept here, and whether the old files below were moved into it | Agent Loops |
| `retention.json` | How long archived agents are kept | **Settings ▸ Machines ▸ This Mac** |
| `person.json` | What agents call you, and your pronouns | **Settings ▸ Account** |
| `client-permissions.json` | The permission mode of each runtime | Under a supported runtime's prompt |
| `sandbox.json` | The command sandbox of each runtime | **Settings ▸ Agent Runtimes** |
| `modes.json` | The mode last chosen for each runtime | The start form |
| `display.json` | Your display choices, the same on every client: how much of a turn shows, the smart groups that are off, whether Archived folders, Status and Usage are open, the new-session form's runtime and choices, how a diff is drawn, and the marketplaces last chosen | Any client |
| `host.json` | The role of each of your Macs (its control plane here or elsewhere, and its web remote), one entry per Mac, by its local host name. | Agent Loops |

A file you change by hand counts at once, without a restart. A file that cannot be read is
set aside as `<name>.corrupt-<time>`, and the setting goes back to its default. For
`host.json`, the file is left as it is, and Agent Loops starts with the defaults until you
mend the file or remove it.

`retention.json`, `person.json`, `client-permissions.json`, `sandbox.json` and `auto.json`
follow you to each of your machines. Your Mac sets each one once on the control plane
(`user/setSettings`, parts `retention`, `person`, `runtimes` and `auto`), and the control
plane sends it to each machine when it joins and when it changes (`machine/userSettings`).
A machine that is older than these parts gets one call from your Mac instead (#5573).

`limits.json`, `workflow-limit.json` and `runtime-environment.json` are not read now. On
the first start, Agent Loops moves what they hold into `policy-yours.json` and renames each one
`<name>.moved`.

### Your rules on another Mac

`policy-yours.json` is signed with a key that is in the keychain of the Mac that signed it,
not in this folder. When you copy or sync the folder to another Mac, that Mac cannot check
the signature, so it does not use the file: your rules do not apply there until you set
them again. Agent Loops does not write over the file. The first change on that Mac keeps it as
`policy-yours.json.untrusted-<time>`, with your rules in it, and starts a new layer
(#3122).

On a server, the settings are in `~/.agents/settings` of the server's account. The Mac
sends its own settings to each server when it connects.

## What stays on each machine

These are not in `~/.agents`, because they belong to one machine, or because an agent must
not be able to change them:

- **A folder's own rules:** `.agents/policy.json` in the folder, the folder's layer of the
  policy (#2957). Commit it with the folder's code. Set it in **Project Settings ▸ Policy**
  on the Mac, or **Settings ▸ Agents ▸ Policy** on the web page
  ([Set rules for a folder](../how-to/set-rules-for-a-folder.md)).
- **A folder's project:** `"project": "<id>"` in the folder's `.agents/folder.json`, the
  project the folder is in (#2317). Commit it, and a clone on another machine joins the same
  project. A folder that names none gets a new id when it is added or when its machine next
  starts. Set it in **Folder Settings ▸ General ▸ Project** on the Mac and the web page (the folder's menu, **Folder Settings…**). The last copy of each project's slots that the control
  plane sent is in `<root>/project-tools.json` on each machine (#2669).
- **Secrets:** `~/.agents-secrets`, a separate folder that only you can read.
- **The machine's list of folders:** `folders.json`, beside this Mac's data (below). Each entry
  is a path of one machine, so it stays there: a sync could lose a folder (#6744).
- **This Mac's data:** agents, transcripts, and the sleep, resource, approval and
  retention-clock files, in `~/Library/Application Support/Agents`
  (`~/.agents-server/root` on a server). Its backups go to `backups/` in that folder unless
  you choose another: the same disk, so they do not survive the loss of the disk. Choose a
  second disk or a synced folder in **Settings ▸ Machines ▸ This Mac ▸ Backups**, or, on a
  server, back up to the control plane
  ([Back up and restore a machine](../how-to/back-up-and-restore.md)).
- **The control plane's folder** (Agent Loops): `~/Library/Application Support/Agents Control`.
  It holds the store, `secrets-key` and the certificate in `tls/`.
  The control key is in the keychain. To back them up together, and restore
  them on a new Mac, see [Back up and restore a control plane](../how-to/back-up-a-control-plane.md).
- **Caches and logs:** the system's cache and log folders. You can delete them at any time.
- **Each device's own choices:** folder folds, drafts, the new-session form's folders,
  window sizes, the selected folder, and pairing.

A scratch copy of the app, started with its own root, keeps its settings in
`<root>/settings` and never writes the real `~/.agents`, unless `AGENTS_PERSONAL_HOME`
names a home folder for it.
