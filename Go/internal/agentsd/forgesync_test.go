package agentsd

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/githubmcp"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// syncGitHub is a GitHub with a store of items, whose search keeps to the filter's state, kind
// and updated bounds, the newest first, and gives at most searchMost results of a query, as
// GitHub's does. Newest is its probe: a mark that changes with any change; PullChecks its batch.
type syncGitHub struct {
	*fakeForgeGitHub
	mu         sync.Mutex
	store      []githubmcp.Item
	searchMost int
	// queries are each search's query, probes each Newest call, and checks each PullChecks call.
	queries []string
	probes  int
	checks  [][]int
	// fail, when set, is what Search gives.
	fail error
}

func (g *syncGitHub) Search(_ context.Context, repos []string, sf githubmcp.SearchFilter, after string, first int) (githubmcp.SearchPage, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.queries = append(g.queries, githubmcp.SearchQuery(repos, sf))
	if g.fail != nil {
		return githubmcp.SearchPage{}, g.fail
	}
	var hits []githubmcp.Item
	for _, i := range g.store {
		if i.PR != sf.PRs || !slices.Contains(repos, i.Repo) ||
			sf.State == "open" && i.State != "open" || sf.State == "closed" && i.State == "open" ||
			sf.UpdatedSince != "" && i.UpdatedAt < sf.UpdatedSince || sf.UpdatedBefore != "" && i.UpdatedAt > sf.UpdatedBefore {
			continue
		}
		hits = append(hits, i)
	}
	slices.SortStableFunc(hits, func(a, b githubmcp.Item) int {
		return cmp.Or(strings.Compare(b.UpdatedAt, a.UpdatedAt), b.Number-a.Number)
	})
	total := len(hits)
	if g.searchMost > 0 && len(hits) > g.searchMost {
		hits = hits[:g.searchMost]
	}
	from, _ := strconv.Atoi(after)
	to := min(from+first, len(hits))
	p := githubmcp.SearchPage{Items: slices.Clone(hits[from:to]), Total: &total}
	for n := from; n < to; n++ {
		p.Cursors = append(p.Cursors, strconv.Itoa(n+1))
	}
	if to < len(hits) {
		p.Next = strconv.Itoa(to)
	}
	return p, nil
}

func (g *syncGitHub) Newest(_ context.Context, repo, since string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.probes++
	newest := ""
	for _, i := range g.store {
		if i.Repo == repo && i.UpdatedAt >= since {
			newest = max(newest, fmt.Sprintf("%s %d %s", i.UpdatedAt, i.Number, i.Title))
		}
	}
	return newest, nil
}

func (g *syncGitHub) PullChecks(_ context.Context, repo string, numbers []int) ([]githubmcp.Item, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.checks = append(g.checks, slices.Clone(numbers))
	var out []githubmcp.Item
	for _, i := range g.store {
		if i.PR && i.Repo == repo && slices.Contains(numbers, i.Number) {
			out = append(out, i)
		}
	}
	return out, nil
}

func (g *syncGitHub) put(i githubmcp.Item) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.store = slices.DeleteFunc(g.store, func(o githubmcp.Item) bool { return o.Repo == i.Repo && o.Number == i.Number })
	g.store = append(g.store, i)
}

func (g *syncGitHub) asked() ([]string, int, [][]int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.queries), g.probes, slices.Clone(g.checks)
}

// syncDaemon is forgeDaemon (folders on alexec/agents and alexec/web) with a syncGitHub.
func syncDaemon(t *testing.T) (*Daemon, *conn, *syncGitHub) {
	t.Helper()
	d, c, gh, _ := forgeDaemon(t)
	g := &syncGitHub{fakeForgeGitHub: gh}
	d.newForgeGitHub = func(string) forgeGitHub { return g }
	return d, c, g
}

// at is a time n minutes after 10:00 on 2026-11-19, as GitHub says it.
func at(n int) string {
	return time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC).Add(time.Duration(n) * time.Minute).Format(time.RFC3339)
}

func dateOf(s string) *wire.Date {
	t, _ := time.Parse(time.RFC3339, s)
	return new(wire.DateOf(t))
}

func readChanges(t *testing.T, c *conn, r wire.ForgeChangesRequest) []wire.ForgeChanges {
	t.Helper()
	var pages []wire.ForgeChanges
	for n := 0; ; n++ {
		var p wire.ForgeChanges
		if err := json.Unmarshal(c.call(t, 10+n, "forge/changes", r), &p); err != nil {
			t.Fatal(err)
		}
		pages = append(pages, p)
		if p.NextPageCursor == nil {
			return pages
		}
		if n > 100 {
			t.Fatal("the pages do not end")
		}
		r.PageCursor = p.NextPageCursor
	}
}

