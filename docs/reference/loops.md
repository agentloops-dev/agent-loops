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
| `loops` | Continues the most recent session of the folder here, in the chat. With no session, it waits for the first prompt. When stdin is not a terminal and the session is idle, it writes the session's last entries and stops. |
| `loops "<prompt>"` | Starts a new session here with this prompt, and shows it in the chat. |
| `loops -c ["<prompt>"]` | Continues the most recent session here, with the prompt when you give one. Also `--continue`. |
| `loops -r <id or title> ["<prompt>"]` | Continues one session. Also `--resume`. |
| `loops -p "<prompt>"` | Print mode: starts a new session here, waits until its turn ends, and writes the agent's last reply. Also `--print`. |
| `loops --detach "<prompt>"` | Starts a new session here, writes its id, and stops at once. |
| `loops ls [--all]` | The 20 sessions here with the most recent activity. `--all` shows all of them, also the archived ones. |
| `loops folder add [dir]` | Adds this directory, or `dir`, as a folder of this machine. |
| `loops pair '<code>'` | Pairs this terminal with a control plane, with a client code from **Settings ▸ Control plane ▸ Clients ▸ Pair a Mac…**. When you have a machine lock, it then asks to join it, shows six digits, and waits until you approve it on another device (below). |
| `loops unpair` | Deletes the pairing and the machine lock key here, and removes this terminal from the control plane's clients. When the control plane does not answer, it deletes them here only. |
| `loops status` | The pairing, this machine, and the folder of the directory here. |
| `loops help` | The commands, flags and exit codes. Also `-h` and `--help`. |

Flags can come before or after the command. A first word that is not a command is the
start of a prompt: `loops fix the test` starts a session with the prompt "fix the test".

### Which session `-r` finds

`-r` takes, in this order:

1. a session's whole id, as `-p --json` writes it, which takes one read; or the start of an id, as `loops ls` shows it (6 characters or more);
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
| `--mode <level>` | a new session | The permission that a new session starts at: `plan`, `ask`, `approve-for-me` or `bypass-permissions`. When you do not give it, the session starts at the model and permission that you chose last on a start form on any client (#3798, #6651). With no choice kept, the default is Ask, or your policy's limit. |
| `--debug` | all | When `loops` cannot get to the control plane or the machine, it also writes the full error on a line that starts with `Debug:`. |
| `--all` | `ls` | All the sessions, in pages of 20, also the archived ones. |
| `--json` | `-p`, `--detach` | One JSON object on each line (below). |
| `--on-ask=wait` | `-p` | When a card waits, waits for an answer on any client. This is the default. |
| `--on-ask=fail` | `-p` | When a card waits, stops with exit code 3. |
| `--timeout <duration>` | `-p` | Stops waiting after this time, as `90s` or `10m`, with exit code 4. The time starts when the session starts. The session continues. It is also the longest time that `loops` tries to connect again (exit code 6). |

`--json`, `--on-ask` and `--timeout` without `-p` or `--detach` are an error. `-p` always
starts a new session: it does not take `-c` or `-r`.

## Exit codes

| Code | Meaning |
|---|---|
| 0 | Done: the turn ended, or the command did its work. |
| 1 | An error: the control plane or the machine did not answer, the session stopped with an error, or the command was not correct. Nothing changed when `loops` could not get to the control plane. |

When `loops` cannot get to the control plane, it gives the cause in a few words, as
`Cannot reach control.example (it refused the connection).`:

| Words | Cause |
|---|---|
| `timed out after 10 s` | The control plane took the connection, but did not answer. |
| `timed out` | The time to connect went by. |
| `it refused the connection` | Nothing listens at the address. |
| `no such host` | The host name is not in DNS. |
| `could not look up the host` | DNS did not answer. |
| `its certificate is not the paired one` | The certificate is not the one that the pairing code pinned. |
| `it takes requests only from your devices…` | The machine holds your machine lock, and this terminal is not in it. Type `loops status` to ask to join. |

Other errors keep their own words. `--debug` adds the full error.
| 2 | The directory is not in a folder of the machine, or `--folder` names no folder. |
| 3 | A card waits for an answer, or this terminal must join your machine lock, with `--on-ask=fail`. |
| 4 | The time of `--timeout` went by. |
| 5 | This terminal is not paired, or not with the control plane of `--control`. |
| 6 | The connection to the control plane, or from it to the session's machine, was lost and did not come back (below). The session continues. |

## When the connection is lost

When the connection to the control plane is lost during a turn, the chat and print mode
write "reconnecting…" (print mode on standard error) and connect again. The time between
two tries starts at 0.25 seconds and doubles, to 5 seconds at most. When the machine of the
session is offline, as it is for a short time after the control plane starts again, they
wait for it in the same way. They try for the time of `--timeout`, or for 1 minute without
it. When the connection comes back, they read the transcript from the first entry that
they did not read, so no entry is lost and no entry shows two times. When it does not come
back, `loops` stops with exit code 6.

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
| `lost` | 6 | The connection was lost and did not come back. |
| `gone` | 1 | The machine refused a read, or the session was deleted. |

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

## The machine lock

A machine that holds your machine lock takes requests only from your devices
(`specs/3399-machine-lock`). `loops` has a key of its own for each pairing. When the lock
does not name that key, the first command (or `loops pair`) asks to join, writes
`Digits:` and six digits, and waits until another of your devices compares them and
approves, or until the request runs out. Then each request to a machine goes on a
channel that the key opened (`lock/hello`), and `loops` takes an answer only when its MAC
checks. `loops` checks the lock that the control plane sends against the head it saw last,
and refuses a lock that does not grow from it.

In print mode with `--on-ask=fail`, `loops` does not ask to join: it stops with exit 3.
When the link comes back after a loss, it does not ask either.

| Variable | What it does |
|---|---|
| `LOOPS_KEY_FILES` | `1`: the machine lock key is in a file in `lock-keys/` beside `hosts.json`, also on a Mac. For a pairing that you copy to CI. |

## What it keeps

| File | What it is |
|---|---|
| `~/.config/loops/hosts.json` | The pairing: one entry for each control plane, with its key, and the head of your machine lock that it saw last. Mode 0600. With `XDG_CONFIG_HOME` set, it is `$XDG_CONFIG_HOME/loops/hosts.json`. |
| The Keychain, `dev.agentloops.loops.lock` | On a Mac, the machine lock key of each pairing. Only `loops` reads it. |
| `~/.config/loops/lock-keys/<client>.key` | On Linux, or with `LOOPS_KEY_FILES=1`, the machine lock key of each pairing. Mode 0600. |

`loops` keeps no other file and no copy of your sessions. A session that it starts says
that it started from the command line (`startedVia`: `cli`, or `cli-print` in print mode
and with `--detach`). A terminal is in the control plane's list of clients as **loops on**
the computer's name.
