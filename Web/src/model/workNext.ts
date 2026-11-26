// Work ▸ Next (#5691, epic #5687): the rows of forge/next (#5689) for one project's folder, the work that is ready in
// the one order (on hold last, then priority, the milestone's due date and the oldest), each with its place and the
// reasons for it. A filter for one epic, and the epics in order (level epic). On hold rows go at the end, under their
// own heading, with no place: no lead takes one next. The page is Web + Mac link (#4937): the Mac's Issues list has
// Next ↗, which opens #/work/next?folder=<path>.
import type { AutopilotRow, AutopilotStatus, ForgeNext, ForgeNextRow, LineageNode } from "../protocol/generated";
import { fromWireDate } from "../protocol/dates";
import { shortAge } from "./work";
import type { WorkProject } from "./work";

/** What the page shows: the ready issues, or the open epics in the same order. */
export type NextLevel = "issue" | "epic";
export const nextLevels: readonly NextLevel[] = ["issue", "epic"];
export const nextLevelTitle: Record<NextLevel, string> = { issue: "Issues", epic: "Epics" };

/** The page's own state, in its address: #/work/next?project=<id>&epic=<ref>&level=epic, or ?folder=<path> from the Mac. */
export interface NextChoice {
  project: string;
  /** An epic's lineage ref: only its children. "" for every issue. */
  epic: string;
  level: NextLevel;
}

/** The address's words: the project (or a folder's path, which the Mac's link names), the epic and the level. */
export function parseNextQuery(query: string | undefined, projects: readonly WorkProject[]): NextChoice {
  const q = new URLSearchParams(query ?? "");
  const folder = q.get("folder");
  const project = q.get("project") ?? (folder ? projectOfFolder(projects, folder) ?? "" : "");
  const level = q.get("level") === "epic" ? "epic" : "issue";
  return { project, epic: level === "epic" ? "" : q.get("epic") ?? "", level };
}

/** The address's words for a choice, each left out while it is its default. */
export function nextQuery(c: NextChoice): string {
  const q = new URLSearchParams();
  if (c.project) q.set("project", c.project);
  if (c.level === "epic") q.set("level", "epic");
  else if (c.epic) q.set("epic", c.epic);
  return q.toString();
}

/** A folder's path, with no slash at its end: as workFolderKey writes it after the host. */
const pathOf = (key: string) => key.slice(key.indexOf("|") + 1);
const trimmed = (path: string) => path.replace(/(.)\/+$/, "$1");

/** The project that holds a folder at `path` on any machine: the Mac names its folder by path, as its host id may not
 * be the one this page knows. */
export function projectOfFolder(projects: readonly WorkProject[], path: string): string | undefined {
  const want = trimmed(path);
  return projects.find((p) => [...p.folders].some((f) => pathOf(f) === want))?.id;
}

/** The project's folders that a machine in `hosts` reads, as forge/next asks them: the host, then the folder's path.
 * The first machine of `hosts` first, so the window's own Mac goes before the others when it is there. */
export function nextFolders(project: WorkProject | undefined, hosts: readonly string[]): { host: string; folder: string }[] {
  if (!project) return [];
  const out: { host: string; folder: string }[] = [];
  for (const host of hosts) {
    for (const key of project.folders) if (key.slice(0, key.indexOf("|")) === host) out.push({ host, folder: pathOf(key) });
  }
  return out;
}

/** The epic as forge/next takes it, from its lineage ref (`github:issue:owner/repo#12`). */
export function epicNode(ref: string): LineageNode | undefined {
  const kind = ref.split(":")[1];
  return ref && kind ? { kind, ref } : undefined;
}

/** The rows that are ready, then those on hold under their own heading. */
export function splitNext(next: ForgeNext | null): { ready: ForgeNextRow[]; onHold: ForgeNextRow[] } {
  const rows = next?.rows ?? [];
  return { ready: rows.filter((r) => !r.onHold), onHold: rows.filter((r) => r.onHold) };
}

/** The reasons for a row's place, as one line: priority high · milestone Work, due 20 Nov · oldest. */
export const reasonsWords = (row: ForgeNextRow) => row.reasons.join(" · ");

/** The item's short number: #5691, or a Linear or Jira key as it is. */
export function itemNumber(ref: string): string {
  const hash = ref.lastIndexOf("#");
  return hash >= 0 ? `#${ref.slice(hash + 1)}` : ref.slice(ref.lastIndexOf(":") + 1);
}

