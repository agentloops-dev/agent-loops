// Work ▸ Next (#5691, epic #5687): what goes next, and why. The rows of forge/next (#5689) for the chosen project's
// folder: the place, the item (its number, title, priority chip and labels) and the reasons for its place. A filter for
// one epic, and the epics in order. On hold rows are at the end, under their own heading, with no place. A Web + Mac
// link page (#4937): the Mac's Issues list has Next ↗; the Remote has none. The state lives for the tab, so Back from an
// issue's page finds the page as it was left.
import { signal } from "@preact/signals";
import { useEffect } from "preact/hooks";
import type { ForgeNext, ForgeNextRequest, ForgeNextRow } from "../protocol/generated";
import { fromWireDate, toWireDate } from "../protocol/dates";
import type { Store } from "../model/store";
import { describe } from "../model/errors";
import { forgeHosts } from "../model/workLists";
import { keepWork, keptWorkRead } from "../model/workCache";
import { choiceFrom, rememberEach, remembered } from "../model/workRemembered";
import {
  allEpics, emptyLine, emptyTitle, epicChoices, epicNode, itemNumber, nextFolders, nextLevelTitle, nextLevels, nextProblem,
  nextQuery, noMachine, noProject, noProjectTitle, onHoldLine, onHoldTitle, parseNextQuery, placeHelp, reasonsWords, splitNext,
  type NextChoice, type NextLevel,
} from "../model/workNext";
import { go, replace, route, routeHash } from "../route";
import { ForgeTitle } from "../render/ForgeTitle";
import { workProjects } from "./Work";
import { EmptyState, ErrorStrip, FilterMenu, LabelChip, PlaceholderRows, PriorityChip, RefreshButton, StateIcon, WorkFooter } from "./WorkParts";
import { pageCount, pageFooterState } from "../model/workFooter";

/** The middle of Next when nothing was read (#5971). */
const couldNotReadNext = "Could not read the order";

// The project, the epic and the level are what the page had when it was last left (#5451).
const kept = remembered("next");
const choice = signal<NextChoice>({
  project: choiceFrom(kept, "project", ""), epic: choiceFrom(kept, "epic", ""), level: choiceFrom<NextLevel>(kept, "level", "issue", nextLevels),
});
rememberEach("next", () => nextQuery(choice.value));
const next = signal<ForgeNext | null>(null);
/** The open epics in order: the Epic filter's choices. */
const epics = signal<ForgeNext | null>(null);
const problem = signal<string | null>(null);
const reading = signal(false);
const asked = signal(0);
/** When what is on screen was read (#5139): the machine's readAt, or this device's copy's time. */
const readAt = signal<number | undefined>(undefined);
/** What is on screen came from this device's copy (#4930), until the machine answers. */
const fromDevice = signal(false);
let read: string | undefined;

/** forge/next on the first of the project's folders that answers: they read the same repos, so one is enough. */
async function ask(store: Store, folders: readonly { host: string; folder: string }[], params: Omit<ForgeNextRequest, "folder">):
  Promise<{ got?: ForgeNext; failed: string[] }> {
  const failed: string[] = [];
  for (const f of folders) {
    const name = store.hosts.value.find((h) => h.id === f.host)?.name ?? f.host;
    const say = (words: string) => folders.length > 1 ? `${name}: ${words}` : words;
    try {
      const got: ForgeNext = await store.link.call("forge/next", { folder: f.folder, ...params }, f.host);
      // A forge that could not be read and no rows is a failed read, not an empty list: the rows on screen stay.
      if (got.rows.length === 0 && got.errors?.length) failed.push(...got.errors.map((e) => say(e.message)));
      else return { got, failed };
    } catch (e) {
      failed.push(say(nextProblem(describe(e))));
    }
  }
  return { failed };
}