// #6349: forge/changes of one source reads what changed at or after updatedSince in every
// state, issues and pull requests, with the forge's own bound (updated:>=); highWater is the
// latest updatedAt; an item at the bound itself is in it (the overlap a client asks from); a
// source not on this machine is refused.
func TestForgeChangesReadsWhatChangedSince(t *testing.T) {
	t.Parallel()

	_, c, g := syncDaemon(t)
	for n, i := range []githubmcp.Item{
		{Number: 1, State: "open", UpdatedAt: at(0)},
		{Number: 2, State: "closed", UpdatedAt: at(5)},
		{Number: 3, State: "merged", PR: true, UpdatedAt: at(9)},
		{Number: 4, State: "open", PR: true, UpdatedAt: at(3)},
	} {
		i.Repo, i.Title = "alexec/agents", "item "+strconv.Itoa(n)
		g.put(i)
	}
	g.put(githubmcp.Item{Repo: "alexec/web", Number: 9, State: "open", UpdatedAt: at(8)})

	pages := readChanges(t, c, wire.ForgeChangesRequest{Source: new("github:alexec/agents"), Kinds: []string{"issue", "pr"}, UpdatedSince: dateOf(at(3))})
	if len(pages) != 1 {
		t.Fatalf("pages: %+v", pages)
	}
	p := pages[0]
	var refs []string
	for _, i := range p.Items {
		refs = append(refs, i.Node.Ref)
	}
	if want := []string{"github:issue:alexec/agents#2", "github:pr:alexec/agents#3", "github:pr:alexec/agents#4"}; !slices.Equal(refs, want) {
		t.Fatalf("items: %q, want %q", refs, want)
	}
	if p.HighWater != *dateOf(at(9)) || len(p.Errors) != 0 || len(p.Forges) != 1 {
		t.Fatalf("answer: %+v", p)
	}
	queries, _, _ := g.asked()
	if len(queries) != 2 || !strings.Contains(queries[0], "is:issue repo:alexec/agents updated:>="+at(3)) || strings.Contains(queries[0], "is:open") ||
		!strings.Contains(queries[1], "is:pr repo:alexec/agents updated:>="+at(3)) {
		t.Fatalf("queries: %q", queries)
	}

	if e := c.callError(t, 2, "forge/changes", wire.ForgeChangesRequest{Source: new("github:alexec/other"), Kinds: []string{"issue"}}); e == nil || e.Code != CodeInvalidParams {
		t.Fatalf("another source: %+v", e)
	}
	if e := c.callError(t, 3, "forge/changes", wire.ForgeChangesRequest{Kinds: []string{"epic"}}); e == nil || e.Code != CodeInvalidParams {
		t.Fatalf("a kind: %+v", e)
	}
}

// #6349: a first fill (no updatedSince) of more than GitHub's 1,000 results of a query goes on in
// pages of the limit and then in windows of updatedAt, so every item comes once, though several
// share a time at a window's edge; every page says highWater so far, the last the latest.
func TestForgeChangesPagesPastTheSearchLimit(t *testing.T) {
	t.Parallel()

	_, c, g := syncDaemon(t)
	g.searchMost = 1000
	const count = 2500
	for n := range count {
		// Four items a minute, so the items at a window's edge share their time.
		g.put(githubmcp.Item{Repo: "alexec/agents", Number: n + 1, State: "open", Title: "i", UpdatedAt: at(n / 4)})
	}
	pages := readChanges(t, c, wire.ForgeChangesRequest{Source: new("github:alexec/agents"), Kinds: []string{"issue"}, Limit: new(int64(100))})
	seen := map[string]bool{}
	for _, p := range pages {
		if len(p.Items) > 100 {
			t.Fatalf("a page of %d", len(p.Items))
		}
		for _, i := range p.Items {
			if seen[i.Node.Ref] {
				t.Fatalf("%s came twice", i.Node.Ref)
			}
			seen[i.Node.Ref] = true
		}
	}
	if len(seen) != count {
		t.Fatalf("%d of %d items", len(seen), count)
	}
	if last := pages[len(pages)-1]; last.HighWater != *dateOf(at((count - 1) / 4)) {
		t.Fatalf("highWater %v", last.HighWater)
	}
	queries, _, _ := g.asked()
	windows := 0
	for _, q := range queries {
		if strings.Contains(q, "updated:<=") && !slices.Contains(queries[:slices.Index(queries, q)], q) {
			windows++
		}
	}
	if windows < 2 {
		t.Fatalf("%d windows in %d queries", windows, len(queries))
	}
	// A cursor of another request is refused.
	other := wire.ForgeChangesRequest{Source: new("github:alexec/agents"), Kinds: []string{"pr"}, PageCursor: pages[0].NextPageCursor}
	if e := c.callError(t, 2, "forge/changes", other); e == nil || e.Code != CodeInvalidParams {
		t.Fatalf("another request's cursor: %+v", e)
	}
}

