// Each Work list's state (#3257), as the window's WorkListState: its filters, sort, the rows read and
// the cursors to the next pages, kept per kind for the tab's life. An item's page goes through the list
// it was opened from (Previous and Next, j and k), and Back finds the list as it was left.
import { signal, type Signal } from "@preact/signals";
import type { ForgeCapabilities, ForgeItem, ForgeListAll, ForgeListError, ProjectBoard, ReportNode, ReportNodesSort, WireDate } from "../protocol/generated";
import type { Store } from "./store";
import { Capability } from "../protocol/capabilities.generated";
import { CallFailed } from "../wire/link";
import { describe } from "./errors";
import { hostAnswers } from "./capabilities";
import { keepWork, keptWorkRead, queryKey, workCache, type WorkAnswer, type WorkQuery } from "./workCache";
import { mark } from "./marks";
import {
  filterRows, forgeListAllRequest, fromForge, lineageRow, listReadAt, mergeBoards, mergeForge, neighbours, parseWorkQuery, projectRepos, refForge,
  shownErrors, sortRows, workQuery,
  type Neighbours, type WorkFilter, type WorkKind, type WorkProject, type WorkProjectRepos, type WorkRow, type WorkSort, type WorkState,
} from "./work";
import { rememberEach, remembered } from "./workRemembered";
import { joinForges, rowSetsPriority } from "./forgeNextOrder";

/** How many pages of reports/nodes a list reads for its lineage: at most 500 nodes. */
const lineagePages = 5;

/** A machine's page of a forge list, and the cursor to its next. */
export interface HostList {
  host: string; items: ForgeItem[]; cursor?: string | undefined; errors: ForgeListError[];
  /** How many items the whole list has, from the machine's first page (#4396); none when a forge on it gives no count. */
  total?: number | undefined;
  /** The refs of the items carried from the last read for a forge this read could not reach, shown dimmed (#4392). */
  stale?: string[] | undefined;
  /** What each forge on the list fills and sets (#4361): a row's menu offers Priority and On hold by it (epic #6086). */
  forges?: ForgeCapabilities[] | undefined;
  /** How many of the machine's pages Work's cache keeps (#6356): the next page is kept after them. */
  pages?: number | undefined;
  /** When the machine read its forges for it (#6642); none from an earlier daemon. */
  readAt?: WireDate | undefined;
  /** What the list reads (#6641): forge:repo per repo; none from an earlier daemon. */
  scopes?: string[] | undefined;
}

export interface WorkListState {
  kind: WorkKind;
  filter: Signal<WorkFilter>;
  /** The person's projects (#3283), as the view last said them, for the Project filter. */
  projects: Signal<readonly WorkProject[]>;
  sort: Signal<WorkSort>;
  lists: Signal<HostList[] | null>;
  /** A read runs: the rows read before stay (#4392), and Refresh spins. */
  loading: Signal<boolean>;
  lineage: Signal<ReportNode[]>;
  lineageCursor: Signal<string | undefined>;
  problem: Signal<string | null>;
  more: Signal<boolean>;
  /** What a page after the first said when it did not load (lists.md section 5): "Could not load more." at the end. */
  moreFailed: Signal<string | null>;
  asked: Signal<number>;
  /** The row whose page was opened last, which the list scrolls to when it is shown again. */
  opened: Signal<string | undefined>;
  /** What the rows were read for; the list reads them again only when that changes. */
  read: string | undefined;
  /** The Refresh count (`asked`) of that read: a higher one asks the forges, not the daemons' kept answers (#6642). */
  readAsked: number;
  /** The rows on screen came from this device's copy (#4930): the machines are asked again, and the list says so. */
  fromDevice: Signal<boolean>;
  /** When the rows on screen were read (#5142): this device's copy's time, or the last read; with rows carried over a
   * failed read, the oldest. The "Last update" line of the strips. */
  readAt: Signal<number | undefined>;
  /** What the rows on screen were read for, without Refresh (`copyAsk`): the copy is drawn only for another. */
  shownFor: string | undefined;
}

/** The lineage figures a list keeps in Work's cache (#6356), beside its items: Releases' whole list, and the figures an
 * issue or PR list joins by ref. */
export interface KeptLineage { lineage: ReportNode[]; lineageCursor?: string | undefined }

