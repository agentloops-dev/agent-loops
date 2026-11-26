// The control plane web page by section (#2141): one bar at the top of every page, in place of
// Workspace | Console | Reports (#1804). Overview is the home; Agents is the sidebar and the chat;
// every other section is a list of pages beside the page open. A section or page only an admin can
// use is left out for a member; the control plane still checks each call's role itself.
import type { ActivityPage } from "../route";
import { featuresSection, reportSections, storesID, type Section } from "./console";
import { flagOn } from "./featureFlags";
import { workKinds, workKindTitle, type WorkKind } from "./work";

export type NavSection = "overview" | "agents" | "work" | "machines" | "workflows" | "reports" | "requests" | "people" | "status" | "settings";

export const navSections: readonly NavSection[] = ["overview", "agents", "work", "machines", "workflows", "reports", "requests", "people", "status", "settings"];

export const navTitle: Record<NavSection, string> = {
  overview: "Overview", agents: "Agents", work: "Work", machines: "Machines", workflows: "Workflows", reports: "Reports",
  requests: "Requests", people: "People", status: "Status", settings: "Settings",
};

/** A section's title now: Work with its flag off (#6572) is the pages it still has, Docs (with its flag) and Messages. */
export function sectionTitle(section: NavSection): string {
  if (section !== "work" || flagOn("work")) return navTitle[section];
  return flagOn("docs") ? "Docs and Messages" : "Messages";
}

/** The sections only an admin sees in the bar. */
const adminSections: ReadonlySet<NavSection> = new Set(["requests", "people"]);

/** How a page is drawn: an Activity page, a page of the old Console or of Reports, or one of its own. */
export type PageDraw =
  | { kind: "activity"; page: ActivityPage }
  | { kind: "console"; id: string }
  | { kind: "report"; section: Section }
  | { kind: "builds" }
  | { kind: "delivery" }
  | { kind: "work"; work: WorkKind }
  | { kind: "timeline" }
  | { kind: "deploys" }
  | { kind: "next" }
  | { kind: "docs" }
  | { kind: "messages" }
  | { kind: "machine"; host: string }
  | { kind: "secrets" }
  | { kind: "environment" }
  | { kind: "accounts" }
  | { kind: "machines" | "workflows" | "settings" };

export interface NavPage {
  id: string;
  title: string;
  /** Under which heading of the section's list it goes. */
  group: string;
  /** Only an admin sees it in the list; a member who opens it by its address is told why. */
  admin?: boolean;
  /** A page under another of the list (#5178): its row is indented under that page's, and its address is the
   * other page's item, #/reports/delivery/builds. */
  under?: string;
  /** A tab of another page of the list (#6884): that page's row and title are its own. */
  tabOf?: string;
  /** Only with this feature flag on (#6572): a page whose flag is off is in no list and opens by no address. */
  flag?: "work" | "docs" | "store-sizes";
  draw: PageDraw;
}

/** Whether a page's feature flag, if it has one, is on. */
export function flagged(p: NavPage): boolean {
  return p.flag === undefined || flagOn(p.flag);
}

/** Whether an address names a page whose feature flag is off (#6572): it opens the Overview, the page's default. */
export function hiddenPage(nav: NavSection, id: string | undefined): boolean {
  if (nav !== "work" || id === undefined) return false;
  const page = navPages.work.find((p) => p.id === id);
  // An id Work does not have opens Work's own default, so it is Work's.
  return page ? !flagged(page) : !flagOn("work");
}

const adminPage = (id: string, title: string, group: string, consoleID = id): NavPage =>
  ({ id, title, group, admin: true, draw: { kind: "console", id: consoleID } });

