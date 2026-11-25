package agentsd

import (
	"fmt"
	"log"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/diskjson"
	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// The bounds of the workflow stores (#6730). A state is what the host remembers of a workflow
// that exists, so its approval, its on or off and what else it needs to run as the person said
// stay until the workflow goes; what it tells of the past ages out. The states have no age of
// their own, so they are not a store.JSONFile. The runs are one, workflow-runs.json.

const (
	workflowStatesMost   = 2000
	workflowStateBytes   = 64 << 10
	workflowFileMost     = 1 << 20 // a workflow or a skill it names
	workflowSkillsMost   = 50      // the skills one workflow names
	workflowCopiesMost   = 64 << 20
	workflowRunsMost     = 1000
	workflowRunBytes     = 64 << 10
	workflowRanByMost    = 1000
	workflowDetailsKept  = store.MaxAge
	workflowStatesFull   = "This machine remembers as many workflows as it can. Delete some first."
	workflowCopiesNoRoom = "There is no room for a copy of it to approve. Delete some workflows first."
)

// openWorkflowRuns opens workflow-runs.json. It evicts: a run let go of ends all the same.
func (d *Daemon) openWorkflowRuns() *store.JSONFile[workflowRun] {
	return smallFile(d, store.Spec{Name: "workflow-runs.json", Kind: "log", Scope: "machine",
		Bounds: store.Bounds{MaxRecordBytes: workflowRunBytes, MaxCount: workflowRunsMost, MaxAge: store.MaxAge, Action: store.EvictOldest}},
		d.workflowRunsPath, func(r *workflowRun) *wire.Date { return &r.StartedAt })
}

// loadRunsLocked adds the runs of workflow-runs.json to those that a workflows.json from before
// the two were apart has.
func (d *Daemon) loadRunsLocked(r *workflowRecords) {
	runs, err := d.runsFile.Read()
	if err != nil {
		log.Printf("workflow-runs.json: could not read: %v", err)
	}
	for _, run := range r.Runs {
		delete(runs, string(run.ID))
	}
	for _, run := range runs {
		r.Runs = append(r.Runs, run)
	}
	slices.SortFunc(r.Runs, compareRuns)
}

func compareRuns(a, b workflowRun) int {
	if a.StartedAt != b.StartedAt {
		if a.StartedAt < b.StartedAt {
			return -1
		}
		return 1
	}
	return strings.Compare(string(a.ID), string(b.ID))
}

// saveRunsLocked writes workflow-runs.json. A run too large for it is kept in memory only.
func (d *Daemon) saveRunsLocked(runs []workflowRun) error {
	return d.runsFile.Update(func(entries map[string]workflowRun) error {
		clear(entries)
		for _, run := range runs {
			key := string(run.ID)
			if b, err := diskjson.Marshal(run); err != nil || len(key)+len(b) > workflowRunBytes {
				log.Printf("workflow-runs.json: the %s run is not kept: it is too large", run.WorkflowID)
				continue
			}
			entries[key] = run
		}
		return nil
	})
}

// boundStates takes what a state over its largest size tells of the past and holds to
// run: what is left (the approval, on or off) is always under it.
func (r *workflowRecords) boundStates() {
	for i := range r.States {
		st := &r.States[i]
		if b, err := diskjson.Marshal(st); err != nil || len(b) <= workflowStateBytes {
			continue
		}
		log.Printf("workflows: let go of what %s held to run and when it last ran: its record is over %d bytes", st.WorkflowID, workflowStateBytes)
		st.forgetLastRun()
		st.HeldFire, st.QueuedFires = nil, nil
	}
}

func (st *workflowState) forgetLastRun() {
	st.LastFiredAt, st.LastFiredBy, st.LastOutcome, st.LastCausingEvent, st.DismissedFailure = nil, nil, nil, nil, nil
}

// ageDetails drops what is 90 days old in the state of a workflow that is still there: when
// and how it last ran, and the fires it held. A cooldown still on keeps the last run.
func (st *workflowState) ageDetails(f *workflowFile, now time.Time) bool {
	old := func(at wire.Date) bool { return now.Sub(at.Time()) >= workflowDetailsKept }
	changed := false
	if st.HeldFire != nil && old(st.HeldFire.HeldAt) {
		st.HeldFire, changed = nil, true
	}
	if n := len(st.QueuedFires); n > 0 {
		st.QueuedFires = slices.DeleteFunc(st.QueuedFires, func(q heldFire) bool { return old(q.HeldAt) })
		changed = changed || len(st.QueuedFires) != n
	}
	if o := st.LastOutcome; o != nil && (o.Ran != nil && old(o.Ran.At) || o.Refused != nil && old(o.Refused.At)) {
		st.LastOutcome, st.LastCausingEvent, st.DismissedFailure, changed = nil, nil, nil, true
	}
	if at := st.LastFiredAt; at != nil && old(*at) {
		cooling := f != nil && f.wf.Cooldown != nil && now.Sub(at.Time()).Seconds() < *f.wf.Cooldown
		if !cooling {
			st.LastFiredAt, st.LastFiredBy, changed = nil, nil, true
		}
	}
	return changed
}

// stateRoomLocked says whether there is room for n more states, and makes it from those whose
// workflows went first. Those of workflows that are there are the person's: never evicted.
func (d *Daemon) stateRoomLocked(n int) bool {
	r := d.recordsLocked()
	r.evictGone(n)
	return len(r.States)+n <= workflowStatesMost
}

// evictGone drops the states whose workflows went first, to take n more under the limit.
func (r *workflowRecords) evictGone(n int) bool {
	over := len(r.States) + n - workflowStatesMost
	if over <= 0 {
		return false
	}
	var gone []int
	for i, st := range r.States {
		if st.GoneSince != nil {
			gone = append(gone, i)
		}
	}
	over = min(over, len(gone))
	slices.SortStableFunc(gone, func(a, b int) int {
		return r.States[a].GoneSince.Time().Compare(r.States[b].GoneSince.Time())
	})
	drop := map[int]bool{}
	for _, i := range gone[:over] {
		drop[i] = true
	}
	// A new slice: a caller may hold a pointer into the old one.
	kept := make([]workflowState, 0, len(r.States)-over+n)
	for i, st := range r.States {
		if !drop[i] {
			kept = append(kept, st)
		}
	}
	r.States = kept
	return over > 0
}

// canRememberLocked refuses what a person or an agent sets on a workflow that has no state and
// no room for one, in any of these folders.
func (d *Daemon) canRememberLocked(id string, folders ...string) error {
	n := 0
	for _, folder := range folders {
		if d.stateLocked(folder, id, false) == nil {
			n++
		}
	}
	if n > 0 && !d.stateRoomLocked(n) {
		return &Error{Code: wire.FailureCouldNotSave, Message: workflowStatesFull}
	}
	return nil
}

// copiesFit says whether workflow-copies has room for the approved text of f and its skills.
func (d *Daemon) copiesFit(f *workflowFile) bool {
	total := int64(len(f.text))
	for _, s := range f.skills {
		total += int64(len(s.text))
	}
	entries, _ := os.ReadDir(d.workflowCopies()) // none yet: all the room
	for _, e := range entries {
		if fi, err := e.Info(); err == nil {
			total += fi.Size()
		}
	}
	return total <= workflowCopiesMost
}

// sweepRanByLocked forgets who ran the sessions that are gone or archived.
func (d *Daemon) sweepRanByLocked() bool {
	rec := d.recordsLocked()
	n := len(rec.RanBy)
	for id := range rec.RanBy {
		if a := d.Store.Agent(id); a == nil || a.State == wire.AgentStateArchived {
			delete(rec.RanBy, id)
		}
	}
	return len(rec.RanBy) != n
}

// tooLargeToRun is why a workflow's file is not read.
func tooLargeToRun(size int64) string {
	if size <= workflowFileMost {
		return ""
	}
	return fmt.Sprintf("This file is larger than %d MiB", workflowFileMost>>20)
}
