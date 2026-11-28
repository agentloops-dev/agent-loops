// The bar of sections at the top of every page (#2141), in place of Workspace | Console | Reports
// (#1804), and every section but the Overview and Agents drawn as the Console was: its list of pages
// on the left, the page open on the right. Below 760 the bar is one menu with the open section's
// name. g then a letter goes to a section, when the focus is not in a field; ⌘K opens the command menu (#4788).
import { askKey, forgeFolders, forgeHosts, loadList, workList } from "../model/workLists";
import type { WorkKind } from "../model/work";
import { useEffect } from "preact/hooks";
import { useSignal } from "@preact/signals";
import { Fragment, type ComponentChildren } from "preact";
import type { Store } from "../model/store";
import { go, route, sectionOf } from "../route";
import { flagOn } from "../model/featureFlags";
import { gated, groupedPages, machinePages, navPages, openPage, pagesShown, sectionKeys, sectionsShown,
  type NavPage, type NavSection, pageRoute, routePageID, sectionTitle } from "../model/nav";
import { adminOf, adminsOnly } from "../model/console";
import { hostAnswers } from "../model/capabilities";
import { needingYou, placedSessions } from "../model/overview";
import { ActivityPageView } from "./Activity";
import { ConsoleSection, MachinesPage, roleProblemOf } from "./ConsoleApp";
import { requestCount } from "./ConsoleRecords";
import { policyNotSaved } from "./ConsolePolicy";
import { orgPolicyNotSaved } from "./ConsoleOrgPolicy";
import { ReportsPage, lensName } from "./Reports";
import { lensAsked, lensTitle } from "../model/reports";
import { BuildsPage } from "./Builds";
import { DeliveryPage } from "./Delivery";
import { WorkList, WorkPage } from "./Work";
import { WorkReleasePage } from "./WorkRelease";
import { WorkNext } from "./WorkNext";
import { WorkTimeline } from "./WorkTimeline";
import { WorkDeploysPage } from "./WorkDeploysPage";
import { DocPage, DocsList } from "./Docs";
import { ConversationPage, MessagesList } from "./Messages";
import { MachinePage } from "./MachinePage";
import { isBusy, runningLineOf } from "../model/machineSessions";
import { machineNeed, machineNeedLine } from "./OfflineStrip";
import { MachinesList } from "./MachinesList";
import { type GroupRows, machinesGroupLimit, machinesGroupRows, machinesGroupWords, sidebarPins } from "../model/machinesGroup";
import { MachineRow, MakingRow } from "./MachineRow";
import { ControlPlanePage, ControlPlaneRow, controlPlaneTitle, isControlPlaneOnly, runsControlPlane } from "./ControlPlane";
import { agentsRowLine, lookOn } from "../model/controlPlaneMachine";
import { deleteMakingQuestion, joinTrustQuestion, type MachineMaking, makingRows, newMachineItems } from "../model/machineRow";
import { openContextMenu, type MenuItem } from "./ContextMenu";
import { AddMachineDialog } from "./AddMachineDialog";
import { addMachineWords } from "../model/addMachine";
import { SettingsPage } from "./Settings";
import { SecretsPage } from "./SecretsPage";
import { EnvironmentPage } from "./EnvironmentPage";
import { AccountsPage } from "./AccountsPage";
import { StatusListMark, StatusListLastLine, useUserScopeStatus } from "./StatusSecrets";
import { WorkflowRow } from "./WorkflowRow";
import { BackToList } from "./BackToList";
import { PageTitle, PersonMenu, SearchBox } from "./TopBar";
import { pageCrumbs, type Crumb } from "../model/crumbs";
import { lowDetail, lowWords, runtimeTally, setupRuntimes } from "../model/runtimes";
import { eventsWaitingLine } from "../model/eventsWaiting";
import type { Session } from "../session";
import { ask } from "./MessageKinds";
import { CommandMenu } from "./CommandMenu";
import { Icon } from "./Icon";

