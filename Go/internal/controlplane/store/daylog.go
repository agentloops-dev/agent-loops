package store

import (
	"context"
	"errors"
	"iter"
	"path"
	"slices"
	"strings"
	"sync"
	"time"
)

// recountAfter is how long a sweep goes by the counts of the sweep before: the rounds of one
// hour's sweep list no day twice for its count.
const recountAfter = 5 * time.Minute

const dayLayout = "2006-01-02"

// DayLog is a log whose records are one object each, under <prefix><day>/<id> (#6755). An id
// may name folders (a partition), and its last part sorts by time, so that the records of a
// day, or of one partition of a day, are read with no list of the others. Its sweep evicts
// the oldest records by age and by count, from the keys alone.
type DayLog struct {
	store  Store
	spec   Spec
	prefix string

	mu sync.Mutex
	// counts is each day's records as last listed; nil before the first sweep. total is
	// their sum, and appended the records this copy appended since.
	counts          map[string]int64
	total, appended int64
	refreshed       time.Time
	requested       bool
}

var _ Log = (*DayLog)(nil)

// OpenDayLog is the log of spec under prefix, which ends in "/". It needs a count limit:
// its sweep does not read a record's size.
func OpenDayLog(spec Spec, prefix string, backend Store) (*DayLog, error) {
	if backend == nil || !strings.HasSuffix(prefix, "/") {
		return nil, errors.New("a day log needs a backend and a prefix that ends in /")
	}
	if err := validateSpec(spec, "log"); err != nil {
		return nil, err
	}
	if spec.Bounds.MaxCount <= 0 {
		return nil, errors.New("a day log needs a count limit")
	}
	return &DayLog{store: backend, spec: spec, prefix: prefix}, nil
}

// Append keeps one record. Appending the same one again changes nothing. When the log goes
// past twice its limit, it asks once for a sweep before the hour.
func (l *DayLog) Append(ctx context.Context, id string, at time.Time, value []byte) error {
	if id == "" || at.IsZero() {
		return errors.New("log event id and time are required")
	}
	if size := int64(len(value)); size > l.spec.Bounds.MaxRecordBytes {
		return &LimitError{Name: l.spec.Name, Size: size, Limit: l.spec.Bounds.MaxRecordBytes}
	}
	key := l.prefix + at.UTC().Format(dayLayout) + "/" + id
	if _, err := l.store.Put(ctx, key, value, Absent); err != nil {
		if !IsConflict(err) {
			return err
		}
		old, getErr := l.store.Get(ctx, key)
		if getErr != nil {
			return getErr
		}
		if old != nil && string(old.Data) == string(value) {
			return nil
		}
		return err
	}
	l.mu.Lock()
	l.appended++
	ask := !l.requested && l.total+l.appended > 2*l.spec.Bounds.MaxCount && l.spec.Bounds.SweepRequested != nil
	if ask {
		l.requested = true
	}
	l.mu.Unlock()
	if ask {
		l.spec.Bounds.SweepRequested(ctx)
	}
	return nil
}

// Range is the records from since's day to until's, by time: oldest first, or newest first.
// partition, when there is one, is the folder of the ids to read, and ends in "/". A record
// is read as it is asked for, so a caller that stops reads no more.
func (l *DayLog) Range(ctx context.Context, since, until time.Time, partition string, newestFirst bool) iter.Seq2[LogRecord, error] {
	var days []string
	last := until.UTC().Format(dayLayout)
	for d := since.UTC().Truncate(24 * time.Hour); d.Format(dayLayout) <= last; d = d.AddDate(0, 0, 1) {
		days = append(days, d.Format(dayLayout))
	}
	if newestFirst {
		slices.Reverse(days)
	}
	return l.records(ctx, days, partition, newestFirst)
}

// All is every record, oldest day first.
func (l *DayLog) All(ctx context.Context) iter.Seq2[LogRecord, error] {
	return func(yield func(LogRecord, error) bool) {
		days, err := Folders(ctx, l.store, l.prefix)
		if err != nil {
			yield(LogRecord{}, err)
			return
		}
		for r, err := range l.records(ctx, days, "", false) {
			if !yield(r, err) {
				return
			}
		}
	}
}

func (l *DayLog) records(ctx context.Context, days []string, partition string, newestFirst bool) iter.Seq2[LogRecord, error] {
	return func(yield func(LogRecord, error) bool) {
		for _, day := range days {
			keys, err := l.day(ctx, day, partition)
			if err != nil {
				yield(LogRecord{}, err)
				return
			}
			if newestFirst {
				slices.Reverse(keys)
			}
			for _, k := range keys {
				object, err := l.store.Get(ctx, k.Key)
				if err != nil {
					yield(LogRecord{}, err)
					return
				}
				if object == nil {
					continue
				}
				if !yield(l.record(day, k.Key, object.Data), nil) {
					return
				}
			}
		}
	}
}

// record is a record as a caller sees it; At is its day, for the time is in its value.
func (l *DayLog) record(day, key string, data []byte) LogRecord {
	at, _ := time.Parse(dayLayout, day)
	return LogRecord{ID: strings.TrimPrefix(key, l.prefix), Cursor: cursorOf(day, key), At: at, Value: data}
}

// cursorOf is a key's place: by day, then by the last part of its id, then by all of it.
func cursorOf(day, key string) string { return day + "/" + path.Base(key) + "/" + key }

