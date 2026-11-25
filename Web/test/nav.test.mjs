// The control plane web page by section (#2141): the bar, each section's pages, who sees what, and
// every address from before it at its new place.
import { test } from "node:test";
import assert from "node:assert/strict";
import { load } from "./load.mjs";

const n = await load("src/model/nav.ts");

test("a member's bar has eight sections, an admin's ten (Docs and Messages are in Work, #5267)", () => {
  assert.deepEqual(n.sectionsShown(false, "overview").map((s) => n.navTitle[s]),
    ["Overview", "Agents", "Work", "Machines", "Workflows", "Reports", "Status", "Settings"]);
  assert.deepEqual(n.sectionsShown(null, "agents"), n.sectionsShown(false, "agents"), "not known yet is a member's");
  assert.deepEqual(n.sectionsShown(true, "overview").map((s) => n.navTitle[s]),
    ["Overview", "Agents", "Work", "Machines", "Workflows", "Reports", "Requests", "People", "Status", "Settings"]);
  assert.ok(n.sectionsShown(false, "people").includes("people"), "a member who opens People by its address sees where they are");
});

test("a member sees no admin page in a list, and opens the first they may use", () => {
  assert.deepEqual(n.pagesShown("settings", false).map((p) => p.id), ["mcp", "secrets", "accounts", "environment", "settings"]);
  assert.deepEqual(n.pagesShown("workflows", false).map((p) => p.id), ["workflows"]);
  assert.deepEqual(n.pagesShown("agents", false).map((p) => p.id), ["projects"]);
  assert.ok(n.pagesShown("reports", false).every((p) => p.id !== "audit"));
  assert.deepEqual(n.pagesShown("settings", false, "policy").map((p) => p.id), ["mcp", "secrets", "accounts", "environment", "settings", "policy"], "and the one open");
  assert.equal(n.openPage("people", undefined, false).id, "people");
  assert.equal(n.gated(n.openPage("people", undefined, false), false), true);
  assert.equal(n.gated(n.openPage("people", undefined, true), true), false);
  assert.equal(n.openPage("machines", "nowhere", true).id, "machines");
  assert.equal(n.openPage("reports", undefined, false).id, "spend");
});

test("every page has a title, and its group headings come in order", () => {
  for (const s of n.navSections) for (const p of n.navPages[s]) assert.ok(p.title, `${s}/${p.id}`);
  assert.deepEqual(n.groupedPages(n.navPages.reports).map(([g]) => g), ["Cost", "Output", "Plan", "Use", "Storage", "Saved", "Records"],
    "grouped by the question each page answers (#5280)");
  assert.deepEqual(n.groupedPages(n.navPages.machines).map(([g]) => g), ["Machines"]);
  assert.deepEqual(n.navPages.status.map((p) => p.id), ["runtimes", "events", "secrets", "accounts", "environment", "features"],
    "Status is status only (#3998), with Secrets and Accounts after Events (#4161), then Environment (#5260), then Features (#5280)");
  assert.ok(n.navPages.reports.every((p) => p.id !== "events"), "Events left Reports ▸ Records (#3998)");
  assert.deepEqual(n.navPages.reports.filter((p) => p.group === "Output").map((p) => p.id),
    ["outcomes", "delivery", "builds", "environments"],
    "Delivery after Outcomes and its Multiplier and Velocity; the daily summary's page is gone (#5853)");
  assert.ok(Object.values(n.navPages).every((pages) => pages.every((p) => p.id !== "summary")), "on no list (#5853)");
  assert.deepEqual(n.groupedPages(n.navPages.settings).map(([g]) => g), ["You", "Settings", "Organisation"], "You first (#3998)");
  assert.deepEqual(n.navPages.settings.filter((p) => p.group === "You").map((p) => p.id), ["mcp", "secrets", "accounts", "environment"],
    "Connectors, Secrets (#3632) and Accounts (#3967) are the person's own settings (#3998)");
  assert.equal(n.openPage("settings", undefined, false).id, "settings", "Settings still opens on Settings");
  assert.ok(n.navPages.machines.every((p) => p.id !== "runtimes" && p.id !== "mcp"));
});