/**
 * How many requests and sessions need the person, across every folder: Agents' count in the bar.
 * Requests, not sessions (#3864): each open request, and each session needing the person that asked none;
 * a terminal's agents-secret too (#4179). A request in Later counts nothing, and its asker neither (#4236).
 */
export function needsYouCount(store: Store): number {
  const folders = store.hosts.value.flatMap((h) => (store.folders.value[h.id] ?? [])
    .filter((f) => f.folder.archivedAt === undefined).map((f) => ({ host: h.id, folder: f.folder.folder })));
  const requests = folders.flatMap(({ host, folder }) => store.personRequestsIn(host, folder));
  const askers = new Set(folders.flatMap(({ host, folder }) => store.openPersonRequestsIn(host, folder)
    .map((r) => `${host}|${r.askedBy}`)));
  const sessions = needingYou(placedSessions(store.hosts.value, store.folders.value,
    (h, f) => store.folderView(h, f).headings.flatMap((x) => x.agents)))
    .filter((p) => !askers.has(`${p.host.id}|${p.agent.id}`));
  // A terminal's agents-secret (#4179) is in no folder.
  const terminal = store.hosts.value.reduce((n, h) => n + store.terminalRequests(h.id).length, 0);
  return requests.length + sessions.length + terminal;
}

function countOf(store: Store, section: NavSection, admin: boolean | null): number {
  if (section === "agents") return needsYouCount(store);
  if (section === "requests" && admin === true) return requestCount(store);
  return 0;
}

/** The sections, at the top of every page. */
export function NavBar({ store, session }: { store: Store; session: Session }) {
  const r = route.value;
  const current = sectionOf(r);
  const admin = adminOf(store);
  const shown = sectionsShown(admin, current);
  const name = store.plane.value?.name || "Control plane";
  const menuOpen = useSignal(false);
  // g then a letter: to a section, as a mail app's keys go to a mailbox.
  useEffect(() => {
    let armed = 0;
    const onKey = (e: KeyboardEvent) => {
      const target = e.target as HTMLElement | null;
      if (e.metaKey || e.ctrlKey || e.altKey || target?.closest("input, textarea, select, [contenteditable=true]")) return;
      if (e.key === "g") { armed = Date.now(); return; }
      const to = sectionKeys[e.key];
      if (armed && Date.now() - armed < 1500 && to && sectionsShown(adminOf(store), sectionOf(route.peek())).includes(to)) {
        e.preventDefault();
        go(to === "overview" ? {} : { nav: to });
      }
      armed = 0;
    };
    addEventListener("keydown", onKey);
    return () => removeEventListener("keydown", onKey);
  }, []);
  const to = (s: NavSection) => { menuOpen.value = false; go(s === "overview" ? {} : { nav: s }); };
  const item = (s: NavSection) => {
    const count = countOf(store, s, admin);
    return (
      <button key={s} type="button" class={`nav-item${s === current ? " on" : ""}`} aria-current={s === current ? "page" : undefined}
        aria-label={count ? `${sectionTitle(s)}, ${count} waiting` : undefined} onClick={() => to(s)}>
        {sectionTitle(s)}{count > 0 && <span class="sub-app-count" aria-hidden="true">{count}</span>}
      </button>
    );
  };
  return (
    <header class="top-bar">
      <nav class="nav-bar" aria-label="Sections">
        <button type="button" class="nav-home" onClick={() => to("overview")}>{name}</button>
        <div class="nav-items wide-only">{shown.map(item)}</div>
        <div class="nav-menu narrow-only">
          <button type="button" class="nav-menu-button" aria-expanded={menuOpen.value} onClick={() => { menuOpen.value = !menuOpen.value; }}>
            {sectionTitle(current)} <Icon name={menuOpen.value ? "chevron-up" : "chevron-down"} />
            {!menuOpen.value && shown.some((s) => countOf(store, s, admin) > 0) && <span class="sub-app-count" aria-hidden="true">•</span>}
          </button>
          {menuOpen.value && <div class="nav-menu-list">{shown.map(item)}</div>}
        </div>
      </nav>
      <SearchBox store={store} />
      <PersonMenu store={store} session={session} />
      <CommandMenu store={store} />
    </header>
  );
}

