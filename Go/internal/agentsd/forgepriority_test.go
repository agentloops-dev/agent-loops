package agentsd

import (
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/githubmcp"
	"github.com/agentloops-dev/agent-loops/Go/internal/jiramcp"
	"github.com/agentloops-dev/agent-loops/Go/internal/linearmcp"
)

func TestForgePriorityFromLabels(t *testing.T) {
	for _, c := range []struct {
		labels []string
		want   string
	}{
		{[]string{"bug"}, ""},
		{[]string{"Priority:HIGH"}, "high"},
		{[]string{"priority:low", "priority:medium"}, "medium"},
		{[]string{"priority:low", "priority:high"}, "high"},
	} {
		got := forgeItemOf(githubmcp.Item{Repo: "a/b", Number: 1, State: "open", Labels: c.labels}, "")
		if deref(got.Priority) != c.want {
			t.Errorf("%v: priority %q, want %q", c.labels, deref(got.Priority), c.want)
		}
	}
}

func TestForgePriorityFromFields(t *testing.T) {
	if got := linearItemOf(linearmcp.Item{Identifier: "ENG-1", Priority: "high", MilestoneDue: "2026-12-01"}, ""); deref(got.Priority) != "high" || got.MilestoneDue == nil {
		t.Errorf("linear: %+v", got)
	}
	if got := jiraItemOf(jiramcp.Item{Key: "P-1", Priority: "low"}, ""); deref(got.Priority) != "low" || got.MilestoneDue != nil {
		t.Errorf("jira: %+v", got)
	}
	if got := forgeItemOf(githubmcp.Item{Repo: "a/b", Number: 2, State: "open", MilestoneDue: "2026-12-01T00:00:00Z"}, ""); got.MilestoneDue == nil {
		t.Error("github: no due date")
	}
}
