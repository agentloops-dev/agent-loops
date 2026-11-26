package controlplane

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/records"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// control/move (#6612, epic #6608): the control plane moves to an eligible machine in one
// call. Today's handover (handover.go, R16) is driven by hand; here this copy drives it
// itself, with the machine that it moves to:
//
//  1. checking: control/eligible's checks, and one move at a time.
//  2. receiving: the control key, sealed to the machine's own key, goes to its host on its
//     channel 0 (control/receive). The host keeps it for its own copy, and starts that copy
//     empty, to receive. The key is never in clear in a store or on the wire.
//  3. frozen: this copy changes no record from here on.
//  4. copying: the store goes to the new copy over its handover session (CopyStore), and it
//     takes over (handover/take).
//  5. forwarding: when the new address is not this one, this copy sends each member to it,
//     for 30 days at most. No DNS record is changed. done: it is the same address.
//
// A step that fails before the new copy takes over leaves this copy serving, and the move
// says which step failed. Once it has taken over, two copies must never serve, so this one
// stays frozen and says so.

const (
	// store-bounds: kind=document; count=20; size=4KiB a record; age=90d; applied=on write (refused past its size), and each write deletes, from the keys, the moves past the newest 20 and those started 90 days ago, and a read writes nothing (#7396)
	movesPrefix = "v1/moves/"
	// movesKept is how many moves are kept, the newest.
	movesKept = 20
	// moveMostBytes is the largest move record.
	moveMostBytes = 4 << 10
	// moveKeptFor is how long a move is kept.
	moveKeptFor = 90 * 24 * time.Hour
	// moveReceivePatience is how long the machine has to start its copy empty.
	moveReceivePatience = 2 * time.Minute
	// moveDialPatience is how long the new copy has to answer after that.
	moveDialPatience = 2 * time.Minute
)

// moveState is the move that runs, if any: one at a time.
type moveState struct {
	mu      sync.Mutex
	running string
	// dialEvery is the wait between dials of the new copy; tests make it short.
	dialEvery time.Duration
}

func (m *moveState) take(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running != "" {
		return false
	}
	m.running = id
	return true
}

func (m *moveState) release() {
	m.mu.Lock()
	m.running = ""
	m.mu.Unlock()
}

func (m *moveState) now() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}

// controlMoveMethod is control/move and control/moves/list: an admin's.
func (m *Methods) controlMoveMethod(ctx context.Context, method string, params json.RawMessage, from Caller) (any, error) {
	person := m.callerPerson(from)
	if !m.s.isAdmin(person) {
		return nil, fail(notPermitted, "Only an admin can move the control plane.")
	}
	switch method {
	case wire.CallControlMovesList.Name:
		return wire.ControlMoves{Moves: m.s.listMoves(ctx)}, nil
	case wire.CallControlMove.Name:
		request, err := require[wire.ControlMoveRequest](params, "ControlMoveRequest")
		if err != nil {
			return nil, err
		}
		move, err := m.s.startMove(ctx, string(request.Machine), person)
		if err != nil {
			return nil, err
		}
		return wire.ControlMoveResult{Move: move}, nil
	}
	return nil, notFound(method)
}

