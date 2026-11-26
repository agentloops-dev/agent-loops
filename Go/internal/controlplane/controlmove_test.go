package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/records"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// moveRig is a control plane and a machine it can move to: the machine's host answers
// machine/usage and control/receive, the second by starting a copy of its own, empty, with
// the key it opened.
type moveRig struct {
	t      *testing.T
	p      *plane
	box    *fakeHost
	window *controllink.Session
	// receive starts the machine's copy with the key opened; nil starts one that receives.
	receive func(key []byte) *Service

	mu     sync.Mutex
	opened []byte
	fresh  *Service
}

func newMoveRig(t *testing.T) *moveRig {
	r := &moveRig{t: t}
	r.p = startPlane(t, store.NewMemory(), controllink.NewPrivateKey())
	r.p.asked = func(host, method string, params json.RawMessage) (any, bool) {
		switch method {
		case wire.CallMachineUsage.Name:
			return goodUsage(), true
		case wire.CallControlReceive.Name:
			return r.answerReceive(host, params), true
		}
		return nil, false
	}
	r.box = r.p.host("MACHINE-2")
	// Its uplink comes from a public address, where its control role answers.
	r.p.s.router.mu.Lock()
	r.p.s.router.hosts[r.box.member.Host].conn.(*Conn).Remote = "203.0.113.7"
	r.p.s.router.mu.Unlock()
	r.p.s.dialMachine = func(_ context.Context, address string) error {
		if address == "203.0.113.7:8791" {
			return nil
		}
		return errors.New("refused")
	}
	r.p.s.moving.dialEvery = 20 * time.Millisecond
	_, r.window = r.p.client("Window")
	return r
}

func (r *moveRig) answerReceive(host string, params json.RawMessage) any {
	var request wire.ControlReceiveRequest
	fakeMust(json.Unmarshal(params, &request))
	if request.URL != "https://203.0.113.7:8791" {
		return controllink.AskFailure{Code: invalidParams, Message: "not the address: " + request.URL}
	}
	key, err := controllink.Open(r.box.private, controllink.ControlMoveInfo, controllink.ControlMoveAD(request.Move, host, request.ControlKey), request.Key)
	if err != nil {
		return controllink.AskFailure{Code: notPermitted, Message: "did not open"}
	}
	var fresh *Service
	if r.receive != nil {
		fresh = r.receive(key)
	} else {
		fresh = r.startCopy(store.NewMemory(), key, true)
	}
	r.mu.Lock()
	r.opened, r.fresh = key, fresh
	r.mu.Unlock()
	return wire.ControlReceive{URL: fresh.cfg.URL}
}

// startCopy is the machine's own copy, on loopback.
func (r *moveRig) startCopy(st store.Store, key []byte, receive bool) *Service {
	port := freePort(r.t)
	s, err := New(Config{Store: st, PrivateKey: key, URL: "http://127.0.0.1:" + itoa(port), Bind: "127.0.0.1", Port: port,
		Name: "Moved", MachineID: "MACHINE-2", Receive: receive})
	fakeMust(err)
	_, err = s.Start(context.Background())
	fakeMust(err)
	r.t.Cleanup(s.Stop)
	return s
}

func (r *moveRig) move() wire.ControlMove {
	r.t.Helper()
	var out wire.ControlMoveResult
	must(r.t, json.Unmarshal(call(r.t, r.window, "", wire.CallControlMove.Name, map[string]any{"machine": r.box.member.Host}), &out))
	return out.Move
}

func (r *moveRig) moves() []wire.ControlMove {
	r.t.Helper()
	var out wire.ControlMoves
	must(r.t, json.Unmarshal(call(r.t, r.window, "", wire.CallControlMovesList.Name, nil), &out))
	return out.Moves
}

// waitEnded waits for the move to end, and is how it ended.
func (r *moveRig) waitEnded() wire.ControlMove {
	r.t.Helper()
	var last wire.ControlMove
	r.p.waitFor(func() bool {
		if r.p.s.moving.now() != "" {
			return false
		}
		// Read here: forwarding closes the window's socket.
		moves := r.p.s.listMoves(context.Background())
		if len(moves) == 0 {
			return false
		}
		last = moves[0]
		return true
	}, "the move to end")
	return last
}

