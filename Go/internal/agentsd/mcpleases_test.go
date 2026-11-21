package agentsd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// #4731: a wait's subscription whose poll another machine holds stands by: it does not
// poll, and the event the holder's poll made, delivered by the bus, wakes the wait as if
// this machine had polled it. When the holder sleeps and the lease comes to this machine, it
// polls from now: nothing from before is raised, and nothing is raised twice. Stopping lets
// the lease go.
func TestAWaitStandsByWhileAnotherMachinePollsAndTakesOverFromNow(t *testing.T) {
	t.Parallel()

	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	ci := newEventsServer(t)
	folder := makeLeaseTestFolder(t)
	a := seeded(t, d, folder, nil)
	m := appClient(t, d, a.ID)
	m.post("initialize", map[string]any{}, 1)
	serverHome(t, d, ci.URL)
	quickPolls(d)

	var mine atomic.Bool
	var mu sync.Mutex
	var asked, released []string
	d.SetControlCapabilities(func() []string { return []string{wire.CapEventsBus1, wire.CapEventsLeases1} })
	d.SetControlAsk(askControl(func(_ context.Context, method string, params any) (json.RawMessage, error) {
		if method != wire.CallBusLease.Name {
			return json.RawMessage(`{}`), nil
		}
		p := params.(wire.BusLease)
		mu.Lock()
		asked = append(asked, p.Polls...)
		released = append(released, p.Release...)
		mu.Unlock()
		return json.Marshal(answerLeasesForTest(p.Polls, mine.Load()))
	}))
	d.StartMCPEvents()

	answered := make(chan string, 1)
	go func() {
		text, _ := m.tool(21, "wait_for_event", map[string]any{"events": "pr.merged", "where": map[string]any{"repo": "alexec/Agents", "pr": 5}, "until_minutes": 10})
		answered <- text
	}()
	var sub *mcpSubscription
	waitFor(t, "the wait's subscription stands by", func() bool {
		d.mcpEvMu.Lock()
		for _, s := range d.mcpSubs {
			sub = s
		}
		d.mcpEvMu.Unlock()
		d.mcpLeases.mu.Lock()
		defer d.mcpLeases.mu.Unlock()
		return sub != nil && d.mcpLeases.standing[sub.id] != ""
	})
	poll := d.producerPoll(sub.folder, sub.key)
	firstWait := d.Store.Agent(a.ID).EventWait.ID
	d.mcpLeases.mu.Lock()
	standing := d.mcpLeases.standing[sub.id]
	d.mcpLeases.mu.Unlock()
	if poll == "" || standing != poll {
		t.Fatalf("the poll is %q, standing on %q", poll, standing)
	}
	time.Sleep(200 * time.Millisecond)
	ci.mu.Lock()
	polls := len(ci.polls)
	ci.mu.Unlock()
	if polls != 0 {
		t.Fatalf("a machine standing by polled %d times", polls)
	}

	// The holder's poll made pr.merged: the bus delivers it, and the wait wakes.
	subject := wire.Ref{Provider: "github", Type: "pr", Topic: "alexec/agents", ID: "5"}
	bus := wire.BusEvent{Specversion: "1.0", ID: subject.String() + "@pr.merged", Type: "pr.merged", Provider: "github",
		Topic: "alexec/agents", Subject: &subject, Source: "github:alexec/agents", Time: "2026-11-11T17:02:11Z",
		Producer: "agents:machine:#linux-b", Poll: &poll}
	raw, _ := json.Marshal(wire.BusDelivery{Event: bus, Subscriptions: []string{}})
	d.Told(wire.NotifyBusDeliver.Name, raw)
	d.Told(wire.NotifyBusDeliver.Name, raw)
	awaitLeaseTestAnswer(t, answered, "the wait was never answered")
	checkBusEventLoggedOnce(t, d, bus, sub)

	// A second wait stands by too, with no cursor: one from before goes at once.
	answered2 := make(chan string, 1)
	go func() {
		text, _ := m.tool(22, "wait_for_event", map[string]any{"events": "pr.merged", "where": map[string]any{"repo": "alexec/Agents", "pr": 5}, "until_minutes": 10})
		answered2 <- text
	}()
	// The second wait's own subscription, not the first's stand-by before its unsubscribe
	// (#5742): that passed by, and the second wait's start then cleared the record below.
	// Once a subscription holds the second wait, it stays, and so does its stand-by.
	waitFor(t, "the second wait stands by", func() bool {
		w := d.Store.Agent(a.ID).EventWait
		if w == nil || w.ID == firstWait {
			return false
		}
		d.mcpEvMu.Lock()
		s := d.mcpSubs[sub.id]
		holds := s != nil && slices.Contains(s.waitIDs, w.ID)
		d.mcpEvMu.Unlock()
		d.mcpLeases.mu.Lock()
		defer d.mcpLeases.mu.Unlock()
		return holds && d.mcpLeases.standing[sub.id] == poll
	})
	old := "old"
	ci.mu.Lock()
	ci.byCursor[old] = []map[string]any{{"eventId": "missed", "name": "pr.merged", "data": map[string]any{}}}
	ci.mu.Unlock()
	d.mcpEvMu.Lock()
	d.mcpRecordsLocked().Subscriptions[sub.id] = &mcpRecord{Cursor: &old, Seen: []mcpSeen{}, Delivering: []string{}, LastNamedAt: now()}
	d.mcpEvMu.Unlock()
	waitFor(t, "the cursor gone", func() bool {
		d.mcpEvMu.Lock()
		defer d.mcpEvMu.Unlock()
		r := d.mcpRecordsLocked().Subscriptions[sub.id]
		return r != nil && r.Cursor == nil
	})

	// The holder sleeps and the lease is this machine's: it polls from now, so neither
	// what the server had before nor what came while it stood by is raised.
	mine.Store(true)
	waitFor(t, "the first poll from now", func() bool { return ci.polled("") && ci.polled("c1") })
	if ci.polled(old) || len(mcpEventsRaised(d, "before")) != 0 || len(mcpEventsRaised(d, "missed")) != 0 {
		t.Fatal("it backfilled")
	}
	waitFor(t, "the lease held", func() bool { return d.mcpHeldPoll(sub.id) == poll })
	// What its own poll brings now wakes the second wait.
	ci.mu.Lock()
	ci.queued = []map[string]any{{"eventId": "w9", "name": "pr.merged", "data": map[string]any{}}}
	ci.mu.Unlock()
	awaitLeaseTestAnswer(t, answered2, "the second wait was never answered")

	// The subscription ends with its waits, or the host stops: either lets the lease go.
	d.stopMCPEvents()
	waitFor(t, "the lease let go", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return contains(released, poll) && contains(asked, poll)
	})
}

