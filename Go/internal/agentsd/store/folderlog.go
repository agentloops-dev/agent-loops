package store

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/diskjson"
	kinds "github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
)

// FolderLog is the log kind (#6670) over one JSONL file in a folder: a conversation's
// transcript and its record of app tool calls (#6706). A record is a line, and it carries
// its own id and time. The file has no TTL, so the daemon's sweep calls Trim.
//
// A trim takes whole lines off the front and leaves a header line that says how many lines
// and bytes went. So a line's number and its place in bytes never change while it is kept:
// a client's page, a turn's start and said.json's place stay true across a trim.
type FolderLog struct {
	file string
	spec kinds.Spec
	// id and at of a record, read from its line. Not ok for a line that says no time.
	stamp func(line []byte) (id string, at time.Time, ok bool)
	// lock is held for an append and for the end of a trim, so neither loses the other's.
	lock sync.Locker
	// open opens the file to append to.
	open func(file string) (*os.File, error)
}

var _ kinds.Log = (*FolderLog)(nil)

// OpenFolderLog is the log at file. Nothing is read or made until it is used.
func OpenFolderLog(spec kinds.Spec, file string, lock sync.Locker, stamp func([]byte) (string, time.Time, bool)) (*FolderLog, error) {
	if err := spec.Validate("log"); err != nil {
		return nil, err
	}
	if file == "" || lock == nil || stamp == nil {
		return nil, errors.New("a folder log requires a file, a lock and a record's stamp")
	}
	return &FolderLog{file: file, spec: spec, stamp: stamp, lock: lock, open: openTranscript}, nil
}

// logHead is what trims have taken off the front of a log, and the bytes of the line that
// says so.
type logHead struct {
	lines int
	bytes int64
	size  int64
	// why is what took the lines: by age, by size, or both (#7405). An older header says neither.
	why TrimCause
}

// TrimCause is what a log's trims took lines for.
type TrimCause struct {
	Age  bool `json:"age,omitempty"`
	Size bool `json:"size,omitempty"`
}

type headLine struct {
	Trimmed struct {
		Lines int   `json:"lines"`
		Bytes int64 `json:"bytes"`
		TrimCause
	} `json:"trimmed"`
}

var headPrefix = []byte(`{"trimmed":{"lines":`)

// maxHead is more than a header line can be.
const maxHead = 128

func readHead(f io.ReaderAt) logHead {
	buf := make([]byte, maxHead)
	n, _ := f.ReadAt(buf, 0)
	end := bytes.IndexByte(buf[:n], '\n')
	if end < 0 || !bytes.HasPrefix(buf, headPrefix) {
		return logHead{}
	}
	var h headLine
	if json.Unmarshal(buf[:end], &h) != nil || h.Trimmed.Lines < 0 || h.Trimmed.Bytes < 0 {
		return logHead{}
	}
	return logHead{lines: h.Trimmed.Lines, bytes: h.Trimmed.Bytes, size: int64(end) + 1, why: h.Trimmed.TrimCause}
}

func headOf(lines int, size int64, why TrimCause) []byte {
	var h headLine
	h.Trimmed.Lines, h.Trimmed.Bytes, h.Trimmed.TrimCause = lines, size, why
	b, _ := json.Marshal(h) // whole numbers and booleans always encode
	return append(b, '\n')
}

// at is where in the file the place abs is, and abs is the place of a byte of the file.
func (h logHead) at(abs int64) int64   { return abs - h.bytes + h.size }
func (h logHead) abs(file int64) int64 { return max(0, file-h.size) + h.bytes }

// openLog is the file to read, its header, and its size as a place.
func (l *FolderLog) openLog() (*os.File, logHead, int64, error) {
	f, err := os.Open(l.file)
	if err != nil {
		return nil, logHead{}, 0, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close() // only read
		return nil, logHead{}, 0, err
	}
	h := readHead(f)
	return f, h, h.abs(info.Size()), nil
}

// Size is the log's end as a place: the bytes ever written to it.
func (l *FolderLog) Size() (int64, error) {
	f, _, size, err := l.openLog()
	if err != nil {
		return 0, err
	}
	return size, f.Close()
}

// count is the lines ever written to it.
func (l *FolderLog) count() int {
	f, h, size, err := l.openLog()
	if err != nil {
		return 0
	}
	defer f.Close()
	n := h.lines
	_ = scanLines(io.NewSectionReader(f, h.size, size-h.bytes), func([]byte) bool { n++; return true })
	return n
}

