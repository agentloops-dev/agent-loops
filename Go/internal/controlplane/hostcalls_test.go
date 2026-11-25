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

	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/providers"
)

// slowHandler holds every lineage/sync until let go, and answers the rest at once. When
// began is set, each lineage/sync that starts says so on it.
type slowHandler struct {
	letGo chan struct{}
	began chan struct{}
}

func (h *slowHandler) Handles(string) bool { return false }
func (h *slowHandler) Handle(string, json.RawMessage, Caller) (any, error) {
	return nil, nil
}
func (h *slowHandler) HostSaid(_, method string, _ json.RawMessage) (any, error) {
	if method == "lineage/sync" {
		select {
		case h.began <- struct{}{}:
		default:
		}
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
	t.Cleanup(func() { _ = ws.CloseNow() })
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

// asksBack answers machine/machines by asking the same host, as a provider the host carries
// does (providers.Ask, providers/carry).
type asksBack struct{ r *Router }

func (h *asksBack) Handles(string) bool { return false }
func (h *asksBack) Handle(string, json.RawMessage, Caller) (any, error) {
	return nil, nil
}
func (h *asksBack) HostSaid(host, method string, _ json.RawMessage) (any, error) {
	if method != "machine/machines" {
		return map[string]any{}, nil
	}
	if reply := h.r.Ask(host, "providers/carry", map[string]any{}, 10*time.Second); reply == nil {
		return nil, fail(hostOffline, "it did not answer")
	}
	return map[string]any{}, nil
}

// #6223, the slow call it was: an agent's machine/machines on the Mac lists a provider the
// Mac carries, so its handler asks the Mac and waits. The Mac's answer comes on the same
// link, so a handler on the read loop waited on itself until the Mac dropped the link.
func TestAHostCallThatAsksTheSameHostIsAnswered(t *testing.T) {
	t.Parallel()
	h := &asksBack{}
	r := NewRouter(h, nil)
	h.r = r
	ws, lines := hostLink(t, r)

	callHost(t, ws, "list", "machine/machines")
	deadline := time.After(5 * time.Second)
	for asked := false; !asked; {
		select {
		case line := <-lines:
			var f struct {
				M struct {
					ID     string `json:"id"`
					Method string `json:"method"`
				} `json:"m"`
			}
			if json.Unmarshal([]byte(line), &f) == nil && f.M.Method == "providers/carry" {
				answered := `{"c":0,"m":{"jsonrpc":"2.0","id":"` + f.M.ID + `","result":{}}}`
				if err := ws.Write(context.Background(), websocket.MessageText, []byte(answered)); err != nil {
					t.Fatal(err)
				}
				asked = true
			}
		case <-deadline:
			t.Fatal("the control plane did not ask the host")
		}
	}
	if answer(t, lines, "list", 5*time.Second) {
		t.Fatal("machine/machines failed: the host's answer to the control plane's ask was not read")
	}
}

// #6223: a lane with no room refuses a call at once, so the host asks again, and the
// link's reads still go on.
func TestAHostLaneWithNoRoomRefusesACall(t *testing.T) {
	t.Parallel()
	h := &slowHandler{letGo: make(chan struct{}), began: make(chan struct{}, 1)}
	r := NewRouter(h, nil)
	ws, lines := hostLink(t, r)
	defer close(h.letGo)

	// One runs, hostCallQueue wait, and the next has no room. The lane's goroutine takes
	// the first call only when it is scheduled, so wait until that call runs (#6338):
	// before it does, the queue is full one call early, and that call is refused in place
	// of over, which then waits behind the held calls.
	callHost(t, ws, "held", "lineage/sync")
	select {
	case <-h.began:
	case <-time.After(5 * time.Second):
		t.Fatal("the first call did not start in 5s")
	}
	for i := 1; i <= hostCallQueue; i++ {
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

// partHandler holds each part of a send until let go, and answers the rest at once.
type partHandler struct{ began, letGo chan struct{} }

func (h *partHandler) Handles(string) bool { return false }
func (h *partHandler) Handle(string, json.RawMessage, Caller) (any, error) {
	return nil, nil
}
func (h *partHandler) HostSaid(_, method string, params json.RawMessage) (any, error) {
	if providers.IsPart(method, params) {
		h.began <- struct{}{}
		<-h.letGo
	}
	return map[string]any{}, nil
}

// #7120: several parts of a machine's send are under way at once, and its other calls of the
// same method do not wait for them.
func TestAHostsOtherCallsDoNotWaitForItsSend(t *testing.T) {
	t.Parallel()
	h := &partHandler{began: make(chan struct{}, 8), letGo: make(chan struct{})}
	r := NewRouter(h, nil)
	ws, lines := hostLink(t, r)
	for _, id := range []string{"p1", "p2", "p3", "p4"} {
		line := `{"c":0,"m":{"jsonrpc":"2.0","id":"` + id + `","method":"machine/machines","params":` +
			`{"agent":"You","method":"machines/sendFolder","params":{"host":"a","read":{"offset":0}}}}}`
		if err := ws.Write(context.Background(), websocket.MessageText, []byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	for range providers.SendPartsAtOnce {
		select {
		case <-h.began:
		case <-time.After(5 * time.Second):
			t.Fatal("the parts wait for each other")
		}
	}
	callHost(t, ws, "list", "machine/machines")
	if answer(t, lines, "list", 5*time.Second) {
		t.Fatal("the list failed")
	}
	close(h.letGo)
}

// The link's other lines go before a send's parts (#7120), and a part's channel keeps its order.
func TestAConnsOtherLinesGoBeforeBulk(t *testing.T) {
	t.Parallel()
	c := &Conn{out: make(chan string, 4), bulk: make(chan string, 4), done: make(chan struct{})}
	if !c.SendBulk("open") || !c.SendBulk("part") || !c.Send("a") || !c.Send("b") {
		t.Fatal("not queued")
	}
	for _, want := range []string{"a", "b", "open", "part"} {
		if got, ok := c.next(); !ok || got != want {
			t.Fatal(got, want)
		}
	}
}
