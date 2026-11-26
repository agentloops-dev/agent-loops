// The commitTrailers rule (#6365): each trailer of an agent's commits is on or off, by its key. The
// same keys, kinds and defaults as Go/internal/policylayers/committrailers.go. A record trailer is a
// record of agent work, and on is tighter: a folder or an organisation can only require it. A spend
// trailer says what the work cost, and off is tighter: a folder or an organisation can only keep it out.
import type { CommitTrailers, CommitTrailerValue } from "../protocol/generated";

export type TrailerKind = "record" | "spend";

export interface Trailer {
  key: string;
  kind: TrailerKind;
  /** The value when no layer sets the key. */
  on: boolean;
  /** An example value, as a commit shows it, in the mono font: the same as the Mac's (CommitTrailerWords, #6533). */
  example: string;
}

/**
 * Every trailer this client knows: the record trailers, then the spend trailers, as the panes list them.
 * No Co-authored-by: it is the runtime's own line, which the app never adds, changes or removes (#7174).
 */
export const trailers: readonly Trailer[] = [
  { key: "Managed-by", kind: "record", on: true, example: "Agent Loops <noreply@agentloops.dev>" },
  { key: "Runtime", kind: "record", on: true, example: "claude-code" },
  { key: "Model", kind: "record", on: true, example: "opus-5-5" },
  { key: "Session", kind: "record", on: true, example: "4f2c9a1e-…" },
  { key: "Fixes", kind: "record", on: true, example: "#6334" },
  { key: "Epic", kind: "record", on: true, example: "#5627" },
  { key: "Requested-by", kind: "record", on: true, example: "alexec" },
  { key: "Approved-by", kind: "record", on: true, example: "alexec" },
  { key: "Cost", kind: "spend", on: false, example: "$1.84" },
  { key: "Tokens", kind: "spend", on: true, example: "in=412.0k out=38.0k" },
  { key: "Duration", kind: "spend", on: true, example: "47m" },
];

/** The capability of a machine that follows the rule (#6371). */
export const capability = "host.commit-trailers/1";

export const title = "Commit trailers";
export const sub = "Lines at the end of each commit that an agent makes.";
export const kindTitle: Record<TrailerKind, string> = { record: "Record", spend: "Spend" };
/** The direction of each sub-group, for an organisation's policy (#6375). */
export const orgKindHelp: Record<TrailerKind, string> = {
  record: "Your organisation can require these. On holds every folder and person to it; Off leaves them free to turn it on.",
  spend: "Your organisation can keep these out. Off holds every folder and person to it; On leaves them free to turn it off.",
};

/** A key's choice in a form: on, off, or not set (""). */
export type TrailerChoice = CommitTrailerValue | "";

/** "Not set (On)": the choice for no rule, with the value a key then has. */
export function notSetName(t: Trailer): string {
  return `Not set (${t.on ? "On" : "Off"})`;
}

/** Whether a value holds the layers below to it: on for a record trailer, off for a spend trailer. */
export function binds(t: Trailer, value: CommitTrailerValue): boolean {
  return (value === "on") === (t.kind === "record");
}

/** A key's value set, in words: "On, required", "Off, kept out of commits", or the loose value. */
export function valueWords(t: Trailer, value: CommitTrailerValue): string {
  if (binds(t, value)) return t.kind === "record" ? "On, required" : "Off, kept out of commits";
  return value === "on" ? "On unless a folder or person turns it off" : "Off unless a folder or person turns it on";
}

/** Each known key's choice in a layer's rule. */
export function choicesOf(rule: CommitTrailers | undefined): Record<string, TrailerChoice> {
  return Object.fromEntries(trailers.map((t) => {
    const v = rule?.[t.key];
    return [t.key, v === "on" || v === "off" ? v : ""];
  }));
}

/**
 * The rule the choices say, on top of the one loaded: a key this client does not know (from a newer
 * machine) is kept as it was. Undefined when no key is set.
 */
export function ruleOf(choices: Record<string, TrailerChoice>, base: CommitTrailers | undefined): CommitTrailers | undefined {
  const known = new Set(trailers.map((t) => t.key));
  const out: CommitTrailers = Object.fromEntries(Object.entries(base ?? {}).filter(([k]) => !known.has(k)));
  for (const t of trailers) {
    const c = choices[t.key];
    if (c === "on" || c === "off") out[t.key] = c;
  }
  return Object.keys(out).length > 0 ? out : undefined;
}