// The whole move from one call: the key opens only at the machine, the new copy takes the
// store and serves, the old copy forwards to it for 30 days, and both keep the move.
func TestControlMoveMovesThePlaneToTheMachine(t *testing.T) {
	t.Parallel()
	r := newMoveRig(t)
	started := r.move()
	if started.Phase != wire.ControlMovePhaseChecking || started.By == "" || started.To != wire.MachineRef(r.box.member.Host) {
		t.Fatalf("%+v", started)
	}
	// One move at a time.
	if e := callError(t, r.window, "", wire.CallControlMove.Name, map[string]any{"machine": r.box.member.Host}); !strings.Contains(e, "Another move") &&
		!strings.Contains(e, "in a handover already") {
		t.Fatal(e)
	}
	ended := r.waitEnded()
	if ended.Phase != wire.ControlMovePhaseForwarding || ended.ForwardingUntil == nil || ended.Error != nil {
		t.Fatalf("%+v", ended)
	}
	if until := time.Until(ended.ForwardingUntil.Time()); until < 29*24*time.Hour || until > 30*24*time.Hour {
		t.Fatal(until)
	}
	r.mu.Lock()
	opened, fresh := r.opened, r.fresh
	r.mu.Unlock()
	if !bytes.Equal(opened, r.p.private) {
		t.Fatal("the machine did not open the control key")
	}
	if *ended.Address != fresh.cfg.URL {
		t.Fatal(*ended.Address)
	}
	if r.p.s.phase.now() != phaseForwarding || fresh.phase.now() != phaseServing {
		t.Fatal(r.p.s.phase.now(), fresh.phase.now())
	}
	// The members are sent to the new copy, which has the old one's records and this move.
	settings := r.p.s.records.Settings()
	if list := currentEndpoints(settings); len(list) != 1 || list[0].URL != fresh.cfg.URL {
		t.Fatalf("%+v", list)
	}
	if fresh.records.Host(r.box.member.Host) == nil {
		t.Fatal("the new copy has no machines")
	}
	kept, err := fresh.keptMoves(context.Background())
	must(t, err)
	if len(kept) != 1 || kept[0].ID != started.ID || kept[0].Phase != wire.ControlMovePhaseForwarding {
		t.Fatalf("%+v", kept)
	}
	// The key is in clear nowhere in the store.
	keys, err := r.p.store.List(context.Background(), "v1/moves/")
	must(t, err)
	for _, k := range keys {
		object, err := r.p.store.Get(context.Background(), k.Key)
		must(t, err)
		if bytes.Contains(object.Data, r.p.private) || strings.Contains(string(object.Data), controllink.B64URL(r.p.private)) {
			t.Fatal("the move's record holds the key")
		}
	}
}

// A move the machine can't take refuses at once, and keeps why.
func TestControlMoveRefusesAMachineThatIsNotEligible(t *testing.T) {
	t.Parallel()
	r := newMoveRig(t)
	r.p.s.dialMachine = func(context.Context, string) error { return errors.New("refused") }
	if e := callError(t, r.window, "", wire.CallControlMove.Name, map[string]any{"machine": r.box.member.Host}); !strings.Contains(e, "nothing answers") {
		t.Fatal(e)
	}
	moves := r.moves()
	if len(moves) != 1 || moves[0].Phase != wire.ControlMovePhaseFailed || *moves[0].FailedPhase != wire.ControlMovePhaseChecking {
		t.Fatalf("%+v", moves)
	}
	if e := callError(t, r.window, "", wire.CallControlMove.Name, map[string]any{"machine": r.p.s.controlMachineID()}); !strings.Contains(e, "already runs") {
		t.Fatal(e)
	}
	sam := r.p.invite(r.window, "Sam", wire.UserRoleMember)
	for _, method := range []string{wire.CallControlMove.Name, wire.CallControlMovesList.Name} {
		if e := callError(t, sam.window, "", method, map[string]any{"machine": r.box.member.Host}); !strings.Contains(e, "Only an admin") {
			t.Fatal(method, e)
		}
	}
}

// failingPuts refuses to write the settings: a store copy that fails half way.
type failingPuts struct{ store.Store }

func (f failingPuts) Put(ctx context.Context, key string, data []byte, when store.Condition) (string, error) {
	if key == records.SettingsKey {
		return "", errors.New("the disk is full")
	}
	return f.Store.Put(ctx, key, data, when)
}

