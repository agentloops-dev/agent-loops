---
diataxis: how-to
devices: [mac, iphone, ipad, web]
description: Add AWS EC2, Google Compute Engine, Google Cloud Workstations, DigitalOcean, GitHub Codespaces or Coder as a machine provider, with the credentials each one needs and where they go.
---

# Set up a cloud machine provider

A machine provider starts and stops the machines that your hosts run on. One of your own
machines carries it, such as your Mac, and the control plane sends it each list, start and
stop, so it can start a host that is down (#3398). This page tells you how
to add one for a cloud, and which credentials each cloud needs.

Most clouds have an **API preset**: you give a token or a key, and there is nothing to
install. A **CLI preset** uses the cloud's own CLI, which must be installed and signed in
on the machine that carries the provider.

| Cloud | Through its API | Through its CLI | Its machines |
| --- | --- | --- | --- |
| AWS EC2 Instances | an IAM user's access key | no | instances with a tag, in one region |
| Google Compute Engine VMs | a service account's JSON key | no | VMs with a label, in one zone |
| Google Cloud Workstations | no | `gcloud` | one cluster and configuration's workstations |
| DigitalOcean Droplets | an API token | `doctl` | droplets with a tag |
| GitHub Codespaces | a GitHub token | `gh` | your codespaces, or one repository's |
| Coder Machines | a session token | `coder` | your Coder machines |

## Where to add one

- **On the Mac:** **Settings ▸ Machine Providers**, in the Control plane group, then
  **Add Provider…**. Choose the cloud from the list (under **Cloud** or **Development
  environments**). For DigitalOcean, Codespaces and Coder, **Connect with** offers **Its API**
  (first) and **Its CLI**. The Mac also adds Docker and your own commands.
- **On the web page:** the **Machine providers** section of Settings, then
  **Add Provider…**.

The iPhone and iPad do not add providers (#2798).

The web page shows the same list, but opens only the API presets: DigitalOcean, Codespaces,
Coder, AWS EC2 and Compute Engine. The other rows say **In the Mac app**. A CLI preset needs a shell where the control plane runs, to install and
sign in the CLI, and a person in a browser has none there.

The control plane runs providers only when it has a folder for them (#3113): `--home`, or
`AGENTS_CONTROL_PROVIDER_HOME`. The control plane on your Mac has one.
`deploy/compose.public.yaml` sets `/data/providers`, in the data volume. A control plane
with several copies runs no providers. When providers are off, the pane and the section
stay, and say why.

Each form fills in its own name, such as "AWS EC2 Instances". Change it in **Name** when
you add more than one of a kind.

**New Machine…**, which makes a new machine from a provider's card, is on the Mac. On all
three clients, **Start a Machine from a Repository…** makes a machine for one repository
from a provider that can make machines.

## Where the credentials go

You type a token or a key once, and it is never shown again. It goes to the machine that
carries the provider, which keeps it in `~/.agents-secrets/provider-secrets.json`, readable
only by you (#3398). The control plane passes it on once and keeps none. The device that
you typed it on does not keep it either. It is never logged. An API preset sends it only in
a request's header, never in an address.

To change it later, choose **Edit…** on the provider's card. An empty field keeps the one
it has.

## AWS EC2 Instances

You need an IAM user's access key. Give the user the smallest policy: it lists the
instances, and starts and stops only the ones with the tag (here `agents`):

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

AWS allows `ec2:DescribeInstances` on all instances or none, so it can't be limited.

1. Make the IAM user and its access key in the AWS console.
2. In the form, give **Access key ID** and **Secret access key**.
3. Give the **Region**, such as `us-east-1`.
4. Give the tag key in **Instances tagged** (the Mac) or **Tag** (the Remote and the web
   page). The default is `agents`. Put that tag on each instance that you want.
5. Give the ssh user in **ssh as** or **ssh user**. The default is `ec2-user`.

The control plane signs each request with AWS's Signature Version 4. The secret key is
never sent. Agent Loops reaches each instance with plain ssh to its public address. The preset
makes and deletes no instances.

## Google Compute Engine VMs

You need a service account's JSON key. Give the service account the smallest role:

- To list, start and stop: `compute.instances.list`, `compute.instances.start` and
  `compute.instances.stop`.
- To make and delete VMs with **New Machine…** as well: `compute.instances.create`,
  `compute.instances.delete`, `compute.instances.setMetadata`,
  `compute.instances.setLabels`, `compute.disks.create`, `compute.subnetworks.use` and
  `compute.subnetworks.useExternalIp`.

1. Make the service account and a JSON key for it in the Google Cloud console.
2. Give the key. On the Mac, choose **Choose Key File…** next to **Service account key**.
   On the Remote and the web page, paste the key's JSON in **Service account key (JSON)**.
3. Give the **Folder** and the **Zone**, such as `europe-west2-a`.
4. Give the label key in **VMs labelled** (the Mac) or **Label**. The default is `agents`.
5. Give the ssh user. The default is `agents`.
6. To make VMs from Agent Loops, give a public key in **ssh key for new VMs** (the Mac) or
   **ssh public key**. A new VM lets in that key for the `agents` user.

The control plane gets an access token from the key, and keeps the token until a minute
before it ends. The key itself is never sent. ssh goes to each VM's external address, so
the network must let in port 22. A static address keeps the address the same after a stop.

A VM made with **New Machine…** makes the user `agents` with your key on its first boot,
and installs Agent Loops as that user, never as root. Its boot disk is deleted with the VM.

## Google Cloud Workstations

This one is on the Mac only, through `gcloud`. It has no API preset.

1. On the Mac that runs the control plane, install `gcloud` and sign it in.
2. Add a host to your ssh config that reaches the workstation's port 22 through
   `gcloud workstations start-tcp-tunnel`.
3. In the form, give the **Cluster**, the **Configuration** and the **Region**. **Folder**
   is optional: without it, `gcloud`'s default is used.
4. In **ssh**, give the host from your ssh config.

A workstation makes a new ssh host key each time it starts. After a Start that the control
plane made, its ssh start takes the new key and pins it again. At any other time a changed
key is refused. See [Integrations](../reference/integrations.md#machine-providers) for
its state.

## DigitalOcean Droplets

DigitalOcean is guided (#6074): you connect your account with clicks, and copy no token.

1. In **Add a Provider**, choose **DigitalOcean Droplets** (the row says **Guided**).
2. In **Keeps the connection**, choose the machine that keeps the connection. The token
   stays on that machine, never on the control plane. With one machine, the sheet shows
   its name. With none, add a machine first.
3. Choose **Connect DigitalOcean…**. Your browser opens DigitalOcean's consent page (on the
   Mac, in a sign-in window; on the web page, in a new window). Sign in, choose the team,
   and choose **Authorize**. The sheet says **Waiting for DigitalOcean…** until the browser
   comes back, and **Open the Page Again** opens the page again.
4. The sheet closes, and the provider's card says **Connected**, with the account and the
   team. **New Droplet** on the card makes a droplet (below).

When it does not connect, the sheet says why and gives one button: **Connect
DigitalOcean…** after a Deny, a sign-in that took too long or fewer permissions than Agent
Loops needs; **Add a Payment Method** and **Check Again** when the account cannot make
droplets yet.

Agent Loops asks for these scopes: droplet create, read, update and delete; ssh_key create
and read; regions read; sizes read; tag create; and account read.

**Use an API token instead**, under **Connect DigitalOcean…**, is for a team that does not
let a person authorise apps. Make a token with the scopes above in the DigitalOcean control
panel, under API, give it in **API token**, and choose **Connect with Token**.

On the card, **Reconnect…** connects again, on the same machine. When DigitalOcean stops
taking the connection (you removed Agent Loops under **Authorized Applications**, or a
refresh failed), the card says **Disconnected**: "DigitalOcean disconnected Agent Loops.
Reconnect to start and stop its droplets." **Remove…** revokes the connection at
DigitalOcean. The droplets stay: delete them on DigitalOcean to stop paying for them.

A provider added before #6074 with a tag and an ssh user keeps them: **Edit…** gives its
token again.

**New Droplet** on the provider's card makes a droplet in one click (#6577). Beside it, the
card shows the region, the size and the monthly price that it uses: the ones you chose last
on this provider, or else the nearest region and the recommended size. Each step shows on
the droplet's row until it joins. Then the card says that it is ready, with **Open
Machine**. **Change…** beside **New Droplet** opens the whole form, where you choose
(#6073):

- **Region** and **Size** come from your account. The region nearest the machine that
  keeps the token is first. The sizes are DigitalOcean's Basic sizes, with DigitalOcean's
  monthly and hourly prices. The recommended size is `s-2vcpu-4gb` (2 vCPUs, 4 GB),
  enough for 2 or 3 agents at once. When the account does not answer, the form has plain
  text fields.
- **Image** is `ubuntu-24-04-x64`. Choose an image with systemd.
- **An ssh key for Agent Loops:** the machine that keeps the token makes a key pair once,
  in `~/.agents/providers/agent-loops-ssh/`, and adds its public half, named
  "Agent Loops", to your account. Each new droplet gets it, so DigitalOcean sends no root
  password. You can reach the droplet with
  `ssh -i ~/.agents/providers/agent-loops-ssh/id_ed25519 agents@<address>`. A token
  without the ssh_key scopes makes droplets with no key, as before.

The first boot makes a user `agents` with root's ssh keys and installs Agent Loops as that
user, never as root. The machine's set-up shows each step as it goes: the key, the
droplet (its region, size and price), its start, the install and the join. When
DigitalOcean refuses a step, the step shows DigitalOcean's words and says whether a
droplet was made.

A `doctl` provider that you added before #6074 still works: `doctl` must be signed in on the
machine that carries it.

## GitHub Codespaces

Through its API, you need a GitHub token: a fine-grained one with Codespaces lifecycle
admin (read and write), or a classic one with the `codespace` scope.

1. Make the token on github.com, under Settings ▸ Developer settings.
2. Give it in **Token** (the Mac) or **GitHub token**.
3. In **Repository**, give `owner/name` for one repository's codespaces, or leave it empty
   for all of yours.
4. On the Mac that runs the control plane, run
   `gh codespace ssh --config >> ~/.ssh/config`. A control plane in the `agents-control`
   image needs nothing: the image has `ssh` and `gh`, and the control plane writes each
   codespace's ssh host itself, with the provider's token (#3347).

A codespace has no ssh port of its own, so ssh still goes through `gh`. When the machine that
runs the control plane has no `gh`, the provider's card says "needs gh for ssh". It can make a codespace from a repository, or from GitHub's blank template, `github/codespaces-blank`, when you give no repository, but it can't install
Agent Loops on its first boot: add it with **Add as Host…** on the Mac.

Through its CLI, install `gh` and sign it in.

## Coder Machines

Through its API, you need your deployment's address and a session token.

1. Make the token with `coder tokens create`.
2. Give the address in **Deployment**, such as `https://coder.example.com`.
3. Give the token in **Session token**.
4. On the Mac that runs the control plane, sign `coder` in with `coder login <address>`,
   then run `coder config-ssh`. The control plane installs Agent Loops on a Coder machine
   over the `coder.<name>` host that it writes.

ssh still goes through `coder`, so the card says "needs coder for ssh" when the machine that
runs the control plane has no `coder`.

Through its CLI, install `coder`, sign it in, and run `coder config-ssh`.

## After you add it

When you choose **Add**, the control plane checks that the provider lists its machines.
If it can't, it says why, in the cloud's own words. The provider's card then shows its
machines, and you can link each one to a host. See
[Start machines from Agent Loops](start-machines-from-agents.md).

Machine providers need a control plane with one copy, such as the one on your Mac. A
control plane with several copies does not run them yet.

## See also

- [Add a machine provider](add-a-machine-provider.md): your own commands.
- [Machine providers](../reference/machine-providers.md): every preset's parameters
  and the contract.
- [Start machines from Agent Loops](start-machines-from-agents.md)
