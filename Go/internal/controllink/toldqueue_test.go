package controllink

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// logged captures the log for the test.
type logged struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logged) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logged) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func captureLog(t *testing.T) *logged {
	t.Helper()
	l := &logged{}
	was := log.Writer()
	log.SetOutput(l)
	t.Cleanup(func() { log.SetOutput(was) })
	return l
}

func notification(method string, n int) string {
	return `{"c":0,"m":{"jsonrpc":"2.0","method":"` + method + `","params":{"n":` + strconv.Itoa(n) + `}}}`
}

// A Told handler that waits holds neither the link's reads nor its pongs, both ways, and
// the log names it; what is told after it waits its turn, in order (#6260). Before, the
// read loop ran Told inline: the host's pings got no pong while a handler waited, and the
// Mac dropped its link to the control plane 1 to 5 times an hour.
func TestASlowToldHandlerLeavesTheLinkRead(t *testing.T) {
	logs := captureLog(t)
	f := newFakeControl(t)
	f.say = make(chan string, 16)
	release := make(chan struct{})
	var once sync.Once
	free := func() { once.Do(func() { close(release) }) }
	t.Cleanup(free)
	heard := make(chan string, 16)
	read := make(chan string, 16)
	u := enrolled(t, f, func(c Conn) {
		for line := range c.In() {
			read <- line
		}
	})
	u.slowAfter = 50 * time.Millisecond
	// The host pings every 300 ms and drops a link whose pong takes as long.
	u.pingEvery = 300 * time.Millisecond
	u.Told = func(method string, params json.RawMessage) {
		var p struct{ N int }
		_ = json.Unmarshal(params, &p)
		heard <- method + " " + strconv.Itoa(p.N)
		if method == "machine/slow" {
			<-release
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go u.Run(ctx)
	hello(t, f, 10*time.Second, "no machine/hello")
	within := func(what string, ch chan string, want string) {
		t.Helper()
		select {
		case got := <-ch:
			if !strings.Contains(got, want) {
				t.Fatalf("%s %q, want %q", what, got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s nothing, want %q", what, want)
		}
	}
	within("read", read, "daemon/ping")

	f.say <- notification("machine/slow", 1)
	f.say <- notification("machine/clients", 2)
	within("told", heard, "machine/slow 1")

	// While it waits, for well over the host's ping: the host reads a channel's lines and
	// answers the control plane's pings, and its own pings get their pongs.
	ws := f.ws.Load()
	for i := 0; i < 8; i++ {
		f.say <- `{"c":1,"m":{"jsonrpc":"2.0","id":` + strconv.Itoa(100+i) + `,"method":"daemon/ping"}}`
		within("read", read, `"id":`+strconv.Itoa(100+i))
		pctx, done := context.WithTimeout(ctx, 2*time.Second)
		err := ws.Ping(pctx)
		done()
		if err != nil {
			t.Fatalf("ping %d from the control plane got no pong while a handler waited: %v", i, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	select {
	case <-f.hellos:
		t.Fatalf("the host dropped its link while a handler waited:\n%s", logs)
	default:
	}
	if s := logs.String(); strings.Contains(s, "did not answer a ping") {
		t.Fatalf("the host's ping went unanswered:\n%s", s)
	}
	select {
	case got := <-heard:
		t.Fatalf("told %q before machine/slow ended: notifications go in order", got)
	default:
	}

	free()
	within("told", heard, "machine/clients 2")
	for deadline := time.Now().Add(5 * time.Second); !strings.Contains(logs.String(), "uplink: machine/slow took"); {
		if time.Now().After(deadline) {
			t.Fatalf("the slow handler was not logged by name:\n%s", logs)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A full queue drops what comes next, with one line in the log, and never holds the read
// loop (#6260).
func TestAFullToldQueueDropsAndDoesNotWait(t *testing.T) {
	logs := captureLog(t)
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	var mu sync.Mutex
	count := 0
	u := &Uplink{Told: func(string, json.RawMessage) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		mu.Lock()
		count++
		mu.Unlock()
	}}
	line := `{"jsonrpc":"2.0","method":"machine/slow","params":{}}`
	u.message(0, line)
	<-entered
	start := time.Now()
	for i := 0; i < toldQueue+10; i++ {
		u.message(0, line)
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("queueing took %s", took)
	}
	if n := strings.Count(logs.String(), "are dropped until"); n != 1 {
		t.Fatalf("%d lines say the queue is full, want 1:\n%s", n, logs)
	}
	close(release)
	for deadline := time.Now().Add(5 * time.Second); ; {
		mu.Lock()
		n := count
		mu.Unlock()
		// The one being handled when the queue filled, and the toldQueue that waited.
		if n == toldQueue+1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d told, want %d", n, toldQueue+1)
		}
		time.Sleep(10 * time.Millisecond)
	}
	for deadline := time.Now().Add(5 * time.Second); !strings.Contains(logs.String(), "10 were dropped"); {
		if time.Now().After(deadline) {
			t.Fatalf("the catch-up was not logged:\n%s", logs)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
