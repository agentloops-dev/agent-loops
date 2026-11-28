---
diataxis: how-to
devices: [mac, server]
description: Start or continue a session in the folder you are in with the loops command, answer its cards in the terminal, and run it from a script or a CI job.
---

# Start or continue a session from a terminal

`loops` is Agent Loops on the command line. It starts a session, or continues one, in the
folder of the directory it runs in. The session is a normal session. It runs on the folder's
machine, and the Mac window, the iPhone and iPad app and the web page show it at once. You
can start it in a terminal and answer it on your phone.

`loops` uses noun-verb commands. It has three nouns today: `sessions`, `auth` and `folders`.
It keeps a short form for the prompt: `loops "..."` starts a session, and `loops` continues
the newest one.

`loops` keeps no copy of your sessions. Each command reads them from the control plane.

## Before you start

- A control plane that your other clients use, and a paired window or web page.
- A folder on a machine of that control plane. The directory you run `loops` in is that
  folder, or a directory in it (a worktree in it is also correct).
- The `loops` program on your `PATH`. See [Install loops](#install-loops), below.

## Install loops

Do one of these.

**From Agent Loops.app, on a Mac that has it.** The app has `loops` in it, and its first-run
set-up links it to `~/.local/bin/loops` for you. It does not ask a question, and it does
not need an administrator password (#6875). The link goes to the app, so when the app
updates itself, `loops` updates too.

To see the link, or to install it again after you removed it, open **Settings ▸ Machines ▸
This Mac ▸ Command line**, or choose **Agent Loops ▸ Install loops Command…**. The section
shows **Installed at ~/.local/bin/loops** or **Not installed**, with **Install** or
**Remove**, and whether `loops` is paired. When another program is at `~/.local/bin/loops`
(a release download, for example), Agent Loops does not change it.

Agent Loops does not edit your shell's files. When `~/.local/bin` is not on your login
shell's `PATH`, the section shows the line to add, with **Copy**. For zsh, add this line to
`~/.zshrc`:

```sh
export PATH="$HOME/.local/bin:$PATH"
```

To make the link by hand:

```sh
mkdir -p ~/.local/bin
ln -sf "/Applications/Agent Loops.app/Contents/Helpers/loops" ~/.local/bin/loops
```

**From a checkout of this repository.** `.agents/skills/ship-app/scripts/ship.sh
--install-cli` makes the same link in `~/.local/bin` when it installs the app. With
`--no-mac`, it builds `loops` and copies it there.

**From a release download, on any Mac or Linux computer.** Each release has
`loops-<os>-<arch>-<version>.tar.gz` for `darwin` and `linux`, `arm64` and `amd64`, on the
[releases page](https://github.com/agentloops-dev/agents-releases/releases/latest).
Download the one for your computer, compare it with the release's `SHA256SUMS` (which `SHA256SUMS.sig` signs), and put
`loops` on your `PATH`:

```sh
tar -xzf loops-linux-amd64-<version>.tar.gz
install -m 755 loops ~/.local/bin/loops
```

The macOS programs are signed with Developer ID and notarized.

## Pair the terminal

You pair a terminal one time, as you pair a window.

**On a Mac with Agent Loops,** you need no code (#6875). Run any `loops` command, or
`loops auth login` with no code:

1. `loops` asks Agent Loops on this Mac to pair it. It says
   **Asked Agent Loops on this Mac to pair this terminal**, and waits.
2. In the Mac window, **Needs you** shows **Wants to pair loops on <this Mac's name>**, with
   the program that ran `loops` (your shell, for example). Click **Approve**.
3. The window makes a pairing code for this Mac's control plane. When you have a machine
   lock, the window also puts the terminal's key in the lock. Then it gives the code to
   `loops`, and `loops` says **Paired with**. There are no digits to compare.

Only the Mac window can approve this request, because that Mac makes the code. On the web page
and the iPhone and iPad app, the request shows only **Deny**. Agent Loops cannot tell a
terminal from an agent, so approve only a request that you started. Click **Deny** for a
request that you did not start. When you click **Deny**, or nobody answers, `loops` is not
paired.

**On Linux, or on a Mac without Agent Loops,** pair the terminal with a code:

1. In a paired window, open **Settings ▸ Control plane**, and click **Show** beside
   **Clients**. On the web page, open **Settings ▸ Control plane ▸ Clients**.
2. Click **Pair a Client…**, choose **Mac**, then click **Copy**. On the web page, **Copy Command** copies
   the whole `loops auth login` line.
3. In the terminal, type:

   ```sh
   loops auth login '<the code>'
   ```

   It says **Paired with** the control plane, as **loops on** this computer's name.
4. When you have a machine lock, the terminal must join it too: your machines take
   requests only from your devices. `loops auth login` asks to join and shows **Digits:**
   and six digits. On another of your devices (a window, the Remote or the web page), click
   **Compare Digits** on the card, make sure that it shows the same digits, and click
   **Approve**. The terminal says **Approved**. When you have no lock yet, there is no
   step 4.
5. Type `loops auth status`. It shows the profile, the control plane, the client, this
   machine, and the folder of the directory you are in.

The pairing is in `~/.config/loops/hosts.json` (or `$XDG_CONFIG_HOME/loops/hosts.json`).
Only you can read it. Anyone who has a copy of it can use your agents, as a paired window
can. The terminal's key for the machine lock is in the Keychain on a Mac, and in
`~/.config/loops/lock-keys/` (mode 0600) on Linux. `loops auth logout` deletes both, and
removes the terminal from the control plane's list of clients.

A terminal that you paired before it could join the lock (or whose request ran out, or
was refused) asks to join at the next command, as in step 4. When a machine refuses it,
`loops` says **takes requests only from your devices**: type `loops auth status` to ask
again.

## Start or continue a session

In the folder:

- `loops sessions create "fix the flaky test"` starts a new session with this prompt, and
  shows it.
- `loops` continues the most recent session here. When the folder has no session, it waits
  for you to type the first prompt.
- `loops sessions follow` continues the most recent session here.
- `loops sessions follow <id or title>` continues one session. Use an id as
  `loops sessions list` shows it, or the session's title, or a part of the title that only
  one session has. To send a prompt at once, use `loops -r <id or title> "…"`. The short
  forms are `loops -c` for the newest session and `loops -r <id or title>` for one session.
- `loops sessions list` shows the 20 most recent sessions here: the id, the title, the
  state, the runtime, the folder and the last update. `loops sessions list --all` shows all
  of them, also the archived ones.

A new session starts on Auto. To use a runtime, add `--runtime`, as
`loops sessions create --runtime claude "…"`.

The agent's reply comes into the terminal as it writes it. In the chat:

- **Return** sends what you typed, and **Option-Return** or **Control-J** starts a new line.
- **Left** and **Right** move one character. **Option-Left** and **Option-Right** move one
  word.
- **Home** and **End** move to the start and the end of the line.
- **Control-C** stops the turn. Push it again in 2 seconds, or when no turn runs, to
  leave.
- **Control-D** leaves the chat. The session continues on its machine. Continue it again
  with the `loops sessions follow` command that `loops` shows.

[The `loops` command](../reference/loops.md#the-chat) lists every key.

### Answer a card

When the agent asks a question or asks for permission, the terminal shows a card with
numbered answers. Type the number and push **Return**. For a question with more than one
answer, type the numbers, as `1,3`. The same card is also in **Needs you** on every client:
answer it where you are. When you answer it on another client, the terminal says
**answered on another client**.

A card that asks for a secret, a step in a browser or a page to open shows in the terminal,
but you answer it on another client.

### When the directory is not a folder

`loops` says that the directory is not in a folder on this machine. Then do one of these:

- `loops folders add` adds this directory as a folder of this machine.
  `loops folders add <dir>` adds another directory.
- `--folder <name>` names a folder of the machine by its name, or `--folder <path>` by its
  path.
- `--machine <name>` names another machine of the control plane. Use it when this
  computer is not a machine of the control plane.

When you have more than one control plane, `--control <host>` names the one to use.

## Run it from a script

Print mode is for scripts, cron jobs and git hooks. It shows no chat. It starts a new
session here, waits until its turn ends, and then writes the agent's last reply:

```sh
loops -p "update the changelog for this release"
```

- The prompt can also come on standard input: `git log -5 | loops -p`.
- `--json` writes one JSON object on each line: the session first, the entries as they come,
  and the end last. See [the `loops` command](../reference/loops.md) for each object.
- `loops sessions create --detach "…"` starts the session, writes its id, and stops at
  once. The session continues.
- `--timeout 10m` stops waiting after 10 minutes. The session continues on its machine.

The exit code tells your script how it ended: 0 when the turn ended, 3 when a card waits
and you gave `--on-ask=fail`, and 4 after the timeout. The
[reference](../reference/loops.md#exit-codes) has all of them.

### When the agent asks

By default, print mode waits for an answer. It writes a line on standard error, and you
answer the card on any client, in **Needs you**. With `--on-ask=fail`, it stops with exit
code 3 when a card waits, and tells you how to continue: answer the card, then use
`loops sessions follow <id>`. `loops` never answers a card itself.

## Run it from CI

A CI runner is not a machine of your control plane. The session runs on the machine that
has the folder, and the runner only starts it and follows it.

1. Make sure that the runner can get to the control plane. A control plane on your Mac
   with no public address is not available from a hosted runner. See
   [Run the control plane in the cloud](run-the-control-plane-in-the-cloud.md).
2. On your computer, pair `loops` one time for CI only, with its own configuration
   directory. `LOOPS_KEY_FILES=1` keeps its machine lock key in a file of that directory,
   not in the Keychain. When it shows the digits, approve it on another device:

   ```sh
   export LOOPS_KEY_FILES=1 XDG_CONFIG_HOME=$(mktemp -d)
   loops auth login '<the code>' && tar -C "$XDG_CONFIG_HOME" -cz loops | base64
   ```

3. Keep the output as a secret of the CI, for example `LOOPS_CONFIG`. It is a key to your
   agents: do not put it in the repository or in a log.
4. In the job, write the files and run print mode, with the machine and the folder named:

   ```sh
   export LOOPS_KEY_FILES=1 XDG_CONFIG_HOME="$RUNNER_TEMP/loops-config"
   mkdir -p "$XDG_CONFIG_HOME"
   printf '%s' "$LOOPS_CONFIG" | base64 -d | tar -C "$XDG_CONFIG_HOME" -xz
   loops -p --machine build-mac --folder my-app --on-ask=fail --timeout 30m \
     "the nightly build failed: find why and open a PR with the fix"
   ```

To stop the CI's access, remove its client (**loops on** the computer that paired it) from
the control plane's list of clients, or run `loops auth logout` with the same file.

## Where next

- [The `loops` command](../reference/loops.md): every command, flag, exit code and JSON
  object.
- [Answer a question](answer-a-question.md): the cards on the other clients.
- [Connect a window or phone](connect-a-window-or-phone.md): pair the other clients, and
  forget one.
