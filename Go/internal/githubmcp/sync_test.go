package githubmcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// #6349: Newest is a REST read with its ETag kept: while nothing changed, GitHub answers 304
// (which costs no rate limit) and the mark is the same; a change is a new body, a new ETag and a
// new mark. Nothing updated since is "".
func TestNewestCostsNoRateWhileNothingChanged(t *testing.T) {
	var mu sync.Mutex
	body, etag := `[{"number":1,"updated_at":"2026-11-19T10:00:00Z"}]`, `"v1"`
	var asked, notModified int
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		asked++
		if r.URL.Path != "/repos/alexec/agents/issues" || r.URL.Query().Get("since") != "2026-11-19T09:59:00Z" ||
			r.URL.Query().Get("per_page") != "1" || r.URL.Query().Get("state") != "all" || r.URL.Query().Get("sort") != "updated" {
			t.Errorf("asked %s", r.URL)
		}
		if r.Header.Get("If-None-Match") == etag {
			notModified++
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(s.Close)
	g := NewRealGitHub(s.URL, func() string { return "t" }, nil)
	ctx := context.Background()
	first, err := g.Newest(ctx, "alexec/agents", "2026-11-19T09:59:00Z")
	if err != nil || first == "" {
		t.Fatalf("first: %q, %v", first, err)
	}
	again, err := g.Newest(ctx, "alexec/agents", "2026-11-19T09:59:00Z")
	if err != nil || again != first || notModified != 1 {
		t.Fatalf("again: %q (first %q), %v, %d answered 304", again, first, err, notModified)
	}
	mu.Lock()
	body, etag = `[{"number":1,"updated_at":"2026-11-19T10:05:00Z"}]`, `"v2"`
	mu.Unlock()
	changed, err := g.Newest(ctx, "alexec/agents", "2026-11-19T09:59:00Z")
	if err != nil || changed == first || notModified != 1 || asked != 3 {
		t.Fatalf("changed: %q, %v, %d of %d answered 304", changed, err, notModified, asked)
	}
	mu.Lock()
	body, etag = `[]`, `"v3"`
	mu.Unlock()
	if none, err := g.Newest(ctx, "alexec/agents", "2026-11-19T09:59:00Z"); err != nil || none != "" {
		t.Fatalf("nothing since: %q, %v", none, err)
	}
}

// #6349: PullChecks reads many pull requests' checks in one GraphQL call, one alias each; a
// number that is not a pull request (null, with a NOT_FOUND error) is left out.
func TestPullChecksReadsManyInOneCall(t *testing.T) {
	var calls int
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req struct {
			Query string `json:"query"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &req)
		if !strings.Contains(req.Query, "p5: pullRequest(number: 5)") || !strings.Contains(req.Query, "p7: pullRequest(number: 7)") {
			t.Errorf("query: %s", req.Query)
		}
		_, _ = io.WriteString(w, `{"data":{"repository":{
			"p5":{"number":5,"pullState":"OPEN","mergeable":"MERGEABLE","reviewDecision":"APPROVED","autoMergeRequest":{"enabledAt":"x"},
				"checkState":{"nodes":[{"commit":{"statusCheckRollup":{"state":"FAILURE"}}}]}},
			"p7":null}},
			"errors":[{"type":"NOT_FOUND","path":["repository","p7"],"message":"no pull request 7"}]}`)
	}))
	t.Cleanup(s.Close)
	g := NewRealGitHub(s.URL, func() string { return "t" }, nil)
	items, err := g.PullChecks(context.Background(), "alexec/agents", []int{5, 7})
	if err != nil || calls != 1 || len(items) != 1 {
		t.Fatalf("%d calls, %+v, %v", calls, items, err)
	}
	i := items[0]
	if !i.PR || i.Number != 5 || i.Repo != "alexec/agents" || i.State != "open" || i.Checks != "failing" || i.Review != "approved" ||
		i.Mergeable != "mergeable" || !i.AutoMerge {
		t.Fatalf("item: %+v", i)
	}
	many := make([]int, ChecksMost+1)
	if _, err := g.PullChecks(context.Background(), "alexec/agents", many); err == nil || calls != 1 {
		t.Fatalf("%d pull requests: %v", len(many), err)
	}
}

// #6349: a list's updated bounds are search qualifiers.
func TestSearchQueryUpdatedBounds(t *testing.T) {
	q := SearchQuery([]string{"alexec/agents"}, SearchFilter{State: "all", UpdatedSince: "2026-11-19T10:00:00Z", UpdatedBefore: "2026-11-19T11:00:00Z"})
	if !strings.Contains(q, "updated:>=2026-11-19T10:00:00Z updated:<=2026-11-19T11:00:00Z") || strings.Contains(q, "is:open") {
		t.Fatalf("query: %s", q)
	}
}
