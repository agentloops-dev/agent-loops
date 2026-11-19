package agentsd

import (
	"os"
	"testing"
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
