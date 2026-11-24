package agentsd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/skillcatalog"
	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// #6727: a copy goes 90 days after the removal, and the ones from before copies had a time are
// dated, not lost. A copy that ages out brings nothing back.
func TestRemovedConnectorsAgeOut(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	personalHomeFor(t, home)
	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	path := filepath.Join(home, ".agents", "mcp.removed.json")
	must(t, os.MkdirAll(filepath.Dir(path), 0o700))
	must(t, os.WriteFile(path, []byte(`{"mcpServers": {"mine": {"command": "a"}, "theirs": {"url": "b"}}}`), 0o600))

	d.sweepSmallStores(nowFunc())
	got, err := d.removedConnectors.Read()
	if err != nil || len(got) != 2 || string(got["mine"].Entry) != `{"command":"a"}` || got["theirs"].RemovedAt == 0 {
		t.Fatalf("carried over: %+v, %v", got, err)
	}
	if aside, _ := filepath.Glob(path + ".*"); len(aside) != 0 {
		t.Fatalf("a copy with no age stayed: %v", aside)
	}

	old := nowFunc().Add(-store.MaxAge - time.Hour)
	must(t, d.removedConnectors.Update(func(m map[string]removedConnector) error {
		m["fresh"] = removedConnector{RemovedAt: wire.DateOf(old.Add(2 * time.Hour)), Entry: json.RawMessage(`{}`)}
		return nil
	}))
	b, err := os.ReadFile(path)
	must(t, err)
	var file map[string]map[string]any
	must(t, json.Unmarshal(b, &file))
	file["mine"]["removedAt"] = old.UTC().Format(time.RFC3339)
	b, err = json.Marshal(file)
	must(t, err)
	must(t, os.WriteFile(path, b, 0o600))
	d.sweepSmallStores(nowFunc())
	got, err = d.removedConnectors.Read()
	if _, there := got["mine"]; err != nil || there || len(got) != 2 {
		t.Fatalf("after the sweep: %+v, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".agents", "mcp.json")); !os.IsNotExist(err) {
		t.Fatalf("mcp.json was written: %v", err)
	}
}

// A copy that cannot be kept keeps the connector: nothing goes to make room, and nothing is lost.
func TestAFullRemovedFileRefusesTheRemoval(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	personalHomeFor(t, home)
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	mcp := filepath.Join(home, ".agents", "mcp.json")
	must(t, os.MkdirAll(filepath.Dir(mcp), 0o700))
	must(t, os.WriteFile(mcp, []byte(`{"mcpServers": {"mine": {"command": "a"}}}`), 0o600))
	for i := range 100 {
		must(t, d.keepRemovedConnector(fmt.Sprint("c", i), skillcatalog.Object()))
	}
	if err := d.keepRemovedConnector("c0", skillcatalog.Value{Kind: 's', String: strings.Repeat("x", 256<<10)}); !errors.Is(err, store.ErrLimit) {
		t.Fatalf("a copy over the largest: %v", err)
	}
	remove := map[string]any{"destination": map[string]any{"kind": "personal"}, "name": "mine"}
	if e := c.callError(t, 1, "mcp/remove", remove); e.Code != wire.FailureCatalogRefused {
		t.Fatalf("%+v", e)
	}
	if _, there, err := mcpEntryAt(mcp, "mine"); err != nil || !there {
		t.Fatalf("it went: %v", err)
	}
	got, err := d.removedConnectors.Read()
	if err != nil || len(got) != 100 {
		t.Fatalf("%d kept, %v", len(got), err)
	}
}
