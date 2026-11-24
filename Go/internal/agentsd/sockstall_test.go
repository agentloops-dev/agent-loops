package agentsd

import (
	"bufio"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/flaky"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// A daemon.sock client that never reads does not hold Broadcast, or the clients that do
// read: it is let go, and the others hear everything (#1399).
//
// It holds Broadcast once, for at most the product's own bound (its queue's wait, or a
// write's deadline), and the test checks that bound for each call. It sets no limit for
// all the calls together (#5998): the client that reads sets their pace, so on a CI
// runner under load they took over 30 s, while the one call that let go of the stalled
// client still took sockQueueWait.
func TestAStalledSocketClientIsLetGo(t *testing.T) {
	flaky.UnderLoad(t, "#7012")
	t.Parallel()

	// Both callers are windows, which hear notifications, whatever the platform's door.
	d, c := start(t, t.TempDir())
	usePolicy(t, d, fixedPolicy{role: RoleControl, ok: true})
	// The test's own connection is read too: only the socket's client stalls.
	c.drain()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir, err := os.MkdirTemp("", "sock") // short: a Unix socket's path is at most 104 bytes
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "d.sock")
	go func() { _ = d.ServeSocket(ctx, path) }() // it ends with ctx; a socket that does not listen fails the dial
	stalled := dialUnix(t, path)
	defer stalled.Close()
	reader := dialUnix(t, path)
	defer reader.Close()
	clients := func() int {
		d.mu.Lock()
		defer d.mu.Unlock()
		return len(d.clients)
	}
	for deadline := time.Now().Add(5 * time.Second); clients() < 3; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the clients were not taken")
		}
	}
	// More lines than the stalled client's queue and its socket's buffers hold, so its
	// queue fills and it is let go; then more, which the reader must hear too.
	const n = sockQueue + 256
	var heard atomic.Int64
	go func() {
		r := bufio.NewReaderSize(reader, 1<<20)
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			if strings.Contains(line, wire.NotifyFolderChanged.Name) {
				heard.Add(1)
			}
		}
	}()
	big := strings.Repeat("x", 16<<10)
	done := make(chan struct{})
	var longest time.Duration
	held := 0 // the calls that waited sockQueueWait or more
	go func() {
		defer close(done)
		for i := 0; i < n; i++ {
			began := time.Now()
			Broadcast(d, wire.NotifyFolderChanged, wire.FolderSummary{Name: big})
			took := time.Since(began)
			longest = max(longest, took)
			if took >= sockQueueWait {
				held++
			}
		}
	}()
	select {
	case <-done:
	case <-afterLimit(t):
		waitFailed(t, "Broadcast is held by a client that reads nothing")
	}
	// One stalled client: one call waits for its full queue, then it is gone. With no
	// let-go, a call waits for ever, or once for each line.
	if bound := sockQueueWait + sockWriteDeadline; longest >= bound {
		t.Fatalf("a Broadcast was held %s by the stalled client; the bound is %s", longest, bound)
	}
	if held > 1 {
		t.Fatalf("%d Broadcasts were held for the stalled client, not one", held)
	}
	for deadline := time.Now().Add(waitLimit(t)); heard.Load() < n && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
	}
	if got := heard.Load(); got != n {
		t.Fatalf("the reading client heard %d of %d", got, n)
	}
	// The stalled one was let go: what it has buffered ends in EOF.
	must(t, stalled.SetReadDeadline(time.Now().Add(waitLimit(t))))
	if _, err := io.Copy(io.Discard, stalled); err != nil {
		t.Fatalf("the stalled client was not let go: %v", err)
	}
}