/** Each section's pages, in the order of its list. Overview has none; Agents' are rows at the sidebar's foot. */
export const navPages: Record<NavSection, readonly NavPage[]> = {
  overview: [],
  agents: [
    adminPage("live", "Live Now", "Agents"),
    { id: "projects", title: "Projects", group: "Agents", draw: { kind: "console", id: "projects" } },
  ],
  // Every issue and pull request from the forges, with lineage on top, and the releases lineage knows
  // (#3119). The page's id is the lineage kind, so #/work/pr/<ref> is a PR's page.
  // Deploys (#5270) after Releases: every component in every environment, from deploys/targets.
  // Timeline (#4206) after Deploys: the epics in time, from work/timeline.
  // Docs and Messages first (#5267), as the window's Work group has them; were sections of their own.
  work: [
    // The folders' docs and the docs connectors' (#3321, spec 3319); #/work/docs/<ref> is a doc's page.
    { id: "docs", title: "Docs", group: "Work", flag: "docs", draw: { kind: "docs" } },
    // The messaging connectors' conversations (#4344), with a reply (#4345); #/work/messages/<key> is a conversation's.
    { id: "messages", title: "Messages", group: "Work", draw: { kind: "messages" } },
    ...workKinds.flatMap((k): NavPage[] => [
      { id: k, title: workKindTitle[k], group: "Work", flag: "work", draw: { kind: "work", work: k } },
      // Next (#5691) after Issues: the ready work in one order, with the reason for each place, from forge/next.
      ...k === "issue" ? [{ id: "next", title: "Next", group: "Work", flag: "work", draw: { kind: "next" } } satisfies NavPage] : [],
      ...k === "release" ? [
        { id: "deploys", title: "Deploys", group: "Work", flag: "work", draw: { kind: "deploys" } } satisfies NavPage,
        { id: "timeline", title: "Timeline", group: "Work", flag: "work", draw: { kind: "timeline" } } satisfies NavPage,
      ] : [],
    ]),
  ],
  machines: [
    { id: "machines", title: "Machines", group: "Machines", draw: { kind: "machines" } },
  ],
  workflows: [
    { id: "workflows", title: "Workflows", group: "Workflows", draw: { kind: "workflows" } },
    adminPage("team", "Team Workflows", "Workflows", "workflows"),
  ],
  reports: [
    ...reportSections.flatMap((s): NavPage[] => [
      { id: s.id, title: s.title, group: s.group, ...s.tabOf ? { tabOf: s.tabOf } : {}, ...s.id === storesID ? { flag: "store-sizes" as const } : {}, draw: { kind: "report", section: s } },
      // Build trends (#5178, spec 5166) and the DORA figures of each environment (#5245, spec 5239 board 5) under Delivery
      // (#5280; were under Planning): #/reports/delivery/builds, #/reports/delivery/environments.
      ...s.id === "delivery" ? [
        { id: "builds", title: "Builds", group: s.group, under: "delivery", draw: { kind: "builds" } },
        { id: "environments", title: "Environments", group: s.group, under: "delivery", draw: { kind: "delivery" } },
      ] satisfies NavPage[] : [],
    ]),
    // Multiplier and Usage went into the Me lens of Outcomes, Spend and Time (#5280): an old address opens there (movedReport).
    adminPage("audit", "Audit", "Records"),
  ],
  requests: [adminPage("requests", "Requests", "Requests")],
  people: [adminPage("people", "People and Groups", "People"), adminPage("signIn", "Sign-In", "People")],
  // Status (#3998), status only and no settings, as the window's sidebar group: each runtime once, with the accounts, and a
  // line per machine (#3631), and the record of what happened (was Reports ▸ Records ▸ Events).
  status: [
    { id: "runtimes", title: "Runtimes", group: "Status", draw: { kind: "activity", page: "runtimes" } },
    { id: "events", title: "Events", group: "Status", draw: { kind: "activity", page: "events" } },
    // Every secret (#5257) and every outside account (#5258) of the person's, to find, see, add, edit and remove, at
    // #/secrets and #/accounts.
    { id: "secrets", title: "Secrets", group: "Status", draw: { kind: "secrets" } },
    { id: "accounts", title: "Accounts", group: "Status", draw: { kind: "accounts" } },
    // The plain variables sessions get when their agent starts (#5260): ~/.agents/env and each repo's .agents/env, at #/environment.
    { id: "environment", title: "Environment", group: "Status", draw: { kind: "environment" } },
    // Which features the apps and agents used on the person's machines (#2165): about the product and the machines, not
    // about work, so it left Reports (#5280).
    { id: "features", title: featuresSection.title, group: "Status", draw: { kind: "report", section: featuresSection } },
  ],
  // You first (#3998), as the window's Settings rail: the person's own connectors, their secrets and cards, which go to every
  // machine of theirs (#3632), and their outside accounts (#3967), kept and sent as their secrets are. Was Your setup.
  // Settings itself is still the page the section opens on (settingsHome).
  settings: [
    { id: "mcp", title: "Connectors", group: "You", draw: { kind: "activity", page: "mcp" } },
    // Opens Status ▸ Secrets (#5257): #/settings/secrets is the Secrets page's address too, for people who look in Settings.
    { id: "secrets", title: "Secrets", group: "You", draw: { kind: "secrets" } },
    // Opens Status ▸ Accounts (#5258), as Secrets does.
    { id: "accounts", title: "Accounts", group: "You", draw: { kind: "accounts" } },
    // Opens Status ▸ Environment (#5260), as Secrets opens its page: the window's Settings ▸ You has Open Environment.
    { id: "environment", title: "Environment", group: "You", draw: { kind: "environment" } },
    { id: "settings", title: "Settings", group: "Settings", draw: { kind: "settings" } },
    adminPage("policy", "Policy", "Organisation"),
    adminPage("modelAccess", "Model Access", "Organisation"),
    adminPage("reportPolicy", "Report Policy", "Organisation"),
    adminPage("catalogue", "Catalogue", "Organisation"),
    adminPage("readApi", "Read API", "Organisation"),
  ],
};

