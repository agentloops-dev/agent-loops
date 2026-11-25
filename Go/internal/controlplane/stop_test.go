package controlplane

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
)

// #3869: Stop waits for a socket still in accept, so nothing it does, its log line
// included, comes after Stop returns. Its owner's log can be a test's t.Log, which must
// not be called after the test ends.
func TestStopWaitsForASocketInAccept(t *testing.T) {
	t.Parallel()
	logging, release := make(chan struct{}), make(chan struct{})
	var held, logged atomic.Bool
	p := startPlaneWith(t, store.NewMemory(), controllink.NewPrivateKey(), func(cfg *Config) {
		cfg.Log = func(line string) {
			// accept's last line for the socket: "refused a connection from …" or "a
			// connection from … ended"; the first one is held.
			if strings.Contains(line, "a connection from ") && held.CompareAndSwap(false, true) {
				close(logging)
				<-release
				logged.Store(true)
			}
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, strings.Replace(p.url, "http", "ws", 1)+"/v1/connect", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ws.CloseNow() }() // the test has its answer by then
	if _, _, err := ws.Read(ctx); err != nil {
		t.Fatal(err)
	}
	// No hello back: accept ends the socket, and its log line holds it in accept until
	// released.
	if err := ws.Write(ctx, websocket.MessageText, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-logging:
	case <-ctx.Done():
		t.Fatal("accept logged nothing for the socket")
	}
	stopped := make(chan bool, 1)
	go func() {
		p.s.Stop()
		stopped <- logged.Load()
	}()
	select {
	case <-stopped:
		t.Fatal("Stop returned while a socket's accept was still logging")
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	if !<-stopped {
		t.Fatal("Stop returned before the socket's accept ended")
	}
}

// #5001: Stop waits for a write a socket started and did not wait for (a member's epoch),
// so none goes into the store after Stop returns: a test's TempDir could not be removed
// while one wrote store/v1/clients. One asked for once Stop has begun does not run.
func TestStopWaitsForABackgroundWrite(t *testing.T) {
	t.Parallel()
	p := startPlaneWith(t, store.NewMemory(), controllink.NewPrivateKey(), nil)
	writing, release := make(chan struct{}), make(chan struct{})
	var wrote atomic.Bool
	p.s.inBackground(func() {
		close(writing)
		<-release
		wrote.Store(true)
	})
	<-writing
	stopped := make(chan bool, 1)
	go func() {
		p.s.Stop()
		stopped <- wrote.Load()
	}()
	select {
	case <-stopped:
		t.Fatal("Stop returned while a background write was still writing")
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	if !<-stopped {
		t.Fatal("Stop returned before the background write ended")
	}
	var late atomic.Bool
	p.s.inBackground(func() { late.Store(true) })
	p.s.writing.Wait()
	if late.Load() {
		t.Fatal("a write asked for after Stop ran")
	}
}

// heldPut is a store whose first write of a capacity snapshot waits for release.
type heldPut struct {
	store.Store
	held             atomic.Bool
	writing, release chan struct{}
}

func (h *heldPut) Put(ctx context.Context, key string, data []byte, c store.Condition) (string, error) {
	if strings.HasPrefix(key, controlCapacityPrefix) && h.held.CompareAndSwap(false, true) {
		close(h.writing)
		<-h.release
	}
	return h.Store.Put(ctx, key, data, c)
}

// #7171: Stop waits for the capacity monitor's sample, so it neither logs after Stop
// returns nor puts back the snapshot Stop deletes.
func TestStopWaitsForACapacitySample(t *testing.T) {
	t.Parallel()
	h := &heldPut{Store: store.NewMemory(), writing: make(chan struct{}), release: make(chan struct{})}
	p := startPlaneWith(t, h, controllink.NewPrivateKey(), nil)
	select {
	case <-h.writing:
	case <-time.After(10 * time.Second):
		t.Fatal("no capacity snapshot was written")
	}
	stopped := make(chan struct{})
	go func() {
		p.s.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
		t.Fatal("Stop returned while a capacity sample was still writing")
	case <-time.After(200 * time.Millisecond):
	}
	close(h.release)
	<-stopped
	keys, err := h.List(context.Background(), controlCapacityPrefix)
	if err != nil || len(keys) != 0 {
		t.Fatalf("the snapshot after Stop: %v %v", keys, err)
	}
}