// A step that fails leaves the old copy serving, and says which step.
func TestControlMoveThatFailsLeavesTheOldCopyServing(t *testing.T) {
	t.Parallel()
	r := newMoveRig(t)
	r.receive = func(key []byte) *Service { return r.startCopy(failingPuts{store.NewMemory()}, key, true) }
	r.move()
	ended := r.waitEnded()
	if ended.Phase != wire.ControlMovePhaseFailed || *ended.FailedPhase != wire.ControlMovePhaseCopying || !strings.Contains(*ended.Error, "the disk is full") {
		t.Fatalf("%+v", ended)
	}
	if r.p.s.phase.now() != phaseServing {
		t.Fatal(r.p.s.phase.now())
	}
	call(t, r.window, "", "clients/startPairing", nil)

	// A copy that is not empty is not filled.
	r.receive = func(key []byte) *Service { return r.startCopy(store.NewMemory(), key, false) }
	r.move()
	if ended = r.waitEnded(); ended.Phase != wire.ControlMovePhaseFailed || *ended.FailedPhase != wire.ControlMovePhaseReceiving ||
		!strings.Contains(*ended.Error, "not empty and receiving") {
		t.Fatalf("%+v", ended)
	}
	if r.p.s.phase.now() != phaseServing {
		t.Fatal(r.p.s.phase.now())
	}
}

// At most 20 moves are kept, and none older than 90 days; one that no copy runs any more
// failed at its step.
func TestControlMovesAreBounded(t *testing.T) {
	t.Parallel()
	p := startPlane(t, store.NewMemory(), controllink.NewPrivateKey())
	ctx := context.Background()
	old := dateOf(time.Now().Add(-91 * 24 * time.Hour))
	must(t, p.s.putMove(ctx, wire.ControlMove{ID: "0000000000001-aa", Phase: wire.ControlMovePhaseDone, UpdatedAt: old}))
	for i := range 22 {
		must(t, p.s.putMove(ctx, wire.ControlMove{ID: newMoveID(), Phase: wire.ControlMovePhaseDone, UpdatedAt: dateOf(time.Now()), By: itoa(i)}))
		time.Sleep(2 * time.Millisecond)
	}
	stuck := wire.ControlMove{ID: newMoveID(), Phase: wire.ControlMovePhaseCopying, UpdatedAt: dateOf(time.Now())}
	must(t, p.s.putMove(ctx, stuck))
	moves := p.s.listMoves(ctx)
	if len(moves) != movesKept || moves[0].ID != stuck.ID || moves[0].Phase != wire.ControlMovePhaseFailed ||
		*moves[0].FailedPhase != wire.ControlMovePhaseCopying {
		t.Fatalf("%d %+v", len(moves), moves[0])
	}
	keys, err := p.store.List(ctx, movesPrefix)
	must(t, err)
	if len(keys) != movesKept {
		t.Fatal(len(keys))
	}
}

// The store goes across in batches (#7394): with a link between two machines, 25 ms a call,
// 5,000 records are frozen for about a second, not the 4 minutes that two calls a record took.
func TestControlMoveCopiesALargeStoreInBatches(t *testing.T) {
	t.Parallel()
	r := newMoveRig(t)
	ctx := context.Background()
	const many = 5000
	body := bytes.Repeat([]byte("x"), 200)
	for i := range many {
		_, err := r.p.store.Put(ctx, "v1/test-load/"+itoa(i)+".json", body, store.Absent)
		must(t, err)
	}
	r.p.s.moving.linkDelay = 25 * time.Millisecond
	began := time.Now()
	r.move()
	ended := r.waitEnded()
	took := time.Since(began)
	if ended.Phase != wire.ControlMovePhaseForwarding || ended.Error != nil {
		t.Fatalf("%+v", ended)
	}
	// Two calls a record would be 250 s; a batch is 1,000 records, so it is about 15 calls.
	if took > 5*time.Second {
		t.Fatalf("the move took %v", took)
	}
	r.mu.Lock()
	fresh := r.fresh
	r.mu.Unlock()
	keys, err := fresh.cfg.Store.List(ctx, "v1/test-load/")
	must(t, err)
	if len(keys) != many {
		t.Fatal(len(keys))
	}
}
