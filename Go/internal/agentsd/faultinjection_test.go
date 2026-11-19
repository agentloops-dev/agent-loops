package agentsd

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/diskjson"
	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// The agent store is the one record of each agent. This test kills agentsd at each write of a
// scripted session (#6184, epic #6183): a root copied just before a write is what a kill at
// that write leaves. It starts a daemon on each copy, and checks every agent. Not parallel:
// the hook is the package's, and the other tests' writes go past it.

// killPoint is a root as a kill leaves it.
type killPoint struct {
	name string
	dir  string
	// torn is the half line or half file the kill left (a variant), or "".
	torn string
}

// copyRoot copies a root's files, as a kill at that moment leaves them. A file a running
// daemon removes while it is copied is not in the copy: a kill can also see that.
func copyRoot(from, to string) error {
	return filepath.WalkDir(from, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(from, p)
		if e.IsDir() {
			return os.MkdirAll(filepath.Join(to, rel), 0o755)
		}
		if !e.Type().IsRegular() {
			return nil // the daemon's socket
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		return os.WriteFile(filepath.Join(to, rel), b, 0o644)
	})
}

// killPoints records a kill point before each write under root, with a variant that has the
// write cut short: half of a line added to a transcript, half of a record left in the
// temporary file that WriteAtomically had not renamed yet.
type killPoints struct {
	mu     sync.Mutex
	root   string
	into   string
	points []killPoint
}

func (k *killPoints) fault(file string, appends bool, data []byte) error {
	if !strings.HasPrefix(file, k.root+string(filepath.Separator)) {
		return nil
	}
	rel, _ := filepath.Rel(k.root, file)
	k.mu.Lock()
	defer k.mu.Unlock()
	add := func(label, torn string, mend func(dir string)) {
		dir := filepath.Join(k.into, fmt.Sprintf("%03d", len(k.points)))
		if copyRoot(k.root, dir) != nil {
			return
		}
		if mend != nil {
			mend(dir)
		}
		k.points = append(k.points, killPoint{name: fmt.Sprintf("%03d %s %s", len(k.points), label, rel), dir: dir, torn: torn})
	}
	add("before", "", nil)
	half := data[:len(data)/2]
	if appends {
		add("half a line of", string(half), func(dir string) {
			f, err := os.OpenFile(filepath.Join(dir, rel), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
			if err == nil {
				f.Write(half)
				f.Close()
			}
		})
	} else {
		add("half a file for", string(half), func(dir string) {
			os.WriteFile(filepath.Join(dir, filepath.Dir(rel), "."+filepath.Base(rel)+".00ff.tmp"), half, 0o644)
		})
	}
	return nil
}

// scriptedSession runs prompts, a queued prompt, a card and a tool call on a daemon of its own
// and gives the kill points of every write it made.
func scriptedSession(t *testing.T) (root string, points []killPoint, id wire.UUID) {
	t.Helper()
	fakeRuntimes(t)
	root = t.TempDir()
	k := &killPoints{root: root, into: t.TempDir()}
	d, c := start(t, root)
	store.SetWriteFault(k.fault)
	t.Cleanup(func() { store.SetWriteFault(nil) })

	var permission wire.PermissionRequest
	json.Unmarshal(c.callHearing(t, 1, "agents/start", map[string]any{"runtimeID": "claude", "cwd": "file://" + t.TempDir(),
		"prompt": "Please ask, then write hi.txt"}, "a card", aCard(&permission)), &id)
	waitFor(t, "the card", func() bool { return d.Store.Agent(id).State == wire.AgentStateWaitingOnUser })
	c.call(t, 2, "agents/prompt", map[string]any{"agentID": id, "text": "and then say done", "from": "person"})
	waitFor(t, "the prompt queued", func() bool { return len(d.Store.Agent(id).QueuedPrompts) == 1 })
	c.call(t, 3, "permissions/answer", map[string]any{"permissionID": permission.ID, "optionID": "reject"})
	waitFor(t, "the end", func() bool {
		a := d.Store.Agent(id)
		return a.State == wire.AgentStateFinished && len(a.QueuedPrompts) == 0 && !d.anyLive()
	})
	d.Shutdown()
	store.SetWriteFault(nil)
	k.mu.Lock()
	defer k.mu.Unlock()
	return root, k.points, id
}

// transcriptLines are the non-empty lines of an agent's transcript and the ones that do not decode.
func transcriptLines(t *testing.T, file string) (lines, bad int) {
	t.Helper()
	f, err := os.Open(file)
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	r := bufio.NewScanner(f)
	r.Buffer(make([]byte, 1<<20), 64<<20)
	for r.Scan() {
		if len(bytes.TrimSpace(r.Bytes())) == 0 {
			continue
		}
		lines++
		var e wire.TranscriptEntry
		if diskjson.Unmarshal(r.Bytes(), &e) != nil {
			bad++
		}
	}
	return lines, bad
}

// agentsIn reads each agent's record in a root as it is on disk.
func agentsIn(root string) map[wire.UUID]wire.Agent {
	out := map[wire.UUID]wire.Agent{}
	entries, _ := os.ReadDir(filepath.Join(root, "agents"))
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(root, "agents", e.Name(), "agent.json"))
		if err != nil {
			continue
		}
		var a wire.Agent
		if diskjson.Unmarshal(b, &a) == nil {
			out[a.ID] = a
		}
	}
	return out
}

