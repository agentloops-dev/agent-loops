// Settings ▸ Machine Providers on the web (#1808), as the window's ControlProvidersPage:
// each machine provider as a card of its workspaces, Add Provider, Edit, List Again, Remove,
// and a workspace's host linked, started and stopped. A machine of the person's carries each
// provider (#3398): it runs its server and keeps its tokens, and Add and Edit… choose it (#3496).
// Only the providers that work through their API with a token (#1119) are offered here: a CLI
// preset needs its CLI signed in on that machine, and a person who starts in a browser has no
// shell there.
import type { ControlMachine, MachineRef, ProviderAdd, ProviderSummary, MachineInfo } from "../protocol/generated";
import { fromWireDate } from "../protocol/dates";
import { Capability } from "../protocol/capabilities.generated";
import { hostAnswers } from "./capabilities";
import { doWords } from "./digitalOceanConnect";

export type ProviderKind = "digitalOcean" | "codespaces" | "coder" | "aws" | "compute";

export const providerKinds: readonly ProviderKind[] = ["digitalOcean", "codespaces", "coder", "aws", "compute"];

/** One field of a kind's form. A secret goes only to the machine that carries it, never in the configuration. */
export interface ProviderField {
  id: string;
  label: string;
  prompt?: string;
  /** The help line under the field (#6069), in place of one paragraph of needs. */
  help?: string;
  secret?: true;
  optional?: true;
  value?: string;
}

interface KindInfo {
  title: string;
  preset: string;
  /** What has no field of its own: one short line under the header (#6069). */
  note: string;
  fields: ProviderField[];
}

/** The window's titles, notes and help lines (AddProviderSheet.Kind), for the API way only. */
const kinds: Record<ProviderKind, KindInfo> = {
  digitalOcean: {
    title: "DigitalOcean Droplets",
    preset: "digitalocean-api",
    note: "Nothing to install.",
    fields: [
      { id: "token", label: "API token", secret: true, help: "Custom scopes: droplet read and update, and droplet create and delete to make machines." },
      { id: "tag", label: "Droplets tagged", value: "agents" },
      { id: "user", label: "ssh as", value: "root" },
    ],
  },
  codespaces: {
    title: "GitHub Codespaces",
    preset: "codespaces-api",
    note: "ssh still goes through gh on the machine that carries it.",
    fields: [
      { id: "token", label: "GitHub token", secret: true, help: "Fine-grained, with Codespaces lifecycle admin (read and write), or classic, with the codespace scope." },
      { id: "repo", label: "Repository", prompt: "owner/name", optional: true, help: "Empty: every codespace of yours." },
    ],
  },
  coder: {
    title: "Coder Workspaces",
    preset: "coder-api",
    note: "ssh still goes through coder on the machine that carries it.",
    fields: [
      { id: "token", label: "Session token", secret: true, help: "Make one with `coder tokens create`." },
      { id: "url", label: "Deployment", prompt: "Your deployment’s address" },
    ],
  },
  aws: {
    title: "AWS EC2 Instances",
    preset: "aws-ec2-api",
    note: "Nothing to install.",
    fields: [
      { id: "accessKey", label: "Access key ID", secret: true, help: "An IAM user’s key. Its policy: ec2:DescribeInstances, and ec2:StartInstances and ec2:StopInstances on tagged instances only (How to set up)." },
      { id: "secretKey", label: "Secret access key", secret: true },
      { id: "region", label: "Region", prompt: "us-east-1" },
      { id: "tag", label: "Instances tagged", value: "agents" },
      { id: "user", label: "ssh as", value: "ec2-user" },
    ],
  },
  compute: {
    title: "Google Compute Engine VMs",
    preset: "gcp-compute-api",
    note: "Nothing to install.",
    fields: [
      { id: "serviceKey", label: "Service account key (JSON)", secret: true, help: "A role with compute.instances list, start and stop; New Machine… needs more (How to set up)." },
      { id: "project", label: "Project" },
      { id: "zone", label: "Zone", prompt: "us-west1-b" },
      { id: "label", label: "VMs labelled", value: "agents" },
      { id: "user", label: "ssh as", value: "agents", help: "ssh goes to each VM’s external address: the network must let in port 22." },
      { id: "sshKey", label: "ssh key for new VMs", prompt: "ssh-ed25519 AAAA…", optional: true },
    ],
  },
};

