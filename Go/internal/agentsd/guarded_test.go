package agentsd

import (
	"encoding/json"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// Ports GuardedFilesTests: a change to the app's own files made while an agent works waits,
// the app goes on with the approved copy and will not write over it, and Keep and Undo
// settle it; the person's own edit is theirs; a pull's waits too.

func TestAChangeWhileAnAgentWorksWaitsForKeepOrUndo(t *testing.T) {
	t.Parallel()

	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	dir := folder(t, c, 1)
	folder := store.FolderURL(dir)
	must(t, os.WriteFile(filepath.Join(dir, "a.md"), nil, 0o644)) //nolint:gosec // G306: the mode this file always had; only its error is new
	c.call(t, 2, "pins/pin", map[string]any{"folder": folder, "path": "a.md"})
	approved, _ := os.ReadFile(filepath.Join(dir, ".agents", "pins.json"))
	worker := seeded(t, d, dir, func(a *wire.Agent) {
		title := "Tidy"
		a.State, a.EndedReason, a.Title = wire.AgentStateRunning, nil, &title
	})
	must(t, os.WriteFile(filepath.Join(dir, ".agents", "pins.json"), []byte(`{"pins":[]}`), 0o644)) //nolint:gosec // G306: the mode this file always had; only its error is new
	if f := d.readPins(dir); len(f.pins) != 1 {
		t.Fatalf("the change was used: %+v", f.pins)
	}
	if e := c.callError(t, 3, "pins/unpin", map[string]any{"folder": folder, "path": "a.md"}); !strings.HasSuffix(e.Message, "is waiting for the person to keep or undo the change in Project Settings.") {
		t.Fatal(e.Message)
	}
	s, _ := d.summaryFor(dir)
	if len(s.GuardedChanges) != 1 || s.GuardedChanges[0].ChangedBy[0] != "Tidy" || *s.GuardedChanges[0].AskedIn != worker.ID ||
		len(s.Approvals) != 1 || s.Approvals[0].Line != "Changed by Tidy" || len(s.Approvals[0].Actions) != 3 {
		t.Fatalf("%+v %+v", s.GuardedChanges, s.Approvals)
	}
	if n := d.attentionPending().Needs; len(n) != 1 || n[0].Kind != wire.NeedKindGuardedChange || n[0].Headline.H3 != "Tidy changed .agents/pins.json" {
		t.Fatalf("%+v", n)
	}
	var reading wire.GuardedChangeReading
	mustUnmarshal(t, c.call(t, 4, "folders/readGuardedChange", map[string]any{"folder": folder, "path": ".agents/pins.json"}), &reading)
	if reading.Approved == nil || *reading.Approved != string(approved) || reading.Current == nil || len(reading.Lines) == 0 {
		t.Fatalf("%+v", reading)
	}
	// Undo, with the digest that was looked at: the approved copy goes back.
	if e := c.callError(t, 5, "folders/undoGuardedChange", map[string]any{"folder": folder, "path": ".agents/pins.json", "digest": "stale"}); e.Code != wire.FailureNotChanged {
		t.Fatalf("%+v", e)
	}
	c.call(t, 6, "approvals/answer", map[string]any{"folder": folder, "kind": "file", "key": ".agents/pins.json", "digest": reading.Digest, "action": "deny"})
	if b, _ := os.ReadFile(filepath.Join(dir, ".agents", "pins.json")); string(b) != string(approved) {
		t.Fatalf("%s", b)
	}
	if s, _ := d.summaryFor(dir); len(s.GuardedChanges) != 0 {
		t.Fatalf("%+v", s.GuardedChanges)
	}
	// Changed again and kept: the change is used from now on.
	must(t, os.WriteFile(filepath.Join(dir, ".agents", "pins.json"), []byte(`{"pins":[]}`), 0o644)) //nolint:gosec // G306: the mode this file always had; only its error is new
	d.readPins(dir)
	mustUnmarshal(t, c.call(t, 7, "folders/readGuardedChange", map[string]any{"folder": folder, "path": ".agents/pins.json"}), &reading)
	c.call(t, 8, "folders/keepGuardedChange", map[string]any{"folder": folder, "path": ".agents/pins.json", "digest": reading.Digest})
	if f := d.readPins(dir); len(f.pins) != 0 {
		t.Fatalf("%+v", f.pins)
	}
}

func TestThePersonsOwnEditIsTheirs(t *testing.T) {
	t.Parallel()

	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	dir := folder(t, c, 1)
	must(t, os.WriteFile(filepath.Join(dir, "a.md"), nil, 0o644)) //nolint:gosec // G306: the mode this file always had; only its error is new
	c.call(t, 2, "pins/pin", map[string]any{"folder": store.FolderURL(dir), "path": "a.md"})
	must(t, os.WriteFile(filepath.Join(dir, ".agents", "pins.json"), []byte(`{"pins":[]}`), 0o644)) //nolint:gosec // G306: the mode this file always had; only its error is new
	if f := d.readPins(dir); len(f.pins) != 0 {
		t.Fatal("an edit with nothing running, outside a repository, is the person's")
	}
}

// Not parallel: prepareWatch sets the watch helper for the whole package, as the other
// watched tests do. The folder's watch runs here as on CI's Linux, also on a Mac (#6521).
func TestAPullsChangeWaits(t *testing.T) {
	prepareWatch(t)
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	dir := repo(t)
	c.call(t, 1, "folders/add", map[string]any{"folder": store.FolderURL(dir)})
	// The folder's watcher checks folder.json too, whenever it hears it (#6521). So each write
	// is atomic, and the pulled copy is in HEAD before it is on disk: whenever the watcher
	// looks, it sees the approved copy or the pull's, never a half-written file.
	file := filepath.Join(dir, ".agents", "folder.json")
	must(t, os.MkdirAll(filepath.Dir(file), 0o755)) //nolint:gosec // G301: the mode this folder always had; only its error is new
	must(t, store.WriteAtomically(file, []byte(`{"diskSpace":{"lowPercent":30}}`), 0o644))
	approvedSoon(t, d, dir, ".agents/folder.json")
	pulled := []byte(`{"diskSpace":{"lowPercent":50}}`)
	blob, err := gitRaw(dir, pulled, "hash-object", "-w", "--stdin")
	must(t, err)
	must(t, errOnly(git(dir, "update-index", "--add", "--cacheinfo", "100644,"+strings.TrimSpace(string(blob))+",.agents/folder.json")))
	must(t, errOnly(git(dir, "commit", "-q", "-m", "pulled")))
	must(t, store.WriteAtomically(file, pulled, 0o644))
	d.checkGuarded(dir, ".agents/folder.json", nil)
	s, _ := d.summaryFor(dir)
	if len(s.GuardedChanges) != 1 || !s.GuardedChanges[0].ByGit || s.Approvals[0].Line != "Changed with the last pull" ||
		s.Folder.DiskSpace == nil || *s.Folder.DiskSpace.LowPercent != 30 {
		t.Fatalf("%+v %+v %+v", s.GuardedChanges, s.Approvals, s.Folder.DiskSpace)
	}
	if e := c.callError(t, 2, "folders/setDiskSpace", map[string]any{"folder": store.FolderURL(dir), "diskSpace": map[string]any{"lowPercent": 40}}); !strings.HasPrefix(e.Message, "The disk space lines were not changed: ") {
		t.Fatal(e.Message)
	}
}

// A pull writes the work tree before it moves HEAD (#6523): its change, seen in between,
// is not the person's own edit. It is not used while git holds the index lock, also past
// the branch settle, and once HEAD has it, it waits for Keep or Undo.
func TestAPullsChangeSeenBeforeHEADMovesWaits(t *testing.T) {
	t.Parallel()

	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	dir := repo(t)
	c.call(t, 1, "folders/add", map[string]any{"folder": store.FolderURL(dir)})
	file := filepath.Join(dir, ".agents", "folder.json")
	must(t, os.MkdirAll(filepath.Dir(file), 0o755)) //nolint:gosec // G301: the mode this folder always had; only its error is new
	must(t, store.WriteAtomically(file, []byte(`{"diskSpace":{"lowPercent":30}}`), 0o644))
	approvedSoon(t, d, dir, ".agents/folder.json")
	// The pull: git holds the index lock, writes the work tree, then moves HEAD.
	lock := filepath.Join(dir, ".git", "index.lock")
	must(t, os.WriteFile(lock, nil, 0o644)) //nolint:gosec // G306: git's own mode for its lock
	pulled := []byte(`{"diskSpace":{"lowPercent":50}}`)
	must(t, store.WriteAtomically(file, pulled, 0o644))
	d.checkGuarded(dir, ".agents/folder.json", nil)
	until(t, "held past the branch settle", func() bool {
		d.checkGuarded(dir, ".agents/folder.json", nil)
		d.guardedMu.Lock()
		defer d.guardedMu.Unlock()
		u := d.guardedUnsure[guardedKey{dir, ".agents/folder.json"}]
		return u != nil && time.Since(u.since) > branchSettle*3/2
	})
	if s, _ := d.summaryFor(dir); len(s.GuardedChanges) != 0 || s.Folder.DiskSpace == nil || *s.Folder.DiskSpace.LowPercent != 30 {
		t.Fatalf("before HEAD moved: %+v %+v", s.GuardedChanges, s.Folder.DiskSpace)
	}
	// HEAD moves while the lock is still held: the commit goes through an index of its own.
	index := []string{"GIT_INDEX_FILE=" + filepath.Join(dir, ".git", "pull-index")}
	for _, args := range [][]string{{"read-tree", "HEAD"}, {"add", ".agents/folder.json"}, {"commit", "-q", "-m", "pulled"}} {
		_, err := gitRawEnv(dir, index, nil, args...)
		must(t, err)
	}
	must(t, os.Remove(lock))
	until(t, "the pull's change waits", func() bool {
		s, _ := d.summaryFor(dir)
		return len(s.GuardedChanges) == 1 && s.GuardedChanges[0].ByGit
	})
	if s, _ := d.summaryFor(dir); s.Approvals[0].Line != "Changed with the last pull" || *s.Folder.DiskSpace.LowPercent != 30 {
		t.Fatalf("%+v %+v", s.Approvals, s.Folder.DiskSpace)
	}
}

// The person's own edit in a repository, which HEAD never gets, is theirs once the branch
// settle has passed (#6523).
func TestThePersonsOwnEditInARepositoryIsTheirsAfterTheSettle(t *testing.T) {
	t.Parallel()

	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	dir := repo(t)
	c.call(t, 1, "folders/add", map[string]any{"folder": store.FolderURL(dir)})
	file := filepath.Join(dir, ".agents", "folder.json")
	must(t, os.MkdirAll(filepath.Dir(file), 0o755)) //nolint:gosec // G301: the mode this folder always had; only its error is new
	must(t, store.WriteAtomically(file, []byte(`{"diskSpace":{"lowPercent":30}}`), 0o644))
	approvedSoon(t, d, dir, ".agents/folder.json")
	must(t, store.WriteAtomically(file, []byte(`{"diskSpace":{"lowPercent":50}}`), 0o644))
	d.checkGuarded(dir, ".agents/folder.json", nil)
	if d.settled(dir, ".agents/folder.json") {
		t.Fatal("approved before the branch settle")
	}
	until(t, "the person's edit is used", func() bool {
		s, _ := d.summaryFor(dir)
		return len(s.GuardedChanges) == 0 && s.Folder.DiskSpace != nil && *s.Folder.DiskSpace.LowPercent == 50
	})
	if !d.settled(dir, ".agents/folder.json") {
		t.Fatal("the person's edit is not settled")
	}
}

// approvedSoon waits until a guarded file is the approved copy: in a repository, with nothing
// running, the person's edit is approved once the branch settle has passed (#6523).
func approvedSoon(t *testing.T, d *Daemon, dir, path string) {
	t.Helper()
	until(t, path+" approved", func() bool { return d.settled(dir, path) })
}

// A guarded file first seen while it is written (empty, or half of it) is not approved
// as it stands: the whole copy is then a change like any other, and a pull's waits (#6525).
func TestAFileFirstSeenHalfWrittenIsNotApproved(t *testing.T) {
	t.Parallel()

	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	for _, half := range []string{"", `{"diskSpace":{"lowPer`} {
		// A folder the app has not added, so no watch: only the test checks the file.
		dir := repo(t)
		file := filepath.Join(dir, ".agents", "folder.json")
		must(t, os.MkdirAll(filepath.Dir(file), 0o755))  //nolint:gosec // G301: the mode .agents always has
		must(t, os.WriteFile(file, []byte(half), 0o644)) //nolint:gosec // G306: the mode folder.json always has
		d.checkGuarded(dir, ".agents/folder.json", nil)
		if s := guardedStateOf(d, dir, ".agents/folder.json"); s.ApprovedDigest != nil || s.ApprovedContent != nil {
			t.Fatalf("%q was approved", half)
		}
		// The whole copy, as a pull brings it: in HEAD, then on disk.
		must(t, store.WriteAtomically(file, []byte(`{"diskSpace":{"lowPercent":50}}`), 0o644))
		must(t, errOnly(git(dir, "add", ".agents/folder.json")))
		must(t, errOnly(git(dir, "commit", "-q", "-m", "pulled")))
		d.checkGuarded(dir, ".agents/folder.json", nil)
		if s := guardedStateOf(d, dir, ".agents/folder.json"); s.Pending == nil || !s.Pending.ByGit || s.ApprovedDigest != nil {
			t.Fatalf("after %q: %+v", half, s)
		}
	}
}

// A check reads the file with guardedMu held (#6521, #6525): a check that waits for the
// lock records the copy on disk when it gets it, not an older one it read before.
func TestACheckReadsTheFileUnderTheLock(t *testing.T) {
	t.Parallel()

	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	for _, tc := range []struct{ path, first, next string }{
		{".agents/folder.json", `{"diskSpace":{"lowPercent":30}}`, `{"diskSpace":{"lowPercent":50}}`},
		{folderPolicyFile, `{"helpers":{"mostRunning":2}}`, `{"helpers":{"mostRunning":1}}`}, // tighter: applies at once
	} {
		// Not a repository, with nothing running: the person's own edit is approved at once.
		dir := t.TempDir()
		file := filepath.Join(dir, tc.path)
		must(t, os.MkdirAll(filepath.Dir(file), 0o755)) //nolint:gosec // G301: the mode .agents always has
		must(t, store.WriteAtomically(file, []byte(tc.first), 0o644))
		d.checkGuarded(dir, tc.path, nil)
		if s := guardedStateOf(d, dir, tc.path); string(s.ApprovedContent) != tc.first {
			t.Fatalf("%s first seen: %+v", tc.path, s)
		}
		d.guardedMu.Lock()
		done := make(chan struct{})
		go func() { d.checkGuarded(dir, tc.path, nil); close(done) }()
		until(t, tc.path+"'s check waits for the lock", checkWaitsForTheLock)
		must(t, store.WriteAtomically(file, []byte(tc.next), 0o644))
		d.guardedMu.Unlock()
		<-done
		if s := guardedStateOf(d, dir, tc.path); string(s.ApprovedContent) != tc.next || s.Pending != nil {
			t.Fatalf("%s: the check recorded the copy it read before the lock: %q %+v", tc.path, s.ApprovedContent, s.Pending)
		}
	}
}

// checkWaitsForTheLock is whether a goroutine in a guarded check is blocked on a mutex.
// Another test's check can be too, so it can say yes early: the test then passes for a
// check that had not begun, never fails for one that has.
func checkWaitsForTheLock() bool {
	buf := make([]byte, 1<<20)
	for _, g := range strings.Split(string(buf[:goruntime.Stack(buf, true)]), "\n\n") {
		if strings.Contains(g, "[sync.Mutex.Lock") && strings.Contains(g, ".checkGuarded(") {
			return true
		}
	}
	return false
}

// guardedStateOf is a copy of a guarded file's record in dir.
func guardedStateOf(d *Daemon, dir, path string) guardedState {
	d.guardedMu.Lock()
	defer d.guardedMu.Unlock()
	s, _ := d.stateOf(dir, path)
	return *s
}

func TestDiffLines(t *testing.T) {
	t.Parallel()

	got := diffLines("a\nb\nc\n", "a\nB\nc\nd\n")
	var s []string
	for _, l := range got {
		s = append(s, string(l.Kind)+":"+l.Text)
	}
	if strings.Join(s, " ") != "same:a removed:b added:B same:c added:d" {
		t.Fatal(s)
	}
}

// A change waiting on a file the app no longer keeps goes when the records are read (#2680).
func TestAFileTheAppNoLongerKeepsWaitsForNothing(t *testing.T) {
	t.Parallel()

	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	dir := folder(t, c, 1)
	recs := guardedRecords{States: []guardedState{
		{Folder: store.FolderURL(dir), Path: ".agents/project.json", Pending: &guardedPending{ChangedByIDs: []wire.UUID{}, ChangedBy: []string{"Tidy"}}},
		{Folder: store.FolderURL(dir), Path: ".agents/pins.json", Pending: &guardedPending{ChangedByIDs: []wire.UUID{}, ChangedBy: []string{"Tidy"}}},
	}}
	b, _ := json.Marshal(recs)
	must(t, os.WriteFile(d.guardedFile(), b, 0o644)) //nolint:gosec // G306: the mode this file always had; only its error is new
	d.guardedMu.Lock()
	d.guarded = nil
	d.guardedMu.Unlock()
	if s, _ := d.summaryFor(dir); len(s.GuardedChanges) != 1 || s.GuardedChanges[0].Path != ".agents/pins.json" {
		t.Fatalf("%+v", s.GuardedChanges)
	}
}
