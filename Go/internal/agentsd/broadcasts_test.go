package agentsd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// agent: labelled reads to: and deliver: (#6954). The mode stays new on the wire, with to:
// beside it; a file without to:, or with a to: it cannot read, says why.
func TestLabelledWorkflowFile(t *testing.T) {
	t.Parallel()

	good := parseWorkflow("---\nagent: labelled\nto:\n  labels: [epic-6951, lane]\n  not-labels: lead\n  runtime: claude\n  account: default\n"+
		"deliver: steer\n---\nStop pushing.\n", "stop", "/tmp/f")
	if good.Problem != nil || good.Mode != wire.WorkflowModeNew || good.To == nil || good.Deliver == nil || *good.Deliver != wire.WorkflowDeliverSteer {
		t.Fatalf("%+v", good)
	}
	if strings.Join(good.To.Labels, ",") != "epic-6951,lane" || strings.Join(good.To.NotLabels, ",") != "lead" ||
		*good.To.Runtime != "claude" || *good.To.Account != "default" || len(good.UnknownFields) != 0 {
		t.Fatalf("%+v", *good.To)
	}
	if plain := parseWorkflow("---\nagent: labelled\nto:\n  labels: x\n---\nHi.\n", "p", "/tmp/f"); plain.Problem != nil || plain.Deliver != nil {
		t.Fatalf("%+v", plain)
	}
	for _, c := range []struct{ file, says string }{
		{"---\nagent: labelled\n---\nHi.\n", "needs `to:`"},
		{"---\nagent: labelled\nto: x\n---\nHi.\n", "must have `labels:`"},
		{"---\nagent: labelled\nto:\n  runtime: claude\n---\nHi.\n", "needs `labels:`"},
		{"---\nagent: labelled\nto:\n  labels: x\n  folder: y\n---\nHi.\n", "does not know `folder:`"},
		{"---\nagent: labelled\nto:\n  labels: x\ndeliver: shout\n---\nHi.\n", "next-turn, steer or wake"},
		{"---\nagent: new\nto:\n  labels: x\n---\nHi.\n", "only for `agent: labelled`"},
	} {
		w := parseWorkflow(c.file, "w", "/tmp/f")
		if w.Problem == nil || w.Problem.Unreadable == nil || !strings.Contains(w.Problem.Unreadable.Value, c.says) {
			t.Errorf("%q: %+v", c.file, w.Problem)
		}
	}
}

// to: matches a session by its folder, every label, none of not-labels, the runtime that ran
// it, and its account, with default for the runtime's own sign-in (#6952).
func TestBroadcastMatches(t *testing.T) {
	t.Parallel()

	claude, profile := "claude", "work"
	def := "default"
	labelled := func(state wire.AgentState, account *string, labels ...string) *wire.Agent {
		a := &wire.Agent{RuntimeID: "claude", Cwd: store.FolderURL("/tmp/f"), State: state, AccountID: account}
		for _, l := range labels {
			a.Labels = append(a.Labels, wire.SessionLabel{Value: l})
		}
		return a
	}
	to := wire.WorkflowTo{Labels: []string{"#6951"}, NotLabels: []string{"lead"}}
	for _, c := range []struct {
		name string
		to   wire.WorkflowTo
		a    *wire.Agent
		want bool
	}{
		{"its label", to, labelled(wire.AgentStateFinished, nil, "#6951"), true},
		{"no label", to, labelled(wire.AgentStateFinished, nil, "#7000"), false},
		{"a not-label", to, labelled(wire.AgentStateFinished, nil, "#6951", "lead"), false},
		{"archived", to, labelled(wire.AgentStateArchived, nil, "#6951"), false},
		{"its runtime", wire.WorkflowTo{Labels: []string{"#6951"}, Runtime: &claude}, labelled(wire.AgentStateRunning, nil, "#6951"), true},
		{"default sign-in", wire.WorkflowTo{Labels: []string{"#6951"}, Account: &def}, labelled(wire.AgentStateRunning, nil, "#6951"), true},
		{"not default", wire.WorkflowTo{Labels: []string{"#6951"}, Account: &def}, labelled(wire.AgentStateRunning, &profile, "#6951"), false},
		{"its profile", wire.WorkflowTo{Labels: []string{"#6951"}, Account: &profile}, labelled(wire.AgentStateRunning, &profile, "#6951"), true},
	} {
		if got := broadcastMatches(c.to, "/tmp/f", c.a); got != c.want {
			t.Errorf("%s: %v", c.name, got)
		}
	}
	if broadcastMatches(to, "/tmp/other", labelled(wire.AgentStateFinished, nil, "#6951")) {
		t.Error("another folder's session")
	}
}

