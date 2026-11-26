package agentsd

import (
	"fmt"
	"os"
	goruntime "runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/storesizes"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// The review of the bounded stores (#7240, epic #6669) on a machine at realistic counts: about
// 3,000 sessions with their transcripts. It times the hourly sweep (the first after an update,
// which removes the most, and one after it), the size measurement, and a read of the sizes.
// It writes about 150 MB into a scratch root, so it runs only with AGENTS_RUN_PERF=1:
//
//	AGENTS_RUN_PERF=1 Go/scripts/go.sh test -run '^TestStoreReviewAtRealisticCounts$' -v -timeout 30m ./internal/agentsd/
func TestStoreReviewAtRealisticCounts(t *testing.T) {
	if os.Getenv("AGENTS_RUN_PERF") != "1" {
		t.Skip("a measurement for #7240; set AGENTS_RUN_PERF=1")
	}
	const (
		sessions = 3000
		live     = 300
		entries  = 40
		// Each tenth session has entries older than the 90 days that a conversation is kept.
		oldEvery = 10
	)
	root := t.TempDir()
	d, c := start(t, root)
	defer d.Shutdown()
	dir := folder(t, c, 1)
	text := strings.Repeat("a realistic line of a chat with an agent. ", 25) // about 1 KiB
	now := time.Now()
	byUser := wire.AgentArchivedReasonByUser
	seed := cost(func() {
		for i := range sessions {
			a := seeded(t, d, dir, func(a *wire.Agent) {
				if i >= live {
					at := wire.DateOf(now.Add(-time.Duration(i%25) * 24 * time.Hour))
					a.State, a.ArchivedAt, a.ArchivedReason = wire.AgentStateArchived, &at, &byUser
				}
			})
			es := make([]wire.TranscriptEntry, entries)
			for j := range es {
				at := now.Add(-time.Duration(entries-j) * time.Minute)
				if i%oldEvery == 0 && j < entries/2 {
					at = now.Add(-100 * 24 * time.Hour)
				}
				es[j] = wire.TranscriptEntry{ID: store.NewUUID(), At: wire.DateOf(at),
					Kind: wire.TranscriptEntryKind{UserMessage: &wire.TranscriptEntryKindUserMessage{Value: text}}}
			}
			if _, err := d.Store.AppendMany(a.ID, es); err != nil {
				t.Fatal(err)
			}
		}
	})
	t.Logf("seed: %d sessions, %d entries each: %s", sessions, entries, seed)
	t.Logf("first hourly sweep (trims %d conversations): %s", sessions/oldEvery, cost(d.sweepHourly))
	t.Logf("next hourly sweep: %s", cost(d.sweepHourly))
	t.Logf("trimConversations alone: %s", cost(d.trimConversations))
	t.Logf("measureStores alone: %s", cost(d.measureStores))
	limit := int64(storesizes.MostPage)
	var page wire.StoreSizes
	read := cost(func() {
		var err error
		if page, err = d.storeSizesPage(wire.StoreSizesQuery{Limit: &limit}); err != nil {
			t.Fatal(err)
		}
	})
	t.Logf("stores/list, %d stores: %s", len(page.Stores), read)
	over := 0
	for _, s := range page.Stores {
		if s.State == wire.StoreSizeStateOver {
			over++
			t.Logf("over its limit: %s", s.ID)
		}
	}
	t.Logf("%d stores over a limit", over)
	if left := len(d.Store.Agents()); left > sessions {
		t.Errorf("%d agents after the sweep", left)
	}
}

// cost runs f once and says its wall time, the CPU time of the whole process in it, and what it
// allocated.
func cost(f func()) string {
	var before, after syscall.Rusage
	var m0, m1 goruntime.MemStats
	goruntime.ReadMemStats(&m0)
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &before)
	start := time.Now()
	f()
	wall := time.Since(start)
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &after)
	goruntime.ReadMemStats(&m1)
	cpu := time.Duration(after.Utime.Nano()-before.Utime.Nano()) + time.Duration(after.Stime.Nano()-before.Stime.Nano())
	return fmt.Sprintf("wall %v, cpu %v, alloc %d MB", wall.Round(time.Millisecond), cpu.Round(time.Millisecond),
		(m1.TotalAlloc-m0.TotalAlloc)>>20)
}
