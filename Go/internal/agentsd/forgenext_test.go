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

// #5689, #7034: the one order. On hold last; then priority high, medium, none, low; then size
// XS, S, M, L, XL, none last; then the oldest, then the lowest number. A milestone's due date
// changes no place.
func TestNextCompare(t *testing.T) {
	t.Parallel()

	at := func(s string) *wire.Date {
		return new(wire.DateOf(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, len(s))))
	}
	sized := func(sizes ...string) []wire.ForgeLabel {
		var out []wire.ForgeLabel
		for _, s := range sizes {
			out = append(out, wire.ForgeLabel{Name: s})
		}
		return out
	}
	item := func(n int, edit func(*wire.ForgeItem)) wire.ForgeItem {
		i := wire.ForgeItem{Node: wire.LineageNode{Kind: "issue", Ref: fmt.Sprintf("github:issue:a/b#%d", n)}, State: "open"}
		if edit != nil {
			edit(&i)
		}
		return i
	}
	items := []wire.ForgeItem{
		item(1, func(i *wire.ForgeItem) { i.Priority, i.OnHold, i.Labels = new("high"), new(true), sized("size:XS") }),
		item(2, func(i *wire.ForgeItem) { i.Priority, i.Labels = new("low"), sized("size:XS") }),
		item(3, func(i *wire.ForgeItem) { i.Labels = sized("size:XL") }),
		item(4, func(i *wire.ForgeItem) { i.Priority = new("medium") }),
		item(5, func(i *wire.ForgeItem) { i.Priority, i.Labels = new("high"), sized("Size:m") }),
		item(6, func(i *wire.ForgeItem) { i.Priority, i.Labels = new("high"), sized("size:S", "size:L") }),
		item(7, func(i *wire.ForgeItem) { i.Priority, i.CreatedAt, i.Labels = new("high"), at("xx"), sized("size:XS") }),
		item(8, func(i *wire.ForgeItem) { i.Priority, i.CreatedAt, i.Labels = new("high"), at("x"), sized("size:XS") }),
		item(9, func(i *wire.ForgeItem) { i.Priority, i.MilestoneDue = new("high"), at("") }),
		item(10, func(i *wire.ForgeItem) { i.Priority, i.Labels = new("high"), sized("size: S ") }),
		item(11, func(i *wire.ForgeItem) { i.OnHold = new(true) }),
		item(12, func(i *wire.ForgeItem) { i.Labels = sized("size:XS") }),
	}
	nextOrder(items)
	var got []string
	for _, i := range items {
		got = append(got, fmt.Sprint(refNumber(i.Node.Ref)))
	}
	if strings.Join(got, " ") != "8 7 10 5 6 9 4 12 3 2 1 11" {
		t.Fatalf("order: %v", got)
	}
	due := wire.DateOf(time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC))
	i := wire.ForgeItem{Milestone: new("Work"), MilestoneDue: &due, Priority: new("high"), OnHold: new(true),
		Labels: sized("size:S")}
	if r := strings.Join(nextReasons(i, true), "; "); r != "on hold; priority high; size S; oldest" {
		t.Errorf("reasons: %q", r)
	}
	if r := strings.Join(nextReasons(wire.ForgeItem{Milestone: new("Later"), Labels: sized("size:huge")}, false), "; "); r != "no priority; no size" {
		t.Errorf("reasons: %q", r)
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
	small := issue(33, "2026-11-02T00:00:00Z", "priority:high", "size:S")
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
		small,
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
		"33: priority high; size S",
		"24: priority high; no size; oldest",
		"25: priority high; no size; oldest",
		"23: priority high; no size; oldest",
		"32: priority high; no size",
		"31: no priority; no size",
		"20: priority low; no size",
		"28: on hold; priority high; no size",
	}
	if got := nextRows(t, next); strings.Join(got, "\n") != strings.Join(want, "\n") || next.ReadAt == 0 || !next.Rows[7].OnHold {
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
	if got := strings.Join(nextRows(t, next), "\n"); got != "41: priority high; no size\n40: no priority; no size\n42: on hold; no priority; no size" {
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
	if got := strings.Join(nextRows(t, next), "\n"); got != "5690: priority medium; no size\n5689: no priority; no size" {
		t.Fatalf("children:\n%s", got)
	}
}

// #7035: an epic on hold, at any depth, puts the issues and epics under it on hold ("epic on
// hold"), and an epic with an open blocker, at any depth, leaves them out: in the folder, at level
// epic and in one epic's children. The epics and their blockers are read once.
func TestForgeNextFollowsTheEpicsAbove(t *testing.T) {
	t.Parallel()

	d, c, gh, _ := forgeDaemon(t)
	folder := agentsFolder(t, d)
	under := func(i githubmcp.Item, parent int) githubmcp.Item {
		i.ParentRepo, i.ParentNumber = "alexec/agents", parent
		return i
	}
	gh.epics = []githubmcp.Item{
		planIssue(50, "open", "epic", "on hold"),
		under(planIssue(51, "open", "epic", "priority:low"), 50),
		planIssue(52, "open", "epic"),
		planIssue(53, "open", "epic"),
		under(planIssue(54, "open", "epic"), 52),
	}
	gh.open = []githubmcp.Item{
		under(planIssue(60, "open", "priority:high"), 51),
		under(planIssue(61, "open", "priority:high"), 54),
		under(planIssue(62, "open", "priority:medium"), 53),
		under(planIssue(63, "open", "priority:high"), 52),
		planIssue(64, "open"),
		under(planIssue(65, "open", "on hold"), 50),
	}
	gh.blockers = map[int][]githubmcp.Ref{52: {{Repo: "alexec/agents", Number: 99, Open: true}}}
	call := func(id int, r wire.ForgeNextRequest) string {
		t.Helper()
		var next wire.ForgeNext
		if err := json.Unmarshal(c.call(t, id, "forge/next", r), &next); err != nil {
			t.Fatal(err)
		}
		return strings.Join(nextRows(t, next), "\n")
	}

	if got := call(1, wire.ForgeNextRequest{Folder: folder}); got != strings.Join([]string{
		"62: priority medium; no size",
		"64: no priority; no size",
		"60: epic on hold; priority high; no size",
		"65: on hold; no priority; no size",
	}, "\n") {
		t.Fatalf("issues:\n%s", got)
	}
	if got := call(2, wire.ForgeNextRequest{Folder: folder, Level: new("epic")}); got != strings.Join([]string{
		"53: no priority; no size",
		"50: on hold; no priority; no size",
		"51: epic on hold; priority low; no size",
	}, "\n") {
		t.Fatalf("epics:\n%s", got)
	}
	gh.mu.Lock()
	asked := len(gh.blocked)
	gh.mu.Unlock()
	if asked != 3 { // the epics, then the issues; the epics once more at level epic
		t.Errorf("blockers read %d times: %v", asked, gh.blocked)
	}

	gh.plan = githubmcp.Plan{Epic: under(planIssue(51, "open", "epic"), 50), Children: []githubmcp.PlanChild{{Item: planIssue(70, "open", "task")}}}
	if got := call(3, wire.ForgeNextRequest{Folder: folder, Epic: &wire.LineageNode{Kind: "issue", Ref: "github:issue:alexec/agents#51"}}); got != "70: epic on hold; no priority; no size" {
		t.Fatalf("children of 51:\n%s", got)
	}
	gh.plan = githubmcp.Plan{Epic: under(planIssue(54, "open", "epic"), 52), Children: []githubmcp.PlanChild{{Item: planIssue(71, "open", "task")}}}
	if got := call(4, wire.ForgeNextRequest{Folder: folder, Epic: &wire.LineageNode{Kind: "issue", Ref: "github:issue:alexec/agents#54"}}); got != "" {
		t.Fatalf("children of 54:\n%s", got)
	}
}