test("each section is its own address, and #/ is the Overview", async () => {
  globalThis.location = { hash: "" };
  globalThis.addEventListener = () => {};
  try {
    const r = await load("src/route.ts");
    assert.deepEqual(r.parseRoute("#/"), {});
    assert.equal(r.sectionOf(r.parseRoute("#/")), "overview");
    assert.deepEqual(r.parseRoute("#/agents"), { nav: "agents" });
    assert.deepEqual(r.parseRoute("#/agents/live"), { nav: "agents", section: "live" });
    assert.deepEqual(r.parseRoute("#/status/runtimes"), { nav: "status", section: "runtimes" });
    assert.deepEqual(r.parseRoute("#/machines/runtimes"), { nav: "status", section: "runtimes" }, "an old link opens its new place (#3631, #3998)");
    assert.deepEqual(r.parseRoute("#/machines/mcp"), { nav: "settings", section: "mcp" }, "an old link opens its new place (#3632, #3998)");
    // Your setup went (#3998): Runtimes to Status, the rest to Settings ▸ You.
    assert.deepEqual(r.parseRoute("#/setup/runtimes"), { nav: "status", section: "runtimes" });
    assert.deepEqual(r.parseRoute("#/setup"), { nav: "status", section: "runtimes" });
    assert.deepEqual(r.parseRoute("#/setup/mcp"), { nav: "settings", section: "mcp" });
    assert.deepEqual(r.parseRoute("#/setup/secrets"), { nav: "status", section: "secrets" }, "the Secrets page (#5257)");
    assert.deepEqual(r.parseRoute("#/setup/accounts"), { nav: "status", section: "accounts" }, "the Accounts page (#5258)");
    assert.deepEqual(r.parseRoute("#/reports/events"), { nav: "status", section: "events" }, "Events left Reports ▸ Records (#3998)");
    assert.deepEqual(r.parseRoute("#/reports/spend"), { nav: "reports", section: "spend" });
    assert.equal(r.routeHash({ nav: "settings", section: "policy" }, undefined), "#/settings/policy");
    assert.equal(r.routeHash({}, undefined), "#/");
    assert.deepEqual(r.parseRoute(r.routeHash({ nav: "reports", section: "a b" }, undefined)), { nav: "reports", section: "a b" });
    // A list's filters and sort after a "?" (#4395); an item's page has none.
    assert.deepEqual(r.parseRoute("#/work/issue?label=bug&sort=newest"), { nav: "work", section: "issue", query: "label=bug&sort=newest" });
    assert.equal(r.routeHash({ nav: "work", section: "issue", query: "label=bug" }, undefined), "#/work/issue?label=bug");
    assert.equal(r.routeHash({ nav: "work", section: "issue", item: "github:a/b#1", query: "label=bug" }, undefined), "#/work/issue/github%3Aa%2Fb%231");
    // An issue's or a PR's page is its Work list's page with the ref after it (#3119).
    const pr = { nav: "work", section: "pr", item: "github:alexec/Agents#99" };
    assert.equal(r.routeHash(pr, undefined), "#/work/pr/github%3Aalexec%2FAgents%2399");
    assert.deepEqual(r.parseRoute("#/work/pr/github%3Aalexec%2FAgents%2399"), pr);
    assert.deepEqual(r.parseRoute("#/work/issue"), { nav: "work", section: "issue" });
    // A session is in Agents, at the address it always had: every notice and link still opens it.
    const chat = { host: "mac", folder: "file:///w/a", session: "S1" };
    assert.equal(r.routeHash(chat, undefined), "#/h/mac/p/file%3A%2F%2F%2Fw%2Fa/s/S1");
    assert.equal(r.sectionOf(r.parseRoute(r.routeHash(chat, undefined))), "agents");
  } finally {
    delete globalThis.location;
    delete globalThis.addEventListener;
  }
});

