package storesizes

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

var now = time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

func write(t *testing.T, path string, size int) {
	t.Helper()
	put(t, path, strings.Repeat("x", size))
}

func put(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func one(t *testing.T, r Row, m Measure, at time.Time) wire.StoreSize {
	t.Helper()
	page, err := Page([]Row{r}, map[string]Measure{r.ID: m}, wire.StoreSizesQuery{}, at, 2*time.Hour)
	if err != nil || len(page.Stores) != 1 {
		t.Fatalf("%v, %v", page, err)
	}
	return page.Stores[0]
}

// Every row has an id of its own, a limit and a place or a reason, and its place is one that is
// measured.
func TestTheCatalogueIsWhole(t *testing.T) {
	seen := map[string]bool{}
	kinds := map[string]bool{"document": true, "log": true, "time series": true, "graph": true, "blob": true, "key-value": true, "cache": true, "secret": true}
	for _, owner := range []wire.StoreOwner{wire.StoreOwnerMachine, wire.StoreOwnerControlPlane, wire.StoreOwnerClient} {
		rows := Catalogue(owner)
		if len(rows) == 0 {
			t.Errorf("%s has no store", owner)
		}
		for _, r := range rows {
			if seen[r.ID] || r.Scope == "" || !kinds[r.Kind] || r.MaxBytes+r.MaxRecords == 0 {
				t.Errorf("%+v", r)
			}
			seen[r.ID] = true
			if r.At == nil {
				if r.Why == "" && owner != wire.StoreOwnerClient {
					t.Errorf("%s is measured nowhere, with no reason", r.ID)
				}
				continue
			}
			if (len(r.At.Files) == 0 && !r.At.Custom) == (r.At.Prefix == "") {
				t.Errorf("%s has no one place: %+v", r.ID, r.At)
			}
		}
	}
}

// A store at its limit is not over; one past it is, by bytes or by records, and its percent
// is of the limit.
func TestAtAndOverALimit(t *testing.T) {
	dir := t.TempDir()
	row := Row{ID: "machine.x", Owner: wire.StoreOwnerMachine, Scope: "machine", Kind: "blob", MaxBytes: 100, MaxRecords: 2,
		At: &Place{Base: "root", Files: []string{"x/*"}, Count: "files"}}
	write(t, filepath.Join(dir, "x", "a"), 60)
	write(t, filepath.Join(dir, "x", "b"), 40)
	m := row.Files([]string{dir}, Measure{}, now)
	s := one(t, row, m, now)
	if s.State != wire.StoreSizeStateCurrent || *s.Bytes != 100 || *s.Records != 2 || *s.PercentBytes != 100 || *s.PercentRecords != 100 ||
		*s.AverageRecordBytes != 50 || *s.MeasuredAt != wire.DateOf(now) {
		t.Errorf("at its limits: %+v", s)
	}
	write(t, filepath.Join(dir, "x", "b"), 41)
	if s := one(t, row, row.Files([]string{dir}, m, now), now); s.State != wire.StoreSizeStateOver || *s.PercentBytes <= 100 {
		t.Errorf("a byte over: %+v", s)
	}
	write(t, filepath.Join(dir, "x", "b"), 1)
	write(t, filepath.Join(dir, "x", "c"), 1)
	if s := one(t, row, row.Files([]string{dir}, m, now), now); s.State != wire.StoreSizeStateOver || *s.PercentRecords != 150 {
		t.Errorf("a record over: %+v", s)
	}
}

func TestStaleAndUnavailable(t *testing.T) {
	dir := t.TempDir()
	row := Row{ID: "machine.x", Owner: wire.StoreOwnerMachine, MaxRecords: 2, At: &Place{Files: []string{"x"}, Count: "items"}}
	put(t, filepath.Join(dir, "x"), `{"a":1}`)
	m := row.Files([]string{dir}, Measure{}, now)
	if s := one(t, row, m, now.Add(2*time.Hour+time.Minute)); s.State != wire.StoreSizeStateStale || *s.Records != 1 {
		t.Errorf("an old measurement: %+v", s)
	}
	for name, m := range map[string]Measure{
		"no place": row.Files(nil, Measure{}, now),
		"failed":   Unmeasured(now, "The store could not be listed."),
	} {
		if s := one(t, row, m, now); s.State != wire.StoreSizeStateUnavailable || s.Why == nil || s.Bytes != nil || s.MeasuredAt != nil {
			t.Errorf("%s: %+v", name, s)
		}
	}
	page, err := Page([]Row{row, {ID: "client.x", Owner: wire.StoreOwnerClient, MaxBytes: 1}}, nil, wire.StoreSizesQuery{}, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range page.Stores {
		if s.State != wire.StoreSizeStateUnavailable || s.Why == nil {
			t.Errorf("not measured: %+v", s)
		}
	}
	// A file that does not decode has bytes, and its record limit cannot be compared.
	put(t, filepath.Join(dir, "x"), `nonsense`)
	if s := one(t, row, row.Files([]string{dir}, m, now), now); s.State != wire.StoreSizeStateUnavailable || s.Records != nil || *s.Bytes != 8 {
		t.Errorf("not JSON: %+v", s)
	}
	// A store that is not there yet is empty, not unavailable.
	if s := one(t, row, row.Files([]string{t.TempDir()}, m, now), now); s.State != wire.StoreSizeStateCurrent || *s.Bytes != 0 || *s.Records != 0 {
		t.Errorf("no file: %+v", s)
	}
}

// A limit for each part is compared with the largest part, and the whole is what is measured.
func TestALimitForEachPart(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "agents", "A", "transcript.jsonl"), 90)
	write(t, filepath.Join(dir, "agents", "B", "transcript.jsonl"), 80)
	row := Row{ID: "x", MaxBytes: 100, LimitPer: "session", At: &Place{Files: []string{"agents/*/transcript.jsonl"}, Part: "file"}}
	s := one(t, row, row.Files([]string{dir}, Measure{}, now), now)
	if s.State != wire.StoreSizeStateCurrent || *s.Bytes != 170 || *s.PercentBytes != 90 || s.Records != nil || *s.LimitPer != "session" {
		t.Errorf("%+v", s)
	}
	put(t, filepath.Join(dir, "f.json"), `{"v":1,"a":{"x":1,"y":2},"b":{"z":1},"gone":{"q":1}}`)
	fields := Row{ID: "x", MaxRecords: 1, LimitPer: "answer", At: &Place{Files: []string{"f.json"}, Count: "fields:a,b,c"}}
	if s := one(t, fields, fields.Files([]string{dir}, Measure{}, now), now); s.State != wire.StoreSizeStateOver || *s.Records != 3 || *s.PercentRecords != 200 {
		t.Errorf("%+v", s)
	}
}