// startMove checks the machine, writes the move and starts it; it answers once the checks
// pass.
func (s *Service) startMove(ctx context.Context, machine, person string) (wire.ControlMove, error) {
	if s.cfg.Hosted {
		return wire.ControlMove{}, fail(notPermitted, "A machine of yours can’t run a hosted control plane.")
	}
	h := s.records.Host(machine)
	if h == nil {
		return wire.ControlMove{}, fail(noSuchHost, "No machine is called %s.", machine)
	}
	if h.ID == s.controlMachineID() {
		return wire.ControlMove{}, fail(invalidParams, "%s already runs this control plane.", cmpOr(h.Name, h.ID))
	}
	if s.mesh != nil && len(s.mesh.peers()) > 0 {
		return wire.ControlMove{}, fail(invalidParams, "This control plane runs as more than one copy over one store. A move takes one copy to one machine: stop the other copies first.")
	}
	if phase := s.phase.now(); phase != phaseServing {
		return wire.ControlMove{}, fail(invalidParams, "This copy is %s in a handover already.", phase)
	}
	id := newMoveID()
	if !s.moving.take(id) {
		return wire.ControlMove{}, fail(changedElsewhere, "Another move of the control plane is running. Wait for it to end.")
	}
	s.failLeftMoves(ctx)
	now := time.Now()
	move := wire.ControlMove{ID: id, From: s.copyID, To: wire.MachineRef(h.ID), ToName: cmpOr(h.Name, h.ID),
		Phase: wire.ControlMovePhaseChecking, StartedAt: dateOf(now), UpdatedAt: dateOf(now), By: person}
	s.auditTo(s.personActor(person), wire.CallControlMove.Name, "Started a move of the control plane", move.ToName)
	s.moveChanged(ctx, &move)
	address, why := s.moveChecks(ctx, *h)
	if why != "" {
		s.moveFailed(ctx, &move, why, false)
		return move, fail(invalidParams, "%s", why)
	}
	go s.runMove(context.WithoutCancel(ctx), move, *h, "https://"+address)
	return move, nil
}

// moveChecks is control/eligible's checks and what a move needs on top: an address that
// clients reach, and a key to seal to. why is "" when the machine passes.
func (s *Service) moveChecks(ctx context.Context, h records.MachineRecord) (address, why string) {
	facts := s.eligibleFacts(ctx, h, false)
	if reasons := eligibleReasons(facts); len(reasons) > 0 {
		words := make([]string, len(reasons))
		for i, r := range reasons {
			words[i] = r.Text
		}
		return "", strings.Join(words, " ")
	}
	if facts.address == "" {
		return "", "Agent Loops reaches this machine only by its tunnel. Clients need an address of its own to reach the control plane at: open its port, then try again."
	}
	if !keyed(h.PublicKey) {
		return "", "This machine has no key to seal the control key to. Update it, then try again."
	}
	return facts.address, ""
}

// runMove is the move's steps, after its checks.
func (s *Service) runMove(ctx context.Context, move wire.ControlMove, h records.MachineRecord, address string) {
	defer s.moving.release()
	step := func(phase wire.ControlMovePhase) {
		move.Phase = phase
		s.moveChanged(ctx, &move)
	}

	step(wire.ControlMovePhaseReceiving)
	endpoint, err := s.moveReceive(h, move.ID, address)
	if err != nil {
		s.moveFailed(ctx, &move, moveWords(err), false)
		return
	}
	move.Address = &endpoint.URL
	link, err := s.moveDial(ctx, endpoint)
	if err != nil {
		s.moveFailed(ctx, &move, moveWords(err), false)
		return
	}
	defer link.Close()

	if behind := s.hostsLeftBehind(); len(behind) > 0 {
		s.moveFailed(ctx, &move, "Not moving: "+strings.Join(behind, ", ")+" can't follow a move. Update it first, or remove it and add it again afterwards.", false)
		return
	}
	if s.phase.now() != phaseServing {
		s.moveFailed(ctx, &move, "This copy went into another handover.", false)
		return
	}
	s.methods.setFrozen(true)
	s.phase.set(phaseFrozen)
	s.log("frozen to move the control plane to " + move.ToName)
	step(wire.ControlMovePhaseFrozen)

	step(wire.ControlMovePhaseCopying)
	forwarding := endpoint.URL != s.cfg.URL
	until := time.Now().Add(longestForwarding)
	if _, _, err := copyStoreLeaving(ctx, s.cfg.Store, PeerStore{Link: link}, append(slices.Clone(copySkipped), movesPrefix)); err != nil {
		s.moveFailed(ctx, &move, "The store did not copy: "+moveWords(err), true)
		return
	}
	// The new copy keeps this move as it ends there, so its own list has it.
	ended := move
	ended.Phase, ended.UpdatedAt = wire.ControlMovePhaseDone, dateOf(time.Now())
	if forwarding {
		ended.Phase, ended.ForwardingUntil = wire.ControlMovePhaseForwarding, new(dateOf(until))
	}
	if data, err := json.Marshal(ended); err == nil {
		if _, err := (PeerStore{Link: link}).Put(ctx, moveKey(move.ID), data, store.Absent); err != nil {
			s.log("the move's record could not be kept at the new copy: " + err.Error())
		}
	}
	if _, err := link.Call("handover/take", nil); err != nil {
		s.moveFailed(ctx, &move, "The new copy did not take over: "+moveWords(err), true)
		return
	}
	s.log("the control plane moved to " + move.ToName + " at " + endpoint.URL)

	if !forwarding {
		// The same address: whoever comes reaches the new copy. This one stays frozen.
		step(wire.ControlMovePhaseDone)
		return
	}
	if err := s.forwardTo([]records.Endpoint{endpoint}, until); err != nil {
		// The new copy serves: this one stays frozen, and members that come here are not sent on.
		s.moveFailed(ctx, &move, fmt.Sprintf("The new copy took over, but this one could not send members to it: %v. Pair each client with %s again.", err, endpoint.URL), false)
		return
	}
	move.ForwardingUntil = new(dateOf(until))
	step(wire.ControlMovePhaseForwarding)
}

