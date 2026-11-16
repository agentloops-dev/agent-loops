// The ⌘K command menu's secrets, accounts and environment variables (#5261, epic #5254,
// specs/5254-secrets-accounts-environment/wireframes.md section 7): each found by its name, and opened on its page
// (Status ▸ Secrets, Accounts or Environment) with it selected. A row says the kind and the scope, never a value: an
// item holds none, so the menu can match none. The window's rules are the same, CommandMenuSetup in
// Packages/AgentsKit/Sources/AgentsKitCore/Model/CommandMenuSetup.swift.
//
// The menu lists the person's own secrets (Yours) and accounts, and the variables of the files of one machine (Yours and
// each repo's), read when the menu opens. A secret for one session or on one machine is found on the Secrets page's
// search, which reads them from the machine.
import type { EnvFile, OutsideAccount, UserSecretRow } from "../protocol/generated";
import { accountID, accountName } from "./accountsPage";
import { id as variableID, repoName, splitLine } from "./environmentPage";
import type { CommandItem } from "./commandMenu";

const byName = (a: string, b: string) => a.localeCompare(b, undefined, { numeric: true });

/** The person's own secrets: `secret:yours/NAME`, "Secret · Yours" (or "Card · Yours"). */
export function secretItems(rows: readonly UserSecretRow[]): CommandItem[] {
  return [...rows].sort((a, b) => byName(a.name, b.name)).map((row) => ({
    id: `secret:yours/${row.name}`, kind: "secret", title: row.name, detail: `${row.kind === "card" ? "Card" : "Secret"} · Yours`,
  }));
}

/** The person's accounts: `account:<id>`, "GitHub · alexec", "Account". Its service and address find it too. */
export function accountItems(accounts: readonly OutsideAccount[]): CommandItem[] {
  return [...accounts].sort((a, b) => byName(accountName(a), accountName(b))).map((a) => ({
    id: `account:${accountID(a)}`, kind: "account", title: accountName(a), detail: "Account",
    keywords: [a.service, a.address ?? ""].filter(Boolean),
  }));
}

/** The names in a `~/.agents/env` text, with no values: the menu has no use for one. */
function names(text: string): EnvFile["variables"] {
  return text.split(/\r?\n/).flatMap((line, i) => {
    const pair = line.trim().startsWith("#") ? null : splitLine(line);
    return pair ? [{ name: pair.name, value: "", line: i + 1, state: "given" as const }] : [];
  });
}

/** The variables of a machine's files, Yours first: `variable:yours/NAME` or `variable:repo/<folder>/NAME`,
 * "Environment · Yours" or "Environment · <repo>". `yoursText` is the person's `~/.agents/env` from their settings, for
 * Yours when the machine read no Yours file or an empty one (a machine with no personal home has none). */
export function variableItems(files: readonly EnvFile[], yoursText?: string): CommandItem[] {
  const all = [...files];
  if (!all.some((f) => f.layer === "yours" && f.variables.length > 0) && yoursText !== undefined) {
    for (let i = all.length - 1; i >= 0; i--) if (all[i]!.layer === "yours") all.splice(i, 1);
    all.unshift({ layer: "yours", path: "~/.agents/env", exists: true, variables: names(yoursText) });
  }
  const repoOf = (f: EnvFile) => repoName(f.folder ?? f.path);
  const repos = all.filter((f) => f.layer === "repo").sort((a, b) => byName(repoOf(a), repoOf(b)));
  return [...all.filter((f) => f.layer === "yours"), ...repos].flatMap((file) => {
    const scope = file.layer === "yours" ? "Yours" : repoOf(file);
    return [...file.variables].sort((a, b) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0)).map((v): CommandItem => ({
      id: `variable:${variableID(file.layer, file.folder, v.name)}`, kind: "variable", title: v.name, detail: `Environment · ${scope}`,
    }));
  });
}

/** All three, in the menu's order: the secrets, the accounts, then the variables. */
export function setupItems(secrets: readonly UserSecretRow[], accounts: readonly OutsideAccount[], files: readonly EnvFile[],
  yoursText?: string): CommandItem[] {
  return [...secretItems(secrets), ...accountItems(accounts), ...variableItems(files, yoursText)];
}

/** The page an item opens and the address of what it selects there, as the page writes it: a secret of Yours by its
 * name (`#/secrets/NAME`), an account by its id, a variable by `yours/NAME` or `repo/<folder>/NAME`; undefined for an
 * item that is not one of these. */
export function setupPlace(item: Pick<CommandItem, "id">): { section: "secrets" | "accounts" | "environment"; item: string } | undefined {
  const cut = item.id.indexOf(":");
  const rest = item.id.slice(cut + 1);
  switch (item.id.slice(0, cut)) {
    case "secret": return { section: "secrets", item: rest.startsWith("yours/") ? rest.slice("yours/".length) : rest };
    case "account": return { section: "accounts", item: rest };
    case "variable": return { section: "environment", item: rest };
    default: return undefined;
  }
}
