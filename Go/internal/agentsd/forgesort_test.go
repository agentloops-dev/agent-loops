package agentsd

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/githubmcp"
	"github.com/agentloops-dev/agent-loops/Go/internal/jiramcp"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// #6538: two asks of the same list at once read GitHub once, and both get the list.
func TestTwoAsksOfOneListAtOnceSearchOnce(t *testing.T) {
	t.Parallel()

	d, _, gh, _ := forgeDaemon(t)
	gh.hold, gh.entered = make(chan struct{}), make(chan struct{}, 2)
	joining := make(chan struct{}, 2)
	d.forgeListJoining = func() { joining <- struct{}{} }
	var wg sync.WaitGroup
	lists := make([]wire.ForgeList, 2)
	ask := func(n int) {
		wg.Go(func() {
			list, err := d.forgeList(context.Background(), true, wire.ForgeListRequest{Label: new("enhancement")})
			if err != nil {
				t.Error(err)
			}
			lists[n] = list
		})
	}
	// The first ask's search is held; the second comes to the join while it is.
	ask(0)
	<-joining
	<-gh.entered
	ask(1)
	<-joining
	close(gh.hold)
	wg.Wait()
	searches, _ := gh.asked()
	held := 0
	for _, s := range searches {
		if s == "held" {
			held++
		}
	}
	if held != 1 {
		t.Fatalf("two asks at once searched %d times: %q", held, searches)
	}
	if len(lists[0].Items) != 1 || len(lists[1].Items) != 1 {
		t.Fatalf("lists: %+v", lists)
	}
}

// A list's sort goes to GitHub, and a sort that is not one is refused.
func TestForgeListSortsByPriorityOnlyWhenAsked(t *testing.T) {
	t.Parallel()

	_, c, gh, _ := forgeDaemon(t)
	c.call(t, 1, "forge/issues", wire.ForgeListRequest{Sort: new("priority")})
	gh.mu.Lock()
	sorted := gh.sorts
	gh.mu.Unlock()
	if len(sorted) != 1 || sorted[0] != githubmcp.SortPriority {
		t.Fatalf("sorts: %q", sorted)
	}
	if _, err := forgeListCheckedSort(wire.ForgeListRequest{Sort: new("title")}); err == nil {
		t.Fatal("sort title was taken")
	}
	for _, s := range []*string{nil, new("updated"), new("priority")} {
		if _, err := forgeListCheckedSort(wire.ForgeListRequest{Sort: s}); err != nil {
			t.Fatalf("sort %v: %v", s, err)
		}
	}
}

// forgeListCheckedSort is forgeListChecked's answer about the sort only.
func forgeListCheckedSort(r wire.ForgeListRequest) (int, error) {
	n, _, _, err := forgeListChecked("forge/issues", r)
	return n, err
}

// Forges' pages in priority order merge in priority order: High, Medium, Low, none, each newest first.
func TestForgePagesMergeInPriorityOrder(t *testing.T) {
	t.Parallel()

	item := func(title, priority, updated string) wire.ForgeItem {
		at, _ := time.Parse(time.RFC3339, updated)
		i := wire.ForgeItem{Title: title, UpdatedAt: wire.DateOf(at)}
		if priority != "" {
			i.Priority = &priority
		}
		return i
	}
	pages := []forgePage{
		{forge: "GitHub", items: []wire.ForgeItem{item("gh-high", "high", "2026-11-01T00:00:00Z"), item("gh-low", "low", "2026-11-05T00:00:00Z"),
			item("gh-none", "", "2026-11-09T00:00:00Z")}, cursors: []string{"1", "2", "3"}},
		{forge: "Jira", items: []wire.ForgeItem{item("jira-high", "high", "2026-11-02T00:00:00Z"), item("jira-medium", "medium", "2026-10-01T00:00:00Z")},
			cursors: []string{"a", "b"}},
	}
	items, _, _ := mergeForgePagesBy(pages, forgeCursor{}, 10, true)
	var got []string
	for _, i := range items {
		got = append(got, i.Title)
	}
	if strings.Join(got, " ") != "jira-high gh-high jira-medium gh-low gh-none" {
		t.Fatalf("merged %v", got)
	}
}

// Jira sorts by its own priority field.
func TestJiraSortsByItsPriority(t *testing.T) {
	t.Parallel()

	if q := jiramcp.SearchJQL([]string{"AL"}, jiramcp.SearchFilter{Sort: jiramcp.SortPriority}); !strings.HasSuffix(q, " ORDER BY priority DESC, updated DESC") {
		t.Fatalf("JQL %q", q)
	}
	if q := jiramcp.SearchJQL([]string{"AL"}, jiramcp.SearchFilter{}); !strings.HasSuffix(q, " ORDER BY updated DESC") {
		t.Fatalf("JQL %q", q)
	}
}