// #6349: a poll whose probe says nothing changed since the last read answers with that read's
// items and makes no search (on GitHub the probe is a REST read with its ETag, a 304 that costs
// no rate limit); a change makes the next poll search again. An item the forge has not changed
// but a client has in its overlap comes again, with the same updatedAt, for the client to keep
// once.
func TestForgeChangesProbeSkipsTheSearchWhileNothingChanged(t *testing.T) {
	t.Parallel()

	_, c, g := syncDaemon(t)
	g.put(githubmcp.Item{Repo: "alexec/agents", Number: 1, State: "open", Title: "one", UpdatedAt: at(10)})
	r := wire.ForgeChangesRequest{Source: new("github:alexec/agents"), Kinds: []string{"issue"}, UpdatedSince: dateOf(at(9))}
	first := readChanges(t, c, r)[0]
	again := readChanges(t, c, r)[0]
	queries, probes, _ := g.asked()
	if len(queries) != 1 || probes != 2 {
		t.Fatalf("%d searches and %d probes; want 1 search, 2 probes", len(queries), probes)
	}
	if len(again.Items) != 1 || again.Items[0].Node != first.Items[0].Node || again.Items[0].UpdatedAt != first.Items[0].UpdatedAt || again.HighWater != first.HighWater {
		t.Fatalf("the unchanged answer: %+v, first %+v", again, first)
	}

	g.put(githubmcp.Item{Repo: "alexec/agents", Number: 1, State: "closed", Title: "one", UpdatedAt: at(12)})
	changed := readChanges(t, c, r)[0]
	if queries, _, _ = g.asked(); len(queries) != 2 {
		t.Fatalf("a change did not search again: %q", queries)
	}
	if len(changed.Items) != 1 || changed.Items[0].State != "closed" || changed.HighWater != *dateOf(at(12)) {
		t.Fatalf("after the change: %+v", changed)
	}
}

// #6349: a source that cannot be read says so in errors, and highWater stays at updatedSince, so
// the next read asks again from there; forge/ids fails the read instead of giving a short list.
func TestForgeChangesFailedSourceKeepsTheCursor(t *testing.T) {
	t.Parallel()

	_, c, g := syncDaemon(t)
	g.fail = &githubmcp.GitHubError{Kind: githubmcp.RateLimited, Message: "GitHub's rate limit"}
	p := readChanges(t, c, wire.ForgeChangesRequest{Source: new("github:alexec/agents"), Kinds: []string{"issue"}, UpdatedSince: dateOf(at(3))})[0]
	if len(p.Errors) != 1 || p.Errors[0].Forge != "github" || p.HighWater != *dateOf(at(3)) {
		t.Fatalf("answer: %+v", p)
	}
	if e := c.callError(t, 2, "forge/ids", wire.ForgeIdsRequest{Source: "github:alexec/agents", Kinds: []string{"issue"}}); e == nil {
		t.Fatal("forge/ids answered with a failed forge")
	}
}

