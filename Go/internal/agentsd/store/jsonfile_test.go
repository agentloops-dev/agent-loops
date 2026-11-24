package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

type jsonEntry struct {
	Text string    `json:"text"`
	At   wire.Date `json:"at"`
}

// testJSONFile is a store of at most 2 entries of 100 bytes for a day, its file and its clock.
func testJSONFile(t *testing.T, action Action) (*JSONFile[jsonEntry], string, *time.Time) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "x.json")
	now := time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC)
	f := OpenJSONFile(Spec{Name: "x.json", Kind: "document", Scope: "person",
		Bounds: Bounds{MaxRecordBytes: 100, MaxCount: 2, MaxAge: 24 * time.Hour, Action: action}},
		func() string { return file }, func(e *jsonEntry) *wire.Date { return &e.At }, func() time.Time { return now })
	return f, file, &now
}

func putEntry(f *JSONFile[jsonEntry], key, text string) error {
	return f.Update(func(m map[string]jsonEntry) error { m[key] = jsonEntry{Text: text}; return nil })
}

func entryCount(t *testing.T, f *JSONFile[jsonEntry]) int {
	t.Helper()
	m, err := f.Read()
	if err != nil {
		t.Fatal(err)
	}
	return len(m)
}

func TestJSONFileEvictsTheOldest(t *testing.T) {
	t.Parallel()
	f, _, now := testJSONFile(t, EvictOldest)
	for n := range 4 {
		if err := putEntry(f, fmt.Sprint(n), "a"); err != nil {
			t.Fatal(err)
		}
		*now = now.Add(time.Minute)
	}
	if n := entryCount(t, f); n != 4 {
		t.Fatalf("%d entries before the sweep", n)
	}
	if n, err := f.Sweep(); n != 2 || err != nil {
		t.Fatal(n, err)
	}
	m, _ := f.Read()
	if len(m) != 2 || m["2"].Text != "a" || m["3"].Text != "a" {
		t.Fatalf("kept %v", m)
	}
	// Past twice the count, the write sweeps.
	for n := range 3 {
		if err := putEntry(f, fmt.Sprint("b", n), "a"); err != nil {
			t.Fatal(err)
		}
	}
	if m, _ := f.Read(); len(m) != 2 || m["b2"].Text != "a" {
		t.Fatalf("%v past the high-water mark", m)
	}
}

func TestJSONFileAgesOut(t *testing.T) {
	t.Parallel()
	for _, action := range []Action{EvictOldest, Refuse} {
		f, file, now := testJSONFile(t, action)
		// An entry from before the store had limits, with no time, is dated by the first sweep.
		if err := os.WriteFile(file, []byte(`{"old": {"text": "a"}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if n, err := f.Sweep(); n != 0 || err != nil {
			t.Fatal(n, err)
		}
		*now = now.Add(23 * time.Hour)
		if err := putEntry(f, "new", "a"); err != nil {
			t.Fatal(err)
		}
		*now = now.Add(time.Hour)
		if n, err := f.Sweep(); n != 1 || err != nil {
			t.Fatal(n, err)
		}
		*now = now.Add(24 * time.Hour)
		if err := putEntry(f, "newer", "a"); err != nil || entryCount(t, f) != 1 {
			t.Fatalf("a write kept an old entry: %v", err)
		}
	}
}

func TestJSONFileRefuses(t *testing.T) {
	t.Parallel()
	f, _, _ := testJSONFile(t, Refuse)
	long := string(make([]byte, 100))
	if err := putEntry(f, "long", long); !errors.Is(err, ErrLimit) {
		t.Fatalf("a long entry: %v", err)
	}
	for n := range 2 {
		if err := putEntry(f, fmt.Sprint(n), "a"); err != nil {
			t.Fatal(err)
		}
	}
	if err := putEntry(f, "2", "a"); !errors.Is(err, ErrLimit) {
		t.Fatalf("a third entry: %v", err)
	}
	if err := putEntry(f, "1", "b"); err != nil {
		t.Fatalf("a change when full: %v", err)
	}
	if n := entryCount(t, f); n != 2 {
		t.Fatalf("%d entries", n)
	}
}

// A long entry from before the store had limits does not block other writes, and the sweep drops it.
func TestJSONFileOldLongEntry(t *testing.T) {
	t.Parallel()
	f, file, _ := testJSONFile(t, EvictOldest)
	long := fmt.Sprintf(`{"long": {"text": "%0200d"}}`, 0)
	if err := os.WriteFile(file, []byte(long), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := putEntry(f, "a", "a"); err != nil {
		t.Fatal(err)
	}
	if n, err := f.Sweep(); n != 1 || err != nil {
		t.Fatal(n, err)
	}
}

func TestJSONFileSetsAsideWhatItCannotRead(t *testing.T) {
	t.Parallel()
	f, file, _ := testJSONFile(t, EvictOldest)
	if err := os.WriteFile(file, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Read(); err == nil {
		t.Fatal("no error")
	}
	if _, err := f.Sweep(); err == nil {
		t.Fatal("swept")
	}
	if err := putEntry(f, "a", "a"); err != nil || entryCount(t, f) != 1 {
		t.Fatal(err)
	}
	if aside, _ := filepath.Glob(file + ".corrupt-*"); len(aside) != 1 {
		t.Fatalf("set aside %v", aside)
	}
}

func TestJSONFileNeedsItsLimits(t *testing.T) {
	t.Parallel()
	good := Spec{Name: "x", Kind: "document", Scope: "person", Bounds: Bounds{MaxRecordBytes: 1, MaxCount: 1, MaxAge: MaxAge, Action: Refuse}}
	if err := good.check(); err != nil {
		t.Fatal(err)
	}
	for n, spoil := range []func(*Spec){
		func(s *Spec) { s.Name = "" }, func(s *Spec) { s.Kind = "" }, func(s *Spec) { s.Scope = "" },
		func(s *Spec) { s.Bounds.MaxRecordBytes = 0 }, func(s *Spec) { s.Bounds.MaxCount = 0 },
		func(s *Spec) { s.Bounds.MaxAge = 0 }, func(s *Spec) { s.Bounds.MaxAge++ }, func(s *Spec) { s.Bounds.Action = "" },
	} {
		s := good
		spoil(&s)
		if s.check() == nil {
			t.Errorf("%d: a spec with no limit passed", n)
		}
	}
}
