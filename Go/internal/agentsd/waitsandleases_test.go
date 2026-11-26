package agentsd

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/flaky"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// #6190: what makes a lane stop with no error, when an event does not come or a lease is not
// let go. A server killed during a wait, an event sent twice, a lease holder that died, many
// holders that ask at once, a Mac that slept, and a wait or a place in line that makes no
// progress.

// leaseArgs is lease_resource's or release_resource's arguments.
func leaseArgs(name string, more ...any) map[string]json.RawMessage {
	args := map[string]json.RawMessage{"name": json.RawMessage(`"` + name + `"`)}
	for i := 0; i+1 < len(more); i += 2 {
		b, _ := json.Marshal(more[i+1])
		args[more[i].(string)] = b
	}
	return args
}

// mustLease fails the test on a lease call's error.
func mustLease(t *testing.T) func(string, error) {
	return func(_ string, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
}

func holdersOf(d *Daemon, name wire.ResourceName) []wire.UUID {
	d.leaseMu.Lock()
	defer d.leaseMu.Unlock()
	var ids []wire.UUID
	if e := d.book.Entries[name]; e != nil {
		for _, l := range e.Leases {
			ids = append(ids, l.Holder)
		}
	}
	return ids
}

func stallNotices(d *Daemon) []postedNotice {
	d.attentionMu.Lock()
	defer d.attentionMu.Unlock()
	var out []postedNotice
	for _, n := range d.notices {
		if strings.HasPrefix(n.id, stallNoticePrefix) {
			out = append(out, n)
		}
	}
	return out
}

// The server is killed while a wait is open, and an event comes meanwhile. When the server
// is back, the event wakes the wait, once. The server sends it again, to a new wait from the
// same cursor: it is not raised again and does not wake that wait.
func TestAnEventWhileItsServerWasDownIsRaisedOnce(t *testing.T) {
	flaky.UnderLoad(t, "#6992")
	t.Parallel()

	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	ci := newEventsServer(t)
	a := seeded(t, d, t.TempDir(), nil)
	m := appClient(t, d, a.ID)
	m.post("initialize", map[string]any{}, 1)
	serverHome(t, d, ci.URL)
	quickPolls(d)
	d.StartMCPEvents()
	wait := func(id int) chan string {
		answered := make(chan string, 1)
		go func() {
			text, _ := m.tool(id, "wait_for_event", map[string]any{"events": "pr.merged", "where": map[string]any{"pr": 5}, "until_minutes": 10})
			answered <- text
		}()
		return answered
	}
	answered := wait(2)
	waitFor(t, "the first poll", func() bool { return ci.polled("c1") })
	ci.mu.Lock()
	ci.down = true
	ci.queued = []map[string]any{{"eventId": "w1", "name": "pr.merged", "data": map[string]any{"pr": map[string]any{"number": 5}}}}
	ci.mu.Unlock()
	waitFor(t, "polls to the killed server", func() bool {
		ci.mu.Lock()
		defer ci.mu.Unlock()
		return ci.downHits >= 2
	})
	select {
	case text := <-answered:
		t.Fatalf("answered while the server was down: %s", text)
	default:
	}
	ci.mu.Lock()
	ci.down = false
	ci.mu.Unlock()
	select {
	case text := <-answered:
		if !strings.Contains(text, "pr.merged happened at ") {
			t.Fatal(text)
		}
	case <-afterLimit(t):
		waitFailed(t, "the wait was never answered after the server came back")
	}
	if n := len(mcpEventsRaised(d, "w1")); n != 1 {
		t.Fatalf("w1 raised %d times", n)
	}

	// The server sends w1 again, from the cursor it gave with it.
	ci.mu.Lock()
	ci.byCursor["c2"] = []map[string]any{{"eventId": "w1", "name": "pr.merged", "data": map[string]any{"pr": map[string]any{"number": 5}}}}
	before := len(ci.polls)
	ci.mu.Unlock()
	again := wait(3)
	waitFor(t, "two polls that send w1 again", func() bool {
		ci.mu.Lock()
		defer ci.mu.Unlock()
		n := 0
		for _, c := range ci.polls[before:] {
			if c == "c2" {
				n++
			}
		}
		return n >= 2
	})
	select {
	case text := <-again:
		t.Fatalf("an event sent twice woke a second wait: %s", text)
	default:
	}
	if n := len(mcpEventsRaised(d, "w1")); n != 1 {
		t.Fatalf("w1 raised %d times", n)
	}
	ci.mu.Lock()
	delete(ci.byCursor, "c2")
	ci.queued = []map[string]any{{"eventId": "w2", "name": "pr.merged", "data": map[string]any{"pr": map[string]any{"number": 5}}}}
	ci.mu.Unlock()
	select {
	case text := <-again:
		if !strings.Contains(text, "pr.merged happened at ") {
			t.Fatal(text)
		}
	case <-afterLimit(t):
		waitFailed(t, "the second wait was never answered")
	}
	if n1, n2 := len(mcpEventsRaised(d, "w1")), len(mcpEventsRaised(d, "w2")); n1 != 1 || n2 != 1 {
		t.Fatalf("w1 raised %d times, w2 %d", n1, n2)
	}
}

// A wait whose server stays down is a stall: after waitStallAfter with no poll, a notice in
// Needs you says so, on the waiting session. The wait goes on; when it times out, it says the
// server could not be polled, not only "no match". Its end takes the notice back.
func TestAWaitOnAServerThatStaysDownIsANotice(t *testing.T) {
	t.Parallel()

	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	ci := newEventsServer(t)
	a := seeded(t, d, t.TempDir(), nil)
	m := appClient(t, d, a.ID)
	m.post("initialize", map[string]any{}, 1)
	serverHome(t, d, ci.URL)
	quickPolls(d)
	d.StartMCPEvents()
	answered := make(chan string, 1)
	go func() {
		text, _ := m.tool(2, "wait_for_event", map[string]any{"events": "pr.merged", "where": map[string]any{"pr": 5}, "until_minutes": 10})
		answered <- text
	}()
	waitFor(t, "the first poll", func() bool { return ci.polled("c1") })
	ci.mu.Lock()
	ci.down = true
	ci.mu.Unlock()
	waitFor(t, "the subscription retrying", func() bool {
		d.mcpEvMu.Lock()
		defer d.mcpEvMu.Unlock()
		for _, l := range d.mcpLives {
			if l.state == wire.MCPTriggerStatusStateRetrying && l.failure != nil {
				return true
			}
		}
		return false
	})
	d.checkStalls(time.Now().Add(waitStallAfter / 2))
	if n := stallNotices(d); len(n) != 0 {
		t.Fatalf("a notice before the wait stalled: %+v", n)
	}
	d.checkStalls(time.Now().Add(waitStallAfter + time.Minute))
	n := stallNotices(d)
	if len(n) != 1 || n[0].agentID != a.ID || !strings.HasPrefix(n[0].h3, "Waiting for pr.merged on ci (pr 5), but no poll of it worked since ") {
		t.Fatalf("%+v", n)
	}
	// Told once: the next look does not post it again.
	d.checkStalls(time.Now().Add(waitStallAfter + 2*time.Minute))
	if again := stallNotices(d); len(again) != 1 || again[0].raised != n[0].raised {
		t.Fatalf("%+v", again)
	}
	// Its time is up.
	d.change(a.ID, func(a *wire.Agent) {
		w := *a.EventWait
		past := wire.DateOf(time.Now().Add(-time.Second))
		w.Deadline = &past
		a.EventWait = &w
	})
	d.waitDeadlinesPassed()
	select {
	case text := <-answered:
		if !strings.Contains(text, "timed out at") || !strings.Contains(text, "ci could not be polled since ") ||
			!strings.Contains(text, "An event may have come that this wait did not hear.") {
			t.Fatal(text)
		}
	case <-afterLimit(t):
		waitFailed(t, "the wait never timed out")
	}
	d.checkStalls(time.Now().Add(waitStallAfter + 3*time.Minute))
	if n := stallNotices(d); len(n) != 0 {
		t.Fatalf("the notice outlived its wait: %+v", n)
	}
}

// A lease's holder whose runtime dies lets go of it at once, and the one in line gets it; it
// used to keep it until it ran out.
func TestADeadHoldersLeaseGoesToTheLine(t *testing.T) {
	t.Parallel()

	fakeRuntimes(t)
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	c.drain()
	dir := t.TempDir()
	holder, next := seeded(t, d, dir, nil).ID, seeded(t, d, dir, nil).ID
	if text, err := d.leaseResource(holder, leaseArgs("build", "minutes", 30)); err != nil || !strings.HasPrefix(text, "You hold build") {
		t.Fatal(text, err)
	}
	if text, err := d.leaseResource(next, leaseArgs("build")); err != nil || !strings.Contains(text, "1st in line") {
		t.Fatal(text, err)
	}
	d.end(d.live(holder), wire.AgentStateStopped, wire.EndedReasonProcessDied)
	if got := holdersOf(d, "build"); len(got) != 1 || got[0] != next {
		t.Fatalf("holders %v, want the one in line", got)
	}
	if got := lineOf(d, "build"); len(got) != 0 {
		t.Fatalf("line %v", got)
	}
}

// Many ask at once: each gets one place, in the order they asked, and a holder that lets go
// and asks again goes behind every one that waited. Nobody holds it twice before all have
// held it once.
func TestALeasesLineIsFair(t *testing.T) {
	t.Parallel()

	fakeRuntimes(t)
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	c.drain()
	dir := t.TempDir()
	first := seeded(t, d, dir, nil).ID
	if _, err := d.leaseResource(first, leaseArgs("fair")); err != nil {
		t.Fatal(err)
	}
	const many = 8
	agents := []wire.UUID{first}
	for range many {
		agents = append(agents, seeded(t, d, dir, nil).ID)
	}
	var wg sync.WaitGroup
	for _, id := range agents[1:] {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := d.leaseResource(id, leaseArgs("fair")); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	line := lineOf(d, "fair")
	if len(line) != many {
		t.Fatalf("line %v", line)
	}
	seen := map[wire.UUID]int{}
	for _, id := range line {
		seen[id]++
	}
	if len(seen) != many {
		t.Fatalf("a place twice: %v", line)
	}
	// Each holder lets go and asks again at once, for two rounds.
	held := []wire.UUID{first}
	for range 2 * (many + 1) {
		h := holdersOf(d, "fair")
		if len(h) != 1 {
			t.Fatalf("holders %v", h)
		}
		if _, err := d.releaseResource(h[0], leaseArgs("fair")); err != nil {
			t.Fatal(err)
		}
		if _, err := d.leaseResource(h[0], leaseArgs("fair")); err != nil {
			t.Fatal(err)
		}
		h = holdersOf(d, "fair")
		if len(h) != 1 || h[0] == held[len(held)-1] {
			t.Fatalf("the holder that let go got it again: %v", h)
		}
		held = append(held, h[0])
	}
	for round := 0; round < 2; round++ {
		once := map[wire.UUID]bool{}
		for _, id := range held[round*(many+1) : (round+1)*(many+1)] {
			if once[id] {
				t.Fatalf("round %d: %s held it twice before all held it once: %v", round, id, held)
			}
			once[id] = true
		}
	}
	if held[1] != line[0] || held[many] != line[many-1] {
		t.Fatalf("not in the order they asked: line %v, held %v", line, held)
	}
}

// A holder that extends again and again while others wait cannot keep it past one longest
// lease from its grant; with nobody in line it can.
func TestAnExtensionWhileOthersWaitStopsAtOneLongestLease(t *testing.T) {
	t.Parallel()

	granted := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	longest := 60 * time.Minute
	maxMinutes := int64(60)
	entry := func() *leaseEntry {
		return &leaseEntry{Rules: leaseRules{Holders: 1, MaximumMinutes: &maxMinutes}, Leases: []wire.Lease{{Resource: "build", DisplayName: "build",
			GrantedAt: wire.DateOf(granted), ExpiresAt: wire.DateOf(granted.Add(longest))}}}
	}
	at := wire.DateOf(granted.Add(50 * time.Minute))
	e := entry()
	_, words := extendHeldLease(e, 0, at, longest, false, true)
	if got := e.Leases[0].ExpiresAt.Time(); !got.Equal(granted.Add(longest)) || !strings.HasSuffix(words, leaseLineCapWords) {
		t.Fatalf("with a line: until %v, %q", got, words)
	}
	e = entry()
	_, words = extendHeldLease(e, 0, at, longest, false, false)
	if got := e.Leases[0].ExpiresAt.Time(); !got.Equal(granted.Add(50*time.Minute + longest)) || strings.Contains(words, "Others wait") {
		t.Fatalf("with no line: until %v, %q", got, words)
	}
	// A short extension that stays inside the cap says nothing of it.
	e = entry()
	_, words = extendHeldLease(e, 0, wire.DateOf(granted.Add(10*time.Minute)), 20*time.Minute, false, true)
	if got := e.Leases[0].ExpiresAt.Time(); !got.Equal(granted.Add(longest)) || strings.Contains(words, "Others wait") {
		t.Fatalf("inside the cap: until %v, %q", got, words)
	}
}

// A place in a lease's line that has not moved for leaseStallAfter is a notice on the
// waiting session, which names the holder; the grant takes it back.
func TestALineThatDoesNotMoveIsANotice(t *testing.T) {
	t.Parallel()

	fakeRuntimes(t)
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	c.drain()
	dir := t.TempDir()
	title := "Build it"
	holder := seeded(t, d, dir, func(a *wire.Agent) { a.Title = &title }).ID
	waiting := seeded(t, d, dir, nil).ID
	mustLease(t)(d.leaseResource(holder, leaseArgs("stuck", "minutes", 240)))
	mustLease(t)(d.leaseResource(waiting, leaseArgs("stuck")))
	d.checkStalls(time.Now().Add(leaseStallAfter - time.Minute))
	if n := stallNotices(d); len(n) != 0 {
		t.Fatalf("%+v", n)
	}
	d.checkStalls(time.Now().Add(leaseStallAfter + time.Minute))
	n := stallNotices(d)
	if len(n) != 1 || n[0].agentID != waiting || !strings.HasPrefix(n[0].h3, "In line for stuck since ") ||
		!strings.Contains(n[0].h3, "“Build it” holds it until ") {
		t.Fatalf("%+v", n)
	}
	mustLease(t)(d.releaseResource(holder, leaseArgs("stuck")))
	d.checkStalls(time.Now().Add(leaseStallAfter + 2*time.Minute))
	if n := stallNotices(d); len(n) != 0 {
		t.Fatalf("the notice outlived the stall: %+v", n)
	}
}

// The Mac sleeps past a lease's end and a wait's deadline. Its timers do not count the
// sleep, so the wake acts on both at once: the lease goes to the line, and the wait is told
// its time came. Not parallel: it moves the daemon's clock.
func TestAWakeEndsWhatCameDueInTheSleep(t *testing.T) {
	fakeRuntimes(t)
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	c.drain()
	dir := t.TempDir()
	holder, next := seeded(t, d, dir, nil).ID, seeded(t, d, dir, nil).ID
	sleeper := seeded(t, d, dir, nil)
	m := appClient(t, d, sleeper.ID)
	m.post("initialize", map[string]any{}, 1)
	mustLease(t)(d.leaseResource(holder, leaseArgs("build", "minutes", 10)))
	mustLease(t)(d.leaseResource(next, leaseArgs("build")))
	answered := make(chan string, 1)
	go func() {
		text, _ := m.tool(2, "wait_for_event", map[string]any{"events": "time.reached", "until_minutes": 5})
		answered <- text
	}()
	waitFor(t, "the time wait", func() bool {
		a := d.Store.Agent(sleeper.ID)
		return a.EventWait != nil && a.EventWait.Ending == nil
	})
	at := time.Now().Add(2 * time.Hour)
	t.Cleanup(func() { setNowFunc(time.Now) })
	setNowFunc(func() time.Time { return at })
	d.machineChanged(machineChange{kind: "wake"})
	waitFor(t, "the lease handed on", func() bool {
		h := holdersOf(d, "build")
		return len(h) == 1 && h[0] == next
	})
	select {
	case text := <-answered:
		if !strings.HasPrefix(text, "The time you asked for (") || !strings.Contains(text, "late") {
			t.Fatal(text)
		}
	case <-afterLimit(t):
		waitFailed(t, "the wait was not told its time came")
	}
}
