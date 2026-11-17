package githubmcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
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
			p, err := g.Search(context.Background(), []string{"o/r"}, SearchFilter{}, "", 10)
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
