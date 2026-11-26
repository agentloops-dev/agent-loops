package store

import (
	"cmp"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// SyncWrites is whether WriteAtomically syncs the file before it renames it. Only tests
// turn it off: on a Mac, Go's Sync is F_FULLFSYNC, which flushes the disk's own cache and
// costs about 4 ms a write, so agentsd's tests took twice as long on a Mac as on Linux
// (#4709). A test proves nothing about a crash, so it gives up nothing.
var SyncWrites = true

// The write fault is a test's way into every write the store makes (#6184): it is called before
// the write, with the file, whether the write appends to it (a transcript line) or makes it
// whole, and the bytes. A test copies the root there, which is what a kill at that point
// leaves, or returns an error, which is a disk that refuses the write. Nil in a build.
var writeFault atomic.Pointer[func(file string, appends bool, data []byte) error]

// SetWriteFault sets (or, with nil, clears) the hook. Other tests' daemons write
// at the same time, so a hook looks at the file's path and lets a file that is not its own go.
func SetWriteFault(f func(file string, appends bool, data []byte) error) {
	if f == nil {
		writeFault.Store(nil)
		return
	}
	writeFault.Store(&f)
}

// fault runs the hook, if there is one.
func fault(file string, appends bool, data []byte) error {
	if f := writeFault.Load(); f != nil {
		return (*f)(file, appends, data)
	}
	return nil
}

// WriteAtomically writes data whole, as StoreCoding.writeAtomically does: to a temporary
// file beside it, synced, then renamed into place, so a crash leaves the old file or the
// new one and never a torn one.
func WriteAtomically(file string, data []byte, mode os.FileMode) error {
	if err := fault(file, false, data); err != nil {
		return err
	}
	dir := filepath.Dir(file)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b := make([]byte, 16)
	rand.Read(b)
	tmp := filepath.Join(dir, "."+filepath.Base(file)+"."+hex.EncodeToString(b)+".tmp")
	// store-bounds: not a store: the atomic write that every store's interface does.
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil && SyncWrites {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, file)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

// The bounds of the set-aside copies of one file, of each kind (#2292, #6737). The newest is
// kept whatever its size, until its age.
const (
	SetAsidesKept  = 3
	SetAsidesBytes = 100 << 20
	SetAsidesAge   = MaxAge
)

// setAsideName is a set-aside copy's name: the file, the kind, and the time it was set aside, as
// Unix seconds or as the Mac app's daemon wrote it.
var setAsideName = regexp.MustCompile(`^(.+)\.(unreadable|corrupt)-(?:(\d{1,12})|(\d{4}-\d{2}-\d{2}T\d{6}Z)(?:-\d+)?)$`)

// setAsideTime is when the copy called name was set aside, of which file and kind.
func setAsideTime(name string) (file, kind string, at time.Time, ok bool) {
	m := setAsideName.FindStringSubmatch(name)
	if m == nil {
		return "", "", at, false
	}
	if m[3] != "" {
		n, err := strconv.ParseInt(m[3], 10, 64)
		return m[1], m[2], time.Unix(n, 0), err == nil
	}
	at, err := time.Parse("2006-01-02T150405Z", m[4])
	return m[1], m[2], at, err == nil
}

// TrimSetAsides takes the copies of a file that a read set aside as <name>.unreadable-<time> or
// <name>.corrupt-<time> under their bounds (#2292): each is a whole copy, and a file that is
// unreadable again and again would fill the disk.
func TrimSetAsides(file string) { SweepSetAside(file, time.Now()) }

// SweepSetAside is TrimSetAsides at a time, and says how many went.
func SweepSetAside(file string, now time.Time) int {
	return sweepSetAsides(filepath.Dir(file), filepath.Base(file), now)
}

// SweepSetAsides takes every file's set-aside copies in dir under their bounds, and says how
// many went.
func SweepSetAsides(dir string, now time.Time) int { return sweepSetAsides(dir, "", now) }

func sweepSetAsides(dir, only string, now time.Time) int {
	type aside struct {
		path string
		at   time.Time
		size int64
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	copies := map[[2]string][]aside{}
	for _, e := range entries {
		file, kind, at, ok := setAsideTime(e.Name())
		if !ok || only != "" && file != only {
			continue
		}
		fi, err := e.Info()
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		// A clock that was ahead: the file's own time, so that the copy still ages.
		if at.After(now) {
			at = fi.ModTime()
		}
		key := [2]string{file, kind}
		copies[key] = append(copies[key], aside{filepath.Join(dir, e.Name()), at, fi.Size()})
	}
	gone := 0
	for _, list := range copies {
		slices.SortFunc(list, func(a, b aside) int {
			return cmp.Or(b.at.Compare(a.at), cmp.Compare(b.path, a.path))
		})
		var total int64
		for i, c := range list {
			total += c.size
			if i >= SetAsidesKept || i > 0 && total > SetAsidesBytes || now.Sub(c.at) >= SetAsidesAge {
				if os.Remove(c.path) == nil {
					gone++
				}
			}
		}
	}
	return gone
}

// globEscape is a path with the glob's own characters taken as they are.
func globEscape(p string) string {
	r := strings.NewReplacer(`\`, `\\`, `*`, `\*`, `?`, `\?`, `[`, `\[`)
	return r.Replace(p)
}
