package agentsd

import (
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/agentloops-dev/agent-loops/Go/internal/safe"
)

// maxWatchedFolders bounds one tree's inotify watches: past it, deeper folders are not
// heard, and the log says so once.
const maxWatchedFolders = 4096

// folderWatch is FolderWatch on Linux: inotify on every folder under root but the ones a
// folder's watch leaves out, changes gathered for a quarter of a second and said as the
// folders they were in.
type folderWatch struct {
	fd      int
	root    string
	mu      sync.Mutex
	paths   map[int]string
	changed func([]string)
	done    chan struct{}
	once    sync.Once
	// skip is which folders under root are not watched; nil is an agent folder's rule.
	skip func(path, name string) bool
	// files says the paths that changed, not the folders they were in.
	files bool
	// warned is whether a folder that could not be watched was logged.
	warned bool
}

func newFilesWatch(root string, changed func([]string)) (*folderWatch, error) {
	return startFolderWatch(&folderWatch{root: root, changed: changed}, "files watch")
}

// newFolderWatch is a folder's one watch (#173): every folder but the ones skipWatched
// leaves out, said as the paths that changed.
func newFolderWatch(root string, changed func([]string)) (*folderWatch, error) {
	return startFolderWatch(&folderWatch{root: root, changed: changed, files: true,
		skip: func(p, name string) bool { return skipWatched(root, p, name) }}, "folder watch")
}

func startFolderWatch(w *folderWatch, name string) (*folderWatch, error) {
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		return nil, err
	}
	w.fd, w.paths, w.done = fd, map[int]string{}, make(chan struct{})
	// A root that cannot be watched (no inotify watches left: ENOSPC) is not a watch: one
	// that said it was up and heard nothing lost a workflow written on disk (#6248). The
	// folder list's next look tries it again.
	if err := w.addTree(w.root); err != nil {
		_ = unix.Close(fd) // the error that counts is the watch's
		return nil, err
	}
	safe.Go(name, w.read)
	return w, nil
}

const watchMask = unix.IN_CREATE | unix.IN_DELETE | unix.IN_MODIFY | unix.IN_MOVED_FROM | unix.IN_MOVED_TO |
	unix.IN_CLOSE_WRITE | unix.IN_ATTRIB | unix.IN_DELETE_SELF

// addTree watches dir and every folder under it the watch does not leave out. It says why
// the root could not be watched; a folder under it that cannot be is logged, once a watch,
// as it is not heard (#6248).
func (w *folderWatch) addTree(dir string) error {
	var rootErr error
	filepath.WalkDir(dir, func(p string, e os.DirEntry, err error) error {
		if err != nil || !e.IsDir() {
			return nil
		}
		if w.skip != nil && p != w.root && w.skip(p, e.Name()) ||
			w.skip == nil && (p != w.root && excludedName(e.Name()) || filepath.Base(filepath.Dir(p)) == ".agents" && e.Name() == "worktrees") {
			return filepath.SkipDir
		}
		w.mu.Lock()
		full := len(w.paths) >= maxWatchedFolders
		w.mu.Unlock()
		if full {
			log.Printf("files watch: %s has more than %d folders; the rest are not watched", w.root, maxWatchedFolders)
			return filepath.SkipAll
		}
		wd, err := unix.InotifyAddWatch(w.fd, p, watchMask)
		if err != nil {
			if p == w.root {
				rootErr = err
				return filepath.SkipAll
			}
			w.mu.Lock()
			warned := w.warned
			w.warned = true
			w.mu.Unlock()
			if !warned {
				log.Printf("files watch: %s is not heard: %v", p, err)
			}
			return nil
		}
		w.mu.Lock()
		w.paths[wd] = p
		w.mu.Unlock()
		return nil
	})
	return rootErr
}

// read is the only one that uses w.fd once the watch is up, so it is the one that closes
// it, when it returns. Closed from stop on another goroutine, the number was given to the
// next socket the process opened, and this loop's next Read took that socket's bytes: an
// HTTP request the app tools' server never saw, its client waiting 120 s for an answer
// (#6395, #4593).
func (w *folderWatch) read() {
	defer unix.Close(w.fd)
	buf := make([]byte, 64<<10)
	pending := map[string]bool{}
	flush := time.NewTicker(250 * time.Millisecond)
	defer flush.Stop()
	for {
		select {
		case <-w.done:
			return
		case <-flush.C:
			pending = w.readFlush(pending)
		default:
		}
		n, err := unix.Read(w.fd, buf)
		if err != nil || n <= 0 {
			if err == unix.EAGAIN || n == 0 {
				time.Sleep(50 * time.Millisecond)
				continue
			}
			select {
			case <-w.done:
			default:
				log.Printf("files watch: %v", err)
			}
			return
		}
		for off := 0; off+unix.SizeofInotifyEvent <= n; {
			size, rootGone := w.readEvent(buf, off, pending)
			off += size
			// The root itself gone (a folder removed, a test's TempDir): nothing
			// more can be heard, so the watch lets its descriptor go.
			if rootGone {
				go w.stop()
			}
		}
	}
}

// readEvent reads the inotify event at off in buf and adds what changed to pending. It
// gives the length of the event, and whether it says the watch's root itself is gone.
func (w *folderWatch) readEvent(buf []byte, off int, pending map[string]bool) (int, bool) {
	ev := (*unix.InotifyEvent)(unsafePointer(&buf[off]))
	nameLen := int(ev.Len)
	w.mu.Lock()
	dir := w.paths[int(ev.Wd)]
	w.mu.Unlock()
	if dir != "" {
		name := ""
		if nameLen > 0 {
			name = cString(buf[off+unix.SizeofInotifyEvent : off+unix.SizeofInotifyEvent+nameLen])
		}
		if w.files && name != "" {
			pending[filepath.Join(dir, name)] = true
		} else {
			pending[dir] = true
		}
		if ev.Mask&unix.IN_ISDIR != 0 && ev.Mask&(unix.IN_CREATE|unix.IN_MOVED_TO) != 0 && name != "" {
			_ = w.addTree(filepath.Join(dir, name)) // only the root says an error; a folder under it is logged
		}
	}
	rootGone := dir != "" && ev.Mask&unix.IN_DELETE_SELF != 0 && dir == w.root
	return unix.SizeofInotifyEvent + nameLen, rootGone
}

func cString(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}

// stop ends the watch: read sees it within its 50 ms nap and closes the descriptor.
func (w *folderWatch) stop() {
	w.once.Do(func() { close(w.done) })
}

// readFlush tells what changed in pending, in order, when there is any. It gives the
// pending set to use next.
func (w *folderWatch) readFlush(pending map[string]bool) map[string]bool {
	if len(pending) == 0 {
		return pending
	}
	folders := make([]string, 0, len(pending))
	for f := range pending {
		folders = append(folders, f)
	}
	sort.Strings(folders)
	w.changed(folders)
	return map[string]bool{}
}