/**
 * Every kind Add a Provider lists (#6069), as the window's AddProviderSheet.Kind: the API kinds
 * this page adds, and the ones only the Mac app adds, which the list shows but does not open.
 */
export type ProviderChoice = ProviderKind | "docker" | "workstations" | "commands";

/** The list's groups, in order: a person looks first for the place their machines are (#6069). */
export const providerGroups: readonly { title: string; kinds: readonly ProviderChoice[] }[] = [
  { title: "Cloud", kinds: ["digitalOcean", "compute", "aws"] },
  { title: "Development environments", kinds: ["codespaces", "coder", "workstations"] },
  { title: "On a machine you have", kinds: ["docker"] },
  { title: "Custom", kinds: ["commands"] },
];

/** The docs site (#1327), as the sidebar's Help: a link opened in a new tab, never fetched. */
const helpSite = "https://docs.agentloops.dev/";
const docs = `${helpSite}how-to/`;

/** Each kind's row: its title, its one line, and its part of the docs (How to set up). */
const choices: Record<ProviderChoice, { title: string; description: string; docs: string }> = {
  digitalOcean: { title: kinds.digitalOcean.title, description: doWords.rowSummary, docs: `${docs}set-up-a-cloud-machine-provider/#digitalocean-droplets` },
  compute: { title: kinds.compute.title, description: "VMs with a label, in one zone.", docs: `${docs}set-up-a-cloud-machine-provider/#google-compute-engine-vms` },
  aws: { title: kinds.aws.title, description: "Instances with a tag, in one region.", docs: `${docs}set-up-a-cloud-machine-provider/#aws-ec2-instances` },
  codespaces: { title: kinds.codespaces.title, description: "Your codespaces, or the codespaces of one repository.", docs: `${docs}set-up-a-cloud-machine-provider/#github-codespaces` },
  coder: { title: kinds.coder.title, description: "Workspaces on your Coder deployment.", docs: `${docs}set-up-a-cloud-machine-provider/#coder-machines` },
  workstations: { title: "Google Cloud Workstations", description: "Workstations in one cluster.", docs: `${docs}set-up-a-cloud-machine-provider/#google-cloud-workstations` },
  docker: { title: "Docker Containers", description: "Containers on a machine that has docker.", docs: `${docs}start-machines-from-agents/#add-a-provider` },
  commands: { title: "Your commands", description: "Your own commands to list, start and stop machines.", docs: `${docs}start-machines-from-agents/#add-a-provider` },
};

export function choiceTitle(choice: ProviderChoice): string { return choices[choice].title; }
export function choiceDescription(choice: ProviderChoice): string { return choices[choice].description; }
export function choiceDocs(choice: ProviderChoice): string { return choices[choice].docs; }

/** The API kind a row opens on this page, or undefined for one only the Mac app adds. */
export function choiceKind(choice: ProviderChoice): ProviderKind | undefined {
  return (providerKinds as readonly string[]).includes(choice) ? choice as ProviderKind : undefined;
}

