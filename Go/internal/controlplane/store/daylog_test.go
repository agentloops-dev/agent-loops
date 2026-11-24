package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func dayLog(t *testing.T, s Store, count int64, sweep func(context.Context)) *DayLog {
	t.Helper()
	l, err := OpenDayLog(Spec{Name: "audit", Kind: "log", Scope: "organisation", Bounds: Bounds{
		MaxRecordBytes: 64, MaxCount: count, MaxAge: 90 * 24 * time.Hour, SweepRequested: sweep,
	}}, "v1/log/", s)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func put(t *testing.T, l *DayLog, at time.Time, name string) {
	t.Helper()
	if err := l.Append(context.Background(), fmt.Sprintf("by/%s/%013d-x.json", name, at.UnixMilli()), at, []byte(name)); err != nil {
		t.Fatal(err)
	}
}

func left(t *testing.T, s Store) int {
	t.Helper()
	keys, err := s.List(context.Background(), "v1/log/")
	if err != nil {
		t.Fatal(err)
	}
	return len(keys)
}

// #6755: past the count, the age and the record's size, the sweep brings the log back under
// each, oldest first whoever wrote it, and a write over the record's size is refused.
func TestADayLogComesBackUnderItsBounds(t *testing.T) {
	ctx := context.Background()
	s := NewMemory()
	l := dayLog(t, s, 10, nil)
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	for n := range 5 {
		put(t, l, now.AddDate(0, 0, -91).Add(time.Duration(n)*time.Second), "old")
	}
	for n := range 14 {
		put(t, l, now.Add(time.Duration(n)*time.Minute), string(rune('z'-n)))
	}
	var limit *LimitError
	if err := l.Append(ctx, "x", now, make([]byte, 65)); !errors.As(err, &limit) {
		t.Fatal(err)
	}
	if n, err := l.Sweep(ctx, now, now.AddDate(0, 0, -90), 3); n != 3 || err != nil {
		t.Fatal("a round stops at its bound", n, err)
	}
	if n, err := l.Sweep(ctx, now, now.AddDate(0, 0, -90), 1000); n != 6 || err != nil || left(t, s) != 10 {
		t.Fatal(n, err, left(t, s))
	}
	var got string
	for r, err := range l.Range(ctx, now, now, "", false) {
		if err != nil {
			t.Fatal(err)
		}
		got += string(r.Value)
	}
	if got != "vutsrqponm" {
		t.Fatal(got)
	}
	// Another copy's sweep deleted two: the next counts them as gone.
	keys, _ := s.List(ctx, "v1/log/")
	for _, k := range keys[:2] {
		if err := s.Delete(ctx, k.Key); err != nil {
			t.Fatal(err)
		}
	}
	put(t, l, now.Add(time.Hour), "a")
	if n, err := l.Sweep(ctx, now.Add(time.Hour), now.AddDate(0, 0, -90), 1000); n != 0 || err != nil {
		t.Fatal(n, err)
	}
	if err := l.Trim(ctx, now.AddDate(0, 0, 1)); err != nil || left(t, s) != 0 {
		t.Fatal(err, left(t, s))
	}
}

// Records of one second go together.
func TestADayLogEvictsAWholeSecond(t *testing.T) {
	s := NewMemory()
	l := dayLog(t, s, 8, nil)
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	for n := range 10 {
		put(t, l, now.Add(time.Duration(n)*250*time.Millisecond), "a")
	}
	if n, err := l.Sweep(context.Background(), now, now.AddDate(0, 0, -90), 1000); n != 4 || err != nil {
		t.Fatal(n, err)
	}
}

// A burst asks for one sweep before the hour, and that sweep sees it.
func TestADayLogAsksForASweepAtTwiceItsLimit(t *testing.T) {
	ctx := context.Background()
	asked := 0
	s := NewMemory()
	l := dayLog(t, s, 10, func(context.Context) { asked++ })
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	if _, err := l.Sweep(ctx, now, now.AddDate(0, 0, -90), 1000); err != nil {
		t.Fatal(err)
	}
	for n := range 30 {
		put(t, l, now.Add(time.Duration(n)*time.Second), "a")
		if n == 19 && asked != 0 {
			t.Fatal("asked too soon")
		}
	}
	if n, err := l.Sweep(ctx, now.Add(time.Minute), now.AddDate(0, 0, -90), 1000); n != 20 || err != nil || asked != 1 {
		t.Fatal(n, err, asked)
	}
}

func TestADayLogPagesByTimeAndAppendsOnce(t *testing.T) {
	ctx := context.Background()
	l := dayLog(t, NewMemory(), 10, nil)
	now := time.Date(2026, 11, 24, 23, 59, 58, 0, time.UTC)
	for n, name := range []string{"c", "b", "a", "a"} {
		put(t, l, now.Add(time.Duration(min(n, 2))*time.Second), name)
	}
	if err := l.Append(ctx, fmt.Sprintf("by/a/%013d-x.json", now.Add(2*time.Second).UnixMilli()), now.Add(2*time.Second), []byte("other")); !IsConflict(err) {
		t.Fatal(err)
	}
	page, cursor, err := l.Read(ctx, "", 2)
	if err != nil || len(page) != 2 || string(page[0].Value) != "c" || string(page[1].Value) != "b" {
		t.Fatal(page, err)
	}
	page, cursor, err = l.Read(ctx, cursor, 2)
	if err != nil || len(page) != 1 || string(page[0].Value) != "a" {
		t.Fatal(page, err)
	}
	if page, _, err = l.Read(ctx, cursor, 2); err != nil || len(page) != 0 {
		t.Fatal(page, err)
	}
}