/** A page per machine (#3626), after Machines ▸ Machines: its CPU, memory, disks, worktrees (#3627), runtimes and resources (#3629). The Mac first, then the
 * others by name. */
export function machinePages(hosts: readonly { id: string; name: string }[], macID: string): NavPage[] {
  const sorted = [...hosts].sort((a, b) => Number(b.id === macID) - Number(a.id === macID) || a.name.localeCompare(b.name));
  return sorted.map((h) => ({ id: `machine-${h.id}`, title: h.name, group: "Machines", draw: { kind: "machine", host: h.id } }));
}

/** A section's pages with the ones made from data put in: the machines' after Machines ▸ Machines. */
export function withPages(section: NavSection, more: readonly NavPage[]): readonly NavPage[] {
  if (section !== "machines" || more.length === 0) return navPages[section];
  const list = navPages[section];
  return [...list.slice(0, 1), ...more, ...list.slice(1)];
}

/** The sections in the bar: every one for an admin; for a member (or while the role is not known), not Requests or People. */
export function sectionsShown(admin: boolean | null, current: NavSection): NavSection[] {
  return navSections.filter((s) => admin === true || !adminSections.has(s) || s === current);
}

/** The pages in a section's list: every one for an admin; a member's, and the one open. */
export function pagesShown(section: NavSection, admin: boolean | null, open?: string, more: readonly NavPage[] = []): NavPage[] {
  return withPages(section, more).filter((p) => flagged(p) && (admin === true || !p.admin || p.id === open));
}

/** The id of the page a route names: a page under another (#5178) is that page's item. */
export function routePageID(section: NavSection, r: { section?: string | undefined; item?: string | undefined }): string | undefined {
  return r.item && navPages[section].some((p) => p.under === r.section && p.id === r.item) ? r.item : r.section;
}

/** Where a page's row goes: its own address, or its parent's item. */
export function pageRoute(section: Exclude<NavSection, "overview">, p: NavPage): { nav: Exclude<NavSection, "overview">; section: string; item?: string } {
  return p.under ? { nav: section, section: p.under, item: p.id } : { nav: section, section: p.id };
}

/** The page a route opens: the one named, or the section's first that this person may use. */
export function openPage(section: NavSection, id: string | undefined, admin: boolean | null,
                         more: readonly NavPage[] = []): NavPage | undefined {
  const list = withPages(section, more).filter(flagged);
  return list.find((p) => p.id === id) ?? list.find((p) => p.id === sectionHome[section])
    ?? list.find((p) => admin === true || !p.admin) ?? list[0];
}

/** The page a section opens on when it is not its first: Settings, below the You group (#3998); Work's Issues, after Docs and
 * Messages (#5267). */
const sectionHome: Partial<Record<NavSection, string>> = { settings: "settings", work: "issue" };

/** Whether a page is for admins only and this person is not one (or not known yet). */
export function gated(page: NavPage, admin: boolean | null): boolean {
  return page.admin === true && admin !== true;
}

/** The page groups of a section's list, in order. */
export function groupedPages(list: readonly NavPage[]): [string, NavPage[]][] {
  const out = new Map<string, NavPage[]>();
  for (const p of list) out.set(p.group, [...(out.get(p.group) ?? []), p]);
  return [...out];
}

