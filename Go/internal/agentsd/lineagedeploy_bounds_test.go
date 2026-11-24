package agentsd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/diskjson"
	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// #6719: a deploy goes 90 days after its last event, from a walk and from the file in the sweep;
// a file from before the limits keeps its deploys, and past the count the oldest goes.
func TestDeploysAgeOut(t *testing.T) {
	root, now := t.TempDir(), nowFunc()
	seen := func(ago time.Duration) deployRecord {
		return deployRecord{Deploy: wire.Deploy{State: wire.DeployStateSucceeded}, Environments: []string{}, Seen: wire.DateOf(now.Add(-ago))}
	}
	data, err := diskjson.Marshal(map[string]map[string]deployRecord{"deploys": {"aged": seen(store.MaxAge + time.Hour), "kept": seen(time.Hour)}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "lineage-deploys.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := New(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Shutdown)
	onDisk := func() map[string]deployRecord {
		t.Helper()
		m, err := d.deployStore.Read()
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	inWalk := func() map[string]deployRecord {
		d.deployBookMu.Lock()
		defer d.deployBookMu.Unlock()
		return d.deploysFileLocked().Deploys
	}
	if m := onDisk(); len(m) != 1 || m["kept"].Deploy.State != wire.DeployStateSucceeded {
		t.Fatalf("brought over %+v", m)
	}

	// An aged record on disk: a walk does not say it, and the sweep takes it.
	if data, err = diskjson.Marshal(map[string]deployRecord{"aged": seen(store.MaxAge), "kept": seen(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(d.deploysPath(), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if m := inWalk(); len(m) != 1 {
		t.Fatalf("a walk has %d deploys", len(m))
	}
	if m := onDisk(); len(m) != 2 {
		t.Fatalf("%d on disk before the sweep", len(m))
	}
	d.sweepSmallStores(now)
	if m := onDisk(); len(m) != 1 {
		t.Fatalf("%d on disk after the sweep", len(m))
	}

	// One over the count, with the oldest first to go.
	full := map[string]deployRecord{}
	for i := range deploysKept {
		full["d"+strings.Repeat("x", i%7)+time.Duration(i).String()] = seen(time.Hour)
	}
	if err := d.deployStore.Update(func(m map[string]deployRecord) error {
		clear(m)
		for k, r := range full {
			m[k] = r
		}
		m["oldest"] = seen(48 * time.Hour)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	d.keepDeploy("new", seen(0))
	m := inWalk()
	if _, there := m["oldest"]; there || len(m) != deploysKept {
		t.Fatalf("%d kept, the oldest there: %v", len(m), there)
	}
	if _, there := m["new"]; !there {
		t.Fatal("the new deploy went")
	}

	// A record too large for its entry is not kept.
	big := seen(0)
	big.Environments = []string{strings.Repeat("x", 17<<10)}
	d.keepDeploy("big", big)
	if _, there := inWalk()["big"]; there {
		t.Fatal("a record over its largest size was kept")
	}
}