export const providerWords = {
  heading: "Machine providers",
  /** Under the list: what a provider is, and the kinds only the Mac app adds (#3425). */
  footer: (hasMac: boolean) => "A provider starts and stops your machines: DigitalOcean Droplets, GitHub Codespaces, Coder workspaces, "
    + "Google Compute Engine VMs or AWS EC2 instances, through their API. One of your machines carries it: it runs there and keeps its tokens, and the control plane keeps none. "
    + (hasMac ? "Docker, Cloud Workstations and your own commands are added in the Mac app."
      : "The web page cannot add Docker, Cloud Workstations or your own commands yet."),
  off: "Machine providers are off on this control plane.",
  add: "Add Provider…",
  /** Step 1's line under the title (#6069). */
  chooseIntro: "A provider starts and stops the machines that agents run on.",
  /** On a row this page cannot add, and why. */
  inTheMacApp: (hasMac: boolean) => hasMac ? "In the Mac app" : "Not on the web page yet",
  howToSetUp: "How to set up",
  secretsOptional: "Secrets (optional)",
  checking: "Checking…",
  /** Under the form: where its tokens and keys go (#3496). */
  secretsFoot: (machine: string | undefined) => `Tokens and keys go to ${machine ?? "the machine that carries it"}, which keeps them. The control plane keeps none.`,
  carriedBy: "Carried by",
  /** In Edit…, when another machine is chosen. */
  movedNote: "Its secrets stay on the machine that carried it: give them again.",
  giveAgain: "Give it again.",
  /** In place of the menu when none of the person's machines can carry it. */
  noneOnline: "None of your machines is online to carry it: its server runs there, with its secrets. Start one, then add it.",
  notCarried: "No machine carries it: choose one with Edit…, and give its secrets again.",
  editSecret: "Leave empty to keep the one it has.",
  removeTitle: (name: string) => `Remove ${name}?`,
  removeNote: "Its machines stay as they are, unlinked: nothing here can start or stop them any more.",
  noHosts: "No machine to link: add one with Add a Machine… in the sidebar’s Machines menu, or start one from a repository.",
  notAHostYet: "Not joined yet, and it gives no ssh destination: add it with the command from Add a Machine…, then Link to Machine….",
  addAsMachine: "Add as Machine…",
  /** Delete's question on a failed make's row (#6078), as the window's. */
  deleteFailed: (name: string, provider: string) => `Delete ${name}? Only its row goes: ${provider} never made it, so nothing is deleted there.`,
  addAsMachineHelp: "Installs Agent Loops on it over ssh, and links the machine to it.",
  startFirst: "Start it first: it has to be running to be installed.",
  installNote: (provider: string) => `The control plane installs Agent Loops on it over its own ssh, and links the machine to it, so ${provider} starts it again.`,
  installing: "Installing Agent Loops… This takes a minute or two.",
  linking: "Linking… This reads the machine’s ssh host key.",
  linkTrustNote: (fingerprint: string) => `This machine’s host key is new to the control plane: ${fingerprint}. Link only if this is the key the machine shows (ssh-keygen -lf on its host key): its provider starts it over ssh with this key.`,
  trustNote: (fingerprint: string) => `This machine’s host key is new to the control plane: ${fingerprint}. Install only if this is the key the machine shows (ssh-keygen -lf on its host key).`,
  installed: (name: string) => `Installed on ${name}. It is linked to this machine when it joins.`,
};

export function kindTitle(kind: ProviderKind): string { return kinds[kind].title; }
export function kindNote(kind: ProviderKind): string { return kinds[kind].note; }

/** A kind's fields, with their starting values. */
export function kindFields(kind: ProviderKind): ProviderField[] { return kinds[kind].fields.map((f) => ({ ...f })); }

/** The kind a preset stands for, when it is one this page can add or edit. */
export function kindOfPreset(preset: string | undefined): ProviderKind | undefined {
  return providerKinds.find((k) => kinds[k].preset === preset);
}

/** The name a new provider gets when the person gives none: its kind's title. */
export function defaultName(kind: ProviderKind): string { return kinds[kind].title; }

/**
 * What Add or Save sends: `providers/add`, or with `editing` `providers/update`. On an edit the
 * configuration is left out, so the control plane keeps it, and an empty secret keeps the one it
 * has. Answers a sentence that says what is missing, or the request.
 */