// A labelled workflow's run with deliver: next-turn starts no session and queues nothing:
// the matching session hears it once, with its next turn. With the flag off it sends nothing
// and says why.
func TestLabelledWorkflowSendsAtTheNextTurn(t *testing.T) {
	t.Parallel()

	fakeRuntimes(t)
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	dir := folder(t, c, 1)
	url := store.FolderURL(dir)
	writeLabelledWorkflow(t, dir, "stop", "---\nname: Stop pushing\nagent: labelled\nto:\n  labels: lane\n---\nThe allowance is low: push nothing new.\n")
	approveAll(t, c, url)
	lane := seeded(t, d, dir, func(a *wire.Agent) {
		a.Labels = []wire.SessionLabel{{Value: "lane", Owner: wire.SessionLabelOwnerAgent}}
	})
	other := seeded(t, d, dir, nil)
	before := len(d.Store.Agents())

	var s wire.WorkflowSummary
	mustUnmarshal(t, c.call(t, 2, "workflows/run", map[string]any{"folder": url, "workflowID": "stop"}), &s)
	if s.LastOutcome == nil || s.LastOutcome.Refused == nil || s.LastOutcome.Refused.Value.Unreadable == nil ||
		s.LastOutcome.Refused.Value.Unreadable.Value != errLabelledWorkflowsOff.Message {
		t.Fatalf("flag off: %+v", s.LastOutcome)
	}
	if d.broadcastTurnText(lane.ID) != "" {
		t.Fatal("sent with the flag off")
	}

	c.call(t, 3, "preferences/set", map[string]any{labelledWorkflowsFlag: true})
	mustUnmarshal(t, c.call(t, 4, "workflows/run", map[string]any{"folder": url, "workflowID": "stop"}), &s)
	if s.LastOutcome != nil || s.LastFiredAt == nil || len(s.Broadcasts) != 1 || s.Broadcasts[0].Reachable != 1 || s.Broadcasts[0].Read != 0 {
		t.Fatalf("%+v %+v", s.LastOutcome, s.Broadcasts)
	}
	if n := len(d.Store.Agents()); n != before {
		t.Fatalf("%d sessions, was %d", n, before)
	}
	if a := d.Store.Agent(lane.ID); len(a.QueuedPrompts) != 0 || a.State != wire.AgentStateFinished {
		t.Fatalf("queued or started: %+v %s", a.QueuedPrompts, a.State)
	}
	if e := lastRan(t, d); e.Name != "workflow.ran" || e.Details["next_turn"] != "1" || e.Details["told"] != "0" {
		t.Fatalf("%+v", e)
	}

	// The next turn takes it, with what was queued, and only once.
	d.change(lane.ID, func(a *wire.Agent) {
		a.QueuedPrompts = []wire.QueuedPrompt{{ID: store.NewUUID(), Text: "Carry on.", From: wire.PromptOriginPerson}}
	})
	_, tn, ok := d.live(lane.ID).takeTurnWords()
	if !ok || !strings.Contains(tn.text, "The allowance is low: push nothing new.") || !strings.Contains(tn.text, "From the app") {
		t.Fatalf("%v %q", ok, tn.text)
	}
	if d.broadcastTurnText(lane.ID) != "" {
		t.Fatal("told twice")
	}
	if d.broadcastTurnText(other.ID) != "" {
		t.Fatal("told a session without the label")
	}

	// A second start while it is active changes nothing, and says so.
	mustUnmarshal(t, c.call(t, 5, "workflows/run", map[string]any{"folder": url, "workflowID": "stop"}), &s)
	if s.LastOutcome == nil || s.LastOutcome.Refused == nil || !strings.Contains(refusalWords(s.LastOutcome.Refused.Value), "still active") {
		t.Fatalf("%+v", s.LastOutcome)
	}
}

// wake never starts a stopped session or one waiting for an answer: each hears it at its next
// turn, and the run's history says why.
func TestLabelledWorkflowWakeSaysWhoWasNotTold(t *testing.T) {
	t.Parallel()

	fakeRuntimes(t)
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	dir := folder(t, c, 1)
	url := store.FolderURL(dir)
	writeLabelledWorkflow(t, dir, "wake", "---\nagent: labelled\nto:\n  labels: lane\ndeliver: wake\n---\nLook at main.\n")
	approveAll(t, c, url)
	c.call(t, 2, "preferences/set", map[string]any{labelledWorkflowsFlag: true})
	label := []wire.SessionLabel{{Value: "lane", Owner: wire.SessionLabelOwnerAgent}}
	stopped := seeded(t, d, dir, func(a *wire.Agent) { a.Labels, a.State = label, wire.AgentStateStopped })
	asking := seeded(t, d, dir, func(a *wire.Agent) { a.Labels, a.State = label, wire.AgentStateWaitingOnUser })

	c.call(t, 3, "workflows/run", map[string]any{"folder": url, "workflowID": "wake"})
	for _, id := range []wire.UUID{stopped.ID, asking.ID} {
		if a := d.Store.Agent(id); len(a.QueuedPrompts) != 0 {
			t.Fatalf("woken: %+v", a)
		}
	}
	e := lastRan(t, d)
	if e.Name != "workflow.ran" || e.Details["told"] != "0" || e.Details["next_turn"] != "2" ||
		!strings.Contains(e.Details["not_told"], "it is stopped") || !strings.Contains(e.Details["not_told"], "waits for an answer") {
		t.Fatalf("%+v", e)
	}
	if !strings.Contains(d.broadcastTurnText(stopped.ID), "Look at main.") {
		t.Fatal("the stopped session did not hear it at its next turn")
	}
}

// lastRan is the newest workflow.ran on the log.
func lastRan(t *testing.T, d *Daemon) wire.Event {
	t.Helper()
	for _, e := range logged(d) {
		if e.Name == "workflow.ran" {
			return e
		}
	}
	t.Fatal("no workflow.ran")
	return wire.Event{}
}

// writeLabelledWorkflow writes a workflow file in the folder.
func writeLabelledWorkflow(t *testing.T, dir, id, text string) {
	t.Helper()
	must(t, os.MkdirAll(workflowsFolder(dir), 0o755))                                         //nolint:gosec // G301: the mode a workflow folder has
	must(t, os.WriteFile(filepath.Join(workflowsFolder(dir), id+".md"), []byte(text), 0o644)) //nolint:gosec // G306: the mode a workflow file has
}
