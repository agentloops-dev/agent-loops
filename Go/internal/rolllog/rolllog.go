// Package rolllog is the one log roller (#1555): a file kept open for appending that,
// past its limit or its period, becomes <name>.previous.log, as DaemonLog and ControlLog do.
// agentsd's daemon.log, its hosted MCP servers' logs and the control plane's control.log go
// through it.
//
// A line is kept at most 7 days (#6735, #6736, #6901), without reading one, because what a
// crash or an MCP server writes has no time on it. Time is cut into fixed periods, a file
// holds the lines of one period only, and the previous file goes when its period is not the
// last one. A file's period is its last write's, which every file system keeps.
package rolllog

import (
	"errors"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	// Limit is the size daemon.log and control.log roll at: two files are under 10 MB.
	Limit = 4 * 1024 * 1024
	// period is under half of 7 days, so two periods and an hourly sweep are inside 7 days.
	period = 83 * time.Hour
)

func periodOf(t time.Time) int64 { return t.Unix() / int64(period/time.Second) }

// Expired says whether a file last written at holds only lines of before the last period, so
// its lines are past the age, or soon are. A time still to come is a wrong clock's.
func Expired(at, now time.Time) bool {
	age := periodOf(now) - periodOf(at)
	return age > 1 || age < 0
}

// File is a rolled log. Its path is looked at before each write, so a log rolled by
// another process (the supervisor and agentsd share daemon.log), moved or removed is
// opened again, and the other process's lines count towards the limit.
type File struct {
	mu    sync.Mutex
	path  string
	limit int64
	std   bool
	out   *os.File
	info  os.FileInfo
	stop  chan struct{}
	// period is that of the open file's lines.
	period int64
}

// CheckEvery is how often a watched log is looked at (#3687): what the runtime writes to the
// standard error it takes (a panic, a child's output) does not go through Write, so without it
// a log nothing else writes to would grow past its limit, and an idle one past its age.
var CheckEvery = 30 * time.Second

// Open opens path for appending, rolling it first when it is already past limit or its period. With
// std, the process's standard output and error go to the file too, opened again at each
// roll, so what the runtime itself writes (a panic) lands beside the lines.
func Open(path string, limit int64, std bool) (*File, error) {
	f := &File{path: path, limit: limit, std: std}
	if err := f.reopen(); err != nil {
		return nil, err
	}
	f.Check()
	if std {
		f.Watch()
	}
	return f, nil
}

// Append adds p to the log at path, which is open only for that.
func Append(path string, limit int64, p []byte) error {
	f, err := Open(path, limit, false)
	if err != nil {
		return err
	}
	_, err = f.Write(p)
	return errors.Join(err, f.Close())
}

// Watch checks the log every CheckEvery until it is closed. A log whose File another process
// is started with is not watched: a roll closes that file.
func (f *File) Watch() {
	f.stop = make(chan struct{})
	go f.watch(f.stop, time.NewTicker(CheckEvery))
}

func (f *File) watch(stop chan struct{}, t *time.Ticker) {
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			f.Check()
		}
	}
}

// Check rolls the log when it is past its limit, as what was written behind Write (the
// duplicated standard error) left it, or past its period, and removes a previous log past
// its age.
func (f *File) Check() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.out == nil {
		return
	}
	// A log that cannot be opened again keeps the open file.
	_ = f.ready(0)
	f.expire()
}

// ready makes the open file the one at path, with room for n more bytes of this period.
func (f *File) ready(n int64) error {
	now, err := os.Stat(f.path)
	if err != nil || !os.SameFile(now, f.info) {
		return f.reopen()
	}
	this := periodOf(time.Now())
	stale := f.period != this || periodOf(now.ModTime()) != this
	switch {
	case now.Size() == 0:
		f.period = this
	case stale || now.Size()+n > f.limit:
		f.roll()
	}
	return nil
}

// expire removes a previous log past its age.
func (f *File) expire() {
	previous := Previous(f.path)
	if fi, err := os.Stat(previous); err == nil && Expired(fi.ModTime(), time.Now()) {
		// The next check tries again.
		_ = os.Remove(previous)
	}
}

// Previous is where path goes when it rolls: daemon.log is daemon.previous.log.
func Previous(path string) string { return strings.TrimSuffix(path, ".log") + ".previous.log" }

// Write appends p, rolling first when p would take the log past its limit. What is larger
// than the limit goes in parts of it, so no file passes it.
func (f *File) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	written := 0
	for {
		part := p[written:min(len(p), written+int(f.limit))]
		// On a failure the open file stays; with none, the error is Write's.
		if err := f.ready(int64(len(part))); err != nil && f.out == nil {
			return written, err
		}
		if f.out == nil {
			return written, os.ErrClosed
		}
		n, err := f.out.Write(part)
		written += n
		if err != nil || written == len(p) {
			return written, err
		}
	}
}

// File is the open file, to hand to a child process as its output.
func (f *File) File() *os.File {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.out
}

// Close closes the file.
func (f *File) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stop != nil {
		close(f.stop)
		f.stop = nil
	}
	if f.out == nil {
		return nil
	}
	err := f.out.Close()
	f.out = nil
	return err
}

// roll makes the log the previous one, replacing the one before, and starts a new one. The
// lock is for another process that rolls it at the same time: the second finds it rolled, and
// does not put the new log in the place of the previous one.
func (f *File) roll() {
	// This is the log, so a failure has nowhere to be said: with no lock it rolls as it can, a
	// failed rename leaves the log growing until a later roll works, and a failed reopen keeps
	// the open file.
	fd := int(f.out.Fd())
	_ = syscall.Flock(fd, syscall.LOCK_EX)
	if now, err := os.Stat(f.path); err == nil && os.SameFile(now, f.info) {
		_ = os.Rename(f.path, Previous(f.path))
	}
	_ = syscall.Flock(fd, syscall.LOCK_UN)
	_ = f.reopen()
	f.expire()
}

func (f *File) reopen() error {
	// store-bounds: kind=log; size=8MiB (two files of 4MiB, and a write is cut to it); age=7d; applied=on write, at open and every 30 s
	out, err := os.OpenFile(f.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	info, err := out.Stat()
	if err != nil {
		out.Close()
		return err
	}
	if f.std {
		// A failure leaves stdout or stderr where it was, and this is the log, so there is
		// nowhere to say it.
		_ = dupTo(int(out.Fd()), 1)
		_ = dupTo(int(out.Fd()), 2)
	}
	if f.out != nil {
		f.out.Close()
	}
	f.out, f.info, f.period = out, info, periodOf(info.ModTime())
	return nil
}
