package controlplane

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// changesPlane is a plane with two machines' lineage for reports/changes' tests: the
// owner's issue #1 with its PR #2, and PR #5 that fixes #4; Bea's issue #6.
type changesPlane struct {
	p     *plane
	admin *controllink.Session
	bea   *person
	beas  *fakeHost
	now   time.Time
}

func newChangesPlane(t *testing.T) changesPlane {
	t.Helper()
	p := startPlane(t, store.NewMemory(), controllink.NewPrivateKey())
	owner := p.host("OWNERS-MAC")
	_, admin := p.client("Owner's window")
	bea := p.invite(admin, "Bea", wire.UserRoleMember)
	c := changesPlane{p: p, admin: admin, bea: bea, beas: p.machine(bea, "BEAS-MAC"), now: time.Now()}
	c.send(t, owner.member.Host, c.node("issue", "github:issue:o/r#1", 1), c.node("pr", "github:issue:o/r#2", 4, "github:issue:o/r#1"),
		c.node("pr", "github:issue:o/r#5", 2, "github:issue:o/r#4"))
	c.send(t, c.beas.member.Host, c.node("issue", "github:issue:o/r#6", 1))
	return c
}

func (c changesPlane) node(kind, ref string, usd float64, fixes ...string) wire.LineageCostNode {
	return wire.LineageCostNode{Node: wire.LineageNode{Kind: kind, Ref: ref}, Fixes: fixes,
		Banked: wire.LineageCost{Cost: map[string]float64{"USD": usd}, Turns: 1}, LastAt: wire.DateOf(c.now)}
}

func (c changesPlane) send(t *testing.T, host string, work ...wire.LineageCostNode) {
	t.Helper()
	params, _ := json.Marshal(wire.UsageRollup{Hours: []wire.UsageRollupPeriod{}, Days: []wire.UsageRollupPeriod{}, Work: work})
	if _, err := c.p.s.methods.HostSaid(host, "usage/rollup", params); err != nil {
		t.Fatal(err)
	}
}

func (c changesPlane) changes(t *testing.T, s *controllink.Session, q wire.ReportChangesQuery) wire.ReportChanges {
	t.Helper()
	var v wire.ReportChanges
	if err := json.Unmarshal(call(t, s, "", "reports/changes", q), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func changedRefs(v wire.ReportChanges) []string {
	out := []string{}
	for _, n := range v.Nodes {
		out = append(out, n.Node.Ref)
	}
	return out
}

var changesOrg = &wire.ReportScope{Kind: wire.ReportScopeKindOrg}

// reports/changes (#6350): no cursor sends every row; the same cursor with no change sends
// none and merges no lineage; a change on one machine sends only the rows it touches.
func TestReportsChangesSendsOnlyWhatChanged(t *testing.T) {
	t.Parallel()
	c := newChangesPlane(t)
	org := changesOrg
	all := c.changes(t, c.admin, wire.ReportChangesQuery{Kind: "issue", Scope: org})
	if got := changedRefs(all); !all.Reset || all.Cursor == nil || all.NextPageCursor != nil ||
		len(got) != 3 || got[0] != "github:issue:o/r#1" || got[1] != "github:issue:o/r#4" || got[2] != "github:issue:o/r#6" ||
		all.Nodes[0].Cost.Total.Cost["USD"] != 5 {
		t.Fatalf("%+v", all)
	}

	// Nothing changed: no rows, the same cursor, and no read or merge of the lineage.
	merges := c.p.s.lineageMerges.Load()
	same := c.changes(t, c.admin, wire.ReportChangesQuery{Kind: "issue", Scope: org, Since: all.Cursor})
	if same.Reset || len(same.Nodes) != 0 || same.Cursor == nil || *same.Cursor != *all.Cursor {
		t.Fatalf("%+v", same)
	}
	if got := c.p.s.lineageMerges.Load(); got != merges {
		t.Fatalf("a read with no change merged the lineage %d times", got-merges)
	}

	// Bea's machine banks a PR that fixes #1: #1 changes (and Bea's own #6, which its object
	// holds), and the owner's #4 does not.
	c.send(t, c.beas.member.Host, c.node("issue", "github:issue:o/r#6", 1), c.node("pr", "github:issue:o/r#7", 3, "github:issue:o/r#1"))
	moved := c.changes(t, c.admin, wire.ReportChangesQuery{Kind: "issue", Scope: org, Since: all.Cursor})
	got := changedRefs(moved)
	if moved.Reset || moved.Cursor == nil || *moved.Cursor == *all.Cursor || len(got) != 2 ||
		got[0] != "github:issue:o/r#1" || got[1] != "github:issue:o/r#6" || moved.Nodes[0].Cost.Total.Cost["USD"] != 8 {
		t.Fatalf("%+v", moved)
	}
	// A cursor of another question (kind) is a reset, not an error.
	if other := c.changes(t, c.admin, wire.ReportChangesQuery{Kind: "pr", Scope: org, Since: moved.Cursor}); !other.Reset || len(other.Nodes) != 3 {
		t.Fatalf("%+v", other)
	}
}

// Pages of one: each row once, by ref; only the last page carries the cursor.
func TestReportsChangesPagesByRef(t *testing.T) {
	t.Parallel()
	c := newChangesPlane(t)
	var paged []string
	q := wire.ReportChangesQuery{Kind: "issue", Scope: changesOrg, Limit: new(int64(1))}
	for range 5 {
		page := c.changes(t, c.admin, q)
		paged = append(paged, changedRefs(page)...)
		if !page.Reset || (page.NextPageCursor == nil) == (page.Cursor == nil) {
			t.Fatalf("%+v", page)
		}
		if page.NextPageCursor == nil {
			break
		}
		q.PageCursor = page.NextPageCursor
	}
	if len(paged) != 3 || paged[0] != "github:issue:o/r#1" || paged[2] != "github:issue:o/r#6" {
		t.Fatalf("%v", paged)
	}
	if none := c.changes(t, c.admin, wire.ReportChangesQuery{Kind: "issue", Scope: changesOrg, Repo: new("github:repo:#o/other")}); len(none.Nodes) != 0 {
		t.Fatalf("%+v", none)
	}
}

// A member sees their own machine only, and a bad question is refused.
func TestReportsChangesScopeAndRefusals(t *testing.T) {
	t.Parallel()
	c := newChangesPlane(t)
	if got := changedRefs(c.changes(t, c.bea.window, wire.ReportChangesQuery{Kind: "issue"})); len(got) != 1 || got[0] != "github:issue:o/r#6" {
		t.Fatalf("%v", got)
	}
	if e := callError(t, c.bea.window, "", "reports/changes", wire.ReportChangesQuery{Kind: "issue", Scope: changesOrg}); e == "" {
		t.Fatal("a member read the organisation")
	}
	for _, bad := range []wire.ReportChangesQuery{
		{Kind: "commit"},
		{Kind: "issue", Limit: new(int64(101))},
		{Kind: "issue", Limit: new(int64(0))},
		{Kind: "issue", Since: new("not a cursor")},
		{Kind: "issue", PageCursor: new("not a cursor")},
	} {
		if e := callError(t, c.admin, "", "reports/changes", bad); e == "" {
			t.Errorf("%+v: no error", bad)
		}
	}
}
