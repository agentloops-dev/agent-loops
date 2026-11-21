---
diataxis: how-to
devices: [mac, server]
description: Install loops, the command-line client, from Agent Loops.app or from a release download, pair it with your control plane, and start or continue a session in the folder you are in.
---

# Use loops in a terminal

`loops` is Agent Loops on the command line. Run it in a folder and it continues that
folder's newest session, or starts one, and follows it live in the terminal. It works on
any Mac or Linux machine that can reach your control plane.

## Before you start

- A control plane that you can pair with: Agent Loops on a Mac, or one that runs in the
  cloud.
- A terminal on macOS or Linux (arm64 or amd64).

## Install loops

Do one of these.

**From Agent Loops.app, on a Mac that has it.** The app carries `loops`. Link it into a
folder on your `PATH`:

```sh
sudo ln -sf "/Applications/Agent Loops.app/Contents/Helpers/loops" /usr/local/bin/loops
```

When the app is in `~/Applications`, use that path, and a folder of your own on `PATH`,
such as `~/.local/bin`, with no `sudo`. The link follows the app: when the app updates
itself, `loops` updates too.

**From a release download, on any Mac or Linux machine.** Each release has
`loops-<os>-<arch>-<version>.tar.gz` for `darwin` and `linux`, `arm64` and `amd64`, on
the [releases page](https://github.com/agentloops-dev/agents-releases/releases/latest).
Download the one for your machine, check it against the release's `SHA256SUMS`, and put
`loops` on your `PATH`:

```sh
tar -xzf loops-linux-amd64-<version>.tar.gz
install -m 755 loops ~/.local/bin/loops
```

The macOS programs are signed with Developer ID and notarized.

## Pair it with your control plane

1. Get a pairing code: in Agent Loops, **Settings ▸ Devices ▸ Pair a Device**, or the
   same place on the web page.
2. Run `loops pair '<code>'`.

`loops status` shows the pairing, the machine and the folder you are in. `loops unpair`
forgets the pairing, here and on the control plane.

## Steps

1. Go to a folder that Agent Loops knows, or add the one you are in with
   `loops folder add`.
2. Run `loops` to continue the folder's newest session, or `loops "<prompt>"` to start a
   new one.
3. Type at the `›` prompt and press Return to send. Option-Return makes a new line.
4. Answer a question or permission card by its number, here or on any other client.
5. Press Control-C to stop the turn. Press Control-D to leave: the session goes on.

`loops ls` lists the folder's sessions, and `loops -r <id>` continues one. For a script,
`loops -p "<prompt>"` prints the agent's last reply, and `--json` prints each entry as a
line of JSON. `loops --help` lists every command and flag.
