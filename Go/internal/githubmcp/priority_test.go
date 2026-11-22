package githubmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// priorityItem is a search's answer with one issue, its issue field values and its repo's fields.
func priorityItem(values, fields string) string {
	return `{"data":{"search":{"issueCount":1,"pageInfo":{"hasNextPage":false},"edges":[{"cursor":"c","node":{"__typename":"Issue",
  "issueState":"OPEN","number":5,"title":"t","repository":{"nameWithOwner":"o/r"},"labels":{"nodes":[]},"assignees":{"nodes":[]},
  "fieldRepo":{"issueFields":{"nodes":[` + fields + `]}},"issueFieldValues":{"nodes":[` + values + `]}}}]}}}`
}

// #5747: an issue's Priority issue field, Urgent and High as high; a repo with the field and no
// value on the issue says the field is there; a repo without it says not.
func TestPriorityFromIssueField(t *testing.T) {
	priority := `{"name":"Priority"},{},{"name":"Effort"}`
	for _, c := range []struct {
		values, fields, want string
		has                  bool
	}{
		{`{"name":"Urgent","field":{"name":"Priority"}}`, priority, "high", true},
		{`{"name":"High","field":{"name":"Effort"}},{"name":"Low","field":{"name":"priority"}}`, priority, "low", true},
		{`{"name":"Medium","field":{"name":"Priority"}}`, priority, "medium", true},
		{`{"name":"P9","field":{"name":"Priority"}}`, priority, "", true},
		{``, priority, "", true},
		{`{"name":"High","field":{"name":"Effort"}}`, `{"name":"Effort"}`, "", false},
	} {
		var data struct {
			Search struct {
				Edges []struct {
					Node gqlItem `json:"node"`
				} `json:"edges"`
			} `json:"search"`
		}
		var answer struct {
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal([]byte(priorityItem(c.values, c.fields)), &answer); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(answer.Data, &data); err != nil {
			t.Fatal(err)
		}
		i := data.Search.Edges[0].Node.item()
		if i.Priority != c.want || i.PriorityField != c.has {
			t.Errorf("%s / %s: %q %v, want %q %v", c.values, c.fields, i.Priority, i.PriorityField, c.want, c.has)
		}
	}
}

// #5747: a GitHub with no issue fields refuses them; the read asks again without them, and
// from then on leaves them out. A refusal for another reason (a token) is asked again each time.
func TestSearchWithoutIssueFields(t *testing.T) {
	for _, c := range []struct {
		refusal string
		sticky  bool
	}{
		{`{"errors":[{"message":"Field 'issueFieldValues' doesn't exist on type 'Issue'"}]}`, true},
		{`{"data":null,"errors":[{"type":"FORBIDDEN","message":"Resource not accessible","path":["search","edges",0,"node","fieldRepo"]}]}`, false},
	} {
		var with, without atomic.Int32
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				Query string `json:"query"`
			}
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &req)
			if strings.Contains(req.Query, "issueField") {
				with.Add(1)
				_, _ = io.WriteString(w, c.refusal)
				return
			}
			without.Add(1)
			_, _ = io.WriteString(w, `{"data":{"search":{"issueCount":1,"pageInfo":{"hasNextPage":false},"edges":[{"cursor":"c","node":{"__typename":"Issue",
  "issueState":"OPEN","number":5,"title":"t","repository":{"nameWithOwner":"o/r"},"labels":{"nodes":[]},"assignees":{"nodes":[]}}}]}}}`)
		}))
		g := NewRealGitHub(s.URL, func() string { return "t" }, nil)
		for range 2 {
			p, err := g.Search(context.Background(), []string{"o/r"}, SearchFilter{Query: "t"}, "", 10)
			if err != nil || len(p.Items) != 1 || p.Items[0].PriorityField {
				t.Fatalf("%s: %v %+v", c.refusal, err, p)
			}
		}
		if want := map[bool]int32{true: 1, false: 2}[c.sticky]; with.Load() != want || without.Load() != 2 {
			t.Errorf("%s: asked with fields %d, without %d", c.refusal, with.Load(), without.Load())
		}
		s.Close()
	}
}