// scanLines calls see with each whole line and its newline until see says false. The bytes
// are only see's until it returns. What follows the last newline is not a line.
func scanLines(from io.Reader, see func(line []byte) bool) error {
	r := bufio.NewReaderSize(from, 1<<16)
	var long []byte
	for {
		chunk, err := r.ReadSlice('\n')
		if err == bufio.ErrBufferFull {
			long = append(long, chunk...)
			continue
		}
		line := chunk
		if len(long) > 0 {
			long = append(long, chunk...)
			line = long
		}
		if len(line) > 0 && line[len(line)-1] == '\n' && !see(line) {
			return nil
		}
		long = long[:0]
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// Append is kinds.Log's: one record, refused over the record limit. The same record as the
// last one is the retry of an append that was written, and is not written again.
func (l *FolderLog) Append(ctx context.Context, id string, at time.Time, value []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if id == "" || at.IsZero() {
		return errors.New("log event id and time are required")
	}
	if bytes.IndexByte(value, '\n') >= 0 {
		return errors.New("a folder log's record is one line")
	}
	line := append(append(make([]byte, 0, len(value)+1), value...), '\n')
	l.lock.Lock()
	defer l.lock.Unlock()
	endTornLine(l.file)
	if l.endsWith(line) {
		return nil
	}
	return l.write(line)
}

// endsWith says whether the last record is line.
func (l *FolderLog) endsWith(line []byte) bool {
	f, h, size, err := l.openLog()
	if err != nil {
		return false
	}
	defer f.Close()
	start := size - int64(len(line))
	if start < h.bytes {
		return false
	}
	last := make([]byte, len(line))
	if _, err := f.ReadAt(last, h.at(start)); err != nil || !bytes.Equal(last, line) {
		return false
	}
	if start == h.bytes {
		return true
	}
	before := make([]byte, 1)
	_, err = f.ReadAt(before, h.at(start)-1)
	return err == nil && before[0] == '\n'
}

// write appends whole lines, with lock held. A write the disk cut short is taken back, so
// the next line is never glued to half of this one. A log past twice its bytes asks for the
// sweep now rather than at the hour.
func (l *FolderLog) write(lines []byte) error {
	for rest := lines; len(rest) > 0; {
		end := bytes.IndexByte(rest, '\n')
		if end < 0 {
			return errors.New("a folder log is written a whole line at a time")
		}
		if int64(end) > l.spec.Bounds.MaxRecordBytes {
			return &kinds.LimitError{Name: l.spec.Name, Size: int64(end), Limit: l.spec.Bounds.MaxRecordBytes}
		}
		rest = rest[end+1:]
	}
	if err := fault(l.file, true, lines); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(l.file), 0o755); err != nil { //nolint:gosec // G301: the mode this folder always had
		return err
	}
	f, err := l.open(l.file)
	if err != nil {
		return err
	}
	defer f.Close()
	var size int64 = -1
	if info, err := f.Stat(); err == nil {
		size = info.Size()
	}
	if _, err := f.Write(lines); err != nil {
		if size >= 0 {
			if terr := f.Truncate(size); terr != nil {
				return fmt.Errorf("%w; a torn write could not be taken out: %w", err, terr)
			}
		}
		return err
	}
	b := l.spec.Bounds
	if b.SweepRequested != nil && size+int64(len(lines)) > 2*b.MaxBytes {
		b.SweepRequested(context.Background())
	}
	return nil
}

// Read is kinds.Log's: at most limit records after cursor, oldest first. A cursor holds
// across a trim: one before the oldest record kept reads on from there.
func (l *FolderLog) Read(ctx context.Context, cursor string, limit int) ([]kinds.LogRecord, string, error) {
	if limit <= 0 {
		return nil, "", errors.New("log page limit must be positive")
	}
	limit = min(limit, 1000)
	line, place, err := parseCursor(cursor)
	if err != nil {
		return nil, "", err
	}
	f, h, size, err := l.openLog()
	if os.IsNotExist(err) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	if place < h.bytes {
		line, place = h.lines, h.bytes
	}
	if place > size {
		return nil, "", errors.New("log cursor is past the end")
	}
	var out []kinds.LogRecord
	next := cursor
	err = scanLines(io.NewSectionReader(f, h.at(place), size-place), func(b []byte) bool {
		if ctx.Err() != nil || len(out) == limit {
			return false
		}
		line, place = line+1, place+int64(len(b))
		value := bytes.TrimSuffix(b, []byte{'\n'})
		if id, at, ok := l.stamp(value); ok {
			next = strconv.Itoa(line) + "." + strconv.FormatInt(place, 10)
			out = append(out, kinds.LogRecord{ID: id, Cursor: next, At: at, Value: bytes.Clone(value)})
		}
		return true
	})
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		return nil, "", err
	}
	return out, next, nil
}