// #6349: forge/ids gives the nodes of every item of the source now in the states asked, 1,000 a
// page, each once, past GitHub's 1,000 results of a query; every page says when the read began.
func TestForgeIdsPagesEveryItem(t *testing.T) {
	t.Parallel()

	_, c, g := syncDaemon(t)
	g.searchMost = 1000
	for n := range 1500 {
		g.put(githubmcp.Item{Repo: "alexec/agents", Number: n + 1, State: map[bool]string{false: "open", true: "closed"}[n%3 == 0], UpdatedAt: at(n / 2)})
	}
	g.put(githubmcp.Item{Repo: "alexec/web", Number: 1, State: "open", UpdatedAt: at(1)})
	r := wire.ForgeIdsRequest{Source: "github:alexec/agents", Kinds: []string{"issue"}, States: []string{"open"}}
	seen := map[string]bool{}
	var began wire.Date
	for n := 0; ; n++ {
		var p wire.ForgeIds
		_ = json.Unmarshal(c.call(t, 10+n, "forge/ids", r), &p)
		if len(p.Nodes) > 1000 || p.At == 0 || began != 0 && p.At != began {
			t.Fatalf("page %d: %d nodes, at %v (began %v)", n, len(p.Nodes), p.At, began)
		}
		began = p.At
		for _, node := range p.Nodes {
			if seen[node.Ref] {
				t.Fatalf("%s came twice", node.Ref)
			}
			seen[node.Ref] = true
		}
		if p.NextPageCursor == nil {
			break
		}
		r.PageCursor = p.NextPageCursor
	}
	if len(seen) != 1000 || seen["github:issue:alexec/agents#1"] || !seen["github:issue:alexec/agents#2"] {
		t.Fatalf("%d nodes", len(seen))
	}
	for _, bad := range []wire.ForgeIdsRequest{{Kinds: []string{"issue"}}, {Source: "github:alexec/agents", Kinds: []string{"issue"}, States: []string{"merged"}}} {
		if e := c.callError(t, 2, "forge/ids", bad); e == nil || e.Code != CodeInvalidParams {
			t.Fatalf("%+v: %+v", bad, e)
		}
	}
}

// #6349: forge/checks reads the checks of many pull requests of one source in one call.
func TestForgeChecksReadsManyPullsInOneCall(t *testing.T) {
	t.Parallel()

	_, c, g := syncDaemon(t)
	g.put(githubmcp.Item{Repo: "alexec/agents", Number: 5, PR: true, State: "open", Checks: "failing", Review: "approved", Mergeable: "mergeable", UpdatedAt: at(1)})
	g.put(githubmcp.Item{Repo: "alexec/agents", Number: 6, PR: true, State: "open", Checks: "running", UpdatedAt: at(2)})
	var p wire.ForgeChecks
	_ = json.Unmarshal(c.call(t, 1, "forge/checks", wire.ForgeChecksRequest{Source: "github:alexec/agents", Numbers: []int64{5, 6, 7, 5}}), &p)
	if _, _, checks := g.asked(); len(checks) != 1 || !slices.Equal(checks[0], []int{5, 6, 7}) {
		t.Fatalf("calls: %v", checks)
	}
	if len(p.Pulls) != 2 || p.At == 0 || deref(p.Pulls[0].Checks) != "failing" || deref(p.Pulls[0].Review) != "approved" ||
		p.Pulls[0].Node.Ref != "github:pr:alexec/agents#5" || deref(p.Pulls[1].Checks) != "running" {
		t.Fatalf("answer: %+v", p)
	}
	many := make([]int64, 101)
	for n := range many {
		many[n] = int64(n + 1)
	}
	for _, bad := range []wire.ForgeChecksRequest{{Source: "github:alexec/agents", Numbers: many}, {Source: "github:alexec/agents"}, {Source: "jira:PROJ", Numbers: []int64{1}}} {
		if e := c.callError(t, 2, "forge/checks", bad); e == nil || e.Code != CodeInvalidParams {
			t.Fatalf("%d numbers of %s: %+v", len(bad.Numbers), bad.Source, e)
		}
	}
}

// #6349: forge/sources says each source of the machine's folders once, with its kinds and a
// folder on it, and makes no forge call.
func TestForgeSourcesSaysEachRepoOnce(t *testing.T) {
	t.Parallel()

	d, c, g := syncDaemon(t)
	var s wire.ForgeSources
	_ = json.Unmarshal(c.call(t, 1, "forge/sources", wire.Empty{}), &s)
	var names []string
	for _, src := range s.Sources {
		names = append(names, src.Source)
		if src.Forge != "github" || !slices.Equal(src.Kinds, []string{"issue", "pr"}) || !slices.Contains(d.folderPaths(), deref(src.Folder)) {
			t.Fatalf("source: %+v", src)
		}
	}
	if !slices.Equal(names, []string{"github:alexec/agents", "github:alexec/web"}) {
		t.Fatalf("sources: %q", names)
	}
	if queries, probes, _ := g.asked(); len(queries)+probes != 0 {
		t.Fatalf("forge calls: %q, %d probes", queries, probes)
	}
}