/** Status ▸ Runtimes' grey line (#3956), as the window's Runtimes row: each runtime read on the machine the page reads
 * it on. `detail` is the title's and a screen reader's, with the reset when it is on course to run out (#5000). */
function runtimesLowWords(store: Store): { words: string; detail: string } | undefined {
  const all = setupRuntimes(store.hosts.value, store.macHostID.value, store.runtimes.value);
  const home = new Map(all.map((r) => [r.status.runtime.id, r.home]));
  const tally = runtimeTally(all.map((r) => r.status), (id) => store.runtimeAllowances.value[home.get(id) ?? ""]);
  const words = lowWords(tally);
  return words ? { words, detail: lowDetail(tally) ?? words } : undefined;
}

const isRuntimesPage = (page: NavPage) => page.draw.kind === "activity" && page.draw.page === "runtimes";

/** A page's mark beside its row in the list: a count, or that it is not saved. */
function PageMark({ store, page }: { store: Store; page: NavPage }) {
  // Status ▸ Secrets and Accounts (#4161): the newest operation's age.
  if (page.draw.kind === "secrets" || page.draw.kind === "accounts") return <StatusListMark store={store} list={page.draw.kind} />;
  // Status ▸ Runtimes (#3956): an allowance below 10% left or on course to run out (#5000), in the warning tint, as the window's orange Runtimes icon.
  if (isRuntimesPage(page)) {
    const low = runtimesLowWords(store);
    if (low) return <span class="small attention" title={low.detail} aria-hidden="true">●</span>;
  }
  if (page.id === "requests" && requestCount(store) > 0) return <span class="sub-app-count">{requestCount(store)}</span>;
  if (page.id === "reportPolicy" && policyNotSaved(store)) return <span class="quiet small attention">Not saved</span>;
  if (page.id === "policy" && orgPolicyNotSaved(store)) return <span class="quiet small attention">Not saved</span>;
  // Connectors: a connector of the person's that does not answer, or a connection that ended (#3407), as the window's red icon.
  if (page.id === "mcp") {
    const words = store.connectorsWarning();
    if (words) return <span class="small failure" title={words} aria-label={words}>●</span>;
  }
  return null;
}

/** Under a machine's row (#3764), in grey: the most urgent thing it needs; Start is in the row's menu and swipe (#3974).
 * Nothing when it needs nothing. The window's MachineRow. A sign-in is what its runtimes last said. */
function MachineNeedLine({ store, host }: { store: Store; host: string }) {
  const row = store.hosts.value.find((h) => h.id === host);
  const need = row ? machineNeed(row, store.hostIsOnline(host), store.runtimes.value[host] ?? [],
    store.disk.value[host]?.alarms ?? [], store.capacity.value[host]?.level, store.capacity.value[host]?.reason) : null;
  if (!row) return null;
  // "Control plane near capacity", when it runs the control plane too and needs nothing more urgent (#6592).
  const control = runsControlPlane(store, host) ? agentsRowLine(lookOn(host, store.hosts.value)) : null;
  // Nothing wrong: how many sessions run on it, or Idle (#5490), from the count the machine's page reads.
  const words = need ? machineNeedLine(need, row) : control ? control
    : store.hostIsOnline(host) ? runningLineOf({ running: store.sessionsByMachine.value[host]?.working ?? 0 }) : null;
  if (!words) return null;
  return (
    <p class="machine-need quiet small" role="status">{words}</p>
  );
}

/** Under Status ▸ Runtimes (#3956), in grey: `Claude: 8% left`, or `Claude: runs out Fri at this pace` (#5000). Nothing when no allowance is low, or none work. */
function RuntimesLowLine({ store }: { store: Store }) {
  const low = runtimesLowWords(store);
  return low ? <p class="page-need quiet small" role="status" title={low.detail} aria-label={low.detail}>{low.words}</p> : null;
}

