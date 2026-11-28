package agentsd

import (
	"context"
	"log"
	"strings"
	"time"
	"unicode"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// loops with no pairing on this Mac (#6875): it asks agentsd for one on daemon.sock
// (clients/pairTerminal), and a person approves it once in Needs you, in Agent Loops on this
// Mac. The window that approves makes a one-use client code for this Mac's control plane
// (clients/startPairing) and, with a machine lock, signs an addClient for the terminal's key
// and client id, so the terminal does not ask to join. agentsd only carries the code from the
// answer to the call that waits, in memory, and never writes it down.
//
// The known gap, which the request's words say, is #4179's: agentsd cannot tell a terminal
// from an agent of this account. Any process of the account may ask; nothing is paired until
// a person approves, and an approval makes that one caller a client of the control plane and
// one of the person's devices, as a code pasted by hand does.

// pairingCaveat is the known gap, in the request's words on every client.
const pairingCaveat = "Agent Loops cannot tell a terminal from an agent outside its session."

// pairingOnTheMac is what agentsd says to an approval with no code: from a client that cannot
// make one here, such as the web page or the Remote.
const pairingOnTheMac = "Approve it in Agent Loops on this Mac: it makes the pairing code there."

// pairingCodeKept is how long an approved code waits for its terminal to take it: a code lives
// five minutes on the control plane.
var pairingCodeKept = controllink.CodeLifetime

// The most a request holds: a name and a client id are short, a key is 65 bytes.
const (
	maxPairingName   = 80
	maxPairingClient = 64
	maxPairingKey    = 133
	maxPairingCode   = 2 << 10
)

// pairingState is the codes approved for terminals, by the request's id, until each terminal
// takes its own. In memory only.
type pairingState struct {
	pairingCodes map[wire.UUID]pairingCode
}

type pairingCode struct {
	text  string
	until time.Time
}

func (d *Daemon) registerTerminalPairing() {
	On(d, wire.CallClientsPairTerminal, func(ctx context.Context, _ *Client, r wire.TerminalPairingRequest) (wire.TerminalPairingReply, error) {
		return d.pairTerminal(ctx, r, formHoldLimit)
	})
}

// pairTerminal is clients/pairTerminal: the code once a person approved it, the open request
// it waits on after hold, or an error with words.
func (d *Daemon) pairTerminal(ctx context.Context, r wire.TerminalPairingRequest, hold time.Duration) (wire.TerminalPairingReply, error) {
	name := printable(r.Name, maxPairingName)
	client := strings.TrimSpace(r.Client)
	if name == "" {
		return wire.TerminalPairingReply{}, Invalid("Give the terminal's name.")
	}
	if client == "" || len(client) > maxPairingClient || strings.ContainsFunc(client, func(c rune) bool { return !unicode.IsLetter(c) && !unicode.IsDigit(c) && c != '-' }) {
		return wire.TerminalPairingReply{}, Invalid("Give the client id the terminal pairs with: letters, digits and dashes.")
	}
	if len(r.Key) == 0 || len(r.Key) > maxPairingKey {
		return wire.TerminalPairingReply{}, Invalid("Give the terminal's machine lock key.")
	}
	home := d.personalHome()
	if home == "" {
		return wire.TerminalPairingReply{}, Invalid("This machine has no personal home.")
	}
	if !d.hasControlPlane() {
		return wire.TerminalPairingReply{}, Invalid("This Mac is not on a control plane yet. Open Agent Loops and finish set-up, then run loops again.")
	}
	ask := wire.RequestSecretUse{Name: name, Use: wire.SecretUsePairing, Place: terminalCommandName(deref(r.Command)),
		Client: &client, Key: r.Key}
	id, err := d.openPairingRequest(home, ask)
	if err != nil {
		return wire.TerminalPairingReply{}, err
	}
	d.holdOnTerminalRequest(ctx, id, hold)
	if code, ok := d.takePairingCode(id); ok {
		log.Printf("terminal pairing: %s (%s) took its code", name, ask.Place)
		return wire.TerminalPairingReply{Code: &code}, nil
	}
	if rec := d.request(id); rec != nil && rec.State == wire.RequestStateOpen {
		words := "Asked Agent Loops on this Mac to pair this terminal. Approve it in Needs you there (Approve or Deny)."
		return wire.TerminalPairingReply{Request: &id, Words: &words}, nil
	}
	return wire.TerminalPairingReply{}, Invalid("A person did not pair this terminal.")
}

// hasControlPlane is whether this machine is on a control plane, whose code a window can make.
func (d *Daemon) hasControlPlane() bool {
	m, err := controllink.LoadMembership(d.Store.Loc.Root)
	return err == nil && m != nil
}

// printable is s with no control characters, trimmed, and at most n runes.
func printable(s string, n int) string {
	s = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return -1
	}, s))
	if r := []rune(s); len(r) > n {
		s = string(r[:n])
	}
	return s
}

