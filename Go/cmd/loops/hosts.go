package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"slices"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/machinelock"
)

// hosts is ~/.config/loops/hosts.json (mode 0600): one pairing per control plane, as gh
// keeps one entry per host in ~/.config/gh/hosts.yml. The CLI keeps nothing else.
//
// It is not in ~/.agents, on purpose (#6807, as #6800): a pairing is the identity of one
// terminal, with its private key, and a person syncs ~/.agents. The default and the lock
// pins mean nothing without the pairings, so they stay with them.
type hosts struct {
	// Default is the URL of the control plane a command uses with no --control.
	Default string                         `json:"default,omitempty"`
	Hosts   []controllink.ClientMembership `json:"hosts"`
	// LockPins is the head of the machine lock each control plane last sent, by its URL
	// (#6563): a lock that does not grow from it is not taken.
	LockPins map[string]machinelock.Pin `json:"lockPins,omitempty"`
}

func (h *hosts) lockPin(u string) machinelock.Pin { return h.LockPins[u] }

func (h *hosts) setLockPin(u string, p machinelock.Pin) {
	if h.LockPins == nil {
		h.LockPins = map[string]machinelock.Pin{}
	}
	h.LockPins[u] = p
}

// The most that hosts.json holds: a pairing more is refused, as each is the person's own.
const (
	maxHosts     = 20
	maxCodeBytes = 2 << 10 // a pairing holds what its code holds, a key and two names
	maxHostBytes = 4 << 10 // a pairing as it is kept: the control plane gives the client's id
)

// fits refuses a pairing that is too large to keep.
func fits(m *controllink.ClientMembership) error {
	b, err := json.MarshalIndent(m, "", "  ") //nolint:gosec // G117: only its size is used
	if err != nil {
		return err
	}
	if len(b) > maxHostBytes {
		return fmt.Errorf("it is %d bytes, at most %d", len(b), maxHostBytes)
	}
	return nil
}

// room says why a pairing by the code text with the control plane at u cannot be kept, if
// it cannot. A pairing with the same control plane replaces the old one.
func (h *hosts) room(text, u string) error {
	if len(text) > maxCodeBytes {
		return &exit{code: exitError, msg: "That pairing code is too long."}
	}
	if len(h.Hosts) < maxHosts || slices.ContainsFunc(h.Hosts, func(m controllink.ClientMembership) bool { return m.URL == u }) {
		return nil
	}
	return &exit{code: exitError, msg: fmt.Sprintf("This terminal is paired with %d control planes, the most it keeps.\n"+
		"First unpair from one: loops auth logout --control %s", len(h.Hosts), hostOf(h.Hosts[0].URL))}
}

// configDir is $XDG_CONFIG_HOME/loops, else ~/.config/loops.
func configDir() string {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "loops")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "loops")
}

func hostsFile() string { return filepath.Join(configDir(), "hosts.json") }

// readHosts reads the file; a missing file is no pairing, not an error.
func readHosts() (*hosts, error) {
	b, err := os.ReadFile(hostsFile())
	if errors.Is(err, fs.ErrNotExist) {
		return &hosts{}, nil
	}
	if err != nil {
		return nil, err
	}
	var h hosts
	if err := json.Unmarshal(b, &h); err != nil {
		return nil, fmt.Errorf("%s: %w", hostsFile(), err)
	}
	return &h, nil
}

// write saves the file whole, through a file of mode 0600 renamed over it, so a reader
// never sees half of it and the keys are never readable by others.
func (h *hosts) write() error {
	dir := configDir()
	// A pin whose pairing went is of no use.
	maps.DeleteFunc(h.LockPins, func(u string, _ machinelock.Pin) bool {
		return !slices.ContainsFunc(h.Hosts, func(m controllink.ClientMembership) bool { return m.URL == u })
	})
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return err
	}
	// store-bounds: kind=secret; count=20; size=4KiB a record; age=until-deleted; applied=before a pairing, and loops auth logout deletes it
	tmp, err := os.CreateTemp(dir, ".hosts-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), hostsFile())
}

// put adds m, or replaces the pairing with the same control plane, and makes it the default.
func (h *hosts) put(m controllink.ClientMembership) {
	for i := range h.Hosts {
		if h.Hosts[i].URL == m.URL {
			h.Hosts[i] = m
			h.Default = m.URL
			return
		}
	}
	h.Hosts = append(h.Hosts, m)
	h.Default = m.URL
}

// pick is the pairing for --control (a URL or its host name), else the default one, else
// the only one. Nil when there is none.
func (h *hosts) pick(control string) *controllink.ClientMembership {
	want := control
	if want == "" {
		want = h.Default
	}
	for i := range h.Hosts {
		if h.Hosts[i].URL == want || control != "" && hostOf(h.Hosts[i].URL) == control {
			return &h.Hosts[i]
		}
	}
	if control == "" && len(h.Hosts) > 0 {
		return &h.Hosts[0]
	}
	return nil
}

// remove takes out the pairing with this URL; the default moves to the first one left.
func (h *hosts) remove(u string) {
	kept := h.Hosts[:0]
	for _, m := range h.Hosts {
		if m.URL != u {
			kept = append(kept, m)
		}
	}
	h.Hosts = kept
	delete(h.LockPins, u)
	if h.Default == u {
		h.Default = ""
		if len(kept) > 0 {
			h.Default = kept[0].URL
		}
	}
}

// hostOf is a control plane's host name, as people say it: control.agentloops.dev.
func hostOf(u string) string {
	p, err := url.Parse(u)
	if err != nil || p.Host == "" {
		return u
	}
	return p.Hostname()
}