export function providerRequest(kind: ProviderKind, name: string, values: Record<string, string>, editing?: ProviderSummary,
  carrier?: string): ProviderAdd | string {
  const v = (id: string) => (values[id] ?? "").trim();
  const title = name.trim() || defaultName(kind);
  const secretNames: Record<string, string> = kind === "aws"
    ? { accessKey: "AWS_ACCESS_KEY_ID", secretKey: "AWS_SECRET_ACCESS_KEY" }
    : kind === "compute" ? { serviceKey: "GOOGLE_SERVICE_ACCOUNT_KEY" }
    : { token: { digitalOcean: "DIGITALOCEAN_API_TOKEN", codespaces: "GITHUB_TOKEN", coder: "CODER_SESSION_TOKEN" }[kind] };
  const secrets: Record<string, string> = {};
  for (const [field, secret] of Object.entries(secretNames)) {
    // The secret key is not trimmed: a key is what it is.
    const value = field === "secretKey" || field === "serviceKey" ? (values[field] ?? "") : v(field);
    if (value) secrets[secret] = value;
    else if (!editing || movesTo(editing, carrier)) return `Give the ${kinds[kind].fields.find((f) => f.id === field)?.label ?? field}.`;
  }
  if (editing) return { id: editing.id, name: title, kind: "commands", ...(carrier && movesTo(editing, carrier) ? { machine: carrier as MachineRef } : {}), secrets };
  const commands: Record<string, string | string[]> = { preset: kinds[kind].preset };
  switch (kind) {
    case "digitalOcean":
      commands["tag"] = v("tag") || "agents";
      commands["user"] = v("user") || "root";
      break;
    case "codespaces":
      if (v("repo")) commands["repo"] = v("repo");
      break;
    case "coder":
      if (!v("url")) return "Give the deployment’s address.";
      commands["url"] = v("url");
      break;
    case "aws":
      if (!v("region")) return "Give the region.";
      commands["region"] = v("region");
      commands["tag"] = v("tag") || "agents";
      commands["user"] = v("user") || "ec2-user";
      break;
    case "compute":
      if (!v("project") || !v("zone")) return "Give the project and the zone.";
      commands["project"] = v("project");
      commands["zone"] = v("zone");
      commands["label"] = v("label") || "agents";
      commands["user"] = v("user") || "agents";
      if (v("sshKey")) commands["sshKeys"] = [v("sshKey")];
      break;
  }
  return { name: title, kind: "commands", commands, ...(carrier ? { machine: carrier as MachineRef } : {}), secrets };
}

/**
 * The machines that may carry a provider (#3496), as the window's ProviderCarrier.choices: the
 * person's own, online, able to, and running agents; `own` (the person's Mac) first, then by name.
 */
export function carrierChoices(hosts: readonly ControlMachine[], person: string | undefined, own?: string): ControlMachine[] {
  return hosts
    .filter((h) => hostAnswers(h.state) && (h.capabilities?.includes(Capability.providersCarry1) ?? true)
      && (!person || !h.owner || h.owner === person))
    .sort((a, b) => Number(b.id === own) - Number(a.id === own) || a.name.localeCompare(b.name, undefined, { numeric: true }));
}

/** What Add and Edit… start at: the one it has, when it can still carry it, else the first choice. */
export function carrierStart(provider: ProviderSummary | undefined, choices: readonly ControlMachine[]): string | undefined {
  if (provider?.machine && choices.some((h) => h.id === provider.machine)) return provider.machine;
  return choices[0]?.id;
}

/** Whether Edit… moves the provider to `carrier`: then its secrets must be given again. */
export function movesTo(provider: ProviderSummary, carrier: string | undefined): boolean {
  return !!carrier && carrier !== provider.machine;
}

/**
 * The card's line for the machine that carries it (#3496): offline, carried by, or, for one made
 * before #3398 that an admin can edit, that none does.
 */
export function carrierLine(provider: ProviderSummary, canEdit: boolean): string | undefined {
  if (provider.machineOffline) return `${provider.machineName ?? "Its machine"} is offline: its machines can’t be started or stopped until it is back.`;
  if (provider.machine) return `Carried by ${provider.machineName ?? "a machine"}`;
  return canEdit ? providerWords.notCarried : undefined;
}

/** The chip on a card, as the window's: "command provider · digitalocean-api · API". */
export function kindChip(provider: ProviderSummary): string {
  if (provider.preset) return `command provider · ${provider.preset} · ${provider.preset.endsWith("-api") ? "API" : "CLI"}`;
  return "command provider";
}

/** The grey line under a provider's name, as the window's providerLine. */
export function providerLine(provider: ProviderSummary, time: (at: Date) => string): string {
  const parts: string[] = [];
  if (provider.problem && (!provider.current || provider.listedAt == null)) parts.push(`Couldn’t list: ${provider.problem}`);
  if (provider.listedAt != null) parts.push(`${provider.current ? "Listed" : "Showing what it listed at"} ${time(fromWireDate(provider.listedAt))}`);
  if (provider.workspaces.length === 0 && provider.listedAt != null) parts.push("No machines");
  // Only when the machine that runs it has no such CLI (#3367).
  if (provider.sshToolMissing) parts.push(`needs ${provider.sshToolMissing} for ssh`);
  return parts.join(" · ");
}

function capitalized(word: string): string { return word.slice(0, 1).toUpperCase() + word.slice(1); }