const lists = new Map<WorkKind, WorkListState>();

/** The page opened from its list (#3257), "kind ref"; a page opened from a link or an address is not it. */
export const fromList = signal<string | undefined>(undefined);
export const placeKey = (kind: WorkKind, ref: string) => `${kind} ${ref}`;

export function workList(kind: WorkKind): WorkListState {
  let l = lists.get(kind);
  if (!l) {
    // It opens with the filters and the sort it had when last left (#5451).
    const kept = parseWorkQuery(remembered(kind) ?? "", kind);
    l = {
      kind, filter: signal(kept.filter), projects: signal([]), sort: signal<WorkSort>(kept.sort), lists: signal(null), loading: signal(false), lineage: signal([]),
      lineageCursor: signal(undefined), problem: signal(null), more: signal(false), moreFailed: signal(null), asked: signal(0), opened: signal(undefined),
      read: undefined, readAsked: 0, fromDevice: signal(false), readAt: signal(undefined), shownFor: undefined,
    };
    lists.set(kind, l);
    const { filter, sort } = l;
    rememberEach(kind, () => workQuery(filter.value, sort.value, kind));
  }
  return l;
}

export const forgeState = (s: WorkState) => s === "open" ? "open" : s === "all" ? "all" : "closed";

/** An issue's or a PR's lineage is joined by ref, in any order; a release's list is lineage's own. */
export const nodesSort = (l: WorkListState): ReportNodesSort => !fromForge(l.kind) && l.sort.value === "cost" ? "cost" : "newest";

export const forgeHosts = (store: Store) => store.hosts.value.filter((h) => hostAnswers(h.state) && store.has(h.id, Capability.forge1));

/** The machines that know boards (#3282): only they are asked for a board's list, as one that does not would list it all. */
export const boardHosts = (store: Store) => forgeHosts(store).filter((h) => store.has(h.id, Capability.forgeBoards1));

/** The machines a list is read from: with a board chosen, those that know boards. */
export const listHosts = (store: Store, board: string) => board ? boardHosts(store) : forgeHosts(store);

/** The boards of the machines' projects (forge/boards, #3282), for Issues' and Features' Board menu. */
export const boards = signal<ProjectBoard[]>([]);
let boardsRead: string | undefined;

/** Read the boards from each machine that knows them, unless they were read from those already; `again` reads them anyway. */
export async function loadBoards(store: Store, again = false): Promise<void> {
  const hosts = boardHosts(store);
  const mine = hosts.map((h) => h.id).join(",");
  if (boardsRead === mine && !again) return;
  boardsRead = mine;
  // The boards this device kept from the same machines at once (#4930), until they answer.
  if (boards.peek().length === 0) {
    const kept = await keptWorkRead<ProjectBoard[]>("boards", mine);
    if (kept && boardsRead === mine && boards.peek().length === 0) boards.value = kept.value;
  }
  // Every machine's boards in one call to the control plane (#5592), which waits for a slow machine at most 5 s.
  const asked = new Set(hosts.map((h) => h.id));
  const all = await store.link.call("forge/boardsAll", {}).catch(() => null);
  if (boardsRead !== mine) return;
  boards.value = mergeBoards((all?.boards ?? []).filter((b) => asked.has(b.machine)).map((b) => b.boards.boards));
  if (boards.peek().length > 0) keepWork("boards", mine, boards.peek());
}

/** Every row read, one per ref. */
export function allRows(l: WorkListState): WorkRow[] {
  return fromForge(l.kind) ? mergeForge(l.lists.value ?? [], l.lineage.value) : l.lineage.value.map(lineageRow);
}

/** Each project with the repos its folders read (#3283); none for releases, which lineage alone lists, with
 * no folder. */
export function listProjects(l: WorkListState): WorkProjectRepos[] {
  return fromForge(l.kind) ? projectRepos(l.projects.value, l.lists.value ?? []) : [];
}

/** The rows as the list shows them: filtered and sorted. Releases have no state in v1. */
export function shownRows(l: WorkListState): WorkRow[] {
  return sortRows(filterRows(allRows(l), fromForge(l.kind) ? l.filter.value : { ...l.filter.value, state: "all" }, listProjects(l)),
    l.sort.value);
}

