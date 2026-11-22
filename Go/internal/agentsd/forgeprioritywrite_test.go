package agentsd

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/jiramcp"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// The fakes' writes of forge/priority and forge/hold (#6091).

func (f *fakeForgeGitHub) SetPriorityField(_ context.Context, repo string, number int, priority string) (bool, error) {
	f.mu.Lock()
	has := f.priorityField
	f.mu.Unlock()
	if has {
		f.note("priority-field %s#%d %q", repo, number, priority)
	}
	return has, nil
}

// AddLabels gives the labels after the add, with an old priority label still on (#6644).
func (f *fakeForgeGitHub) AddLabels(_ context.Context, repo string, number int, add []string) ([]string, error) {
	f.note("add-labels %s#%d +%s", repo, number, strings.Join(add, ","))
	return append(slices.Clone(add), "bug", "Priority:LOW"), nil
}

func (f *fakeForgeLinear) SetPriority(_ context.Context, identifier, priority string) error {
	f.note("priority %s %q", identifier, priority)
	return nil
}

func (f *fakeForgeJira) SetPriority(_ context.Context, key, priority string) error {
	return f.note(fmt.Sprintf("priority %s %q", key, priority))
}

func (f *fakeForgeBitbucket) SetPriority(_ context.Context, repo string, number int, priority string) error {
	f.note(&f.wrote, fmt.Sprintf("priority %s#%d %q", repo, number, priority))
	return nil
}

func (f *fakeForgeBitbucket) SetHold(_ context.Context, repo string, number int, on bool) error {
	f.note(&f.wrote, fmt.Sprintf("hold %s#%d %v", repo, number, on))
	return nil
}

func (f *fakeForgeBoards) SetPriority(_ context.Context, project string, id int, priority string) (bool, error) {
	f.mu.Lock()
	none := f.noPriority
	f.mu.Unlock()
	if none {
		return false, nil
	}
	return true, f.note(fmt.Sprintf("priority %s#%d %q", project, id, priority))
}

// setTo is a forge/priority request's priority: nil for none.
func setTo(p string) *string {
	if p == "" {
		return nil
	}
	return &p
}

// #6091: a priority by the labels adds its own label once and takes off every other priority
// label, in any case; on hold adds the label only where no spelling of it is there, and off
// takes each spelling off.
func TestPlanLabelEdits(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		have        []string
		p           string
		add, remove []string
	}{
		{nil, "high", []string{"priority:high"}, nil},
		{[]string{"bug", "Priority:LOW", "priority:medium"}, "high", []string{"priority:high"}, []string{"Priority:LOW", "priority:medium"}},
		{[]string{"Priority:High", "priority:low"}, "high", nil, []string{"priority:low"}},
		{[]string{"priority:high", "priority:urgent"}, "", nil, []string{"priority:high"}},
		{[]string{"bug"}, "", nil, nil},
	} {
		add, remove := priorityLabelEdit(c.have, c.p)
		if !slices.Equal(add, c.add) || !slices.Equal(remove, c.remove) {
			t.Errorf("%v to %q: +%v -%v, want +%v -%v", c.have, c.p, add, remove, c.add, c.remove)
		}
	}
	if add, _ := holdLabelEdit([]string{"On-Hold"}, true, holdLabel); add != nil {
		t.Errorf("on hold twice: +%v", add)
	}
	if add, _ := holdLabelEdit([]string{"bug"}, true, holdLabel); !slices.Equal(add, []string{"on hold"}) {
		t.Errorf("on hold: +%v", add)
	}
	if _, remove := holdLabelEdit([]string{"On Hold", "bug", "on_hold"}, false, holdLabel); !slices.Equal(remove, []string{"On Hold", "on_hold"}) {
		t.Errorf("off hold: -%v", remove)
	}
}

