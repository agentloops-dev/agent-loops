package agentsd

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// #6711: a file in the drop box goes at 90 days, also an old one put there by hand, but not before
// it has been there so long; a tracked file, a repository and what a link leads to stay.
func TestADropBoxFileGoesAtNinetyDays(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	box := filepath.Join(dir, ".agents", "dropbox")
	for _, f := range []string{"in/old.txt", "kept.md", "clone/a.txt", "clone/.git/HEAD"} {
		must(t, os.MkdirAll(filepath.Dir(filepath.Join(box, f)), 0o750))
		writeFile(t, filepath.Join(box, f), "x")
	}
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "theirs.txt"), "x")
	must(t, os.Symlink(outside, filepath.Join(box, "link")))
	year := time.Now().Add(-365 * 24 * time.Hour)
	must(t, os.Chtimes(filepath.Join(box, "in", "old.txt"), year, year))
	for _, args := range [][]string{{"init", "-q"}, {"add", ".agents/dropbox/kept.md"}} {
		if _, err := git(dir, args...); err != nil {
			t.Fatal(args, err)
		}
	}
	day := 24 * time.Hour
	if n, err := sweepDropbox(dir, time.Now().Add(89*day)); n != 0 || err != nil {
		t.Fatal(n, err)
	}
	// With git not able to say what it tracks, nothing goes.
	head := filepath.Join(dir, ".git", "HEAD")
	was, err := os.ReadFile(head)
	must(t, err)
	must(t, os.Remove(head))
	if n, err := sweepDropbox(dir, time.Now().Add(91*day)); n != 0 || err == nil {
		t.Fatal(n, err)
	}
	writeFile(t, head, string(was))
	if n, err := sweepDropbox(dir, time.Now().Add(91*day)); n != 1 || err != nil {
		t.Fatal(n, err)
	}
	if _, err := os.Stat(filepath.Join(box, "in", "old.txt")); !os.IsNotExist(err) {
		t.Fatal("a file past 90 days stayed")
	}
	for _, f := range []string{"in", "kept.md", "clone/a.txt", "clone/.git/HEAD", "link/theirs.txt"} {
		if _, err := os.Stat(filepath.Join(box, f)); err != nil {
			t.Fatal(err)
		}
	}
	// A drop box that is a link is not swept.
	other := t.TempDir()
	must(t, os.Mkdir(filepath.Join(other, ".agents"), 0o750))
	must(t, os.Symlink(outside, filepath.Join(other, ".agents", "dropbox")))
	if n, err := sweepDropbox(other, time.Now().Add(91*day)); n != 0 || err != nil {
		t.Fatal(n, err)
	}
}