export const hasMore = (l: WorkListState) => fromForge(l.kind) ? (l.lists.value ?? []).some((h) => h.cursor) : !!l.lineageCursor.value;

/** The lineage of a kind, at most `lineagePages` pages of it; none when the control plane does not list nodes. */
async function readLineage(store: Store, kind: WorkKind, sort: ReportNodesSort, cursor?: string): Promise<{ nodes: ReportNode[]; cursor?: string | undefined }> {
  const nodes: ReportNode[] = [];
  let next = cursor;
  for (let i = 0; i < lineagePages; i++) {
    const page = await store.link.call("reports/nodes", { kind, sort, limit: 100, ...next ? { cursor: next } : {} });
    nodes.push(...page.nodes);
    next = page.cursor;
    if (!next || !fromForge(kind)) break;
  }
  return { nodes, cursor: next };
}

/** What the list is read for: its state, board, sort, machines, their folders and Refresh. */
export function askKey(store: Store, l: WorkListState): string {
  const { state, board } = l.filter.value;
  const hosts = listHosts(store, board).map((h) => h.id);
  return [forgeState(state), board, nodesSort(l), forgeSort(l), hosts.join(","), forgeFolders(store, hosts), l.asked.value].join("|");
}

/** The machines' live folders, as the page knows them (#6152): a list read before a folder was added is read again when
 * one is added or goes, as Refresh does. */
export function forgeFolders(store: Store, hosts: readonly string[]): string {
  return hosts.flatMap((host) => (store.folders.value[host] ?? []).filter((f) => f.folder.archivedAt === undefined)
    .map((f) => `${host} ${f.folder.folder}`)).sort().join(",");
}

/** A read with errors keeps, dimmed under the error (#4392), the items of the last read from each forge it could not reach
 * (all of them when the machine itself failed, forge ""), unless the new read has them. */
export function carryStale(got: HostList, had: HostList | undefined): HostList {
  if (!had || got.errors.length === 0) return got;
  const failed = new Set(got.errors.map((e) => e.forge));
  const fresh = new Set(got.items.map((i) => i.node.ref));
  const kept = had.items.filter((i) => !fresh.has(i.node.ref) && (failed.has("") || failed.has(i.node.ref.slice(0, i.node.ref.indexOf(":")))));
  return kept.length === 0 ? got : { ...got, items: [...got.items, ...kept], stale: kept.map((i) => i.node.ref) };
}

/** What a machine the control plane could not reach for a list says (#5592). */
export const notAnswered = "It did not answer.";

/** forge/listAll's answer (#5592) as a HostList for each of `hosts` it names, in their order, each carrying what `before` had
 * for a forge it could not read (#4392). A machine it could not reach has an error for all its forges; one it names that is not
 * of `hosts` (offline when the page asked) is left out, as it was not asked before. */
export function hostListsOf(got: ForgeListAll, hosts: readonly string[], before: readonly HostList[]): HostList[] {
  const by = new Map<string, HostList>();
  for (const l of got.lists) by.set(l.machine, { host: l.machine, items: l.list.items, cursor: l.list.cursor, errors: l.list.errors ?? [], total: l.list.total, forges: l.list.forges, readAt: l.list.readAt, scopes: l.list.scopes });
  for (const u of got.unreached) by.set(u.machine, { host: u.machine, items: [], errors: [{ forge: "", message: notAnswered }] });
  return hosts.flatMap((h) => {
    const list = by.get(h);
    return list ? [carryStale(list, before.find((b) => b.host === h))] : [];
  });
}

/** The machines to ask a list's page 1 (#6641), in `hosts`' order, and those left out: a machine whose scopes are known is
 * left out when the machines asked already read every one of them, so each repo is asked once, not once per machine. The
 * machines that read the most go first, so one that reads all covers the others; one whose scopes are not known is always
 * asked. As the window's `WorkList.oneEach`. */
export function oneEach(hosts: readonly string[], scopes: ReadonlyMap<string, readonly string[]>): { ask: string[]; covered: string[] } {
  const count = (h: string) => scopes.get(h)?.length ?? Number.MAX_SAFE_INTEGER;
  const order = hosts.map((h, i) => ({ h, i })).sort((a, b) => count(b.h) - count(a.h) || a.i - b.i);
  const read = new Set<string>();
  const covered = new Set<string>();
  for (const { h } of order) {
    const known = scopes.get(h);
    if (!known) continue;
    if (known.every((s) => read.has(s))) covered.add(h);
    else for (const s of known) read.add(s);
  }
  return { ask: hosts.filter((h) => !covered.has(h)), covered: hosts.filter((h) => covered.has(h)) };
}

