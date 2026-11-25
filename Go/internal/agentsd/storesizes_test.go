package agentsd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// askStores is every page of stores/list.
func askStores(t *testing.T, c *conn) map[string]wire.StoreSize {
	t.Helper()
	out := map[string]wire.StoreSize{}
	twenty := int64(20)
	q := wire.StoreSizesQuery{Limit: &twenty}
	for id := 1; ; id++ {
		var page wire.StoreSizes
		if err := json.Unmarshal(c.call(t, id, "stores/list", q), &page); err != nil {
			t.Fatal(err)
		}
		for _, s := range page.Stores {
			if _, twice := out[s.ID]; twice {
				t.Errorf("%s is listed twice", s.ID)
			}
			out[s.ID] = s
		}
		if page.Cursor == nil {
			if int(page.Total) != len(out) {
				t.Errorf("total %d, %d stores", page.Total, len(out))
			}
			return out
		}
		q.Cursor = page.Cursor
	}
}

// stores/list and list_stores (#6906): the last measurement of each declared store, at a limit,
// over it and unavailable, with no path and nothing that is stored.
func TestStoreSizes(t *testing.T) {
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	ask := func() map[string]wire.StoreSize { return askStores(t, c) }
	for id, s := range ask() {
		if s.State != wire.StoreSizeStateUnavailable || s.Why == nil {
			t.Errorf("%s before a measurement: %+v", id, s)
		}
	}

	root := d.Store.Loc.Root
	write := func(name string, data []byte) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("policy-seen.json", make([]byte, 4<<10))
	write("host-user.json", make([]byte, 256<<10+1))
	write("devices.json", []byte(`["secretword","b"]`))
	d.measureStores()
	got := ask()
	if s := got["machine.org-policy-seen"]; s.State != wire.StoreSizeStateCurrent || *s.PercentBytes != 100 {
		t.Errorf("at its limit: %+v", s)
	}
	if s := got["machine.host-user"]; s.State != wire.StoreSizeStateOver || *s.PercentBytes <= 100 {
		t.Errorf("over its limit: %+v", s)
	}
	if s := got["machine.devices"]; s.State != wire.StoreSizeStateCurrent || *s.Records != 2 || *s.MaxRecords != 256 {
		t.Errorf("devices: %+v", s)
	}
	if s := got["machine.workflow-runs"]; s.State != wire.StoreSizeStateCurrent || *s.MaxRecords != workflowRunsMost {
		t.Errorf("a small file, by its Spec: %+v", s)
	}
	all, _ := json.Marshal(got)
	for _, private := range []string{root, "secretword"} {
		if strings.Contains(string(all), private) {
			t.Errorf("the answer holds %q", private)
		}
	}

	// It measures nothing when it is read, and a failed measurement says so.
	write("devices.json", []byte(`{}`))
	if s := ask()["machine.devices"]; *s.Records != 2 {
		t.Errorf("a read measured: %+v", s)
	}
	write("devices.json", []byte(`no`))
	d.measureStores()
	if s := ask()["machine.devices"]; s.State != wire.StoreSizeStateUnavailable || s.Why == nil {
		t.Errorf("a file that does not read: %+v", s)
	}

	text, err := d.listStoresTool("", map[string]json.RawMessage{"limit": json.RawMessage("2")})
	var page struct {
		Stores []struct{ ID, MeasuredAt string }
		Cursor string
	}
	if err != nil || json.Unmarshal([]byte(text), &page) != nil || len(page.Stores) != 2 || page.Cursor == "" {
		t.Fatalf("%s, %v", text, err)
	}
	if _, err := time.Parse(time.RFC3339, page.Stores[0].MeasuredAt); err != nil {
		t.Errorf("the tool's time: %v in %s", err, text)
	}
	if _, err := d.listStoresTool("", map[string]json.RawMessage{"limit": json.RawMessage("101")}); err == nil {
		t.Error("a page of 101")
	}
}
