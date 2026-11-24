package agentsd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// #6730: the state of a workflow that is there keeps its approval and who turned it off at any
// age, and lets go of its past and of the fires it held at 90 days.
func TestALiveWorkflowStateAgesOutItsPastOnly(t *testing.T) {
	t.Parallel()

	now := time.Now()
	day := func(n int) wire.Date { return wire.DateOf(now.Add(-time.Duration(n) * 24 * time.Hour)) }
	state := func(at wire.Date) workflowState {
		return workflowState{ApprovedDigest: strPtr("d"), ApprovedCopy: strPtr("c"), OffBy: strPtr("person"), AlarmHandled: &at,
			LastFiredAt: &at, LastFiredBy: &wire.WorkflowCause{}, DismissedFailure: strPtr("k"),
			LastOutcome: &wire.WorkflowOutcome{Ran: &wire.WorkflowOutcomeRan{At: at}},
			HeldFire:    &heldFire{HeldAt: at}, QueuedFires: []heldFire{{HeldAt: at}, {HeldAt: day(1)}}}
	}
	st := state(day(89))
	if st.ageDetails(&workflowFile{}, now) || st.LastOutcome == nil || st.HeldFire == nil {
		t.Fatalf("89 days: %+v", st)
	}
	st = state(day(91))
	if !st.ageDetails(&workflowFile{}, now) || st.LastOutcome != nil || st.LastFiredAt != nil || st.LastFiredBy != nil ||
		st.DismissedFailure != nil || st.HeldFire != nil || len(st.QueuedFires) != 1 {
		t.Fatalf("91 days: %+v", st)
	}
	if st.ApprovedDigest == nil || st.ApprovedCopy == nil || st.OffBy == nil || st.AlarmHandled == nil {
		t.Fatalf("lost what it needs: %+v", st)
	}
	// A cooldown still on keeps the last run.
	long := (100 * 24 * time.Hour).Seconds()
	st = state(day(91))
	st.ageDetails(&workflowFile{wf: wire.Workflow{Cooldown: &long}}, now)
	if st.LastFiredAt == nil {
		t.Fatal("a cooldown still on lost its start")
	}
}

// A full store drops the one gone longest, and refuses when every workflow is there.
func TestWorkflowStatesAreBounded(t *testing.T) {
	t.Parallel()

	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	dir := t.TempDir()
	d.workflowMu.Lock()
	defer d.workflowMu.Unlock()
	r := d.recordsLocked()
	r.States = nil
	for i := range workflowStatesMost {
		st := workflowState{Folder: store.FolderURL(dir), WorkflowID: fmt.Sprint("w", i)}
		if i < 2 {
			gone := wire.DateOf(time.Now().Add(-time.Duration(i+1) * time.Hour))
			st.GoneSince = &gone
		}
		r.States = append(r.States, st)
	}
	d.stateLocked(dir, "new1", true).OffBy = strPtr("person")
	if d.stateLocked(dir, "w1", false) != nil || d.stateLocked(dir, "w0", false) == nil || d.stateLocked(dir, "new1", false) == nil {
		t.Fatal("not the one gone longest went")
	}
	d.stateLocked(dir, "new2", true)
	if err := d.canRememberLocked("new3", dir); err == nil {
		t.Fatal("a full store was not refused")
	}
	d.stateLocked(dir, "new3", true).OffBy = strPtr("person")
	if len(r.States) != workflowStatesMost || d.stateLocked(dir, "new3", false) != nil {
		t.Fatalf("%d states", len(r.States))
	}
	if err := d.canRememberLocked("w5", dir); err != nil {
		t.Fatalf("one it has: %v", err)
	}

	// Over the largest record: the approval stays, what it held to run goes.
	big := d.stateLocked(dir, "w5", false)
	big.ApprovedDigest = strPtr("d")
	big.OffBy = strPtr("person")
	big.QueuedFires = []heldFire{{}}
	big.DismissedFailure = strPtr(strings.Repeat("x", workflowStateBytes))
	r.boundStates()
	if big.DismissedFailure != nil || big.QueuedFires != nil || big.ApprovedDigest == nil || big.OffBy == nil {
		t.Fatalf("over the largest record: %d queued", len(big.QueuedFires))
	}
}

// The runs of a workflows.json from before they were apart are kept, in workflow-runs.json.
func TestWorkflowRunsAreInAFileOfTheirOwn(t *testing.T) {
	t.Parallel()

	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	d.workflowMu.Lock()
	defer d.workflowMu.Unlock()
	id := store.NewUUID()
	must(t, writeStore(d.workflowsPath(), workflowRecords{States: []workflowState{{Folder: "file:///x/", WorkflowID: "w"}},
		Runs: []workflowRun{{ID: id, WorkflowID: "w", Folder: "file:///x/", StartedAt: now()}}, ApprovalsBegan: d.recordsLocked().ApprovalsBegan}))
	for range 2 { // the old file, then the new ones
		d.workflowRecs = nil
		if r := d.recordsLocked(); len(r.Runs) != 1 || r.Runs[0].ID != id || len(r.States) != 1 {
			t.Fatalf("read: %+v", r)
		}
		d.saveRecordsLocked(false)
		old, _ := os.ReadFile(d.workflowsPath())
		runs, err := d.runsFile.Read()
		if strings.Contains(string(old), `"runs"`) || err != nil || len(runs) != 1 {
			t.Fatalf("saved %s and %v (%v)", old, runs, err)
		}
	}
	// Past 90 days it goes, and past the count the oldest do.
	runs := []workflowRun{{ID: store.NewUUID(), StartedAt: wire.DateOf(time.Now().Add(-store.MaxAge))}}
	for range workflowRunsMost + 1 {
		runs = append(runs, workflowRun{ID: store.NewUUID(), WorkflowID: "w", StartedAt: now()})
	}
	d.workflowRecs.Runs = runs
	d.workflowMu.Unlock()
	d.sweepOldRuns()
	d.workflowMu.Lock()
	saved, err := d.runsFile.Read()
	if n := len(d.workflowRecs.Runs); n != workflowRunsMost || err != nil || len(saved) != n {
		t.Fatalf("%d runs kept, %d saved (%v)", n, len(saved), err)
	}
}

// An approval with no room for its copy approves nothing, as Undo without a copy deletes the file.
func TestNoApprovalWithoutItsCopy(t *testing.T) {
	t.Parallel()

	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	must(t, os.MkdirAll(d.workflowCopies(), 0o700))
	p := filepath.Join(d.workflowCopies(), strings.Repeat("ab", 32))
	must(t, os.WriteFile(p, nil, 0o600))
	must(t, os.Truncate(p, workflowCopiesMost))
	st := workflowState{}
	d.keepCopyLocked(&st, &workflowFile{text: "x", digest: "d"})
	if st.ApprovedDigest != nil {
		t.Fatal("approved with no copy")
	}
	large := filepath.Join(t.TempDir(), "w.md")
	must(t, os.WriteFile(large, make([]byte, workflowFileMost+1), 0o600))
	if _, _, why := readWorkflowFile(large); !strings.Contains(why, "larger than 1 MiB") {
		t.Fatalf("read: %q", why)
	}
}
