package agentsd

import (
	"cmp"
	"fmt"
	"log"
	"path/filepath"
	"slices"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// Autopilot's setting and its status (#5898, epic #5897). The setting is Folder.autopilot in the
// folder's record: off, high or on, absent is off. The status is what Autopilot did in the folder
// and why each item waits: at most autopilotMostRows rows, kept in autopilot-status.json, one
// entry for each folder. The loop (autopilotrun.go, #5899) calls autopilotNote.
// With the mode off the status has no rows.

const (
	autopilotMostRows  = 100
	autopilotRefBytes  = 200
	autopilotWhyBytes  = 200
	autopilotSetByLeft = "this machine"
	autopilotPaused    = "autopilot.paused"
	autopilotResumed   = "autopilot.resumed"
	autopilotStarted   = "autopilot.started"
)

// autopilotRecord is one folder's status as kept.
type autopilotRecord struct {
	LastRunAt *wire.Date          `json:"lastRunAt,omitzero"`
	Rows      []wire.AutopilotRow `json:"rows"`
	// Restarts is when Autopilot started a lead again, by epic ref, for the last day (#5901).
	Restarts map[string][]wire.Date `json:"restarts,omitzero"`
	Updated  wire.Date              `json:"updated"`
}

func (d *Daemon) autopilotPath() string {
	return filepath.Join(d.Store.Loc.Root, "autopilot-status.json")
}

// openAutopilot opens the status file, which evicts: it is a record of what the loop did, and the
// loop makes it again. An entry is a folder's whole status, at most 100 rows.
func (d *Daemon) openAutopilot() *store.JSONFile[autopilotRecord] {
	return smallFile(d, store.Spec{Name: "autopilot-status.json", Kind: "key-value", Scope: "machine",
		Bounds: store.Bounds{MaxRecordBytes: 64 << 10, MaxCount: 1000, MaxAge: store.MaxAge, Action: store.EvictOldest}},
		d.autopilotPath, func(r *autopilotRecord) *wire.Date { return &r.Updated })
}

// autopilotModeOf is the folder's mode: absent is off.
func autopilotModeOf(p wire.Folder) wire.AutopilotMode {
	if p.Autopilot == nil {
		return wire.AutopilotModeOff
	}
	return p.Autopilot.Mode
}

// autopilotStatus is what Autopilot did in the folder, the newest row first.
func (d *Daemon) autopilotStatus(r wire.AutopilotRequest) (wire.AutopilotStatus, error) {
	folder := store.FolderPath(r.Folder)
	if _, ok := d.summaryFor(folder); !ok {
		return wire.AutopilotStatus{}, noSuchFolder(folder + " is not a folder.")
	}
	p, i, _ := d.folderRecord(folder)
	st := wire.AutopilotStatus{Mode: wire.AutopilotModeOff, Rows: []wire.AutopilotRow{}}
	if i >= 0 {
		st.Mode = autopilotModeOf(p)
	}
	if st.Mode == wire.AutopilotModeOff {
		return st, nil
	}
	entries, err := d.autopilotFile.Read()
	if err != nil {
		return wire.AutopilotStatus{}, &Error{Code: CodeInternal, Message: "Autopilot's status could not be read: " + err.Error()}
	}
	if e, ok := entries[folder]; ok {
		st.LastRunAt = e.LastRunAt
		st.Rows = append(st.Rows, e.Rows...)
	}
	return st, nil
}

func autopilotSetBy(c *Client) string {
	if c != nil {
		if a := c.actor(); a != nil {
			if a.UserName != nil && *a.UserName != "" {
				return *a.UserName
			}
			if a.User != "" {
				return a.User
			}
		}
	}
	return autopilotSetByLeft
}

// setAutopilot chooses what Autopilot starts in a folder. Off is no key. On, or high, for one
// folder of a project turns it off for the project's other folders on this machine.
func (d *Daemon) setAutopilot(c *Client, r wire.AutopilotSetRequest) (wire.AutopilotStatus, error) {
	folder := store.FolderPath(r.Folder)
	if _, ok := d.summaryFor(folder); !ok {
		return wire.AutopilotStatus{}, noSuchFolder(folder + " is not a folder.")
	}
	switch r.Mode {
	case wire.AutopilotModeOff, wire.AutopilotModeHigh, wire.AutopilotModeOn:
	default:
		return wire.AutopilotStatus{}, Invalid("Autopilot is off, high or on, not %q.", r.Mode)
	}
	var others []string
	if r.Mode != wire.AutopilotModeOff {
		others = d.autopilotSiblings(folder)
	}
	setBy, at := autopilotSetBy(c), now()
	if err := d.editRecord(folder, func(p *wire.Folder) {
		if r.Mode == wire.AutopilotModeOff {
			p.Autopilot = nil
		} else {
			p.Autopilot = &wire.FolderAutopilot{Mode: r.Mode, SetBy: cutJSON(setBy, 200), SetAt: at}
		}
	}); err != nil {
		return wire.AutopilotStatus{}, err
	}
	if r.Mode == wire.AutopilotModeOff {
		d.autopilotForget(folder)
	}
	for _, o := range others {
		if err := d.editRecord(o, func(p *wire.Folder) { p.Autopilot = nil }); err != nil {
			log.Printf("autopilot: %s could not be turned off: %v", o, err)
			continue
		}
		d.autopilotForget(o)
		if _, err := d.sendFolder(o); err != nil {
			log.Printf("autopilot: %s: %v", o, err)
		}
	}
	if _, err := d.sendFolder(folder); err != nil {
		return wire.AutopilotStatus{}, err
	}
	return d.autopilotStatus(wire.AutopilotRequest{Folder: r.Folder})
}

// autopilotSiblings is the other folders of the folder's project on this machine that have
// Autopilot on.
func (d *Daemon) autopilotSiblings(folder string) []string {
	id, _, _ := d.folderProject(folder)
	if id == "" {
		return nil
	}
	var out []string
	for _, p := range d.Store.Folders() {
		f := store.FolderPath(p.Folder)
		if f == folder || p.Autopilot == nil || p.Autopilot.Mode == wire.AutopilotModeOff {
			continue
		}
		if other, _, _ := d.folderProject(f); other == id {
			out = append(out, f)
		}
	}
	slices.Sort(out)
	return out
}

// resumeAutopilot drops the paused row of ref in the folder, raises autopilot.resumed with the
// reason that was cleared, and wakes the loop. A ref that is not paused is left as it is.
func (d *Daemon) resumeAutopilot(r wire.AutopilotResumeRequest) (wire.AutopilotStatus, error) {
	folder := store.FolderPath(r.Folder)
	if _, ok := d.summaryFor(folder); !ok {
		return wire.AutopilotStatus{}, noSuchFolder(folder + " is not a folder.")
	}
	var cleared *wire.AutopilotRow
	err := d.autopilotFile.Update(func(m map[string]autopilotRecord) error {
		e, ok := m[folder]
		if !ok {
			return nil
		}
		rows := make([]wire.AutopilotRow, 0, len(e.Rows))
		for _, row := range e.Rows {
			if row.Ref == r.Ref && row.State == wire.AutopilotRowStatePaused {
				row := row
				cleared = &row
				continue
			}
			rows = append(rows, row)
		}
		e.Rows, e.Updated = rows, now()
		m[folder] = e
		return nil
	})
	if err != nil {
		return wire.AutopilotStatus{}, &Error{Code: CodeInternal, Message: "Autopilot's status could not be written: " + err.Error()}
	}
	if cleared != nil {
		d.raise(autopilotDraft(autopilotResumed, folder, "resumed", cleared.Ref, map[string]string{"ref": cleared.Ref, "reason": cleared.Reason}, now()))
		d.autopilotHear(wire.Event{Name: "forge.changed"})
	}
	return d.autopilotStatus(wire.AutopilotRequest{Folder: r.Folder})
}

// autopilotForget drops the folder's rows.
func (d *Daemon) autopilotForget(folder string) {
	if err := d.autopilotFile.Update(func(m map[string]autopilotRecord) error {
		delete(m, folder)
		return nil
	}); err != nil {
		log.Printf("autopilot: %s: status not cleared: %v", folder, err)
	}
}

// autopilotNote records one row of the folder's status, as the loop (#5899) finds it: a row is by
// its ref, so a new one for a ref replaces the old. The row's time, since, stays while its state
// and reason stay. It raises autopilot.started when the row starts a session, autopilot.paused
// when it becomes paused and autopilot.resumed when it stops being paused. The status keeps the
// newest 100 rows.
func (d *Daemon) autopilotNote(folder string, row wire.AutopilotRow) error {
	row.Ref = cutJSON(row.Ref, autopilotRefBytes)
	row.Reason = cutJSON(row.Reason, autopilotWhyBytes)
	at := now()
	var was *wire.AutopilotRow
	err := d.autopilotFile.Update(func(m map[string]autopilotRecord) error {
		e := m[folder]
		rows := make([]wire.AutopilotRow, 0, len(e.Rows)+1)
		for _, r := range e.Rows {
			if r.Ref == row.Ref {
				r := r
				was = &r
				continue
			}
			rows = append(rows, r)
		}
		if was != nil && was.State == row.State && was.Reason == row.Reason {
			row.Since = was.Since
		} else {
			row.Since = at
		}
		rows = append(rows, row)
		slices.SortStableFunc(rows, func(a, b wire.AutopilotRow) int { return cmp.Compare(b.Since, a.Since) })
		if len(rows) > autopilotMostRows {
			rows = rows[:autopilotMostRows]
		}
		m[folder] = autopilotRecord{LastRunAt: &at, Rows: rows, Restarts: e.Restarts, Updated: at}
		return nil
	})
	if err != nil {
		return err
	}
	switch {
	case row.State == wire.AutopilotRowStateStarted && (was == nil || was.State != wire.AutopilotRowStateStarted):
		session := ""
		if row.Session != nil {
			session = string(*row.Session)
		}
		d.raise(autopilotDraft(autopilotStarted, folder, "started", row.Ref, map[string]string{"ref": row.Ref, "session": session}, at))
	case row.State == wire.AutopilotRowStatePaused && (was == nil || was.State != wire.AutopilotRowStatePaused):
		d.raise(autopilotDraft(autopilotPaused, folder, "paused: "+row.Reason, row.Ref, map[string]string{"ref": row.Ref, "reason": row.Reason}, at))
	case was != nil && was.State == wire.AutopilotRowStatePaused && row.State != wire.AutopilotRowStatePaused:
		d.raise(autopilotDraft(autopilotResumed, folder, "resumed", row.Ref, map[string]string{"ref": row.Ref, "reason": was.Reason}, at))
	}
	return nil
}

func autopilotDraft(name, folder, what, ref string, details map[string]string, at wire.Date) wire.EventDraft {
	return wire.EventDraft{Name: name, At: at, Scope: folderScope(folder), Details: details,
		Sentence: fmt.Sprintf("Autopilot %s for %s.", what, ref)}
}
