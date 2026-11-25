package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/records"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/lineage"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// #4275: lineage/sync keeps each machine's links and its sessions' changes of state, bounded,
// says full when it has no copy of the links, and the sweep bounds them again and deletes a
// removed machine's.
func TestLineageSyncKeepsAMergedCopy(t *testing.T) {
	t.Parallel()
	mem := store.NewMemory()
	p := startPlane(t, mem, controllink.NewPrivateKey())
	host := p.host("OWNERS-MAC").member.Host
	ctx := context.Background()
	now := time.Now()
	session := wire.LineageNode{Kind: "session", Ref: "agents:session:#a1"}
	issue := wire.LineageNode{Kind: "issue", Ref: "github:issue:o/r#1"}
	pr := wire.LineageNode{Kind: "pr", Ref: "github:o/r!2"}
	send := func(sync wire.LineageSync) (wire.LineageSyncAnswer, error) {
		t.Helper()
		return sendLineageSync(t, p, host, sync)
	}
	links := func() []wire.LineageLink {
		t.Helper()
		return keptLineageLinks(ctx, t, mem, host)
	}
	states := func() []wire.LineageStateChange {
		t.Helper()
		return keptLineageStates(ctx, t, mem, host)
	}
	at := wire.DateOf(now.Add(-time.Hour))
	works := wire.LineageLink{From: session, To: issue, Rel: "works-on", Source: "start", At: at}
	working := wire.LineageStateChange{Node: session, State: wire.LineageSessionStateNameWorking, At: wire.DateOf(now)}

	// No copy yet: the states are kept, and the machine is asked for every link.
	answer, err := send(wire.LineageSync{Links: []wire.LineageLink{works}, States: []wire.LineageStateChange{working}})
	if err != nil || answer.Full == nil || !*answer.Full {
		t.Fatalf("want full: %+v %v", answer, err)
	}
	if got := states(); len(got) != 1 || got[0] != working {
		t.Fatalf("states %+v", got)
	}
	if object, _ := mem.Get(ctx, lineageLinksKey(host)); object != nil {
		t.Fatal("links kept without a whole send")
	}

	// The whole send, then a change over it: the link ended, and a new one.
	yes := true
	if answer, err := send(wire.LineageSync{Links: []wire.LineageLink{works}, Reset: &yes}); err != nil || answer.Full != nil {
		t.Fatalf("%+v %v", answer, err)
	}
	ended := works
	endedAt := wire.DateOf(now)
	ended.EndedAt = &endedAt
	opens := wire.LineageLink{From: session, To: pr, Rel: "opens", Source: "agent", At: wire.DateOf(now)}
	stopped := wire.LineageStateChange{Node: session, State: wire.LineageSessionStateNameStopped, At: wire.DateOf(now.Add(time.Second))}
	if answer, err := send(wire.LineageSync{Links: []wire.LineageLink{ended, opens}, States: []wire.LineageStateChange{stopped, working}}); err != nil || answer.Full != nil {
		t.Fatalf("%+v %v", answer, err)
	}
	got := links()
	if len(got) != 2 || got[0].EndedAt == nil || *got[0].EndedAt != endedAt || got[1] != opens {
		t.Fatalf("links %+v", got)
	}
	// A change sent again is kept once.
	if s := states(); len(s) != 2 || s[0] != working || s[1] != stopped {
		t.Fatalf("states %+v", s)
	}

	// A reset starts the links again; the states stay.
	if _, err := send(wire.LineageSync{Links: []wire.LineageLink{opens}, Reset: &yes}); err != nil {
		t.Fatal(err)
	}
	if got := links(); len(got) != 1 || got[0] != opens {
		t.Fatalf("after reset %+v", got)
	}

	// Refused: a state that is not a session's, and a send too big. (A state this build does
	// not know reads as stopped.)
	bad := []wire.LineageSync{
		{States: []wire.LineageStateChange{{Node: issue, State: wire.LineageSessionStateNameWorking, At: working.At}}},
		{Links: make([]wire.LineageLink, mostSyncLinks+1)},
	}
	for _, b := range bad {
		if _, err := send(b); err == nil {
			t.Fatalf("took %+v", b)
		}
	}

	// The sweep, 91 days on: the ended link and the old changes go, but each session's last
	// state. A removed machine's objects go once a day old.
	later := now.Add(91 * 24 * time.Hour)
	old := wire.LineageLink{From: session, To: issue, Rel: "reviews", Source: "agent", At: at, EndedAt: &endedAt}
	if _, err := send(wire.LineageSync{Links: []wire.LineageLink{old}}); err != nil {
		t.Fatal(err)
	}
	other := wire.LineageNode{Kind: "session", Ref: "agents:session:#b2"}
	archived := wire.LineageStateChange{Node: other, State: wire.LineageSessionStateNameArchived, At: wire.DateOf(now)}
	if _, err := send(wire.LineageSync{States: []wire.LineageStateChange{archived}}); err != nil {
		t.Fatal(err)
	}
	gone := lineageGraphPrefix + "gone-mac/links.json"
	data, _ := records.Encode(storedLinks{Links: []wire.LineageLink{}, At: records.At(now)})
	if _, err := mem.Put(ctx, gone, data, store.Always); err != nil {
		t.Fatal(err)
	}
	// One roll-up of the machine's changes (which bounds its links and states), and the
	// removed machine's object.
	if n, err := sweepLineageGraph(ctx, mem, p.s.records, later, 10); err != nil || n != 2 {
		t.Fatalf("swept %d %v", n, err)
	}
	if got := links(); len(got) != 1 || got[0] != opens {
		t.Fatalf("links after the sweep %+v", got)
	}
	if s := states(); len(s) != 2 || s[0] != archived || s[1] != stopped {
		t.Fatalf("states after the sweep %+v", s)
	}
	if object, _ := mem.Get(ctx, gone); object != nil {
		t.Fatal("a removed machine's links were kept")
	}
	wantNoLineageChanges(ctx, t, mem, host)
	if n, _ := sweepLineageGraph(ctx, mem, p.s.records, later, 10); n != 0 {
		t.Fatalf("swept again: %d", n)
	}
}

