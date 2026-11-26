package agentsd

import (
	"context"
	"net"
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

// fakeTunnels is a control plane that opens each tunnel, or refuses each with refuse.
type fakeTunnels struct {
	mu     sync.Mutex
	asked  []string
	refuse error
}

func (f *fakeTunnels) OpenTunnel(_ context.Context, runtime string) (controllink.Conn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, runtime)
	if f.refuse != nil {
		return nil, f.refuse
	}
	return &fakeTunnel{in: make(chan string)}, nil
}

func (f *fakeTunnels) refuseWith(err error) { f.mu.Lock(); defer f.mu.Unlock(); f.refuse = err }

func (f *fakeTunnels) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.asked) }

type fakeTunnel struct {
	in   chan string
	once sync.Once
}

func (t *fakeTunnel) Write(string) error { return nil }
func (t *fakeTunnel) In() <-chan string  { return t.in }
func (t *fakeTunnel) Close()             { t.once.Do(func() { close(t.in) }) }
func (t *fakeTunnel) Device() string     { return "" }

const keptCA = "-----BEGIN CERTIFICATE-----\nlender\n-----END CERTIFICATE-----\n"

// lend is relay/offer as the Mac's window sends it (Lending.swift).
func lend(t *testing.T, d *Daemon, runtime, standIn string) {
	t.Helper()
	if err := d.offerRelay(wire.RelayOffer{Runtime: runtime, SocketPath: "tunnel", CaCertificate: keptCA, StandIn: standIn}); err != nil {
		t.Fatal(err)
	}
}

