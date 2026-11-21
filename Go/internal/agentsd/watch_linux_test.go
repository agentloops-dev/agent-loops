package agentsd

import (
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// A root inotify cannot watch (here no read on it; on a runner, no watches left) is no
// watch (#6248): one that said it was up heard nothing, and a workflow written on disk
// was never told.
func TestAFolderWatchItCannotHearIsNoWatch(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root reads every folder")
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(root, 0o700); err != nil { //nolint:gosec // a folder needs x for TempDir to remove it
			t.Error(err)
		}
	})
	w, err := newFolderWatch(root, func([]string) {})
	if err == nil {
		w.stop()
		t.Fatal("a watch on a root it cannot hear")
	}
}

// A watch whose root is gone lets its descriptor go, and takes no other file's bytes after
// (#6395, #6431, #6359). Its stop once closed the number while its reader still read it, so
// the next pipe or file the process opened got the number, and the reader took its bytes:
// `git remote -v` said nothing, so a gitea folder was put on GitHub, and connections.json
// read short, so a Forgejo connection that had just been put was not there.
func TestAGoneWatchTakesNoOtherFilesBytes(t *testing.T) {
	t.Parallel()
	for range 5 {
		root, err := os.MkdirTemp(t.TempDir(), "watch")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := newFilesWatch(root, func([]string) {}); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(root); err != nil { // IN_DELETE_SELF: the watch stops
			t.Fatal(err)
		}
		for range 3 {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.WriteString("origin\thttps://gitea.example.org/team/app.git (fetch)\n"); err != nil {
				t.Fatal(err)
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			time.Sleep(60 * time.Millisecond) // longer than the reader's nap
			b, err := io.ReadAll(r)
			_ = r.Close()
			if err != nil || !strings.HasPrefix(string(b), "origin\t") {
				t.Fatalf("the pipe read %q, %v: the gone watch took its bytes", b, err)
			}
		}
	}
}
