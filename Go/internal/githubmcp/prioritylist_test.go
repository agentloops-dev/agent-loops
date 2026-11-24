package githubmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAPriorityCursorReadsBackItsPlace(t *testing.T) {
	t.Parallel()
	for _, want := range []priorityPlace{{}, {Bucket: 1, After: "Y3Vyc29y", Read: 4}, {Bucket: 3, After: "a:b", Counts: []int{1, 0, 2, 9}}} {
		got, err := readPriorityCursor(want.text())
		if err != nil || got.Bucket != want.Bucket || got.After != want.After || got.Read != want.Read || !slices.Equal(got.Counts, want.Counts) {
			t.Fatalf("read %+v, %v; want %+v", got, err, want)
		}
	}
	if p, err := readPriorityCursor(""); err != nil || p.Bucket != 0 || p.After != "" || p.Counts != nil {
		t.Fatalf("the start: %+v %v", p, err)
	}
	for _, bad := range []string{"Y3Vyc29y", "prio:!!", priorityCursorPrefix + "eyJiIjo5fQ", priorityCursorPrefix + "eyJiIjowLCJjIjpbMV19"} {
		if _, err := readPriorityCursor(bad); err == nil {
			t.Fatalf("%q read as a priority cursor", bad)
		}
	}
}

func TestAPriorityListReadsGitHubsListOnlyWhenItCan(t *testing.T) {
	t.Parallel()
	one := []string{"o/r"}
	cases := []struct {
		repos []string
		f     SearchFilter
		want  bool
	}{
		{one, SearchFilter{}, true},
		{one, SearchFilter{Assignee: "sam"}, true},
		{one, SearchFilter{PRs: true}, true},
		// GitHub's lists match labels any of, so another label needs search, whose labels match all.
		{one, SearchFilter{Label: "epic"}, false},
		{one, SearchFilter{PRs: true, Assignee: "sam"}, false},
		{one, SearchFilter{Query: "crash"}, false},
		{one, SearchFilter{UpdatedSince: "2026-11-01T00:00:00Z"}, false},
		{[]string{"o/r", "o/s"}, SearchFilter{}, false},
	}
	for _, c := range cases {
		if got := readsList(c.repos, c.f); got != c.want {
			t.Fatalf("readsList(%v, %+v) = %v, want %v", c.repos, c.f, got, c.want)
		}
	}
}

func TestAnItemIsInItsHighestPriority(t *testing.T) {
	t.Parallel()
	if b := priorityBucket(Item{Labels: []string{"bug", "Priority:Low", "priority:medium"}}); b != 1 {
		t.Fatalf("bucket %d, want medium's", b)
	}
	if b := priorityBucket(Item{Labels: []string{"bug", "priority"}}); b != noPriority {
		t.Fatalf("a label that is no priority's put it in bucket %d", b)
	}
}

func TestALaterPageAsksOnlyThePrioritiesItReaches(t *testing.T) {
	t.Parallel()
	counts := []int{30, 0, 5, 100}
	buckets := func(asks []bucketAsk) []int {
		out := []int{}
		for _, a := range asks {
			out = append(out, a.bucket)
		}
		return out
	}
	if got := buckets(priorityPlace{}.plan(25)); !slices.Equal(got, []int{0, 1, 2, 3}) {
		t.Fatalf("the first page asks %v", got)
	}
	if got := buckets(priorityPlace{Bucket: 0, Read: 0, Counts: counts}.plan(25)); !slices.Equal(got, []int{0}) {
		t.Fatalf("a page that High fills asks %v", got)
	}
	// High has 10 left: Medium has none, so it is left out; Low's 5 do not fill the page, so none comes too.
	asks := priorityPlace{Bucket: 0, Read: 20, Counts: counts}.plan(25)
	if got := buckets(asks); !slices.Equal(got, []int{0, 2, 3}) {
		t.Fatalf("a page across priorities asks %v", got)
	}
	if asks[1].first != 15 || asks[2].first != 10 {
		t.Fatalf("each later priority asks for what is left of the page: %+v", asks)
	}
}

// fakeItem is an issue of the fake GitHub.
type fakeItem struct {
	number  int
	labels  []string
	updated time.Time
}

// priorityGitHub is a fake GitHub that answers a priority list's one request: each aliased search or list, with
// its labels, count and cursor. It counts the requests and can hold them, to see two asks join.
type priorityGitHub struct {
	items    []fakeItem
	cap      int // search's cap on results, as GitHub's 1,000; 0 for none
	requests atomic.Int32
	hold     chan struct{}
	asked    chan struct{}
	mu       sync.Mutex
	last     string
}

