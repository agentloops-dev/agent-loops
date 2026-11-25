package agentsd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// conn is a client's end of a connection, in memory.
type conn struct {
	in   chan string
	out  chan string
	gone chan struct{} // closed once the daemon has shut down: drain stops

	heardMu sync.Mutex
	heard   []message // the notifications the last call read past: until takes them first
}

func newConn() *conn {
	return &conn{in: make(chan string, 64), out: make(chan string, 64), gone: make(chan struct{})}
}

// drain reads and drops what the daemon says on c, until the test's daemon has shut down
// (#6130): a bare `for range c.out` never ends, as nothing closes c.out.
func (c *conn) drain() {
	go func() {
		for {
			select {
			case <-c.out:
			case <-c.gone:
				return
			}
		}
	}()
}

// Write gives up on a line nobody reads within 5 s: a test that has its answer stops
// reading, and a watch's notification blocked on it held Shutdown for ever.
func (c *conn) Write(line string) error {
	select {
	case c.out <- line:
	case <-time.After(5 * time.Second):
	}
	return nil
}
func (c *conn) In() <-chan string { return c.in }
func (c *conn) Close()            {}
func (c *conn) Device() string    { return "" }

// call makes a call, and is its answer. A notification that comes before the answer may be
// the call's own effect, so until hears it first (#7227). One from before the call is dropped.
func (c *conn) call(t testing.TB, id int, method string, params any) json.RawMessage {
	t.Helper()
	c.forget()
	p, _ := json.Marshal(params)
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": json.RawMessage(p)})
	c.in <- string(b)
	deadline := afterLimit(t)
	for {
		select {
		case line := <-c.out:
			var m message
			mustUnmarshal(t, []byte(line), &m)
			if string(m.ID) != strings.TrimSpace(string(mustJSON(id))) {
				c.keep(m) // a notification, or another call's answer
				continue
			}
			if m.Error != nil {
				t.Fatalf("%s: %v", method, m.Error)
			}
			return m.Result
		case <-deadline:
			waitFailed(t, "%s: no answer", method)
		}
	}
}

// forget drops what the client has not taken yet.
func (c *conn) forget() {
	c.heardMu.Lock()
	defer c.heardMu.Unlock()
	c.heard = nil
	for {
		select {
		case <-c.out:
		default:
			return
		}
	}
}

func (c *conn) keep(m message) {
	c.heardMu.Lock()
	defer c.heardMu.Unlock()
	if m.Method != "" {
		c.heard = append(c.heard, m)
	}
}

// nextHeard takes the first notification that the last call read past.
func (c *conn) nextHeard() (message, bool) {
	c.heardMu.Lock()
	defer c.heardMu.Unlock()
	if len(c.heard) == 0 {
		return message{}, false
	}
	m := c.heard[0]
	c.heard = c.heard[1:]
	return m, true
}

// aCard takes the first agent/permission notification, and keeps its request in into.
func aCard(into *wire.PermissionRequest) func(string, json.RawMessage) bool {
	return func(method string, p json.RawMessage) bool {
		var n wire.PermissionNotification
		if method == "agent/permission" && json.Unmarshal(p, &n) == nil && n.Request != nil {
			*into = *n.Request
			return true
		}
		return false
	}
}

