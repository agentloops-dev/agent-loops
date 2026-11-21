package agentsd

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// Send to ‹machine›… (#4613): folders/send answers at once and this host sends on its own,
// through machines/sendFolder, saying how it goes with send/changed; a window that connects
// mid-send finds it with folders/sends. A folder there with work of its own, or an offline
// machine, refuses at once, and nothing goes.

// sendsHeard are the send/changed notifications c hears, in order. c.call reads past
// notifications and drops them, and a send's job can say every part before the call's
// answer (#6001): call keeps the ones it reads past, and next gives them first.
type sendsHeard struct {
	c    *conn
	held []wire.SendNotification
}

// keep holds line when it is a send/changed, and is its id when it is an answer.
func (h *sendsHeard) keep(line string) (message, bool) {
	var m message
	if json.Unmarshal([]byte(line), &m) != nil {
		return m, false
	}
	if m.Method == wire.NotifySendChanged.Name {
		var n wire.SendNotification
		if json.Unmarshal(m.Params, &n) == nil {
			h.held = append(h.held, n)
		}
	}
	return m, m.Method == ""
}

// call is c.call that keeps the send/changed it reads past.
func (h *sendsHeard) call(t *testing.T, id int, method string, params any) json.RawMessage {
	t.Helper()
	p, _ := json.Marshal(params)
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": json.RawMessage(p)})
	h.c.in <- string(b)
	for deadline := afterLimit(t); ; {
		select {
		case line := <-h.c.out:
			m, answer := h.keep(line)
			if !answer || string(m.ID) != strings.TrimSpace(string(mustJSON(id))) {
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

// next is the next send/changed. It waits as the other helpers do (#5632): the first one
// after the answer comes only when sendPack's git steps are done, which take longer than
// 10 s on a runner under load (#6001). When it waits too long, waitFailed shows every
// goroutine's stack: a send still in git, or a line that went nowhere.
func (h *sendsHeard) next(t *testing.T, what string) wire.SendNotification {
	t.Helper()
	for deadline := afterLimit(t); len(h.held) == 0; {
		select {
		case line := <-h.c.out:
			h.keep(line)
		case <-deadline:
			waitFailed(t, "no %s", what)
		}
	}
	n := h.held[0]
	h.held = h.held[1:]
	return n
}

// sendPeer is the other machine of a send test: this host, which answers the
// send's steps with its own methods. It keeps each step and who asked. While
// offline, it refuses. It holds the receive until receiving closes.
type sendPeer struct {
	d          *Daemon
	mu         sync.Mutex
	steps, who []string
	offline    bool
	receiving  chan struct{}
}

// ask is the control plane's ask for the send's steps.
func (p *sendPeer) ask(ctx context.Context, method string, params any) (json.RawMessage, error) {
	// Only the send's steps: the host's other calls to the control plane go nowhere.
	call, ok := params.(wire.MachineWorkspaceCall)
	if !ok || call.Method != sendFolderMethod {
		return json.RawMessage(`{}`), nil
	}
	b, _ := json.Marshal(call.Params)
	p.mu.Lock()
	p.who = append(p.who, call.Agent)
	if p.offline {
		p.mu.Unlock()
		return nil, &controllink.AskError{Code: wire.FailureHostOffline, Message: "pc is offline."}
	}
	p.mu.Unlock()
	var s wire.MachineFolderSend
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	out, err := p.step(ctx, s)
	if err != nil {
		return nil, err
	}
	r, _ := json.Marshal(out)
	return r, nil
}

// addStep keeps the name of a step.
func (p *sendPeer) addStep(step string) {
	p.mu.Lock()
	p.steps = append(p.steps, step)
	p.mu.Unlock()
}

// step does one step of the send on this host.
func (p *sendPeer) step(ctx context.Context, s wire.MachineFolderSend) (wire.MachineFolderSent, error) {
	var out wire.MachineFolderSent
	switch {
	case s.State != nil:
		p.addStep("state")
		st, err := p.d.receiveState(*s.State)
		if err != nil {
			return out, err
		}
		out.State = &st
	case s.Upload != nil:
		p.addStep("upload")
		if err := p.d.receiveUpload(*s.Upload); err != nil {
			return out, err
		}
	case s.Receive != nil:
		// Held until the test has seen the send under way, or the host stops: the
		// control plane's ask ends with ctx, so Shutdown leaves no job running.
		select {
		case <-p.receiving:
		case <-ctx.Done():
			return out, ctx.Err()
		}
		p.addStep("receive")
		f, err := p.d.receive(*s.Receive)
		if err != nil {
			return out, &controllink.AskError{Code: wire.FailureFolderInTheWay, Message: err.Error()}
		}
		out.Folder = &f
	}
	return out, nil
}

func TestFoldersSendSendsOnItsOwn(t *testing.T) {
	t.Parallel()

	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	src := repo(t)
	sendGit(t, src, nil, "checkout", "-q", "-b", "fix-login")
	// More than one part of machines/sendFolder, so the send says how far it is.
	big := make([]byte, sendThereParts+sendThereParts/2)
	rand.Read(big)
	must(t, os.WriteFile(filepath.Join(src, "big.bin"), big, 0o644)) //nolint:gosec // G306: the mode this file always had; only its error is new
	sendGit(t, src, nil, "add", "-A")
	sendGit(t, src, nil, "commit", "-q", "-m", "big")
	must(t, os.WriteFile(filepath.Join(src, "notes.txt"), []byte("not committed\n"), 0o644)) //nolint:gosec // G306: the mode this file always had; only its error is new
	head := sendGit(t, src, nil, "rev-parse", "HEAD")
	// The other machine is this one, at another path: the steps go to its own methods.
	there := filepath.Join(canonical(t.TempDir()), "app")

	peer := &sendPeer{d: d, receiving: make(chan struct{})}
	d.SetControlAsk(peer.ask)
	send := map[string]any{"folder": store.PathURL(src), "machine": "pc-id", "machineName": "pc", "destination": there,
		"branches": []string{"fix-login"}, "checkout": "fix-login", "includeChanges": true}

	// Offline: refused at once, and nothing is under way.
	peer.mu.Lock()
	peer.offline = true
	peer.mu.Unlock()
	if e := c.callError(t, 1, "folders/send", send); e.Message != "Nothing was sent to pc: pc is offline." {
		t.Fatal(e.Message)
	}
	peer.mu.Lock()
	peer.offline = false
	peer.mu.Unlock()

	// It answers before the send is done.
	heard := &sendsHeard{c: c}
	var s wire.SendSummary
	mustUnmarshal(t, heard.call(t, 2, "folders/send", send), &s)
	if s.Machine != "pc-id" || s.MachineName != "pc" || s.Destination != there || s.Folder != store.PathURL(src) {
		t.Fatalf("%+v", s)
	}
	// The parts go, and each says how far the send is.
	var last *wire.SendNotification
	for last == nil || last.Sent < last.Total || last.Total == 0 {
		n := heard.next(t, "send/changed")
		last = &n
		if last.Finished {
			t.Fatalf("finished before the receive: %+v", last)
		}
	}
	if last.Send.ID != s.ID || last.Total < int64(len(big)) {
		t.Fatalf("%+v", last)
	}
	// A window that connects now finds it, and a second send to the same folder is refused.
	var under []wire.SendNotification
	mustUnmarshal(t, c.call(t, 3, "folders/sends", nil), &under)
	if len(under) != 1 || under[0].Send.ID != s.ID || under[0].Sent != last.Sent {
		t.Fatalf("%+v", under)
	}
	if e := c.callError(t, 4, "folders/send", send); e.Message != there+" on pc is already being sent." {
		t.Fatal(e.Message)
	}
	close(peer.receiving)
	for !last.Finished {
		n := heard.next(t, "finished send/changed")
		last = &n
	}
	if last.Problem != nil || last.Received == nil || last.Received.Folder.Folder != store.FolderURL(there) {
		t.Fatalf("%+v", last)
	}
	peer.mu.Lock()
	if strings.Join(peer.steps, ",") != "state,upload,upload,upload,receive" {
		t.Fatalf("steps %v", peer.steps)
	}
	for _, w := range peer.who {
		if w != "You" {
			t.Fatalf("asked as %q", w)
		}
	}
	peer.steps = nil
	peer.mu.Unlock()
	if got := sendGit(t, there, nil, "rev-parse", "HEAD"); got != head {
		t.Fatalf("there is at %s, not %s", got, head)
	}
	if b, _ := os.ReadFile(filepath.Join(there, "notes.txt")); string(b) != "not committed\n" {
		t.Fatalf("notes.txt there: %q", b)
	}
	if left, _ := os.ReadDir(d.outgoingFolder()); len(left) != 0 {
		t.Fatalf("left in Outgoing: %v", left)
	}
	mustUnmarshal(t, c.call(t, 5, "folders/sends", nil), &under)
	if len(under) != 0 {
		t.Fatalf("still under way: %+v", under)
	}

	// Work of its own there: refused at once, and nothing goes.
	must(t, os.WriteFile(filepath.Join(there, "mine.txt"), []byte("mine\n"), 0o644)) //nolint:gosec // G306: the mode this file always had; only its error is new
	if e := c.callError(t, 6, "folders/send", send); e.Message != "Nothing was sent to pc: "+there+" has changes of its own there." {
		t.Fatal(e.Message)
	}
	peer.mu.Lock()
	if strings.Join(peer.steps, ",") != "state" {
		t.Fatalf("steps %v", peer.steps)
	}
	peer.mu.Unlock()
}