// The entries of a file that did not change are not counted again, a link is not followed, and
// a folder's own name is no glob.
func TestFilesReadsNoMoreThanItNeeds(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a[b]")
	put(t, filepath.Join(dir, "x.json"), `[1,2]`)
	row := Row{ID: "x", MaxRecords: 9, At: &Place{Files: []string{"x.json"}, Count: "items"}}
	m := row.Files([]string{dir}, Measure{}, now)
	if m.Records == nil || *m.Records != 2 {
		t.Fatalf("%+v", m)
	}
	seven := int64(7)
	m.Records = &seven
	if again := row.Files([]string{dir}, m, now); *again.Records != 7 {
		t.Errorf("read again: %+v", again)
	}
	put(t, filepath.Join(dir, "x.json"), `[1,2,3]`)
	if again := row.Files([]string{dir}, m, now); *again.Records != 3 {
		t.Errorf("a changed file: %+v", again)
	}
	write(t, filepath.Join(dir, "big"), 500)
	if err := os.MkdirAll(filepath.Join(dir, "box"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "big"), filepath.Join(dir, "box", "link")); err != nil {
		t.Fatal(err)
	}
	box := Row{ID: "x", MaxBytes: 9, At: &Place{Files: []string{"box"}, Count: "files"}}
	if m := box.Files([]string{dir}, Measure{}, now); *m.Bytes != 0 || *m.Records != 0 {
		t.Errorf("a link was followed: %+v", m)
	}
}