// openPairingRequest is the open request for a terminal's pairing: the one opened before for
// the same client id, when it is still open, so a call made again does not ask twice.
func (d *Daemon) openPairingRequest(home string, ask wire.RequestSecretUse) (wire.UUID, error) {
	d.requests.mu.Lock()
	for _, r := range d.requests.byID {
		if s := r.Detail.SecretUse; s != nil && r.State == wire.RequestStateOpen && r.AskedBy == nil &&
			s.Use == wire.SecretUsePairing && deref(s.Client) == deref(ask.Client) {
			id := r.ID
			d.requests.mu.Unlock()
			return id, nil
		}
	}
	at := now()
	r := &wire.Request{ID: store.NewUUID(), Kind: wire.RequestKindApprove, Source: wire.RequestSourceSecretUse,
		State: wire.RequestStateOpen, Title: "Pair " + ask.Name + " with this Mac (" + ask.Place + ")",
		Detail: wire.RequestDetail{SecretUse: &ask}, Folder: string(fileURL(home, true)),
		Waiters: []wire.RequestWaiter{}, AskedAt: at}
	c, err := d.openRequestLocked(r)
	d.requests.mu.Unlock()
	if err != nil {
		return "", err
	}
	log.Printf("terminal pairing: %s (%s) asks to pair", ask.Name, ask.Place)
	Broadcast(d, wire.NotifyRequestsChanged, wire.RequestUpdate{Request: c})
	d.raiseRequestEvent("request.opened", c, nil, nil)
	d.reconsider() // its banner
	return r.ID, nil
}

// answerPairing is requests/answer for a terminal's pairing: once with the code the window
// made, or deny.
func (d *Daemon) answerPairing(rec wire.Request, a *wire.RequestSecretUseAnswer, by *wire.AuditActor) (wire.RequestUpdate, error) {
	ask := *rec.Detail.SecretUse
	var summary string
	switch a.Action {
	case secretUseOnce:
		code := strings.TrimSpace(deref(a.PairingCode))
		if code == "" {
			return wire.RequestUpdate{}, Invalid("%s", pairingOnTheMac)
		}
		if len(code) > maxPairingCode {
			return wire.RequestUpdate{}, Invalid("That pairing code is too long.")
		}
		if c, err := controllink.ParseCode(code); err != nil || c.Host {
			return wire.RequestUpdate{}, Invalid("The answer's pairing code is not a client code.")
		}
		d.secretUseMu.Lock()
		if d.pairingCodes == nil {
			d.pairingCodes = map[wire.UUID]pairingCode{}
		}
		// In memory only: one a request, each at most 2 KiB, taken once or dropped after 5 minutes.
		d.pairingCodes[rec.ID] = pairingCode{text: code, until: nowFunc().Add(pairingCodeKept)}
		d.secretUseMu.Unlock()
		summary = "Paired"
	case secretUseDeny:
		summary = "Denied"
	default:
		return wire.RequestUpdate{}, Invalid("A terminal's pairing takes once or deny.")
	}
	if !d.endRequest(rec.ID, wire.RequestStateAnswered, &summary, "", by) {
		return wire.RequestUpdate{}, &Error{Code: wire.FailureNoSuchAgent, Message: "That request has ended already."}
	}
	log.Printf("terminal pairing: %s (%s): %s", ask.Name, ask.Place, a.Action)
	d.secretUseEnded(rec.ID)
	after := d.request(rec.ID)
	if after == nil {
		after = &rec
	}
	return wire.RequestUpdate{Request: *after}, nil
}

// takePairingCode is the code approved for this request, once: the next call gets none. Old
// ones go too.
func (d *Daemon) takePairingCode(id wire.UUID) (string, bool) {
	d.secretUseMu.Lock()
	defer d.secretUseMu.Unlock()
	at := nowFunc()
	for k, c := range d.pairingCodes {
		if at.After(c.until) {
			delete(d.pairingCodes, k)
		}
	}
	c, ok := d.pairingCodes[id]
	if ok {
		delete(d.pairingCodes, id)
	}
	return c.text, ok
}

// pairingWanted is a pairing's line on its banner.
func pairingWanted(u wire.RequestSecretUse) string {
	return "Wants to pair " + u.Name + " with this Mac, for " + u.Place + ". " + pairingCaveat
}
