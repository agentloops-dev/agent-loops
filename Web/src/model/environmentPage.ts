// Status ▸ Environment (#5260, specs/5254-secrets-accounts-environment/wireframes.md sections 4 and 5): the plain variables
// that sessions get when their agent starts. Yours (~/.agents/env, on every machine) and each repo's (.agents/env), grouped,
// with Needs you on top. The rules and words are EnvironmentPage's and EnvFileText's in
// Packages/AgentsKit/Sources/AgentsKitCore/Model/EnvironmentPage.swift; the edit rule is Go/internal/envfile's Edit.
import type { EnvFile, EnvLayer, EnvResolvedVariable, EnvVariable, EnvVariableState } from "../protocol/generated";

// The file's text

/** A name a shell takes. */
export function isName(name: string): boolean {
  return /^[A-Za-z_][A-Za-z0-9_]*$/.test(name);
}

/** A name that says it holds a secret, as the daemon's SecretLookingName. */
export function nameLooksSecret(name: string): boolean {
  return /token|secret|key|password|passwd|auth|bearer|cookie|credential/i.test(name);
}

/** Why a variable looks like a secret, or null: a name that says so, with a value. The daemon also refuses a value with
 * a token's shape, and says why. */
export function secretWords(name: string, value: string): string | null {
  if (value === "" || !nameLooksSecret(name)) return null;
  return `${name} looks like the name of a secret.`;
}

/** NAME=value from one trimmed line, with `export ` and one pair of quotes taken off. */
export function splitLine(raw: string): { name: string; value: string } | null {
  let line = raw.trim();
  if (line.startsWith("export ")) line = line.slice(7).trim();
  const eq = line.indexOf("=");
  if (eq < 0) return null;
  const name = line.slice(0, eq).trim();
  if (!isName(name)) return null;
  let value = line.slice(eq + 1).trim();
  const first = value[0];
  if (value.length >= 2 && (first === "\"" || first === "'") && value[value.length - 1] === first) value = value.slice(1, -1);
  return { name, value };
}