// sendLineageSync sends a lineage/sync from the machine and returns the control plane's
// answer.
func sendLineageSync(t *testing.T, p *plane, host string, sync wire.LineageSync) (wire.LineageSyncAnswer, error) {
	t.Helper()
	params, _ := json.Marshal(sync)
	raw, err := p.s.methods.HostSaid(host, "lineage/sync", params)
	var answer wire.LineageSyncAnswer
	if err == nil {
		b, _ := json.Marshal(raw)
		_ = json.Unmarshal(b, &answer)
	}
	return answer, err
}

// keptLineageLinks reads the links the store keeps for the machine: its links.json with its
// changes since put over it.
func keptLineageLinks(ctx context.Context, t *testing.T, mem store.Store, host string) []wire.LineageLink {
	t.Helper()
	object, err := mem.Get(ctx, lineageLinksKey(host))
	if err != nil || object == nil {
		t.Fatalf("no links: %v", err)
	}
	var kept storedLinks
	if err := json.Unmarshal(object.Data, &kept); err != nil {
		t.Fatal(err)
	}
	for _, c := range keptLineageChanges(ctx, t, mem, host) {
		kept.Links = mergeLinks(kept.Links, c.Links)
	}
	return lineage.Bound(kept.Links, time.Now())
}

// keptLineageStates reads the changes of state the store keeps for the machine: its
// states.json and its changes since.
func keptLineageStates(ctx context.Context, t *testing.T, mem store.Store, host string) []wire.LineageStateChange {
	t.Helper()
	var kept storedStates
	object, err := mem.Get(ctx, lineageStatesKey(host))
	if err != nil {
		t.Fatal(err)
	}
	if object != nil {
		if err := json.Unmarshal(object.Data, &kept); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range keptLineageChanges(ctx, t, mem, host) {
		kept.States = append(kept.States, c.States...)
	}
	if len(kept.States) == 0 {
		t.Fatal("no states")
	}
	return boundStates(kept.States, time.Now())
}

// keptLineageChanges is the machine's changes not rolled up yet, oldest first.
func keptLineageChanges(ctx context.Context, t *testing.T, mem store.Store, host string) []storedChanges {
	t.Helper()
	keys, err := mem.List(ctx, lineageChangesPrefix(host))
	if err != nil {
		t.Fatal(err)
	}
	var out []storedChanges
	for _, k := range keys {
		object, err := mem.Get(ctx, k.Key)
		if err != nil || object == nil {
			t.Fatalf("change %s: %v", k.Key, err)
		}
		var c storedChanges
		if err := json.Unmarshal(object.Data, &c); err != nil {
			t.Fatal(err)
		}
		out = append(out, c)
	}
	return out
}

// bytesStore counts the bytes each Put under prefix writes. The plane's own timed writes, as
// its capacity snapshot every controlCapacityEvery, are elsewhere and not counted (#6994).
type bytesStore struct {
	store.Store
	prefix  string
	mu      sync.Mutex
	written int
}

func (c *bytesStore) Put(ctx context.Context, key string, data []byte, when store.Condition) (string, error) {
	if strings.HasPrefix(key, c.prefix) {
		c.mu.Lock()
		c.written += len(data)
		c.mu.Unlock()
	}
	return c.Store.Put(ctx, key, data, when)
}

func (c *bytesStore) take() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := c.written
	c.written = 0
	return n
}