// dialGate opens one connection to runtime's gate, as the runtime does.
func dialGate(t *testing.T, d *Daemon, runtime string) {
	t.Helper()
	rl := d.relayFor(runtime)
	if rl == nil {
		t.Fatalf("%s has no gate", runtime)
	}
	c, err := net.Dial("tcp", rl.ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
}

// #7369: a sign-in lent to this machine stays lent over a restart of its own agentsd. Its
// gate opens again before the pick-ups, with the stand-in the Mac granted, which the Mac
// still takes after its own restart (#7170).
func TestALentSignInIsLentAgainAfterARestart(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // no sign-in of this machine's own
	root := t.TempDir()
	d, _ := start(t, root)
	d.SetTunneler(&fakeTunnels{})
	lend(t, d, "claude", "agents-relay-claude")
	lend(t, d, "codex", `{"tokens":"agents-relay-codex"}`)
	d.Shutdown()

	kept := filepath.Join(root, "runtimes", relaysFile)
	info, err := os.Stat(kept)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("%s is %v, not 0600", relaysFile, info.Mode().Perm())
	}

	again, _ := start(t, root)
	rl := again.relayFor("claude")
	if rl == nil {
		t.Fatal("claude's lent sign-in went with the restart")
	}
	if rl.standIn != "agents-relay-claude" {
		t.Fatalf("claude's stand-in after the restart: %q", rl.standIn)
	}
	if b, err := os.ReadFile(rl.caFile); err != nil || string(b) != keptCA {
		t.Fatalf("claude's CA after the restart: %q, %v", b, err)
	}
	if again.relayFor("codex") == nil {
		t.Fatal("codex's lent sign-in went with the restart")
	}
	env, err := again.withRuntimeOwnEnv(runtimeByID("claude"), t.TempDir(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !hasVariable(env, "CLAUDE_CODE_OAUTH_TOKEN") || !strings.Contains(strings.Join(env, "\n"), "CLAUDE_CODE_OAUTH_TOKEN=agents-relay-claude") {
		t.Fatalf("claude does not start with the lent sign-in: %q", env)
	}
	// A pick-up is not refused for a sign-in: the gate is there.
	tunnels := &fakeTunnels{}
	again.SetTunneler(tunnels)
	if err := again.needsSignIn("claude"); err != nil {
		t.Fatalf("a pick-up after the restart is refused: %v", err)
	}
	// Its connections go through the control plane again, once it is connected.
	dialGate(t, again, "claude")
	waitFor(t, "a tunnel through the gate kept over the restart", func() bool { return tunnels.count() == 1 })
}

// #7369: when the control plane says no host lends the sign-in now (the Mac took it back),
// the gate and what was kept of it go, so a restart does not open it again.
func TestALendTakenBackIsDropped(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	d, _ := start(t, root)
	tunnels := &fakeTunnels{}
	d.SetTunneler(tunnels)
	lend(t, d, "claude", "agents-relay-claude")
	lend(t, d, "codex", `{"tokens":"agents-relay-codex"}`)

	// Not a refusal of the lend: kept.
	tunnels.refuseWith(&controllink.AskError{Code: -32006, Message: "The host that lends this sign-in is offline."})
	dialGate(t, d, "claude")
	waitFor(t, "a first tunnel", func() bool { return tunnels.count() == 1 })
	if d.relayFor("claude") == nil {
		t.Fatal("a lender that is offline dropped the lend")
	}

	tunnels.refuseWith(&controllink.AskError{Code: notPermitted, Message: "No host lends this host its claude sign-in."})
	dialGate(t, d, "claude")
	waitFor(t, "dropped the lend taken back", func() bool { return d.relayFor("claude") == nil })
	if _, ok := d.keptRelays()["claude"]; ok {
		t.Fatal("claude's lend is still kept")
	}
	if _, err := os.Stat(filepath.Join(root, "runtimes", "claude-relay-ca.pem")); !os.IsNotExist(err) {
		t.Fatalf("claude's CA is still there: %v", err)
	}
	if _, ok := d.keptRelays()["codex"]; !ok || d.relayFor("codex") == nil {
		t.Fatal("codex's lend went with claude's")
	}
	d.Shutdown()
	again, _ := start(t, root)
	if again.relayFor("claude") != nil {
		t.Fatal("a lend taken back was opened again after a restart")
	}
	if again.relayFor("codex") == nil {
		t.Fatal("codex's lend did not open again")
	}
}

// #7369: a machine taken out of the lock keeps no sign-in lent to it.
func TestAMachineThatLeavesTheLockDropsItsLentSignIns(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	d, key, _ := joinedMachine(t)
	d.SetTunneler(&fakeTunnels{})
	mac, invite := newLockKey(t), newLockKey(t)
	root := lockEntry(t, mac, wire.LockEntryKindRoot, mac, nil)
	h := machinelock.HashOf(root)
	inv, err := machinelock.Sign(mac, wire.LockBody{Kind: wire.LockEntryKindInvite, Person: "sam", Previous: h[:],
		At: wire.DateOf(time.Now()), Expires: new(wire.DateOf(time.Now().Add(5 * time.Minute))), Key: machinelock.PublicKey(invite)})
	must(t, err)
	state, err := machinelock.Check([]wire.LockEntry{root, inv})
	must(t, err)
	add, err := machinelock.AddMachine(invite, state, key, "h-1", "box", time.Now())
	must(t, err)
	in := []wire.LockEntry{root, inv, add}
	if !d.TakeLock(wire.Lock{Person: "sam", Entries: in}) {
		t.Fatal("the lock with this machine in it was not taken")
	}
	lend(t, d, "claude", "agents-relay-claude")

	// A lock that grows with this machine still in it keeps the lend.
	phone := newLockKey(t)
	grown := append(in, lockEntry(t, mac, wire.LockEntryKindAddClient, phone, &in[len(in)-1]))
	if !d.TakeLock(wire.Lock{Person: "sam", Entries: grown}) || d.relayFor("claude") == nil {
		t.Fatal("a lock that still names this machine dropped the lend")
	}

	hh := machinelock.HashOf(grown[len(grown)-1])
	remove, err := machinelock.Sign(mac, wire.LockBody{Kind: wire.LockEntryKindRemove, Person: "sam", Previous: hh[:],
		At: wire.DateOf(time.Now()), Key: key})
	must(t, err)
	if !d.TakeLock(wire.Lock{Person: "sam", Entries: append(grown, remove)}) {
		t.Fatal("the lock with this machine removed was not taken")
	}
	if d.relayFor("claude") != nil {
		t.Fatal("a machine out of the lock kept its gate")
	}
	if _, err := os.Stat(filepath.Join(d.Store.Loc.Root, "runtimes", relaysFile)); !os.IsNotExist(err) {
		t.Fatalf("a machine out of the lock kept the lend on the disk: %v", err)
	}
}

// #7369: the kept file is bounded: one too large, or a runtime that is never lent, opens nothing.
func TestTheKeptLendsAreBounded(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	dir := filepath.Join(root, "runtimes")
	must(t, os.MkdirAll(dir, 0o700))
	must(t, os.WriteFile(filepath.Join(dir, relaysFile),
		[]byte(`{"grok":{"standIn":"x","caCertificate":"y"},"claude":{"standIn":"`+strings.Repeat("a", relaysMostBytes)+`","caCertificate":"y"}}`), 0o600))
	d, _ := start(t, root)
	if d.relayFor("claude") != nil || d.relayFor("grok") != nil {
		t.Fatal("a kept file over its bound was opened")
	}
	d.SetTunneler(&fakeTunnels{})
	if err := d.offerRelay(wire.RelayOffer{Runtime: "grok", StandIn: "x", CaCertificate: "y"}); err == nil {
		t.Fatal("a runtime that is never lent was taken")
	}
}
