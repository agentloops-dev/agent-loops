---
diataxis: how-to
devices: [mac, iphone, ipad, web, server]
description: Make anything that can start and stop a machine a machine provider - a preset, or your own commands.
---

# Add a machine provider

A machine provider lists machines, starts them and stops them. Agent Loops ships one, the
**command provider**, and there are two ways to set it up: a preset, or your own commands.
Neither needs any code. Agent Loops no longer takes an MCP server of your own as a provider,
with or without a tool map (#6623).

## Where a provider runs

A provider runs on one of your machines, which **carries** it (#3398). Its server, its
commands and its CLI run there, as you, and its tokens and keys are kept there, in
`~/.agents-secrets/provider-secrets.json`, which only you can read. The control plane keeps
the provider's name, kind and settings, never a secret. It sends each list, start, stop,
make and delete to the machine that carries the provider, and that machine answers.

- Add Provider asks which of your machines carries it. With none chosen, it is your first
  machine that is online, by name. The machine must be online when you add the provider.
- The tokens you type go through the control plane to that machine once, and the control
  plane keeps none. Until clients seal secrets to machines (#3397), the control plane can
  read them as they pass.
- When the machine that carries a provider is offline, the provider's machines can't be
  listed, started, stopped, made or deleted. The Providers page says so on the provider.
- To move a provider to another machine, edit it, choose the machine and type its secrets
  again: the old machine keeps them until you remove the provider.
- Removing a provider tells its machine to delete its secrets. A machine that is offline
  then keeps them: delete the provider's entry from that file by hand.
- A provider added before this change has no machine: remove it and add it again.

No provider needs a cloud's workload identity (OIDC federation) instead: every preset and
your own commands run on a machine of yours.

## A preset, or your own commands

Agent Loops ships one provider, the **command provider**. Each of its machines is a few
shell commands or web requests, and its presets fill those in for the common tools.

DigitalOcean, GitHub Codespaces and Coder can be reached in two ways. Add Provider shows
**Its API** first: a token, and nothing to install (#1119). **Its CLI** uses the CLI, signed
in, as before.

| Preset | Through its API, it needs | Through its CLI, it needs | Machines |
| --- | --- | --- | --- |
| Docker Containers | — | `docker` | the containers you name, or the containers labelled `agents.machine` |
| DigitalOcean Droplets | a token with custom scopes: droplet read and update (and create and delete, to make and delete droplets) | `doctl`, signed in | droplets with a tag |
| GitHub Codespaces | a token: fine-grained with Codespaces lifecycle admin, or classic with `codespace`; and `gh` for ssh, after `gh codespace ssh --config >> ~/.ssh/config` (the `agents-control` image has `gh` and does this itself) | `gh`, signed in | your codespaces, or one repository's |
| Coder Machines | the deployment's address and a session token (`coder tokens create`); and `coder` for ssh, after `coder config-ssh` | `coder`, signed in, after `coder config-ssh` | your Coder machines |
| Google Cloud Workstations | — | `gcloud`, signed in | one cluster and configuration's workstations |
| AWS EC2 Instances | an IAM user's access key (the policy is below) | — | instances with a tag in one region |

The token goes in only as the provider's secret, kept on the machine that carries it. It is
sent in a request's header, never in an address, a log or an error. A codespace and a Coder
machine have no ssh port of their own, so ssh to them still goes through `gh` or `coder`, as
the ssh ProxyCommand. A codespace's agentsd is started over ssh from the machine that
carries its provider, with the token there, so that machine needs `gh`. **Add as Machine…**
on a codespace installs Agent Loops in it from that machine too (#3497): the control plane makes a
host code and sends it there, and the codespace runs the control plane's `install.sh` with
it, as its join command would. The steps show as the install goes. The control plane has no
token, so it keeps no tunnel to a codespace: the codespace joins over the control plane's own
address, so that address must be one the internet reaches (agentloops.dev, or a control plane
in the cloud). A control plane on a Mac with no such address can't take a codespace this way.
The Providers page says "needs coder
for ssh" when the machine that runs the control plane does not have `coder` (#3367). A
droplet is reached with plain ssh to its public address, and so is an EC2 instance.

AWS EC2 Instances is through its API only, with an IAM user's access key ID and secret
access key. The smallest policy lists the instances and starts and stops only the ones with
the tag (here `agents`):

```json
{
  "Version": "2012-10-17",
  "Statement": [
    { "Effect": "Allow", "Action": "ec2:DescribeInstances", "Resource": "*" },
    { "Effect": "Allow", "Action": ["ec2:StartInstances", "ec2:StopInstances"],
      "Resource": "arn:aws:ec2:*:*:instance/*",
      "Condition": { "Null": { "aws:ResourceTag/agents": "false" } } }
  ]
}
```

`ec2:DescribeInstances` can't be limited to some instances: AWS allows it on all or none.
The preset makes and deletes no instances.

For anything else, choose **Your commands** and give:

- **Start** and **Stop**: the commands you would type, such as `cws start devstack-1`.
- **Status** (optional): a command that prints `running` or `stopped`. Without it, Agent Loops
  goes by what it last did.
- **ssh**: how the control plane's machine reaches it, such as `devstack-1.cws` or
  `alex@devbox.example -p 2222`. Agent Loops uses it to add the machine as a server, and to start
  agentsd if it doesn't start on boot.

The commands run with `/bin/sh` on the machine that carries the provider, as you, never as
root. A secret you add goes to them as an environment variable of the same name: write
`$TOKEN` in a command to use it.

To make and delete machines from Agent Loops as well, configure the command provider with a
`list` command and add:

- **`create`**: the command that makes one. `${name}`, `${firstBoot}` and each option's
  key are filled in, each as one shell word. With `${firstBoot}` in it, the new machine
  installs Agent Loops on its first boot. Without it, you add it over ssh once it is made.
- **`delete`**: the command that deletes one, with `${id}` and its list's columns.
- **`options`**: the New Machine form, as a JSON Schema object. A property's `default`
  fills in a choice the form leaves empty.

The Docker preset makes machines when you give it an **Image** and no containers. New
Machine… runs `docker run -d --name <name> --label agents.machine <image>`, then the
install command in it with `docker exec`, so the container joins by itself. The image must
keep running and have `curl`. Delete Machine… runs `docker rm -f -v`, which also removes
the container's anonymous volumes.

DigitalOcean through its API makes droplets too. New Machine… makes a droplet with the
tag, in the region, size and image you choose (an image with systemd, such as
`ubuntu-24-04-x64`). Its first boot makes a user, `agents`, with root's ssh keys, and
installs Agent Loops as that user, never as root. The host starts with the droplet. Delete
Machine… deletes the droplet. Any other preset needs a provider configured by hand to make
machines.

The command provider is the `workspace-commands` binary. The machine that carries the
provider looks for it beside its `agentsd`, then where `AGENTS_WORKSPACE_COMMANDS` says, then
on its `PATH`. Its configuration is described in full in
[Machine providers](../reference/machine-providers.md#the-command-provider).

## On the iPhone, the iPad and the web page

The Remote and the web page add, edit and remove the API presets: DigitalOcean Droplets,
GitHub Codespaces, Coder machines, Google Compute Engine VMs and AWS EC2 instances.

- **iPhone and iPad:** open **Settings**, tap **Machine providers**, then tap
  **Add Provider…**.
- **Web page:** open **Settings**, go to **Machine providers**, then click **Add
  Provider…**.

Each one asks for the same fields and the same token as the Mac. The token goes to the
machine that carries the provider, and the page never shows it again. **List Again**, **Edit…** and
**Remove…** are on each provider, and **Stop** and **Unlink** on each linked host.

Add Docker, Cloud Workstations and your own commands in the Mac app. They need a CLI or
commands, which the Remote and the page do not edit.

## Related

- [Start machines from Agent Loops](start-machines-from-agents.md): link a server and press
  Start.
- [Add a connector by hand](add-a-connector-by-hand.md): connectors (MCP servers) for agents, which are
  set up per folder, not on the control plane.
