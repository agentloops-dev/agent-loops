package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Bounds declares the limits for one log or time-series collection. One of
// MaxCount and MaxBytes must be set. MaxRecordBytes is always required.
type Bounds struct {
	MaxRecordBytes int64
	MaxCount       int64
	MaxBytes       int64
	MaxAge         time.Duration
	DetailAge      time.Duration
	SweepRequested func(context.Context)
}

// Spec binds a kind adapter to a stable inventory id and an owner scope.
type Spec struct {
	Name, Kind, Scope string
	Bounds            Bounds
}

// LimitError reports a record that exceeds the declared per-record limit.
type LimitError struct {
	Name  string
	Size  int64
	Limit int64
}

func (e *LimitError) Error() string {
	return fmt.Sprintf("%s record is %d bytes; limit is %d", e.Name, e.Size, e.Limit)
}

func validateSpec(spec Spec, kind string) error {
	b := spec.Bounds
	if spec.Name == "" || spec.Kind != kind || spec.Scope == "" {
		return fmt.Errorf("%s store requires a name, %q kind, and scope", kind, kind)
	}
	if b.MaxRecordBytes <= 0 || (b.MaxCount <= 0 && b.MaxBytes <= 0) || b.MaxCount < 0 || b.MaxBytes < 0 || b.MaxAge <= 0 || b.MaxAge > 90*24*time.Hour {
		return fmt.Errorf("%s has invalid bounds", spec.Name)
	}
	if b.MaxCount > 0 && b.MaxBytes == 0 && b.MaxCount*b.MaxRecordBytes <= 0 {
		return fmt.Errorf("%s count bound overflows", spec.Name)
	}
	return nil
}

func collectionPrefix(spec Spec) string {
	return "kinds/" + base64.RawURLEncoding.EncodeToString([]byte(spec.Scope)) + "/" + base64.RawURLEncoding.EncodeToString([]byte(spec.Name)) + "/"
}

func idKey(id string) string { return base64.RawURLEncoding.EncodeToString([]byte(id)) }

type storedRecord struct {
	ID    string    `json:"id"`
	At    time.Time `json:"at"`
	Value []byte    `json:"value"`
}

// Log is an append-only, paged log. Cursor is opaque to callers.
type Log interface {
	Append(context.Context, string, time.Time, []byte) error
	Read(context.Context, string, int) ([]LogRecord, string, error)
	Trim(context.Context, time.Time) error
}

type LogRecord struct {
	ID, Cursor string
	At         time.Time
	Value      []byte
}

type logStore struct {
	store Store
	spec  Spec
}

// OpenLog creates a log over the conditional key-object store. The key layout is
// private to this adapter so callers can move to a stream backend without changes.
func OpenLog(spec Spec, backend Store) (Log, error) {
	if backend == nil {
		return nil, errors.New("log backend is required")
	}
	if err := validateSpec(spec, "log"); err != nil {
		return nil, err
	}
	return &logStore{store: backend, spec: spec}, nil
}

func (l *logStore) Append(ctx context.Context, id string, at time.Time, value []byte) error {
	if id == "" || at.IsZero() {
		return errors.New("log event id and time are required")
	}
	if int64(len(value)) > l.spec.Bounds.MaxRecordBytes {
		return &LimitError{Name: l.spec.Name, Size: int64(len(value)), Limit: l.spec.Bounds.MaxRecordBytes}
	}
	rec := storedRecord{ID: id, At: at.UTC(), Value: append([]byte(nil), value...)}
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if int64(len(data)) > l.spec.Bounds.MaxRecordBytes {
		return &LimitError{Name: l.spec.Name, Size: int64(len(data)), Limit: l.spec.Bounds.MaxRecordBytes}
	}
	key := l.key(at, id)
	if _, err = l.store.Put(ctx, key, data, Absent); IsConflict(err) {
		old, getErr := l.store.Get(ctx, key)
		if getErr != nil {
			return getErr
		}
		if old != nil && string(old.Data) == string(data) {
			return nil
		}
		return err
	}
	if err == nil {
		l.requestSweep(ctx)
	}
	return err
}

func (l *logStore) key(at time.Time, id string) string {
	return collectionPrefix(l.spec) + at.UTC().Format("20060102T150405.000000000Z") + "/" + idKey(id)
}

func (l *logStore) Read(ctx context.Context, cursor string, limit int) ([]LogRecord, string, error) {
	if limit <= 0 {
		return nil, "", errors.New("log page limit must be positive")
	}
	if limit > 1000 {
		limit = 1000
	}
	keys, err := l.store.List(ctx, collectionPrefix(l.spec))
	if err != nil {
		return nil, "", err
	}
	start := 0
	if cursor != "" {
		for start < len(keys) && keys[start].Key <= cursor {
			start++
		}
	}
	out := make([]LogRecord, 0, limit)
	for _, k := range keys[start:] {
		if len(out) == limit {
			break
		}
		obj, e := l.store.Get(ctx, k.Key)
		if e != nil {
			return nil, "", e
		}
		if obj == nil {
			continue
		}
		var rec storedRecord
		if e := json.Unmarshal(obj.Data, &rec); e != nil {
			return nil, "", fmt.Errorf("decode log record: %w", e)
		}
		out = append(out, LogRecord{ID: rec.ID, Cursor: k.Key, At: rec.At, Value: append([]byte(nil), rec.Value...)})
	}
	next := ""
	if len(out) > 0 {
		next = out[len(out)-1].Cursor
	}
	return out, next, nil
}

