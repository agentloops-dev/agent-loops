package agentsd

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/machinelock"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// cpLines is a control plane between a client's Session and the machine h-1: it carries each
// message as it came, framed with the machine's id, and a test may change what it carries.
type cpLines struct {
	mu     sync.Mutex
	to     *conn
	out    chan string
	closed chan struct{}
	once   sync.Once
	// tamper changes a line from the machine on its way, when set.
	tamper func(line string) string
}

func newCPLines() *cpLines {
	return &cpLines{out: make(chan string, 64), closed: make(chan struct{})}
}

// attach carries the client's lines to c from now on: a new uplink, as when the machine's
// link went and came back.
func (l *cpLines) attach(c *conn) {
	l.mu.Lock()
	l.to = c
	l.mu.Unlock()
	go func() {
		for {
			select {
			case line := <-c.out:
				l.mu.Lock()
				tamper := l.tamper
				l.mu.Unlock()
				if tamper != nil {
					line = tamper(line)
				}
				select {
				case l.out <- `{"h":"h-1","m":` + line + `}`:
				case <-l.closed:
					return
				}
			case <-l.closed:
				return
			}
		}
	}()
}

func (l *cpLines) Write(_ context.Context, line string) error {
	var f struct {
		H string          `json:"h"`
		M json.RawMessage `json:"m"`
	}
	if err := json.Unmarshal([]byte(line), &f); err != nil {
		return err
	}
	if f.H != "h-1" {
		return errors.New("only h-1 is here")
	}
	l.mu.Lock()
	c := l.to
	l.mu.Unlock()
	c.send(string(f.M))
	return nil
}

func (l *cpLines) Read(ctx context.Context) (string, error) {
	select {
	case line := <-l.out:
		return line, nil
	case <-l.closed:
		return "", errors.New("closed")
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (l *cpLines) Close() { l.once.Do(func() { close(l.closed) }) }

func sessionTo(t *testing.T, d *Daemon) (*controllink.Session, *cpLines) {
	t.Helper()
	l := newCPLines()
	l.attach(channelTo(t, d))
	s := controllink.NewSession(t.Context(), l)
	t.Cleanup(s.Close)
	return s, l
}

func folders(t *testing.T, s *controllink.Session) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), waitLimit(t))
	defer cancel()
	_, err := s.Call(ctx, "h-1", "folders/list", map[string]any{})
	return err
}

// #6563: a client such as loops, with its key in the lock, calls a machine that holds the
// lock through controllink.Session: a channel opened with lock/hello, each request sealed,
// each answer checked. Without the key, the machine refuses it as before.
func TestASessionWithTheLockCallsAMachineThatHoldsIt(t *testing.T) {
	d, phone, _, state := lockedMachine(t)

	// No lock: the machine refuses the call.
	plain, _ := sessionTo(t, d)
	if err := folders(t, plain); controllink.FailureCode(err) != wire.FailureNotInLock {
		t.Fatalf("an unsigned call was not refused: %v", err)
	}
	// A key the lock does not name: lock/hello is refused, and so is the call.
	stranger, _ := sessionTo(t, d)
	stranger.UseLock(&controllink.ClientLock{Signer: newLockKey(t), State: state})
	if err := folders(t, stranger); controllink.FailureCode(err) != wire.FailureNotInLock {
		t.Fatalf("a call with a key not in the lock was not refused: %v", err)
	}

	// The lock's client: taken, again and again, on one channel.
	s, cp := sessionTo(t, d)
	s.UseLock(&controllink.ClientLock{Signer: phone, State: state})
	for range 3 {
		if err := folders(t, s); err != nil {
			t.Fatalf("the client's call: %v", err)
		}
	}
	// What the machine says unasked comes opened, with its MAC checked.
	Broadcast(d, wire.NotifyWorkflowRemoved, wire.WorkflowRemovedNotification{})
	deadline := afterLimit(t)
	for got := false; !got; {
		select {
		case n := <-s.Notes:
			got = n.Method == wire.NotifyWorkflowRemoved.Name && n.Host == "h-1"
		case <-deadline:
			t.Fatal("the notification did not come")
		}
	}

	// The control plane puts an answer of its own in place of the machine's: not taken.
	cp.mu.Lock()
	cp.tamper = func(line string) string {
		var m struct {
			ID *int64 `json:"id"`
		}
		if json.Unmarshal([]byte(line), &m) != nil || m.ID == nil || !machinelock.Sealed(line) {
			return line
		}
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": *m.ID, "result": []any{}})
		return string(b)
	}
	cp.mu.Unlock()
	if err := folders(t, s); controllink.FailureCode(err) != wire.FailureNotInLock || !strings.Contains(err.Error(), "not signed") {
		t.Fatalf("an answer with no MAC was taken: %v", err)
	}
	cp.mu.Lock()
	cp.tamper = nil
	cp.mu.Unlock()

	// The machine's link goes and comes back: it forgot the channel, refuses the call on it,
	// and the session opens a new one and sends the call again.
	cp.attach(channelTo(t, d))
	if err := folders(t, s); err != nil {
		t.Fatalf("the call after the machine forgot the channel: %v", err)
	}
}
