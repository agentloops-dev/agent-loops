package githubmcp

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// checkSlimChildren says that query asks for none of a list row's fields of a sub-issue (#7026).
func checkSlimChildren(t *testing.T, query string) {
	t.Helper()
	_, children, _ := strings.Cut(query, "subIssues(")
	for _, f := range []string{"title", "url", "color", "issueFieldValues", "author", "assignees", "milestone", "comments"} {
		if strings.Contains(children, f) {
			t.Errorf("a child is read with %s: %s", f, children)
		}
	}
}

// #4172: Timeline reads the open epics, and those closed since a time, as two light lists of
// ids at once, then the epics by id in batches, each with its sub-issues' dates and the PRs that
// close them; the releases come with the open list, drafts left out; an epic in both lists is
// said once, and the order is the lists' order. A child has no field that only
// a list's row draws, and an open epic that skip takes is not read (#7026).
func TestTimelineReadsEpicsChildrenAndReleases(t *testing.T) {
	issue := func(n int, state, extra string) string {
		return `{"number":` + strconv.Itoa(n) + `,"title":"t","url":"u","issueState":"` + state + `","updatedAt":"2026-11-08T10:00:00Z","author":null,
		 "repository":{"nameWithOwner":"alexec/Agents"},"labels":{"nodes":[{"name":"epic"}]},"assignees":{"nodes":[]},"milestone":null,
		 "commentCount":{"totalCount":0},"parent":null,"subIssuesSummary":{"total":1,"completed":0},
		 "createdAt":"2026-11-01T00:00:00Z","closedAt":null,"stateReason":null` + extra + `}`
	}
	child := `"subIssues":{"nodes":[` + strings.TrimSuffix(issue(11, "CLOSED", `,"closedByPullRequestsReferences":{"nodes":[{"createdAt":"2026-11-02T00:00:00Z"}]}`), "}") +
		`}]}`
	child = strings.Replace(child, `"closedAt":null,"stateReason":null`, `"closedAt":"2026-11-03T00:00:00Z","stateReason":"COMPLETED"`, 1)
	child = strings.Replace(child, `{"name":"epic"}`, `{"name":"alex-todo"}`, 1)
	byID := map[string]string{
		"I1": issue(1, "OPEN", ","+child),
		"I2": issue(2, "OPEN", `,"subIssues":{"nodes":[]}`),
		"I3": strings.Replace(issue(3, "CLOSED", `,"subIssues":{"nodes":[]}`), `"closedAt":null,"stateReason":null`,
			`"closedAt":"2026-11-04T00:00:00Z","stateReason":"NOT_PLANNED"`, 1),
	}
	var mu sync.Mutex
	var lists []map[string]any
	var batches [][]any
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		body, _ := io.ReadAll(r.Body)
		unmarshal(t, body, &req)
		mu.Lock()
		defer mu.Unlock()
		if ids, ok := req.Variables["ids"].([]any); ok {
			batches = append(batches, ids)
			checkSlimChildren(t, req.Query)
			var nodes []string
			for _, id := range ids {
				if n, ok := byID[id.(string)]; ok {
					nodes = append(nodes, n)
				} else {
					nodes = append(nodes, "null")
				}
			}
			reply(t, w, `{"data":{"nodes":[`+strings.Join(nodes, ",")+`]}}`)
			return
		}
		lists = append(lists, req.Variables)
		releases := `"releases":{"nodes":[{"tagName":"v1","publishedAt":"2026-11-05T00:00:00Z","url":"r1","isDraft":false},{"tagName":"v2","publishedAt":"2026-11-06T00:00:00Z","url":"r2","isDraft":true}]}`
		// A closed epic is read whatever its labels: the forecast learns from it.
		ids := `{"id":"I2","number":2},{"id":"I3","number":3,"labels":{"nodes":[{"name":"on hold"}]}},{"id":"I9","number":9}`
		if req.Variables["states"].([]any)[0] == "OPEN" {
			ids = `{"id":"I1","number":1},{"id":"I2","number":2},{"id":"I4","number":4,"labels":{"nodes":[{"name":"epic"},{"name":"on hold"}]}}`
		}
		reply(t, w, `{"data":{"repository":{"issues":{"nodes":[`+ids+`]},`+releases+`}}}`)
	}))
	t.Cleanup(s.Close)
	g := NewRealGitHub(s.URL, func() string { return "t" }, nil)
	tl, err := g.Timeline(context.Background(), "alexec/Agents", "2026-10-09T00:00:00Z", "", func(labels []string) bool { return slices.Contains(labels, "on hold") })
	if err != nil {
		t.Fatal(err)
	}
	if len(lists) != 2 {
		t.Fatalf("lists %v", lists)
	}
	for _, l := range lists {
		open := l["states"].([]any)[0] == "OPEN"
		if open && (l["since"] != nil || l["releases"] != float64(timelineReleases)) ||
			!open && (l["since"] != "2026-10-09T00:00:00Z" || l["releases"] != float64(1)) || l["first"] != float64(timelineEpics) {
			t.Fatalf("lists %v", lists)
		}
	}
	if len(batches) != 1 || len(batches[0]) != 4 {
		t.Fatalf("batches %v", batches)
	}
	if len(tl.Releases) != 1 || tl.Releases[0].Tag != "v1" {
		t.Fatalf("releases %+v", tl.Releases)
	}
	if len(tl.Epics) != 3 || tl.Epics[0].Number != 1 || tl.Epics[1].Number != 2 || tl.Epics[2].StateReason != "not_planned" || tl.Epics[2].ClosedAt == "" {
		t.Fatalf("epics %+v", tl.Epics)
	}
	c := tl.Epics[0].Children
	if len(c) != 1 || c[0].State != "closed" || c[0].StateReason != "completed" || c[0].ClosedAt != "2026-11-03T00:00:00Z" ||
		len(c[0].PROpenedAt) != 1 || c[0].CreatedAt != "2026-11-01T00:00:00Z" ||
		len(c[0].Labels) != 1 || c[0].Labels[0] != "alex-todo" {
		t.Fatalf("children %+v", c)
	}
}

