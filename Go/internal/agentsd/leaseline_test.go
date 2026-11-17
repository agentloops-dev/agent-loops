package agentsd

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// A lease's line in `by` order (#4268).

func TestLeaseByReadsMinutesAndTimes(t *testing.T) {
	at := time.Date(2026, 11, 9, 14, 0, 0, 0, time.Local)
	for _, tc := range []struct {
		raw  string
		want time.Time
	}{
		{`5`, at.Add(5 * time.Minute)},
		{`"5"`, at.Add(5 * time.Minute)},
		{`"14:30"`, at.Add(30 * time.Minute)},
		{`"2026-11-09T22:10:00Z"`, time.Date(2026, 11, 9, 22, 10, 0, 0, time.UTC)},
	} {
		got := leaseBy(json.RawMessage(tc.raw), at)
		if got == nil || !got.Time().Equal(tc.want) {
			t.Fatalf("%s: %v, want %v", tc.raw, got, tc.want)
		}
	}
	for _, raw := range []string{``, `0`, `"soon"`, `true`} {
		if got := leaseBy(json.RawMessage(raw), at); got != nil {
			t.Fatalf("%q: %v", raw, *got)
		}
	}
}

func TestTheLineIsInByOrder(t *testing.T) {
	at := time.Date(2026, 11, 9, 14, 0, 0, 0, time.UTC)
	when := func(m int) *wire.Date { d := wire.DateOf(at.Add(time.Duration(m) * time.Minute)); return &d }
	e := &leaseEntry{}
	w := func(id string, asked int, by *wire.Date) waiter {
		return waiter{AgentID: wire.UUID(id), AskedAt: wire.DateOf(at.Add(time.Duration(asked) * time.Second)), By: by}
	}
	same := func(wire.UUID) int { return 2 }
	places := []int{
		joinLine(e, w("none-1", 1, nil), same),
		joinLine(e, w("by-10", 2, when(10)), same),
		joinLine(e, w("by-5", 3, when(5)), same),
		joinLine(e, w("none-2", 4, nil), same),
		joinLine(e, w("by-5-later", 5, when(5)), same),
	}
	var got []string
	for _, l := range e.Line {
		got = append(got, string(l.AgentID))
	}
	if strings.Join(got, " ") != "by-5 by-5-later by-10 none-1 none-2" || places[2] != 1 || places[3] != 4 || places[4] != 2 {
		t.Fatalf("%v %v", got, places)
	}
}

// A lease's line and the start queue go by the session's priority (#5848).

func lineDaemon(t *testing.T) (*Daemon, string) {
	t.Helper()
	d, err := New(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Shutdown)
	t.Cleanup(d.stopFolderWatches)
	return d, t.TempDir()
}

func savePrioritised(t *testing.T, d *Daemon, folder string, state wire.AgentState, created wire.Date, p *wire.SessionPriority) wire.UUID {
	t.Helper()
	id := store.NewUUID()
	starter := store.NewUUID()
	if err := d.Store.Save(wire.Agent{ID: id, RuntimeID: "claude", Cwd: store.FolderURL(folder), State: state,
		CreatedAt: created, LastActivityAt: created, StartedByAgent: &starter, PrioritySet: p}); err != nil {
		t.Fatal(err)
	}
	d.refreshPriorities(map[wire.UUID]bool{id: true})
	return id
}

