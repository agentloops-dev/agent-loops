package controlplane

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// busOps counts a store's calls on the bus (#6569), by kind, and the prefixes it lists.
type busOps struct {
	store.Store
	mu     sync.Mutex
	counts map[string]int
	listed []string
}

func newBusOps(inner store.Store) *busOps { return &busOps{Store: inner, counts: map[string]int{}} }

func (b *busOps) note(kind, key string) {
	if !strings.HasPrefix(key, busPrefix) {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.counts[kind]++
	if kind == "list" {
		b.listed = append(b.listed, key)
	}
}

func (b *busOps) Get(ctx context.Context, key string) (*store.Object, error) {
	b.note("get", key)
	return b.Store.Get(ctx, key)
}

func (b *busOps) Put(ctx context.Context, key string, data []byte, when store.Condition) (string, error) {
	b.note("put", key)
	return b.Store.Put(ctx, key, data, when)
}

func (b *busOps) List(ctx context.Context, prefix string) ([]store.Key, error) {
	b.note("list", prefix)
	return b.Store.List(ctx, prefix)
}

func (b *busOps) Folders(ctx context.Context, prefix string) ([]string, error) {
	b.note("folders", prefix)
	return store.Folders(ctx, b.Store, prefix)
}

// take is the counts since the last take.
func (b *busOps) take() (map[string]int, []string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	counts, listed := b.counts, b.listed
	b.counts, b.listed = map[string]int{}, nil
	return counts, listed
}

// seedBus writes events envelopes on bus straight into a folder store's root, as the live
// store holds them: each its log file of size bytes, in its day's folder, and its id's
// file, each with the lock a write leaves beside it. The first starts at from, a minute
// apart.
func seedBus(tb testing.TB, root, bus string, events, size int, from time.Time) {
	tb.Helper()
	seedBusAs(tb, root, bus, events, size, from, busLogKey)
}

// busFlatLogKey is a log key as it was before #6582: flat in log/, with no day.
func busFlatLogKey(bus string, at time.Time, hash string) string {
	return busLogPrefix(bus) + at.UTC().Format(busLogStamp) + "-" + hash + ".json"
}

// seedBusAs is seedBus with the log's keys made by logKeyOf.
func seedBusAs(tb testing.TB, root, bus string, events, size int, from time.Time, logKeyOf func(string, time.Time, string) string) {
	tb.Helper()
	for i := range events {
		hash := busIDHash(fmt.Sprint("seed-", from.Unix(), "-", i))
		at := from.Add(time.Duration(i) * time.Minute)
		body := `{"specversion":"1.0","id":"seed-` + hash + `@pr.merged","type":"pr.merged","time":"` + at.Format(time.RFC3339) + `","pad":"` + strings.Repeat("x", max(0, size-160)) + `"}`
		logKey := logKeyOf(bus, at, hash)
		for key, data := range map[string]string{logKey: body, busIDKey(bus, hash): `{"id":"seed","log":"` + logKey + `"}`} {
			p := filepath.Join(root, filepath.FromSlash(key))
			if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
				tb.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
				tb.Fatal(err)
			}
			if err := os.WriteFile(p+".lock", nil, 0o600); err != nil {
				tb.Fatal(err)
			}
		}
	}
}