const isEventsPage = (page: NavPage) => page.draw.kind === "activity" && page.draw.page === "events";

/** Under Status ▸ Events, in grey: `4 sessions waiting`, as the window's Events row. Nothing when none waits;
 * no time, as a wait's age says nothing. */
function EventsWaitingLine({ store }: { store: Store }) {
  const words = eventsWaitingLine(store.events.value);
  return words ? <p class="page-need quiet small" role="status">{words}</p> : null;
}

/** The machines that run the control plane first (#6592), when the person sees them as such; a machine that runs only
 * the control plane is called Control plane. */
function controlFirst(store: Store, pages: NavPage[]): NavPage[] {
  const runs = (p: NavPage) => p.draw.kind === "machine" && runsControlPlane(store, p.draw.host);
  const named = (p: NavPage) => p.draw.kind === "machine" && isControlPlaneOnly(store, p.draw.host)
    ? { ...p, title: controlPlaneTitle(store, p.draw.host) } : p;
  return [...pages.filter(runs), ...pages.filter((p) => !runs(p))].map(named);
}

/** The Machines group's rows (#5286): the machine pages it draws, at most seven, This Mac, pinned and busy first. */
function machinesGroup(store: Store, pages: readonly NavPage[], making: number): { pages: NavPage[]; rows: GroupRows } {
  const pins = sidebarPins.value;
  const macID = store.macHostID.value;
  const ids = pages.flatMap((p) => p.draw.kind === "machine" ? [p.draw.host] : []);
  // With seven or fewer every machine is drawn in its order, so `active` changes nothing and is not worked out (#6158).
  const ranks = ids.length + making > machinesGroupLimit;
  // The control plane's machines stay in the group, as a pinned one does (#6592).
  const listed = ids.map((id) => ({ id, pinned: pins.includes(id) || runsControlPlane(store, id), active: ranks && machineIsActive(store, id) }));
  const rows = machinesGroupRows(listed, making, macID);
  const byID = new Map(pages.map((p) => [p.draw.kind === "machine" ? p.draw.host : p.id, p]));
  return { pages: rows.machines.flatMap((id) => byID.get(id) ?? []), rows };
}

/** Past seven, the Machines page's row goes after the machines as All Machines (N); else it stays first. */
function boundedPages(list: NavPage[], rows: GroupRows): NavPage[] {
  if (!rows.bounded) return list;
  const page = list.find((p) => p.draw.kind === "machines");
  if (!page) return list;
  return [...list.filter((p) => p !== page), { ...page, title: machinesGroupWords.all(rows.total) }];
}

/** It needs something, it is starting, or sessions work or wait there: the group shows it before the rest (#5286). */
function machineIsActive(store: Store, host: string): boolean {
  const row = store.hosts.value.find((h) => h.id === host);
  if (!row) return false;
  if (store.hostsBeingStarted.value.includes(host)) return true;
  if (machineNeed(row, store.hostIsOnline(host), store.runtimes.value[host] ?? [], store.disk.value[host]?.alarms ?? [],
    store.capacity.value[host]?.level)) return true;
  return isBusy(store.sessionsByMachine.value[host]);
}