func parseCursor(cursor string) (line int, place int64, err error) {
	if cursor == "" {
		return 0, 0, nil
	}
	a, b, _ := strings.Cut(cursor, ".")
	line, err = strconv.Atoi(a)
	if err == nil {
		place, err = strconv.ParseInt(b, 10, 64)
	}
	if err != nil || line < 0 || place < 0 {
		return 0, 0, errors.New("invalid log cursor")
	}
	return line, place, nil
}

// Trim is kinds.Log's: the records before before go, then the oldest until the log is
// within its bytes.
func (l *FolderLog) Trim(ctx context.Context, before time.Time) error {
	_, err := l.trim(ctx, before)
	return err
}

// trim is Trim, and says whether anything went.
func (l *FolderLog) trim(ctx context.Context, before time.Time) (bool, error) {
	f, h, size, err := l.openLog()
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	lines, cut, why, err := l.cut(ctx, f, h, size, before)
	if err != nil || cut == h.bytes {
		return false, err
	}
	// store-bounds: not a store: the log itself, trimmed, before it takes the old one's place
	tmp, err := os.CreateTemp(filepath.Dir(l.file), "."+filepath.Base(l.file)+".*.tmp")
	if err != nil {
		return false, err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	// The kept records are copied with no lock held, and then only what was appended since.
	why.Age, why.Size = why.Age || h.why.Age, why.Size || h.why.Size
	if _, err := tmp.Write(headOf(lines, cut, why)); err != nil {
		return false, err
	}
	if _, err := io.Copy(tmp, io.NewSectionReader(f, h.at(cut), size-cut)); err != nil {
		return false, err
	}
	if info, err := f.Stat(); err == nil {
		if err := tmp.Chmod(info.Mode().Perm()); err != nil {
			return false, err
		}
	}
	if SyncWrites {
		if err := tmp.Sync(); err != nil {
			return false, err
		}
	}
	l.lock.Lock()
	defer l.lock.Unlock()
	now, err := os.Stat(l.file)
	if os.IsNotExist(err) {
		return false, nil // deleted meanwhile
	}
	if err != nil {
		return false, err
	}
	// Some other file now (a restore's), or a torn write taken out: the next sweep looks again.
	if was, err := f.Stat(); err != nil || !os.SameFile(was, now) || h.abs(now.Size()) < size {
		return false, err
	}
	if _, err := io.Copy(tmp, io.NewSectionReader(f, h.at(size), h.abs(now.Size())-size)); err != nil {
		return false, err
	}
	if err := fault(l.file, false, nil); err != nil {
		return false, err
	}
	return true, os.Rename(tmp.Name(), l.file)
}

// cut is the lines and the place up to which a trim takes. A record is too old by its own
// time; a line with no time goes with the record before it. Records are in order, so the
// first record that stays ends the cut. why says what this cut takes lines for.
func (l *FolderLog) cut(ctx context.Context, f io.ReaderAt, h logHead, size int64, before time.Time) (int, int64, TrimCause, error) {
	lines, place := h.lines, h.bytes
	cutLines, cut := lines, place
	var why TrimCause
	over := int64(0)
	if most := l.spec.Bounds.MaxBytes; most > 0 {
		over = max(0, size-most)
	}
	err := scanLines(io.NewSectionReader(f, h.size, size-h.bytes), func(b []byte) bool {
		if ctx.Err() != nil {
			return false
		}
		tooBig := place < over
		timed := false
		if !tooBig {
			var at time.Time
			if _, at, timed = l.stamp(b); timed && !at.Before(before) {
				return false
			}
		}
		lines, place = lines+1, place+int64(len(b))
		if tooBig || timed {
			cutLines, cut = lines, place
			why.Size, why.Age = why.Size || tooBig, why.Age || timed
		}
		return true
	})
	if err == nil {
		err = ctx.Err()
	}
	return cutLines, cut, why, err
}

// RecordStamp is the id and the time of a record that has them as "id" and "at", as a
// transcript's entries and the record of app tool calls (which have no id) do.
func RecordStamp(line []byte) (string, time.Time, bool) {
	var r struct {
		ID string `json:"id"`
		At string `json:"at"`
	}
	if json.Unmarshal(line, &r) != nil {
		return "", time.Time{}, false
	}
	at, err := diskjson.ParseDate(r.At)
	return r.ID, at.Time(), err == nil
}
