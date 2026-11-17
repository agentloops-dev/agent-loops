package agentsd

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/githubmcp"
	"github.com/agentloops-dev/agent-loops/Go/internal/lineage"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// nextRows are a forge/next answer's rows as "number: reason; reason", in order.
func nextRows(t *testing.T, next wire.ForgeNext) []string {
	t.Helper()
	var out []string
	for n, r := range next.Rows {
		if r.Place != int64(n+1) || r.OnHold != onHold(r.Item) {
			t.Errorf("row %d: %+v", n, r)
		}
		out = append(out, fmt.Sprintf("%d: %s", refNumber(r.Item.Node.Ref), strings.Join(r.Reasons, "; ")))
	}
	return out
}

func agentsFolder(t *testing.T, d *Daemon) string {
	t.Helper()
	for _, f := range d.folderPaths() {
		if d.folderRepo(f) == (forgeRepo{"github", "alexec/agents"}) {
			return f
		}
	}
	t.Fatal("no folder on alexec/agents")
	return ""
}

// #5689: the one order. On hold last; then priority high, medium, none, low; then the
// milestone's due date, none last; then the oldest, then the lowest number.
func TestNextCompare(t *testing.T) {
	t.Parallel()

	at := func(s string) *wire.Date {
		return new(wire.DateOf(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, len(s))))
	}
	item := func(n int, edit func(*wire.ForgeItem)) wire.ForgeItem {
		i := wire.ForgeItem{Node: wire.LineageNode{Kind: "issue", Ref: fmt.Sprintf("github:issue:a/b#%d", n)}, State: "open"}
		if edit != nil {
			edit(&i)
		}
		return i
	}
	items := []wire.ForgeItem{
		item(1, func(i *wire.ForgeItem) { i.Priority, i.OnHold = new("high"), new(true) }),
		item(2, func(i *wire.ForgeItem) { i.Priority = new("low") }),
		item(3, nil),
		item(4, func(i *wire.ForgeItem) { i.Priority = new("medium") }),
		item(5, func(i *wire.ForgeItem) { i.Priority, i.MilestoneDue = new("high"), at("xx") }),
		item(6, func(i *wire.ForgeItem) { i.Priority, i.MilestoneDue = new("high"), at("x") }),
		item(7, func(i *wire.ForgeItem) { i.Priority, i.CreatedAt = new("high"), at("xx") }),
		item(8, func(i *wire.ForgeItem) { i.Priority, i.CreatedAt = new("high"), at("x") }),
		item(9, func(i *wire.ForgeItem) { i.Priority = new("high") }),
		item(10, func(i *wire.ForgeItem) { i.OnHold = new(true) }),
	}
	nextOrder(items)
	var got []string
	for _, i := range items {
		got = append(got, fmt.Sprint(refNumber(i.Node.Ref)))
	}
	if strings.Join(got, " ") != "6 5 8 7 9 4 3 2 1 10" {
		t.Fatalf("order: %v", got)
	}
	now := wire.DateOf(time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC))
	due := wire.DateOf(time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC))
	i := wire.ForgeItem{Milestone: new("Work"), MilestoneDue: &due, Priority: new("high"), OnHold: new(true)}
	if r := strings.Join(nextReasons(i, true, now), "; "); r != "on hold; priority high; milestone Work, due 20 Nov; oldest" {
		t.Errorf("reasons: %q", r)
	}
	if r := strings.Join(nextReasons(wire.ForgeItem{Milestone: new("Later")}, false, wire.DateOf(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))), "; "); r != "no priority; milestone Later, no due date" {
		t.Errorf("reasons: %q", r)
	}
	if dueDay(due, wire.DateOf(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))) != "20 Nov 2026" {
		t.Error("another year")
	}
}