/** A section's list of pages on the left, the page open on the right. */
export function SectionPage({ store, section, down }: { store: Store; section: Exclude<NavSection, "overview" | "agents">; down: boolean }) {
  const admin = adminOf(store);
  const r = route.value;
  // A page per machine under Machines (#3626).
  const more = section === "machines" ? controlFirst(store, machinePages(store.hosts.value, store.macHostID.value)) : [];
  const open = openPage(section, routePageID(section, r), admin, more);
  // At most seven machines, then All Machines (N), the Machines page, after them (#5286).
  const making = section === "machines" ? makingRows(store.machinesMaking.value, store.providers.list.value, store.hosts.value,
    store.providers.pools.value.map((p) => p.name)) : [];
  const bounded = section === "machines" ? machinesGroup(store, more, making.length) : null;
  const groupRows = bounded?.rows;
  const list = (bounded ? boundedPages(pagesShown(section, admin, open?.id, bounded.pages), bounded.rows)
    : pagesShown(section, admin, open?.id, more));
  const registering = useSignal(false);
  useUserScopeStatus(store, section === "status");
  // Work shown: the list it opens on read ahead (#4930), from this device's copy and then the machines, so it draws at once;
  // and again when a machine joins or a folder is added (#6152). A list read already for them is not read again.
  const forgeIds = forgeHosts(store).map((h) => h.id);
  const forgeOn = forgeIds.join(",");
  const forgeOnFolders = forgeFolders(store, forgeIds);
  const workOn = flagOn("work");
  useEffect(() => {
    if (section !== "work" || !workOn || !forgeOn) return;
    const kind: WorkKind = r.section === "pr" || r.section === "release" ? r.section : "issue";
    const l = workList(kind);
    if (!l.loading.peek() || l.read !== askKey(store, l)) void loadList(store, l);
  }, [section, workOn, forgeOn, forgeOnFolders]);
  // Status ▸ Events' grey line reads who waits on each machine that answers; events/changed keeps it.
  const online = store.hosts.value.filter((h) => hostAnswers(h.state)).map((h) => h.id);
  useEffect(() => {
    if (section === "status") void store.loadEventsAll(online);
  }, [section, online.join(",")]);
  // The Machines header (#3974): its menu has New Machine and registers one. New Machine is also in the sidebar's
  // + (#5447); either opens its sheet (#4634).
  const machinesHead = (group: string) => section === "machines" && group === "Machines";
  // With two or more providers, New Machine names the default, and New Machine on ‹name› opens the sheet on another (#3979).
  useEffect(() => {
    if (section === "machines" && store.providers.list.value === null) void store.providers.load();
  }, [section]);
  const headItems = (): MenuItem[] => [
    ...newMachineItems(store.providers.list.value ?? []).map((item) => ({ label: item.label, run: () => store.newMachine(item.provider) })),
    { label: addMachineWords.menuItem, run: () => { registering.value = true; } },
  ];
  return (
    <div class={`columns sub-app console${list.length > 1 ? "" : " one-page"}`} aria-busy={down}>
      {list.length > 1 && (
        <nav class="folders sub-app-list" aria-label={sectionTitle(section)}>
          {groupedPages(list).map(([group, pages]) => (
            <div key={group} class="sub-app-group">
              {machinesHead(group) ? (
                <h2 class="section-head" onContextMenu={(e) => openContextMenu(e, headItems())}>{group}</h2>
              ) : <h2 class="section-head">{section === "work" ? sectionTitle(section) : group}</h2>}
              {pages.filter((p) => !p.tabOf).map((p) => p.draw.kind === "machine" && isControlPlaneOnly(store, p.draw.host) ? (
                <ControlPlaneRow key={p.id} store={store} host={p.draw.host} on={p.id === open?.id} open={() => go({ nav: section, section: p.id })} />
              ) : p.draw.kind === "machine" ? (
                <MachineRow key={p.id} store={store} host={p.draw.host} on={p.id === open?.id}
                  open={() => go({ nav: section, section: p.id })}>
                  <MachineNeedLine store={store} host={p.draw.host} />
                </MachineRow>
              ) : (
                <Fragment key={p.id}>
                  <button type="button" class={`sub-app-row${p.id === (open?.tabOf ?? open?.id) ? " on" : ""}${p.under ? " under" : ""}`}
                    aria-current={p.id === (open?.tabOf ?? open?.id) ? "page" : undefined} onClick={() => go(pageRoute(section, p))}>
                    {p.title}
                    <PageMark store={store} page={p} />
                  </button>
                  {isRuntimesPage(p) && <RuntimesLowLine store={store} />}
                  {isEventsPage(p) && <EventsWaitingLine store={store} />}
                  {(p.draw.kind === "secrets" || p.draw.kind === "accounts") && <StatusListLastLine store={store} list={p.draw.kind} />}
                </Fragment>
              ))}
              {machinesHead(group) && making.slice(0, groupRows?.making ?? making.length)
                .map((row) => <MakingRow key={`making-${row.provider}/${row.workspace}/${row.name}`} row={row}
                  join={store.machineJoins.value[`${row.provider}/${row.workspace}`]}
                  onDelete={async () => { if (await ask(deleteMakingQuestion(row))) void store.deleteWorkspace(row.provider, row.workspace); }}
                  onJoin={() => void joinAsking(store, row)} />)}
            </div>
          ))}
        </nav>
      )}
      {open && <PageView store={store} page={open} down={down} />}
      {registering.value && <AddMachineDialog store={store} close={() => { registering.value = false; }} />}
    </div>
  );
}

