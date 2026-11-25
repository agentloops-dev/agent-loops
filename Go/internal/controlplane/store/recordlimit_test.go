package store

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// #6908: each kind of the control plane's stores takes a record at its largest size and
// refuses one a byte over it, with what it has as it was: alone, again (a replay) and in a
// roll-up.

// contents is everything a store holds.
func contents(t *testing.T, s Store) map[string]string {
	t.Helper()
	keys, err := s.List(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, k := range keys {
		o, err := s.Get(context.Background(), k.Key)
		if err != nil {
			t.Fatal(err)
		}
		out[k.Key] = string(o.Data)
	}
	return out
}

// refusedAtLimit is that write is refused as over limit, and the store is as it was.
func refusedAtLimit(t *testing.T, s Store, limit int64, write func() error) {
	t.Helper()
	before := contents(t, s)
	err := write()
	var l *LimitError
	if !errors.As(err, &l) || l.Limit != limit || l.Size <= limit || l.Name == "" {
		t.Fatalf("not refused as over %d: %v", limit, err)
	}
	if after := contents(t, s); !reflect.DeepEqual(before, after) {
		t.Fatal("a refused write changed the store")
	}
}

func TestRecordLimitOfALog(t *testing.T) {
	t.Parallel()
	ctx, s := context.Background(), NewMemory()
	const limit = 1024
	log, err := OpenLog(Spec{Name: "l", Kind: "log", Scope: "o", Bounds: Bounds{MaxRecordBytes: limit, MaxCount: 10, MaxAge: time.Hour}}, s)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC)
	// What is stored counts, not only the value in it, which it holds in base64.
	n := 1
	for ; log.Append(ctx, "a", at, make([]byte, n)) == nil; n++ {
		if err := s.Delete(ctx, log.(*logStore).key(at, "a")); err != nil {
			t.Fatal(err)
		}
	}
	if n < limit/2 || n > limit {
		t.Fatalf("the first refused is %d bytes", n)
	}
	for range 2 { // and the replay
		if err := log.Append(ctx, "a", at, make([]byte, n-1)); err != nil {
			t.Fatal(err)
		}
	}
	for _, data := range contents(t, s) {
		if len(data) > limit {
			t.Fatalf("%d bytes are stored", len(data))
		}
	}
	for _, id := range []string{"a", "b"} {
		refusedAtLimit(t, s, limit, func() error { return log.Append(ctx, id, at, make([]byte, n)) })
		refusedAtLimit(t, s, limit, func() error { return log.Append(ctx, id, at, make([]byte, limit+1)) })
	}
}

func TestRecordLimitOfADayLog(t *testing.T) {
	t.Parallel()
	ctx, s := context.Background(), NewMemory()
	l := dayLog(t, s, 10, nil)
	at := time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC)
	for range 2 { // and the replay
		if err := l.Append(ctx, "a", at, make([]byte, 64)); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"a", "b"} {
		refusedAtLimit(t, s, 64, func() error { return l.Append(ctx, id, at, make([]byte, 65)) })
	}
}

func timeSeries(t *testing.T, s Store, limit int64) TimeSeries {
	t.Helper()
	ts, err := OpenTimeSeries(Spec{Name: "t", Kind: "time series", Scope: "o", Bounds: Bounds{
		MaxRecordBytes: limit, MaxCount: 100, MaxAge: 90 * 24 * time.Hour, DetailAge: time.Hour}}, s)
	if err != nil {
		t.Fatal(err)
	}
	return ts
}

func TestRecordLimitOfATimeSeries(t *testing.T) {
	t.Parallel()
	ctx, s := context.Background(), NewMemory()
	const limit = 256
	ts := timeSeries(t, s, limit)
	at := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	point := func(n int) Point { return Point{Series: "s", ID: strings.Repeat("x", n), At: at, Value: 1} }
	n := 1
	for ; ts.Append(ctx, point(n)) == nil; n++ {
	}
	for range 2 { // and the replay
		if err := ts.Append(ctx, point(n-1)); err != nil {
			t.Fatal(err)
		}
	}
	refusedAtLimit(t, s, limit, func() error { return ts.Append(ctx, point(n)) })
	for _, data := range contents(t, s) {
		if len(data) > limit {
			t.Fatalf("%d bytes are stored", len(data))
		}
	}
}

// A point that a time series took rolls up: its series' name is in the day's record too.
func TestRecordLimitOfATimeSeriesRollup(t *testing.T) {
	t.Parallel()
	ctx, s := context.Background(), NewMemory()
	ts := timeSeries(t, s, 256)
	at := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	if err := ts.Append(ctx, Point{Series: strings.Repeat("s", 300), ID: "a", At: at, Value: 1}); err != nil {
		return // a name that cannot roll up is refused at the start
	}
	if err := ts.Rollup(ctx, at.Add(24*time.Hour), time.Hour); err != nil {
		t.Fatalf("a point that was taken does not roll up: %v", err)
	}
}

// A series whose points cannot roll up (an older build took them) or be read, and one whose sum
// is past the largest number, do not stop the others.
func TestASeriesThatCannotRollUpDoesNotStopTheOthers(t *testing.T) {
	t.Parallel()
	ctx, s := context.Background(), NewMemory()
	at := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	long, ts := Point{Series: strings.Repeat("a", 300), ID: "a", At: at, Value: 1}, timeSeries(t, s, 256)
	if err := timeSeries(t, s, 4096).Append(ctx, long); err != nil {
		t.Fatal(err)
	}
	unread := collectionPrefix(ts.(*timeSeriesStore).spec) + "detail/" + idKey("b") + "/20261101T000000.000000000Z/x"
	if _, err := s.Put(ctx, unread, []byte("{"), Absent); err != nil {
		t.Fatal(err)
	}
	for _, p := range []Point{{Series: "huge", ID: "a", At: at, Value: 1.7e308}, {Series: "huge", ID: "b", At: at, Value: 1.7e308}, {Series: "s", ID: "a", At: at, Value: 1}} {
		if err := ts.Append(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	var limit *LimitError
	if err := ts.Rollup(ctx, at.Add(24*time.Hour), time.Hour); !errors.As(err, &limit) {
		t.Fatalf("the series that cannot roll up is not said: %v", err)
	}
	for _, series := range []string{"huge", "s"} {
		if got, _, err := ts.Range(ctx, series, at, at.Add(24*time.Hour), "day", "", 10); err != nil || len(got) != 1 {
			t.Fatalf("%s did not roll up: %v %v", series, got, err)
		}
	}
	// What could not roll up is still there.
	if got, _, err := ts.Range(ctx, long.Series, at, at.Add(time.Hour), "hour", "", 10); err != nil || len(got) != 1 {
		t.Fatalf("%v %v", got, err)
	}
	if o, err := s.Get(ctx, unread); err != nil || o == nil {
		t.Fatal("a point that cannot be read went")
	}
}
