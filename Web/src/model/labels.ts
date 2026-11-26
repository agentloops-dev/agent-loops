// Labels on a session and the search field's query: SessionLabelPolicy (Model/SessionLabel.swift)
// and SessionLabelQuery (Model/SessionLabelQuery.swift), ported by hand and held to
// Fixtures/web/labels (research R7).
import type { Agent, LineageSessionIssue, SessionLabel } from "../protocol/generated";

/** Plain labels. Links (`#12`, `ENG-4`, `P#12`) and the finished words (only three) are not
 *  counted in it (#7241), so a lane that joins several issues still links its PR and shows it merged. */
export const maximumLabels = 5;
/** Links to issues and pull requests, a bound of their own. */
export const maximumLinks = 20;
export const maximumLabelLength = 24;

/** Swift's whitespacesAndNewlines, near enough for typed text. */
function trim(value: string): string {
  return value.replace(/^[\s\u0085]+|[\s\u0085]+$/gu, "");
}

export function labelKey(value: string): string {
  return trim(value).toLowerCase();
}

/** A typed value as a label, or null when empty or too long. Counted in characters, as Swift does. */
export function cleaned(value: string): string | null {
  const result = trim(value);
  if (!result || [...new Intl.Segmenter().segment(result)].length > maximumLabelLength) return null;
  return result;
}

/** Everything before the last comma is finished labels; what follows is still being typed. */
export function splitTyped(text: string): { finished: string[]; remainder: string } {
  const pieces = text.split(",");
  const remainder = pieces.pop() ?? "";
  return { finished: pieces, remainder };
}

type Counted = "plain" | "link" | "finished";

function counted(value: string): Counted {
  const kind = labelKind(value);
  return kind === "issue" || kind === "pullRequest" ? "link" : kind;
}

/** Why one more label like `value` does not fit beside `existing`, or null (SessionLabelPolicy.roomRefusal). */
export function roomRefusal(value: string, existing: readonly string[]): string | null {
  const kind = counted(value);
  const same = existing.filter((l) => counted(l) === kind);
  if (kind === "link" && same.length >= maximumLinks) {
    return `A session can have at most ${maximumLinks} links to issues and pull requests (#12, P#12). It has ${same.join(", ")}.`;
  }
  if (kind === "plain" && same.length >= maximumLabels) {
    return `A session can have at most five labels, apart from links (#12, P#12) and merged, closed or archived. It has ${same.join(", ")}.`;
  }
  return null;
}

/** Whether the field still takes a label of a bounded kind (SessionLabelPolicy.hasRoom). */
export function hasRoom(existing: readonly string[]): boolean {
  return roomRefusal("label", existing) === null || roomRefusal("#1", existing) === null;
}

/** Why a whole set of labels is refused, if it is (SessionLabelPolicy.refusal(of:), a start's labels). */
export function setRefusal(labels: readonly string[]): string | null {
  const kept: string[] = [];
  for (const value of labels) {
    const refusal = roomRefusal(value, kept);
    if (refusal) return refusal;
    kept.push(value);
  }
  return null;
}

/** The typed values a session can take: cleaned, new, not repeated, and only as many as fit. */
export function accepted(typed: readonly string[], existing: readonly string[]): string[] {
  const seen = new Set(existing.map(labelKey));
  const result: string[] = [];
  for (const raw of typed) {
    const value = cleaned(raw);
    if (value === null || roomRefusal(value, [...existing, ...result]) !== null || seen.has(labelKey(value))) continue;
    seen.add(labelKey(value));
    result.push(value);
  }
  return result;
}

export type LabelKind = "issue" | "pullRequest" | "finished" | "plain";

