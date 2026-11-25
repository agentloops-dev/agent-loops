package agentsd

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
)

// #2292: host files that were never removed each have a bound. Each test makes one more
// than the bound and finds the bound.

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, _ := os.ReadDir(dir)
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// A copy of an approved workflow goes once no record names it: an edit approved again
// leaves only the new copy.
func TestOnlyTheNamedWorkflowCopiesAreKept(t *testing.T) {
	t.Parallel()

	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	dir := folder(t, c, 1)
	folder := store.FolderURL(dir)
	must(t, os.MkdirAll(workflowsFolder(dir), 0o755)) //nolint:gosec // G301: the mode this folder always had; only its error is new
	file := filepath.Join(workflowsFolder(dir), "nightly.md")
	stray := strings.Repeat("ab", 32)
	must(t, os.MkdirAll(d.workflowCopies(), 0o755))                                                  //nolint:gosec // G301: the mode this folder always had; only its error is new
	must(t, os.WriteFile(filepath.Join(d.workflowCopies(), stray), []byte("an old version"), 0o644)) //nolint:gosec // G306: the mode this file always had; only its error is new
	var first []string
	for i := range 3 {
		must(t, os.WriteFile(file, fmt.Appendf(nil, "---\non:\n  - schedule:\n      at: \":00\"\n---\nTidy up %d.\n", i), 0o644)) //nolint:gosec // G306: the mode this file always had; only its error is new
		approveAll(t, c, folder)
		copies := dirNames(t, d.workflowCopies())
		if len(copies) != 1 {
			t.Fatalf("version %d: %v, want the one copy", i, copies)
		}
		if i > 0 && copies[0] == first[0] {
			t.Fatalf("version %d kept the copy of version 0", i)
		}
		if i == 0 {
			first = copies
		}
	}
}

// The newest 50 assessments are kept, each with its score and its scope file.
func TestTheNewestAssessmentsAreKept(t *testing.T) {
	t.Parallel()

	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	dir := d.assessmentsPath()
	must(t, os.MkdirAll(dir, 0o755)) //nolint:gosec // G301: the mode this folder always had; only its error is new
	old := time.Now().Add(-time.Hour)
	for i := range assessmentsKept + 1 {
		id := fmt.Sprintf("agent-%02d", i)
		scope := filepath.Join(dir, "scope-"+id+".txt")
		must(t, os.WriteFile(scope, []byte("scope"), 0o644))                       //nolint:gosec // G306: the mode this file always had; only its error is new
		must(t, os.WriteFile(filepath.Join(dir, id+".json"), []byte("{}"), 0o644)) //nolint:gosec // G306: the mode this file always had; only its error is new
		kept := filepath.Join(dir, id+".start.json")
		must(t, writeStore(kept, assessStart{AgentID: "x", ScopePath: scope}))
		// agent-00 is the oldest.
		at := old.Add(time.Duration(i) * time.Second)
		must(t, os.Chtimes(kept, at, at))
	}
	d.sweepHourly()
	got := dirNames(t, dir)
	if len(got) != 3*assessmentsKept {
		t.Fatalf("%d files, want %d", len(got), 3*assessmentsKept)
	}
	for _, gone := range []string{"agent-00.start.json", "agent-00.json", "scope-agent-00.txt"} {
		if slices.Contains(got, gone) {
			t.Errorf("%s was kept", gone)
		}
	}
}

// The sweep of mcp-logs drops what is past the age, and the oldest past the size (#6736).
func TestMCPLogsAreSweptByAgeAndSize(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	now := time.Now()
	put := func(name string, size int, age time.Duration) {
		p := filepath.Join(dir, name)
		must(t, os.WriteFile(p, make([]byte, size), 0o600))
		must(t, os.Chtimes(p, now.Add(-age), now.Add(-age)))
	}
	put("aged.log", 10, 7*24*time.Hour)
	put("aged.log.1", 10, 8*24*time.Hour) // a name from before #6736
	for i := range 4 {
		put(fmt.Sprintf("big-%d.log", i), 2<<20, time.Duration(i)*time.Minute)
	}
	put("small.previous.log", 10, 10*time.Minute)
	dropOldHostedLogs(dir, now)
	got := dirNames(t, dir)
	slices.Sort(got)
	if want := []string{"big-0.log", "big-1.log", "big-2.log", "small.previous.log"}; !slices.Equal(got, want) {
		t.Fatalf("%v, want %v", got, want)
	}
}

// Busy servers do not take mcp-logs past 10 MB between two hourly sweeps.
func TestBusyMCPLogsStayUnderTheCap(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	chunk := make([]byte, 32<<10)
	for server := range 40 {
		l := &hostedLog{path: filepath.Join(dir, fmt.Sprintf("s-%d.log", server))}
		for range 20 {
			l.write(chunk)
		}
	}
	var total int64
	for _, name := range dirNames(t, dir) {
		if fi, err := os.Stat(filepath.Join(dir, name)); err == nil {
			total += fi.Size()
		}
	}
	if total > 10_000_000 {
		t.Fatalf("%d bytes", total)
	}
}

// A folder that is no longer a folder leaves events-state.json's branch tips. On the
// map alone: a started daemon's own folder watch drops "/kept" too, as it has no folder.
func TestBranchTipsOfAGoneFolderGo(t *testing.T) {
	t.Parallel()

	tips := map[string]map[string]string{"/kept": {"main": "a"}, "/gone": {"main": "b"}}
	if !dropGone(tips, map[string]bool{"/kept": true}) {
		t.Fatal("nothing went")
	}
	if _, ok := tips["/gone"]; ok || len(tips) != 1 {
		t.Fatalf("%v", tips)
	}
	if dropGone(tips, map[string]bool{"/kept": true}) {
		t.Fatal("a second drop said one went")
	}
}