func settledAfterKill(a *wire.Agent) bool {
	switch a.State {
	case wire.AgentStateFinished, wire.AgentStateStopped, wire.AgentStateArchived:
		return true
	}
	return false
}

// After a kill at any write, each agent is there, its transcript reads (a cut line does not
// glue to the next), its queue is kept or sent, and no card waits that nothing can answer: the
// agent resumes or says it stopped.
func TestAKillAtEachWriteLeavesEveryAgentWhole(t *testing.T) {
	_, points, id := scriptedSession(t)
	if len(points) < 20 {
		t.Fatalf("the scripted session made %d kill points, want a record's and a transcript's writes", len(points))
	}
	for _, p := range points {
		t.Run(p.name, func(t *testing.T) {
			fakeRuntimes(t)
			was := agentsIn(p.dir)
			wasLines, _ := transcriptLines(t, filepath.Join(p.dir, "agents", string(id), "transcript.jsonl"))
			d, _ := start(t, p.dir)
			for wid, w := range was {
				a := d.Store.Agent(wid)
				if a == nil {
					t.Fatalf("agent %s (%s) is not in the store after the restart", wid, w.State)
				}
				waitFor(t, "agent "+string(wid)+" resumed or said it stopped (it was "+string(w.State)+")", func() bool {
					return settledAfterKill(d.Store.Agent(wid)) && !d.anyLive()
				})
				a = d.Store.Agent(wid)
				if a.State == wire.AgentStateStopped && (a.EndedReason == nil) {
					t.Errorf("agent %s stopped and does not say why", wid)
				}
				// A queued prompt is kept or was sent: never lost.
				for _, q := range w.QueuedPrompts {
					kept := false
					for _, now := range a.QueuedPrompts {
						kept = kept || now.ID == q.ID
					}
					for _, u := range userMessages(d, wid) {
						kept = kept || strings.Contains(u, q.Text)
					}
					if !kept {
						t.Errorf("queued prompt %q is lost after the restart", q.Text)
					}
				}
			}
			file := filepath.Join(p.dir, "agents", string(id), "transcript.jsonl")
			lines, bad := transcriptLines(t, file)
			// A cut line stays, ended, and a read skips it: it is the only one that may not decode.
			allowed := 0
			if p.torn != "" && strings.HasSuffix(p.name, "transcript.jsonl") {
				allowed = 1
			}
			if bad > allowed {
				t.Errorf("%d of %d transcript lines do not decode after the restart", bad, lines)
			}
			if lines < wasLines-1 {
				t.Errorf("the transcript had %d lines before the restart and %d after", wasLines, lines)
			}
			// The next line is a line of its own, not glued to a cut one.
			if d.Store.Agent(id) == nil {
				return
			}
			next := wire.TranscriptEntry{Kind: wire.TranscriptEntryKind{AgentMessage: &wire.TranscriptEntryKindAgentMessage{Text: "after the kill"}}}
			if _, err := d.Store.Append(id, next); err != nil {
				t.Fatal(err)
			}
			if after, bad := transcriptLines(t, file); after != lines+1 || bad > allowed {
				t.Errorf("a line added after the restart: %d lines, %d that do not decode (before it: %d)", after, bad, lines)
			}
			// attention.json, when it is there, reads.
			if b, err := os.ReadFile(filepath.Join(p.dir, "attention.json")); err == nil && !json.Valid(b) {
				t.Errorf("attention.json does not read after the restart")
			}
		})
	}
}

