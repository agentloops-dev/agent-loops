package main

// The machine lock (#6563, specs/3399-machine-lock): a machine that holds its person's lock
// takes calls only from that person's devices, each on a channel that a key of the lock
// opened (lock/hello). loops has a key of its own for each pairing (lockkey.go), reads the
// lock from the control plane and checks it against the head it pinned, and asks to join
// when the lock does not name its key yet: it prints the six digits, and another of the
// person's devices compares them and approves. Then every call to a machine goes on a
// checked channel (controllink.UseLock), as the web page's does.

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/machinelock"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// methodNotFound is JSON-RPC's code for a method the other side does not have.
const methodNotFound = -32601

// joinCheckEvery is how often a join that waits reads its request again, in case a notice
// did not come.
var joinCheckEvery = 3 * time.Second

// joinMode is what useLock does for a terminal its lock does not name yet.
type joinMode int

const (
	joinNever joinMode = iota // a relink: as before the lock, and the machines say why
	joinAsk                   // ask to join, and wait for the approval
	joinFail                  // print mode with --on-ask=fail: end with exit 3
)

// useLock reads the lock, pins it, and checks calls to machines on s with this terminal's
// key when the lock names it; with join, it asks to join first when it does not.
func useLock(ctx context.Context, s *controllink.Session, m *controllink.ClientMembership, h *hosts, mode joinMode, say io.Writer) error {
	l, err := clientLock(ctx, s, s.Notes, m, h, mode, say)
	if err != nil || l == nil {
		return err
	}
	s.UseLock(l)
	return nil
}

// clientLock is this terminal's key and its checked lock; nil when the person has no lock
// yet, or (joinNever) when the lock does not name the key.
func clientLock(ctx context.Context, c caller, notes <-chan controllink.Note, m *controllink.ClientMembership, h *hosts, mode joinMode, say io.Writer) (*controllink.ClientLock, error) {
	state, err := readLock(ctx, c, m, h)
	if err != nil || state == nil {
		return nil, err
	}
	key, err := lockKey(m)
	if err != nil {
		return nil, err
	}
	if member, ok := state.Member(machinelock.PublicKey(key)); ok && member.Client() {
		return &controllink.ClientLock{Signer: key, State: state}, nil
	}
	switch mode {
	case joinNever:
		return nil, nil
	case joinFail:
		return nil, &exit{code: exitAsked, msg: "This terminal is not one of your devices yet, so your machines refuse it.\n" +
			"Run any loops command without --on-ask=fail to ask to join, and approve it on another of your devices."}
	}
	if state, err = join(ctx, c, notes, m, h, key, say); err != nil {
		return nil, err
	}
	return &controllink.ClientLock{Signer: key, State: state}, nil
}

// readLock is the person's lock, checked against the pin, and the pin moved on; nil when
// the person has no lock yet. A lock that does not check is an error: the control plane
// sent another than the one this terminal saw.
func readLock(ctx context.Context, c caller, m *controllink.ClientMembership, h *hosts) (*machinelock.State, error) {
	raw, err := c.Call(ctx, "", wire.CallLockGet.Name, wire.LockGet{})
	if controllink.FailureCode(err) == methodNotFound {
		return nil, nil // a control plane from before the lock
	}
	if err != nil {
		return nil, unreachable(hostOf(m.URL), err)
	}
	var lock wire.Lock
	if err := json.Unmarshal(raw, &lock); err != nil {
		return nil, unreachable(hostOf(m.URL), err)
	}
	pin := h.lockPin(m.URL)
	state, err := machinelock.Follow(pin, lock)
	if err != nil {
		return nil, &exit{code: exitError, msg: fmt.Sprintf("The machine lock that %s sent does not check (%v).\n"+
			"Nothing was sent to your machines. Check your devices in Agent Loops ▸ Settings ▸ Machine lock.", hostOf(m.URL), err)}
	}
	if state.Count() == 0 {
		return nil, nil
	}
	if next := state.Pin(); next.Count != pin.Count || !bytes.Equal(next.Head, pin.Head) {
		h.setLockPin(m.URL, next)
		if err := h.write(); err != nil {
			return nil, &exit{code: exitError, msg: fmt.Sprintf("Could not keep the machine lock's head in %s: %v", hostsFile(), err)}
		}
	}
	return state, nil
}