// day is the keys of a day, or of one partition of it, oldest first.
func (l *DayLog) day(ctx context.Context, day, partition string) ([]Key, error) {
	keys, err := l.store.List(ctx, l.prefix+day+"/"+partition)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(keys, func(a, b Key) int { return strings.Compare(cursorOf(day, a.Key), cursorOf(day, b.Key)) })
	return keys, nil
}

// Read is the records after cursor, at most limit, oldest first.
func (l *DayLog) Read(ctx context.Context, cursor string, limit int) ([]LogRecord, string, error) {
	if limit <= 0 {
		return nil, "", errors.New("log page limit must be positive")
	}
	limit = min(limit, 1000)
	days, err := Folders(ctx, l.store, l.prefix)
	if err != nil {
		return nil, "", err
	}
	from, _, _ := strings.Cut(cursor, "/")
	var out []LogRecord
	for _, day := range days {
		if day < from {
			continue
		}
		keys, err := l.day(ctx, day, "")
		if err != nil {
			return nil, "", err
		}
		for _, k := range keys {
			if cursorOf(day, k.Key) <= cursor {
				continue
			}
			object, err := l.store.Get(ctx, k.Key)
			if err != nil {
				return nil, "", err
			}
			if object == nil {
				continue
			}
			out = append(out, l.record(day, k.Key, object.Data))
			if len(out) == limit {
				return out, out[limit-1].Cursor, nil
			}
		}
	}
	if len(out) > 0 {
		cursor = out[len(out)-1].Cursor
	}
	return out, cursor, nil
}

// Trim evicts every day before before's, and then the oldest records over the count limit.
func (l *DayLog) Trim(ctx context.Context, before time.Time) error {
	const most = 1000
	for {
		n, err := l.Sweep(ctx, time.Now(), before, most)
		if err != nil || n < most {
			return err
		}
	}
}

// Sweep is one round of Trim: it deletes about most records at most, and says how many.
// It lists every record the first time only; then the days that have gone, the oldest day
// while over the limit, and the days that may have new records (#2053).
func (l *DayLog) Sweep(ctx context.Context, now, before time.Time, most int) (int, error) {
	l.mu.Lock()
	recount := l.counts == nil || l.requested || now.Sub(l.refreshed) >= recountAfter
	l.mu.Unlock()
	if recount {
		if err := l.recount(ctx, now); err != nil {
			return 0, err
		}
	}
	cutoff := before.UTC().Format(dayLayout)
	deleted := 0
	for _, day := range l.days() {
		old := day < cutoff
		if deleted >= most || !old && l.over() <= 0 {
			break
		}
		n, err := l.evict(ctx, day, old, most-deleted)
		deleted += n
		if err != nil {
			return deleted, err
		}
	}
	return deleted, nil
}

// evict deletes the oldest records of a day, about most at most: all of them when old, else
// those over the limit. The day is listed, so what another copy's sweep deleted is not
// counted again.
func (l *DayLog) evict(ctx context.Context, day string, old bool, most int) (int, error) {
	keys, err := l.day(ctx, day, "")
	if err != nil {
		return 0, err
	}
	l.setCount(day, int64(len(keys)))
	n := min(len(keys), most)
	if !old {
		n = int(min(int64(n), max(l.over(), 0)))
		// Records of one second go together: they have no order that a reader can rely on.
		for n > 0 && n < len(keys) && second(keys[n].Key) != "" && second(keys[n].Key) == second(keys[n-1].Key) {
			n++
		}
	}
	for i, k := range keys[:n] {
		if err := l.store.Delete(ctx, k.Key); err != nil {
			l.setCount(day, int64(len(keys)-i))
			return i, err
		}
	}
	l.setCount(day, int64(len(keys)-n))
	return n, nil
}

// second is the second in a key whose last part starts with 13 digits of milliseconds.
func second(key string) string {
	ms, _, _ := strings.Cut(path.Base(key), "-")
	if len(ms) != 13 {
		return ""
	}
	return ms[:10]
}

func (l *DayLog) recount(ctx context.Context, now time.Time) error {
	l.mu.Lock()
	first, from := l.counts == nil, l.refreshed
	l.appended, l.requested = 0, false
	l.mu.Unlock()
	if first {
		keys, err := l.store.List(ctx, l.prefix)
		if err != nil {
			return err
		}
		counts := map[string]int64{}
		for _, k := range keys {
			day, _, _ := strings.Cut(strings.TrimPrefix(k.Key, l.prefix), "/")
			if _, err := time.Parse(dayLayout, day); err == nil {
				counts[day]++
			}
		}
		l.mu.Lock()
		defer l.mu.Unlock()
		l.counts, l.refreshed, l.total = counts, now, int64(0)
		for _, n := range counts {
			l.total += n
		}
		return nil
	}
	// A record can land a while after its time, so the day before is counted again too.
	for d := from.UTC().Truncate(24*time.Hour).AddDate(0, 0, -1); !d.After(now); d = d.AddDate(0, 0, 1) {
		day := d.Format(dayLayout)
		keys, err := l.store.List(ctx, l.prefix+day+"/")
		if err != nil {
			return err
		}
		l.setCount(day, int64(len(keys)))
	}
	l.mu.Lock()
	l.refreshed = now
	l.mu.Unlock()
	return nil
}

func (l *DayLog) setCount(day string, n int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.total += n - l.counts[day]
	if n == 0 {
		delete(l.counts, day)
	} else {
		l.counts[day] = n
	}
}

func (l *DayLog) over() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.total - l.spec.Bounds.MaxCount
}

func (l *DayLog) days() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	days := make([]string, 0, len(l.counts))
	for day := range l.counts {
		days = append(days, day)
	}
	slices.Sort(days)
	return days
}
