package controlplane

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// slowHandler holds every lineage/sync until let go, and answers the rest at once.
type slowHandler struct {
	letGo chan struct{}
}

func (h *slowHandler) Handles(string) bool { return false }
func (h *slowHandler) Handle(string, json.RawMessage, Caller) (any, error) {
	return nil, nil
}
func (h *slowHandler) HostSaid(_, method string, _ json.RawMessage) (any, error) {
	if method == "lineage/sync" {
		<-h.letGo
	}
	return map[string]any{}, nil
}

// hostLink is a host's end of a real WebSocket uplink to r, and the lines it reads.
func hostLink(t *testing.T, r *Router) (*websocket.Conn, chan string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ws, err := websocket.Accept(w, req, nil)
		if err != nil {
			return
		}
		r.AttachHost("mac", newConn(ws, ""))
		<-req.Context().Done()
	}))
	t.Cleanup(srv.Close)
	ws, _, err := websocket.Dial(context.Background(), strings.Replace(srv.URL, "http", "ws", 1), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ws.CloseNow() })
	lines := make(chan string, 1024)
	go func() {
		for {
			_, b, err := ws.Read(context.Background())
			if err != nil {
				return
			}
			lines <- string(b)
		}
	}()
	return ws, lines
}

func callHost(t *testing.T, ws *websocket.Conn, id, method string) {
	t.Helper()
	line := `{"c":0,"m":{"jsonrpc":"2.0","id":"` + id + `","method":"` + method + `","params":{}}}`
	if err := ws.Write(context.Background(), websocket.MessageText, []byte(line)); err != nil {
		t.Fatal(err)
	}
}

// answer waits for the reply to id, and says whether it was an error.
func answer(t *testing.T, lines chan string, id string, within time.Duration) (failed bool) {
	t.Helper()
	deadline := time.After(within)
	for {
		select {
		case line := <-lines:
			if strings.Contains(line, `"id":"`+id+`"`) {
				return strings.Contains(line, `"error"`)
			}
		case <-deadline:
			t.Fatalf("no answer to %s in %v", id, within)
		}
	}
}

// #6223: a slow call does not stop the link's reads: its pings are answered, and the
// host's other calls too, while it runs. The log names it.
func TestASlowHostCallLeavesTheLinkRead(t *testing.T) {
	t.Parallel()
	h := &slowHandler{letGo: make(chan struct{})}
	var mu sync.Mutex
	var logged []string
	r := NewRouter(h, func(line string) {
		mu.Lock()
		defer mu.Unlock()
		logged = append(logged, line)
	})
	ws, lines := hostLink(t, r)

	callHost(t, ws, "slow", "lineage/sync")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := ws.Ping(ctx); err != nil {
		t.Fatalf("no pong while a call runs: %v", err)
	}
	callHost(t, ws, "quick", "bus/lease")
	if answer(t, lines, "quick", 5*time.Second) {
		t.Fatal("the quick call failed")
	}

	time.Sleep(slowHostCall + 200*time.Millisecond)
	if err := ws.Ping(ctx); err != nil {
		t.Fatalf("no pong after %v: %v", slowHostCall, err)
	}
	close(h.letGo)
	if answer(t, lines, "slow", 5*time.Second) {
		t.Fatal("the slow call failed")
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(strings.Join(logged, "\n"), "host mac: lineage/sync took") {
		t.Fatalf("the log does not name the slow call: %q", logged)
	}
}

// #6223: a lane with no room refuses a call at once, so the host asks again, and the
// link's reads still go on.
func TestAHostLaneWithNoRoomRefusesACall(t *testing.T) {
	t.Parallel()
	h := &slowHandler{letGo: make(chan struct{})}
	r := NewRouter(h, nil)
	ws, lines := hostLink(t, r)
	defer close(h.letGo)

	// One runs, hostCallQueue wait, and the next has no room.
	for i := 0; i <= hostCallQueue; i++ {
		callHost(t, ws, "held"+strings.Repeat("x", i), "lineage/sync")
	}
	callHost(t, ws, "over", "lineage/sync")
	if !answer(t, lines, "over", 5*time.Second) {
		t.Fatal("a call past the lane's room was not refused")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := ws.Ping(ctx); err != nil {
		t.Fatalf("no pong with a full lane: %v", err)
	}
}
