package agentsd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// registerMac serves what a sandboxed window asks of its Mac's host (058, R12) — Finder,
// another app, Terminal — and what agents call the person (#121). Off a Mac the three
// verbs say so (mac_other.go), as the Swift host does.
func (d *Daemon) registerMac() {
	On(d, wire.CallMacReveal, func(_ context.Context, _ *Client, r wire.MacPathRequest) (wire.Empty, error) {
		if err := present(r.Path); err != nil {
			return wire.Empty{}, err
		}
		return wire.Empty{}, macOpen("-R", r.Path)
	})
	On(d, wire.CallMacOpen, func(_ context.Context, _ *Client, r wire.MacPathRequest) (wire.Empty, error) {
		if err := present(r.Path); err != nil {
			return wire.Empty{}, err
		}
		if r.App != nil && *r.App != "" {
			return wire.Empty{}, macOpen("-a", *r.App, r.Path)
		}
		return wire.Empty{}, macOpen(r.Path)
	})
	On(d, wire.CallMacTerminal, func(_ context.Context, _ *Client, r wire.MacTerminalRequest) (wire.Empty, error) {
		if r.Path != nil {
			return wire.Empty{}, macOpen("-a", "Terminal", *r.Path)
		}
		return wire.Empty{}, macOpen("-a", "Terminal")
	})

	On(d, wire.CallPersonState, func(context.Context, *Client, wire.Empty) (wire.PersonSettings, error) {
		d.personMu.Lock()
		defer d.personMu.Unlock()
		return readPerson(d.personFile()), nil
	})
	// person/set is said to agents from their next briefing on.
	On(d, wire.CallPersonSet, func(_ context.Context, _ *Client, p wire.PersonSettings) (wire.PersonSettings, error) {
		return d.setPerson(p)
	})
}

func (d *Daemon) personFile() string { return filepath.Join(d.SettingsDir, "person.json") }

// setPerson is person/set, and the part person of the user settings (#5573). The same
// settings again change nothing and say nothing.
func (d *Daemon) setPerson(p wire.PersonSettings) (wire.PersonSettings, error) {
	d.personMu.Lock()
	defer d.personMu.Unlock()
	b, err := storeJSON(p)
	if err == nil {
		if had, readErr := os.ReadFile(d.personFile()); readErr == nil && bytes.Equal(had, b) {
			return p, nil
		}
		err = settingFits(d.personFile(), b, personMostBytes)
		if err == nil {
			err = os.MkdirAll(d.SettingsDir, 0o700)
		}
		if err == nil {
			// store-bounds: kind=key-value; count=1; size=4KiB; age=until-deleted; applied=on write (refused)
			err = store.WriteAtomically(d.personFile(), b, 0o644)
		}
	}
	if err != nil {
		return wire.PersonSettings{}, &Error{Code: wire.FailureCouldNotSave, Message: "person.json: " + err.Error()}
	}
	Broadcast(d, wire.NotifyPersonChanged, p)
	return p, nil
}

// readPerson is person.json, empty when it is missing; one that will not read is set
// aside, as StoreFile.load does.
func readPerson(path string) wire.PersonSettings {
	var p wire.PersonSettings
	b, err := os.ReadFile(path)
	if err != nil {
		return p
	}
	if json.Unmarshal(b, &p) != nil {
		if err := os.Rename(path, fmt.Sprintf("%s.corrupt-%d", path, time.Now().Unix())); err != nil {
			log.Printf("%s: unreadable, and could not be set aside: %v", filepath.Base(path), err)
		}
		store.TrimSetAsides(path)
		return wire.PersonSettings{}
	}
	return p
}

// present is DaemonCore.gone's check: a path that is not there is fileGone.
func present(path string) error {
	if _, err := os.Stat(path); err != nil {
		return &Error{Code: wire.FailureFileGone, Message: filepath.Base(path) + " is gone."}
	}
	return nil
}