// aliases are the connections the last request asked for.
func (g *priorityGitHub) aliases() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := []string{}
	for _, m := range connRE.FindAllStringSubmatch(g.last, -1) {
		out = append(out, m[1])
	}
	return out
}

var (
	connRE    = regexp.MustCompile(`(b\d|total|l): (search|issues|pullRequests)\((.*?)\) \{`)
	firstRE   = regexp.MustCompile(`first: (\d+)`)
	afterRE   = regexp.MustCompile(`after: \$(\w+)`)
	queryRE   = regexp.MustCompile(`query: \$(\w+)`)
	listLabel = regexp.MustCompile(`labels: \["([^"]+)"\]`)
	hasLabel  = regexp.MustCompile(`(-?)label:"([^"]+)"`)
)

func (g *priorityGitHub) serve(t *testing.T) *RealGitHub {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.requests.Add(1)
		if g.asked != nil {
			g.asked <- struct{}{}
		}
		if g.hold != nil {
			<-g.hold
		}
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		b, _ := io.ReadAll(r.Body)
		if json.Unmarshal(b, &req) != nil {
			t.Error("not JSON")
		}
		g.mu.Lock()
		g.last = req.Query
		g.mu.Unlock()
		out, _ := json.Marshal(map[string]any{"data": g.answer(req.Query, req.Variables)})
		_, _ = w.Write(out)
	}))
	t.Cleanup(s.Close)
	return NewRealGitHub(s.URL+"/", func() string { return "t" }, nil)
}

// answer is the data for a request: each aliased search at the root, each list in repository.
func (g *priorityGitHub) answer(query string, vars map[string]any) map[string]any {
	root, repo := map[string]any{}, map[string]any{}
	for _, m := range connRE.FindAllStringSubmatch(query, -1) {
		alias, kind, args := m[1], m[2], m[3]
		found := g.found(kind, args, vars)
		conn := g.connection(found, args, vars)
		if kind == "search" && g.cap > 0 && len(found) > g.cap {
			// GitHub search counts every match but gives only the first 1,000.
			conn = g.connection(found[:g.cap], args, vars)
			conn["issueCount"] = len(found)
		}
		if kind == "search" {
			root[alias] = conn
		} else {
			repo[alias] = conn
		}
	}
	if len(repo) > 0 {
		root["repository"] = repo
	}
	return root
}

// found are the items a connection's labels match, the newest first.
func (g *priorityGitHub) found(kind, args string, vars map[string]any) []fakeItem {
	keep := func(i fakeItem) bool {
		l := listLabel.FindStringSubmatch(args)
		return l == nil || slices.Contains(i.labels, l[1])
	}
	if kind == "search" {
		words := vars[queryRE.FindStringSubmatch(args)[1]].(string)
		keep = func(i fakeItem) bool {
			return !slices.ContainsFunc(hasLabel.FindAllStringSubmatch(words, -1), func(l []string) bool {
				return slices.Contains(i.labels, l[2]) == (l[1] == "-")
			})
		}
	}
	var found []fakeItem
	for _, i := range g.items {
		if keep(i) {
			found = append(found, i)
		}
	}
	slices.SortFunc(found, func(a, b fakeItem) int { return b.updated.Compare(a.updated) })
	return found
}

// connection is a page of found from the connection's after, its count and its cursors.
func (g *priorityGitHub) connection(found []fakeItem, args string, vars map[string]any) map[string]any {
	first, _ := strconv.Atoi(firstRE.FindStringSubmatch(args)[1])
	from := 0
	if a := afterRE.FindStringSubmatch(args); a != nil {
		from, _ = strconv.Atoi(vars[a[1]].(string))
	}
	end := min(len(found), from+first)
	edges := []any{}
	for n := from; n < end; n++ {
		edges = append(edges, map[string]any{"cursor": strconv.Itoa(n + 1), "node": fakeNode(found[n])})
	}
	return map[string]any{"totalCount": len(found), "issueCount": len(found), "edges": edges,
		"pageInfo": map[string]any{"hasNextPage": end < len(found), "endCursor": strconv.Itoa(end)}}
}

// fakeNode is an item as GitHub's GraphQL says it.
func fakeNode(i fakeItem) map[string]any {
	labels := []any{}
	for _, l := range i.labels {
		labels = append(labels, map[string]any{"name": l, "color": "ededed"})
	}
	return map[string]any{"__typename": "Issue", "number": i.number, "issueState": "OPEN",
		"repository": map[string]any{"nameWithOwner": "o/r"}, "updatedAt": i.updated.Format(time.RFC3339),
		"labels": map[string]any{"nodes": labels}}
}

