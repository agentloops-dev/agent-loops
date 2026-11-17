package agentsd

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"
)

// A goroutine the daemon started and nothing stopped is named by where it started (#5653), and
// nothing starts once the host has stopped.
func TestAGoroutineLeftRunningIsNamedByWhereItStarted(t *testing.T) {
	t.Parallel()

	d, err := New(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	d.goJob("stuck", func() { <-release })
	// Each of the daemon's own goroutines ends with d.ctx, which waitJobs ends.
	if d.waitJobs(200 * time.Millisecond) {
		t.Fatal("the wait ended with a goroutine still running")
	}
	live := d.liveJobs()
	if !slices.ContainsFunc(live, func(j string) bool { return strings.HasPrefix(j, "stuck (Go/internal/agentsd/jobs_test.go:") }) {
		t.Fatalf("still running: %q", live)
	}
	close(release)
	if !d.waitJobs(5 * time.Second) {
		t.Fatalf("still running: %q", d.liveJobs())
	}

	ran := make(chan struct{}, 2)
	d.goJob("after", func() { ran <- struct{}{} })
	d.jobFunc("timer", func() { ran <- struct{}{} })()
	select {
	case <-ran:
		t.Fatal("a goroutine started after the host stopped")
	case <-time.After(50 * time.Millisecond):
	}
	d.Shutdown()
}