func TestAHighSessionIsGrantedBeforeEarlierOnesWithNone(t *testing.T) {
	d, folder := lineDaemon(t)
	high, low := wire.SessionPriorityHigh, wire.SessionPriorityLow
	holder := savePrioritised(t, d, folder, wire.AgentStateRunning, 1, nil)
	none1 := savePrioritised(t, d, folder, wire.AgentStateRunning, 2, nil)
	none2 := savePrioritised(t, d, folder, wire.AgentStateRunning, 3, nil)
	lowID := savePrioritised(t, d, folder, wire.AgentStateRunning, 4, &low)
	highID := savePrioritised(t, d, folder, wire.AgentStateRunning, 5, &high)
	at := now()
	d.leaseMu.Lock()
	d.loadLeases()
	e := &leaseEntry{Kind: "named", DisplayName: "build", Rules: standardRules, Line: []waiter{},
		Leases: []wire.Lease{{Resource: "build", DisplayName: "build", Holder: holder, GrantedAt: at, ExpiresAt: wire.DateOf(at.Time().Add(time.Hour))}}}
	var places []int
	for i, id := range []wire.UUID{none1, none2, lowID, highID} {
		places = append(places, joinLine(e, waiter{AgentID: id, AskedAt: wire.DateOf(at.Time().Add(time.Duration(i) * time.Second))}, d.lineRank))
	}
	ahead := higherAhead(e, none2, d.lineRank)
	d.book.Entries["build"] = e
	// The holder is not disturbed: the line only moves.
	held := len(e.Leases) == 1 && e.Leases[0].Holder == holder
	e.Leases = nil
	events := d.handOn("build", e)
	d.leaseMu.Unlock()
	if !held || places[3] != 1 || places[2] != 4 || ahead != 1 {
		t.Fatalf("held %v, places %v, ahead %d", held, places, ahead)
	}
	if len(events) != 1 || events[0].lease.Holder != highID {
		t.Fatalf("granted %+v, want the high session", events)
	}
	if got := lineOf(d, "build"); len(got) != 3 || got[0] != none1 || got[1] != none2 || got[2] != lowID {
		t.Fatalf("line %v", got)
	}
	raised := d.raiseLeaseEvent(events[0])
	if raised == nil || raised.Details["priority"] != "high" || raised.Details["waited_seconds"] == "" {
		t.Fatalf("event %+v", raised)
	}
}

func TestAPriorityChangedWhileWaitingMovesItAhead(t *testing.T) {
	d, folder := lineDaemon(t)
	low, high := wire.SessionPriorityLow, wire.SessionPriorityHigh
	first := savePrioritised(t, d, folder, wire.AgentStateRunning, 1, nil)
	second := savePrioritised(t, d, folder, wire.AgentStateRunning, 2, nil)
	waiting := savePrioritised(t, d, folder, wire.AgentStateRunning, 3, &low)
	at := now()
	d.leaseMu.Lock()
	d.loadLeases()
	e := &leaseEntry{Kind: "named", DisplayName: "ci", Rules: standardRules, Line: []waiter{},
		Leases: []wire.Lease{{Resource: "ci", DisplayName: "ci", Holder: store.NewUUID(), GrantedAt: at, ExpiresAt: wire.DateOf(at.Time().Add(time.Hour))}}}
	for i, id := range []wire.UUID{waiting, first, second} {
		joinLine(e, waiter{AgentID: id, AskedAt: wire.DateOf(at.Time().Add(time.Duration(i) * time.Second))}, d.lineRank)
	}
	d.book.Entries["ci"] = e
	d.leaseMu.Unlock()
	if got := lineOf(d, "ci"); got[2] != waiting {
		t.Fatalf("a low session first in time is behind: %v", got)
	}
	if _, err := d.setPriority(waiting, &high); err != nil {
		t.Fatal(err)
	}
	if got := lineOf(d, "ci"); got[0] != waiting || got[1] != first || got[2] != second {
		t.Fatalf("after low to high: %v", got)
	}
}

func TestTheStartQueueStartsAHighHelperBeforeAnOlderOne(t *testing.T) {
	d, folder := lineDaemon(t)
	high := wire.SessionPriorityHigh
	older := savePrioritised(t, d, folder, wire.AgentStateQueued, 10, nil)
	newer := savePrioritised(t, d, folder, wire.AgentStateQueued, 20, &high)
	q := d.helperQueue(folder)
	if len(q) != 2 || q[0].ID != newer || q[1].ID != older {
		t.Fatalf("queue %v", q)
	}
}

func TestTheInLineReplySaysWhoIsAheadByPriority(t *testing.T) {
	if got := placeWords(3, 2); got != "3rd in line; 2 sessions with a higher priority are ahead" {
		t.Fatal(got)
	}
	if got := placeWords(2, 1); got != "2nd in line; 1 session with a higher priority is ahead" {
		t.Fatal(got)
	}
	if got := placeWords(2, 0); got != "2nd in line" {
		t.Fatal(got)
	}
}
