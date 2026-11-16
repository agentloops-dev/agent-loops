package agentsd

import (
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/azureboardsmcp"
	"github.com/agentloops-dev/agent-loops/Go/internal/bitbucketmcp"
	"github.com/agentloops-dev/agent-loops/Go/internal/forgejomcp"
	"github.com/agentloops-dev/agent-loops/Go/internal/githubmcp"
	"github.com/agentloops-dev/agent-loops/Go/internal/gitlabmcp"
	"github.com/agentloops-dev/agent-loops/Go/internal/jiramcp"
	"github.com/agentloops-dev/agent-loops/Go/internal/linearmcp"
	"github.com/agentloops-dev/agent-loops/Go/wire"
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

// #5747: each forge's own priority and hold, and the labels only where it has none of its own.
func TestForgePriorityAndHoldFromEachForge(t *testing.T) {
	for _, c := range []struct {
		name     string
		item     wire.ForgeItem
		priority string
		hold     bool
	}{
		{"github labels", forgeItemOf(githubmcp.Item{Repo: "a/b", Number: 1, State: "open", Labels: []string{"priority:high", "On-Hold"}}, ""), "high", true},
		{"github field", forgeItemOf(githubmcp.Item{Repo: "a/b", Number: 1, State: "open", Labels: []string{"priority:high"}, Priority: "low", PriorityField: true}, ""), "low", false},
		{"github field empty", forgeItemOf(githubmcp.Item{Repo: "a/b", Number: 1, State: "open", Labels: []string{"priority:high"}, PriorityField: true}, ""), "", false},
		{"github pull", forgeItemOf(githubmcp.Item{Repo: "a/b", Number: 2, PR: true, State: "open", Labels: []string{"priority:low", "on hold"}}, ""), "low", true},
		{"gitlab", gitlabItemOf(gitlabmcp.Item{Project: "a/b", Number: 1, State: "open", Labels: []string{"Priority:Medium", "ON HOLD"}}, ""), "medium", true},
		{"forgejo", forgejoItemOf(forgejomcp.Item{Repo: "a/b", Number: 1, State: "open", Labels: []string{"priority:low", "on_hold"}}, ""), "low", true},
		{"linear none", linearItemOf(linearmcp.Item{Identifier: "ENG-1", Labels: []string{"priority:high", "on hold"}}, ""), "", true},
		{"jira field", jiraItemOf(jiramcp.Item{Key: "P-1", Labels: []string{"priority:low"}, Priority: "high", PriorityField: true}, ""), "high", false},
		{"jira no field", jiraItemOf(jiramcp.Item{Key: "P-1", Labels: []string{"priority:low"}}, ""), "low", false},
		{"jira status", jiraItemOf(jiramcp.Item{Key: "P-1", Status: "On Hold", PriorityField: true}, ""), "", true},
		{"bitbucket", bitbucketItemOf(bitbucketmcp.Item{Repo: "a/b", Number: 1, State: "open", Labels: []string{"priority:high", "on hold"}, Priority: "low", OnHold: true}, ""), "low", true},
		{"bitbucket default", bitbucketItemOf(bitbucketmcp.Item{Repo: "a/b", Number: 1, State: "open", Labels: []string{"priority:high", "on hold"}}, ""), "", false},
		{"azure field", boardsItemOf(azureboardsmcp.Item{Project: "o/p", ID: 1, State: "open", Labels: []string{"priority:low", "On Hold"}, Priority: "high", PriorityField: true}, ""), "high", true},
		{"azure no field", boardsItemOf(azureboardsmcp.Item{Project: "o/p", ID: 1, State: "open", Labels: []string{"priority:low"}}, ""), "low", false},
	} {
		if deref(c.item.Priority) != c.priority || (c.item.OnHold != nil && *c.item.OnHold) != c.hold {
			t.Errorf("%s: priority %q hold %v, want %q %v", c.name, deref(c.item.Priority), c.item.OnHold != nil, c.priority, c.hold)
		}
		if c.item.OnHold != nil && !*c.item.OnHold {
			t.Errorf("%s: onHold false is set; absent is false", c.name)
		}
	}
}
