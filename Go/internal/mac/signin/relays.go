package signin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Policy is a runtime's SignInRelay, as ToolPolicyCatalog has it: where its service is and
// where this Mac keeps its sign-in (a Keychain item, or a file under the home folder).
type Policy struct {
	Upstream string
	Keychain string
	File     string
}

// Policies are the runtimes a Mac relays the sign-in of (ToolPolicyCatalog).
var Policies = map[string]Policy{
	"claude": {Upstream: "api.anthropic.com", Keychain: "Claude Code-credentials"},
	"codex":  {Upstream: "chatgpt.com", File: ".codex/auth.json"},
}

// Relays is HostSignInRelays: one relay per runtime, started on its first grant, or on the
// first tunnel after a restart of a host that granted it (#7170), and kept for the host's
// life. Its keys and certificates are in <root>/relay; its log, which says
// only what was asked and how it was answered, is host-relay.log, rolled at 1 MB.
type Relays struct {
	Root, Logs, Home string
	// TestKeychain is a walk's stand-in for Claude's item: never renewed (debug builds).
	TestKeychain string

	mu      sync.Mutex
	relays  map[string]*Relay
	sources map[string]Source
	stopped bool
	logMu   sync.Mutex
}

// Grant is the reply to relay/grant.
type Grant struct {
	Runtime, CACertificate, StandIn string
}

// SignIn is this Mac's own sign-in for runtime, where its policy says it is kept.
func (s *Relays) SignIn(runtime string) Source {
	s.mu.Lock()
	defer s.mu.Unlock()
	if src := s.sources[runtime]; src != nil {
		return src
	}
	p, ok := Policies[runtime]
	if !ok {
		return nil
	}
	var src Source
	if p.File != "" {
		src = NewCodex(filepath.Join(s.Home, p.File))
	} else if s.TestKeychain != "" {
		src = NewClaude(s.TestKeychain, true)
	} else {
		src = NewClaude(p.Keychain, false)
	}
	if s.sources == nil {
		s.sources = map[string]Source{}
	}
	s.sources[runtime] = src
	return src
}

// Grant starts relaying runtime's sign-in and says what the borrower's gate needs: the
// relay's CA and a stand-in holding no secret of this Mac's, fresh each time.
func (s *Relays) Grant(runtime string) (Grant, error) {
	p, ok := Policies[runtime]
	src := s.SignIn(runtime)
	if !ok || src == nil {
		return Grant{}, fmt.Errorf("This Mac does not relay %s's sign-in.", runtime)
	}
	if _, err := src.Current(); err != nil {
		return Grant{}, fmt.Errorf("This Mac is not signed in to %s the way a server can use.", runtime)
	}
	certs := s.certs()
	s.mu.Lock()
	r := s.relay(runtime, p, src)
	s.mu.Unlock()
	if _, err := r.Start(); err != nil {
		return Grant{}, err
	}
	standIn, err := src.StandIn()
	if err != nil {
		return Grant{}, err
	}
	ca, err := certs.CAPEM()
	if err != nil {
		return Grant{}, err
	}
	bearer := ClientBearer(standIn)
	r.ExpectClientBearer(bearer)
	if err := s.keep(runtime, bearerHash(bearer)); err != nil {
		// The lend works until this host restarts.
		s.write("the grant of " + runtime + " could not be kept: " + err.Error())
	}
	s.write("granted " + runtime + " for a tunnel")
	return Grant{Runtime: runtime, CACertificate: ca, StandIn: standIn}, nil
}

// Port is where runtime's relay listens once granted: what a tunnel connects to; 0 if none.
// A borrower keeps its stand-in over this host's restart and does not ask again, so a grant
// made before the restart starts its relay here (#7170). The sign-in itself is read only
// when a request comes.
func (s *Relays) Port(runtime string) int {
	src := s.SignIn(runtime)
	s.mu.Lock()
	defer s.mu.Unlock()
	if r := s.relays[runtime]; r != nil {
		return r.Port()
	}
	if src == nil || s.stopped {
		return 0
	}
	hash := s.granted()[runtime]
	if hash == "" {
		return 0
	}
	r := s.relay(runtime, Policies[runtime], src)
	r.bearer = hash // nobody else has r before s.mu is let go
	port, err := r.Start()
	if err != nil {
		delete(s.relays, runtime)
		s.write("relaying " + runtime + " again failed: " + err.Error())
		return 0
	}
	s.write("relaying " + runtime + " again, as granted before the restart")
	return port
}

// relay is runtime's relay, made if need be, with the lock held.
func (s *Relays) relay(runtime string, p Policy, src Source) *Relay {
	r := s.relays[runtime]
	if r == nil {
		r = &Relay{Upstream: p.Upstream, Source: src, Certs: s.certs(), Log: s.write}
		if s.relays == nil {
			s.relays = map[string]*Relay{}
		}
		s.relays[runtime] = r
	}
	return r
}

func (s *Relays) certs() Certificates { return Certificates{Folder: filepath.Join(s.Root, "relay")} }

// grantedFile holds the hash of the last stand-in bearer granted for each runtime, never
// the bearer: an entry per runtime of Policies at most.
func (s *Relays) grantedFile() string { return filepath.Join(s.certs().Folder, "granted.json") }

// granted is what grantedFile holds, with the lock held; empty when there is none to read.
func (s *Relays) granted() map[string]string {
	kept := map[string]string{}
	b, err := os.ReadFile(s.grantedFile())
	if err == nil {
		err = json.Unmarshal(b, &kept)
	}
	if err != nil && !os.IsNotExist(err) {
		s.write("the kept grants could not be read: " + err.Error())
	}
	if err != nil {
		return map[string]string{}
	}
	out := map[string]string{}
	for runtime := range Policies {
		if h := kept[runtime]; h != "" {
			out[runtime] = h
		}
	}
	return out
}

// keep writes the hash of runtime's granted bearer, whole or not at all.
func (s *Relays) keep(runtime, hash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	all := s.granted()
	all[runtime] = hash
	b, err := json.Marshal(all)
	if err != nil {
		return err
	}
	next := s.grantedFile() + ".new"
	if err := os.WriteFile(next, b, 0o600); err != nil {
		return err
	}
	return os.Rename(next, s.grantedFile())
}

// Stop closes every relay.
func (s *Relays) Stop() {
	s.mu.Lock()
	all := s.relays
	s.relays, s.stopped = nil, true
	s.mu.Unlock()
	for _, r := range all {
		r.Stop()
	}
}

const logLimit = 1 << 20

func (s *Relays) write(line string) {
	if s.Logs == "" {
		return
	}
	s.logMu.Lock()
	defer s.logMu.Unlock()
	file := filepath.Join(s.Logs, "host-relay.log")
	if fi, err := os.Stat(file); err == nil && fi.Size() > logLimit {
		// This is the log, so a failed roll has nowhere to be said; the log grows until one works.
		_ = os.Rename(file, filepath.Join(s.Logs, "host-relay.previous.log"))
	}
	f, err := os.OpenFile(file, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().UTC().Format("2006-01-02T15:04:05Z"), line)
}
