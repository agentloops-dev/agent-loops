package githubmcp

import (
	"encoding/json"
	"testing"
)

// #7525: an epic's read has its blockers, each with its labels and whether it is open, and says when GitHub has
// more sub-issues or blockers than the read gives.
func TestTimelineEpicBlockersAndMore(t *testing.T) {
	var n timelineNode
	if err := json.Unmarshal([]byte(`{"number":7,"issueState":"OPEN","repository":{"nameWithOwner":"o/r"},
	 "subIssues":{"totalCount":1,"nodes":[{"number":8,"issueState":"OPEN","repository":{"nameWithOwner":"o/r"}}]},
	 "blockedBy":{"totalCount":2,"nodes":[
	  {"number":5,"state":"OPEN","repository":{"nameWithOwner":"o/r"},"labels":{"nodes":[{"name":"epic"},{"name":"on hold"}]}},
	  {"number":6,"state":"CLOSED","repository":{"nameWithOwner":"o/other"},"labels":{"nodes":[]}},
	  {"number":0,"state":"OPEN","repository":{"nameWithOwner":""},"labels":{"nodes":[]}}]}}`), &n); err != nil {
		t.Fatal(err)
	}
	e := timelineEpicOf(n, "o/r")
	if len(e.BlockedBy) != 2 || e.More {
		t.Fatalf("blockers %+v, more %v", e.BlockedBy, e.More)
	}
	if b := e.BlockedBy[0]; b.Repo != "o/r" || b.Number != 5 || !b.Open || len(b.Labels) != 2 || b.Labels[1] != "on hold" {
		t.Errorf("first blocker %+v", b)
	}
	if b := e.BlockedBy[1]; b.Repo != "o/other" || b.Number != 6 || b.Open {
		t.Errorf("second blocker %+v", b)
	}
	n.SubIssues.TotalCount = 101
	if !timelineEpicOf(n, "o/r").More {
		t.Error("more sub-issues than read: More is false")
	}
	n.SubIssues.TotalCount, n.BlockedBy.TotalCount = 1, 51
	if !timelineEpicOf(n, "o/r").More {
		t.Error("more blockers than read: More is false")
	}
}
