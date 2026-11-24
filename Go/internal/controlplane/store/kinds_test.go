package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestLogAppendsIdempotentlyAndPages(t *testing.T) {
	ctx := context.Background()
	log, err := OpenLog(Spec{Name: "audit", Kind: "log", Scope: "org-1", Bounds: Bounds{MaxRecordBytes: 1024, MaxCount: 10, MaxAge: 7 * 24 * time.Hour}}, NewMemory())
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	if err = log.Append(ctx, "first", at, []byte("not necessarily JSON")); err != nil {
		t.Fatal(err)
	}
	if err = log.Append(ctx, "first", at, []byte("not necessarily JSON")); err != nil {
		t.Fatal(err)
	}
	if err = log.Append(ctx, "second", at.Add(time.Second), []byte("two")); err != nil {
		t.Fatal(err)
	}
	page, cursor, err := log.Read(ctx, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 1 || string(page[0].Value) != "not necessarily JSON" || cursor == "" {
		t.Fatalf("unexpected first page: %#v %q", page, cursor)
	}
	page, _, err = log.Read(ctx, cursor, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 1 || page[0].ID != "second" {
		t.Fatalf("unexpected second page: %#v", page)
	}
	if err = log.Append(ctx, "large", at, make([]byte, 1025)); err == nil {
		t.Fatal("expected record limit")
	} else {
		var limit *LimitError
		if !errors.As(err, &limit) {
			t.Fatalf("wrong error: %v", err)
		}
	}
}

func TestTimeSeriesRollupIsRetrySafeAndRetained(t *testing.T) {
	ctx := context.Background()
	backend := NewMemory()
	ts, err := OpenTimeSeries(Spec{Name: "usage", Kind: "time series", Scope: "org-1", Bounds: Bounds{MaxRecordBytes: 1024, MaxCount: 100, MaxAge: 90 * 24 * time.Hour, DetailAge: 168 * time.Hour}}, backend)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 11, 1, 3, 0, 0, 0, time.UTC)
	for _, p := range []Point{{Series: "sessions", ID: "a", At: base, Value: 2}, {Series: "sessions", ID: "b", At: base.Add(time.Minute), Value: 3}} {
		if err = ts.Append(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	cutoff := base.Add(8 * 24 * time.Hour)
	if err = ts.Rollup(ctx, cutoff, 24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if err = ts.Rollup(ctx, cutoff, 24*time.Hour); err != nil {
		t.Fatal(err)
	}
	points, _, err := ts.Range(ctx, "sessions", base.Add(-time.Hour), cutoff, "day", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 1 || points[0].Value != 5 || points[0].Count != 2 || points[0].ID != "2026-11-01" {
		t.Fatalf("unexpected aggregate: %#v", points)
	}
}
