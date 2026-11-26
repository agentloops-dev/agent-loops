package agentsd

import (
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/machinelock"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// testClientCode is a client code's text, as clients/startPairing gives one.
func testClientCode(t *testing.T, host bool) string {
	t.Helper()
	key, err := machinelock.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	c := controllink.Code{Host: host, ControlKey: machinelock.PublicKey(key), Secret: make([]byte, 32),
		URL: "https://control.test:8791", Name: "Test"}
	return c.Text()
}

// #6875: loops with no pairing asks this Mac's agentsd over daemon.sock, which a stranger may
// call. The request has no session, names the terminal, its client id and key, and takes once
// or deny. Only an answer that carries a client code pairs it: the web page's or the Remote's
// Approve, with none, gets words that say to approve on this Mac. The code goes to the call
// that waits, once.
func TestALoopsWithNoPairingAsksAPersonOnThisMac(t *testing.T) {
	answered, _ := formHolds(t)
	home := t.TempDir()
	personalHomeFor(t, home)
	d, sock := serveWith(t, fixedPolicy{role: RoleStranger, ok: true})
	key, err := machinelock.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	ask := wire.TerminalPairingRequest{Name: "loops on test-mac", Client: "C0FFEE-6875", Key: machinelock.PublicKey(key)}
	command := "zsh"
	ask.Command = &command
	type result struct {
		got wire.TerminalPairingReply
		err error
	}
	run := func() chan result {
		done := make(chan result, 1)
		go func() {
			var got wire.TerminalPairingReply
			err := callSocketFor(sock, wire.CallClientsPairTerminal.Name, ask, &got, 10*time.Second)
			done <- result{got, err}
		}()
		return done
	}
	got := func(done chan result) result {
		t.Helper()
		select {
		case r := <-done:
			return r
		case <-afterLimit(t):
			t.Fatal("clients/pairTerminal never ended")
		}
		return result{}
	}

	// A Mac that is on no control plane has no code to give: words, and no request.
	if r := got(run()); r.err == nil || !strings.Contains(r.err.Error(), "not on a control plane") {
		t.Fatalf("with no control plane: %+v", r)
	}
	if err := (&controllink.Membership{Host: "h-1", Addresses: []string{}}).Save(d.Store.Loc.Root); err != nil {
		t.Fatal(err)
	}

	answered()
	done := run()
	r := openSecretUse(t, d)
	u := r.Detail.SecretUse
	if r.AskedBy != nil || r.Kind != wire.RequestKindApprove || u.Use != wire.SecretUsePairing || u.Name != ask.Name ||
		u.Place != "zsh" || deref(u.Client) != ask.Client || string(u.Key) != string(ask.Key) {
		t.Fatalf("the request: %+v %+v", r, u)
	}
	// The same call again waits on the same request.
	again := run()
	waitFor(t, "both calls held on the request", func() bool {
		d.secretUseMu.Lock()
		defer d.secretUseMu.Unlock()
		return len(d.terminalHeld[r.ID]) == 2
	})
	if open := d.listRequests(wire.RequestsListStateOpen, ""); len(open) != 1 {
		t.Fatalf("a call made again asked twice: %+v", open)
	}
	for _, bad := range []*wire.RequestSecretUseAnswer{
		{Action: secretUseOnce},
		{Action: secretUseOnce, PairingCode: ptr(testClientCode(t, true))},
		{Action: secretUseOnce, PairingCode: ptr("not a code")},
		{Action: secretUseHour, PairingCode: ptr(testClientCode(t, false))},
	} {
		if _, err := d.answerSecretUse(r, bad, nil); err == nil {
			t.Fatalf("took %+v", bad)
		}
	}
	if _, err := d.answerSecretUse(r, &wire.RequestSecretUseAnswer{Action: secretUseOnce}, nil); err == nil || !strings.Contains(err.Error(), "on this Mac") {
		t.Fatalf("an Approve with no code: %v", err)
	}
	code := testClientCode(t, false)
	if _, err := d.answerSecretUse(r, &wire.RequestSecretUseAnswer{Action: secretUseOnce, PairingCode: &code}, nil); err != nil {
		t.Fatal(err)
	}
	// One of the two calls takes the code; the other is told nobody paired it.
	first, second := got(done), got(again)
	if first.err != nil && second.err == nil {
		first, second = second, first
	}
	if first.err != nil || deref(first.got.Code) != code {
		t.Fatalf("approved: %+v", first)
	}
	if second.err == nil || deref(second.got.Code) != "" {
		t.Fatalf("the code went twice: %+v", second)
	}

	// Deny refuses, with words.
	done = run()
	answerUse(t, d, openSecretUse(t, d), secretUseDeny)
	if r := got(done); r.err == nil || !strings.Contains(r.err.Error(), "did not pair") {
		t.Fatalf("denied: %+v", r)
	}

	// Its banner names the terminal, with Approve and Deny.
	wanted := pairingWanted(*u)
	if !strings.Contains(wanted, "loops on test-mac") || !strings.Contains(wanted, pairingCaveat) {
		t.Fatalf("the banner's line: %q", wanted)
	}
	if answers := secretUseAnswers(*u); len(answers) != 2 || answers[0].ID != secretUseOnce || answers[1].ID != secretUseDeny {
		t.Fatalf("the banner's answers: %+v", answers)
	}
}

// #6875: what a request holds is bounded, and a client id is one word.
func TestAPairingRequestIsBounded(t *testing.T) {
	_, _ = formHolds(t)
	home := t.TempDir()
	personalHomeFor(t, home)
	d, _ := serveWith(t, fixedPolicy{role: RoleStranger, ok: true})
	key := make([]byte, 65)
	for _, bad := range []wire.TerminalPairingRequest{
		{Name: "", Client: "C1", Key: key},
		{Name: "loops", Client: "", Key: key},
		{Name: "loops", Client: "../C1", Key: key},
		{Name: "loops", Client: strings.Repeat("A", 65), Key: key},
		{Name: "loops", Client: "C1"},
		{Name: "loops", Client: "C1", Key: make([]byte, 200)},
	} {
		if _, err := d.pairTerminal(t.Context(), bad, time.Millisecond); err == nil {
			t.Fatalf("took %+v", bad)
		}
	}
	if n := printable("loops\x1b[31m on "+strings.Repeat("x", 200), maxPairingName); len([]rune(n)) != maxPairingName || strings.ContainsRune(n, '\x1b') {
		t.Fatalf("printable: %q", n)
	}
}