// moveReceive asks the machine to keep the control key, sealed to it, and to start its copy
// empty, and says where that copy answers.
func (s *Service) moveReceive(h records.MachineRecord, move, address string) (records.Endpoint, error) {
	public := s.publicKey()
	sealed, err := controllink.Seal(h.PublicKey, controllink.ControlMoveInfo, controllink.ControlMoveAD(move, h.ID, public), s.privateKey())
	if err != nil {
		return records.Endpoint{}, fail(internalError, "The control key could not be sealed to %s: %v", cmpOr(h.Name, h.ID), err)
	}
	request := wire.ControlReceiveRequest{Move: move, URL: address, ControlKey: public, Key: sealed}
	answer := s.router.Ask(h.ID, wire.CallControlReceive.Name, request, moveReceivePatience)
	if answer == nil {
		return records.Endpoint{}, fail(internalError, "%s did not answer. Check that it is online and runs this version, then try again.", cmpOr(h.Name, h.ID))
	}
	if len(answer.Error) > 0 {
		var e RPCError
		if json.Unmarshal(answer.Error, &e) == nil && e.Code == methodNotFound {
			return records.Endpoint{}, fail(internalError, "%s can't receive the control plane: update it, then try again.", cmpOr(h.Name, h.ID))
		}
		return records.Endpoint{}, fail(internalError, "%s could not start its copy: %s", cmpOr(h.Name, h.ID), cmpOr(e.Message, string(answer.Error)))
	}
	var receive wire.ControlReceive
	if json.Unmarshal(answer.Result, &receive) != nil {
		return records.Endpoint{}, fail(internalError, "The machine's answer could not be read.")
	}
	endpoint := records.Endpoint(receive)
	if !acceptableEndpoint(endpoint) {
		return records.Endpoint{}, fail(internalError, "The machine's copy answers at %s, which clients can't dial.", receive.URL)
	}
	return endpoint, nil
}