/** What a label names, by its text alone, so a chip can be coloured by it (SessionLabel.kind, #2958). */
export function labelKind(value: string): LabelKind {
  const text = trim(value);
  if (/^#[0-9]+$/.test(text) || /^[A-Z][A-Z0-9]*-[0-9]+$/.test(text)) return "issue";
  if (/^[Pp]#[0-9]+$/.test(text)) return "pullRequest";
  if (["merged", "closed", "archived"].includes(text.toLowerCase())) return "finished";
  return "plain";
}

/** A label chip's classes: what it names, and `agent` for one the agent added, outlined and
 *  not filled, as LabelChip draws it on the Mac and the Remote (#2997). */
export function labelClass(label: Pick<SessionLabel, "value" | "owner">): string {
  return `chip label ${labelKind(label.value)}${label.owner === "agent" ? " agent" : ""}`;
}

export interface LabelQuery {
  label: string | null;
  /** The issue the search names (#3899): `#3465`, `issue:#3465` or `issue:3465`, as a word of its own. */
  issue: number | null;
  text: string;
  /** The sessions whose prompts or replies hold the words, by id, as their hosts found them (agents/said, #1585). */
  said?: Readonly<Record<string, string>>;
  /** The sessions Lineage links to the issue, its sub-issues or their PRs, by id (lineage/sessions, #3899). */
  onIssue?: Readonly<Record<string, unknown>>;
  /** The issue, then its sub-issues in the epic's order, as the hosts said them (lineage/sessions, #3900). */
  issueHeadings?: readonly LineageSessionIssue[];
}

/** The first match of `pattern` in `input`, taken out of it: its first capture that matched, or "". */
function take(pattern: RegExp, input: string): { value: string | null; rest: string } {
  const match = pattern.exec(input);
  if (!match) return { value: null, rest: input };
  return { value: match[1] ?? match[2] ?? "", rest: input.slice(0, match.index) + input.slice(match.index + match[0].length) };
}

/** The search field's words: `label:x` or `label:"x y"` filters, `#<n>` asks for an issue's sessions,
 *  the rest matches title and report. */
export function parseQuery(input: string): LabelQuery {
  const label = take(/(?:^|\s)label:(?:"([^"]*)"|(\S*))/i, input);
  const issue = take(/(?:^|\s)(?:issue:#?|#)([0-9]{1,9})(?=\s|$)/i, label.rest);
  return { label: label.value, issue: issue.value === null ? null : Number(issue.value), text: trim(issue.rest) };
}

/** Whether text holds `#<n>` as a number of its own: `#38` is not in "#3899". */
function namesIssue(text: string, mark: string): boolean {
  for (let at = text.indexOf(mark); at >= 0; at = text.indexOf(mark, at + mark.length)) {
    if (!/[0-9]/.test(text.charAt(at + mark.length))) return true;
  }
  return false;
}

/** Whether the search asks for this label, so its chip can look chosen (#3898). */
export function asksFor(query: LabelQuery, label: string): boolean {
  return !!query.label && labelKey(query.label) === labelKey(label);
}

/** The search after a click on a label's chip (#3898): it asks for that label in place of any
 *  other, and keeps the words. A second click, when it already asks for it, takes it out again. */
export function toggleLabel(input: string, label: string): string {
  const query = parseQuery(input);
  if (asksFor(query, label)) return query.text;
  const token = /\s/u.test(label) ? `label:"${label}"` : `label:${label}`;
  return query.text ? `${token} ${query.text}` : token;
}

function containsIgnoringCase(haystack: string, needle: string): boolean {
  return haystack.toLocaleLowerCase().includes(needle.toLocaleLowerCase());
}

export function queryMatches(query: LabelQuery, agent: Agent): boolean {
  if (query.issue !== null) {
    const mark = `#${query.issue}`;
    if (query.onIssue?.[agent.id] === undefined && !(agent.labels ?? []).some((label) => labelKey(label.value) === mark)
      && !(agent.title !== undefined && namesIssue(agent.title, mark))) return false;
  }
  if (query.label !== null) {
    const wanted = labelKey(query.label);
    if (!wanted || !(agent.labels ?? []).some((label) => labelKey(label.value) === wanted)) return false;
  }
  if (!query.text) return true;
  return [agent.title, agent.report?.message].some((value) => value !== undefined && containsIgnoringCase(value, query.text))
    || query.said?.[agent.id] !== undefined;
}