test("an address from before #2141 opens its page's new place", async () => {
  globalThis.location = { hash: "" };
  globalThis.addEventListener = () => {};
  try {
    const r = await load("src/route.ts");
    const moved = {
      "#/console": {}, "#/console/overview": {},
      "#/console/live": { nav: "agents", section: "live" }, "#/console/projects": { nav: "agents", section: "projects" },
      "#/console/machines": { nav: "machines", section: "machines" }, "#/console/workflows": { nav: "workflows", section: "team" },
      "#/console/audit": { nav: "reports", section: "audit" }, "#/console/requests": { nav: "requests" },
      "#/console/people": { nav: "people", section: "people" }, "#/console/signIn": { nav: "people", section: "signIn" },
      "#/console/policy": { nav: "settings", section: "policy" }, "#/console/readApi": { nav: "settings", section: "readApi" },
      "#/a/runtimes": { nav: "status", section: "runtimes" }, "#/a/mcp": { nav: "settings", section: "mcp" },
      "#/a/events": { nav: "status", section: "events" }, "#/a/achievements": { nav: "reports", section: "outcomes", query: "lens=me" },
      "#/a/velocity": { nav: "reports", section: "outcomes", query: "lens=me" },
      "#/a/tokens": { nav: "reports", section: "tokens", query: "lens=me" },
    };
    for (const [old, now] of Object.entries(moved)) {
      assert.deepEqual(r.parseRoute(old), now, old);
      assert.equal(r.isOldAddress(old), true, old);
    }
    assert.equal(r.usageAsked.value, "tokens", "Spend's Me lens opens on the pane the old address named");
    r.parseRoute("#/a/velocity");
    assert.equal(r.usageAsked.value, "velocity", "#/a/velocity opens Outcomes' Me lens on Velocity (#4598, #5280)");
    assert.equal(r.isOldAddress("#/reports/spend"), false);
    assert.equal(r.isOldAddress("#/machines/runtimes"), true, "Runtimes went to Status (#3631, #3998)");
    assert.equal(r.isOldAddress("#/machines/mcp"), true, "Connectors went to Settings ▸ You (#3632, #3998)");
    assert.equal(r.isOldAddress("#/setup/secrets"), true, "Your setup went (#3998)");
    assert.equal(r.isOldAddress("#/reports/events"), true, "Events went to Status (#3998)");
    assert.equal(r.isOldAddress("#/settings/secrets"), true, "Secrets is a page of its own (#5257)");
    assert.equal(r.isOldAddress("#/secrets"), false);
    assert.equal(r.isOldAddress("#/machines/machines"), false);
    assert.equal(r.isOldAddress("#/h/mac/p/x"), false);
    // Every page an old address names is a page of its section.
    for (const now of Object.values(moved)) {
      if (now.nav && now.section) assert.ok(n.navPages[now.nav].some((p) => p.id === now.section), `${now.nav}/${now.section}`);
    }
  } finally {
    delete globalThis.location;
    delete globalThis.addEventListener;
  }
});

test("Agents' state filter keeps the sessions in that state", async () => {
  const f = await load("src/model/agentFilters.ts");
  const agent = (state, extra = {}) => ({ id: state, state, createdAt: 0, labels: [], ...extra });
  assert.equal(f.keptByState(agent("running"), "any"), true);
  assert.equal(f.keptByState(agent("running"), "working"), true);
  assert.equal(f.keptByState(agent("running"), "needsYou"), false);
  assert.equal(f.keptByState(agent("finished"), "done"), true);
  assert.equal(f.keptByState(agent("finished"), "working"), false);
});

test("worktrees are on each machine's page, and the old address goes to Machines (#3627)", () => {
  assert.ok(n.navPages.machines.every((p) => p.id !== "worktrees"));
  assert.deepEqual(n.movedPage("a", "worktrees"), { nav: "machines", section: "machines" });
});

test("resources are on each machine's page, and the old address goes to Machines (#3629)", () => {
  assert.ok(n.navPages.machines.every((p) => p.id !== "resources"));
  assert.deepEqual(n.movedPage("a", "resources"), { nav: "machines", section: "machines" });
});

test("the Secrets page has its own address, and one secret's (#5257)", async () => {
  const r = await load("src/route.ts");
  assert.deepEqual(r.parseRoute("#/secrets"), { nav: "status", section: "secrets" });
  assert.deepEqual(r.parseRoute("#/secrets/GITHUB_TOKEN"), { nav: "status", section: "secrets", item: "GITHUB_TOKEN" });
  assert.deepEqual(r.parseRoute("#/settings/secrets"), { nav: "status", section: "secrets" });
  assert.equal(r.routeHash({ nav: "status", section: "secrets" }, undefined), "#/secrets");
  assert.equal(r.routeHash({ nav: "status", section: "secrets", item: "GITHUB_TOKEN" }, undefined), "#/secrets/GITHUB_TOKEN");
  const id = "session/11111111-2222-3333-4444-555555555555/SHOP_PW";
  assert.deepEqual(r.parseRoute(r.routeHash({ nav: "status", section: "secrets", item: id }, undefined)),
    { nav: "status", section: "secrets", item: id });
});