// moveDial opens a handover session with the new copy, which may still be starting, and
// checks that it is empty and receives for this control plane.
func (s *Service) moveDial(ctx context.Context, endpoint records.Endpoint) (*HandoverLink, error) {
	pin := ""
	if endpoint.Pin != nil {
		pin = *endpoint.Pin
	}
	every := cmp.Or(s.moving.dialEvery, 2*time.Second)
	deadline := time.Now().Add(moveDialPatience)
	for {
		link, err := DialHandover(ctx, endpoint.URL, pin, s.privateKey())
		if err == nil {
			status, err := link.Status()
			switch {
			case err != nil:
				link.Close()
				return nil, fail(internalError, "The new copy did not say how it stands: %v", err)
			case status.Phase != phaseReceiving || status.HasRecords:
				link.Close()
				return nil, fail(internalError, "The new copy at %s is not empty and receiving (it is %s).", endpoint.URL, status.Phase)
			case !bytes.Equal(status.ControlKey, s.publicKey()):
				link.Close()
				return nil, fail(internalError, "The new copy at %s has another control key.", endpoint.URL)
			}
			return link, nil
		}
		if time.Now().After(deadline) {
			return nil, fail(internalError, "The new copy did not answer at %s: %v", endpoint.URL, err)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(every):
		}
	}
}

// forwardTo is handover/forward from this copy itself: only the new list, to whoever still
// comes, until then.
func (s *Service) forwardTo(list []records.Endpoint, until time.Time) error {
	if err := s.changeEndpoints(list); err != nil {
		return err
	}
	s.phase.setUntil(until)
	s.phase.set(phaseForwarding)
	if err := s.markForwarding(until); err != nil {
		return err
	}
	s.log(fmt.Sprintf("forwarding until %s: members are told %s", until.UTC().Format(time.RFC3339), list[0].URL))
	return nil
}

// moveFailed ends a move that failed at its step. unfreeze serves again when this copy froze
// for it and the new copy did not take over.
func (s *Service) moveFailed(ctx context.Context, move *wire.ControlMove, why string, unfreeze bool) {
	if unfreeze && s.phase.now() == phaseFrozen {
		s.methods.setFrozen(false)
		s.phase.set(phaseServing)
		s.log("unfrozen: the move did not finish")
	}
	failed := move.Phase
	failedAt(move, why)
	s.log(fmt.Sprintf("the move to %s stopped at %s: %s", move.ToName, failed, why))
	s.moveChanged(ctx, move)
}

// moveChanged keeps the move as it stands now and tells the admins.
func (s *Service) moveChanged(ctx context.Context, move *wire.ControlMove) {
	move.UpdatedAt = dateOf(time.Now())
	if err := s.putMove(ctx, *move); err != nil {
		s.log("the move's record could not be kept: " + err.Error())
	}
	s.tellAdmins(wire.NotifyControlMoveChanged.Name, *move)
}

func moveKey(id string) string { return movesPrefix + id + ".json" }

// newMoveID sorts by when the move started.
func newMoveID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%013d-%s", time.Now().UnixMilli(), hex.EncodeToString(b))
}

// putMove writes the move, then drops the moves past the newest movesKept and those that
// started more than moveKeptFor ago. It writes the store itself: a frozen copy still says
// how its move ends.
func (s *Service) putMove(ctx context.Context, move wire.ControlMove) error {
	data, err := json.Marshal(move)
	if err != nil {
		return err
	}
	if err := fitsRecord("A move", data, moveMostBytes); err != nil {
		return err
	}
	st := s.cfg.Store
	key := moveKey(move.ID)
	held, err := st.Get(ctx, key)
	if err != nil {
		return err
	}
	when := store.Absent
	if held != nil {
		when = store.Matching(held.ETag)
	}
	if _, err := st.Put(ctx, key, data, when); err != nil {
		return err
	}
	return s.pruneMoves(ctx)
}

// moveKeys lists the moves' keys, the newest first: the ids sort by when each started.
func (s *Service) moveKeys(ctx context.Context) ([]store.Key, error) {
	keys, err := s.cfg.Store.List(ctx, movesPrefix)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(keys, func(a, b store.Key) int { return strings.Compare(b.Key, a.Key) })
	return keys, nil
}