/** A machine's scopes as kept on this device (#6641), with the machines and folders the list was read from then: when those
 * change, what it reads is not known until it is asked again. Kept by the kind and the board, as a board narrows a list. */
interface KeptScopes { scopes: string[]; source: string }
const scopesPage = "work-scopes";
const scopesAsked = (kind: WorkKind, board: string, host: string) => [kind, board, host].join("|");

async function keptScopes(kind: WorkKind, board: string, hosts: readonly string[], source: string): Promise<Map<string, string[]>> {
  const kept = await Promise.all(hosts.map((h) => keptWorkRead<KeptScopes>(scopesPage, scopesAsked(kind, board, h))));
  return new Map(hosts.flatMap((h, i) => {
    const k = kept[i]?.value;
    return k && k.source === source ? [[h, k.scopes] as const] : [];
  }));
}

/** Each machine's list with only the errors the page shows (#5449): one machine's answer for a forge is enough, so the
 * others' errors for it go, and with them the rows they carried from the last read (#4392). */
export function settleLists(read: readonly HostList[]): HostList[] {
  const shown = shownErrors(read.map((h) => ({ refs: h.items.filter((i) => !h.stale?.includes(i.node.ref)).map((i) => i.node.ref), errors: h.errors })));
  return read.map((h, i) => {
    const errors = shown[i]!;
    if (errors.length === h.errors.length) return h;
    const failed = new Set(errors.map((e) => e.forge));
    const keep = (ref: string) => failed.has("") || failed.has(refForge(ref));
    const gone = new Set((h.stale ?? []).filter((ref) => !keep(ref)));
    const stale = (h.stale ?? []).filter(keep);
    return { ...h, errors, items: h.items.filter((it) => !gone.has(it.node.ref)), stale: stale.length > 0 ? stale : undefined };
  });
}

/** The forge's order for the list's sort (#6538): Priority is the daemon's priority order, else the last updated first;
 * Newest and Cost sort the answer on the page. */
export const forgeSort = (l: WorkListState): WorkQuery["sort"] => l.sort.peek() === "priority" ? "priority" : "updated";

/** What an issue or PR list asks the forge, and Work's cache keeps the answer under (#6356): the label, the assignee, the
 * words and the project narrow the kept answer on the page. */
export function cacheQuery(l: WorkListState): WorkQuery {
  const { state, board } = l.filter.peek();
  return { kind: l.kind === "pr" ? "pr" : "issue", state: forgeState(state), board: board || undefined, sort: forgeSort(l) };
}

/** What a list's answer is kept under (#4930, #6356): the cache's query, or Releases' kind and sort. */
export function copyAsk(l: WorkListState): string {
  return fromForge(l.kind) ? queryKey(cacheQuery(l)) : [l.kind, nodesSort(l)].join("|");
}

/** The kept answer on screen: the machines asked now, in their order, each with its kept pages. */
function shownAnswer(answer: WorkAnswer, hosts: readonly string[]): HostList[] {
  return hosts.flatMap((host) => {
    const a = answer.hosts.find((h) => h.host === host);
    return a ? [{ host, items: a.items, cursor: a.cursor, errors: a.errors ?? [], total: a.total, forges: a.forges, pages: a.fresh }] : [];
  });
}

/** Draws the answer this device kept for what is asked, with no call, unless rows read for it are on screen (#4930,
 * #6356): each machine's kept pages, and the lineage figures. */