/** The grey line under a workspace, as the window's workspaceLine. */
export function workspaceLine(workspace: MachineInfo, hosts: readonly ControlMachine[]): string {
  const parts = [capitalized(workspace.state)];
  // A make that failed says why (#4706), in place of "not joined".
  const failed = workspace.state === "failed" && workspace.reason ? workspace.reason : null;
  parts.push(workspace.host ? `linked to ${hosts.find((h) => h.id === workspace.host)?.name ?? "a machine"}` : failed ?? "not joined");
  if (workspace.cost) parts.push(workspace.cost);
  return parts.join(" · ");
}

/**
 * What a workspace with no host says when no host is free to link to it and it gives no ssh
 * destination (#1931): Add as Machine… cannot reach it, so the person is told the other way in.
 */
export function noHostLine(workspace: MachineInfo, hosts: readonly ControlMachine[]): string | undefined {
  return !workspace.host && !workspace.ssh && workspace.state !== "failed" && linkableHosts(workspace, hosts).length === 0 ? providerWords.notAHostYet : undefined;
}

/**
 * Add as Machine… on a workspace no host has (#3365), as the window's: offered when the provider
 * gives an ssh destination, and pressable only while it runs. Answers undefined when it is not
 * offered, else whether it can be pressed and its help line.
 */
export function addAsMachine(workspace: MachineInfo): { ready: boolean; help: string } | undefined {
  if (workspace.host || !workspace.ssh || workspace.state === "failed") return undefined;
  const ready = workspace.state === "running";
  return { ready, help: ready ? providerWords.addAsMachineHelp : providerWords.startFirst };
}

/** `alex@127.0.0.1 -p 2222`, as a provider may give it, as ssh takes it in one word (the window's sshDestination). */
export function sshDestination(destination: string): string {
  const words = destination.trim().split(/\s+/);
  if (words.length !== 3 || words[1] !== "-p" || !/^\d+$/.test(words[2]!)) return destination.trim();
  return `ssh://${words[0]}:${Number(words[2])}`;
}

/** Whether a host named `hostName` may be this workspace (MachineInfo.mayBe, #976). */
export function mayBe(workspace: MachineInfo, hostName: string): boolean {
  const host = hostName.toLowerCase();
  if (workspace.ssh && workspace.ssh.toLowerCase() === host) return true;
  const name = workspace.name.toLowerCase();
  if (!name) return false;
  return host === name || `.${host}.`.includes(`.${name}.`);
}

/**
 * The hosts Link to Machine can offer for a workspace no host has: those that run agents and
 * are linked to none, the ones that may be it by name first.
 */
export function linkableHosts(workspace: MachineInfo, hosts: readonly ControlMachine[]): ControlMachine[] {
  if (workspace.host) return [];
  const free = hosts.filter((h) => !h.workspace);
  return [...free.filter((h) => mayBe(workspace, h.name)), ...free.filter((h) => !mayBe(workspace, h.name))];
}

/** The window's Stop when idle choices, in minutes; 0 is Never. */
export const idleChoices: readonly { minutes: number; label: string }[] = [
  { minutes: 0, label: "Never" },
  { minutes: 15, label: "After 15 minutes" },
  { minutes: 30, label: "After 30 minutes" },
  { minutes: 60, label: "After 1 hour" },
  { minutes: 120, label: "After 2 hours" },
];

/** A Stop when idle choice as a linked machine's row says it, standing alone (#3729). */
export function idleRowWords(minutes: number): string {
  const choice = idleChoices.find((c) => c.minutes === minutes);
  if (!minutes) return "Never stop when idle";
  return `Stop ${choice ? choice.label.toLowerCase() : `after ${minutes} minutes`} idle`;
}

/**
 * "Kept awake while it works; GitHub stops it 240 minutes after that." (#4803;
 * OfflineWords.keptAwakeLine): a codespace is kept awake while its machine works (#4637), and
 * GitHub's own idle timeout stops it once the machine is idle. Null with no idle timeout.
 */
export function keptAwakeLine(workspace: { idleTimeoutMinutes?: number }): string | null {
  const minutes = workspace.idleTimeoutMinutes;
  if (!minutes || minutes <= 0) return null;
  return `Kept awake while it works; GitHub stops it ${minutes === 1 ? "1 minute" : `${minutes} minutes`} after that.`;
}

/** WorkspaceLink.defaultIdleMinutes. */
export const defaultIdleMinutes = 120;
