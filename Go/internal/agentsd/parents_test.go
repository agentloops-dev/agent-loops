package agentsd

import (
	"strings"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// Session parents (#7538, specs/7535-session-parents).

func seededLead(t *testing.T, d *Daemon, cwd, title string) wire.Agent {
	t.Helper()
	return seeded(t, d, cwd, func(a *wire.Agent) {
		a.Title = &title
		a.Labels = []wire.SessionLabel{{Value: leadLabel, Owner: wire.SessionLabelOwnerPerson, AddedAt: now()}}
	})
}

func under(child, parent wire.UUID) map[string]any {
	return map[string]any{"agentID": child, "placement": "under", "parentID": parent}
}

func TestSetParentChecks(t *testing.T) {
	t.Parallel()

	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	dir := t.TempDir()
	a, b := seededLead(t, d, dir, "Lead A"), seededLead(t, d, dir, "Lead B")
	plain := seeded(t, d, dir, nil)
	child := seeded(t, d, dir, nil)
	n := 0
	refused := func(params map[string]any, code int) {
		t.Helper()
		n++
		if e := c.callError(t, n, "agents/setParent", params); e == nil || e.Code != code {
			t.Fatalf("%v: %+v, want %d", params, e, code)
		}
	}
	ok := func(params map[string]any) wire.Agent {
		t.Helper()
		n++
		var got wire.Agent
		mustUnmarshal(t, c.call(t, n, "agents/setParent", params), &got)
		return got
	}
	refused(under(child.ID, plain.ID), wire.FailureParentNotLead)
	refused(under(child.ID, store.NewUUID()), wire.FailureParentNotFound)
	refused(under(a.ID, a.ID), wire.FailureParentCycle)
	got := ok(under(b.ID, a.ID))
	if got.Parent == nil || got.Parent.ID == nil || *got.Parent.ID != a.ID || got.Parent.SetBy != wire.SessionParentSetByPerson {
		t.Fatalf("%+v", got.Parent)
	}
	// A under B, with B under A, is a cycle.
	refused(under(a.ID, b.ID), wire.FailureParentCycle)
	// A stale revision is refused.
	stale := int64(1)
	refused(map[string]any{"agentID": child.ID, "placement": "top", "expectedRevision": stale}, wire.FailureChangedElsewhere)
	// At the top, and back under the starter.
	if got := ok(map[string]any{"agentID": child.ID, "placement": "top"}); got.Parent == nil || got.Parent.ID != nil {
		t.Fatalf("%+v", got.Parent)
	}
	if got := ok(map[string]any{"agentID": child.ID, "placement": "starter"}); got.Parent != nil {
		t.Fatalf("%+v", got.Parent)
	}
	// An archived lead is not a parent.
	d.change(a.ID, func(x *wire.Agent) { r := wire.AgentArchivedReasonByUser; x.State, x.ArchivedReason = wire.AgentStateArchived, &r })
	refused(under(child.ID, a.ID), wire.FailureParentNotFound)
}

func TestSetParentDepthAndChildLimits(t *testing.T) {
	t.Parallel()

	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	dir := t.TempDir()
	// A chain of 6 levels: the most.
	chain := []wire.Agent{seededLead(t, d, dir, "L0")}
	for i := 1; i < parentLevelsMost; i++ {
		l := seededLead(t, d, dir, "L")
		if _, err := d.setParent(wire.SetParentRequest{AgentID: l.ID, Placement: wire.SessionPlacementUnder, ParentID: &chain[i-1].ID}, wire.SessionParentSetByPerson); err != nil {
			t.Fatalf("level %d: %v", i, err)
		}
		chain = append(chain, l)
	}
	leaf := seeded(t, d, dir, nil)
	last := chain[len(chain)-1].ID
	if _, err := d.setParent(wire.SetParentRequest{AgentID: leaf.ID, Placement: wire.SessionPlacementUnder, ParentID: &last}, wire.SessionParentSetByPerson); err == nil || err.(*Error).Code != wire.FailureParentTooDeep {
		t.Fatalf("7 levels: %v", err)
	}
	// A lead moves with its subtree: L1 (with 4 levels below) under a new top lead is too deep
	// only when the sum is over 6.
	top := seededLead(t, d, dir, "Top")
	if _, err := d.setParent(wire.SetParentRequest{AgentID: chain[1].ID, Placement: wire.SessionPlacementUnder, ParentID: &top.ID}, wire.SessionParentSetByPerson); err != nil {
		t.Fatalf("subtree of 5 under a top lead: %v", err)
	}
	if p, _ := d.Store.ParentOf(chain[5].ID); p != chain[4].ID {
		t.Fatalf("the subtree did not go with its lead: %s", p)
	}
	// 100 live children, the most.
	full := seededLead(t, d, dir, "Full")
	for range parentChildrenMost {
		seeded(t, d, dir, func(a *wire.Agent) {
			id := full.ID
			a.Parent = &wire.SessionParent{ID: &id, SetAt: now(), SetBy: wire.SessionParentSetByPerson}
		})
	}
	one := seeded(t, d, dir, nil)
	if _, err := d.setParent(wire.SetParentRequest{AgentID: one.ID, Placement: wire.SessionPlacementUnder, ParentID: &full.ID}, wire.SessionParentSetByPerson); err == nil || err.(*Error).Code != wire.FailureParentFull {
		t.Fatalf("101st: %v", err)
	}
	// A new session under it is refused the same way.
	if _, err := d.start(wire.StartRequest{RuntimeID: "claude", Cwd: store.FolderURL(dir), Prompt: "hi", Attachments: []wire.Attachment{},
		AdditionalDirectories: []wire.URL{}, McpServers: []wire.MCPServer{}, Labels: []string{}, ParentID: &full.ID}); err == nil || err.(*Error).Code != wire.FailureParentFull {
		t.Fatalf("start under a full lead: %v", err)
	}
}

func pendingOf(d *Daemon, id wire.UUID) []wire.ParentNotice {
	a := d.Store.Agent(id)
	if a == nil || a.ParentNotices == nil {
		return nil
	}
	return a.ParentNotices.Pending
}

func TestParentNoticesCoalesce(t *testing.T) {
	t.Parallel()

	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	dir := t.TempDir()
	a, b := seededLead(t, d, dir, "Lead A"), seededLead(t, d, dir, "Lead B")
	child := seeded(t, d, dir, nil)
	move := func(p *wire.UUID) {
		t.Helper()
		r := wire.SetParentRequest{AgentID: child.ID, Placement: wire.SessionPlacementStarter}
		if p != nil {
			r.Placement, r.ParentID = wire.SessionPlacementUnder, p
		}
		if _, err := d.setParent(r, wire.SessionParentSetByPerson); err != nil {
			t.Fatal(err)
		}
	}
	move(&a.ID)
	move(&b.ID)
	// The child is told once, of B; A's joined and left cancel; B is told it joined.
	if p := pendingOf(d, child.ID); len(p) != 1 || p[0].Change != wire.ParentNoticeChangeParent || p[0].About != b.ID || p[0].Was != nil {
		t.Fatalf("child %+v", p)
	}
	if p := pendingOf(d, a.ID); len(p) != 0 {
		t.Fatalf("A %+v", p)
	}
	if p := pendingOf(d, b.ID); len(p) != 1 || p[0].Change != wire.ParentNoticeChangeJoined {
		t.Fatalf("B %+v", p)
	}
	// Back where it began: nobody is told anything.
	move(nil)
	if p := pendingOf(d, child.ID); len(p) != 0 {
		t.Fatalf("child after the drag back %+v", p)
	}
	if p := pendingOf(d, b.ID); len(p) != 0 {
		t.Fatalf("B after the drag back %+v", p)
	}
	// The same placement again changes nothing.
	move(&a.ID)
	rev := d.Store.Agent(child.ID).Revision
	move(&a.ID)
	if got := d.Store.Agent(child.ID).Revision; *got != *rev {
		t.Fatal("a no-op wrote the record")
	}
}

func TestParentNoticesBound(t *testing.T) {
	t.Parallel()

	var a wire.Agent
	for range parentNoticesMost + 3 {
		addLeadNotice(&a, store.NewUUID(), wire.ParentNoticeChangeJoined, now())
	}
	if n := a.ParentNotices; len(n.Pending) != parentNoticesMost || n.More == nil || *n.More != 3 {
		t.Fatalf("%d %v", len(n.Pending), n.More)
	}
}

// Each notice goes with the recipient's next turn, once.
func TestParentNoticeGoesWithTheNextTurnOnce(t *testing.T) {
	t.Parallel()

	fakeRuntimes(t)
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	dir := t.TempDir()
	lead := seededLead(t, d, dir, "The lead")
	child := seeded(t, d, dir, nil)
	if _, err := d.setParent(wire.SetParentRequest{AgentID: child.ID, Placement: wire.SessionPlacementUnder, ParentID: &lead.ID}, wire.SessionParentSetByPerson); err != nil {
		t.Fatal(err)
	}
	c.call(t, 1, "agents/prompt", map[string]any{"agentID": child.ID, "text": "say your brief", "from": "person"})
	waitFor(t, "the child's turn", func() bool { return strings.Contains(agentMessages(d, child.ID), "Before:") })
	if said := agentMessages(d, child.ID); !strings.Contains(said, "You now work under “The lead”") {
		t.Fatalf("not told: %s", said)
	}
	waitFor(t, "the notice cleared", func() bool { return d.Store.Agent(child.ID).ParentNotices == nil })
	c.call(t, 2, "agents/prompt", map[string]any{"agentID": child.ID, "text": "say your brief", "from": "person"})
	waitFor(t, "the second turn", func() bool { return strings.Count(agentMessages(d, child.ID), "Before:") == 2 })
	if said := agentMessages(d, child.ID); strings.Count(said, "You now work under") != 1 {
		t.Fatalf("told twice: %s", said)
	}
	// The lead hears of it on its own next turn.
	c.call(t, 3, "agents/prompt", map[string]any{"agentID": lead.ID, "text": "say your brief", "from": "person"})
	waitFor(t, "the lead's turn", func() bool { return strings.Contains(agentMessages(d, lead.ID), "Before:") })
	if said := agentMessages(d, lead.ID); !strings.Contains(said, "now works under you") {
		t.Fatalf("lead not told: %s", said)
	}
}

// A restart keeps what is pending, clears what a runtime answered and tells again what a
// cut-off turn took with no answer.
func TestParentNoticesAcrossARestart(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	d, _ := start(t, root)
	dir := t.TempDir()
	lead := seededLead(t, d, dir, "The lead")
	pending := seeded(t, d, dir, nil)
	answered := seeded(t, d, dir, nil)
	cutOff := seeded(t, d, dir, nil)
	for _, id := range []wire.UUID{pending.ID, answered.ID, cutOff.ID} {
		if _, err := d.setParent(wire.SetParentRequest{AgentID: id, Placement: wire.SessionPlacementUnder, ParentID: &lead.ID}, wire.SessionParentSetByPerson); err != nil {
			t.Fatal(err)
		}
	}
	// Two turns took their notices; one runtime answered before the host went.
	for _, id := range []wire.UUID{answered.ID, cutOff.ID} {
		entry := store.NewUUID()
		d.change(id, func(a *wire.Agent) { takeNotices(a, entry) })
		d.recordEntry(id, entry, wire.TranscriptEntryKind{UserMessage: &wire.TranscriptEntryKindUserMessage{Value: "go"}}, nil)
		if id == answered.ID {
			d.record(id, wire.TranscriptEntryKind{AgentMessage: &wire.TranscriptEntryKindAgentMessage{Text: "ok"}})
		}
	}
	d.Shutdown()
	d2, _ := start(t, root)
	defer d2.Shutdown()
	if p := pendingOf(d2, pending.ID); len(p) != 1 || p[0].Change != wire.ParentNoticeChangeParent {
		t.Fatalf("pending lost: %+v", p)
	}
	if n := d2.Store.Agent(answered.ID).ParentNotices; n != nil {
		t.Fatalf("answered kept: %+v", n)
	}
	n := d2.Store.Agent(cutOff.ID).ParentNotices
	if n == nil || len(n.Sent) != 1 || n.SentWith != nil || n.Sent[0].Again == nil || !*n.Sent[0].Again {
		t.Fatalf("cut off: %+v", n)
	}
	// Its next turn tells it again, saying so.
	var told []wire.ParentNotice
	var more int64
	d2.change(cutOff.ID, func(a *wire.Agent) { told, more = takeNotices(a, store.NewUUID()) })
	if text := d2.noticeText(cutOff.ID, told, more); !strings.Contains(text, "(Said again after a restart.)") {
		t.Fatalf("%q", text)
	}
}

// A deleted lead's children go back under their starters, and are told.
func TestDeletedLeadLetsGoOfItsChildren(t *testing.T) {
	t.Parallel()

	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	dir := t.TempDir()
	lead := seededLead(t, d, dir, "The lead")
	child := seeded(t, d, dir, nil)
	if _, err := d.setParent(wire.SetParentRequest{AgentID: child.ID, Placement: wire.SessionPlacementUnder, ParentID: &lead.ID}, wire.SessionParentSetByPerson); err != nil {
		t.Fatal(err)
	}
	d.change(child.ID, func(a *wire.Agent) { a.ParentNotices = nil })
	d.change(lead.ID, func(a *wire.Agent) { r := wire.AgentArchivedReasonByUser; a.State, a.ArchivedReason = wire.AgentStateArchived, &r })
	if err := d.delete(lead.ID); err != nil {
		t.Fatal(err)
	}
	got := d.Store.Agent(child.ID)
	if got.Parent != nil {
		t.Fatalf("%+v", got.Parent)
	}
	if p := pendingOf(d, child.ID); len(p) != 1 || p[0].Change != wire.ParentNoticeChangeNoParent {
		t.Fatalf("%+v", p)
	}
}

// A session started under a lead knows it from its brief; the lead is told it joined.
func TestStartUnderALead(t *testing.T) {
	t.Parallel()

	fakeRuntimes(t)
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	dir := t.TempDir()
	lead := seededLead(t, d, dir, "The lead")
	var id wire.UUID
	mustUnmarshal(t, c.call(t, 1, "agents/start", map[string]any{"runtimeID": "claude", "cwd": "file://" + dir, "prompt": "say your brief", "parentID": lead.ID}), &id)
	waitFor(t, "the turn", func() bool { return strings.Contains(agentMessages(d, id), "Before:") })
	if said := agentMessages(d, id); !strings.Contains(said, "you work under “The lead”") {
		t.Fatalf("%s", said)
	}
	if p := pendingOf(d, lead.ID); len(p) != 1 || p[0].About != id || p[0].Change != wire.ParentNoticeChangeJoined {
		t.Fatalf("%+v", p)
	}
}