// makeLeaseTestFolder makes a folder with a project, for the lease test.
func makeLeaseTestFolder(t *testing.T) string {
	t.Helper()
	folder := t.TempDir()
	if err := os.MkdirAll(filepath.Join(folder, ".agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, folderConfig), []byte(`{"project":"proj-4731"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return folder
}

// answerLeasesForTest is the control plane's answer to a lease request for polls: this
// machine holds each when mine is true, else another machine holds it.
func answerLeasesForTest(polls []string, mine bool) wire.BusLeased {
	out := wire.BusLeased{Leases: []wire.BusProducerLease{}}
	for _, poll := range polls {
		holder := "LINUX-B"
		if mine {
			holder = "MAC-A"
		}
		out.Leases = append(out.Leases, wire.BusProducerLease{Poll: poll, Holder: holder, Mine: mine, Until: "2026-11-11T17:07:11Z"})
	}
	return out
}

// awaitLeaseTestAnswer waits for a wait's answer, and fails when it does not say pr.merged
// happened, or does not come in time.
func awaitLeaseTestAnswer(t *testing.T, answered <-chan string, never string) {
	t.Helper()
	select {
	case text := <-answered:
		if !strings.Contains(text, "pr.merged happened at ") {
			t.Fatal(text)
		}
	case <-afterLimit(t):
		waitFailed(t, "%s", never)
	}
}

// checkBusEventLoggedOnce fails when the bus event is not in the log once, with the
// subscription, the server and the producer.
func checkBusEventLoggedOnce(t *testing.T, d *Daemon, bus wire.BusEvent, sub *mcpSubscription) {
	t.Helper()
	n := 0
	for _, e := range d.eventsPage(wire.EventsListRequest{Limit: 200}).Events {
		if e.ID != nil && *e.ID == bus.ID {
			n++
			if e.Details["subscription"] != sub.key || e.Details["server"] != "ci" || e.Details["bus_producer"] != bus.Producer {
				t.Fatalf("%+v", e.Details)
			}
		}
	}
	if n != 1 {
		t.Fatalf("the delivered event is in the log %d times", n)
	}
}

// #4731: a subscription a workflow holds needs the event's data, so it polls whoever holds
// the lease.
func TestASubscriptionAWorkflowHoldsNeverStandsBy(t *testing.T) {
	t.Parallel()

	s := &mcpSubscription{workflows: []string{"w"}, waits: []wire.UUID{"a"}}
	if s.mayStandBy() {
		t.Fatal("a workflow's subscription stood by")
	}
	if (&mcpSubscription{archives: true}).mayStandBy() {
		t.Fatal("archive when merged stood by")
	}
	if !(&mcpSubscription{waits: []wire.UUID{"a"}}).mayStandBy() {
		t.Fatal("a wait's subscription may stand by")
	}
}
