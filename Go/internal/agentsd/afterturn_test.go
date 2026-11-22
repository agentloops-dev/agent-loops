package agentsd

import (
	"context"
	"os"
	goruntime "runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// Shutdown returns once the turns it stopped have written their endings and what follows
// them, and starts nothing after (#1272).
func TestShutdownWaitsForTheWritesAfterATurn(t *testing.T) {
	t.Parallel()

	fakeRuntimes(t)
	d, c := start(t, t.TempDir())
	var id wire.UUID
	mustUnmarshal(t, c.call(t, 1, "agents/start", map[string]any{"runtimeID": "claude", "cwd": "file://" + t.TempDir(),
		"prompt": "wait for me"}), &id)
	c.until(t, "running", ended(id, wire.AgentStateRunning))

	d.Shutdown()

	if k := kinds(d, id); len(k) == 0 || k[len(k)-1] != "stateChanged" {
		t.Fatalf("the ending was not recorded before Shutdown returned: %v", k)
	}
	b, _ := os.ReadFile(d.eventsPath())
	if !endingRaised(string(b), id) {
		t.Fatalf("the agent's ending was not in events.jsonl before Shutdown returned:\n%s", b)
	}
	if d.goAfterTurn("a turn after Shutdown", func() {}) {
		t.Fatal("something started after Shutdown")
	}
	time.Sleep(200 * time.Millisecond)
	if after, _ := os.ReadFile(d.eventsPath()); string(after) != string(b) {
		t.Fatalf("events.jsonl was written after Shutdown returned:\n%s", after[len(b):])
	}
}

// What follows a turn and tells the clients as the host goes is done when Shutdown
// returns, also with a client that reads nothing (#3828): each line to it waited the
// conn's 5 s, so a workflow's run that told three things outlived Shutdown's wait and
// wrote into a test's folder as it was deleted.
func TestShutdownWaitsForWhatTellsAClientThatReadsNothing(t *testing.T) {
	t.Parallel()

	d, c := start(t, t.TempDir())
	for len(c.out) < cap(c.out) {
		c.out <- ""
	}
	var done atomic.Bool
	d.goAfterTurn("tells as the host goes", func() {
		for !d.going() {
			time.Sleep(time.Millisecond)
		}
		for range 3 {
			Broadcast(d, wire.NotifyWorkflowRemoved, wire.WorkflowRemovedNotification{WorkflowID: "x"})
		}
		done.Store(true)
	})

	d.Shutdown()

	if !done.Load() {
		t.Fatal("what told the clients was not done when Shutdown returned")
	}
}

// The writes in line for a client that reads nothing end as Shutdown begins (#6268).
// clientsToTell starts no new one, but those already waiting for the client's write lock
// went on one at a time, each to the conn's 5 s: a reconsider among them outlived
// Shutdown's wait for its goroutines.
func TestShutdownEndsTheWritesInLineForAClientThatReadsNothing(t *testing.T) {
	t.Parallel()

	const self = "TestShutdownEndsTheWritesInLineForAClientThatReadsNothing"
	d, c := start(t, t.TempDir())
	for len(c.out) < cap(c.out) {
		c.out <- ""
	}
	for range 3 {
		d.goJob("tells a client", func() {
			Broadcast(d, wire.NotifyWorkflowRemoved, wire.WorkflowRemovedNotification{WorkflowID: "x"})
		})
	}
	waitFor(t, "three writes in line for the client", func() bool { return writesToClientsFrom(self) >= 3 })
	readOneLineAsShutdownBegins(d, c)

	began := time.Now()
	d.Shutdown()

	if took := time.Since(began); took > 2*time.Second {
		t.Errorf("Shutdown took %v", took)
	}
	if live := d.liveJobs(); len(live) > 0 {
		t.Fatalf("still writing to the client after Shutdown: %v", live)
	}
}

// The writes in line behind a loop's write end as Shutdown begins, before it waits for
// the loops (#6603): on the Mac the wake loop told wake/changed to a client that read
// nothing, Shutdown waited for that loop, and the three writes in line behind it went on,
// each to the conn's 5 s, as closeAfterTurns had not come yet: Shutdown took 20 s.
func TestShutdownEndsTheWritesInLineBehindALoop(t *testing.T) {
	t.Parallel()

	const self = "TestShutdownEndsTheWritesInLineBehindALoop"
	d, c := start(t, t.TempDir())
	for len(c.out) < cap(c.out) {
		c.out <- ""
	}
	d.goLoop("tells a client", func(ctx context.Context) {
		Broadcast(d, wire.NotifyWorkflowRemoved, wire.WorkflowRemovedNotification{WorkflowID: "loop"})
		<-ctx.Done()
	})
	waitFor(t, "the loop's write to the client", func() bool { return writesToClientsFrom(self) >= 1 })
	for range 3 {
		d.goJob("tells a client", func() {
			Broadcast(d, wire.NotifyWorkflowRemoved, wire.WorkflowRemovedNotification{WorkflowID: "x"})
		})
	}
	waitFor(t, "three writes in line behind the loop's", func() bool { return writesToClientsFrom(self) >= 4 })
	readOneLineAsShutdownBegins(d, c)

	began := time.Now()
	d.Shutdown()

	if took := time.Since(began); took > 2*time.Second {
		t.Errorf("Shutdown took %v", took)
	}
	if live := d.liveJobs(); len(live) > 0 {
		t.Fatalf("still writing to the client after Shutdown: %v", live)
	}
}

// readOneLineAsShutdownBegins has the client read one line once Shutdown has begun: the
// write that holds the lock goes, and each one in line after it would wait the conn's 5 s
// again. A real socket lets a client that reads nothing go after its own wait (sockConn).
func readOneLineAsShutdownBegins(d *Daemon, c *conn) {
	go func() {
		for !d.isQuiet() {
			time.Sleep(time.Millisecond)
		}
		select {
		case <-c.out:
		case <-c.gone:
		}
	}()
}

// writesToClientsFrom is how many goroutines that test started are in a write to a client,
// or waiting for one. Not those of the whole process (#6603): the tests run in parallel,
// and another test's writes passed a wait before this one's were in line.
func writesToClientsFrom(test string) int {
	buf := make([]byte, 1<<22)
	n := 0
	for _, g := range strings.Split(string(buf[:goruntime.Stack(buf, true)]), "\n\n") {
		if strings.Contains(g, "agentsd.(*Client).write") && strings.Contains(g, "agentsd."+test+".func") {
			n++
		}
	}
	return n
}

// endingRaised is whether events.jsonl has the event that says how the agent ended.
func endingRaised(events string, id wire.UUID) bool {
	for _, line := range strings.Split(events, "\n") {
		if strings.Contains(line, string(id)) && (strings.Contains(line, `"agent.stopped"`) || strings.Contains(line, `"agent.failed"`)) {
			return true
		}
	}
	return false
}

// A wait for the writes after a turn on a live daemon is safe while new ones start (#6269):
// with a sync.WaitGroup, an Add from a count of zero while Wait ran was a data race, and a
// folder.changed timer did that under a test's wait for its queue check.
func TestWaitAfterTurnsWhileTurnsStart(t *testing.T) {
	t.Parallel()

	d, _ := start(t, t.TempDir())
	stop := make(chan struct{})
	started := make(chan struct{})
	go func() {
		defer close(started)
		for {
			select {
			case <-stop:
				return
			default:
			}
			d.goAfterTurn("a short write", func() {})
		}
	}()
	for range 200 {
		if !d.waitAfterTurns(10 * time.Second) {
			t.Fatal("a wait on short writes did not end")
		}
	}
	close(stop)
	<-started

	// A wait sees a write that is under way, and ends when it does.
	release := make(chan struct{})
	d.goAfterTurn("a long write", func() { <-release })
	if d.waitAfterTurns(50 * time.Millisecond) {
		t.Fatal("the wait ended before the write did")
	}
	close(release)
	if !d.waitAfterTurns(10 * time.Second) {
		t.Fatal("the wait did not end after the write did")
	}
}
