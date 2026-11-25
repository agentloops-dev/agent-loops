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
	t.Skip("#7188")
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