// #6091: GitHub writes its Priority issue field where the repo has one, else the labels (the
// add first, then the old priority labels its answer has off, with no read: #6644); on
// hold is a label; a pull request, a word that is not a priority and Azure Repos (no issues)
// are refused.
func TestForgePriorityAndHoldOnGitHub(t *testing.T) {
	t.Parallel()

	_, c, gh, _ := forgeDaemon(t)
	c.call(t, 1, "forge/priority", wire.ForgePriorityRequest{Node: ghIssue, Priority: setTo("high")})
	gh.mu.Lock()
	gh.priorityField = true
	gh.mu.Unlock()
	c.call(t, 2, "forge/priority", wire.ForgePriorityRequest{Node: ghIssue, Priority: setTo("low")})
	c.call(t, 3, "forge/priority", wire.ForgePriorityRequest{Node: ghIssue})
	c.call(t, 4, "forge/hold", wire.ForgeHoldRequest{Node: ghIssue, On: true})
	want := []string{"add-labels alexec/agents#7 +priority:high", "labels alexec/agents#7 + -Priority:LOW", `priority-field alexec/agents#7 "low"`, `priority-field alexec/agents#7 ""`,
		"labels alexec/agents#7 +on hold -"}
	if got := gh.writes(); !slices.Equal(got, want) {
		t.Fatalf("writes:\n%q\nwant\n%q", got, want)
	}
	pr := wire.LineageNode{Kind: "pr", Ref: "github:pr:alexec/agents#12"}
	if e := c.callError(t, 5, "forge/hold", wire.ForgeHoldRequest{Node: pr, On: true}); e == nil || !strings.Contains(e.Message, "not an issue") {
		t.Fatalf("a pull request on hold: %+v", e)
	}
	if e := c.callError(t, 6, "forge/priority", wire.ForgePriorityRequest{Node: ghIssue, Priority: setTo("urgent")}); e == nil {
		t.Fatal("urgent was taken")
	}
	if got := (githubForge{}).Caps().wire("github"); got.Priority == nil || !*got.Priority || got.Hold == nil || !*got.Hold {
		t.Fatalf("GitHub's capabilities: %+v", got)
	}
	if got := (azureDevOpsForge{}).Caps().wire("azuredevops"); got.Priority != nil || got.Hold != nil {
		t.Fatalf("Azure Repos says it sets a priority: %+v", got)
	}
}

// #6091: Linear writes its own priority and puts an issue on hold by the label; GitLab by the
// labels for both.
func TestForgePriorityAndHoldOnLinearAndGitLab(t *testing.T) {
	t.Parallel()

	_, c, _, gl, ln, _ := allForgesDaemon(t)
	eng := wire.LineageNode{Kind: "issue", Ref: "linear:issue:eng#ENG-4"}
	c.call(t, 1, "forge/priority", wire.ForgePriorityRequest{Node: eng, Priority: setTo("medium")})
	c.call(t, 2, "forge/hold", wire.ForgeHoldRequest{Node: eng, On: true})
	ln.mu.Lock()
	got := slices.Clone(ln.wrote)
	ln.mu.Unlock()
	if want := []string{`priority ENG-4 "medium"`, "labels ENG-4 +on hold -"}; !slices.Equal(got, want) {
		t.Fatalf("Linear writes %q, want %q", got, want)
	}

	issue := wire.LineageNode{Kind: "issue", Ref: "gitlab:issue:group/app#3"}
	c.call(t, 3, "forge/priority", wire.ForgePriorityRequest{Node: issue, Priority: setTo("low")})
	c.call(t, 4, "forge/hold", wire.ForgeHoldRequest{Node: issue, On: true})
	gl.mu.Lock()
	got = slices.Clone(gl.wrote)
	gl.mu.Unlock()
	if want := []string{"labels Group/App 3 mr=false +priority:low -", "labels Group/App 3 mr=false +on hold -"}; !slices.Equal(got, want) {
		t.Fatalf("GitLab writes %q, want %q", got, want)
	}
}

