---
diataxis: how-to
devices: [mac, server]
description: Start or continue a session in the folder you are in with the loops command, answer its cards in the terminal, and run it from a script or a CI job.
---

# Start or continue a session from a terminal

`loops` is Agent Loops on the command line. It does one thing: it starts a session, or
continues one, in the folder of the directory it runs in. The session is a normal session.
It runs on the folder's machine, and the Mac window, the iPhone and iPad app and the web
page show it at once. You can start it in a terminal and answer it on your phone.

`loops` keeps no copy of your sessions. Each command reads them from the control plane.

## Before you start

- A control plane that your other clients use, and a paired window or web page.
- A folder on a machine of that control plane. The directory you run `loops` in is that
  folder, or a directory in it (a worktree in it is also correct).
- The `loops` program on your `PATH`. See [Install loops](#install-loops), below.

## Install loops

Do one of these.

**From Agent Loops.app, on a Mac that has it.** The app has `loops` in it. Link it into a
directory on your `PATH`:

```sh
sudo ln -sf "/Applications/Agent Loops.app/Contents/Helpers/loops" /usr/local/bin/loops
```

When the app is in `~/Applications`, use that path, and a directory of your own on `PATH`,
such as `~/.local/bin`, with no `sudo`. The link goes to the app, so when the app updates
itself, `loops` updates too.

**From a release download, on any Mac or Linux computer.** Each release has
`loops-<os>-<arch>-<version>.tar.gz` for `darwin` and `linux`, `arm64` and `amd64`, on the
[releases page](https://github.com/agentloops-dev/agents-releases/releases/latest).
Download the one for your computer, compare it with the release's `SHA256SUMS`, and put
`loops` on your `PATH`:

```sh
tar -xzf loops-linux-amd64-<version>.tar.gz
install -m 755 loops ~/.local/bin/loops
```

The macOS programs are signed with Developer ID and notarized.

## Pair the terminal

You pair a terminal one time, as you pair a window.

1. In a paired window, open **Settings ▸ Control plane**, and click **Show** beside
   **Clients**. On the web page, open **Settings ▸ Control plane ▸ Clients**.
2. Click **Pair a Mac…**, then click **Copy**.
3. In the terminal, type:

   ```sh
   loops pair '<the code>'
   ```

   It says **Paired with** the control plane, as **loops on** this computer's name.
4. Type `loops status`. It shows the control plane, this machine, and the folder of the
   directory you are in.

The pairing is in `~/.config/loops/hosts.json` (or `$XDG_CONFIG_HOME/loops/hosts.json`).
Only you can read it. Anyone who has a copy of it can use your agents, as a paired window
can. `loops unpair` deletes it, and removes the terminal from the control plane's list of
clients.

## Start or continue a session

In the folder:

- `loops "fix the flaky test"` starts a new session with this prompt, and shows it.
- `loops` continues the most recent session here. When the folder has no session, it waits
  for you to type the first prompt.
- `loops -c "and the docs"` continues the most recent session here with a prompt.
- `loops -r <id or title> "…"` continues one session. Use an id as `loops ls` shows it, or
  the session's title, or a part of the title that only one session has.
- `loops ls` shows the 20 most recent sessions here: the id, the title, the state and the
  last update. `loops ls --all` shows all of them, also the archived ones.

A new session starts on Auto. To use a runtime, add `--runtime`, as
`loops --runtime claude "…"`.

The agent's reply comes into the terminal as it writes it. In the chat:

- **Return** sends what you typed, and **Option-Return** starts a new line.
- **Control-C** stops the turn. Push it again in 2 seconds, or when no turn runs, to
  leave.
- **Control-D** leaves the chat. The session continues on its machine. Continue it again
  with the `loops -r` command that `loops` shows.

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

- `loops folder add` adds this directory as a folder of this machine. `loops folder add <dir>`
  adds another directory.
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
- `--detach` starts the session, writes its id, and stops at once. The session continues.
- `--timeout 10m` stops waiting after 10 minutes. The session continues on its machine.

The exit code tells your script how it ended: 0 when the turn ended, 3 when a card waits
and you gave `--on-ask=fail`, and 4 after the timeout. The
[reference](../reference/loops.md#exit-codes) has all of them.

### When the agent asks

By default, print mode waits for an answer. It writes a line on standard error, and you
answer the card on any client, in **Needs you**. With `--on-ask=fail`, it stops with exit
code 3 when a card waits, and tells you how to continue: answer the card, then use
`loops -r <id>`. `loops` never answers a card itself.

## Run it from CI

A CI runner is not a machine of your control plane. The session runs on the machine that
has the folder, and the runner only starts it and follows it.

1. Make sure that the runner can get to the control plane. A control plane on your Mac
   with no public address is not available from a hosted runner. See
   [Run the control plane in the cloud](run-the-control-plane-in-the-cloud.md).
2. On your computer, pair `loops` one time for CI only, with its own configuration
   directory:

   ```sh
   XDG_CONFIG_HOME=$(mktemp -d) sh -c 'loops pair "<the code>" && cat "$XDG_CONFIG_HOME/loops/hosts.json"'
   ```

3. Keep the file's contents as a secret of the CI, for example `LOOPS_HOSTS`. It is a key
   to your agents: do not put it in the repository or in a log.
4. In the job, write the file and run print mode, with the machine and the folder named:

   ```sh
   export XDG_CONFIG_HOME="$RUNNER_TEMP/loops-config"
   mkdir -p "$XDG_CONFIG_HOME/loops"
   printf '%s' "$LOOPS_HOSTS" > "$XDG_CONFIG_HOME/loops/hosts.json"
   chmod 600 "$XDG_CONFIG_HOME/loops/hosts.json"
   loops -p --machine build-mac --folder my-app --on-ask=fail --timeout 30m \
     "the nightly build failed: find why and open a PR with the fix"
   ```

To stop the CI's access, remove its client (**loops on** the computer that paired it) from
the control plane's list of clients, or run `loops unpair` with the same file.

## Where next

- [The `loops` command](../reference/loops.md): every command, flag, exit code and JSON
  object.
- [Answer a question](answer-a-question.md): the cards on the other clients.
- [Connect a window or phone](connect-a-window-or-phone.md): pair the other clients, and
  forget one.
