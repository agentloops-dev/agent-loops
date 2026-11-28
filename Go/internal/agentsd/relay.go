package agentsd

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

const (
	// relaysFile keeps each sign-in lent to this machine, in <root>/runtimes, so that its
	// gate opens again after a restart of this host (#7369). The stand-in in it opens the
	// lender's relay, not the lender's own sign-in, and only while the lender keeps its hash
	// (#7170). It is never logged and goes nowhere else.
	relaysFile = "relays.json"
	// relaysMostBytes bounds the file: a stand-in and a CA for each runtime that is lent.
	relaysMostBytes = 64 << 10
	// notPermitted is the control plane's refusal of a tunnel that no lend allows now.
	notPermitted = -32045
)

// lentRuntimes are the runtimes whose sign-in another host lends (Lending.swift): one entry
// each, at most, in relaysFile.
var lentRuntimes = map[string]bool{"claude": true, "codex": true}

// keptRelay is one offer as relaysFile keeps it.
type keptRelay struct {
	StandIn       string `json:"standIn"`
	CaCertificate string `json:"caCertificate"`
}

// Tunneler opens a tunnel to the host that lends a runtime's sign-in.
type Tunneler interface {
	OpenTunnel(ctx context.Context, runtime string) (controllink.Conn, error)
}

type tunnelerOf struct{ Tunneler }

// SetTunneler is the control plane's link, that a sign-in lent to this host tunnels through.
func (d *Daemon) SetTunneler(t Tunneler) { d.tunneler.Store(&tunnelerOf{t}) }

// tunnels is the link that SetTunneler set; nil when there is none.
func (d *Daemon) tunnels() Tunneler {
	if t := d.tunneler.Load(); t != nil {
		return t.Tunneler
	}
	return nil
}

// relay is a sign-in lent by another host (056, T091): a TCP gate on loopback the runtime
// is pointed at, each of whose connections is a tunnel through the control plane to the
// lender's TLS relay. TLS ends on the lender; this host and the control plane see ciphertext.
type relay struct {
	port    int
	standIn string
	caFile  string
	ln      net.Listener
}

func (d *Daemon) relayFor(runtime string) *relay {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.relays[runtime]
}

func (d *Daemon) offerRelay(o wire.RelayOffer) error {
	if d.tunnels() == nil {
		return &Error{Code: CodeServer, Message: "this machine has no control plane to tunnel through"}
	}
	if !lentRuntimes[o.Runtime] {
		return Invalid("relay/offer: %s's sign-in is not lent", o.Runtime)
	}
	if o.Runtime == "claude" && d.storedClaudeSignIn() != "" {
		// This server has its own sign-in, which wins (#7470): the lend is not opened.
		log.Printf("relay: claude's lent sign-in is not opened: this server has its own")
		return nil
	}
	if o.Runtime == "codex" && d.ownCodexSignIn() {
		log.Printf("relay: codex's lent sign-in is not opened: this server has its own")
		return nil
	}
	if err := d.openRelay(o); err != nil {
		return err
	}
	// A fresh offer replaces the kept one. One that is not kept still works until a restart.
	if err := d.keepRelay(o.Runtime, &keptRelay{StandIn: o.StandIn, CaCertificate: o.CaCertificate}); err != nil {
		log.Printf("relay: %s's lent sign-in is not kept over a restart: %v", o.Runtime, err)
	}
	return nil
}

func (d *Daemon) relaysDir() string { return filepath.Join(d.Store.Loc.Root, "runtimes") }

// openRelay opens runtime's gate, or points the open one at the new stand-in.
func (d *Daemon) openRelay(o wire.RelayOffer) error {
	dir := d.relaysDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	ca := filepath.Join(dir, o.Runtime+"-relay-ca.pem")
	// store-bounds: not a store: a launch file: the CA of a relay offer, written again at each offer.
	if err := os.WriteFile(ca, []byte(o.CaCertificate), 0o600); err != nil {
		return err
	}
	d.mu.Lock()
	old := d.relays[o.Runtime]
	d.mu.Unlock()
	if old != nil {
		old.standIn, old.caFile = o.StandIn, ca
		if o.Runtime == "codex" {
			return writeCodexHome(dir, old.port, o.StandIn)
		}
		return nil
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	r := &relay{port: ln.Addr().(*net.TCPAddr).Port, standIn: o.StandIn, caFile: ca, ln: ln}
	d.mu.Lock()
	d.relays[o.Runtime] = r
	d.mu.Unlock()
	log.Printf("relay: %s's sign-in is lent; gate on 127.0.0.1:%d", o.Runtime, r.port)
	if o.Runtime == "codex" {
		if err := writeCodexHome(dir, r.port, o.StandIn); err != nil {
			return err
		}
	}
	d.goJob("relay gate", func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			d.goJob("relay pump", func() { d.pump(o.Runtime, r, c) })
		}
	})
	return nil
}