// fakeItems are n issues, the newest first by number, each with label(n).
func fakeItems(n int, label func(int) []string) []fakeItem {
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	items := make([]fakeItem, n)
	for k := range items {
		items[k] = fakeItem{number: k + 1, labels: label(k + 1), updated: start.Add(-time.Duration(k) * time.Minute)}
	}
	return items
}

// byPriorityLabel puts each third issue in High, Medium and Low, and every fifth in none, so each priority has
// some; issue 2 also has High, so it shows in High only.
func byPriorityLabel(n int) []string {
	switch {
	case n == 2:
		return []string{"priority:medium", "priority:high"}
	case n%5 == 0:
		return []string{"bug"}
	}
	return []string{priorityLabels[n%3]}
}

// wantOrder is the order of a list in priority order: High, Medium, Low, none, each newest first.
func wantOrder(items []fakeItem) []int {
	var out []int
	for b := 0; b <= noPriority; b++ {
		for _, i := range items {
			if priorityBucket(Item{Labels: i.labels}) == b {
				out = append(out, i.number)
			}
		}
	}
	return out
}

func TestAPriorityListIsOneRequestAPageInPriorityOrder(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		repos []string
		f     SearchFilter
	}{
		{"github's list", []string{"o/r"}, SearchFilter{Sort: SortPriority}},
		{"search", []string{"o/r"}, SearchFilter{Sort: SortPriority, Label: "bug"}},
		{"search of two repos", []string{"o/r", "o/s"}, SearchFilter{Sort: SortPriority}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			fake := &priorityGitHub{items: fakeItems(47, func(n int) []string {
				l := byPriorityLabel(n)
				if c.f.Label != "" && n%2 == 0 {
					l = append(l, c.f.Label)
				}
				return l
			})}
			want := wantOrder(withLabel(fake.items, c.f.Label))
			got, pages := listEvery(t, fake.serve(t), c.repos, c.f, 6, len(want))
			if !slices.Equal(got, want) {
				t.Fatalf("listed %v\nwant   %v", got, want)
			}
			if n := int(fake.requests.Load()); n != pages {
				t.Fatalf("%d requests for %d pages", n, pages)
			}
		})
	}
}

// withLabel are the items with label, all of them for "".
func withLabel(items []fakeItem, label string) []fakeItem {
	if label == "" {
		return items
	}
	var out []fakeItem
	for _, i := range items {
		if slices.Contains(i.labels, label) {
			out = append(out, i)
		}
	}
	return out
}

// listEvery reads a list page by page to its end, and says its items' numbers and how many pages. Every page
// but the last is full, and the first says the count, total.
func listEvery(t *testing.T, gh *RealGitHub, repos []string, f SearchFilter, first, total int) ([]int, int) {
	t.Helper()
	var got []int
	after := ""
	for pages := 1; ; pages++ {
		p, err := gh.Search(context.Background(), repos, f, after, first)
		if err != nil {
			t.Fatal(err)
		}
		if pages == 1 && (p.Total == nil || *p.Total != total) {
			t.Fatalf("the first page's count is %v, want %d", p.Total, total)
		}
		if p.Next != "" && len(p.Items) != first {
			t.Fatalf("page %d has %d items and goes on", pages, len(p.Items))
		}
		for _, i := range p.Items {
			got = append(got, i.Number)
		}
		if p.Next == "" {
			return got, pages
		}
		after = p.Next
	}
}