// A write the disk refuses (a full disk) tells the caller, leaves the old record whole and
// loses no other agent; the transcript's line is held for the disk's space (held.go), and written
// when there is some.
func TestAFullDiskLeavesTheOldRecordAndHoldsTheLine(t *testing.T) {
	fakeRuntimes(t)
	root := t.TempDir()
	d, c := start(t, root)
	var id wire.UUID
	json.Unmarshal(c.call(t, 1, "agents/start", map[string]any{"runtimeID": "claude", "cwd": "file://" + t.TempDir(), "prompt": "hello"}), &id)
	waitFor(t, "the turn's end", func() bool { return d.Store.Agent(id).State == wire.AgentStateFinished && !d.anyLive() })
	before, _ := os.ReadFile(filepath.Join(root, "agents", string(id), "agent.json"))
	_, linesBefore := transcriptLines(t, filepath.Join(root, "agents", string(id), "transcript.jsonl"))
	_ = linesBefore

	var full sync.Mutex
	refuse := true
	store.SetWriteFault(func(file string, appends bool, data []byte) error {
		full.Lock()
		defer full.Unlock()
		if refuse && strings.HasPrefix(file, root+string(filepath.Separator)) {
			return fmt.Errorf("write %s: no space left on device", file)
		}
		return nil
	})
	t.Cleanup(func() { store.SetWriteFault(nil) })

	a := *d.Store.Agent(id)
	a.Title = ptrOf("renamed")
	if err := d.Store.Save(a); err == nil {
		t.Fatal("a refused write of the record said it was saved")
	}
	after, _ := os.ReadFile(filepath.Join(root, "agents", string(id), "agent.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("the refused write changed the record on disk")
	}
	if got := d.Store.Agent(id); got.Title != nil && *got.Title == "renamed" {
		t.Fatal("the store holds a record that is not on disk")
	}
	if entries, _ := os.ReadDir(filepath.Join(root, "agents", string(id))); true {
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".tmp") {
				t.Errorf("a refused write left %s", e.Name())
			}
		}
	}
	n, err := d.Store.Append(id, wire.TranscriptEntry{Kind: wire.TranscriptEntryKind{AgentMessage: &wire.TranscriptEntryKindAgentMessage{Text: "held line"}}})
	if err == nil {
		t.Fatalf("a refused append (line %d) said it was written", n)
	}
	full.Lock()
	refuse = false
	full.Unlock()
	if _, err := d.Store.WriteHeld(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the held line written", func() bool {
		b, _ := os.ReadFile(filepath.Join(root, "agents", string(id), "transcript.jsonl"))
		return strings.Contains(string(b), "held line")
	})
	if _, bad := transcriptLines(t, filepath.Join(root, "agents", string(id), "transcript.jsonl")); bad > 0 {
		t.Fatalf("%d lines do not decode", bad)
	}
	_ = time.Second
}

func ptrOf[T any](v T) *T { return &v }