async function load(store: Store) {
  const projects = workProjects(store);
  const hosts = forgeHosts(store).map((h) => h.id);
  const c = choice.value;
  const project = projects.find((p) => p.id === c.project) ?? projects[0];
  const key = `${asked.value} ${project?.id} ${c.epic} ${c.level} ${hosts.join(",")}`;
  if (read === key) return;
  read = key;
  if (!project) { next.value = { rows: [], readAt: toWireDate(new Date()) }; return; }
  if (c.project !== project.id) choice.value = { ...c, project: project.id };
  const folders = nextFolders(project, hosts);
  const copyKey = [project.id, c.level, c.epic].join("|");
  reading.value = true;
  try {
    if (next.value === null) {
      const copy = await keptWorkRead<ForgeNext>("next", copyKey);
      if (copy && read === key && next.value === null) { next.value = copy.value; readAt.value = copy.at; fromDevice.value = true; }
    }
    const node = c.level === "issue" ? epicNode(c.epic) : undefined;
    const [rows, epicRows] = await Promise.all([
      ask(store, folders, c.level === "epic" ? { level: "epic" } : node ? { epic: node } : {}),
      c.level === "epic" ? Promise.resolve(undefined) : ask(store, folders, { level: "epic" }),
    ]);
    if (read !== key) return;
    problem.value = rows.got ? null : rows.failed.join("\n") || null;
    if (epicRows?.got) epics.value = epicRows.got;
    if (c.level === "epic" && rows.got) epics.value = rows.got;
    // Every folder failed: what is on screen stays, with its time in the footer.
    if (!rows.got) return;
    next.value = rows.got;
    readAt.value = fromWireDate(rows.got.readAt).getTime();
    fromDevice.value = false;
    if (!rows.got.errors?.length) keepWork("next", copyKey, rows.got);
  } finally {
    if (read === key) reading.value = false;
  }
}

export function WorkNext({ store }: { store: Store }) {
  const projects = workProjects(store);
  const hosts = forgeHosts(store);
  const hostKey = hosts.map((h) => h.id).join(",");
  const projectKey = projects.map((p) => p.id).join(",");
  // The address says the choice (#4395): a link from the Mac (?folder=<path>) or a reload keeps it.
  const query = route.value.query;
  useEffect(() => {
    if (query === undefined) return;
    const said = parseNextQuery(query, projects);
    if (nextQuery(said) !== nextQuery(choice.peek())) { choice.value = said; next.value = null; }
  }, [query, projectKey]);
  const words = nextQuery(choice.value);
  useEffect(() => {
    if ((route.peek().query ?? "") !== words) replace({ nav: "work", section: "next", ...words ? { query: words } : {} });
  }, [words]);
  useEffect(() => { void load(store); }, [asked.value, words, hostKey, projectKey]);
  // Read again every minute while the page is open, as Deploys does: forge/next's forge reads are a minute's cache.
  useEffect(() => {
    const t = setInterval(() => { asked.value++; }, 60_000);
    return () => clearInterval(t);
  }, []);
  const refresh = () => { asked.value++; };
  const set = (changed: Partial<NextChoice>) => {
    const c = { ...choice.value, ...changed };
    if (c.level === "epic") c.epic = "";
    choice.value = c;
    next.value = null;
  };
  const c = choice.value;
  const { ready, onHold } = splitNext(next.value);
  const errors = [...problem.value ? problem.value.split("\n") : [], ...(next.value?.errors ?? []).map((e) => e.message)];
  const n = next.value;
  // The read's state, the count and Last update are in the footer, as on every Work page (#5971); no strip.
  const footer = hosts.length === 0 || projects.length === 0 ? { kind: "none" as const }
    : pageFooterState(n !== null, reading.value, fromDevice.value, errors);
  return (
    <div class="work-list next-page work-footed">
      <div class="work-filters next-bar" role="group" aria-label="Next">
        <div class="work-menus">
          {projects.length > 1 && (
            <FilterMenu label="Project" value={c.project} choices={projects.map((p) => ({ value: p.id, words: p.name }))}
              onPick={(id) => set({ project: id, epic: "" })} />
          )}
          {c.level === "issue" && (epicChoices(epics.value).length > 0 || c.epic) && (
            <FilterMenu label="Epic" value={c.epic} any={allEpics} choices={epicChoices(epics.value)} onPick={(ref) => set({ epic: ref })} />
          )}
        </div>
        <span class="next-trailing">
          <div class="diff-choice" role="group" aria-label="Show">
            {nextLevels.map((l) => (
              <button key={l} type="button" aria-pressed={c.level === l} onClick={() => set({ level: l })}>{nextLevelTitle[l]}</button>
            ))}
          </div>
        </span>
        <RefreshButton busy={reading.value} onClick={refresh} />
      </div>
      {hosts.length === 0 ? <EmptyState kind="issue" title={noMachine} />
        : projects.length === 0 ? <EmptyState kind="issue" title={noProjectTitle} line={noProject} />
        : (
          <>
            {next.value === null ? (errors.length > 0 && !reading.value ? <ErrorStrip lines={errors} retry={refresh} middle title={couldNotReadNext} /> : <PlaceholderRows />)
              : ready.length === 0 && onHold.length === 0 ? reading.value ? <PlaceholderRows /> : <EmptyState kind="issue" title={emptyTitle(c)} line={emptyLine(c)} />
              : (
                <>
                  {ready.length > 0 && <ul class="work-rows next-rows" aria-label={c.level === "epic" ? "Epics in order" : "Ready, in order"}>
                    {ready.map((r) => <NextRowView key={r.item.node.ref} row={r} />)}
                  </ul>}
                  {onHold.length > 0 && (
                    <section class="next-on-hold" aria-label={onHoldTitle}>
                      <h3 class="section-head">{onHoldTitle} <span class="quiet small">{onHold.length}</span></h3>
                      <p class="quiet small">{onHoldLine}</p>
                      <ul class="work-rows next-rows">
                        {onHold.map((r) => <NextRowView key={r.item.node.ref} row={r} />)}
                      </ul>
                    </section>
                  )}
                </>
              )}
          </>
        )}
      <WorkFooter state={footer} retry={refresh} at={n !== null ? readAt.value : undefined}
        count={n !== null ? pageCount(ready.length + onHold.length, c.level === "epic" ? "epic" : "issue") : undefined} />
    </div>
  );
}