export async function drawKept(store: Store, l: WorkListState): Promise<boolean> {
  const wanted = copyAsk(l);
  if (l.shownFor === wanted) return false;
  const cache = workCache();
  if (!cache) return false;
  const hosts = listHosts(store, l.filter.peek().board).map((h) => h.id);
  const [answer, figures] = await Promise.all([
    fromForge(l.kind) ? cache.answer(cacheQuery(l), hosts) : Promise.resolve(undefined),
    keptWorkRead<KeptLineage>("lineage", wanted),
  ]);
  if (copyAsk(l) !== wanted || l.shownFor === wanted) return false;
  if (fromForge(l.kind) ? !answer : !figures) return false;
  l.readAt.value = answer?.read ?? figures?.at;
  l.lineage.value = figures?.value.lineage ?? [];
  l.lineageCursor.value = figures?.value.lineageCursor;
  l.lists.value = answer ? shownAnswer(answer, hosts) : [];
  l.shownFor = wanted;
  l.fromDevice.value = true;
  // "Work list, second open" (#4930): from the read's start to the kept rows, which the night's walk times.
  mark("work-list-copy", "work-list-read");
  return true;
}

/** Each machine's answer kept in Work's cache (#6356): one that answered replaces its page 1 and keeps its later pages; one
 * that did not keeps its pages, with what failed. Then the list draws the kept answer, so a refresh keeps the pages read. */
async function keepAnswered(query: WorkQuery, read: readonly HostList[], hosts: readonly string[],
  before: readonly HostList[]): Promise<HostList[]> {
  const cache = workCache();
  if (!cache) return [...read];
  await Promise.all(read.map((h) => {
    const items = h.items.filter((i) => !h.stale?.includes(i.node.ref));
    return items.length > 0 || h.errors.length === 0
      ? cache.keepPage(query, h.host, 0, { items, ...h.cursor ? { cursor: h.cursor } : {}, ...h.total === undefined ? {} : { total: h.total },
        ...h.forges ? { forges: h.forges } : {}, ...h.errors.length > 0 ? { errors: h.errors } : {}, ...h.readAt === undefined ? {} : { readAt: h.readAt } })
      : cache.keepFailure(query, h.host, h.errors);
  }));
  const answer = await cache.answer(query, hosts);
  if (!answer) return [...read];
  // A forge that did not answer keeps, dimmed, the rows read before it (#4392), as without the cache.
  return settleLists(shownAnswer(answer, hosts).map((h) => {
    const got = read.find((r) => r.host === h.host);
    return got ? carryStale({ ...h, errors: got.errors }, before.find((b) => b.host === h.host)) : h;
  }));
}

/** Read the list for what it is asked, unless it was read for that already. A list not read for it yet draws the answer
 * this device kept first (#4930, #6356), with no call, then asks the machines once; their answer replaces page 1. */