/** The Epic filter's choices: the open epics, in forge/next's order, by their number and title. */
export function epicChoices(epics: ForgeNext | null): { value: string; words: string }[] {
  return (epics?.rows ?? []).map((r) => ({ value: r.item.node.ref, words: `${itemNumber(r.item.node.ref)} ${r.item.title}` }));
}

export const allEpics = "All epics";
export const onHoldTitle = "On hold";
export const onHoldLine = "Not picked up next. Take the on hold label off to give one a place.";
export const noMachine = "No machine reads a forge";
export const noProjectTitle = "No project reads a forge";
export const noProject = "Add a folder whose repo is on a forge, and its ready work shows here.";
export const placeHelp = "Its place in the order: on hold last, then priority, the milestone’s due date and the oldest";

/** The middle of a page with no row. */
export function emptyTitle(c: NextChoice): string {
  if (c.level === "epic") return "No open epics";
  return c.epic ? "Nothing in this epic is ready" : "Nothing is ready";
}

export function emptyLine(c: NextChoice): string {
  if (c.level === "epic") return "An open issue with the epic label shows here, in the order a lead is given one.";
  return "An open issue shows here when nothing blocks it and no session, pull request or person has it.";
}

/** What a machine said when its Agent Loops has no forge/next: it is older than #5689. */
export function nextProblem(words: string): string {
  return /method not found|unknown method|no such method/i.test(words) ? "This machine’s Agent Loops is older than Work ▸ Next. Update it." : words;
}

// Autopilot (#5905, epic #5897): what it starts next and why each item waits, from autopilot/status (#5898).

/** The strip at the top: Off, or On with when it last ran, or Paused with the oldest pause's reason and age. */
export function autopilotStrip(st: AutopilotStatus | null, now: number): { text: string; paused: boolean } | null {
  if (!st) return null;
  if (st.mode === "off") return { text: "Autopilot: Off · turn it on in Project Settings", paused: false };
  const paused = st.rows.filter((r) => r.state === "paused").sort((a, b) => fromWireDate(a.since).getTime() - fromWireDate(b.since).getTime())[0];
  if (paused) return { text: `Paused: ${paused.reason.replace(/^paused: /, "")} for ${ageWords(fromWireDate(paused.since).getTime(), now)}`, paused: true };
  const mode = st.mode === "high" ? "On, high priority only" : "On";
  return { text: st.lastRunAt ? `Autopilot: ${mode} · last run ${ageWords(fromWireDate(st.lastRunAt).getTime(), now)} ago` : `Autopilot: ${mode} · not run yet`, paused: false };
}

/** "2 min", "3 h": how long ago, without "ago". */
function ageWords(t: number, now: number): string {
  const age = shortAge(Math.max(0, (now - t) / 1000));
  return age === "now" ? "a moment" : age;
}

/** Autopilot's row for an item: its status rows name an item as the forge does (#123). */
export function autopilotRowOf(st: AutopilotStatus | null, ref: string): AutopilotRow | undefined {
  const number = itemNumber(ref);
  return st?.rows.find((r) => r.ref === number || r.ref === ref);
}

/** The Autopilot cell: Starts next (the first ready item with no row), Started, or the wait reason. */
export function autopilotCell(st: AutopilotStatus | null, row: ForgeNextRow, firstFree: boolean): { words: string; resume: string | null } | null {
  if (!st || st.mode === "off") return null;
  const a = autopilotRowOf(st, row.item.node.ref);
  if (!a) return row.onHold ? { words: "on hold", resume: null } : { words: firstFree ? "Starts next" : "", resume: null };
  if (a.state === "started") return { words: "Started", resume: null };
  return { words: a.reason, resume: a.state === "paused" ? a.ref : null };
}

/** The ref of the first ready row that Autopilot has no row for: the item it starts next. */
export function startsNext(st: AutopilotStatus | null, ready: readonly ForgeNextRow[]): string | undefined {
  if (!st || st.mode === "off") return undefined;
  return ready.find((r) => !autopilotRowOf(st, r.item.node.ref))?.item.node.ref;
}

export const resumeTitle = "Resume";
export const resumeHelp = "Autopilot may start this epic's work again";
