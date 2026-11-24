package skillcatalog

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
)

// #6732, #6733: a record past its largest, and one more than the count, are refused, and the
// file is as it was; one already over can be taken out.
func TestTheSidecarIsBounded(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "catalog-skills.json")
	put := func(key, commit string) error {
		return UpdateSidecar(path, func(m map[string]SidecarRecord) { m[key] = SidecarRecord{Commit: commit} })
	}
	must(t, put("personal/a", "c"))
	if err := put("personal/b", strings.Repeat("x", sidecarRecordMost)); !errors.Is(err, store.ErrLimit) {
		t.Fatal(err)
	}
	must(t, UpdateSidecar(path, func(m map[string]SidecarRecord) {
		for n := range sidecarMost - 1 {
			m[fmt.Sprint("personal/", n)] = SidecarRecord{}
		}
	}))
	if err := put("personal/b", "c"); !errors.Is(err, store.ErrLimit) {
		t.Fatal(err)
	}
	must(t, put("personal/a", "d"))
	if s := LoadSidecar(path); len(s.Skills) != sidecarMost || s.Skills["personal/a"].Commit != "d" {
		t.Fatalf("%d", len(s.Skills))
	}
	must(t, os.WriteFile(path, []byte(`{"version":1,"skills":{"personal/a":{"commit":"`+strings.Repeat("x", sidecarRecordMost)+`"}}}`), 0o600))
	must(t, UpdateSidecar(path, func(m map[string]SidecarRecord) { delete(m, "personal/a") }))
}