// #6584: a lineage/sync of one change writes the same bytes, whatever the machine holds;
// a read gives the same answer before and after a roll-up.
func TestLineageSyncWritesOnlyTheChange(t *testing.T) {
	t.Parallel()
	counting := &bytesStore{Store: store.NewMemory(), prefix: lineageGraphPrefix}
	p := startPlane(t, counting, controllink.NewPrivateKey())
	small := p.host("SMALL-MAC").member.Host
	big := p.host("BIG-MAC").member.Host
	ctx := context.Background()
	now := time.Now()
	session := wire.LineageNode{Kind: "session", Ref: "agents:session:#a1"}
	linkTo := func(i int) wire.LineageLink {
		return wire.LineageLink{From: session, To: wire.LineageNode{Kind: "issue", Ref: fmt.Sprintf("github:issue:o/r#%05d", i)},
			Rel: "works-on", Source: "start", At: wire.DateOf(now.Add(-time.Hour))}
	}
	yes := true
	// The small machine holds one link, the big one 3 sends of mostSyncLinks.
	if _, err := sendLineageSync(t, p, small, wire.LineageSync{Links: []wire.LineageLink{linkTo(0)}, Reset: &yes}); err != nil {
		t.Fatal(err)
	}
	for part := range 3 {
		links := make([]wire.LineageLink, mostSyncLinks)
		for i := range links {
			links[i] = linkTo(part*mostSyncLinks + i)
		}
		sync := wire.LineageSync{Links: links}
		if part == 0 {
			sync.Reset = &yes
		}
		if _, err := sendLineageSync(t, p, big, sync); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := sweepLineageGraph(ctx, counting, p.s.records, now, 10); err != nil {
		t.Fatal(err)
	}
	if got := len(keptLineageLinks(ctx, t, counting, big)); got != 3*mostSyncLinks {
		t.Fatalf("big holds %d links", got)
	}

	one := func(host string, i int) int {
		t.Helper()
		counting.take()
		// A timed write of the plane's between two changes is not a part of either (#6994).
		p.s.keepControlCapacity(controlCapacity{at: time.Now()})
		ended := linkTo(i)
		end := wire.DateOf(now)
		ended.EndedAt = &end
		state := wire.LineageStateChange{Node: session, State: wire.LineageSessionStateNameWorking, At: wire.DateOf(now)}
		if _, err := sendLineageSync(t, p, host, wire.LineageSync{Links: []wire.LineageLink{ended}, States: []wire.LineageStateChange{state}}); err != nil {
			t.Fatal(err)
		}
		return counting.take()
	}
	smallBytes, bigBytes := one(small, 0), one(big, 0)
	if smallBytes == 0 || smallBytes != bigBytes || bigBytes > 1024 {
		t.Fatalf("one change wrote %d bytes on the small machine, %d on the big one", smallBytes, bigBytes)
	}

	// The read's answer is the same before and after a roll-up.
	scope := func() *mergedLineage {
		t.Helper()
		g, err := p.s.keptLineageGraph(ctx, func(string) bool { return true })
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	each := func(from, to int) {
		t.Helper()
		for i := from; i < to; i++ {
			if n := one(big, i); n != bigBytes {
				t.Fatalf("change %d wrote %d bytes, not %d", i, n, bigBytes)
			}
		}
	}
	each(1, mostLineageChanges-1)
	before := scope()
	if s, ok := before.states[session]; endedLinks(before.links) != mostLineageChanges-1 || !ok || s.State != wire.LineageSessionStateNameWorking {
		t.Fatalf("the changes were not read: %d ended, %+v", endedLinks(before.links), before.states)
	}
	if n, err := sweepLineageGraph(ctx, counting, p.s.records, now, 10); err != nil || n != 2 {
		t.Fatalf("swept %d %v", n, err)
	}
	if after := scope(); !reflect.DeepEqual(before.links, after.links) || !reflect.DeepEqual(before.hostOf, after.hostOf) ||
		!reflect.DeepEqual(before.states, after.states) {
		t.Fatal("a roll-up changed the read's answer")
	}
}

// #6863, #7052: the send that makes mostLineageChanges merges them into one, at the cost of
// what they bring, not of what the machine holds; only changes of lineageRollUpBytes roll up.
func TestLineageSyncMergesSmallChanges(t *testing.T) {
	t.Parallel()
	counting := &bytesStore{Store: store.NewMemory(), prefix: lineageGraphPrefix}
	p := startPlane(t, counting, controllink.NewPrivateKey())
	host := p.host("MAC").member.Host
	ctx := context.Background()
	now := time.Now()
	session := wire.LineageNode{Kind: "session", Ref: "agents:session:#a1"}
	links := make([]wire.LineageLink, mostSyncLinks)
	for i := range links {
		links[i] = wire.LineageLink{From: session, To: wire.LineageNode{Kind: "issue", Ref: fmt.Sprintf("github:issue:o/r#%05d", i)},
			Rel: "works-on", Source: "start", At: wire.DateOf(now.Add(-time.Hour))}
	}
	send := func(sync wire.LineageSync) int {
		t.Helper()
		counting.take()
		if _, err := sendLineageSync(t, p, host, sync); err != nil {
			t.Fatal(err)
		}
		return counting.take()
	}
	scope := func() *mergedLineage {
		t.Helper()
		g, err := p.s.keptLineageGraph(ctx, func(string) bool { return true })
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	yes := true
	whole := send(wire.LineageSync{Links: links, Reset: &yes})
	end := wire.DateOf(now)
	last := 0
	for i := range mostLineageChanges {
		ended := links[i]
		ended.EndedAt = &end
		last = send(wire.LineageSync{Links: []wire.LineageLink{ended}})
	}
	if last > whole/10 {
		t.Fatalf("%d bytes for a merge of %d changes; the machine's links are %d", last, mostLineageChanges-1, whole)
	}
	if left := keptLineageChanges(ctx, t, counting, host); len(left) != 2 {
		t.Fatalf("%d changes left after the merge", len(left))
	}
	before := scope()
	if n, err := sweepLineageGraph(ctx, counting, p.s.records, now, 10); err != nil || n != 1 {
		t.Fatalf("swept %d %v", n, err)
	}
	if after := scope(); !reflect.DeepEqual(before.links, after.links) || endedLinks(after.links) != mostLineageChanges {
		t.Fatal("a roll-up of merged changes changed the read's answer")
	}
	wantNoLineageChanges(ctx, t, counting, host)

	for range mostLineageChanges {
		send(wire.LineageSync{Links: links})
	}
	wantNoLineageChanges(ctx, t, counting, host)
	// A link sent again as it was before it ended stays ended.
	if got := endedLinks(scope().links); got != mostLineageChanges {
		t.Fatalf("%d ended", got)
	}
}

// wantNoLineageChanges fails unless the machine's changes are all rolled up.
func wantNoLineageChanges(ctx context.Context, t *testing.T, st store.Store, host string) {
	t.Helper()
	if left := keptLineageChanges(ctx, t, st, host); len(left) != 0 {
		t.Fatalf("%d changes left after the roll-up", len(left))
	}
}

// endedLinks counts the links that ended.
func endedLinks(links []wire.LineageLink) int {
	n := 0
	for _, l := range links {
		if l.EndedAt != nil {
			n++
		}
	}
	return n
}

// #4275: the states are bounded by count, the oldest that are not a session's latest first.
func TestLineageStatesAreBounded(t *testing.T) {
	t.Parallel()
	now := time.Now()
	var changes []wire.LineageStateChange
	for i := range lineage.MostStates + 10 {
		changes = append(changes, wire.LineageStateChange{Node: wire.LineageNode{Kind: "session", Ref: "agents:session:#x"},
			State: wire.LineageSessionStateNameWorking, At: wire.DateOf(now.Add(time.Duration(i) * time.Second))})
	}
	kept := boundStates(changes, now)
	if len(kept) != lineage.MostStates || kept[0].At != changes[10].At {
		t.Fatalf("%d, first %v", len(kept), kept[0].At)
	}
}
