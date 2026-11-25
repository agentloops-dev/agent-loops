package hosttunnels

import (
	"fmt"
	"strings"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/records"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
)

// #7249: a tunnel has a largest record and a count. A refusal leaves the tunnel that is held, and
// one over the limit already is kept, and takes a key no larger.
func TestATunnelIsRefusedOverItsLimitsAndTheHeldOneStays(t *testing.T) {
	st := store.NewMemory()
	tunnels := newTunnels(st, "/usr/bin/false", &heardChanges{}, []records.MachineRecord{{ID: "ws", Name: "ws"}})
	defer tunnels.Stop()
	r := record("ws")
	if err := tunnels.Add(ctx, r); err != nil {
		t.Fatal(err)
	}
	large := record("ws")
	large.ID, large.KnownHosts = "large", strings.Repeat("k", mostRecordBytes)
	if tunnels.Room(large) == nil || tunnels.Add(ctx, large) == nil {
		t.Fatal("a tunnel over the largest was taken")
	}
	if ok, err := tunnels.Repin(ctx, "ws", large.KnownHosts); !ok || err == nil {
		t.Fatalf("%v %v", ok, err)
	}
	if _, keys, _ := tunnels.Installed("ws"); keys != r.KnownHosts {
		t.Fatalf("the held key changed to %d bytes", len(keys))
	}
	if o, _ := st.Get(ctx, Key(r.ID)); o == nil {
		t.Fatal("the held tunnel went")
	}
	// One from before the limit.
	tunnels.mu.Lock()
	old := tunnels.records[r.ID]
	old.KnownHosts = strings.Repeat("o", 2*mostRecordBytes)
	tunnels.records[r.ID] = old
	tunnels.mu.Unlock()
	if ok, err := tunnels.Repin(ctx, "ws", strings.Repeat("n", 2*mostRecordBytes)); !ok || err != nil {
		t.Fatalf("%v %v", ok, err)
	}
	tunnels.mu.Lock()
	for i := len(tunnels.records); i < mostTunnels; i++ {
		id := fmt.Sprint("t", i)
		tunnels.records[id] = Record{ID: id, Destination: id}
	}
	tunnels.mu.Unlock()
	if err := tunnels.Room(Record{ID: "again", Destination: strings.ToUpper(r.Destination)}); err != nil {
		t.Fatalf("the same server again: %v", err)
	}
	if tunnels.Room(Record{ID: "more", Destination: "more"}) == nil {
		t.Fatal("one more than the count was taken")
	}
}
