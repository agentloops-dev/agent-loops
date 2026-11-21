package agentsd

import (
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// #6310: a wait_for_event call answers inside its hold, also when the Mac sleeps or a
// server is slow to say what it offers. The client counts a sleep and the hold's timer
// does not, so a call open over a sleep ended in the client's timeout.

// heldWait starts a wait on branch.moved for a new agent of dir and returns its answer.
func heldWait(t *testing.T, d *Daemon, dir string) (wire.UUID, chan string) {
	t.Helper()
	a := seeded(t, d, dir, nil)
	m := appClient(t, d, a.ID)
	m.post("initialize", map[string]any{}, 1)
	answered := make(chan string, 1)
	// No t.Fatal here: the goroutine can outlive the test, and a Fatal after it ends
	// panics the binary (#6495). What failed comes back as the answer.
	go func() {
		text, _, err := m.tryTool(2, "wait_for_event", map[string]any{"events": "branch.moved", "where": map[string]any{"branch": "cd"}, "until_minutes": 10})
		if err != nil {
			text = err.Error()
		}
		answered <- text
	}()
	waitFor(t, "the wait held", func() bool {
		d.waitMu.Lock()
		defer d.waitMu.Unlock()
		return d.openWaits[a.ID] != nil
	})
	return a.ID, answered
}

// stillWaitsAndWakes checks the call said still waiting, and that the wait it kept starts
// the agent again on its event.
func stillWaitsAndWakes(t *testing.T, d *Daemon, dir string, agent wire.UUID, answered chan string, why string) {
	t.Helper()
	select {
	case text := <-answered:
		if !strings.HasPrefix(text, "Still waiting for branch.moved branch cd, since ") {
			t.Fatal(text)
		}
	case <-afterLimit(t):
		waitFailed(t, "%s", why)
	}
	if w := d.Store.Agent(agent).EventWait; w == nil || w.Ending != nil {
		t.Fatalf("the wait went: %+v", w)
	}
	d.raise(wire.EventDraft{Name: "branch.moved", At: now(), Scope: folderScope(dir), Sentence: "cd moved.", Details: map[string]string{"branch": "cd"}})
	waitFor(t, "the wake in its chat", func() bool {
		for _, u := range userMessages(d, agent) {
			if strings.HasPrefix(u, "The event you were waiting for happened.\n\nbranch.moved at ") {
				return true
			}
		}
		return false
	})
}

func TestASleepAnswersAHeldWaitBeforeTheMacSleeps(t *testing.T) {
	fakeRuntimes(t)
	setFor(t, &eventHoldLimit, 10*time.Minute)
	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	dir := t.TempDir()
	agent, answered := heldWait(t, d, dir)
	d.machineChanged(machineChange{kind: "sleep"})
	stillWaitsAndWakes(t, d, dir, agent, answered, "the sleep left the call held")
}

func TestAWakeAnswersAWaitHeldOverTheSleep(t *testing.T) {
	fakeRuntimes(t)
	setFor(t, &eventHoldLimit, 10*time.Minute)
	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	dir := t.TempDir()
	agent, answered := heldWait(t, d, dir)
	d.machineChanged(machineChange{kind: "wake"})
	stillWaitsAndWakes(t, d, dir, agent, answered, "the wake left the call held")
}

// A server that does not say what events it offers keeps no call past its hold: the call
// says nothing is waiting yet, and the next one, once the server answers, waits.
func TestABlockedServerKeepsNoWaitPastItsHold(t *testing.T) {
	setFor(t, &eventHoldLimit, 300*time.Millisecond)
	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	ci := newEventsServer(t)
	gate := make(chan struct{})
	ci.mu.Lock()
	ci.listGate = gate
	ci.mu.Unlock()
	opened := true
	release := func() {
		if opened {
			opened = false
			close(gate)
		}
	}
	t.Cleanup(release)
	a := seeded(t, d, t.TempDir(), nil)
	m := appClient(t, d, a.ID)
	m.post("initialize", map[string]any{}, 1)
	serverHome(t, d, ci.URL)
	quickPolls(d)
	d.StartMCPEvents()
	args := map[string]any{"events": "pr.merged", "where": map[string]any{"repo": "alexec/Agents", "pr": 5}, "until_minutes": 10}
	asked := time.Now()
	text, isErr := m.tool(2, "wait_for_event", args)
	// The server's own limit is 15 s (mcpEventTimeout): without the hold's, the call waits it.
	if took := time.Since(asked); took > 10*time.Second {
		t.Fatalf("held %s past a hold of %s", took, eventHoldLimit)
	}
	if !isErr || !strings.Contains(text, "did not say in ") || !strings.HasSuffix(text, "Nothing is waiting yet: call wait_for_event again.") {
		t.Fatal(text)
	}
	if w := d.Store.Agent(a.ID).EventWait; w != nil {
		t.Fatalf("a refused call waits: %+v", w)
	}
	release()
	waitFor(t, "the listing", func() bool {
		d.mcpEvMu.Lock()
		defer d.mcpEvMu.Unlock()
		for _, l := range d.mcpListings {
			if l.offers {
				return true
			}
		}
		return false
	})
	text, _ = m.tool(3, "wait_for_event", args)
	if !strings.Contains(text, "Still waiting for pr.merged") {
		t.Fatal(text)
	}
}

// #6541: in a dark wake the Mac sleeps again within seconds, and IOKit says neither the
// wake nor the sleep, so agentsd sees the sleep from its clocks.

func TestASleepNoPowerMessageSaidAnswersAHeldWait(t *testing.T) {
	fakeRuntimes(t)
	setFor(t, &eventHoldLimit, 10*time.Minute)
	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	dir := t.TempDir()
	agent, answered := heldWait(t, d, dir)
	d.sleptThrough(15*time.Minute, time.Now().Add(-time.Second))
	stillWaitsAndWakes(t, d, dir, agent, answered, "the sleep the clocks saw left the call held")
}

func TestADarkWakeHoldsACallOnlyUntilTheNextSleep(t *testing.T) {
	fakeRuntimes(t)
	setFor(t, &eventHoldLimit, 10*time.Minute)
	setFor(t, &darkWakeHold, 200*time.Millisecond)
	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	d.sleptThrough(15*time.Minute, time.Now().Add(-time.Second))
	a := seeded(t, d, t.TempDir(), nil)
	m := appClient(t, d, a.ID)
	m.post("initialize", map[string]any{}, 1)
	asked := time.Now()
	text, _ := m.tool(2, "wait_for_event", map[string]any{"events": "branch.moved", "where": map[string]any{"branch": "cd"}, "until_minutes": 10})
	if took := time.Since(asked); took > 30*time.Second {
		t.Fatalf("held %s in a dark wake", took)
	}
	if !strings.HasPrefix(text, "Still waiting for branch.moved branch cd, since ") {
		t.Fatal(text)
	}
	if w := d.Store.Agent(a.ID).EventWait; w == nil || w.Ending != nil {
		t.Fatalf("the wait went: %+v", w)
	}
	// The power messages say wake: the Mac is up, and a call is held its full time again.
	d.machineChanged(machineChange{kind: "wake"})
	if hold := d.waitHold(); hold != eventHoldLimit {
		t.Fatalf("held %s after a wake, want %s", hold, eventHoldLimit)
	}
}

func TestASleepTheWakeWordSaidIsNoDarkWake(t *testing.T) {
	setFor(t, &eventHoldLimit, 10*time.Minute)
	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	lastLook := time.Now()
	d.machineChanged(machineChange{kind: "wake"})
	d.sleptThrough(15*time.Minute, lastLook)
	if hold := d.waitHold(); hold != eventHoldLimit {
		t.Fatalf("held %s after a heard wake, want %s", hold, eventHoldLimit)
	}
	if got := sleptBetween(lastLook, time.Now()); got < -time.Second || got > time.Second {
		t.Fatalf("an awake second slept %s", got)
	}
}
