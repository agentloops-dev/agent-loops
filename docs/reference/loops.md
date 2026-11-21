---
diataxis: reference
description: Every command, flag, exit code and JSON object of loops, the command line client, and the file it keeps.
---

# The `loops` command

`loops` is a client of the control plane, as the Mac window is one. It works in the folder of
the directory it runs in, on the machine of the control plane that is this computer. For the
steps, see [Start or continue a session from a terminal](../how-to/use-loops-in-a-terminal.md).
`loops help` shows a short form of this page.

## Commands

| Command | What it does |
|---|---|
| `loops` | Continues the most recent session of the folder here, in the chat. With no session, it waits for the first prompt. |
| `loops "<prompt>"` | Starts a new session here with this prompt, and shows it in the chat. |
| `loops -c ["<prompt>"]` | Continues the most recent session here, with the prompt when you give one. Also `--continue`. |
| `loops -r <id or title> ["<prompt>"]` | Continues one session. Also `--resume`. |
| `loops -p "<prompt>"` | Print mode: starts a new session here, waits until its turn ends, and writes the agent's last reply. Also `--print`. |
| `loops --detach "<prompt>"` | Starts a new session here, writes its id, and stops at once. |
| `loops ls [--all]` | The 20 sessions here with the most recent activity. `--all` shows all of them, also the archived ones. |
| `loops folder add [dir]` | Adds this directory, or `dir`, as a folder of this machine. |
| `loops pair '<code>'` | Pairs this terminal with a control plane, with a client code from **Settings ▸ Control plane ▸ Clients ▸ Pair a Mac…**. |
| `loops unpair` | Deletes the pairing here, and removes this terminal from the control plane's clients. When the control plane does not answer, it deletes the pairing here only. |
| `loops status` | The pairing, this machine, and the folder of the directory here. |
| `loops help` | The commands, flags and exit codes. |

Flags can come before or after the command. A first word that is not a command is the
start of a prompt: `loops fix the test` starts a session with the prompt "fix the test".

### Which session `-r` finds

`-r` takes, in this order:

1. the start of a session's id, as `loops ls` shows it (6 characters or more);
2. a session's whole title, with no difference between capital and small letters;
3. a part of a title that only one session has.

It looks in the folder here, or in all the machine's sessions when the directory is not
in a folder. When more than one session agrees, `loops` shows up to 5 of them, and you
name one by its id.

## Flags

| Flag | Use with | What it does |
|---|---|---|
| `--folder <name or path>` | all but `pair` | Works in this folder, not the folder of the directory here. A word with no `/` is a folder's name on the machine; anything else is a path. |
| `--machine <name or id>` | all but `pair` | Works on this machine of the control plane, not on this computer. |
| `--control <host>` | all but `pair` | Uses this control plane, when the terminal is paired with more than one. |
| `--runtime <id>` | a new session | The runtime that a new session starts on, as `claude` or `codex`. Auto when you do not give it. |
| `--all` | `ls` | All the sessions, in pages of 20, also the archived ones. |
| `--json` | `-p`, `--detach` | One JSON object on each line (below). |
| `--on-ask=wait` | `-p` | When a card waits, waits for an answer on any client. This is the default. |
| `--on-ask=fail` | `-p` | When a card waits, stops with exit code 3. |
| `--timeout <duration>` | `-p` | Stops waiting after this time, as `90s` or `10m`, with exit code 4. The time starts when the session starts. The session continues. |

`--json`, `--on-ask` and `--timeout` without `-p` or `--detach` are an error. `-p` always
starts a new session: it does not take `-c` or `-r`.

## Exit codes

| Code | Meaning |
|---|---|
| 0 | Done: the turn ended, or the command did its work. |
| 1 | An error: the control plane or the machine did not answer, the session stopped with an error, or the command was not correct. Nothing changed when `loops` could not get to the control plane. |
| 2 | The directory is not in a folder of the machine, or `--folder` names no folder. |
| 3 | A card waits for an answer, with `--on-ask=fail`. |
| 4 | The time of `--timeout` went by. |
| 5 | This terminal is not paired, or not with the control plane of `--control`. |

## Print mode's JSON

With `--json`, print mode writes one object on each line, in this order:

| `type` | Fields | When |
|---|---|---|
| `session` | `id`, `folder`, `machine`, `startedVia` (`cli-print`) | First, when the session starts. |
| `entry` | `role` (`user`, `agent` or `tool`), `text` | For each message and tool call. An agent's reply is one entry, when it is complete. |
| `card` | `text` | When a card waits, with `--on-ask=wait`. Each card shows one time. |
| `end` | `state`, `exit`, and `reason` or `card` | Last. |

The `state` of `end`:

| `state` | `exit` | Meaning |
|---|---|---|
| `done` | 0 | The turn ended. |
| `stopped` | 1 | The session stopped with an error. `reason` says why. |
| `needs-you` | 3 | A card waits, with `--on-ask=fail`. `card` says which. |
| `timed-out` | 4 | The time of `--timeout` went by. |
| `gone` | 1 | The connection to the control plane or the machine ended, or the session was deleted. |

With `--detach --json`, `loops` writes only the `session` line. Without `--json`, `--detach`
writes only the id. When a command does not end with 0, its message is on standard error.

Print mode takes the prompt from standard input when there are no words after the flags and
standard input is not a terminal. The limit is 1 MB.

## The chat

| Key | What it does |
|---|---|
| Return | Sends what you typed, or answers the card. |
| Option-Return | Starts a new line. |
| Control-C | Stops the turn. Again in 2 seconds, or when no turn runs, leaves. |
| Control-D | Leaves. The session continues on its machine. |

A card shows numbered answers. Type a number, or numbers as `1,3` when the card takes more
than one. Push Return alone to leave out an answer that is not necessary. A card that asks
for a secret, a step in a browser or a page to open is answered on another client.

## Colour

The chat has colour only on a terminal.

| Variable | What it does |
|---|---|
| `NO_COLOR` | When it is set, no colour. `TERM=dumb` also gives no colour. |
| `COLORTERM` | `truecolor` or `24bit`: 24-bit colour. Otherwise, the nearest of 256 colours. |
| `LOOPS_THEME` | `light` or `dark`: the palette. When it is not set, `loops` reads `COLORFGBG`, or asks the terminal for its background. |

## What it keeps

| File | What it is |
|---|---|
| `~/.config/loops/hosts.json` | The pairing: one entry for each control plane, with its key. Mode 0600. With `XDG_CONFIG_HOME` set, it is `$XDG_CONFIG_HOME/loops/hosts.json`. |

`loops` keeps no other file and no copy of your sessions. A session that it starts says
that it started from the command line (`startedVia`: `cli`, or `cli-print` in print mode
and with `--detach`). A terminal is in the control plane's list of clients as **loops on**
the computer's name.