// Many epics are read in batches of timelineBatch, timelineAtOnce at a time, and keep the
// lists' order.
func TestTimelineReadsEpicsInBatchesInOrder(t *testing.T) {
	const epics = 35
	var mu sync.Mutex
	running, most := 0, 0
	var sizes []int
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Variables map[string]any `json:"variables"`
		}
		body, _ := io.ReadAll(r.Body)
		unmarshal(t, body, &req)
		ids, ok := req.Variables["ids"].([]any)
		if !ok {
			var nodes []string
			if req.Variables["states"].([]any)[0] == "OPEN" {
				for n := 1; n <= epics; n++ {
					nodes = append(nodes, `{"id":"I`+strconv.Itoa(n)+`","number":`+strconv.Itoa(n)+`}`)
				}
			}
			reply(t, w, `{"data":{"repository":{"issues":{"nodes":[`+strings.Join(nodes, ",")+`]},"releases":{"nodes":[]}}}}`)
			return
		}
		mu.Lock()
		running++
		most = max(most, running)
		sizes = append(sizes, len(ids))
		mu.Unlock()
		time.Sleep(20 * time.Millisecond)
		mu.Lock()
		running--
		mu.Unlock()
		var nodes []string
		for _, id := range ids {
			n := strings.TrimPrefix(id.(string), "I")
			nodes = append(nodes, `{"number":`+n+`,"issueState":"OPEN","repository":{"nameWithOwner":"alexec/Agents"},"subIssues":{"nodes":[]}}`)
		}
		reply(t, w, `{"data":{"nodes":[`+strings.Join(nodes, ",")+`]}}`)
	}))
	t.Cleanup(s.Close)
	g := NewRealGitHub(s.URL, func() string { return "t" }, nil)
	tl, err := g.Timeline(context.Background(), "alexec/Agents", "2026-10-09T00:00:00Z", "", func([]string) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	if len(sizes) != 4 || most > timelineAtOnce || most < 2 {
		t.Fatalf("batches %v, at most %d at once", sizes, most)
	}
	if len(tl.Epics) != epics {
		t.Fatalf("%d epics", len(tl.Epics))
	}
	for n, e := range tl.Epics {
		if e.Number != n+1 {
			t.Fatalf("epic %d is #%d", n, e.Number)
		}
	}
}

// #7027: a first read leaves the epics closed before from in Rest; TimelineRest reads those alone, and asks for no list.
func TestTimelineFirstLeavesTheRest(t *testing.T) {
	var mu sync.Mutex
	lists := 0
	var batches []string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Variables map[string]any `json:"variables"`
		}
		body, _ := io.ReadAll(r.Body)
		unmarshal(t, body, &req)
		mu.Lock()
		defer mu.Unlock()
		if ids, ok := req.Variables["ids"].([]any); ok {
			var nodes []string
			for _, id := range ids {
				batches = append(batches, id.(string))
				nodes = append(nodes, `{"number":`+id.(string)[1:]+`}`)
			}
			reply(t, w, `{"data":{"nodes":[`+strings.Join(nodes, ",")+`]}}`)
			return
		}
		lists++
		ids := `{"id":"I2","number":2,"closedAt":"2026-11-03T00:00:00Z"},{"id":"I3","number":3,"closedAt":"2026-11-01T00:00:00Z"}`
		if req.Variables["states"].([]any)[0] == "OPEN" {
			ids = `{"id":"I1","number":1}`
		}
		reply(t, w, `{"data":{"repository":{"issues":{"nodes":[`+ids+`]},"releases":{"nodes":[{"tagName":"v1","publishedAt":"2026-11-05T00:00:00Z","url":"r1"}]}}}}`)
	}))
	t.Cleanup(s.Close)
	g := NewRealGitHub(s.URL, func() string { return "t" }, nil)
	first, err := g.Timeline(context.Background(), "alexec/Agents", "2026-10-09T00:00:00Z", "2026-11-02T00:00:00Z", func([]string) bool { return false })
	if err != nil || len(first.Epics) != 2 || !slices.Equal(first.Rest, []string{"I3"}) || !slices.Equal(batches, []string{"I1", "I2"}) {
		t.Fatalf("%v: first %+v, batches %v", err, first, batches)
	}
	all, err := g.TimelineRest(context.Background(), "alexec/Agents", first)
	if err != nil || len(all.Epics) != 3 || all.Epics[2].Number != 3 || len(all.Rest) != 0 || len(all.Releases) != 1 ||
		!slices.Equal(batches, []string{"I1", "I2", "I3"}) || lists != 2 || len(first.Epics) != 2 {
		t.Fatalf("%v: all %+v, batches %v, %d lists", err, all, batches, lists)
	}
}
