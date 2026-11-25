package agentsd

import (
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
)

// The drop box's age (#6711): a file goes 90 days after it was last changed or put there, also
// one put there by hand (Alex, 2026-11-24). Size refuses instead (dropboxTotal): nothing goes to
// make room.
const (
	dropboxKept     = store.MaxAge
	dropboxPartKept = 24 * time.Hour // what a put cut off left
)

// sweepDropboxes takes each folder's drop box under its age.
func (d *Daemon) sweepDropboxes(now time.Time) {
	for _, p := range d.folders(true) {
		folder := store.FolderPath(p.Folder.Folder)
		n, err := sweepDropbox(folder, now)
		if err != nil {
			log.Printf("retention: the drop box of %s: %v", folder, err)
		}
		if n > 0 {
			log.Printf("retention: %d files in the drop box of %s past %v", n, folder, dropboxKept)
		}
	}
}

// sweepDropbox sweeps one folder's drop box, and says how many of its files went. It leaves a
// file that git tracks and a repository in the drop box, and follows no link: what a link leads
// to is not the drop box's. When git cannot say what it tracks, only cut-off puts go.
func sweepDropbox(folder string, now time.Time) (int, error) {
	root, err := os.OpenRoot(folder)
	if err != nil {
		return 0, nil // a folder on a drive not plugged in now, or gone
	}
	defer root.Close()
	box := path.Join(".agents", "dropbox")
	for _, p := range []string{".agents", box} {
		if fi, err := root.Lstat(p); err != nil || !fi.IsDir() {
			return 0, nil
		}
	}
	var old []string
	// The walk sweeps what it can read and goes on, so WalkDir returns no error.
	_ = fs.WalkDir(root.FS(), box, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if e.IsDir() {
			if _, err := root.Lstat(path.Join(p, ".git")); err == nil {
				return fs.SkipDir
			}
			return nil
		}
		fi, err := e.Info()
		if err != nil || !fi.Mode().IsRegular() {
			return nil
		}
		if strings.HasPrefix(e.Name(), ".") && strings.HasSuffix(e.Name(), ".part") && now.Sub(fi.ModTime()) > dropboxPartKept {
			_ = root.Remove(p)
		} else if now.Sub(lastTouched(fi, now)) >= dropboxKept {
			old = append(old, p)
		}
		return nil
	})
	if len(old) == 0 {
		return 0, nil
	}
	dir := filepath.Join(folder, ".agents", "dropbox")
	names, err := trackedFiles(dir)
	if err != nil {
		return 0, err
	}
	// By the file, not the name: git can spell a name in another case or Unicode form.
	var tracked []fs.FileInfo
	for name := range names {
		if fi, err := os.Lstat(filepath.Join(dir, name)); err == nil {
			tracked = append(tracked, fi)
		}
	}
	gone := 0
	for _, p := range old {
		fi, err := root.Lstat(p)
		if err != nil || now.Sub(lastTouched(fi, now)) < dropboxKept || isOneOf(fi, tracked) {
			continue
		}
		if err := root.Remove(p); err != nil {
			return gone, err
		}
		gone++
	}
	return gone, nil
}

func isOneOf(fi fs.FileInfo, list []fs.FileInfo) bool {
	for _, t := range list {
		if os.SameFile(fi, t) {
			return true
		}
	}
	return false
}

// lastTouched is the later of a file's change and its arrival: a file copied or moved in keeps
// the time of its last change, which can be years ago, and its inode has the time it came.
func lastTouched(fi fs.FileInfo, now time.Time) time.Time {
	at := fi.ModTime()
	if at.After(now) {
		at = time.Time{}
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		_, c := statTimes(st)
		if came := time.Unix(c.Unix()); came.After(at) {
			at = came
		}
	}
	return at
}

// sweepSetAsides takes the copies of unreadable files under their bounds (#6737), where the
// daemon and the Mac app's daemon before it made them.
func (d *Daemon) sweepSetAsides(now time.Time) {
	root := d.Store.Loc.Root
	dirs := map[string]bool{root: true, d.SettingsDir: true}
	older, _ := filepath.Glob(filepath.Join(root, "set-aside", "*"))
	for _, dir := range older {
		dirs[dir] = true
	}
	n := 0
	for dir := range dirs {
		n += store.SweepSetAsides(dir, now)
	}
	// Not in a folder of the daemon's own: only these files' copies.
	for _, f := range d.smallFiles {
		if file := f.Path(); !dirs[filepath.Dir(file)] {
			n += store.SweepSetAside(file, now)
		}
	}
	if n > 0 {
		log.Printf("retention: %d copies of unreadable files past their bounds", n)
	}
}
