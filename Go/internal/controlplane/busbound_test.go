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
// store holds them: each its log file of size bytes and its id's file, each with the lock a
// write leaves beside it. The first starts at from, a minute apart.
func seedBus(tb testing.TB, root, bus string, events, size int, from time.Time) {
	tb.Helper()
	body := `{"specversion":"1.0","type":"pr.merged","sealed":"` + strings.Repeat("x", max(0, size-60)) + `"}`
	for _, dir := range []string{"log", "ids"} {
		if err := os.MkdirAll(filepath.Join(root, busPrefix, bus, dir), 0o700); err != nil {
			tb.Fatal(err)
		}
	}
	for i := range events {
		hash := busIDHash(fmt.Sprint("seed-", i))
		logKey := busLogKey(bus, from.Add(time.Duration(i)*time.Minute), hash)
		for key, data := range map[string]string{logKey: body, busIDKey(bus, hash): `{"id":"seed","log":"` + logKey + `"}`} {
			p := filepath.Join(root, filepath.FromSlash(key))
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