// relaysPath is relaysFile's place.
func (d *Daemon) relaysPath() string { return filepath.Join(d.relaysDir(), relaysFile) }

// keptRelays is what relaysFile holds: none when it is missing, too large or not readable.
func (d *Daemon) keptRelays() map[string]keptRelay {
	kept := map[string]keptRelay{}
	f, err := os.Open(d.relaysPath())
	if err != nil {
		return kept
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, relaysMostBytes+1))
	if err == nil {
		err = tooLarge(relaysFile, b, relaysMostBytes)
	}
	if err == nil {
		err = json.Unmarshal(b, &kept)
	}
	if err != nil {
		log.Printf("relay: the kept lent sign-ins are not read: %v", err)
		return map[string]keptRelay{}
	}
	for runtime := range kept {
		if !lentRuntimes[runtime] {
			delete(kept, runtime)
		}
	}
	return kept
}

// keepRelay writes runtime's offer into relaysFile, or takes it out with nil: whole, 0600.
func (d *Daemon) keepRelay(runtime string, k *keptRelay) error {
	d.relaysFileMu.Lock()
	defer d.relaysFileMu.Unlock()
	kept := d.keptRelays()
	if k == nil {
		if _, had := kept[runtime]; !had {
			return nil
		}
		delete(kept, runtime)
	} else {
		kept[runtime] = *k
	}
	if len(kept) == 0 {
		if err := os.Remove(d.relaysPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	b, err := json.Marshal(kept)
	if err == nil {
		err = tooLarge(relaysFile, b, relaysMostBytes)
	}
	if err != nil {
		return err
	}
	// store-bounds: kind=secret; count=2; size=64KiB; age=until-deleted; applied=on write (refused), an entry per lent runtime
	return store.WriteAtomically(d.relaysPath(), b, 0o600)
}

// reopenRelays opens again each gate that relaysFile keeps (#7369), before the pick-ups ask
// whether a runtime needs a sign-in. Tunnels go through the control plane once it is
// connected; the lender's kept hash answers the kept stand-in (#7170).
func (d *Daemon) reopenRelays() {
	for runtime, k := range d.keptRelays() {
		if runtime == "claude" && d.storedClaudeSignIn() != "" || runtime == "codex" && d.ownCodexSignIn() {
			d.dropRelay(runtime, "this server has its own sign-in")
			continue
		}
		if err := d.openRelay(wire.RelayOffer{Runtime: runtime, StandIn: k.StandIn, CaCertificate: k.CaCertificate}); err != nil {
			log.Printf("relay: %s's lent sign-in could not be opened again: %v", runtime, err)
			continue
		}
		log.Printf("relay: %s's lent sign-in is open again, as lent before the restart", runtime)
	}
}

// dropRelay closes runtime's gate and forgets its offer, here and on the disk: the lend was
// taken back, or this machine left the lock. A runtime is then signed out until lent again.
func (d *Daemon) dropRelay(runtime, why string) {
	d.mu.Lock()
	r := d.relays[runtime]
	delete(d.relays, runtime)
	d.mu.Unlock()
	if r != nil && r.ln != nil {
		_ = r.ln.Close() // a gate closed already is closed
	}
	if err := d.keepRelay(runtime, nil); err != nil {
		log.Printf("relay: %s's kept sign-in could not be removed: %v", runtime, err)
	}
	dir := d.relaysDir()
	// What the offer wrote for the runtime goes with it; one that is not there is gone.
	_ = os.Remove(filepath.Join(dir, runtime+"-relay-ca.pem"))
	if runtime == "codex" {
		_ = os.Remove(filepath.Join(dir, "codex-relay-home", "auth.json"))
	}
	if r != nil {
		log.Printf("relay: %s's lent sign-in is dropped: %s", runtime, why)
	}
}

// dropRelays drops every lent sign-in.
func (d *Daemon) dropRelays(why string) {
	runtimes := map[string]bool{}
	d.mu.Lock()
	for runtime := range d.relays {
		runtimes[runtime] = true
	}
	d.mu.Unlock()
	for runtime := range d.keptRelays() {
		runtimes[runtime] = true
	}
	for runtime := range runtimes {
		d.dropRelay(runtime, why)
	}
}

// stopRelayGates closes the gates at shutdown; what relaysFile keeps stays for the next start.
func (d *Daemon) stopRelayGates() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, r := range d.relays {
		if r.ln != nil {
			_ = r.ln.Close() // the next start opens it again from relaysFile
		}
	}
}

// refusedLend says that the control plane refused a tunnel because no lend allows it now.
func refusedLend(err error) bool {
	var refused *controllink.AskError
	return errors.As(err, &refused) && refused.Code == notPermitted
}

// pump carries one TCP connection through one tunnel: bytes as base64 JSON strings, 32 KiB at most.
func (d *Daemon) pump(runtime string, r *relay, c net.Conn) {
	defer c.Close()
	tunnels := d.tunnels()
	if tunnels == nil {
		return
	}
	t, err := tunnels.OpenTunnel(d.ctx, runtime)
	if err != nil {
		log.Printf("relay: %v", err)
		// The lend was taken back: only a gate that a newer offer did not replace goes.
		if refusedLend(err) && d.relayFor(runtime) == r {
			d.dropRelay(runtime, "the control plane says no host lends it now")
		}
		return
	}
	var once sync.Once
	done := func() { once.Do(func() { t.Close(); c.Close() }) }
	d.goJob("relay tunnel", func() {
		defer done()
		for line := range t.In() {
			var s string
			if json.Unmarshal([]byte(line), &s) != nil {
				continue
			}
			b, err := base64.StdEncoding.DecodeString(s)
			if err != nil {
				continue
			}
			if _, err := c.Write(b); err != nil {
				return
			}
		}
	})

	buf := make([]byte, 32<<10)
	for {
		n, err := c.Read(buf)
		if n > 0 {
			if t.Write(strconv.Quote(base64.StdEncoding.EncodeToString(buf[:n]))) != nil {
				break
			}
		}
		if err != nil {
			break
		}
	}
	done()
}

// writeCodexHome is the CODEX_HOME a lent ChatGPT sign-in runs Codex with: its base URL
// the gate, its auth.json the stand-in the lender granted (DaemonCore+Credentials.swift).
func writeCodexHome(dir string, port int, standIn string) error {
	home := filepath.Join(dir, "codex-relay-home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	config := "chatgpt_base_url = \"https://127.0.0.1:" + strconv.Itoa(port) + "/backend-api/\"\n"
	// store-bounds: not a store: a launch file of a lent sign-in, written again at each lend.
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0o600); err != nil {
		return err
	}
	// store-bounds: not a store: a launch file of a lent sign-in, written again at each lend.
	return os.WriteFile(filepath.Join(home, "auth.json"), []byte(standIn), 0o600)
}

func ownCodexSignIn() bool {
	if os.Getenv("OPENAI_API_KEY") != "" {
		return true
	}
	home, _ := os.UserHomeDir()
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" {
		codexHome = filepath.Join(home, ".codex")
	}
	return codexSignInIn(codexHome)
}