test("the Environment page has its own address, and one variable's (#5260)", async () => {
  const r = await load("src/route.ts");
  assert.deepEqual(r.parseRoute("#/environment"), { nav: "status", section: "environment" });
  assert.deepEqual(r.parseRoute("#/environment/yours/AWS_REGION"), { nav: "status", section: "environment", item: "yours/AWS_REGION" });
  assert.deepEqual(r.parseRoute("#/settings/environment"), { nav: "status", section: "environment" });
  assert.equal(r.isOldAddress("#/settings/environment"), true);
  assert.equal(r.isOldAddress("#/environment"), false);
  assert.equal(r.routeHash({ nav: "status", section: "environment" }, undefined), "#/environment");
  const id = "repo//Users/me/My Repo/AWS_REGION";
  assert.deepEqual(r.parseRoute(r.routeHash({ nav: "status", section: "environment", item: id }, undefined)),
    { nav: "status", section: "environment", item: id });
});

test("the Accounts page has its own address, and one account's (#5258)", async () => {
  const r = await load("src/route.ts");
  assert.deepEqual(r.parseRoute("#/accounts"), { nav: "status", section: "accounts" });
  assert.deepEqual(r.parseRoute("#/settings/accounts"), { nav: "status", section: "accounts" });
  assert.deepEqual(r.parseRoute("#/status/accounts"), { nav: "status", section: "accounts" });
  assert.equal(r.isOldAddress("#/settings/accounts"), true);
  assert.equal(r.isOldAddress("#/accounts"), false);
  assert.equal(r.routeHash({ nav: "status", section: "accounts" }, undefined), "#/accounts");
  const id = "github#alex@example.com";
  assert.equal(r.routeHash({ nav: "status", section: "accounts", item: id }, undefined), "#/accounts/github%23alex%40example.com");
  assert.deepEqual(r.parseRoute(r.routeHash({ nav: "status", section: "accounts", item: id }, undefined)),
    { nav: "status", section: "accounts", item: id });
});

test("a page whose feature flag is off is in no list and opens by no address, and is back with no reload (#6572)", async () => {
  const f = await load("test/nav-entry.ts");
  const titles = () => f.pagesShown("work", true).map((p) => p.title);
  f.preferencesSaid({});
  assert.deepEqual(titles(), ["Messages"]);
  assert.equal(f.sectionTitle("work"), "Messages");
  assert.equal(f.openPage("work", "issue", true).id, "messages");
  assert.equal(f.sectionOf({ nav: "work", section: "pr", item: "github:a/b#1" }), "overview");
  assert.equal(f.sectionOf({ nav: "work", section: "docs" }), "overview");
  assert.equal(f.sectionOf({ nav: "work", section: "messages" }), "work");
  assert.equal(f.sectionOf({ nav: "work" }), "work");
  assert.deepEqual(f.pageCrumbs("work", "Messages"), [{ title: "Messages" }]);
  assert.deepEqual(f.searchFound("work", true, [], []).map((x) => x.title), ["Workflows", "Workflows", "Team Workflows"]);
  assert.equal(f.openPage("reports", "stores", true).id, "spend", "Store sizes is behind its flag (#6907)");
  f.preferencesSaid({ "flag:store-sizes": true });
  assert.equal(f.openPage("reports", "stores", false).id, "stores");
  f.preferencesSaid({ "flag:docs": true });
  assert.deepEqual(titles(), ["Docs", "Messages"]);
  assert.equal(f.sectionTitle("work"), "Docs and Messages");
  assert.equal(f.sectionOf({ nav: "work", section: "docs" }), "work");
  f.preferencesSaid({ "flag:work": true });
  assert.deepEqual(titles(), ["Messages", "Issues", "Next", "Pull requests", "Releases", "Deploys", "Timeline"]);
  assert.equal(f.sectionTitle("work"), "Work");
  assert.equal(f.sectionOf({ nav: "work", section: "pr" }), "work");
  assert.equal(f.openPage("work", undefined, true).id, "issue");
});
