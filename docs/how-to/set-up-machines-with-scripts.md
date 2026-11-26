---
diataxis: how-to
devices: [mac, server]
description: Install your own packages on each new machine with root.sh, which runs as root on its first boot.
---

# Set up new machines with your scripts

A new machine has none of your own tools. Two scripts of yours set it up: `root.sh`, which
installs packages as root, and `setup.sh`, which sets up your tools and sign-ins as your user.
They are an early feature: turn on **Machine scripts** in Settings ▸ This app ▸ Early Features
(epic #7151).

## Where the scripts are

Put them in `~/.agents/machine-setup/` on the machine that makes your new machines: the
machine that carries the provider, such as your Mac. Your dotfiles can keep them. Agent Loops
opens them; it does not edit them.

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

`root.sh` installs the GitHub CLI from GitHub's own package source:

```sh
#!/bin/bash
export DEBIAN_FRONTEND=noninteractive
install -d -m 755 /etc/apt/keyrings
curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg \
  -o /etc/apt/keyrings/githubcli-archive-keyring.gpg
echo "deb [signed-by=/etc/apt/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" \
  > /etc/apt/sources.list.d/github-cli.list
apt-get -o DPkg::Lock::Timeout=300 update
apt-get -o DPkg::Lock::Timeout=300 install -y gh
gh --version
```

Wait for the package lock (`DPkg::Lock::Timeout`): the machine's own first-boot updates can
hold it.

## What runs, and when

You approve each script on a device of your machine lock before it is used, and again each
time it changes. Agent Loops uses a script only when the file is the text you approved. With
no lock, no script runs, and the step says so.

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