func codexSignInIn(codexHome string) bool {
	_, err := os.Stat(filepath.Join(codexHome, "auth.json"))
	return err == nil
}

// ownCodexSignIn reads this server's sign-in from the daemon-owned home. The Mac keeps its
// own Codex home, and added accounts have their own folders.
func (d *Daemon) ownCodexSignIn() bool {
	if os.Getenv("OPENAI_API_KEY") != "" || macToolsHome != "" || d.Store == nil {
		return ownCodexSignIn()
	}
	return codexSignInIn(filepath.Join(d.Store.Loc.Root, "runtimes", "codex", "home"))
}

func (d *Daemon) ensureCodexHome() (string, error) {
	if d.Store == nil {
		return os.Getenv("CODEX_HOME"), nil
	}
	home := filepath.Join(d.Store.Loc.Root, "runtimes", "codex", "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(home, 0o700); err != nil { //nolint:gosec // G302: a folder needs its x bit; 0700 is owner only
		return "", err
	}
	return home, nil
}

// signedIn says whether this machine has Claude's own sign-in.
func ownClaudeSignIn() bool {
	if os.Getenv("CLAUDE_CODE_OAUTH_TOKEN") != "" || os.Getenv("ANTHROPIC_API_KEY") != "" {
		return true
	}
	home, _ := os.UserHomeDir()
	if _, err := os.Stat(filepath.Join(home, ".claude", ".credentials.json")); err == nil {
		return true
	}
	return false
}