/** Usage's panes: Activity's Spend, Tokens and Time, then Velocity, the PRs that landed (#4598). Since #5280 each is in the
 * Me lens of the report it repeats (usageReport). */
export type UsagePane = "spending" | "tokens" | "time" | "velocity" | "cycletime";
export const usagePanes: readonly UsagePane[] = ["spending", "tokens", "time", "velocity", "cycletime"];
export const usagePaneTitle: Record<UsagePane, string> = { spending: "Spend", tokens: "Tokens", time: "Time", velocity: "Velocity", cycletime: "Cycle time" };

/** The report whose Me lens draws each of Usage's panes (#5280): Spend, Tokens and Time on the Cost report's tabs (#6884), Velocity and Cycle time on Outcomes. */
export const usageReport: Record<UsagePane, string> = { spending: "spend", tokens: "tokens", time: "time", velocity: "outcomes", cycletime: "outcomes" };

/** The Cost report's tabs (#6884), each a page of Reports with its own address: Spend, Tokens and Time. */
export const costTabs: readonly (readonly [id: string, title: string])[] = [["spend", "Spend"], ["tokens", "Tokens"], ["time", "Time"]];

/** The panes of Usage that a report's Me lens draws (#5280), in Usage's order. */
export function usagePanesOf(report: string): UsagePane[] {
  return usagePanes.filter((p) => usageReport[p] === report);
}

/** The query of a Reports page in the Me lens, named, so the lens last chosen does not take its place (#5279). */
export const meLens = "lens=me";

/** Where a Reports page that moved in #5280 (or before) is now: Planning's parts, Usage, Multiplier and Features. undefined
 * for a page that did not move. */
export function movedReport(page: string, item: string | undefined): { nav: "reports" | "status"; section: string; item?: string; query?: string } | undefined {
  switch (page) {
    // Planning (#4528) is Delivery, Forecast and Estimates; its pages Builds and Delivery are under Delivery.
    case "value":
      if (item === "builds") return { nav: "reports", section: "delivery", item };
      if (item === "delivery") return { nav: "reports", section: "delivery", item: "environments" };
      return { nav: "reports", section: "delivery" };
    // Usage and Multiplier were the person's own: they open in the Me lens, whatever lens was chosen last.
    case "usage": return { nav: "reports", section: "spend", query: meLens };
    // The Multiplier page took the Achievements page's place (#3990).
    case "multiplier": case "achievements": return { nav: "reports", section: "outcomes", query: meLens };
    case "features": return { nav: "status", section: "features" };
    // Events left Reports ▸ Records for Status (#3998).
    case "events": return { nav: "status", section: "events" };
    default: return undefined;
  }
}

/** Where an address from before #2141 goes now: `#/console/<page>` and `#/a/<page>`. */
export function movedPage(app: "console" | "a", page: string | undefined): { nav?: NavSection; section?: string; usage?: UsagePane; query?: string } {
  if (app === "console") {
    switch (page) {
      case undefined: case "overview": return {};
      case "live": case "projects": return { nav: "agents", section: page };
      case "machines": return { nav: "machines", section: "machines" };
      case "workflows": return { nav: "workflows", section: "team" };
      case "audit": return { nav: "reports", section: "audit" };
      case "requests": return { nav: "requests" };
      case "people": case "signIn": return { nav: "people", section: page };
      case "policy": case "modelAccess": case "reportPolicy": case "catalogue": case "readApi": return { nav: "settings", section: page };
      default: return {};
    }
  }
  switch (page) {
    // Runtimes went to Status and Connectors to Settings ▸ You (#3998).
    case "runtimes": return { nav: "status", section: page };
    case "mcp": return { nav: "settings", section: page };
    // Worktrees (#3627) and resources (#3629) are on each machine's page now.
    case "worktrees": case "resources": return { nav: "machines", section: "machines" };
    case "events": return { nav: "status", section: page };
    case "achievements": return { nav: "reports", section: "outcomes", query: meLens };
    case "spending": case "tokens": case "time": case "velocity": return { nav: "reports", section: usageReport[page], usage: page, query: meLens };
    default: return {};
  }
}

/** The g-then-letter key for each section. */
export const sectionKeys: Record<string, NavSection> = {
  o: "overview", a: "agents", i: "work", m: "machines", w: "workflows", r: "reports", q: "requests", p: "people", t: "status", s: "settings",
};