// #6349: forge/issues with updatedSince lists only what changed since then, an older daemon's
// request (none) lists every item.
func TestForgeIssuesUpdatedSince(t *testing.T) {
	t.Parallel()

	_, c, g := syncDaemon(t)
	g.put(githubmcp.Item{Repo: "alexec/agents", Number: 1, State: "open", UpdatedAt: at(1)})
	g.put(githubmcp.Item{Repo: "alexec/agents", Number: 2, State: "open", UpdatedAt: at(5)})
	var list wire.ForgeList
	_ = json.Unmarshal(c.call(t, 1, "forge/issues", wire.ForgeListRequest{UpdatedSince: dateOf(at(4))}), &list)
	if len(list.Items) != 1 || list.Items[0].Node.Ref != "github:issue:alexec/agents#2" {
		t.Fatalf("since: %+v", list.Items)
	}
	_ = json.Unmarshal(c.call(t, 2, "forge/issues", wire.ForgeListRequest{}), &list)
	if len(list.Items) != 2 {
		t.Fatalf("every item: %+v", list.Items)
	}
}

// sinceBounded keeps a forge's page to the bounds where its API does not: an item older than
// UpdatedSince ends the list, and one after UpdatedBefore is left out with its cursor.
func TestSinceBounded(t *testing.T) {
	t.Parallel()

	item := func(n int, minute int) wire.ForgeItem {
		return wire.ForgeItem{Node: wire.LineageNode{Kind: "issue", Ref: fmt.Sprintf("x#%d", n)}, UpdatedAt: *dateOf(at(minute))}
	}
	p := forgePage{items: []wire.ForgeItem{item(1, 9), item(2, 7), item(3, 5), item(4, 2)}, cursors: []string{"a", "b", "c", "d"}, next: "d", total: new(9)}
	f := forgeFilter{UpdatedSince: dateOf(at(5)).Time(), UpdatedBefore: dateOf(at(8)).Time()}
	out, ended := sinceBounded(p, f)
	if !ended || out.next != "" || out.total != nil || len(out.items) != 2 || out.items[0].Node.Ref != "x#2" || !slices.Equal(out.cursors, []string{"b", "c"}) {
		t.Fatalf("bounded: %+v, ended %v", out, ended)
	}
	if out, ended = sinceBounded(p, forgeFilter{}); ended || len(out.items) != 4 || out.next != "d" {
		t.Fatalf("no bounds: %+v", out)
	}
}

// The probe's key keeps a read with another since apart, and sourcesOf names a scope's sources
// as their nodes do.
func TestSourcesOfNamesSourcesAsNodes(t *testing.T) {
	t.Parallel()

	got := sourcesOf("github", githubScope{repos: []string{"Alexec/Agents"}, folderOf: map[string]string{"Alexec/Agents": "/f"}})
	if len(got) != 1 || got[0].name != "github:alexec/agents" || got[0].folder != "/f" {
		t.Fatalf("github: %+v", got)
	}
	got = sourcesOf("linear", linearScope{issues: true, workspace: true, home: "/h"})
	if len(got) != 1 || got[0].name != "linear" || got[0].folder != "/h" {
		t.Fatalf("a Linear workspace: %+v", got)
	}
	got = sourcesOf("jira", jiraScope{issues: true, projects: []string{"PROJ", "OPS"}, projectFolder: map[string]string{"OPS": "/o"}})
	if len(got) != 2 || got[1].name != "jira:ops" || got[1].folder != "/o" || got[1].scope.(jiraScope).projects[0] != "OPS" {
		t.Fatalf("jira: %+v", got)
	}
}

// fixedScope is a scope that names no repos, as a forge's own type may be.
type fixedScope struct{ key string }

func (s fixedScope) Key() []any  { return []any{s.key} }
func (s fixedScope) Empty() bool { return s.key == "" }

func TestForgeListScopeNamesNamesEachRepoOnce(t *testing.T) {
	t.Parallel()

	got := forgeListScopeNames([]forgeListScoped{
		{forge: githubForge{}, scope: githubScope{repos: []string{"example/web", "Example/Shop"}}},
		{forge: githubForge{}, scope: githubScope{repos: []string{"example/shop"}}},
		{forge: githubForge{}, scope: fixedScope{key: "k"}},
	})
	want := []string{`github:["k"]`, "github:example/shop", "github:example/web"}
	if !slices.Equal(got, want) {
		t.Fatalf("scopes = %q, want %q", got, want)
	}
}