func (l *logStore) Trim(ctx context.Context, before time.Time) error {
	return trimStore(ctx, l.store, collectionPrefix(l.spec), l.spec.Bounds, before)
}

func (l *logStore) requestSweep(ctx context.Context) {
	if l.spec.Bounds.SweepRequested != nil {
		l.spec.Bounds.SweepRequested(ctx)
	}
}

// Point is one time-series measurement. Value is numeric so daily roll-ups have
// a deterministic sum and count and can be retried safely.
type Point struct {
	Series, ID string
	At         time.Time
	Value      float64
	Count      int64
}

type Aggregate struct {
	Series string
	Day    time.Time
	Sum    float64
	Count  int64
}

// TimeSeries supports idempotent append, bounded range reads, and safe daily roll-up.
type TimeSeries interface {
	Append(context.Context, Point) error
	Range(context.Context, string, time.Time, time.Time, string, string, int) ([]Point, string, error)
	Rollup(context.Context, time.Time, time.Duration) error
}

type timeSeriesStore struct {
	store Store
	spec  Spec
}

func OpenTimeSeries(spec Spec, backend Store) (TimeSeries, error) {
	if backend == nil {
		return nil, errors.New("time-series backend is required")
	}
	if err := validateSpec(spec, "time series"); err != nil {
		return nil, err
	}
	if spec.Bounds.DetailAge <= 0 || spec.Bounds.DetailAge > 7*24*time.Hour {
		return nil, errors.New("time-series detail age must be at most 168 hours")
	}
	return &timeSeriesStore{store: backend, spec: spec}, nil
}

func (s *timeSeriesStore) Append(ctx context.Context, p Point) error {
	if p.Series == "" || p.ID == "" || p.At.IsZero() {
		return errors.New("time-series point requires series, id, and time")
	}
	series := idKey(p.Series)
	rec := storedRecord{ID: p.ID, At: p.At.UTC()}
	value, err := json.Marshal(p.Value)
	if err != nil {
		return err
	}
	rec.Value = value
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if int64(len(data)) > s.spec.Bounds.MaxRecordBytes {
		return &LimitError{Name: s.spec.Name, Size: int64(len(data)), Limit: s.spec.Bounds.MaxRecordBytes}
	}
	key := collectionPrefix(s.spec) + "detail/" + series + "/" + p.At.UTC().Format("20060102T150405.000000000Z") + "/" + idKey(p.ID)
	_, err = s.store.Put(ctx, key, data, Absent)
	if IsConflict(err) {
		old, getErr := s.store.Get(ctx, key)
		if getErr != nil {
			return getErr
		}
		if old != nil && string(old.Data) == string(data) {
			return nil
		}
	}
	if err == nil && s.spec.Bounds.SweepRequested != nil {
		s.spec.Bounds.SweepRequested(ctx)
	}
	return err
}

func (s *timeSeriesStore) Range(ctx context.Context, series string, from, to time.Time, granularity, cursor string, limit int) ([]Point, string, error) {
	if series == "" || !to.After(from) || limit <= 0 {
		return nil, "", errors.New("invalid time-series range")
	}
	if limit > 1000 {
		limit = 1000
	}
	prefix := collectionPrefix(s.spec) + "detail/" + idKey(series) + "/"
	if granularity == "day" {
		prefix = collectionPrefix(s.spec) + "daily/" + idKey(series) + "/"
	}
	if granularity != "hour" && granularity != "day" {
		return nil, "", errors.New("granularity must be hour or day")
	}
	keys, err := s.store.List(ctx, prefix)
	if err != nil {
		return nil, "", err
	}
	out := make([]Point, 0, limit)
	next := ""
	for _, k := range keys {
		if cursor != "" && k.Key <= cursor {
			continue
		}
		o, e := s.store.Get(ctx, k.Key)
		if e != nil {
			return nil, "", e
		}
		if o == nil {
			continue
		}
		var point Point
		if granularity == "day" {
			var rec storedRecord
			if e = json.Unmarshal(o.Data, &rec); e != nil {
				return nil, "", e
			}
			var aggregate Aggregate
			if e = json.Unmarshal(rec.Value, &aggregate); e != nil {
				return nil, "", e
			}
			point = Point{Series: series, ID: aggregate.Day.Format("2006-01-02"), At: aggregate.Day, Value: aggregate.Sum}
			point.Count = aggregate.Count
		} else {
			var rec storedRecord
			if e = json.Unmarshal(o.Data, &rec); e != nil {
				return nil, "", e
			}
			point = Point{Series: series, ID: rec.ID, At: rec.At, Count: 1}
			if e = json.Unmarshal(rec.Value, &point.Value); e != nil {
				return nil, "", e
			}
		}
		lower, upper := from, to
		if granularity == "day" {
			lower = time.Date(from.UTC().Year(), from.UTC().Month(), from.UTC().Day(), 0, 0, 0, 0, time.UTC)
			upper = time.Date(to.UTC().Year(), to.UTC().Month(), to.UTC().Day(), 0, 0, 0, 0, time.UTC)
		}
		if point.At.Before(lower) || !point.At.Before(upper) {
			continue
		}
		out = append(out, point)
		next = k.Key
		if len(out) >= limit {
			break
		}
	}
	return out, next, nil
}