func TestAPriorityListGoesOnFromAnyItemsCursor(t *testing.T) {
	t.Parallel()
	fake := &priorityGitHub{items: fakeItems(20, byPriorityLabel)}
	gh := fake.serve(t)
	f := SearchFilter{Sort: SortPriority}
	want := wantOrder(fake.items)
	p, err := gh.Search(context.Background(), []string{"o/r"}, f, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	// The daemon keeps only some of a page when it merges forges, and goes on from the last it kept.
	for k := range p.Items {
		next, err := gh.Search(context.Background(), []string{"o/r"}, f, p.Cursors[k], 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(next.Items) != 1 || next.Items[0].Number != want[k+1] {
			t.Fatalf("after item %d: %v, want #%d", k, next.Items, want[k+1])
		}
	}
}

func TestAPriorityWithNoItemsIsNotAsked(t *testing.T) {
	t.Parallel()
	fake := &priorityGitHub{items: fakeItems(12, func(n int) []string {
		if n%2 == 0 {
			return []string{"priority:high"}
		}
		return nil
	})}
	gh := fake.serve(t)
	f := SearchFilter{Sort: SortPriority}
	p, err := gh.Search(context.Background(), []string{"o/r"}, f, "", 4)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gh.Search(context.Background(), []string{"o/r"}, f, p.Next, 4); err != nil {
		t.Fatal(err)
	}
	// High's last 2 and the 2 newest with none: Medium and Low have none, so they are left out.
	if got := fake.aliases(); !slices.Equal(slices.Sorted(slices.Values(got)), []string{"b0", "b3"}) {
		t.Fatalf("the second page asks %v", got)
	}
}

func TestTwoAsksOfTheSameListAtOnceAreOneRequest(t *testing.T) {
	t.Parallel()
	fake := &priorityGitHub{items: fakeItems(5, byPriorityLabel), hold: make(chan struct{}), asked: make(chan struct{}, 4)}
	gh := fake.serve(t)
	var wg sync.WaitGroup
	results := make([]SearchPage, 2)
	for n := range results {
		wg.Go(func() {
			p, err := gh.Search(context.Background(), []string{"o/r"}, SearchFilter{Label: "bug"}, "", 10)
			if err != nil {
				t.Error(err)
			}
			results[n] = p
		})
		if n == 0 {
			<-fake.asked
		}
	}
	// GitHub answers once the second ask waits for the first.
	for gh.waiting() == 0 {
		time.Sleep(time.Millisecond)
	}
	close(fake.hold)
	wg.Wait()
	if n := fake.requests.Load(); n != 1 {
		t.Fatalf("%d requests for two asks at once", n)
	}
	if fmt.Sprint(results[0].Items) != fmt.Sprint(results[1].Items) {
		t.Fatal("the asks that joined got different answers")
	}
}

// bigProject is the fixture of review #6355's big project: 5,000 open issues, 4,000 of them with no priority, and
// search capped at 1,000 as GitHub's.
func bigProject() *priorityGitHub {
	return &priorityGitHub{cap: searchCap, items: fakeItems(5000, func(n int) []string {
		if n%5 != 0 {
			return []string{"bug"}
		}
		return []string{priorityLabels[n%3]}
	})}
}

func TestABigProjectsPriorityListReachesItsEnd(t *testing.T) {
	t.Parallel()
	fake := bigProject()
	gh := fake.serve(t)
	got, pages := listEvery(t, gh, []string{"o/r"}, SearchFilter{Sort: SortPriority}, 100, 5000)
	if want := wantOrder(fake.items); !slices.Equal(got, want) {
		t.Fatalf("listed %d items, want all %d in priority order", len(got), len(want))
	}
	// The whole list drops the 1,000 with a priority, so a page of it can ask twice, never more.
	if n := int(fake.requests.Load()); n > 2*pages {
		t.Fatalf("%d requests for %d pages", n, pages)
	}
}

func TestABigProjectsLastUpdatedListReachesItsEnd(t *testing.T) {
	t.Parallel()
	for _, f := range []SearchFilter{{}, {Label: "bug"}} {
		fake := bigProject()
		want := withLabel(fake.items, f.Label)
		got, _ := listEvery(t, fake.serve(t), []string{"o/r"}, f, 100, len(want))
		if len(got) != len(want) || got[len(got)-1] != want[len(want)-1].number {
			t.Fatalf("label %q: listed %d items, want %d", f.Label, len(got), len(want))
		}
		if a := fake.aliases(); !slices.Equal(a, []string{"l"}) {
			t.Fatalf("label %q: read with %v, not GitHub's list", f.Label, a)
		}
	}
}

func TestOnlyAListOfOneRepoAndOneLabelReadsGitHubsList(t *testing.T) {
	t.Parallel()
	one := []string{"o/r"}
	for _, c := range []struct {
		repos []string
		f     SearchFilter
		want  bool
	}{
		{one, SearchFilter{}, true},
		{one, SearchFilter{Label: "bug", Assignee: "sam"}, true},
		{one, SearchFilter{Feature: "work"}, true},
		{one, SearchFilter{Label: "bug", Feature: "work"}, false},
		{one, SearchFilter{PRs: true, Assignee: "sam"}, false},
		{one, SearchFilter{Query: "crash"}, false},
		{one, SearchFilter{Reviewer: "sam"}, false},
		{[]string{"o/r", "o/s"}, SearchFilter{}, false},
	} {
		if got := readsPlainList(c.repos, c.f); got != c.want {
			t.Fatalf("readsPlainList(%v, %+v) = %v, want %v", c.repos, c.f, got, c.want)
		}
	}
}

func TestASearchCursorFromBeforeGoesOnInSearch(t *testing.T) {
	t.Parallel()
	fake := &priorityGitHub{items: fakeItems(30, byPriorityLabel)}
	if _, err := fake.serve(t).Search(context.Background(), []string{"o/r"}, SearchFilter{}, "10", 10); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if !strings.Contains(fake.last, "search(") || strings.Contains(fake.last, "repository(") {
		t.Fatalf("a search cursor did not go on in search: %s", fake.last)
	}
}