// #6091: the write finds the issue and the repo's Priority field, sets the option of the
// priority (High for high, Urgent where there is no High), clears it for none, and says there
// is no field where the repo has none or the GitHub refuses issue fields.
func TestSetPriorityField(t *testing.T) {
	for _, c := range []struct {
		name, fields, priority string
		has                    bool
		want                   string
	}{
		{"high", `{"id":"F1","name":"Priority","options":[{"id":"O0","name":"Urgent"},{"id":"O1","name":"High"},{"id":"O3","name":"Low"}]}`, "high", true, "set F1 O1"},
		{"urgent", `{"id":"F1","name":"priority","options":[{"id":"O0","name":"Urgent"}]}`, "high", true, "set F1 O0"},
		{"none", `{"id":"F1","name":"Priority","options":[]}`, "", true, "clear F1"},
		{"no field", `{},{"id":"F2","name":"Effort","options":[{"id":"O1","name":"High"}]}`, "high", false, ""},
		{"no issue fields", "refuse", "high", false, ""},
	} {
		var wrote []string
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				Query     string         `json:"query"`
				Variables map[string]any `json:"variables"`
			}
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &req)
			switch {
			case strings.Contains(req.Query, "setIssueFieldValue"):
				wrote = append(wrote, "set "+req.Variables["field"].(string)+" "+req.Variables["option"].(string))
				_, _ = io.WriteString(w, `{"data":{"setIssueFieldValue":{"issue":{"number":5}}}}`)
			case strings.Contains(req.Query, "deleteIssueFieldValue"):
				wrote = append(wrote, "clear "+req.Variables["field"].(string))
				_, _ = io.WriteString(w, `{"data":{"deleteIssueFieldValue":{"success":true}}}`)
			case c.fields == "refuse":
				_, _ = io.WriteString(w, `{"errors":[{"message":"Field 'issueFields' doesn't exist on type 'Repository'"}]}`)
			default:
				_, _ = io.WriteString(w, `{"data":{"repository":{"issue":{"id":"I5"},"issueFields":{"nodes":[`+c.fields+`]}}}}`)
			}
		}))
		g := NewRealGitHub(s.URL, func() string { return "t" }, nil)
		has, err := g.SetPriorityField(context.Background(), "o/r", 5, c.priority)
		if err != nil || has != c.has || strings.Join(wrote, ";") != c.want {
			t.Errorf("%s: has %v, %v, wrote %q, want %v %q", c.name, has, err, wrote, c.has, c.want)
		}
		s.Close()
	}
}

// #6644: the repo's Priority field is kept: a second change in a repo with no field asks
// nothing, one in a repo with the field asks only the write (the issue's id is kept too, and a
// new issue asks only its id), and after fieldsFor or a failed write it asks again.
func TestSetPriorityFieldKept(t *testing.T) {
	var asked []string
	fail := false
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		repo := req.Variables["name"]
		switch {
		case strings.Contains(req.Query, "setIssueFieldValue"):
			asked = append(asked, "set")
			if fail {
				_, _ = io.WriteString(w, `{"errors":[{"message":"Could not resolve to a node"}]}`)
				return
			}
			_, _ = io.WriteString(w, `{"data":{"setIssueFieldValue":{"issue":{"number":5}}}}`)
		case strings.Contains(req.Query, "issueFields"):
			asked = append(asked, fmt.Sprintf("fields %v", repo))
			fields := ``
			if repo == "with" {
				fields = `{"id":"F1","name":"Priority","options":[{"id":"O1","name":"High"}]}`
			}
			_, _ = io.WriteString(w, `{"data":{"repository":{"issue":{"id":"I5"},"issueFields":{"nodes":[`+fields+`]}}}}`)
		default:
			asked = append(asked, fmt.Sprintf("issue %v", repo))
			_, _ = io.WriteString(w, `{"data":{"repository":{"issue":{"id":"I6"}}}}`)
		}
	}))
	defer s.Close()
	now := time.Now()
	g := NewRealGitHub(s.URL, func() string { return "t" }, nil)
	g.fields.now = func() time.Time { return now }
	set := func(repo string, number int) {
		t.Helper()
		_, _ = g.SetPriorityField(context.Background(), repo, number, "high")
	}
	set("o/none", 5)
	set("o/none", 6)
	set("o/with", 5)
	set("o/with", 5)
	set("o/with", 6)
	now = now.Add(fieldsFor)
	set("o/none", 5)
	fail = true
	set("o/with", 6)
	fail = false
	set("o/with", 6)
	want := "fields none;fields with;set;set;issue with;set;fields none;fields with;set;fields with;set"
	if got := strings.Join(asked, ";"); got != want {
		t.Errorf("asked\n%s\nwant\n%s", got, want)
	}
}

// #6644: the add gives the issue's labels from its own answer.
func TestAddLabelsGivesTheLabels(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/repos/o/r/issues/5/labels" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, `[{"name":"bug"},{"name":"priority:high"},{"name":"priority:low"}]`)
	}))
	defer s.Close()
	g := NewRealGitHub(s.URL, func() string { return "t" }, nil)
	got, err := g.AddLabels(context.Background(), "o/r", 5, []string{"priority:high"})
	if err != nil || strings.Join(got, ",") != "bug,priority:high,priority:low" {
		t.Fatalf("labels %q, %v", got, err)
	}
}
