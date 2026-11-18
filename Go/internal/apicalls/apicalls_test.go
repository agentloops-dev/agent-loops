package apicalls

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBudgetReadsEachForgesHeaders(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	for _, c := range []struct {
		name    string
		headers map[string]string
		want    Budget
	}{
		{"GitHub", map[string]string{"X-RateLimit-Limit": "5000", "X-RateLimit-Remaining": "12", "X-RateLimit-Reset": "1800000600", "X-RateLimit-Resource": "graphql"},
			Budget{Resource: "graphql", Limit: 5000, Remaining: 12, Reset: now.Add(10 * time.Minute)}},
		{"GitLab", map[string]string{"RateLimit-Limit": "2000", "RateLimit-Remaining": "1999", "RateLimit-Reset": "1800000060"},
			Budget{Limit: 2000, Remaining: 1999, Reset: now.Add(time.Minute)}},
		{"Linear", map[string]string{"X-RateLimit-Requests-Limit": "1500", "X-RateLimit-Requests-Remaining": "1400", "X-RateLimit-Requests-Reset": "1800003600000"},
			Budget{Limit: 1500, Remaining: 1400, Reset: now.Add(time.Hour)}},
		{"seconds from now", map[string]string{"RateLimit-Limit": "10", "RateLimit-Remaining": "9", "RateLimit-Reset": "30"},
			Budget{Limit: 10, Remaining: 9, Reset: now.Add(30 * time.Second)}},
	} {
		h := http.Header{}
		for k, v := range c.headers {
			h.Set(k, v)
		}
		got, ok := budgetOf(h, now)
		c.want.At = now
		if !ok || got != c.want {
			t.Errorf("%s: %+v, %v; want %+v", c.name, got, ok, c.want)
		}
	}
	if _, ok := budgetOf(http.Header{}, now); ok {
		t.Error("no headers is no budget")
	}
}

func TestTheMeterCountsByCallerAndLogsTheBudget(t *testing.T) {
	remaining := "4000"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", remaining)
		w.Header().Set("X-RateLimit-Reset", "0")
		w.Header().Set("X-RateLimit-Resource", strings.Trim(r.URL.Path, "/"))
		switch r.URL.Query().Get("answer") {
		case "304":
			w.WriteHeader(http.StatusNotModified)
		case "403":
			w.WriteHeader(http.StatusForbidden)
		}
	}))
	defer srv.Close()
	var lines []string
	m := New("github", func(s string) { lines = append(lines, s) })
	client := m.Client(nil)
	get := func(caller, path string) {
		req, _ := http.NewRequestWithContext(WithCaller(context.Background(), caller), http.MethodGet, srv.URL+path, nil)
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
	}
	get("event pr.blocked", "/graphql")
	get("event pr.blocked", "/graphql")
	get("event pr.merged", "/core?answer=304")
	get("", "/core?answer=403")
	if len(lines) != 0 {
		t.Fatalf("logged before the summary: %q", lines)
	}
	remaining = "900"
	get("tool show_pull_request", "/graphql")
	get("tool show_pull_request", "/graphql")
	if len(lines) != 1 || !strings.Contains(lines[0], "github graphql budget low: 900 of 5000 left") {
		t.Fatalf("low budget said once: %q", lines)
	}

	line := m.Summary(time.Now())
	for _, want := range []string{"api calls: github, 6 in ", "event pr.blocked graphql 2", "tool show_pull_request graphql 2",
		"event pr.merged core 1 (1 unchanged)", "other core 1 (1 refused)", "budget: core 4000 of 5000 left; graphql 900 of 5000 left"} {
		if !strings.Contains(line, want) {
			t.Errorf("summary %q: no %q", line, want)
		}
	}
	if strings.Index(line, "event pr.blocked") > strings.Index(line, "event pr.merged") {
		t.Errorf("most calls first: %q", line)
	}
	if again := m.Summary(time.Now()); again != "" {
		t.Errorf("the count starts again: %q", again)
	}
}
