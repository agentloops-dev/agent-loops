package records

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
)

// #6758: a new machine over the machines' limit is refused, and so is a label over it; one
// joined is renewed and says its hello all the same, and a removal makes room.
func TestMachinesRefuseAtTheirLimit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := store.NewMemory()
	r := New(s)
	machine := func(n int) MachineRecord {
		return MachineRecord{ID: fmt.Sprintf("m%03d", n), Name: strings.Repeat("n", 30<<10), PublicKey: fmt.Appendf(nil, "key %d", n)}
	}
	n := 0
	var full *MachineLimitError
	for ; n < 100; n++ {
		if err := r.AddHost(ctx, machine(n)); errors.As(err, &full) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
	}
	if n == 100 {
		t.Fatal("no limit")
	}
	// Fill to the limit with an object nobody can read, which counts too.
	if _, err := s.Put(ctx, HostKey("filler"), make([]byte, MachineMostBytes), store.Absent); err != nil {
		t.Fatal(err)
	}
	if err := r.SaveHost(ctx, MachineRecord{ID: "new"}); !errors.As(err, &full) {
		t.Fatalf("a new machine: %v", err)
	}
	if err := r.MachineRoom(ctx, []byte{9}); !errors.As(err, &full) {
		t.Fatalf("room for one more: %v", err)
	}
	if err := r.MachineRoom(ctx, machine(0).PublicKey); err != nil {
		t.Fatalf("a joined machine has its room: %v", err)
	}
	later := Now()
	if err := r.ChangeHost(ctx, "m000", func(h *MachineRecord) bool {
		h.Expires, h.Extra = &later, Extra{"capabilities": json.RawMessage(`["a"]`)}
		return true
	}); err != nil {
		t.Fatalf("a machine in use: %v", err)
	}
	label := func(h *MachineRecord) bool { h.Labels = []string{"gpu"}; return true }
	if err := r.ChangeHost(ctx, "m000", label); !errors.As(err, &full) {
		t.Fatalf("a label over the limit: %v", err)
	}
	for _, id := range []string{"m001", "m002"} {
		if _, err := r.RemoveHost(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.ChangeHost(ctx, "m000", label); err != nil {
		t.Fatal(err)
	}
	if err := r.AddHost(ctx, MachineRecord{ID: "new"}); err != nil {
		t.Fatalf("a removal made no room: %v", err)
	}
}

// A record over its largest is refused; one that is larger already, from before the limit,
// is renewed all the same.
func TestAMachineRecordHasALargestSize(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := store.NewMemory()
	r := New(s)
	h := MachineRecord{ID: "big", Name: strings.Repeat("n", MachineMostBytes)}
	var full *MachineLimitError
	if err := r.AddHost(ctx, h); !errors.As(err, &full) {
		t.Fatalf("a record over the largest: %v", err)
	}
	data, err := Encode(h)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(ctx, HostKey(h.ID), data, store.Absent); err != nil {
		t.Fatal(err)
	}
	if err := r.Load(ctx); err != nil {
		t.Fatal(err)
	}
	later := Now()
	if err := r.ChangeHost(ctx, h.ID, func(h *MachineRecord) bool { h.Expires = &later; return true }); err != nil {
		t.Fatalf("not renewed: %v", err)
	}
	if err := r.ChangeHost(ctx, h.ID, func(h *MachineRecord) bool { h.Name += "n"; return true }); !errors.As(err, &full) {
		t.Fatalf("grew: %v", err)
	}
}

// Linked or renewed at another copy since it was read: it stays. A tombstone keeps nothing
// that grows.
func TestRemoveHostIfReadsAgain(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := store.NewMemory()
	here, there := New(s), New(s)
	if err := here.AddHost(ctx, MachineRecord{ID: "m", Labels: []string{"gpu"}}); err != nil {
		t.Fatal(err)
	}
	if err := there.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if err := there.ChangeHost(ctx, "m", func(h *MachineRecord) bool { h.Workspace = json.RawMessage(`{"provider":"p"}`); return true }); err != nil {
		t.Fatal(err)
	}
	unlinked := func(h MachineRecord) bool { return len(h.Workspace) == 0 }
	if went, err := here.RemoveHostIf(ctx, "m", unlinked); went || err != nil || here.Host("m") == nil {
		t.Fatalf("a linked machine went: %v %v", went, err)
	}
	if went, err := here.RemoveHost(ctx, "m"); !went || err != nil {
		t.Fatal(went, err)
	}
	o, err := s.Get(ctx, HostKey("m"))
	if err != nil {
		t.Fatal(err)
	}
	var tombstone MachineRecord
	if err := json.Unmarshal(o.Data, &tombstone); err != nil || len(tombstone.Labels) > 0 || len(tombstone.Workspace) > 0 {
		t.Fatalf("%s %v", o.Data, err)
	}
}

// #7304: the control plane's own record, with only the control role, is written at the limit
// and takes no machine's room; with the agents role too it counts.
func TestAControlOnlyMachineIsNotInTheLimit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := store.NewMemory()
	r := New(s)
	var full *MachineLimitError
	for n := 0; ; n++ {
		if n == 100 {
			t.Fatal("no limit")
		}
		err := r.AddHost(ctx, MachineRecord{ID: fmt.Sprintf("m%03d", n), Name: strings.Repeat("n", 30<<10)})
		if errors.As(err, &full) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	// No room is left for a small machine either.
	if _, err := s.Put(ctx, HostKey("filler"), make([]byte, MachineMostBytes), store.Absent); err != nil {
		t.Fatal(err)
	}
	control := MachineRecord{ID: "control", Name: "Box", Roles: []string{RoleControl}}
	if err := r.AddHost(ctx, control); err != nil {
		t.Fatalf("the control plane's own record at the limit: %v", err)
	}
	if err := r.ChangeHost(ctx, "control", func(h *MachineRecord) bool { h.Labels = []string{"eu"}; return true }); err != nil {
		t.Fatalf("a change to it at the limit: %v", err)
	}
	both := *r.Host("control")
	both.Roles = []string{RoleAgents, RoleControl}
	if err := r.SaveHost(ctx, both); !errors.As(err, &full) {
		t.Fatalf("the agents role at the limit: %v", err)
	}
	// It takes no room: the machines are as large with it as without it.
	size := func() int {
		grow := 0
		for r.machinesWithin(ctx, grow) == nil {
			grow += 16
		}
		return MachinesMostBytes - grow
	}
	for _, id := range []string{"m000", "m001", "m002"} {
		if _, err := r.RemoveHost(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	with := size()
	if err := r.SaveHost(ctx, both); err != nil {
		t.Fatal(err)
	}
	if got := size(); got <= with {
		t.Fatalf("with both roles it does not count: %d, and %d before", got, with)
	}
}
