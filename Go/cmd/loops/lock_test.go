package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/machinelock"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// lockPlane is a control plane with sam's lock, and a signer (sam's Mac) that compares the
// digits and approves or says no, as a person does on another device.
type lockPlane struct {
	t       *testing.T
	mu      sync.Mutex
	mac     *ecdsa.PrivateKey
	entries []wire.LockEntry
	row     *wire.LockJoinRequest
	notes   chan controllink.Note
	// approve says what the signer does once the terminal shows its nonce.
	approve bool
	// signerDigits is what the signer's card showed.
	signerDigits string
}

func newLockPlane(t *testing.T, approve bool) *lockPlane {
	mac, err := machinelock.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	p := &lockPlane{t: t, mac: mac, approve: approve, notes: make(chan controllink.Note, 16)}
	p.add(wire.LockBody{Kind: wire.LockEntryKindRoot, Key: machinelock.PublicKey(mac), ID: new("MAC")})
	return p
}

func (p *lockPlane) add(body wire.LockBody) {
	body.Person, body.At = "sam", wire.DateOf(time.Now())
	if n := len(p.entries); n > 0 {
		h := machinelock.HashOf(p.entries[n-1])
		body.Previous = h[:]
	}
	e, err := machinelock.Sign(p.mac, body)
	if err != nil {
		p.t.Fatal(err)
	}
	p.entries = append(p.entries, e)
}