// callError sends a call and is the error it answers with, or fails when it answers.
// It reads past notifications (lines with no id), as call does: a watch's wake/changed
// can come before the answer (#5751). Read an answer with call or callError, never
// with a bare <-c.out.
func (c *conn) callError(t *testing.T, id int, method string, params any) *Error {
	t.Helper()
	p, _ := json.Marshal(params)
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": json.RawMessage(p)})
	c.in <- string(b)
	deadline := afterLimit(t)
	for {
		select {
		case line := <-c.out:
			var m message
			mustUnmarshal(t, []byte(line), &m)
			if string(m.ID) != strings.TrimSpace(string(mustJSON(id))) {
				continue // a notification
			}
			if m.Error == nil {
				t.Fatalf("%s answered %s", method, m.Result)
			}
			return m.Error
		case <-deadline:
			waitFailed(t, "%s: no answer", method)
		}
	}
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

func start(t testing.TB, root string) (*Daemon, *conn) {
	t.Helper()
	return startWith(t, context.Background(), root)
}

// startWith is start on a context of the test's, as main's that a signal ends.
func startWith(t testing.TB, ctx context.Context, root string) (*Daemon, *conn) {
	t.Helper()
	hooks := testHooksOf(t)
	d, err := newWith(ctx, root, hooks.options())
	if err != nil {
		t.Fatal(err)
	}
	// Last of these cleanups, before the root goes: what a test sets off (a turn's
	// ending, an assessment's score) writes into the root after the test has its answer.
	// A test that shuts the daemon down itself shuts it down again here, which does nothing.
	// Then no goroutine the daemon started may still run (#5653): one that does would write
	// into the root as it goes, or read a setting the next test changes. It fails here, by
	// where it started, not at TempDir's RemoveAll.
	hooks.started()
	hooks.add(d)
	c := newConn()
	t.Cleanup(func() {
		d.Shutdown()
		close(c.gone)
		hooks.remove(d)
		if live := d.liveJobs(); len(live) > 0 {
			t.Errorf("goroutines the daemon started still run after Shutdown:\n\t%s", strings.Join(live, "\n\t"))
		}
		hooks.stopped()
		resetToolsHomes()
	})
	// As main does at each start (#4177): a fixture's secrets.env goes into sealed.json.
	d.MoveSecretsFile()
	go d.Accept(c)
	t.Cleanup(func() { close(c.in) })
	clientAdded(t, d, c)
	t.Cleanup(d.stopFolderWatches)
	// A stopped runtime's turn still records how it ended: wait for that before the
	// root's TempDir goes, or the removal races a write into it.
	t.Cleanup(func() {
		for deadline := time.Now().Add(2 * time.Second); d.anyLive() && time.Now().Before(deadline); {
			time.Sleep(10 * time.Millisecond)
		}
	})
	return d, c
}

// clientAdded waits until the daemon has c as a client. Accept runs on its own goroutine,
// so a test that goes on at once can find no window: a notification goes to nobody (#5878),
// and show_file says no window is open (#5940).
func clientAdded(t testing.TB, d *Daemon, c *conn) {
	t.Helper()
	limit := waitLimit(t)
	for deadline := time.Now().Add(limit); ; time.Sleep(time.Millisecond) {
		d.mu.Lock()
		added := false
		for _, each := range d.clients {
			added = added || each.conn == c
		}
		d.mu.Unlock()
		if added {
			return
		}
		if time.Now().After(deadline) {
			acceptStacks(t)
			waitFailed(t, "the daemon never added the test's window client in %v", limit)
		}
	}
}

// anyLive is whether an agent's run loop is still running or in a turn.
func (d *Daemon) anyLive() bool {
	d.mu.Lock()
	ls := make([]*live, 0, len(d.lives))
	for _, l := range d.lives {
		ls = append(ls, l)
	}
	d.mu.Unlock()
	for _, l := range ls {
		l.mu.Lock()
		busy := l.running || l.inTurn
		l.mu.Unlock()
		if busy {
			return true
		}
	}
	return false
}

func TestPingAndAnUnknownMethod(t *testing.T) {
	t.Parallel()
	_, c := start(t, t.TempDir())
	if r := c.call(t, 1, "daemon/ping", nil); string(r) != `{"ok":true}` {
		t.Fatalf("ping %s", r)
	}
	if e := c.callError(t, 2, "nothing/here", nil); e.Code != -32601 {
		t.Fatalf("%+v", e)
	}
}

func TestAPanickingHandlerAnswersAnError(t *testing.T) {
	t.Parallel()
	d, c := start(t, t.TempDir())
	d.handlers["boom"] = func(context.Context, *Client, json.RawMessage) (any, error) {
		var m map[string]int
		m["x"] = 1
		return nil, nil
	}
	if e := c.callError(t, 1, "boom", nil); e.Code != -32603 {
		t.Fatalf("%+v", e)
	}
	if r := c.call(t, 2, "daemon/ping", nil); string(r) != `{"ok":true}` {
		t.Fatal("the host stopped serving")
	}
}

// A root the Swift host wrote: its agent is listed with its folder, and its transcript
// pages as Swift pages it.
func TestASwiftRootIsServed(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	fix := filepath.Join("..", "..", "..", "Packages", "AgentsKit", "Tests", "AgentsKitTests", "Fixtures", "legacy-agent")
	b, _ := os.ReadFile(filepath.Join(fix, "agent.json"))
	var head struct{ ID string }
	mustUnmarshal(t, b, &head)
	// The fixture's folder is a real one on the Mac it was made on: here, a folder of the
	// test's own, so the host never reads (or runs the workflows of) a real folder.
	b = []byte(strings.ReplaceAll(string(b), `"file:///Users/alexcollins/Agents/"`, `"`+string(store.FolderURL(t.TempDir()))+`/"`))
	if strings.Contains(string(b), "file:///Users/alexcollins/Agents/") {
		t.Fatal("the fixture still names a real folder")
	}
	dir := filepath.Join(root, "agents", strings.ToUpper(head.ID))
	must(t, os.MkdirAll(dir, 0o755))                                  //nolint:gosec // G301: the mode this folder always had; only its error is new
	must(t, os.WriteFile(filepath.Join(dir, "agent.json"), b, 0o644)) //nolint:gosec // G306: the mode this file always had; only its error is new
	tb, _ := os.ReadFile(filepath.Join(fix, "transcript.jsonl"))
	must(t, os.WriteFile(filepath.Join(dir, "transcript.jsonl"), tb, 0o644)) //nolint:gosec // G306: the mode this file always had; only its error is new
	// The fixture's agent was left running: its record also says it stopped with the
	// daemon, a note and its ending (DaemonCore+Recovery).
	lines := strings.Count(string(tb), "\n") + 2

	d, c := start(t, root)
	// Stopped before the root goes: recovery may still be writing the agent's record.
	defer d.Shutdown()
	var agents []wire.Agent
	mustUnmarshal(t, c.call(t, 1, "agents/list", map[string]any{"includeArchived": true}), &agents)
	if len(agents) != 1 || string(agents[0].ID) != strings.ToUpper(head.ID) {
		t.Fatalf("agents %v", agents)
	}
	var folders []wire.FolderSummary
	mustUnmarshal(t, c.call(t, 2, "folders/list", map[string]any{"includeArchived": true}), &folders)
	if len(folders) != 1 || folders[0].Name == "" {
		t.Fatalf("folders %+v", folders)
	}
	var page wire.TranscriptPage
	mustUnmarshal(t, c.call(t, 3, "agents/transcript", map[string]any{"agentID": head.ID, "limit": 10}), &page)
	if page.Total != int64(lines) || page.FirstIndex != int64(lines-10) || len(page.Entries) != 10 {
		t.Fatalf("page total %d first %d entries %d (lines %d)", page.Total, page.FirstIndex, len(page.Entries), lines)
	}
	mustUnmarshal(t, c.call(t, 4, "agents/transcript", map[string]any{"agentID": head.ID, "limit": 10, "before": 5}), &page)
	if page.FirstIndex != 0 || len(page.Entries) != 5 {
		t.Fatalf("before 5: first %d entries %d", page.FirstIndex, len(page.Entries))
	}
}

func TestAddAFolder(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	work := filepath.Join(t.TempDir(), "hello")
	must(t, os.Mkdir(work, 0o755)) //nolint:gosec // G301: the mode this folder always had; only its error is new
	d, c := start(t, root)
	var s wire.FolderSummary
	if err := json.Unmarshal(c.call(t, 1, "folders/add", map[string]any{"folder": "file://" + work}), &s); err != nil {
		t.Fatal(err)
	}
	if s.Name != "hello" || !s.Exists {
		t.Fatalf("%+v", s)
	}
	if ps := d.Store.Folders(); len(ps) != 1 || store.FolderPath(ps[0].Folder) != mustResolve(work) {
		t.Fatalf("records %+v", ps)
	}
	c.in <- `{"jsonrpc":"2.0","id":2,"method":"folders/add","params":{"folder":"file:///no/such/folder"}}`
	for line := range c.out {
		if strings.Contains(line, `"id":2`) {
			if !strings.Contains(line, "-32004") {
				t.Fatalf("%s", line)
			}
			break
		}
	}
}

func mustResolve(p string) string { r, _ := store.Resolve(p); return r }
