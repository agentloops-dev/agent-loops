package controlplane

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/audit"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/records"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/storesizes"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// The review of the bounded stores (#7240, epic #6669) on a scratch control plane's folder
// store at realistic counts: the hourly sweep (the first after an update, which lists every
// event and removes the most, and the next), the size measurement (#6906), and the read of
// Reports (#6907). It writes about 200,000 files, so it runs only with AGENTS_RUN_PERF=1:
//
//	AGENTS_RUN_PERF=1 Go/scripts/go.sh test -run '^TestStoreReviewAtRealisticCounts$' -v -timeout 30m ./internal/controlplane/
func TestStoreReviewAtRealisticCounts(t *testing.T) {
	if os.Getenv("AGENTS_RUN_PERF") != "1" {
		t.Skip("a measurement for #7240; set AGENTS_RUN_PERF=1")
	}
	ctx := context.Background()
	folder := store.NewFolder(t.TempDir())
	st := &reviewStore{Store: folder}
	now := time.Now()
	t.Logf("seed: %s", cost(func() { seedReview(ctx, t, folder, now) }))
	files, bytes, err := folder.Stats()
	t.Logf("store: %d files, %d MB (err %v)", files, bytes>>20, err)

	rs := records.New(st)
	if err := rs.Load(ctx); err != nil {
		t.Fatal(err)
	}
	events := &eventSweep{}
	sweepOnce := func(label string) {
		st.reset()
		var report sweepReport
		var rounds int
		c := cost(func() {
			report, rounds, err = sweepRounds(ctx, rs, time.Now, mostPerSweep, events, 0, func(time.Duration) bool { return true })
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: %d rounds (%s), %s; %s", label, rounds, report, st, c)
	}
	sweepOnce("first hourly sweep after an update")
	sweepOnce("next hourly sweep")

	rows := storesizes.Catalogue(wire.StoreOwnerControlPlane)
	measured := map[string]storesizes.Measure{}
	type took struct {
		id   string
		wall time.Duration
		keys int64
	}
	var each []took
	st.reset()
	t.Logf("size measurement, %d stores: %s; %s", len(rows), cost(func() {
		for _, r := range rows {
			if r.At == nil {
				continue
			}
			before, start := st.keys.Load(), time.Now()
			measured[r.ID] = r.Keys(ctx, st, now)
			each = append(each, took{r.ID, time.Since(start), st.keys.Load() - before})
		}
	}), st)
	sort.Slice(each, func(i, j int) bool { return each[i].wall > each[j].wall })
	for _, e := range each[:8] {
		t.Logf("  %s: %v, %d keys listed", e.id, e.wall.Round(time.Millisecond), e.keys)
	}
	data, err := records.Encode(storeSizesRecord{At: wire.DateOf(now), Stores: measured})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("store-sizes.json: %d bytes of its %d", len(data), storeSizesMostBytes)
	limit := int64(storesizes.MostPage)
	st.reset()
	t.Logf("reports/storeSizes read: %s; %s", cost(func() {
		if _, err := storesizes.Page(rows, measured, wire.StoreSizesQuery{Limit: &limit}, time.Now(), 2*storeSizesEvery); err != nil {
			t.Fatal(err)
		}
	}), st)
}

// seedReview fills folder with the review's realistic counts (#7240).
func seedReview(ctx context.Context, t *testing.T, folder *store.Folder, now time.Time) {
	t.Helper()
	const (
		machines     = 10
		auditPerDay  = 1000
		auditDays    = 120 // 30 past the 90 that are kept: an update after a time with no sweep
		busEnvelopes = 20_000
		meshEvents   = 10_000
		runs         = 5_000
	)
	put := func(key string, size int) {
		if _, err := folder.Put(ctx, key, []byte(`{"pad":"`+strings.Repeat("x", max(size-10, 0))+`"}`), store.Always); err != nil {
			t.Fatal(err)
		}
	}
	if err := SeedFakeMachines(ctx, folder, machines); err != nil {
		t.Fatal(err)
	}
	for day := range auditDays {
		at := now.Add(-time.Duration(day) * 24 * time.Hour)
		for i := range auditPerDay {
			put(audit.Key(at.Add(-time.Duration(i)*time.Second), "alex@example.com"), 600)
		}
	}
	for i := range busEnvelopes {
		put(busLogKey("alex", now.Add(-time.Duration(i)*time.Minute), fmt.Sprintf("%016x", i)), 1024)
	}
	for i := range meshEvents {
		put(fmt.Sprintf("%s%013d-%04d.json", meshEventsPrefix, now.Add(-time.Duration(i)*time.Second).UnixMilli(), i), 400)
	}
	for i := range runs {
		put(fmt.Sprintf("v1/runs/run-%05d.json", i), 800)
	}
	for m := 1; m <= machines; m++ {
		host := fmt.Sprintf("fake-%04d", m)
		put(copyKey(host), 1<<20) // about 300 sessions a machine: 3,000 in all
		fillLineage(ctx, t, folder, host, 0)
		for h := range 168 {
			put(usageKey(usageHoursPrefix, now.Add(-time.Duration(h)*time.Hour).UTC().Format(usageHourLayout), host), 2048)
		}
		for d := range 90 {
			put(usageKey(usageDaysPrefix, now.AddDate(0, 0, -d).UTC().Format(usageDayLayout), host), 2048)
		}
	}
}

// reviewStore counts the lists of a store and the keys they return.
type reviewStore struct {
	store.Store
	lists, keys atomic.Int64
}

func (c *reviewStore) List(ctx context.Context, prefix string) ([]store.Key, error) {
	keys, err := c.Store.List(ctx, prefix)
	c.lists.Add(1)
	c.keys.Add(int64(len(keys)))
	return keys, err
}

func (c *reviewStore) Folders(ctx context.Context, prefix string) ([]string, error) {
	return store.Folders(ctx, c.Store, prefix)
}

func (c *reviewStore) Unwrap() store.Store { return c.Store }

func (c *reviewStore) reset() { c.lists.Store(0); c.keys.Store(0) }

func (c *reviewStore) String() string {
	return fmt.Sprintf("%d lists, %d keys listed", c.lists.Load(), c.keys.Load())
}

// cost runs f once and says its wall time, the CPU time of the whole process in it, and what it
// allocated.
func cost(f func()) string {
	var before, after syscall.Rusage
	var m0, m1 runtime.MemStats
	runtime.ReadMemStats(&m0)
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &before)
	start := time.Now()
	f()
	wall := time.Since(start)
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &after)
	runtime.ReadMemStats(&m1)
	cpu := time.Duration(after.Utime.Nano()-before.Utime.Nano()) + time.Duration(after.Stime.Nano()-before.Stime.Nano())
	return fmt.Sprintf("wall %v, cpu %v, alloc %d MB", wall.Round(time.Millisecond), cpu.Round(time.Millisecond),
		(m1.TotalAlloc-m0.TotalAlloc)>>20)
}