/** The line a file keeps: quoted when the value has a space, a # or a quote, so it reads back the same. */
function lineFor(name: string, value: string): string {
  if (value !== value.trim() || /[#"' ]/.test(value)) return value.includes("'") ? `${name}="${value}"` : `${name}='${value}'`;
  return `${name}=${value}`;
}

function oneLine(text: string): string {
  return text.replace(/[\r\n]/g, " ").trim();
}

/** The text of a file with these variables, each with its description above it, a blank line between: for a file the
 * settings have no text of (one from before the part came, or one changed by hand). */
export function textOf(variables: readonly EnvVariable[]): string {
  const body = [...variables].sort((a, b) => a.line - b.line).map((v) => {
    const about = oneLine(v.description ?? "");
    return (about ? `# ${about}\n` : "") + lineFor(v.name, v.value);
  }).join("\n\n");
  return variables.length === 0 ? body : body + "\n";
}

/** Sets (value not null) or removes (null) one variable, keeping every other line, as envfile.Edit. A new one goes at the
 * end, after a blank line, with its description above it. description null keeps the one there; "" removes it. */
export function edit(text: string, name: string, value: string | null, description: string | null): string {
  let body = text.replace(/\r\n/g, "\n");
  if (body.endsWith("\n")) body = body.slice(0, -1);
  const lines = body === "" ? [] : body.split("\n");
  const at = lines.flatMap((l, i) => (splitLine(l)?.name === name ? [i] : []));
  const commentAbove = (i: number) => i > 0 && (lines[i - 1] ?? "").trim().startsWith("#");
  const blank = (i: number) => i < 0 || i >= lines.length || (lines[i] ?? "").trim() === "";
  const replace = at.length > 0 ? at[at.length - 1] : undefined;
  const drop = new Set<number>();
  for (const i of at) {
    if (i === replace && value !== null) continue;
    drop.add(i);
    let first = i;
    if (commentAbove(i)) {
      drop.add(i - 1);
      first = i - 1;
    }
    // A block between two blank lines leaves one, not two.
    if (blank(first - 1) && i + 1 < lines.length && blank(i + 1)) drop.add(i + 1);
  }
  const out: string[] = [];
  lines.forEach((raw, i) => {
    if (drop.has(i)) return;
    if (i === replace && value !== null) {
      if (description !== null) {
        if (commentAbove(i)) out.pop();
        const about = oneLine(description);
        if (about) out.push(`# ${about}`);
      }
      out.push(lineFor(name, value));
      return;
    }
    out.push(raw);
  });
  if (replace === undefined && value !== null) {
    const last = out[out.length - 1];
    if (last !== undefined && last.trim() !== "") out.push("");
    if (description !== null) {
      const about = oneLine(description);
      if (about) out.push(`# ${about}`);
    }
    out.push(lineFor(name, value));
  }
  while (out.length > 0 && (out[out.length - 1] ?? "").trim() === "") out.pop();
  return out.length === 0 ? "" : out.join("\n") + "\n";
}

// The page

export const w = {
  title: "Environment",
  /** The header line: where the values live, so nobody takes the page for a safe place. */
  whereLine: "Plain text in ~/.agents/env and each repo’s .agents/env. Secrets go in Secrets.",
  searchPrompt: "Search names, values, descriptions…",
  clearSearch: "Clear Search",
  add: "Add Variable",
  asSession: "As a Session Sees It…",
  selectOne: "Select a variable to see it.",
  none: "No variables yet. Add one, and sessions get it the next time their agent starts.",
  nextStart: "Sessions get it the next time their agent starts.",
  edit: "Edit",
  remove: "Remove…",
  copy: "Copy",
  saveAsSecret: "Save as a Secret Instead…",
  looksSecret: "This looks like a secret. Values here are not sealed.",
  repoPublic: "Committed with the repo: anyone who can read the repo can read it.",
  notCommitted: "Not committed",
  changedByHand: "~/.agents/env was changed by hand on this machine, so this machine uses the edited file.",
  keepForAll: "Keep for All Machines",
  discard: "Discard",
  yoursWhere: "~/.agents/env, every machine",
  machineLabel: "Machine",
  /** Settings ▸ You's line, beside the Secrets one. */
  open: "Open Environment",
  /** The machine's Agent Loops has no env/list (#5259). */
  tooOld: "This machine’s Agent Loops is too old for environment variables. Update it.",
  noMachine: "No machine of yours can show its variables yet. Update Agent Loops on it.",
} as const;

export function cannotReach(machine: string): string {
  return `${machine === "" ? "The machine" : machine} can’t be reached.`;
}

export function noMatch(search: string): string {
  return `Nothing matches “${search}”.`;
}

export type Chip = "all" | "yours" | "repos" | "needsYou";
export const chips: readonly Chip[] = ["all", "yours", "repos", "needsYou"];
export const chipTitles: Record<Chip, string> = { all: "All", yours: "Yours", repos: "Repos", needsYou: "Needs you" };

/** One variable of one file. */
export interface Item {
  /** `yours/<name>`, or `repo/<folder>/<name>`. */
  id: string;
  name: string;
  value: string;
  description?: string | undefined;
  layer: EnvLayer;
  /** The file's path, as the machine has it. */
  path: string;
  /** For a repo: its folder, and that folder's name. */
  folder?: string | undefined;
  repo?: string | undefined;
  line: number;
  state: EnvVariableState;
  reason?: string | undefined;
  /** The file has changes git has not committed. */
  uncommitted: boolean;
  /** Why it is in Needs you, or null. */
  needs: string | null;
}

/** A group of the list: Needs you, Yours, then one for each repo. */
export interface Group {
  id: string;
  title: string;
  /** The file, as the header says it. */
  file: string;
  uncommitted: boolean;
  /** Why the file is not read. */
  problem?: string | undefined;
  items: Item[];
}

export function id(layer: EnvLayer, folder: string | undefined, name: string): string {
  return layer === "yours" ? `yours/${name}` : `repo/${folder ?? ""}/${name}`;
}

/** The folder's name, as a repo group says it. */
export function repoName(folder: string): string {
  const trimmed = folder.endsWith("/") ? folder.slice(0, -1) : folder;
  return trimmed.split("/").filter(Boolean).pop() ?? trimmed;
}

/** Why a variable is in Needs you, or null when it is given and looks like nothing. */
export function needs(variable: Pick<EnvVariable, "state" | "reason">, layer: EnvLayer): string | null {
  switch (variable.state) {
    case "given": return null;
    case "secretLooking":
      return layer === "repo" ? "This is in the repo, so it is public. Revoke it, then add it to Secrets." : "This looks like a secret. Move it to Secrets.";
    case "refused": return variable.reason ?? (layer === "repo" ? "A repo cannot set this. Not given." : "Agents set this. Not given.");
    case "policyBlocked": return variable.reason ?? "Blocked by your organisation’s policy.";
  }
}

/** Every variable of the files, Yours first, in the order of the files. */
export function items(files: readonly EnvFile[]): Item[] {
  return files.flatMap((file) => file.variables.map((v): Item => ({
    id: id(file.layer, file.folder, v.name), name: v.name, value: v.value, description: v.description, layer: file.layer,
    path: file.path, folder: file.folder, repo: file.folder === undefined ? undefined : repoName(file.folder), line: v.line,
    state: v.state, reason: v.reason, uncommitted: file.uncommitted === true, needs: needs(v, file.layer),
  })));
}

/** The search matches the name, the value and the description, in any case. */
export function matches(item: Item, search: string): boolean {
  const words = search.trim().toLowerCase();
  if (!words) return true;
  return [item.name, item.value, item.description ?? ""].some((f) => f.toLowerCase().includes(words));
}

export function inChip(item: Item, chip: Chip): boolean {
  switch (chip) {
    case "all": return true;
    case "yours": return item.layer === "yours";
    case "repos": return item.layer === "repo";
    case "needsYou": return item.needs !== null;
  }
}

/** Each chip's count, after the search. */
export function counts(list: readonly Item[], search: string): Record<Chip, number> {
  const found = list.filter((i) => matches(i, search));
  const out = {} as Record<Chip, number>;
  for (const chip of chips) out[chip] = found.filter((i) => inChip(i, chip)).length;
  return out;
}

/** The list: Needs you on top, then Yours, then a group per repo, each by name. A file with a problem keeps its group, to
 * say why, when no chip or search hides it. */
export function groups(files: readonly EnvFile[], search: string, chip: Chip): Group[] {
  const all = items(files).filter((i) => matches(i, search) && inChip(i, chip));
  const out: Group[] = [];
  const needing = all.filter((i) => i.needs !== null);
  if (needing.length > 0) out.push({ id: "needs", title: "Needs you", file: "", uncommitted: false, items: needing });
  if (chip === "needsYou") return out;
  const rest = all.filter((i) => i.needs === null);
  const quiet = search.trim() === "";
  for (const file of files) {
    if (chip === "yours" && file.layer !== "yours") continue;
    if (chip === "repos" && file.layer !== "repo") continue;
    const mine = rest.filter((i) => i.layer === file.layer && i.folder === file.folder)
      .sort((a, b) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0));
    if (mine.length === 0 && !(quiet && file.problem !== undefined)) continue;
    if (file.layer === "yours") {
      out.push({ id: "yours", title: "Yours", file: w.yoursWhere, uncommitted: false, problem: file.problem, items: mine });
    } else {
      const folder = file.folder ?? file.path;
      out.push({ id: `repo/${folder}`, title: `Repo · ${repoName(folder)}`, file: ".agents/env", uncommitted: file.uncommitted === true,
        problem: file.problem, items: mine });
    }
  }
  return out;
}

/** The layer, as the detail says it: "Yours (~/.agents/env, every machine)" or "Repo · Agents (.agents/env)". */
export function layerLine(item: Item): string {
  return item.layer === "yours" ? "Yours (~/.agents/env, every machine)" : `Repo · ${item.repo ?? ""} (.agents/env)`;
}

/** The layer in short: "Yours" or "Repo · <name>", the Needs you row's layer line. */
export function layerShort(item: Item): string {
  return item.layer === "yours" ? "Yours" : `Repo · ${item.repo ?? ""}`;
}

/** The row's second line: the layer, or why it needs you. */
export function subtitle(item: Item): string {
  return item.needs ?? layerShort(item);
}

/** A session gets a variable in this state. */
export function gives(state: EnvVariableState): boolean {
  return state === "given" || state === "secretLooking";
}

/** Also set (wireframes §4a): the same name in the other layer, and which one sessions get. Yours wins over a repo's. */
export function alsoSet(item: Item, all: readonly Item[]): string[] {
  const others = all.filter((o) => o.name === item.name && o.id !== item.id);
  if (others.length === 0) return [];
  const lines = others.map((o) => (o.layer === "yours" ? `Yours: ${o.value}` : `Repo · ${o.repo ?? ""}: ${o.value}`));
  const yours = others.find((o) => o.layer === "yours");
  if (item.layer === "repo" && yours && gives(yours.state)) lines.push(`Yours wins: sessions get ${yours.value}`);
  else if (item.layer === "yours") lines.push("Yours wins over a repo’s");
  return lines;
}

/** The where-to sentence for a write: yours goes to every machine; a repo's is left not committed. */
export function writeNote(layer: EnvLayer): string {
  return layer === "yours" ? "Written to ~/.agents/env on every machine of yours."
    : "Written to .agents/env in the folder, not committed: commit it as any other change.";
}

/** The add or edit sheet's refusal before it is sent, or null: a name a shell does not take, or one that says it is a secret. */
export function refusal(name: string, value: string): string | null {
  const trimmed = name.trim();
  if (!trimmed) return null;
  if (!isName(trimmed)) return "A name is letters, digits and _, and does not start with a digit.";
  if (new TextEncoder().encode(value).length > 4096) return "The value is over 4 KB.";
  if (secretWords(trimmed, value) !== null) return w.looksSecret;
  return null;
}

/** True when a refusal from the daemon or the control plane says the value looks like a secret: the sheet then offers
 * Save as a Secret Instead. */
export function refusedAsSecret(problem: string | null | undefined): boolean {
  if (!problem) return false;
  return problem === w.looksSecret || problem.includes("looks like a token") || problem.includes("looks like the name of a secret")
    || problem.includes("looks like a secret");
}

// As a session sees it

/** The layer that wins, as the row says it. */
export function resolvedSource(v: EnvResolvedVariable, repo: string): string {
  if (v.value === undefined || v.layer === undefined) return v.reason ?? (v.state === "policyBlocked" ? "Blocked by policy" : "Not given");
  const name = v.layer === "yours" ? "Yours" : v.layer === "repo" ? `Repo ${repo}` : "Login";
  const over = v.over ?? [];
  if (over.length > 0) return `${name} (wins over ${over.map((l) => (l === "repo" ? "the repo" : l === "yours" ? "yours" : "the login")).join(", ")})`;
  return name;
}

export function asSessionTitle(folder: string, machine?: string): string {
  return `As a session sees it: ${repoName(folder)}` + (machine === undefined ? "" : ` on ${machine}`);
}

/** A folder as a path, from a path or a file URL, with no "/" at its end: the folders of the pickers, once each. */
export function folderPath(folder: string): string {
  let path = folder;
  if (path.startsWith("file://")) {
    try {
      path = decodeURIComponent(new URL(path).pathname);
    } catch {
      // Not a URL after all: keep it as it is.
    }
  }
  return path.length > 1 && path.endsWith("/") ? path.slice(0, -1) : path;
}

/** The folders a picker offers: each repo file's, then the others the machine has, once each, by name. */
export function pickerFolders(files: readonly EnvFile[], known: readonly string[]): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const f of [...files.flatMap((file) => (file.layer === "repo" && file.folder ? [file.folder] : [])), ...known]) {
    const path = folderPath(f);
    if (!path || seen.has(path)) continue;
    seen.add(path);
    out.push(path);
  }
  return out.sort((a, b) => repoName(a).localeCompare(repoName(b), undefined, { sensitivity: "base" }));
}