/** A row: its place (none when on hold), the state icon, the title, the priority chip and at most three labels; under it
 * the number and the reasons for its place. A link to the item's page, as a Work row is. */
function NextRowView({ row }: { row: ForgeNextRow }) {
  const item = row.item;
  const to = { nav: "work" as const, section: item.node.kind, item: item.node.ref };
  const labels = item.labels.filter((l) => !/^priority:/i.test(l.name));
  return (
    <li data-ref={item.node.ref} class={row.onHold ? "on-hold" : undefined}>
      <a class="work-row work-item-row next-row" href={routeHash(to)}
        onClick={(e) => { if (e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return; e.preventDefault(); go(to); }}>
        <span class="next-place" title={row.onHold ? onHoldLine : placeHelp} aria-label={row.onHold ? onHoldTitle : `Place ${row.place}`}>
          {row.onHold ? "–" : row.place}
        </span>
        <span class="work-row-icon"><StateIcon kind={item.node.kind} state={item.state} stateReason={item.stateReason} /></span>
        <span class="work-row-main">
          <span class="work-row-head">
            <span class="work-title"><ForgeTitle text={item.title} /></span>
            <PriorityChip priority={item.priority} />
            {labels.length > 0 && (
              <span class="work-chips">
                {labels.slice(0, 3).map((l) => <LabelChip key={l.name} label={l} />)}
                {labels.length > 3 && <span class="work-chips-more quiet small">+{labels.length - 3}</span>}
              </span>
            )}
          </span>
          <span class="work-row-line quiet small">
            <span class="next-number">{itemNumber(item.node.ref)}</span> <span class="next-reasons">{reasonsWords(row)}</span>
          </span>
        </span>
      </a>
    </li>
  );
}
