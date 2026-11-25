package agentsd

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/skillcatalog"
	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// removedConnector is an entry taken out of the personal mcp.json (#2195), by its name in
// ~/.agents/mcp.removed.json. mcp.json is not in git, so this is where one written by hand comes
// back from, by the person's hand: the app never reads it to connect anything. It may hold a token
// typed in, so the file is the person's alone to read.
type removedConnector struct {
	RemovedAt wire.Date       `json:"removedAt"`
	Entry     json.RawMessage `json:"entry"`
}

// removedConnectorsPath is "" with no personal home: a file that is not there, never a relative one.
func (d *Daemon) removedConnectorsPath() string {
	home := d.personalHome()
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".agents", "mcp.removed.json")
}

// openRemovedConnectors opens the copies. A copy can be the person's only one, so none goes to
// make room: the store refuses, and the removal with it.
func (d *Daemon) openRemovedConnectors() *store.JSONFile[removedConnector] {
	return smallFile(d, store.Spec{Name: "mcp.removed.json", Kind: "key-value", Scope: "person",
		Bounds: store.Bounds{MaxRecordBytes: 256 << 10, MaxCount: 100, MaxAge: store.MaxAge, Action: store.Refuse}},
		d.removedConnectorsPath, func(c *removedConnector) *wire.Date { return &c.RemovedAt })
}

// keepRemovedConnector keeps a copy of an entry about to be removed.
func (d *Daemon) keepRemovedConnector(name string, entry skillcatalog.Value) error {
	if err := d.carryRemovedConnectors(); err != nil {
		return err
	}
	return d.removedConnectors.Update(func(m map[string]removedConnector) error {
		m[name] = removedConnector{RemovedAt: now(), Entry: json.RawMessage(entry.Stringified())}
		return nil
	})
}

// carryRemovedConnectors dates the copies in a file from before #6727, which has mcp.json's own
// shape, so each is kept 90 days from now. A copy can be the person's only one, so one over the
// limits comes over as it was (#7181). It runs before each read of the file: the typed file
// reads that shape as one empty entry.
func (d *Daemon) carryRemovedConnectors() error {
	const top = "mcpServers"
	raw, err := os.ReadFile(d.removedConnectorsPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var old struct {
		Servers map[string]json.RawMessage `json:"mcpServers"`
	}
	// A connector of that name has a time, in the file's shape now; an entry of mcp.json does not.
	if json.Unmarshal(raw, &old) != nil || old.Servers == nil || old.Servers["removedAt"] != nil {
		return nil //nolint:nilerr // a file in no shape known is left to the typed file, which sets it aside
	}
	return d.removedConnectors.CarryOver(func(m map[string]removedConnector) error {
		delete(m, top)
		for name, entry := range old.Servers {
			if _, there := m[name]; !there {
				m[name] = removedConnector{RemovedAt: now(), Entry: entry}
			}
		}
		return nil
	})
}
