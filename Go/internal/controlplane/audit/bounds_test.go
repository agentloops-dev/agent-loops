package audit

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/records"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// #6755: the worst texts make an entry that fits its limit.
func TestAnEntryIsCutToItsSize(t *testing.T) {
	long := strings.Repeat("\x01", 5000)
	e, _ := entry(wire.AuditActor{
		User: long, UserName: &long, Client: new(wire.UUID(long)), ClientName: &long,
		Event:   &wire.AuditCause{Kind: long, ID: long},
		Scim:    &wire.AuditSCIM{Provider: long, Token: long},
		Refresh: &wire.AuditRefresh{Provider: long},
	}, long, long, long, &long, time.Now())
	e.Chain, e.Seq, e.Prev = "aaaa1111", 1<<62, Hash(nil)
	data, err := records.Encode(e)
	if err != nil || len(data) > MostEntryBytes {
		t.Fatal(len(data), err)
	}
	if got := clip("aé", 4); got != "aé" {
		t.Fatal(got)
	}
	if got := clip("ééé", 5); got != "é…" {
		t.Fatal(got)
	}
}

// #6755: the oldest entries evicted by the count are not tampering, and an entry gone after
// the first one kept is.
func TestEntriesEvictedByTheCountAreNotProblems(t *testing.T) {
	ctx := context.Background()
	s := store.NewMemory()
	at := time.Date(2026, 11, 20, 23, 50, 0, 0, time.UTC)
	gone, stays := NewChain("aaaa1111"), NewChain("bbbb2222")
	write := func(c *Chain, when time.Time) {
		l := &Log{Store: s, Now: func() time.Time { return when }, Chain: c}
		must(t, l.Write(ctx, wire.AuditActor{User: "alex"}, "agents/stop", "Stopped an agent", "", nil))
	}
	for n := range 3 {
		write(gone, at.Add(time.Duration(n)*time.Second))
		write(stays, at.Add(time.Duration(n+10)*time.Second))
	}
	write(gone, at.Add(24*time.Hour))
	write(stays, at.Add(24*time.Hour))
	must(t, gone.Seal(ctx, s, at.Add(25*time.Hour)))
	must(t, stays.Seal(ctx, s, at.Add(25*time.Hour)))

	log, err := store.OpenDayLog(store.Spec{Name: "audit", Kind: "log", Scope: "o", Bounds: store.Bounds{
		MaxRecordBytes: MostEntryBytes, MaxCount: 4, MaxAge: time.Hour}}, Prefix, s)
	must(t, err)
	if n, err := log.Sweep(ctx, at.Add(25*time.Hour), at, 1000); n != 4 || err != nil {
		t.Fatal(n, err)
	}
	if r := verified(t, s); !r.OK() || r.Entries != 4 {
		t.Fatalf("%+v", r)
	}
	must(t, s.Delete(ctx, keyOf(t, s, "bbbb2222", 3)))
	if r := verified(t, s); r.OK() {
		t.Fatal("an entry removed was not seen")
	}
}