// #6569: bus/lease and bus/publish read and write only their own keys, never a listing, so
// they answer in the same time and memory whatever the bus holds: 2,000 envelopes here,
// 82,000 on the live control plane.
func TestBusLeaseAndPublishListNothing(t *testing.T) {
	t.Parallel()
	ops := newBusOps(store.NewFolder(t.TempDir()))
	p := startPlane(t, ops, controllink.NewPrivateKey())
	host := p.host("MAC-A").member.Host
	p.waitFor(func() bool { return p.s.router.Holds(host) }, "the machine held")
	root := ops.Store.(*store.Folder).Root
	seedBus(t, root, busOf(p.s.ownerOf(host)), 2_000, 200, time.Now().Add(-48*time.Hour))
	ops.take()

	const poll = "0123456789abcdef0123456789abcdef"
	for range 5 {
		leaseOf(t, p, host, []string{poll})
	}
	counts, listed := ops.take()
	// Each ask reads its lease and writes it once: taken the first time, renewed after.
	if counts["get"] != 5 || counts["put"] != 5 || counts["list"] != 0 || counts["folders"] != 0 {
		t.Fatalf("5 bus/lease calls did %v, listing %v", counts, listed)
	}

	for i := range 5 {
		busSay[wire.BusPublished](t, p, host, wire.CallBusPublish.Name, wire.BusPublish{Events: []wire.BusEvent{prMerged(10_000 + i)}})
	}
	counts, listed = ops.take()
	// Each new event: its id's file, create-if-absent, then its place in the log.
	if counts["get"] != 0 || counts["put"] != 10 || counts["list"] != 0 || counts["folders"] != 0 {
		t.Fatalf("5 bus/publish calls did %v, listing %v", counts, listed)
	}
}

// #6569: the hourly sweep of the bus lists each bus's log and its leases, never its ids,
// which are as many again as the log: a folder store's listing stats each file it lists,
// and keeps it in memory after.
func TestBusSweepsListTheLogAndTheLeasesOnly(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	folder := store.NewFolder(t.TempDir())
	ops := newBusOps(folder)
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	const bus = "0011223344556677"
	seedBus(t, folder.Root, bus, 300, 100, now.Add(-24*time.Hour))
	for poll, until := range map[string]time.Time{"old": now.Add(-busLeaseKept - time.Hour), "live": now.Add(time.Minute)} {
		if _, err := folder.Put(ctx, busLeaseKey(bus, poll), []byte(`{"holder":"H","until":"`+until.Format(time.RFC3339)+`"}`), store.Always); err != nil {
			t.Fatal(err)
		}
	}

	n, err := sweepBus(ctx, ops, 100, 1_000)
	if err != nil || n != 200 {
		t.Fatalf("swept %d, %v", n, err)
	}
	if n, err := sweepBusLeases(ctx, ops, now, 1_000); err != nil || n != 1 {
		t.Fatalf("swept %d leases, %v", n, err)
	}
	_, listed := ops.take()
	want := []string{busPrefix + bus + "/log/", busPrefix + bus + "/leases/"}
	if strings.Join(listed, " ") != strings.Join(want, " ") {
		t.Fatalf("listed %v, want %v", listed, want)
	}
	// Only the log's files were read for their tags (the leases were written here, so
	// known): none of the ids.
	if got := folder.HashedByListing(); got != 300 {
		t.Fatalf("hashed %d files", got)
	}
	ids, err := os.ReadDir(filepath.Join(folder.Root, busPrefix, bus, "ids"))
	if err != nil {
		t.Fatal(err)
	}
	if kept := len(ids) / 2; kept != 100 {
		t.Fatalf("%d ids kept, want 100 with the log's 100", kept)
	}
}

func TestBusSweepExpiresOldEnvelopesAndEnforcesByteLimit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	folder := store.NewFolder(t.TempDir())
	const bus = "0011223344556677"
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	seedBus(t, folder.Root, bus, 2, 100, now.Add(-20*24*time.Hour))
	seedBus(t, folder.Root, bus, 3, 100, now.Add(-time.Hour))
	all, err := folder.List(ctx, busLogPrefix(bus))
	if err != nil || len(all) != 5 {
		t.Fatalf("listed %d envelopes, %v; want 5", len(all), err)
	}

	n, err := sweepBusAt(ctx, folder, now, 100, 2*all[0].Size+1, 10*24*time.Hour, 10)
	if err != nil || n != 3 {
		t.Fatalf("swept %d, %v; want 2 aged and 1 over the byte limit", n, err)
	}
	logs, err := folder.List(ctx, busLogPrefix(bus))
	if err != nil || len(logs) != 2 {
		t.Fatalf("%d envelopes remain, %v; want 2", len(logs), err)
	}
	ids, err := folder.List(ctx, busPrefix+bus+"/ids/")
	if err != nil || len(ids) != 2 {
		t.Fatalf("%d dedupe ids remain, %v; want 2", len(ids), err)
	}
}