// #6091: Jira writes its priority field where the project has one, else the labels; on hold
// moves to a status named On hold where the workflow has one, and back to To Do; else the
// label on-hold, as a Jira label has no spaces.
func TestForgePriorityAndHoldOnJira(t *testing.T) {
	t.Parallel()

	_, c, j, _ := jiraDaemon(t)
	page := func(key, status string, field bool, labels []string, transitions ...jiramcp.Transition) jiramcp.ItemDetail {
		return jiramcp.ItemDetail{Item: jiramcp.Item{Key: key, Project: "PROJ", State: "open", Status: status, Title: "T",
			URL: "https://acme.atlassian.net/browse/" + key, PriorityField: field, Labels: labels}, Transitions: transitions}
	}
	j.pages = map[string]jiramcp.ItemDetail{
		"PROJ-1": page("PROJ-1", "To Do", true, nil, jiramcp.Transition{ID: "41", Name: "Pause", Category: "indeterminate", To: "On Hold"}),
		"PROJ-2": page("PROJ-2", "On hold", true, []string{"on-hold"},
			jiramcp.Transition{ID: "41", Name: "Pause", Category: "indeterminate", To: "On Hold"},
			jiramcp.Transition{ID: "11", Name: "Back", Category: "new", To: "To Do"}),
		"PROJ-3": page("PROJ-3", "To Do", false, []string{"Priority:Low"}),
	}
	node := func(key string) wire.LineageNode { return wire.LineageNode{Kind: "issue", Ref: "jira:issue:proj#" + key} }
	c.call(t, 1, "forge/priority", wire.ForgePriorityRequest{Node: node("PROJ-1"), Priority: setTo("high")})
	c.call(t, 2, "forge/priority", wire.ForgePriorityRequest{Node: node("PROJ-3"), Priority: setTo("high")})
	c.call(t, 3, "forge/hold", wire.ForgeHoldRequest{Node: node("PROJ-1"), On: true})
	c.call(t, 4, "forge/hold", wire.ForgeHoldRequest{Node: node("PROJ-2"), On: false})
	c.call(t, 5, "forge/hold", wire.ForgeHoldRequest{Node: node("PROJ-3"), On: true})
	c.call(t, 6, "forge/hold", wire.ForgeHoldRequest{Node: node("PROJ-2"), On: true})
	_, wrote := j.asked()
	want := []string{`priority PROJ-1 "high"`, "labels PROJ-3 +priority:high -Priority:Low", "transition PROJ-1 41",
		"transition PROJ-2 11", "labels PROJ-2 + -on-hold", "labels PROJ-3 +on-hold -"}
	if !slices.Equal(wrote, want) {
		t.Fatalf("wrote:\n%q\nwant\n%q", wrote, want)
	}
}

// #6091: Bitbucket writes its issue's priority and its state on hold; Azure Boards its Priority
// field where the type has one, else the tags, and on hold by the tag.
func TestForgePriorityAndHoldOnBitbucketAndAzureBoards(t *testing.T) {
	t.Parallel()

	_, c, bb, _ := bitbucketDaemon(t)
	issue := wire.LineageNode{Kind: "issue", Ref: "bitbucket:issue:walkalex/app#7"}
	c.call(t, 1, "forge/priority", wire.ForgePriorityRequest{Node: issue, Priority: setTo("low")})
	c.call(t, 2, "forge/hold", wire.ForgeHoldRequest{Node: issue, On: true})
	c.call(t, 3, "forge/hold", wire.ForgeHoldRequest{Node: issue, On: false})
	if want := []string{`priority walkalex/App#7 "low"`, "hold walkalex/App#7 true", "hold walkalex/App#7 false"}; !slices.Equal(bb.wrote, want) {
		t.Fatalf("Bitbucket wrote %q, want %q", bb.wrote, want)
	}

	_, bc, b, _ := boardsDaemon(t)
	bc.call(t, 1, "forge/priority", wire.ForgePriorityRequest{Node: boardsItem, Priority: setTo("high")})
	b.mu.Lock()
	b.noPriority = true
	b.mu.Unlock()
	bc.call(t, 2, "forge/priority", wire.ForgePriorityRequest{Node: boardsItem, Priority: setTo("medium")})
	bc.call(t, 3, "forge/hold", wire.ForgeHoldRequest{Node: boardsItem, On: true})
	_, wrote := b.asked()
	want := []string{`priority acme/web app#12 "high"`, "tags acme/web app#12 +priority:medium -", "tags acme/web app#12 +on hold -"}
	if !slices.Equal(wrote, want) {
		t.Fatalf("Azure Boards wrote %q, want %q", wrote, want)
	}
}
