---
diataxis: how-to
devices: [mac, server]
description: Install your own packages on each new machine with root.sh, which runs as root on its first boot.
---

# Set up new machines with your scripts

A new machine has none of your own tools. Two scripts of yours set it up: `root.sh`, which
installs packages as root, and `setup.sh`, which sets up your tools and sign-ins as your user.
They are not ready yet: turn on **Machine scripts** in Settings ▸ This app ▸ Features
(epic #7151).

## Where the scripts are

Put them in `~/.agents/machine-setup/` on the machine that makes your new machines: the
machine that carries the provider, such as your Mac. Your dotfiles can keep them. Agent Loops
opens them; it does not edit them.

On the Mac, **Settings ▸ Machines ▸ Machine scripts** lists them, with **Open** (in your
editor) and **Reveal in Finder**. With none there, **New root.sh** and **New setup.sh** write
the example below, and open it. A script that is new, or changed since you approved it for new
machines, has **Review…**: it shows what changed, and **Approve** approves it for new machines.

| | `root.sh` | `setup.sh` |
|---|---|---|
| Runs as | root | the new machine's user |
| When | once, on the machine's first boot | after the machine joins (#7155) |
| Most | 10 KiB | 64 KiB |
| Time limit | 10 minutes, then it is stopped | 10 minutes |
| Secrets | none | only those it declares (#7156) |

Each runs with `bash -e` and no input. Its exit code is the result. Put your checks at the
end, so that a check that fails makes the step fail.

Scripts are not secret: Agent Loops sends `root.sh` to the provider in the machine's first
boot. Do not put a token in a script.

## An example: `gh`

The example is in the ready-made repository
[`agentloops-dev/dotagents`](https://github.com/agentloops-dev/dotagents), as `scripts/root.sh`
and `scripts/setup.sh` (#7461). Each release of Agent Loops pins one tag of it, as it does for
the ready-made workflows, and **New root.sh** and **New setup.sh** write that tag's text. Once
written, the file is yours: a newer tag does not change it.

`root.sh` installs the GitHub CLI from GitHub's own package source. It does nothing when `gh`
is there, and on a machine with no apt it only says so:

```sh
#!/bin/bash
# root.sh: runs once, as root, on each new machine's first boot, with bash -e.
# It goes to the provider in clear: do not put a token here.
# An example: the GitHub CLI, from GitHub's own package source.
set -eu
if command -v gh >/dev/null 2>&1; then
  gh --version
  exit 0
fi
if ! command -v apt-get >/dev/null 2>&1; then
  echo "root.sh: no apt-get here; install gh with this machine's package manager" >&2
  exit 0
fi
export DEBIAN_FRONTEND=noninteractive
install -d -m 755 /etc/apt/keyrings
curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg \
  -o /etc/apt/keyrings/githubcli-archive-keyring.gpg
chmod 644 /etc/apt/keyrings/githubcli-archive-keyring.gpg
echo "deb [signed-by=/etc/apt/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" \
  > /etc/apt/sources.list.d/github-cli.list
apt-get -o DPkg::Lock::Timeout=300 update
apt-get -o DPkg::Lock::Timeout=300 install -y gh
gh --version
```

`setup.sh` signs `gh` in with your secret `GH_TOKEN`, which it declares, and gives the token
to `gh` on stdin only, so it is in no command line or log:

```sh
#!/bin/bash
# secrets: GH_TOKEN
# setup.sh: runs as your user on each new machine, after it joins, with bash -e.
# It reads only the secrets named above, with agents-secret. Keep it safe to run again.
# An example: sign gh in with the token GH_TOKEN from your Secrets.
set -eu
if ! command -v gh >/dev/null 2>&1; then
  echo "setup.sh: gh is not installed; root.sh installs it" >&2
  exit 1
fi
# The token goes from agents-secret to gh on stdin: it is never in a line or a log.
agents-secret GH_TOKEN | gh auth login --with-token
gh auth setup-git
gh auth status
```

Wait for the package lock (`DPkg::Lock::Timeout`): the machine's own first-boot updates can
hold it.

## What runs, and when

You approve each script on a device of your machine lock before it is used, and again each
time it changes. Agent Loops uses a script only when the file is the text you approved. With
no lock, no script runs, and the step says so.

### Approving a script

**New Machine** lists your scripts under **Your scripts, from This Mac**, and reads them again
when you press **Make**. When one is new, or changed since you approved it for new machines,
New Machine shows it first, with what changed, and **Approve and Make**: this Mac's lock key
signs that text for new machines made for you, then the machine is made. When nothing changed,
nothing asks. A line that looks like a token (such as `ghp_…`) is shown in red, and the press
says **Approve and Make Anyway**: keep tokens in Secrets, and read them with `agents-secret`.

- **A warm pool** makes machines with nobody there. When `root.sh` changes, the pool stops
  making and says *root.sh changed*, with **Review root.sh**. After you approve it, the pool
  makes machines again and replaces its unclaimed ones, which ran the older `root.sh`. A
  claim from New Machine asks for `setup.sh` the same way.
- **An agent's `make_machine`** cannot approve a script. When `setup.sh` changed, its card
  says so, and the new machine is made without it. When `root.sh` changed, it asks nothing,
  and tells the agent that you approve it with New Machine.
- **Send Set-up…** on a machine's page shows the same change for that one machine.

This check is against mistakes. It is not a wall: agents on the machine with your scripts run
as your user, and can change the files.

**Root set-up** is a step of the machine's set-up, after **Compare digits**:

- It runs beside the install of Agent Loops, so a slow `apt-get` does not hold the join up.
  The page shows its last line as it runs.
- Installing runtimes and sending projects wait for it.
- When `root.sh` adds your user to a group (such as `docker`), the machine starts your user's
  services again after it, so Agent Loops has the group. Membership of `docker` is the same
  as root.
- When it fails, the step shows its last lines, and **Show Log** shows `root.log`. Fix
  `root.sh` and make a new machine: a machine runs `root.sh` only on its first boot. Or press
  **Copy Root Command…**, run the command as root there, and press **Check** (or **Try
  Again**): the machine reads the new result, then goes on with the steps after it. You can
  still start sessions on it.
- A `root.sh` that restarts the machine is not supported: after 12 minutes with no result,
  the step fails and says so.
- The log and the exit code are in `/var/lib/agents-setup/` on the machine. Only root can
  change them; anyone there can read them.

## Where root.sh runs

| Provider | What it does with `root.sh` |
|---|---|
| DigitalOcean (`digitalocean-api`) | Runs it as root on the first boot, in the droplet's user data. |
| Google Compute Engine (`gcp-compute-api`) | Runs it as root on the first boot, in the startup script, once. |
| Docker (`docker` with an `image`) | Copies it into the new container and runs it there as root (`docker exec -u root`), beside the install. |
| Your own commands | Runs it where your `create` puts `${rootBoot}`: the path of a file of the lines ([The command provider](../reference/machine-providers.md#the-command-provider)). |
| `doctl`, Codespaces, Coder, Google Cloud Workstations, and the other presets | Not run. The step is done with a warning, *‹Provider› can't run root set-up. Put it in the image.* |
| Your own server (a machine no provider made) | Not run by itself. On its page, **Set Up as Root…** gives a command to run there as root, then **Check**. |

`aws-ec2-api` makes no machines, so it is not here.

### Put it in the image

Where a provider can't run `root.sh`, put the packages in the machine's image. One `root.sh`
can do both: run it in the image's build as well, such as `COPY root.sh /tmp/` and
`RUN bash -e /tmp/root.sh` in a `Dockerfile`, or a dev container's `Dockerfile` for
Codespaces. Google Cloud Workstations keeps only `/home` when a workstation stops, so a root
change there must be in its image (its configuration's container).

### Your own server

No provider makes your own server, so Agent Loops can't be root there. On the machine's page,
**Set Up as Root…** shows a command: your approved `root.sh` in base64, run with `sudo`. Run it
on that machine, as root or with `sudo`, then press **Check**: Root set-up reads its result
there. Where your user there has `sudo`, agents there can become root too (epic #7151, the
spec's threat model).

## See also

- [Add a machine provider](add-a-machine-provider.md)
- [Machine providers](../reference/machine-providers.md), `rootBoot`
- A project's own `.agents/setup.sh`: [Send a folder to a server](send-a-folder-to-a-machine.md#the-projects-set-up-script)