export async function loadList(store: Store, l: WorkListState): Promise<void> {
  const mine = askKey(store, l);
  if (l.read === mine && l.lists.peek() !== null) return;
  const fresh = l.read !== undefined && l.asked.peek() !== l.readAsked;
  l.read = mine;
  l.readAsked = l.asked.peek();
  const current = () => l.read === mine;
  const wanted = copyAsk(l);
  mark("work-list-read");
  const { kind } = l;
  const sort = nodesSort(l);
  const { state, board } = l.filter.peek();
  const hosts = listHosts(store, board);
  const query = cacheQuery(l);
  l.loading.value = true;
  l.problem.value = null;
  l.moreFailed.value = null;
  try {
    await drawKept(store, l);
    if (!current()) return;
    // The rows read before stay until the new ones come (#4392): a first read shows placeholders, a refresh the rows.
    const before = l.lists.peek() ?? [];
    // The lineage figures and the rows side by side: the figures do not hold up the rows (#6358). A control plane that does
    // not list nodes gives none.
    const lineage = readLineage(store, kind, sort).catch((e: unknown) => {
      if (current() && !(e instanceof CallFailed && e.code === -32601) && !fromForge(kind)) l.problem.value = describe(e);
      return { nodes: [] as ReportNode[], cursor: undefined, failed: true };
    });
    if (!fromForge(kind)) {
      const known = await lineage;
      if (!current()) return;
      l.lineage.value = known.nodes; l.lineageCursor.value = known.cursor; l.lists.value = [];
      l.shownFor = wanted; l.fromDevice.value = false; l.readAt.value = Date.now();
      if (!l.problem.peek()) keepWork("lineage", wanted, { lineage: known.nodes, lineageCursor: known.cursor } satisfies KeptLineage);
      return;
    }
    // The machines' lists in one call to the control plane (#5592), which waits for a slow machine at most 5 s; the first
    // machine that answers for a forge is enough (#5449). The rows read before stay until it answers. Each repo is asked
    // once (#6641): a machine that reads only what others read is left out, named in no page, unless one asked fails, when
    // the machines it covered are asked too.
    const ids = hosts.map((h) => h.id);
    const source = `${ids.join(",")}|${forgeFolders(store, ids)}`;
    const scopes = await keptScopes(kind, board, ids, source);
    if (!current()) return;
    const pick = oneEach(ids, scopes);
    let readAt = Date.now();
    const firstPages = async (asked: readonly string[]): Promise<HostList[]> => asked.length === 0 ? [] : store.link.call("forge/listAll",
      forgeListAllRequest(kind, forgeState(state), board, asked.length === ids.length ? undefined : asked.map((host) => ({ host, cursor: "" })), query.sort, fresh)).then(
      (all) => { readAt = listReadAt(all, readAt); return hostListsOf(all, asked, before); },
      (e: unknown) => asked.map((host) => carryStale({ host, items: [], errors: [{ forge: "", message: describe(e) }] }, before.find((b) => b.host === host))));
    let answered = await firstPages(pick.ask);
    if (pick.covered.length > 0 && answered.some((h) => h.errors.length > 0)) answered = [...answered, ...await firstPages(pick.covered)];
    if (!current()) return;
    for (const h of answered) {
      if (h.errors.length > 0) scopes.delete(h.host);
      else if (h.scopes) {
        scopes.set(h.host, h.scopes);
        keepWork(scopesPage, scopesAsked(kind, board, h.host), { scopes: h.scopes, source } satisfies KeptScopes);
      }
    }
    // A machine left out loses its kept answer, so its rows do not draw as old ones and its next pages are not asked.
    const covered = new Set(oneEach(ids, scopes).covered);
    await Promise.all([...covered].map((h) => workCache()?.forgetAnswer(query, h)));
    const shown = ids.filter((h) => !covered.has(h));
    answered = shown.flatMap((h) => answered.filter((a) => a.host === h));
    const read = await keepAnswered(query, settleLists(answered), shown, before);
    if (!current()) return;
    l.lists.value = read;
    l.shownFor = wanted;
    l.fromDevice.value = false;
    // Rows carried over a failed read keep the time of the read they came from: the oldest on screen.
    const at = l.readAt.peek();
    if (!read.some((h) => h.stale?.length || h.errors.length) || at === undefined) l.readAt.value = readAt;
    const known = await lineage;
    if (!current()) return;
    l.lineage.value = known.nodes;
    l.lineageCursor.value = known.cursor;
    if (!("failed" in known)) keepWork("lineage", wanted, { lineage: known.nodes, lineageCursor: known.cursor } satisfies KeptLineage);
  } finally {
    if (current()) l.loading.value = false;
  }
}

/** The next page (Load more, or the end of the list in view) of each machine's list, or of lineage's. A machine's next page
 * is kept after its kept pages (#6356), so the next open draws it too (#6358, finding 2). */
export async function loadMore(store: Store, l: WorkListState): Promise<void> {
  // A page after the kept ones is read once the machines answered (#4930): the kept cursors may be old.
  if (l.more.peek() || l.fromDevice.peek()) return;
  l.more.value = true;
  l.moreFailed.value = null;
  try {
    if (fromForge(l.kind)) {
      const { state, board } = l.filter.peek();
      const query = cacheQuery(l);
      const had = l.lists.peek() ?? [];
      const pages = had.flatMap((h) => h.cursor ? [{ host: h.host, cursor: h.cursor }] : []);
      // Each machine's next page in one call to the control plane (#5592). A forge or a machine that could not read its next
      // page says so: the rows and the cursor stay, so Try again reads it again.
      const all = await store.link.call("forge/listAll", forgeListAllRequest(l.kind, forgeState(state), board, pages, query.sort));
      const got = new Map(all.lists.map((m) => [m.machine as string, m.list]));
      const failed: string[] = all.unreached.length > 0 ? [`${all.unreached.map((u) => u.machineName).join(", ")}: ${notAnswered}`] : [];
      l.lists.value = had.map((h) => {
        const next = h.cursor ? got.get(h.host) : undefined;
        if (!next) return h;
        if (next.items.length === 0 && (next.errors ?? []).length > 0) {
          failed.push(...next.errors!.map((e) => e.message));
          return h;
        }
        if (h.pages !== undefined) void workCache()?.keepPage(query, h.host, h.pages, next);
        return { ...h, items: [...h.items, ...next.items], cursor: next.cursor, errors: next.errors ?? [], forges: joinForges(h.forges, next.forges),
          pages: h.pages === undefined ? undefined : h.pages + 1 };
      });
      if (failed.length > 0) l.moreFailed.value = failed.join("; ");
    } else if (l.lineageCursor.peek()) {
      const next = await readLineage(store, l.kind, nodesSort(l), l.lineageCursor.peek());
      l.lineage.value = [...l.lineage.peek(), ...next.nodes];
      l.lineageCursor.value = next.cursor;
    }
  } catch (e) {
    l.moreFailed.value = describe(e);
  } finally {
    l.more.value = false;
  }
}

