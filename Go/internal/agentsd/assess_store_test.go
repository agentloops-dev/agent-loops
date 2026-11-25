package agentsd

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

func touchAt(t *testing.T, path string, size int, at time.Time) {
	t.Helper()
	must(t, os.WriteFile(path, make([]byte, size), 0o600))
	must(t, os.Chtimes(path, at, at))
}

// An assessment ages out 90 days from its start, whenever it was scored; a file of no
// assessment ages out by its own time.
func TestAnOldAssessmentGoes(t *testing.T) {
	t.Parallel()

	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	dir, in, now := d.assessmentsPath(), d.assessments(), time.Now()
	old, due := now.Add(-assessmentMaxAge-time.Minute), now.Add(-assessmentMaxAge+time.Hour)
	for id, at := range map[string]time.Time{"OLD": old, "DUE": due} {
		scope := filepath.Join(dir, "scope-"+id+".txt")
		must(t, in.Put(id, assessment{start: &assessStart{ScopePath: scope}, score: &wire.RuntimeAssessmentScore{RuntimeID: id}}))
		touchAt(t, scope, 1, now)
		must(t, os.Chtimes(filepath.Join(dir, id+assessStartSuffix), at, at))
	}
	for _, name := range []string{"ORPHAN.json", "scope-left.txt", ".cut-off.tmp"} {
		touchAt(t, filepath.Join(dir, name), 1, old)
	}
	touchAt(t, filepath.Join(dir, "NEW.json"), 1, now)

	d.sweepHourly()
	if got := dirNames(t, dir); !slices.Equal(got, []string{"DUE.json", "DUE.start.json", "NEW.json", "scope-DUE.txt"}) {
		t.Fatalf("%v", got)
	}
}

// A start or score over the largest size is refused at the write, one from before goes at
// the sweep, and a scope file is cut and keeps its age.
func TestAnAssessmentsFilesHaveALargestSize(t *testing.T) {
	t.Parallel()

	dir, now := t.TempDir(), time.Now().Truncate(time.Second)
	in := assessmentFiles{dir: dir, mu: &sync.Mutex{}}
	huge := strings.Repeat("x", assessmentMaxBytes)
	if err := in.Put("A", assessment{score: &wire.RuntimeAssessmentScore{RuntimeID: huge}}); !errors.Is(err, store.ErrLimit) {
		t.Fatalf("score: %v", err)
	}
	if err := in.Put("A", assessment{start: &assessStart{Host: huge}}); !errors.Is(err, store.ErrLimit) {
		t.Fatalf("start: %v", err)
	}
	scope := filepath.Join(dir, "scope-a.txt")
	must(t, in.Put("A", assessment{start: &assessStart{ScopePath: scope}}))
	written := now.Add(-time.Hour)
	touchAt(t, scope, assessmentMaxBytes+1, written)
	touchAt(t, filepath.Join(dir, "scope-left.txt"), assessmentMaxBytes+1, written)
	touchAt(t, filepath.Join(dir, "BIG.json"), assessmentMaxBytes+1, written)
	sweepAssessments(in, now)
	if got := dirNames(t, dir); !slices.Equal(got, []string{"A.start.json", "scope-a.txt", "scope-left.txt"}) {
		t.Fatalf("%v", got)
	}
	for _, p := range []string{scope, filepath.Join(dir, "scope-left.txt")} {
		info, err := os.Stat(p)
		must(t, err)
		if info.Size() > assessmentMaxBytes || !info.ModTime().Equal(written) {
			t.Errorf("%s: %d bytes at %v", p, info.Size(), info.ModTime())
		}
	}
	if doc, err := in.Get("A"); err != nil || doc.start == nil || !doc.scopeWritten {
		t.Fatalf("%+v, %v", doc, err)
	}
}

func TestAssessmentDocuments(t *testing.T) {
	t.Parallel()

	in := assessmentFiles{dir: filepath.Join(t.TempDir(), "assessments"), mu: &sync.Mutex{}}
	if page, next, err := in.List("", 2); err != nil || next != "" || len(page) != 0 {
		t.Fatalf("no directory: %v, %q, %v", page, next, err)
	}
	if doc, err := in.Get("A"); err != nil || doc.start != nil || doc.score != nil {
		t.Fatalf("none: %+v, %v", doc, err)
	}
	for _, id := range []string{"A", "B", "C"} {
		must(t, in.Put(id, assessment{start: &assessStart{RuntimeID: id}}))
	}
	if err := in.Put("A", assessment{start: &assessStart{RuntimeID: "other"}}); !errors.Is(err, errAssessmentStarted) {
		t.Fatalf("a second start: %v", err)
	}
	for _, runtime := range []string{"one", "two"} {
		must(t, in.Put("A", assessment{score: &wire.RuntimeAssessmentScore{RuntimeID: runtime}}))
	}
	if doc, err := in.Get("A"); err != nil || doc.start.RuntimeID != "A" || doc.score.RuntimeID != "two" {
		t.Fatalf("%+v, %v", doc, err)
	}
	page, next, err := in.List("", 2)
	if err != nil || next != "B" || len(page) != 2 {
		t.Fatalf("%v, %q, %v", page, next, err)
	}
	if page, next, err = in.List(next, 2); err != nil || next != "" || len(page) != 1 || page[0].id != "C" {
		t.Fatalf("%v, %q, %v", page, next, err)
	}
	must(t, in.Delete("A"))
	must(t, in.Delete("A"))
	if got := dirNames(t, in.dir); !slices.Equal(got, []string{"B.start.json", "C.start.json"}) {
		t.Fatalf("%v", got)
	}
	if err := in.Put("../x", assessment{start: &assessStart{}}); err == nil {
		t.Fatal("an id that is a path was taken")
	}
}