func (p *lockPlane) Call(_ context.Context, host, method string, params any) (json.RawMessage, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if host != "" {
		return nil, fmt.Errorf("only the control plane is here")
	}
	var out any
	switch method {
	case wire.CallLockGet.Name:
		out = wire.Lock{Person: "sam", Entries: append([]wire.LockEntry(nil), p.entries...)}
	case wire.CallLockJoin.Name:
		j := params.(wire.LockJoin)
		p.row = &wire.LockJoinRequest{Request: "r1", Client: "LOOPS", Name: "loops on box", Key: j.Key, Commitment: j.Commitment,
			Expires: wire.DateOf(time.Now().Add(time.Minute))}
		// The signer presses Compare Digits: its nonce.
		p.row.Nonce = bytes.Repeat([]byte{7}, machinelock.NonceSize)
		out = wire.LockJoinStarted{Request: "r1", Expires: p.row.Expires}
	case wire.CallLockJoinRequests.Name:
		rows := []wire.LockJoinRequest{}
		if p.row != nil {
			rows = append(rows, *p.row)
		}
		out = rows
	case wire.CallLockJoinReveal.Name:
		n := params.(wire.LockJoinNonce)
		if p.row == nil || !bytes.Equal(machinelock.Commitment(p.row.Key, n.Nonce), p.row.Commitment) {
			return nil, fmt.Errorf(`{"code":-32602,"message":"That nonce is not the one the request named."}`)
		}
		p.signerDigits = machinelock.Digits(p.row.Key, n.Nonce, p.row.Nonce)
		key := p.row.Key
		p.row = nil
		if p.approve {
			p.add(wire.LockBody{Kind: wire.LockEntryKindAddClient, Key: key, ID: new("LOOPS"), ClientKind: new("mac")})
			p.notes <- controllink.Note{Method: wire.NotifyControlLockJoinDecided.Name, Params: mustJSON(wire.LockJoinDecided{Request: "r1", Result: "approved"})}
		} else {
			p.notes <- controllink.Note{Method: wire.NotifyControlLockJoinDecided.Name, Params: mustJSON(wire.LockJoinDecided{Request: "r1", Result: "denied"})}
		}
		out = wire.Empty{}
	case wire.CallLockJoinDeny.Name:
		p.row = nil
		out = wire.Empty{}
	default:
		return nil, fmt.Errorf("no method %s", method)
	}
	b, _ := json.Marshal(out)
	return b, nil
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// scratchLockKeys keeps the terminal's keys in files under the test's folder, never in the
// person's Keychain.
func scratchLockKeys(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	was := lockKeys
	lockKeys = fileLockKeys{}
	t.Cleanup(func() { lockKeys = was })
}

// #6563: a terminal that is not in its person's lock asks to join it, shows the six digits
// the signer's card shows, and once approved checks its calls with its key; the next time,
// it is in the lock and asks nothing.
func TestATerminalJoinsTheLockWithTheSixDigits(t *testing.T) {
	scratchLockKeys(t)
	p := newLockPlane(t, true)
	h := &hosts{}
	m := &controllink.ClientMembership{Client: "LOOPS", URL: "wss://cp.example"}
	h.put(*m)
	var said bytes.Buffer
	l, err := clientLock(t.Context(), p, p.notes, m, h, joinAsk, &said)
	if err != nil || l == nil {
		t.Fatalf("the join: %v\n%s", err, said.String())
	}
	if member, ok := l.State.Member(machinelock.PublicKey(l.Signer)); !ok || !member.Client() {
		t.Fatal("the lock does not name the terminal's key")
	}
	d := p.signerDigits
	if len(d) != 6 || !strings.Contains(said.String(), "Digits: "+d[:3]+" "+d[3:]) || !strings.Contains(said.String(), "Approved") {
		t.Fatalf("the terminal did not show the signer's digits %s:\n%s", d, said.String())
	}
	if pin := h.lockPin(m.URL); pin.Count != 2 {
		t.Fatalf("the pin is not the lock's head: %+v", pin)
	}
	fi, err := os.Stat(filepath.Join(configDir(), "lock-keys", "LOOPS.key"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("the key file: %v %v", fi, err)
	}

	// The next command: in the lock, with the same key, and nothing asked.
	said.Reset()
	again, err := clientLock(t.Context(), p, p.notes, m, h, joinFail, &said)
	if err != nil || again == nil || !again.Signer.Equal(l.Signer) || said.Len() != 0 {
		t.Fatalf("the next command: %v %q", err, said.String())
	}

	// The control plane sends another lock than the one pinned: refused.
	other := newLockPlane(t, true)
	if _, err := clientLock(t.Context(), other, other.notes, m, h, joinAsk, &said); err == nil || !strings.Contains(err.Error(), "does not check") {
		t.Fatalf("a forked lock was taken: %v", err)
	}
}

func TestATerminalThatIsNotApprovedSaysWhat(t *testing.T) {
	scratchLockKeys(t)
	m := &controllink.ClientMembership{Client: "LOOPS", URL: "wss://cp.example"}

	// Print mode with --on-ask=fail asks nobody, and ends with exit 3.
	p := newLockPlane(t, false)
	if _, err := clientLock(t.Context(), p, p.notes, m, &hosts{}, joinFail, &bytes.Buffer{}); codeOf(err) != exitAsked {
		t.Fatalf("--on-ask=fail: %v", err)
	}
	// A relink asks nobody either: its calls go as before, and the machines say why.
	if l, err := clientLock(t.Context(), p, p.notes, m, &hosts{}, joinNever, &bytes.Buffer{}); l != nil || err != nil {
		t.Fatalf("a relink: %v %v", l, err)
	}
	// Another device says no.
	_, err := clientLock(t.Context(), p, p.notes, m, &hosts{}, joinAsk, &bytes.Buffer{})
	if codeOf(err) != exitError || !strings.Contains(err.Error(), "said no") {
		t.Fatalf("denied: %v", err)
	}
}

func TestAMachinesRefusalSaysWhatToDoInATerminal(t *testing.T) {
	refused := fmt.Errorf(`{"code":-32104,"message":"This machine takes requests only from your devices: the request is not signed by one of them. Open Agent Loops on this device again; if it asks to join, approve it on another of your devices."}`)
	err := unreachable("Sam's MacBook", refused)
	if strings.Contains(err.Error(), "Open Agent Loops") || !strings.Contains(err.Error(), "loops auth status") || !strings.Contains(err.Error(), "Sam's MacBook") {
		t.Fatalf("the refusal: %v", err)
	}
}