// join asks to join the lock and waits until another device approves it, says no, or the
// request runs out. It gives back the lock that names this terminal's key.
func join(ctx context.Context, c caller, notes <-chan controllink.Note, m *controllink.ClientMembership, h *hosts, key *ecdsa.PrivateKey, say io.Writer) (*machinelock.State, error) {
	public := machinelock.PublicKey(key)
	nonce := make([]byte, machinelock.NonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	raw, err := c.Call(ctx, "", wire.CallLockJoin.Name, wire.LockJoin{Key: public, Commitment: machinelock.Commitment(public, nonce)})
	if err != nil {
		return nil, &exit{code: exitError, msg: fmt.Sprintf("Could not ask to join your machine lock (%s).", reason(err)) + debugLine(err)}
	}
	var started wire.LockJoinStarted
	if err := json.Unmarshal(raw, &started); err != nil {
		return nil, err
	}
	name := pairedName()
	fmt.Fprintf(say, "This terminal is not one of your devices yet, so your machines refuse it.\n"+
		"Asked to join your machine lock as %q. On another of your devices (Agent Loops on a Mac,\n"+
		"iPhone or iPad, or the web page), press Compare Digits on the card, check that it shows\n"+
		"the digits below, and press Approve. Waiting until %s (^C to stop)…\n",
		name, started.Expires.Time().Local().Format("15:04"))
	j := &joining{c: c, m: m, h: h, public: public, nonce: nonce, request: started.Request, say: say}
	until := started.Expires.Time()
	tick := time.NewTicker(joinCheckEvery)
	defer tick.Stop()
	for {
		state, done, err := j.check(ctx)
		if done || err != nil {
			return state, err
		}
		wait := time.Until(until)
		if wait <= 0 {
			return nil, j.ended("expired")
		}
		select {
		case <-ctx.Done():
			// The client gives up: the other devices' cards go.
			dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			_, _ = c.Call(dctx, "", wire.CallLockJoinDeny.Name, wire.LockJoinEnd{Request: started.Request})
			cancel()
			return nil, ctx.Err()
		case n, ok := <-notes:
			if !ok {
				return nil, lostForGood(hostOf(m.URL), 0, "", nil)
			}
			if state, done, err := j.note(ctx, n); done || err != nil {
				return state, err
			}
		case <-tick.C:
		case <-time.After(wait):
		}
	}
}

// joining is one request to join, as it goes on.
type joining struct {
	c        caller
	m        *controllink.ClientMembership
	h        *hosts
	public   []byte
	nonce    []byte
	request  string
	revealed bool
	shown    string
	say      io.Writer
}

// note is a control/lock… notice: done with the lock once it names this terminal.
func (j *joining) note(ctx context.Context, n controllink.Note) (*machinelock.State, bool, error) {
	switch n.Method {
	case wire.NotifyControlLockJoinRequested.Name:
		var row wire.LockJoinRequest
		if json.Unmarshal(n.Params, &row) == nil {
			return nil, false, j.take(ctx, row)
		}
	case wire.NotifyControlLockJoinDecided.Name:
		var d wire.LockJoinDecided
		if json.Unmarshal(n.Params, &d) != nil || d.Request != j.request {
			return nil, false, nil
		}
		if d.Result != "approved" {
			return nil, true, j.ended(d.Result)
		}
		return j.member(ctx)
	case wire.NotifyControlLockChanged.Name:
		return j.member(ctx)
	}
	return nil, false, nil
}

// check reads the request again: its row, or the lock once the row is gone.
func (j *joining) check(ctx context.Context) (*machinelock.State, bool, error) {
	raw, err := j.c.Call(ctx, "", wire.CallLockJoinRequests.Name, wire.Empty{})
	if err != nil {
		return nil, false, nil // a blip: the next check, or a notice
	}
	var rows []wire.LockJoinRequest
	_ = json.Unmarshal(raw, &rows)
	for _, row := range rows {
		if row.Request == j.request {
			return nil, false, j.take(ctx, row)
		}
	}
	// The request is no longer open: approved, or not.
	state, done, err := j.member(ctx)
	if err != nil || done {
		return state, done, err
	}
	return nil, true, j.ended("ended")
}

// take is this terminal's row: once a signer sent its nonce, the terminal shows its own
// and the six digits.
func (j *joining) take(ctx context.Context, row wire.LockJoinRequest) error {
	if row.Request != j.request || len(row.Nonce) == 0 {
		return nil
	}
	if len(row.Reveal) == 0 && !j.revealed {
		if _, err := j.c.Call(ctx, "", wire.CallLockJoinReveal.Name, wire.LockJoinNonce{Request: j.request, Nonce: j.nonce}); err != nil {
			return nil // asked again at the next check
		}
		j.revealed = true
	}
	digits := machinelock.Digits(j.public, j.nonce, row.Nonce)
	if digits != j.shown {
		j.shown = digits
		fmt.Fprintf(j.say, "Digits: %s %s\n", digits[:3], digits[3:])
	}
	return nil
}

// member reads the lock: done when it names this terminal's key as a client.
func (j *joining) member(ctx context.Context) (*machinelock.State, bool, error) {
	state, err := readLock(ctx, j.c, j.m, j.h)
	if err != nil {
		return nil, true, err
	}
	if state == nil {
		return nil, false, nil
	}
	if member, ok := state.Member(j.public); ok && member.Client() {
		fmt.Fprintln(j.say, "Approved: this terminal is one of your devices now.")
		return state, true, nil
	}
	return nil, false, nil
}

func (j *joining) ended(result string) error {
	why := "it was not approved in time"
	if result == "denied" {
		why = "another of your devices said no"
	}
	return &exit{code: exitError, msg: fmt.Sprintf("This terminal did not join your machine lock: %s.\nTo ask again, run: loops auth status", why)}
}

// pairedName is the name this terminal pairs with: "loops on <host name>".
func pairedName() string {
	hostname, _ := os.Hostname()
	if hostname == "" {
		return "loops"
	}
	return "loops on " + hostname
}

// notInLock says err is a machine's refusal of a call that no key of the lock vouched for.
func notInLock(err error) bool { return controllink.FailureCode(err) == wire.FailureNotInLock }

// lockRefusal is what a terminal says for a machine's refusal (#6563): the machine's own
// words are for an app ("Open Agent Loops on this device again").
func lockRefusal(what string, err error) error {
	return &exit{code: exitError, msg: fmt.Sprintf("%s takes requests only from your devices, and refused this terminal.\n"+
		"Run loops auth status to ask to join your machine lock, then approve it on another of your devices.", what) + debugLine(err)}
}
