package controlplane

import (
	"context"
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/records"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/lineage"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// fillLineage puts a machine's links and states at their bounds in st, with changes sends of
// 8 links each that are not rolled up (#7052), and returns those 8 links.
func fillLineage(ctx context.Context, t testing.TB, st store.Store, host string, changes int) []wire.LineageLink {
	t.Helper()
	now := time.Now()
	links := make([]wire.LineageLink, lineage.MostLinks)
	states := make([]wire.LineageStateChange, lineage.MostStates)
	for i := range links {
		session := wire.LineageNode{Kind: "session", Ref: fmt.Sprintf("agents:session:o/r#%08d-EFC4-43F5-BC89-9ADFBD272381", i/4)}
		at := wire.DateOf(now.Add(-time.Duration(len(links)-i) * time.Minute))
		links[i] = wire.LineageLink{From: session, To: wire.LineageNode{Kind: "issue", Ref: fmt.Sprintf("github:issue:o/r#%05d", i)},
			Rel: "works-on", Source: "start", At: at}
		states[i] = wire.LineageStateChange{Node: session, State: wire.LineageSessionStateNameWorking, At: at}
	}
	put := func(key string, v any) {
		data, err := records.Encode(v)
		if err == nil {
			_, err = st.Put(ctx, key, data, store.Always)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	put(lineageLinksKey(host), storedLinks{Links: links, At: records.At(now)})
	put(lineageStatesKey(host), storedStates{States: states, At: records.At(now)})
	again := links[:8]
	for i := range changes {
		put(lineageChangeKey(host, now.Add(time.Duration(i))), storedChanges{Links: again, At: records.At(now)})
	}
	return again
}

// readLineageLinks is addLinks over the bounds with changes sends not rolled up.
func readLineageLinks(tb testing.TB, changes int) func() {
	tb.Helper()
	ctx := context.Background()
	st := store.NewMemory()
	again := fillLineage(ctx, tb, st, "mac", 0)
	object, err := st.Get(ctx, lineageLinksKey("mac"))
	if err != nil || object == nil {
		tb.Fatalf("no links: %v", err)
	}
	sent := make([]storedChanges, changes)
	for i := range sent {
		sent[i].Links = again
	}
	return func() {
		g := &mergedLineage{byNode: map[wire.LineageNode][]int{}}
		g.addLinks(object.Data, sent, "mac", map[syncLinkKey]bool{})
		if len(g.links) != lineage.MostLinks {
			tb.Fatalf("read %d links", len(g.links))
		}
	}
}

func BenchmarkLineageRead(b *testing.B) {
	for _, changes := range []int{0, 1, mostLineageChanges - 1} {
		b.Run(fmt.Sprintf("changes=%d", changes), func(b *testing.B) {
			read := readLineageLinks(b, changes)
			b.ReportAllocs()
			for b.Loop() {
				read()
			}
		})
	}
}

// #7052: a read merges a machine's changes once, whatever their number: a merge for each
// change indexed and sorted every kept link again, up to 31 times in each read.
//
//nolint:paralleltest // it measures the process's allocation
func TestLineageReadCostsTheSameForEachChange(t *testing.T) {
	allocated := func(changes int) uint64 {
		read := readLineageLinks(t, changes)
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		read()
		runtime.ReadMemStats(&after)
		return after.TotalAlloc - before.TotalAlloc
	}
	one, most := allocated(1), allocated(mostLineageChanges-1)
	if most > one*3/2 {
		t.Fatalf("a read allocated %d bytes with 1 change, %d with %d", one, most, mostLineageChanges-1)
	}
}

// #6863: a roll-up at the bounds, over one change.
func BenchmarkLineageRollUp(b *testing.B) {
	ctx := context.Background()
	st := store.NewMemory()
	fillLineage(ctx, b, st, "mac", 0)
	b.ReportAllocs()
	for b.Loop() {
		state := wire.LineageStateChange{Node: wire.LineageNode{Kind: "session", Ref: "agents:session:o/r#1"},
			State: wire.LineageSessionStateNameWorking, At: wire.DateOf(time.Now())}
		if _, err := rollUpLineage(ctx, st, "mac", nil, []wire.LineageStateChange{state}, time.Now()); err != nil {
			b.Fatal(err)
		}
	}
}