// A folder store names a prefix's folders from that folder alone; any other store from
// its keys.
func TestFoldersNamesTheFoldersUnderAPrefix(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for name, st := range map[string]store.Store{"folder": store.NewFolder(t.TempDir()), "memory": store.NewMemory()} {
		for _, key := range []string{"v1/bus/b/log/1.json", "v1/bus/a/ids/2.json", "v1/bus/a/log/3.json", "v1/bus/top.json", "v1/other/c/x.json"} {
			if _, err := st.Put(ctx, key, []byte("{}"), store.Always); err != nil {
				t.Fatal(err)
			}
		}
		got, err := store.Folders(ctx, st, "v1/bus/")
		if err != nil || strings.Join(got, ",") != "a,b" {
			t.Fatalf("%s: %v, %v", name, got, err)
		}
		if got, err := store.Folders(ctx, st, "v1/none/"); err != nil || len(got) != 0 {
			t.Fatalf("%s: %v, %v under a prefix with nothing", name, got, err)
		}
		if got, err := store.Folders(ctx, store.NewPrefixed(st, "v1"), "bus/"); err != nil || strings.Join(got, ",") != "a,b" {
			t.Fatalf("%s prefixed: %v, %v", name, got, err)
		}
	}
}

// BenchmarkBusLeaseAtTheLiveShape is bus/lease's store work on a folder store of the live
// control plane's shape (#6569): 41,250 envelopes, 165,000 files with their locks. It
// takes the same time and memory as on an empty store, as it reads one key and writes one.
//
//	Go/scripts/go.sh test -run '^$' -bench BusLeaseAtTheLiveShape -benchmem ./internal/controlplane/
func BenchmarkBusLeaseAtTheLiveShape(b *testing.B) {
	for _, events := range []int{0, 41_250} {
		b.Run(fmt.Sprint(events*4, " files"), func(b *testing.B) {
			ctx := context.Background()
			folder := store.NewFolder(b.TempDir())
			const bus = "0011223344556677"
			seedBus(b, folder.Root, bus, events, 14_000, time.Now().Add(-60*24*time.Hour))
			s := &Service{cfg: Config{Store: folder}}
			runtime.GC()
			var before runtime.MemStats
			runtime.ReadMemStats(&before)
			now := time.Now()
			b.ResetTimer()
			for b.Loop() {
				if _, err := s.busTake(ctx, bus, "MAC-A", "0123456789abcdef0123456789abcdef", now); err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			runtime.GC()
			var after runtime.MemStats
			runtime.ReadMemStats(&after)
			b.ReportMetric((float64(after.HeapInuse)-float64(before.HeapInuse))/(1<<20), "heap-MB-held")
		})
	}
}

// settleBus dates every folder of the bus an hour back, as a live store's days are: a
// folder store trusts a folder's stamp only once it is a few seconds old.
func settleBus(tb testing.TB, root string) {
	tb.Helper()
	var dirs []string
	err := filepath.WalkDir(filepath.Join(root, busPrefix), func(p string, d os.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			dirs = append(dirs, p)
		}
		return err
	})
	if err != nil {
		tb.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	for _, dir := range dirs {
		if err := os.Chtimes(dir, old, old); err != nil {
			tb.Fatal(err)
		}
	}
}

// #6582: the log has a folder for each day, so after a publish a listing of the log, the
// sweep's or bus/log's, stats only today's envelopes: the other days, and the flat
// envelopes from before #6582, come from the folder store's index.
func TestBusLogListingAfterAPublishStatsOnlyToday(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	folder := store.NewFolder(t.TempDir())
	p := startPlane(t, folder, controllink.NewPrivateKey())
	host := p.host("MAC-A").member.Host
	p.waitFor(func() bool { return p.s.router.Holds(host) }, "the machine held")
	person := p.s.ownerOf(host)
	bus := busOf(person)
	noon := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	// 100 flat from before #6582, three earlier days of 1,440, and 30 today.
	seedBusAs(t, folder.Root, bus, 100, 200, noon.Add(-5*24*time.Hour), busFlatLogKey)
	seedBus(t, folder.Root, bus, 3*1_440, 200, noon.Add(-3*24*time.Hour-12*time.Hour))
	seedBus(t, folder.Root, bus, 30, 200, noon.Add(-30*time.Minute))
	settleBus(t, folder.Root)
	if _, err := sweepBus(ctx, folder, busLogKept, busLogKept); err != nil {
		t.Fatal(err)
	}
	if _, err := p.s.busLog(ctx, person, wire.BusLogQuery{}); err != nil {
		t.Fatal(err)
	}

	if _, err := p.s.busPublish(ctx, host, wire.BusPublish{Events: []wire.BusEvent{prMerged(1)}}, noon); err != nil {
		t.Fatal(err)
	}
	today := 31
	before := folder.StatedByListing(busLogPrefix(bus))
	if n, err := sweepBus(ctx, folder, busLogKept, busLogKept); err != nil || n != 0 {
		t.Fatalf("swept %d, %v", n, err)
	}
	if got := folder.StatedByListing(busLogPrefix(bus)) - before; got != today {
		t.Fatalf("the sweep's listing stat'ed %d files after a publish, want today's %d", got, today)
	}

	if _, err := p.s.busPublish(ctx, host, wire.BusPublish{Events: []wire.BusEvent{prMerged(2)}}, noon.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	today++
	before = folder.StatedByListing(busLogPrefix(bus))
	page, err := p.s.busLog(ctx, person, wire.BusLogQuery{})
	if err != nil || len(page.Events) != busLogPage || !page.HasMore {
		t.Fatalf("%v: %d events, more %v", err, len(page.Events), page.HasMore)
	}
	if got := folder.StatedByListing(busLogPrefix(bus)) - before; got != today {
		t.Fatalf("bus/log stat'ed %d files after a publish, want today's %d", got, today)
	}
	if page.Events[0].ID != prMerged(2).ID {
		t.Fatalf("the newest is %s", page.Events[0].ID)
	}
}

// #6582: bus/log reads the days newest first, then the flat envelopes from before #6582,
// a page at a time across them; the sweep deletes the flat ones first, then the oldest
// days, and a day's folder goes with its last envelope.
func TestBusLogReadsTheDaysThenTheFlatLog(t *testing.T) {
	t.Parallel()
	for name, st := range map[string]func(root string) store.Store{
		"folder": func(root string) store.Store { return store.NewFolder(root) },
		"memory": func(string) store.Store { return store.NewMemory() },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := &Service{cfg: Config{Store: st(t.TempDir())}}
			const person = "person"
			want := seedDaysAndFlat(t, s.cfg.Store, busOf(person))
			if got := readBusLog(t, s, person, 4); strings.Join(got, " ") != strings.Join(want, " ") {
				t.Fatalf("read %v, want %v", got, want)
			}
			checkBusSweepByDay(t, s.cfg.Store, busOf(person))
		})
	}
}