// moveStarted is when the move of a key started, from its id; the zero time for a key that
// is not a move's.
func moveStarted(key string) time.Time {
	id := strings.TrimSuffix(strings.TrimPrefix(key, movesPrefix), ".json")
	millis, _, ok := strings.Cut(id, "-")
	if !ok {
		return time.Time{}
	}
	n, err := strconv.ParseInt(millis, 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.UnixMilli(n)
}

// pruneMoves deletes, from the keys alone, the moves past the newest movesKept and those
// that started more than moveKeptFor ago (#7396): one list, and a delete for each.
func (s *Service) pruneMoves(ctx context.Context) error {
	keys, err := s.moveKeys(ctx)
	if err != nil {
		return err
	}
	oldest := time.Now().Add(-moveKeptFor)
	for i, k := range keys {
		if i < movesKept && !moveStarted(k.Key).Before(oldest) {
			continue
		}
		if err := s.cfg.Store.Delete(ctx, k.Key); err != nil {
			return err
		}
	}
	return nil
}

// keptMoves reads the moves within the bounds, the newest first: one list, and a read of
// each of the newest movesKept. It writes nothing (#7396): the next write prunes the rest.
func (s *Service) keptMoves(ctx context.Context) ([]wire.ControlMove, error) {
	keys, err := s.moveKeys(ctx)
	if err != nil {
		return nil, err
	}
	oldest := time.Now().Add(-moveKeptFor)
	var out []wire.ControlMove
	for _, k := range keys[:min(len(keys), movesKept)] {
		if moveStarted(k.Key).Before(oldest) {
			break
		}
		object, err := s.cfg.Store.Get(ctx, k.Key)
		if err != nil {
			return nil, err
		}
		var move wire.ControlMove
		if object == nil || json.Unmarshal(object.Data, &move) != nil {
			continue
		}
		out = append(out, move)
	}
	return out, nil
}

// leftMove is a move that has not ended and that this copy does not run: the copy that ran
// it stopped during it.
func (s *Service) leftMove(move wire.ControlMove) bool {
	switch move.Phase {
	case wire.ControlMovePhaseDone, wire.ControlMovePhaseFailed, wire.ControlMovePhaseForwarding:
		return false
	}
	return move.ID != s.moving.now()
}

// leftMoveWhy is why a left move failed.
const leftMoveWhy = "The control plane stopped during this step."

// failedAt is the move as failed at the step it is at.
func failedAt(move *wire.ControlMove, why string) {
	failed := move.Phase
	move.Phase, move.FailedPhase, move.Error = wire.ControlMovePhaseFailed, &failed, &why
}

// listMoves is control/moves/list. A move that no copy runs any more failed at the step it
// was at: the list says so, and writes nothing (#7396); the next move writes it.
func (s *Service) listMoves(ctx context.Context) []wire.ControlMove {
	moves, err := s.keptMoves(ctx)
	if err != nil {
		s.log("the moves could not be read: " + err.Error())
	}
	for i := range moves {
		if s.leftMove(moves[i]) {
			failedAt(&moves[i], leftMoveWhy)
		}
	}
	if moves == nil {
		moves = []wire.ControlMove{}
	}
	return moves
}

// failLeftMoves keeps each left move as failed at its step, and tells the admins. A move
// that starts does it first, so that one move at a time stands in the record too.
func (s *Service) failLeftMoves(ctx context.Context) {
	moves, err := s.keptMoves(ctx)
	if err != nil {
		s.log("the moves could not be read: " + err.Error())
		return
	}
	for i := range moves {
		if s.leftMove(moves[i]) {
			s.moveFailed(ctx, &moves[i], leftMoveWhy, false)
		}
	}
}

// moveWords is an error in the words a person reads, with no code.
func moveWords(err error) string {
	if e := (*RPCError)(nil); errors.As(err, &e) {
		return e.Message
	}
	return err.Error()
}
