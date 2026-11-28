package agentsd

import (
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// until:, for:, match: and end-message: (#6955) are read only for agent: labelled; for: is at
// most 7 days, and until: takes events, not a schedule.
func TestLabelledWorkflowFileReadsItsEnd(t *testing.T) {
	t.Parallel()

	w := parseWorkflow("---\nagent: labelled\non:\n  - machine.disk_low\nuntil:\n  - machine.disk_ok\nmatch: [volume]\nfor: 4h\n"+
		"to:\n  labels: lead\nend-message: Disk space is normal again. Carry on.\n---\nDisk is low.\n", "disk", "/tmp/f")
	if w.Problem != nil || len(w.Until) != 1 || triggerName(w.Until[0]) != "machine.disk_ok" || w.For == nil || *w.For != 4*3600 ||
		strings.Join(w.Match, ",") != "volume" || w.EndMessage == nil || *w.EndMessage != "Disk space is normal again. Carry on." ||
		len(w.UnknownFields) != 0 {
		t.Fatalf("%+v", w)
	}
	if got := broadcastForOf(parseWorkflow("---\nagent: labelled\nto:\n  labels: x\n---\nHi.\n", "p", "/tmp/f")); got != 24*time.Hour {
		t.Fatalf("default for: %s", got)
	}
	for _, c := range []struct{ file, says string }{
		{"---\nagent: labelled\nto:\n  labels: x\nfor: 8d\n---\nHi.\n", "at most 7 days"},
		{"---\nagent: labelled\nto:\n  labels: x\nfor: soon\n---\nHi.\n", "`for:` must be a length of time"},
		{"---\nagent: labelled\nto:\n  labels: x\nuntil:\n  - schedule:\n      at: \":30\"\n---\nHi.\n", "not a schedule"},
		{"---\nagent: labelled\nto:\n  labels: x\nuntil: nothing_here\n---\nHi.\n", "does not know the event"},
		{"---\nagent: labelled\nto:\n  labels: x\nmatch:\n  a: b\n---\nHi.\n", "`match:` must be"},
		{"---\nagent: new\nuntil: machine.disk_ok\n---\nHi.\n", "only for `agent: labelled`"},
		{"---\nagent: new\nend-message: Done.\n---\nHi.\n", "only for `agent: labelled`"},
	} {
		w := parseWorkflow(c.file, "w", "/tmp/f")
		if w.Problem == nil || w.Problem.Unreadable == nil || !strings.Contains(w.Problem.Unreadable.Value, c.says) {
			t.Errorf("%q: %+v %v", c.file, w.Problem, problemWords(w))
		}
	}
}

const diskBroadcastFile = "---\nname: Disk\nagent: labelled\non:\n  - machine.disk_low\nuntil:\n  - machine.disk_ok\nmatch: [volume]\nfor: 4h\n" +
	"to:\n  labels: lane\nend-message: Disk space is normal again. Carry on.\n---\nDisk is low: build nothing big.\n"

// diskSetUp is a daemon with the flag on, the disk workflow approved, and a session labelled
// lane in its folder.
func diskSetUp(t *testing.T, root string) (*Daemon, *conn, string, wire.Agent) {
	t.Helper()
	fakeRuntimes(t)
	d, c := start(t, root)
	dir := folder(t, c, 1)
	writeLabelledWorkflow(t, dir, "disk", diskBroadcastFile)
	approveAll(t, c, store.FolderURL(dir))
	c.call(t, 2, "preferences/set", map[string]any{labelledWorkflowsFlag: true})
	lane := seeded(t, d, dir, func(a *wire.Agent) {
		a.Labels = []wire.SessionLabel{{Value: "lane", Owner: wire.SessionLabelOwnerAgent}}
	})
	return d, c, dir, lane
}

func raiseDisk(d *Daemon, name, volume, level string) {
	details := map[string]string{"volume": volume}
	if level != "" {
		details["level"] = level
	}
	d.raise(wire.EventDraft{Name: name, At: now(), Scope: macScope(), Sentence: volume + " " + name + ".", Details: details})
}

// activeDisk waits for the workflow's active broadcasts to be n, and is them.
func activeDisk(t *testing.T, d *Daemon, dir string, n int) []wire.WorkflowBroadcastStatus {
	t.Helper()
	var got []wire.WorkflowBroadcastStatus
	waitFor(t, "the active broadcasts", func() bool {
		got = d.broadcastStatuses(dir, "disk")
		return len(got) == n
	})
	return got
}

func turnText(d *Daemon, id wire.UUID) string { return d.broadcastTurnText(id) }

// A broadcast stays active until its until: event with the same match: details: two volumes
// are two broadcasts, and an ok on one ends only that one. A session that never read it gets
// nothing at the end; one that read it gets the end message once. A worse level replaces the
// words, and a late starter hears what is active at its first turn, as one prompt.
func TestBroadcastEndsAtItsUntil(t *testing.T) {
	t.Parallel()

	d, _, dir, lane := diskSetUp(t, t.TempDir())
	defer d.Shutdown()

	// Low then ok before the turn: nothing was queued, so nothing is given.
	raiseDisk(d, "machine.disk_low", "/A", "low")
	activeDisk(t, d, dir, 1)
	raiseDisk(d, "machine.disk_ok", "/A", "")
	activeDisk(t, d, dir, 0)
	if got := turnText(d, lane.ID); got != "" {
		t.Fatalf("an ended, unread broadcast was given: %q", got)
	}
	if e := lastEvent(d, "workflow.completed"); e == nil || e.Details["ended_by"] != endedByEvent || e.Details["match"] != "volume=/A" ||
		e.Details["agent"] != "" {
		t.Fatalf("%+v", e)
	}

	// Two volumes: two broadcasts, each with its match key.
	raiseDisk(d, "machine.disk_low", "/A", "low")
	raiseDisk(d, "machine.disk_low", "/B", "low")
	got := activeDisk(t, d, dir, 2)
	keys := map[string]bool{}
	for _, s := range got {
		if s.MatchKey != nil {
			keys[*s.MatchKey] = true
		}
	}
	if !keys["volume=/A"] || !keys["volume=/B"] {
		t.Fatalf("%+v", got)
	}
	// Low, read: both at one turn, as one prompt.
	text := turnText(d, lane.ID)
	if strings.Count(text, "Disk is low: build nothing big.") != 2 {
		t.Fatalf("%q", text)
	}

	// A level rise on /B replaces its words: the reader reads it again; /A stays read.
	raiseDisk(d, "machine.disk_low", "/B", "critical")
	waitFor(t, "the level to rise", func() bool {
		for _, s := range d.broadcastStatuses(dir, "disk") {
			if s.Level != nil && *s.Level == "critical" {
				return true
			}
		}
		return false
	})
	if text := turnText(d, lane.ID); strings.Count(text, "Disk is low: build nothing big.") != 1 || !strings.Contains(text, "/B") {
		t.Fatalf("after the rise: %q", text)
	}

	// A late starter hears both at its first turn.
	late := seeded(t, d, dir, func(a *wire.Agent) {
		a.Labels = []wire.SessionLabel{{Value: "lane", Owner: wire.SessionLabelOwnerAgent}}
	})
	if text := turnText(d, late.ID); strings.Count(text, "Disk is low: build nothing big.") != 2 {
		t.Fatalf("the late starter: %q", text)
	}

	// Ok on /A ends only /A; the readers get its end message once, at their next turn.
	raiseDisk(d, "machine.disk_ok", "/A", "")
	left := activeDisk(t, d, dir, 1)
	if left[0].MatchKey == nil || *left[0].MatchKey != "volume=/B" {
		t.Fatalf("%+v", left)
	}
	for _, id := range []wire.UUID{lane.ID, late.ID} {
		if text := turnText(d, id); strings.Count(text, "Disk space is normal again. Carry on.") != 1 || !strings.Contains(text, "has ended") {
			t.Fatalf("the end message: %q", text)
		}
		if text := turnText(d, id); text != "" {
			t.Fatalf("told twice: %q", text)
		}
	}
}

// Turning the workflow off or archiving it ends its broadcasts, and for: ends one whose time
// is up.
func TestBroadcastEndsWhenTurnedOffArchivedOrDue(t *testing.T) {
	t.Parallel()

	d, c, dir, lane := diskSetUp(t, t.TempDir())
	defer d.Shutdown()
	url := store.FolderURL(dir)

	raiseDisk(d, "machine.disk_low", "/A", "low")
	activeDisk(t, d, dir, 1)
	c.call(t, 3, "workflows/enable", map[string]any{"folder": url, "workflowID": "disk", "enabled": false})
	activeDisk(t, d, dir, 0)
	if e := lastEvent(d, "workflow.completed"); e == nil || e.Details["ended_by"] != endedByOff {
		t.Fatalf("%+v", e)
	}
	c.call(t, 4, "workflows/enable", map[string]any{"folder": url, "workflowID": "disk", "enabled": true})

	raiseDisk(d, "machine.disk_low", "/A", "low")
	activeDisk(t, d, dir, 1)
	c.call(t, 5, "workflows/archive", map[string]any{"folder": url, "workflowID": "disk", "archived": true})
	activeDisk(t, d, dir, 0)
	if e := lastEvent(d, "workflow.completed"); e == nil || e.Details["ended_by"] != endedByArchived {
		t.Fatalf("%+v", e)
	}
	if text := turnText(d, lane.ID); text != "" {
		t.Fatalf("unread, so nothing at the end: %q", text)
	}
	c.call(t, 6, "workflows/archive", map[string]any{"folder": url, "workflowID": "disk", "archived": false})

	// for: is up: endDueBroadcasts ends it, by time.
	raiseDisk(d, "machine.disk_low", "/C", "low")
	activeDisk(t, d, dir, 1)
	past := wire.DateOf(time.Now().Add(-time.Minute))
	d.broadcastMu.Lock()
	_ = d.updateBroadcastsLocked(func(m map[string]broadcast) error {
		for k, b := range m {
			if b.EndedAt == nil {
				b.EndsAt = past
				m[k] = b
			}
		}
		return nil
	})
	d.broadcastMu.Unlock()
	d.endDueBroadcasts()
	if e := lastEvent(d, "workflow.completed"); e == nil || e.Details["ended_by"] != endedByTime {
		t.Fatalf("%+v", e)
	}
}

// After a restart, the disk's state decides: a disk_low broadcast with until: whose volume is
// no longer low ended while the daemon was away, and the first look ends it.
func TestBroadcastEndsAfterARestartThatMissedItsEnd(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	d, _, dir, _ := diskSetUp(t, root)
	raiseDisk(d, "machine.disk_low", "/NoSuchVolume", "low")
	activeDisk(t, d, dir, 1)
	d.Shutdown()

	again, _ := start(t, root)
	defer again.Shutdown()
	if got := again.broadcastStatuses(dir, "disk"); len(got) != 0 {
		t.Fatalf("still active after the restart: %+v", got)
	}
	again.broadcastMu.Lock()
	var by string
	for _, b := range again.broadcastsLocked() {
		if b.EndedBy != nil {
			by = *b.EndedBy
		}
	}
	again.broadcastMu.Unlock()
	if by != endedByState {
		t.Fatalf("ended by %q", by)
	}
}

func problemWords(w wire.Workflow) string {
	if w.Problem == nil || w.Problem.Unreadable == nil {
		return ""
	}
	return w.Problem.Unreadable.Value
}