// seedDaysAndFlat puts two flat envelopes, then two on the 19th and two on the 20th, a
// minute apart, and gives their ids newest first.
func seedDaysAndFlat(t *testing.T, st store.Store, bus string) []string {
	t.Helper()
	day := time.Date(2026, 11, 19, 23, 58, 0, 0, time.UTC)
	var want []string
	for i := range 6 {
		at := day.Add(time.Duration(i-2) * time.Minute)
		keyOf := busLogKey
		if i < 2 {
			keyOf = busFlatLogKey
		}
		id := at.Format(time.RFC3339) + "@pr.merged"
		e := `{"specversion":"1.0","id":"` + id + `","type":"pr.merged"}`
		if _, err := st.Put(context.Background(), keyOf(bus, at, busIDHash(id)), []byte(e), store.Always); err != nil {
			t.Fatal(err)
		}
		want = append([]string{id}, want...)
	}
	return want
}

// readBusLog reads the whole of a person's log a page of limit at a time, and gives the
// ids in the order read.
func readBusLog(t *testing.T, s *Service, person string, limit int64) []string {
	t.Helper()
	var got []string
	var next *string
	for range 10 {
		page, err := s.busLog(context.Background(), person, wire.BusLogQuery{Before: next, Limit: &limit})
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range page.Events {
			got = append(got, e.ID)
		}
		if !page.HasMore {
			return got
		}
		next = page.Next
	}
	t.Fatal("bus/log does not end")
	return nil
}

