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
- When it fails, the step shows its last lines. Fix `root.sh` and make a new machine: a
  machine runs `root.sh` only on its first boot. You can still start sessions on it.
- A `root.sh` that restarts the machine is not supported: after 12 minutes with no result,
  the step fails and says so.
- The log and the exit code are in `/var/lib/agents-setup/` on the machine. Only root can
  change them; anyone there can read them.

DigitalOcean and Google Compute Engine run `root.sh`. A provider that can't run it shows the
step done with a warning, *‹Provider› can't run root set-up. Put it in the image.*

## See also

- [Add a machine provider](add-a-machine-provider.md)
- [Machine providers](../reference/machine-providers.md), `rootBoot`
- A project's own `.agents/setup.sh`: [Send a folder to a server](send-a-folder-to-a-machine.md#the-projects-set-up-script)
