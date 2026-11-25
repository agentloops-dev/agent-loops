package skillcatalog

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
)

// #6908: a record at its largest is taken, and one a byte over it is refused with the file as it was.
func TestRecordLimitOfTheSidecar(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "catalog-skills.json")
	put := func(n int) error {
		return UpdateSidecar(path, func(m map[string]SidecarRecord) { m["k"] = SidecarRecord{Commit: strings.Repeat("x", n)} })
	}
	n := sidecarRecordMost
	for put(n) != nil {
		n--
	}
	if n < sidecarRecordMost-500 {
		t.Fatalf("the longest commit is %d", n)
	}
	before, err := os.ReadFile(path)
	must(t, err)
	if err := put(n + 1); !errors.Is(err, store.ErrLimit) || !strings.Contains(err.Error(), "8192") {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Fatal("a refused write changed the file")
	}
}

// The sweep marks and drops an item that went, with its record at the largest size.
func TestTheSweepsMarkFitsARecordAtItsLargest(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "catalog-skills.json")
	n := sidecarRecordMost
	for UpdateSidecar(path, func(m map[string]SidecarRecord) { m["k"] = SidecarRecord{Commit: strings.Repeat("x", n)} }) != nil {
		n--
	}
	if err := UpdateSidecar(path, func(m map[string]SidecarRecord) {
		for k, r := range m {
			at := time.Now().UTC().Truncate(time.Second)
			r.GoneSince = &at
			m[k] = r
		}
	}); err != nil {
		t.Fatalf("the sweep of every item is refused: %v", err)
	}
}

// A record over its largest from before there was one is marked too, and only marked; the mark
// makes no room for a longer record.
func TestARecordOverItsLargestIsOnlyMarked(t *testing.T) {
	t.Parallel()
	at := time.Now().UTC().Truncate(time.Second)
	records := map[string]SidecarRecord{"k": {Commit: strings.Repeat("x", 2*sidecarRecordMost)}, "small": {}}
	for _, since := range []*time.Time{&at, nil} {
		changed, err := ChangeRecords(records, func(m map[string]SidecarRecord) {
			for k, r := range m {
				r.GoneSince = since
				m[k] = r
			}
		})
		if err != nil || !changed {
			t.Fatalf("%v: %v", changed, err)
		}
	}
	if _, err := ChangeRecords(records, func(m map[string]SidecarRecord) {
		m["k"] = SidecarRecord{Commit: strings.Repeat("y", 2*sidecarRecordMost), GoneSince: &at}
	}); !errors.Is(err, store.ErrLimit) {
		t.Fatalf("a changed record over its largest: %v", err)
	}
}