// checkBusSweepByDay sweeps seedDaysAndFlat's log to 3, then to 2.
func checkBusSweepByDay(t *testing.T, st store.Store, bus string) {
	t.Helper()
	ctx := context.Background()
	prefix := busLogPrefix(bus)
	if n, err := sweepBus(ctx, st, 3, 10); err != nil || n != 3 {
		t.Fatalf("swept %d, %v", n, err)
	}
	keys, _ := st.List(ctx, prefix)
	var left []string
	for _, k := range keys {
		left = append(left, strings.TrimPrefix(k.Key, prefix)[:8])
	}
	if strings.Join(left, " ") != "20261119 20261120 20261120" {
		t.Fatalf("the sweep left %v", left)
	}
	if days, _ := store.Folders(ctx, st, prefix); strings.Join(days, " ") != "20261119 20261120" {
		t.Fatalf("days %v", days)
	}
	if n, err := sweepBus(ctx, st, 2, 10); err != nil || n != 1 {
		t.Fatalf("swept %d, %v", n, err)
	}
	if days, _ := store.Folders(ctx, st, prefix); strings.Join(days, " ") != "20261120" {
		t.Fatalf("days %v after the 19th's last went", days)
	}
}

// BenchmarkBusLogListingAfterAPublishAtTheLiveShape is what a listing of the log costs
// after each publish on a folder store of the live control plane's shape (#6582): 41,250
// envelopes over 29 days, 165,000 files with their ids and locks. Each publish changes
// only today's folder, so the sweep's listing and bus/log stat today's envelopes only
// (stats/op, against today/op), where a flat log stat'ed all 41,250.
//
//	Go/scripts/go.sh test -run '^$' -bench BusLogListingAfterAPublish -benchmem ./internal/controlplane/
func BenchmarkBusLogListingAfterAPublishAtTheLiveShape(b *testing.B) {
	ctx := context.Background()
	folder := store.NewFolder(b.TempDir())
	const person = "person"
	bus := busOf(person)
	const events = 41_250
	noon := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	seedBus(b, folder.Root, bus, events, 14_000, noon.Add(-events*time.Minute))
	settleBus(b, folder.Root)
	s := &Service{cfg: Config{Store: folder}}
	// The first listing after a start reads every file: not what this measures.
	if _, err := sweepBus(ctx, folder, busLogKept, busLogKept); err != nil {
		b.Fatal(err)
	}
	before := folder.StatedByListing(busLogPrefix(bus))
	b.ResetTimer()
	i := 0
	for b.Loop() {
		i++
		at := noon.Add(time.Duration(i) * time.Millisecond)
		if _, err := folder.Put(ctx, busLogKey(bus, at, busIDHash(fmt.Sprint("bench-", i))), []byte(`{"specversion":"1.0"}`), store.Absent); err != nil {
			b.Fatal(err)
		}
		if _, err := sweepBus(ctx, folder, busLogKept, busLogKept); err != nil {
			b.Fatal(err)
		}
		if _, err := s.busLog(ctx, person, wire.BusLogQuery{}); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	// Two listings each time, of today's 720 seeded and those published so far.
	b.ReportMetric(float64(folder.StatedByListing(busLogPrefix(bus))-before)/float64(i), "stats/op")
	b.ReportMetric(2*(720+float64(i+1)/2), "today/op")
}