func (s *timeSeriesStore) Rollup(ctx context.Context, before time.Time, interval time.Duration) error {
	if interval <= 0 {
		return errors.New("roll-up interval must be positive")
	}
	prefix := collectionPrefix(s.spec) + "detail/"
	keys, err := s.store.List(ctx, prefix)
	if err != nil {
		return err
	}
	type bucket struct {
		series string
		day    time.Time
		sum    float64
		count  int64
		keys   []string
	}
	buckets := map[string]*bucket{}
	for _, k := range keys {
		obj, e := s.store.Get(ctx, k.Key)
		if e != nil {
			return e
		}
		if obj == nil {
			continue
		}
		var rec storedRecord
		if e = json.Unmarshal(obj.Data, &rec); e != nil {
			return e
		}
		if rec.At.After(before.Add(-s.spec.Bounds.DetailAge)) || rec.At.After(before) {
			continue
		}
		remainder := strings.TrimPrefix(k.Key, prefix)
		parts := strings.Split(remainder, "/")
		if len(parts) < 3 {
			continue
		}
		var n float64
		if e = json.Unmarshal(rec.Value, &n); e != nil {
			return e
		}
		day := time.Date(rec.At.UTC().Year(), rec.At.UTC().Month(), rec.At.UTC().Day(), 0, 0, 0, 0, time.UTC)
		id := parts[0] + "/" + day.Format("20060102")
		b := buckets[id]
		if b == nil {
			b = &bucket{series: parts[0], day: day}
			buckets[id] = b
		}
		b.sum += n
		b.count++
		b.keys = append(b.keys, k.Key)
	}
	ids := make([]string, 0, len(buckets))
	for id := range buckets {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		b := buckets[id]
		data, e := json.Marshal(Aggregate{Series: b.series, Day: b.day, Sum: b.sum, Count: b.count})
		if e != nil {
			return e
		}
		key := collectionPrefix(s.spec) + "daily/" + b.series + "/" + b.day.Format("20060102")
		stored, e := json.Marshal(storedRecord{ID: "daily", At: b.day, Value: data})
		if e != nil {
			return e
		}
		if int64(len(stored)) > s.spec.Bounds.MaxRecordBytes {
			return &LimitError{Name: s.spec.Name, Size: int64(len(stored)), Limit: s.spec.Bounds.MaxRecordBytes}
		}
		_, e = s.store.Put(ctx, key, stored, Absent)
		if IsConflict(e) {
			existing, getErr := s.store.Get(ctx, key)
			if getErr != nil {
				return getErr
			}
			if existing == nil {
				return e
			}
			// A previous attempt may have committed the aggregate and deleted only
			// some detail points. The same day key proves the destination is durable;
			// finish deleting the remaining source points without adding them twice.
			e = nil
		}
		if e != nil {
			return e
		}
		for _, detail := range b.keys {
			if e = s.store.Delete(ctx, detail); e != nil {
				return e
			}
		}
	}
	return trimStore(ctx, s.store, collectionPrefix(s.spec)+"daily/", s.spec.Bounds, before.Add(-s.spec.Bounds.MaxAge))
}

func trimStore(ctx context.Context, backend Store, prefix string, bounds Bounds, before time.Time) error {
	keys, err := backend.List(ctx, prefix)
	if err != nil {
		return err
	}
	type item struct {
		key  string
		at   time.Time
		size int64
	}
	items := make([]item, 0, len(keys))
	var bytes int64
	for _, k := range keys {
		o, e := backend.Get(ctx, k.Key)
		if e != nil {
			return e
		}
		if o == nil {
			continue
		}
		var rec storedRecord
		if e = json.Unmarshal(o.Data, &rec); e != nil {
			return e
		}
		items = append(items, item{k.Key, rec.At, int64(len(o.Data))})
		bytes += int64(len(o.Data))
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].at.Equal(items[j].at) {
			return items[i].key < items[j].key
		}
		return items[i].at.Before(items[j].at)
	})
	var count int64 = int64(len(items))
	for _, it := range items {
		tooOld := !before.IsZero() && it.at.Before(before)
		overCount := bounds.MaxCount > 0 && count > bounds.MaxCount
		overBytes := bounds.MaxBytes > 0 && bytes > bounds.MaxBytes
		if !tooOld && !overCount && !overBytes {
			break
		}
		if e := backend.Delete(ctx, it.key); e != nil {
			return e
		}
		count--
		bytes -= it.size
	}
	return nil
}