/** What a row's right-click menu offers (epic #6086): its priority and on hold where its forge sets them and its machine
 * writes; nothing on rows from this device's copy, until the machines answer. */
export function rowSets(store: Store, l: WorkListState, row: WorkRow): { priority: boolean; hold: boolean } {
  const host = row.host;
  if (l.kind !== "issue" || l.fromDevice.value || !host) return { priority: false, hold: false };
  const forges = l.lists.value?.find((h) => h.host === host)?.forges;
  return rowSetsPriority(row, forges, store.has(host, Capability.forgeWrite1));
}

/** A row's priority (forge/priority) or On hold (forge/hold) from its menu: the row shows it at once, and has its old value
 * back when the write fails, which `failed` says; after it, the forges' own read (their labels too). */
export async function setRowPlan(store: Store, l: WorkListState, row: WorkRow,
  to: { priority: ForgeItem["priority"] | undefined } | { onHold: boolean }, failed: (why: string) => void): Promise<void> {
  const host = row.host;
  if (!host) return;
  // The row and Work's cache at once (#6354, #6356), so every list and the item's page show it; the forge's next copy
  // replaces it once the write ends.
  const change = (edit: (i: ForgeItem) => ForgeItem) => {
    l.lists.value = (l.lists.peek() ?? []).map((h) => h.host !== host ? h
      : { ...h, items: h.items.map((i) => i.node.ref === row.node.ref ? edit(i) : i) });
    void workCache()?.edit(row.node, edit);
  };
  const withPriority = (i: ForgeItem, p: ForgeItem["priority"] | undefined): ForgeItem => {
    const { priority: _, ...rest } = i;
    return p ? { ...rest, priority: p } : rest;
  };
  const withHold = (i: ForgeItem, on: boolean): ForgeItem => {
    const { onHold: _, ...rest } = i;
    return on ? { ...rest, onHold: true } : rest;
  };
  try {
    if ("priority" in to) {
      if (to.priority === row.priority) return;
      change((i) => withPriority(i, to.priority));
      await store.link.call("forge/priority", to.priority ? { node: row.node, priority: to.priority } : { node: row.node }, host);
    } else {
      if (to.onHold === (row.onHold === true)) return;
      change((i) => withHold(i, to.onHold));
      await store.link.call("forge/hold", { node: row.node, on: to.onHold }, host);
    }
    l.asked.value++;
  } catch (e) {
    if ("priority" in to) change((i) => withPriority(i, row.priority));
    else change((i) => withHold(i, row.onHold === true));
    failed(describe(e));
  } finally {
    workCache()?.settle(row.node);
  }
}

/** Previous and Next for an item's page opened from its list; undefined for one opened another way. */
export function pageNeighbours(kind: WorkKind, ref: string): Neighbours | undefined {
  if (fromList.value !== placeKey(kind, ref)) return undefined;
  return neighbours(ref, shownRows(workList(kind)));
}

/** The ref after `ref`, reading the next page first (as More does) when `ref` is the last row read. */
export async function nextAfter(store: Store, kind: WorkKind, ref: string): Promise<string | undefined> {
  const l = workList(kind);
  let next = neighbours(ref, shownRows(l))?.next;
  while (!next && hasMore(l) && !l.more.peek()) {
    const before = shownRows(l).length;
    await loadMore(store, l);
    next = neighbours(ref, shownRows(l))?.next;
    if (shownRows(l).length === before || l.moreFailed.peek()) break;
  }
  return next;
}