// #5689: forge/next of a folder gives its open issues that are ready, in the one order, with
// reasons: not an epic, not assigned, not blocked by an open issue, not labelled on a session
// that is not archived, not fixed by an open pull request; on hold ones come last, marked.
func TestForgeNextOfAFolder(t *testing.T) {
	t.Parallel()

	d, c, gh, _ := forgeDaemon(t)
	folder := agentsFolder(t, d)
	year := time.Now().UTC().Year()
	issue := func(n int, created string, labels ...string) githubmcp.Item {
		i := planIssue(n, "open", labels...)
		i.CreatedAt = created
		return i
	}
	dated := issue(23, "2026-11-01T00:00:00Z", "priority:high")
	dated.Milestone, dated.MilestoneDue = "Work", fmt.Sprintf("%d-11-20T00:00:00Z", year)
	assigned := issue(22, "", "priority:high")
	assigned.Assignees = []string{"alexec"}
	gh.open = []githubmcp.Item{
		issue(20, "2026-01-01T00:00:00Z", "priority:low"),
		issue(21, "2026-01-01T00:00:00Z"),
		assigned, dated,
		issue(25, "2026-10-02T00:00:00Z", "priority:high"),
		issue(24, "2026-10-01T00:00:00Z", "priority:high"),
		issue(26, "", "epic", "priority:high"),
		issue(27, "", "priority:high"),
		issue(28, "", "priority:high", "On Hold"),
		issue(30, "", "priority:medium"),
		issue(31, "2026-09-01T00:00:00Z"),
		issue(32, "", "priority:high"),
	}
	gh.blockers = map[int][]githubmcp.Ref{27: {{Repo: "alexec/agents", Number: 99, Open: true}},
		32: {{Repo: "alexec/agents", Number: 98, Open: false}}}
	seeded(t, d, folder, func(a *wire.Agent) { a.Labels = labelled("#21"); a.State = wire.AgentStateFinished })
	seeded(t, d, folder, func(a *wire.Agent) {
		a.Labels = labelled("#20")
		a.State, a.ArchivedReason = wire.AgentStateArchived, new(wire.AgentArchivedReasonByUser)
	})
	repo := forgeRepo{"github", "alexec/agents"}
	d.lineageDo(func() { d.link(repo.pr(40), repo.issue(30), lineage.RelFixes, lineage.SourcePRBody) })
	d.lineageSettle(2 * time.Second)

	var next wire.ForgeNext
	if err := json.Unmarshal(c.call(t, 1, "forge/next", wire.ForgeNextRequest{Folder: folder}), &next); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"23: priority high; milestone Work, due 20 Nov",
		"24: priority high; no milestone; oldest",
		"25: priority high; no milestone; oldest",
		"32: priority high; no milestone",
		"31: no priority; no milestone",
		"20: priority low; no milestone",
		"28: on hold; priority high; no milestone",
	}
	if got := nextRows(t, next); strings.Join(got, "\n") != strings.Join(want, "\n") || next.ReadAt == 0 || !next.Rows[6].OnHold {
		t.Fatalf("rows:\n%s", strings.Join(got, "\n"))
	}

	for _, bad := range []wire.ForgeNextRequest{{}, {Folder: "/nowhere"}, {Folder: folder, Level: new("story")},
		{Folder: folder, Level: new("epic"), Epic: &wire.LineageNode{Kind: "issue", Ref: "github:issue:alexec/agents#1"}}} {
		if _, err := d.forgeNext(t.Context(), bad); err == nil {
			t.Errorf("%+v: no error", bad)
		}
	}
}

// #5689: level epic gives the open epics in the same order, and an epic gives its children that
// are ready, from its plan's blockers.
func TestForgeNextOfEpicsAndOfAnEpic(t *testing.T) {
	t.Parallel()

	d, c, gh, _ := forgeDaemon(t)
	folder := agentsFolder(t, d)
	gh.epics = []githubmcp.Item{planIssue(40, "open", "epic"), planIssue(41, "open", "epic", "priority:high"),
		planIssue(42, "open", "epic", "on-hold"), planIssue(43, "open", "epic")}
	seeded(t, d, folder, func(a *wire.Agent) { a.Labels = labelled("#43", "lead"); a.State = wire.AgentStateFinished })
	var next wire.ForgeNext
	if err := json.Unmarshal(c.call(t, 1, "forge/next", wire.ForgeNextRequest{Folder: folder, Level: new("epic")}), &next); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(nextRows(t, next), "\n"); got != "41: priority high; no milestone\n40: no priority; no milestone\n42: on hold; no priority; no milestone" {
		t.Fatalf("epics:\n%s", got)
	}

	gh.plan = githubmcp.Plan{Epic: planIssue(5687, "open", "epic"), Children: []githubmcp.PlanChild{
		{Item: planIssue(5689, "open", "task"), BlockedBy: []githubmcp.Ref{{Repo: "alexec/agents", Number: 5688, Open: false}}},
		{Item: planIssue(5688, "closed", "task")},
		{Item: planIssue(5691, "open", "story"), BlockedBy: []githubmcp.Ref{{Repo: "alexec/agents", Number: 5689, Open: true}}},
		{Item: planIssue(5690, "open", "story", "priority:medium")},
	}}
	next = wire.ForgeNext{}
	epic := wire.LineageNode{Kind: "issue", Ref: "github:issue:alexec/agents#5687"}
	if err := json.Unmarshal(c.call(t, 2, "forge/next", wire.ForgeNextRequest{Folder: folder, Epic: &epic}), &next); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(nextRows(t, next), "\n"); got != "5690: priority medium; no milestone\n5689: no priority; no milestone" {
		t.Fatalf("children:\n%s", got)
	}
}