func TestPages(t *testing.T) {
	rows := []Row{{ID: "c"}, {ID: "a"}, {ID: "b"}}
	two := int64(2)
	first, err := Page(rows, nil, wire.StoreSizesQuery{Limit: &two}, now, time.Hour)
	if err != nil || len(first.Stores) != 2 || first.Stores[0].ID != "a" || first.Total != 3 || first.Cursor == nil {
		t.Fatalf("%+v, %v", first, err)
	}
	last, err := Page(rows, nil, wire.StoreSizesQuery{Limit: &two, Cursor: first.Cursor}, now, time.Hour)
	if err != nil || len(last.Stores) != 1 || last.Stores[0].ID != "c" || last.Cursor != nil {
		t.Fatalf("%+v, %v", last, err)
	}
	for _, n := range []int64{0, -1, MostPage + 1} {
		if _, err := Page(rows, nil, wire.StoreSizesQuery{Limit: &n}, now, time.Hour); err == nil {
			t.Errorf("a page of %d", n)
		}
	}
}

// sealed is a store whose objects cannot be read: a measurement reads none.
type sealed struct {
	store.Store
	t *testing.T
}

func (s sealed) Get(context.Context, string) (*store.Object, error) {
	s.t.Error("a measurement read an object")
	return nil, nil
}

func TestKeys(t *testing.T) {
	ctx := context.Background()
	for name, s := range map[string]store.Store{"memory": store.NewMemory(), "folder": store.NewFolder(t.TempDir())} {
		for key, size := range map[string]int{
			"v1/one.json": 10, "v1/one.json.other": 99, "v1/x/a.json": 10, "v1/x/b.json": 20, "v1/x/b.secret.json": 5,
			"v1/bus/p/log/1": 10, "v1/bus/p/log/2": 10, "v1/bus/p/ids/1": 99, "v1/bus/q/log/1": 10,
		} {
			if _, err := s.Put(ctx, key, make([]byte, size), store.Always); err != nil {
				t.Fatal(err)
			}
		}
		for _, c := range []struct {
			row            Row
			bytes, records int64
			state          wire.StoreSizeState
		}{
			{Row{ID: "x", MaxBytes: 10, At: &Place{Prefix: "v1/one.json", Count: "files"}}, 10, 1, wire.StoreSizeStateCurrent},
			{Row{ID: "x", MaxRecords: 2, At: &Place{Prefix: "v1/x/", Count: "files", Skip: ".secret.json"}}, 35, 2, wire.StoreSizeStateCurrent},
			{Row{ID: "x", MaxBytes: 20, LimitPer: "person", At: &Place{Prefix: "v1/bus/", Count: "files", Walk: "folder", Sub: "log/", Part: "folder"}}, 30, 3, wire.StoreSizeStateCurrent},
			{Row{ID: "x", MaxBytes: 19, LimitPer: "machine", At: &Place{Prefix: "v1/x/", Count: "files", Part: "object"}}, 35, 3, wire.StoreSizeStateOver},
			{Row{ID: "x", MaxRecords: 1, At: &Place{Prefix: "v1/none/", Count: "files"}}, 0, 0, wire.StoreSizeStateCurrent},
		} {
			got := one(t, c.row, c.row.Keys(ctx, sealed{s, t}, now), now)
			if got.State != c.state || *got.Bytes != c.bytes || *got.Records != c.records {
				t.Errorf("%s: %s: %+v", name, c.row.At.Prefix, got)
			}
		}
	}
}