/** A click on a machine that never joined (#4978): a host key new to the control plane is asked about first. */
async function joinAsking(store: Store, row: MachineMaking): Promise<void> {
  await store.joinMachine(row);
  const join = store.machineJoins.value[`${row.provider}/${row.workspace}`];
  if (!join || !("needsTrust" in join)) return;
  if (await ask(joinTrustQuestion(row, join.needsTrust))) await store.joinMachine(row, join.needsTrust);
  else store.cancelJoin(row);
}

/** One page of a section, in the chat's place: Agents' Live Now and Projects too. */
export function PageView({ store, page, down }: { store: Store; page: NavPage; down: boolean }) {
  const section = sectionOfPage(page);
  const admin = adminOf(store);
  if (!gated(page, admin) && page.draw.kind === "activity") return <ActivityPageView store={store} page={page.draw.page} />;
  if (!gated(page, admin) && page.draw.kind === "settings") return <SettingsPage store={store} down={down} />;
  // A page under another (#5178) names that page in its breadcrumb: Reports › Delivery › Builds.
  const parent = page.under ? navPages[section].find((p) => p.id === page.under) : undefined;
  return (
    <Framed title={page.draw.kind === "report" ? lensTitle(navPages[section].find((p) => p.id === page.tabOf)?.title ?? page.title, lensAsked(route.value.query), lensName.value) : page.title} section={section} parent={parent && { title: parent.title, to: { nav: section, section: parent.id } }}>
      {gated(page, admin) ? <p class="hint">{admin === null ? "Loading…" : roleProblemOf(store) ?? adminsOnly}</p>
        : page.draw.kind === "console" ? <ConsoleSection store={store} id={page.draw.id} down={down} />
        : page.draw.kind === "report" ? <ReportsPage store={store} section={page.draw.section} down={down} />
        : page.draw.kind === "builds" ? <BuildsPage store={store} down={down} />
        : page.draw.kind === "delivery" ? <DeliveryPage store={store} down={down} />
        : page.draw.kind === "machines" ? (admin === true ? <><MachinesList store={store} /><h2 class="section-head">Everyone’s machines</h2><MachinesPage store={store} down={down} /></> : <MachinesList store={store} />)
        : page.draw.kind === "workflows" ? <AllWorkflows store={store} down={down} />
        // An item of a list Work no longer has (#/work/milestone/<ref>, #3878) opens the Work default's list.
        : page.draw.kind === "work" ? (route.value.item && route.value.section === page.id
          ? page.draw.work === "release"
            ? <WorkReleasePage key={route.value.item} store={store} ref={route.value.item} />
            : <WorkPage key={route.value.item} store={store} kind={page.draw.work} ref={route.value.item} />
          : <WorkList key={page.draw.work} store={store} kind={page.draw.work} />)
        : page.draw.kind === "machine" ? (isControlPlaneOnly(store, page.draw.host)
          ? <ControlPlanePage key={page.draw.host} store={store} host={page.draw.host} />
          : <MachinePage key={page.draw.host} store={store} host={page.draw.host} />)
        : page.draw.kind === "timeline" ? <WorkTimeline store={store} />
        : page.draw.kind === "deploys" ? <WorkDeploysPage store={store} />
        : page.draw.kind === "next" ? <WorkNext store={store} />
        // Every secret of the person's (#5257): theirs (#1760), a machine's for one session (#3548) and what agents saved
        // on that machine (#5122), in one list beside the selected one's detail. Settings ▸ You keeps a line to it.
        : page.draw.kind === "secrets" ? <SecretsPage store={store} down={down} />
        // Every outside account of the person's (#5258), in one list beside the selected one's detail.
        : page.draw.kind === "accounts" ? <AccountsPage store={store} down={down} />
        // The plain variables sessions get when their agent starts (#5260): ~/.agents/env and each repo's .agents/env.
        : page.draw.kind === "environment" ? <EnvironmentPage store={store} down={down} />
        : page.draw.kind === "docs" ? (route.value.item
          ? <DocPage key={route.value.item} store={store} docRef={route.value.item} />
          : <DocsList store={store} />)
        : page.draw.kind === "messages" ? (route.value.item
          ? <ConversationPage key={route.value.item} store={store} conversationKey={route.value.item} />
          : <MessagesList store={store} />) : null}
    </Framed>
  );
}

