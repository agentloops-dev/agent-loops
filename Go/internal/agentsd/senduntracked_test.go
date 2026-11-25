package agentsd

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// The Send sheet's measure of untracked files, and a send that leaves them out (#7121).

func putUntracked(t *testing.T, dir, name string, size int) {
	t.Helper()
	path := filepath.Join(dir, name)
	must(t, os.MkdirAll(filepath.Dir(path), 0o700))
	must(t, os.WriteFile(path, []byte(strings.Repeat("x", size)), 0o600))
}

func TestSendFactsMeasureUntrackedFiles(t *testing.T) {
	t.Parallel()

	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	src := repo(t)
	facts := func(folder string, measure *bool) wire.SendFacts {
		t.Helper()
		f, err := d.sendFacts(wire.SendFactsRequest{Folder: store.FolderURL(folder), MeasureUntracked: measure})
		must(t, err)
		return f
	}
	if f := facts(src, new(true)); f.Untracked != nil {
		t.Fatalf("nothing untracked: %+v", f.Untracked)
	}
	must(t, os.WriteFile(filepath.Join(src, ".git", "info", "exclude"), []byte("ignored/\n"), 0o600))
	putUntracked(t, src, "ignored/big", 9000)
	putUntracked(t, src, "web/node_modules/a/index.js", 4000)
	putUntracked(t, src, "web/node_modules/b.js", 1000)
	putUntracked(t, src, "web/src/app.js", 30)
	putUntracked(t, src, "notes/one.md", 200)
	putUntracked(t, src, "dump.sql", 100)
	// A file named as build output is a file.
	putUntracked(t, src, "target", 5)

	if f := facts(src, nil); f.Untracked != nil || !f.HasChanges {
		t.Fatalf("measured with nobody asking: %+v", f)
	}
	// From the top and from a folder in it: a send takes the whole repository.
	for _, folder := range []string{src, filepath.Join(src, "notes")} {
		u := facts(folder, new(true)).Untracked
		if u == nil || u.Files != 6 || u.Size != 5335 || !u.BuildOutput || u.Partial || len(u.Largest) != 3 ||
			u.Largest[0] != (wire.SendUntrackedItem{Path: "web/node_modules/", Size: 5000}) ||
			u.Largest[1] != (wire.SendUntrackedItem{Path: "notes/", Size: 200}) ||
			u.Largest[2] != (wire.SendUntrackedItem{Path: "dump.sql", Size: 100}) {
			t.Fatalf("%s: %+v", folder, u)
		}
	}
	for i := range 20 {
		putUntracked(t, src, "many/"+strconv.Itoa(i), 1)
	}
	if u := measureUntracked(src, 10, time.Minute); u == nil || !u.Partial || u.Files != 10 {
		t.Fatalf("no bound in files: %+v", u)
	}
	if u := measureUntracked(src, 10, 0); u == nil || !u.Partial || u.Files != 0 {
		t.Fatalf("no bound in time: %+v", u)
	}
}

func TestSendPackLeavesOutUntrackedFiles(t *testing.T) {
	t.Parallel()

	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	src := repo(t)
	putUntracked(t, src, "tracked.txt", 1)
	putUntracked(t, src, "gone.txt", 1)
	sendGit(t, src, nil, "add", "-A")
	sendGit(t, src, nil, "commit", "-q", "-m", "tracked")
	putUntracked(t, src, "tracked.txt", 2)
	must(t, os.Remove(filepath.Join(src, "gone.txt")))
	putUntracked(t, src, "staged.txt", 3)
	sendGit(t, src, nil, "add", "staged.txt")
	putUntracked(t, src, "node_modules/a.js", 4)
	files := func(leaveOut *bool) string {
		t.Helper()
		p, err := d.sendPack(wire.SendPackRequest{Folder: store.FolderURL(src), Branches: []string{"main"},
			IncludeChanges: true, LeaveOutUntracked: leaveOut})
		must(t, err)
		if p.Patch == nil {
			return ""
		}
		file, err := d.outgoingFile(p.Patch.ID)
		must(t, err)
		return strings.Join(strings.Fields(sendGit(t, src, nil, "apply", "--numstat", file)), " ")
	}
	all := "0 1 gone.txt 1 0 node_modules/a.js 1 0 staged.txt 1 1 tracked.txt"
	if got := files(nil); got != all {
		t.Fatalf("an agent's send: %q", got)
	}
	if got := files(new(false)); got != all {
		t.Fatalf("kept in: %q", got)
	}
	tracked := "0 1 gone.txt 1 0 staged.txt 1 1 tracked.txt"
	if got := files(new(true)); got != tracked {
		t.Fatalf("left out: %q", got)
	}
	// With no index to copy, the commit's files are the tracked ones: no file is a deletion
	// because the index is gone.
	must(t, os.Remove(filepath.Join(src, ".git", "index")))
	if got := files(new(true)); got != "0 1 gone.txt 1 1 tracked.txt" {
		t.Fatalf("with no index: %q", got)
	}
}