/** The section a page is in, for its breadcrumb. */
function sectionOfPage(page: NavPage): Exclude<NavSection, "overview"> {
  if (page.draw.kind === "machine") return "machines";
  return (Object.keys(navPages) as NavSection[]).find((s) => navPages[s].includes(page)) as Exclude<NavSection, "overview"> ?? "agents";
}

function Framed({ title, section, parent, children }: { title: string; section: Exclude<NavSection, "overview">; parent?: Crumb | undefined;
  children: ComponentChildren }) {
  const crumbs = pageCrumbs(section, title);
  return (
    <section class="chat activity-page sub-app-page console" aria-label={title}>
      <header class="column-head page-head"><BackToList />
        <PageTitle parts={parent ? [...crumbs.slice(0, -1), parent, ...crumbs.slice(-1)] : crumbs}><h1>{title}</h1></PageTitle></header>
      <div class="scroll">
        <div class="activity-body settings-body">{children}</div>
      </div>
    </section>
  );
}

/** Every folder's workflows in one list, by folder: the same rows as each folder's Workflows fold. */
function AllWorkflows({ store, down }: { store: Store; down: boolean }) {
  const hosts = store.hosts.value;
  const folders = hosts.flatMap((host) => (store.folders.value[host.id] ?? [])
    .filter((f) => f.folder.archivedAt === undefined).map((folder) => ({ host, folder })));
  const several = hosts.length > 1;
  const key = folders.filter(({ host }) => hostAnswers(host.state)).map(({ host, folder }) => `${host.id}|${folder.folder.folder}`).join(",");
  // Held while the page is open, and let go when it closes, as a folder's fold holds its list (#291).
  useEffect(() => {
    const held = folders.filter(({ host }) => hostAnswers(host.state));
    for (const { host, folder } of held) store.holdWorkflows(host.id, folder.folder.folder);
    return () => { for (const { host, folder } of held) store.releaseWorkflows(host.id, folder.folder.folder); };
  }, [key]);
  const groups = folders.map(({ host, folder }) => ({ host, folder, list: store.folderWorkflows(host.id, folder.folder.folder) }))
    .filter((g) => g.list.length > 0);
  if (groups.length === 0) return <p class="hint">No workflows yet. A workflow is a file in a folder's .agents/workflows.</p>;
  return (
    <>
      {groups.map(({ host, folder, list }) => (
        <section key={`${host.id}|${folder.folder.folder}`} class="all-workflows">
          <h2 class="section-head">{several ? `${host.name}: ${folder.name}` : folder.name}</h2>
          {list.map((summary) => (
            <WorkflowRow key={summary.workflow.workflowID} store={store} host={host.id} summary={summary}
              disabled={down || !hostAnswers(host.state)} chosen={false}
              onPick={() => go({ host: host.id, folder: folder.folder.folder, workflow: summary.workflow.workflowID })} />
          ))}
        </section>
      ))}
    </>
  );
}

